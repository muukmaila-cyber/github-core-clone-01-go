package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"
)

// ---------------------------------------------------------------- pkt-lines

const hexDigits = "0123456789abcdef"

// pktLine encodes one payload (which must end with \n when it is a text line).
// Manual hex: fmt.Sprintf("%04x") per line was a measurable cost on the
// push/clone paths, which are pkt-line heavy.
func pktLine(payload []byte) []byte {
	n := len(payload) + 4
	out := make([]byte, 0, n)
	out = append(out, hexDigits[(n>>12)&0xf], hexDigits[(n>>8)&0xf], hexDigits[(n>>4)&0xf], hexDigits[n&0xf])
	out = append(out, payload...)
	return out
}

func pktString(s string) []byte { return pktLine([]byte(s)) }

var pktFlush = []byte("0000")
var pktDelim = []byte("0001")

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a'+10), true
	case c >= 'A' && c <= 'F':
		return int(c-'A'+10), true
	}
	return 0, false
}

// parseHex4 parses a 4-hex-digit pkt-line length without fmt.Sscanf.
func parseHex4(data []byte) (int, bool) {
	n := 0
	for i := 0; i < 4; i++ {
		v, ok := hexVal(data[i])
		if !ok {
			return 0, false
		}
		n = n*16 + v
	}
	return n, true
}

type pkt struct {
	kind    string // "data", "flush", "delim"
	payload []byte
}

// parsePktLines splits a byte stream into pkt-lines. Trailing garbage is ignored.
func parsePktLines(data []byte) []pkt {
	var out []pkt
	for len(data) >= 4 {
		n, ok := parseHex4(data[:4])
		if !ok {
			return out
		}
		if n == 0 {
			out = append(out, pkt{kind: "flush"})
			data = data[4:]
			continue
		}
		if n == 1 {
			out = append(out, pkt{kind: "delim"})
			data = data[4:]
			continue
		}
		if len(data) < n {
			return out
		}
		out = append(out, pkt{kind: "data", payload: data[4:n]})
		data = data[n:]
	}
	return out
}

// splitPktBody splits a request body into the pkt-line section and the raw tail
// (a packfile) that follows the first flush.
func splitPktBody(data []byte) (lines []pkt, tail []byte) {
	for len(data) >= 4 {
		n, ok := parseHex4(data[:4])
		if !ok {
			return lines, data
		}
		if n == 0 {
			return lines, data[4:]
		}
		if n == 1 {
			lines = append(lines, pkt{kind: "delim"})
			data = data[4:]
			continue
		}
		if len(data) < n {
			return lines, data
		}
		lines = append(lines, pkt{kind: "data", payload: data[4:n]})
		data = data[n:]
	}
	return lines, data
}

// ---------------------------------------------------------------- zlib

// Pooled zlib writers: buildPack compresses one object per writer, and the
// push/clone paths build hundreds of packs per trial.
var zlibWriterPool = sync.Pool{New: func() any {
	w, _ := zlib.NewWriterLevel(&bytes.Buffer{}, zlib.BestSpeed)
	return w
}}

func zlibCompress(data []byte) []byte {
	var buf bytes.Buffer
	w := zlibWriterPool.Get().(*zlib.Writer)
	w.Reset(&buf)
	w.Write(data)
	w.Close()
	zlibWriterPool.Put(w)
	return buf.Bytes()
}

// ---------------------------------------------------------------- pack build

var objTypeCode = map[string]int{
	"commit": 1,
	"tree":   2,
	"blob":   3,
	"tag":    4,
}

// buildPack serializes objects (oid-sorted, plain, no deltas) into a packfile.
func buildPack(objects map[string]*GitObject, oids []string) []byte {
	sorted := make([]string, len(oids))
	copy(sorted, oids)
	sort.Strings(sorted)
	var buf bytes.Buffer
	buf.WriteString("PACK")
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], 2)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(sorted)))
	buf.Write(hdr[:])
	for _, oid := range sorted {
		obj := objects[oid]
		if obj == nil {
			continue
		}
		code := objTypeCode[obj.Type]
		size := len(obj.Data)
		// varint header
		b := byte((code << 4) | (size & 0x0f))
		size >>= 4
		for size != 0 {
			buf.WriteByte(b | 0x80)
			b = byte(size & 0x7f)
			size >>= 7
		}
		buf.WriteByte(b)
		buf.Write(zlibCompress(obj.Data))
	}
	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	return buf.Bytes()
}

// ---------------------------------------------------------------- pack parse

type parsedObject struct {
	Type string
	Data []byte
}

// parsePack parses a packfile, resolving OFS and REF deltas against already
// parsed objects and the repo's existing store. Returns oid -> object.
func parsePack(r *Repo, pack []byte) (map[string]*parsedObject, error) {
	if len(pack) < 12 || string(pack[:4]) != "PACK" {
		return nil, fmt.Errorf("bad pack header")
	}
	version := binary.BigEndian.Uint32(pack[4:8])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("unsupported pack version %d", version)
	}
	count := int(binary.BigEndian.Uint32(pack[8:12]))
	br := bytes.NewReader(pack[12:])
	out := make(map[string]*parsedObject, count)
	// start offsets for OFS delta resolution
	starts := make(map[int64]string)
	for i := 0; i < count; i++ {
		objStart := int64(len(pack)) - int64(br.Len())
		// object header varint
		b, err := br.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("truncated object header")
		}
		objType := int((b >> 4) & 0x07)
		size := int64(b & 0x0f)
		shift := uint(4)
		for b&0x80 != 0 {
			b, err = br.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("truncated object header")
			}
			size |= int64(b&0x7f) << shift
			shift += 7
		}
		var baseOID string
		var baseData []byte
		var baseType string
		switch objType {
		case 6: // OFS_DELTA
			b, err := br.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("truncated ofs-delta offset")
			}
			off := int64(b & 0x7f)
			for b&0x80 != 0 {
				b, err = br.ReadByte()
				if err != nil {
					return nil, fmt.Errorf("truncated ofs-delta offset")
				}
				off = ((off + 1) << 7) | int64(b&0x7f)
			}
			baseStart := objStart - off
			baseOID, ok := starts[baseStart]
			if !ok {
				return nil, fmt.Errorf("ofs-delta base not parsed")
			}
			baseObj, ok := out[baseOID]
			if !ok {
				return nil, fmt.Errorf("ofs-delta base missing")
			}
			baseData, baseType = baseObj.Data, baseObj.Type
		case 7: // REF_DELTA
			raw := make([]byte, 20)
			if _, err := io.ReadFull(br, raw); err != nil {
				return nil, fmt.Errorf("truncated ref-delta base")
			}
			baseOID = hex.EncodeToString(raw)
			if baseObj, ok := out[baseOID]; ok {
				baseData, baseType = baseObj.Data, baseObj.Type
			} else if storeObj, ok := r.Objects[baseOID]; ok {
				baseData, baseType = storeObj.Data, storeObj.Type
			} else {
				return nil, fmt.Errorf("ref-delta base %s not found", baseOID)
			}
		}
		// zlib stream: bytes.Reader implements io.ByteReader, so flate consumes
		// exactly the stream bytes and br.Len() tracks the position.
		zr, err := zlib.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("bad zlib stream: %v", err)
		}
		raw := make([]byte, size)
		if _, err := io.ReadFull(zr, raw); err != nil {
			zr.Close()
			return nil, fmt.Errorf("short zlib stream: %v", err)
		}
		zr.Close()
		var finalType string
		var finalData []byte
		switch objType {
		case 1:
			finalType, finalData = "commit", raw
		case 2:
			finalType, finalData = "tree", raw
		case 3:
			finalType, finalData = "blob", raw
		case 4:
			finalType, finalData = "tag", raw
		case 6, 7:
			delta, err := applyDelta(baseData, raw)
			if err != nil {
				return nil, fmt.Errorf("delta apply: %v", err)
			}
			finalType, finalData = baseType, delta
		default:
			return nil, fmt.Errorf("unknown object type %d", objType)
		}
		oid := gitOID(finalType, finalData)
		out[oid] = &parsedObject{Type: finalType, Data: finalData}
		starts[objStart] = oid
	}
	return out, nil
}

func readVarintDelta(data []byte, pos *int) (int64, error) {
	var v int64
	shift := uint(0)
	for {
		if *pos >= len(data) {
			return 0, fmt.Errorf("truncated delta varint")
		}
		b := data[*pos]
		*pos++
		v |= int64(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
	}
	return v, nil
}

// applyDelta applies a git delta to base data.
func applyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	srcSize, err := readVarintDelta(delta, &pos)
	if err != nil {
		return nil, err
	}
	if srcSize != int64(len(base)) {
		return nil, fmt.Errorf("delta base size mismatch")
	}
	dstSize, err := readVarintDelta(delta, &pos)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, dstSize)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		if op&0x80 != 0 {
			// copy from base
			var off, size int64
			for i := 0; i < 4; i++ {
				if op&(1<<uint(i)) != 0 {
					if pos >= len(delta) {
						return nil, fmt.Errorf("truncated delta copy")
					}
					off |= int64(delta[pos]) << uint(8*i)
					pos++
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<uint(i)) != 0 {
					if pos >= len(delta) {
						return nil, fmt.Errorf("truncated delta copy")
					}
					size |= int64(delta[pos]) << uint(8*i)
					pos++
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if off+size > int64(len(base)) {
				return nil, fmt.Errorf("delta copy out of range")
			}
			out = append(out, base[off:off+size]...)
		} else if op != 0 {
			n := int(op)
			if pos+n > len(delta) {
				return nil, fmt.Errorf("truncated delta insert")
			}
			out = append(out, delta[pos:pos+n]...)
			pos += n
		} else {
			return nil, fmt.Errorf("delta opcode 0")
		}
	}
	if int64(len(out)) != dstSize {
		return nil, fmt.Errorf("delta result size mismatch")
	}
	return out, nil
}
