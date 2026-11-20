package main

import (
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
)

const zeroOID = "0000000000000000000000000000000000000000"

// ---------------------------------------------------------------- ref listing

type refEntry struct {
	oid     string
	name    string
	peeled  string // target oid when the ref is an annotated tag
	symref  string // symref target for HEAD
	unborn  bool
}

func (r *Repo) refEntries() []refEntry {
	var out []refEntry
	defBranch := "refs/heads/" + r.DefaultBranch
	tip, hasTip := r.Refs[defBranch]
	if hasTip {
		out = append(out, refEntry{oid: tip, name: "HEAD", symref: defBranch})
	} else {
		out = append(out, refEntry{oid: zeroOID, name: "HEAD", symref: defBranch, unborn: true})
	}
	var heads []string
	var tags []string
	for ref := range r.Refs {
		if strings.HasPrefix(ref, "refs/heads/") {
			heads = append(heads, ref)
		} else if strings.HasPrefix(ref, "refs/tags/") {
			tags = append(tags, ref)
		}
	}
	sort.Strings(heads)
	sort.Strings(tags)
	for _, ref := range heads {
		out = append(out, refEntry{oid: r.Refs[ref], name: ref})
	}
	for _, ref := range tags {
		e := refEntry{oid: r.Refs[ref], name: ref}
		if obj, ok := r.Objects[e.oid]; ok && obj.Type == "tag" {
			// annotated tag: peeled target is the object the tag points at
			target := parseTagTarget(obj.Data)
			if target != "" {
				e.peeled = target
			}
		}
		out = append(out, e)
	}
	return out
}

func parseTagTarget(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "object ") {
			return strings.TrimSpace(line[7:])
		}
		if line == "" {
			break
		}
	}
	return ""
}

func formatRefLineV2(e refEntry) string {
	if e.unborn {
		return "unborn HEAD symref-target:" + e.symref + "\n"
	}
	var b strings.Builder
	b.WriteString(e.oid)
	b.WriteString(" ")
	b.WriteString(e.name)
	if e.symref != "" {
		b.WriteString(" symref-target:")
		b.WriteString(e.symref)
	}
	if e.peeled != "" {
		b.WriteString(" peeled:")
		b.WriteString(e.peeled)
	}
	b.WriteString("\n")
	return b.String()
}

// ---------------------------------------------------------------- advertisement

func (r *Repo) advertisementV0(service string) []byte {
	var buf []byte
	if service == "git-upload-pack" {
		buf = append(buf, pktString("# service=git-upload-pack\n")...)
	} else {
		buf = append(buf, pktString("# service=git-receive-pack\n")...)
	}
	buf = append(buf, pktFlush...)
	entries := r.refEntries()
	if service == "git-receive-pack" {
		caps := "report-status side-band-64k delete-refs"
		if len(r.Refs) == 0 {
			buf = append(buf, pktString(zeroOID+" capabilities^{}\x00"+caps+"\n")...)
			buf = append(buf, pktFlush...)
			return buf
		}
		for i, e := range entries {
			line := e.oid + " " + e.name
			if e.name == "HEAD" {
				line += "\x00" + caps
			}
			if e.peeled != "" {
				// peeled lines follow the tag line in v0
				buf = append(buf, pktString(line+"\n")...)
				buf = append(buf, pktString(e.peeled+" "+e.name+"^{}\n")...)
				continue
			}
			buf = append(buf, pktString(line+"\n")...)
			_ = i
		}
		buf = append(buf, pktFlush...)
		return buf
	}
	// upload-pack
	caps := "side-band-64k shallow thin-pack ofs-delta symref=HEAD:refs/heads/" + r.DefaultBranch
	for i, e := range entries {
		line := e.oid + " " + e.name
		if i == 0 {
			line += "\x00" + caps
		}
		buf = append(buf, pktString(line+"\n")...)
		if e.peeled != "" {
			buf = append(buf, pktString(e.peeled+" "+e.name+"^{}\n")...)
		}
	}
	buf = append(buf, pktFlush...)
	return buf
}

func (r *Repo) advertisementV2() []byte {
	// Protocol v2's initial advertisement is capabilities only, then flush.
	// Refs are NOT listed here: they come from the ls-refs command. (Listing
	// them here corrupts the next exchange: the client reads the leftover
	// ref lines where it expects the ls-refs response-end packet.)
	var buf []byte
	buf = append(buf, pktString("version 2\n")...)
	buf = append(buf, pktString("fetch=shallow\n")...)
	buf = append(buf, pktString("ls-refs=unborn\n")...)
	buf = append(buf, pktString("object-format=sha1\n")...)
	buf = append(buf, pktFlush...)
	return buf
}

// ---------------------------------------------------------------- ls-refs (v2)

func (r *Repo) lsRefsV2(lines []pkt) []byte {
	peel := true
	symrefs := true
	unborn := false
	var prefixes []string
	inArgs := false
	for _, p := range lines {
		if p.kind != "data" {
			if p.kind == "delim" {
				inArgs = true
			}
			continue
		}
		text := string(p.payload)
		if !inArgs {
			continue
		}
		switch {
		case text == "peel\n":
			peel = true
		case text == "symrefs\n":
			symrefs = true
		case text == "unborn\n":
			unborn = true
		case strings.HasPrefix(text, "ref-prefix "):
			prefixes = append(prefixes, strings.TrimSpace(strings.TrimPrefix(text, "ref-prefix ")))
		}
	}
	var buf []byte
	for _, e := range r.refEntries() {
		if e.name == "HEAD" && !symrefs {
			continue
		}
		if e.unborn && !unborn {
			continue
		}
		if len(prefixes) > 0 {
			matched := false
			for _, pre := range prefixes {
				if strings.HasPrefix(e.name, pre) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		line := e
		if !peel {
			line.peeled = ""
		}
		if !symrefs {
			line.symref = ""
		}
		buf = append(buf, pktString(formatRefLineV2(line))...)
	}
	buf = append(buf, pktFlush...)
	return buf
}

// ---------------------------------------------------------------- fetch selection

type fetchRequest struct {
	wants   []string
	haves   []string
	shallow []string // client's shallow boundary commits
	deepen  int      // -1 = no limit
}

func parseFetchV2(lines []pkt) fetchRequest {
	req := fetchRequest{deepen: -1}
	for _, p := range lines {
		if p.kind != "data" {
			continue
		}
		text := strings.TrimRight(string(p.payload), "\n")
		switch {
		case strings.HasPrefix(text, "want "):
			req.wants = append(req.wants, strings.TrimSpace(text[5:]))
		case strings.HasPrefix(text, "have "):
			req.haves = append(req.haves, strings.TrimSpace(text[5:]))
		case strings.HasPrefix(text, "shallow "):
			req.shallow = append(req.shallow, strings.TrimSpace(text[8:]))
		case strings.HasPrefix(text, "deepen "):
			req.deepen = atoi(strings.TrimSpace(text[7:]))
		}
	}
	return req
}

func parseFetchV0(body []byte) (fetchRequest, []byte) {
	// two sections: wants (+shallow/deepen) flush, then haves+done flush
	req := fetchRequest{deepen: -1}
	lines, tail := splitPktBody(body)
	for _, p := range lines {
		if p.kind != "data" {
			continue
		}
		text := strings.TrimRight(string(p.payload), "\n")
		if nul := strings.IndexByte(text, 0); nul >= 0 {
			text = text[:nul] // capabilities after NUL on the first want line
		}
		switch {
		case strings.HasPrefix(text, "want "):
			oid := strings.TrimSpace(text[5:])
			if len(oid) > 40 {
				oid = oid[:40]
			}
			req.wants = append(req.wants, oid)
		case strings.HasPrefix(text, "shallow "):
			req.shallow = append(req.shallow, strings.TrimSpace(text[8:]))
		case strings.HasPrefix(text, "deepen "):
			req.deepen = atoi(strings.TrimSpace(text[7:]))
		}
	}
	// second section: haves
	lines2, _ := splitPktBody(tail)
	for _, p := range lines2 {
		if p.kind != "data" {
			continue
		}
		text := strings.TrimRight(string(p.payload), "\n")
		if strings.HasPrefix(text, "have ") {
			req.haves = append(req.haves, strings.TrimSpace(text[5:]))
		}
	}
	return req, tail
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// selectFetch computes the objects to send, plus shallow/unshallow lines.
func (r *Repo) selectFetch(req fetchRequest) (oids []string, shallowOut, unshallowOut []string) {
	shallowSet := make(map[string]bool, len(req.shallow))
	for _, o := range req.shallow {
		shallowSet[o] = true
	}
	// has-set: full closure of haves, stopping at shallow-boundary commits.
	hasSet := make(map[string]bool)
	{
		queue := append([]string{}, req.haves...)
		seen := make(map[string]bool)
		for len(queue) > 0 {
			oid := queue[0]
			queue = queue[1:]
			if seen[oid] {
				continue
			}
			seen[oid] = true
			obj, ok := r.Objects[oid]
			if !ok {
				continue
			}
			hasSet[oid] = true
			switch obj.Type {
			case "commit":
				if shallowSet[oid] {
					// boundary: include tree/blobs below, not ancestors
					addTreeObjects(r, parseCommitTree(obj.Data), hasSet)
					continue
				}
				addTreeObjects(r, parseCommitTree(obj.Data), hasSet)
				queue = append(queue, parseCommitParents(obj.Data)...)
			case "tree":
				addTreeObjects(r, oid, hasSet)
			case "tag":
				if target := parseTagTarget(obj.Data); target != "" {
					queue = append(queue, target)
				}
			}
		}
	}
	// depth-limited closure from wants
	send := make(map[string]bool)
	depthOf := make(map[string]int)
	{
		type item struct {
			oid   string
			depth int
		}
		queue := []item{}
		for _, w := range req.wants {
			queue = append(queue, item{w, 0})
		}
		seenCommit := make(map[string]bool)
		for len(queue) > 0 {
			it := queue[0]
			queue = queue[1:]
			obj, ok := r.Objects[it.oid]
			if !ok {
				continue
			}
			switch obj.Type {
			case "commit":
				if seenCommit[it.oid] {
					continue
				}
				seenCommit[it.oid] = true
				depthOf[it.oid] = it.depth
				if !hasSet[it.oid] {
					send[it.oid] = true
					addTreeObjects(r, parseCommitTree(obj.Data), send)
				} else if it.depth == 0 {
					// want already held: nothing to send from it, but its
					// depth still counts for boundary computation below
				}
				if req.deepen < 0 || it.depth+1 < req.deepen {
					for _, par := range parseCommitParents(obj.Data) {
						queue = append(queue, item{par, it.depth + 1})
					}
				}
			case "tag":
				if !hasSet[it.oid] {
					send[it.oid] = true
				}
				if target := parseTagTarget(obj.Data); target != "" {
					queue = append(queue, item{target, it.depth})
				}
			case "tree", "blob":
				if !hasSet[it.oid] {
					send[it.oid] = true
				}
			}
		}
	}
	// boundary + unshallow
	if req.deepen > 0 {
		boundary := make(map[string]bool)
		for oid, d := range depthOf {
			if d == req.deepen-1 {
				obj, ok := r.Objects[oid]
				if !ok || obj.Type != "commit" {
					continue
				}
				if len(parseCommitParents(obj.Data)) > 0 {
					boundary[oid] = true
					shallowOut = append(shallowOut, oid)
				}
			}
		}
		for _, old := range req.shallow {
			if _, isBoundary := boundary[old]; isBoundary {
				continue
			}
			if _, reachable := depthOf[old]; reachable {
				unshallowOut = append(unshallowOut, old)
			}
		}
	} else {
		// no depth limit: every old shallow commit is now interior
		unshallowOut = append(unshallowOut, req.shallow...)
	}
	sort.Strings(shallowOut)
	sort.Strings(unshallowOut)
	// addTreeObjects adds whole trees at once, so already-held objects can
	// slip into `send` (unchanged subtrees/blobs under a new tree). An
	// incremental fetch must never re-send what the client holds.
	for oid := range send {
		if !hasSet[oid] {
			oids = append(oids, oid)
		}
	}
	sort.Strings(oids)
	return oids, shallowOut, unshallowOut
}

// addTreeObjects adds a tree and everything under it to set.
func addTreeObjects(r *Repo, treeOID string, set map[string]bool) {
	obj, ok := r.Objects[treeOID]
	if !ok || obj.Type != "tree" {
		return
	}
	if set[treeOID] {
		return
	}
	set[treeOID] = true
	data := obj.Data
	for len(data) > 0 {
		sp := strings.IndexByte(string(data), ' ')
		if sp < 0 {
			return
		}
		mode := string(data[:sp])
		data = data[sp+1:]
		nul := strings.IndexByte(string(data), 0)
		if nul < 0 || len(data) < nul+21 {
			return
		}
		oid := hex.EncodeToString(data[nul+1 : nul+21])
		data = data[nul+21:]
		if mode == "40000" {
			addTreeObjects(r, oid, set)
		} else {
			set[oid] = true
		}
	}
}

func sidebandPack(pack []byte) []byte {
	var buf []byte
	const chunk = 32768
	for len(pack) > 0 {
		n := len(pack)
		if n > chunk {
			n = chunk
		}
		payload := make([]byte, 0, n+1)
		payload = append(payload, 1)
		payload = append(payload, pack[:n]...)
		buf = append(buf, pktLine(payload)...)
		pack = pack[n:]
	}
	if len(buf) == 0 {
		payload := []byte{1}
		buf = append(buf, pktLine(payload)...)
	}
	return buf
}

// fetchResponseV2 builds the v2 fetch response for a parsed request.
//
// Section framing (protocol v2, as real git sends it): every section except
// the last ends with a DELIM (0001); the last section ends with a FLUSH
// (0000). The acknowledgments section is sent ONLY when the request carried
// "have" lines (there is something to negotiate): with no haves the client
// expects the packfile section first. When sent, it carries NAK + "ready"
// (a packfile always follows, even an empty one for fetch-nothing-new).
func (s *Store) fetchResponseV2(r *Repo, req fetchRequest) []byte {
	oids, shallowOut, unshallowOut := r.selectFetch(req)
	pack := s.cachedPack(r, req, oids)
	// sections, in order; each is a list of pkt-lines (without terminator)
	var sections [][]byte
	if len(req.haves) > 0 {
		ack := []byte{}
		ack = append(ack, pktString("acknowledgments\n")...)
		ack = append(ack, pktString("NAK\n")...)
		ack = append(ack, pktString("ready\n")...)
		sections = append(sections, ack)
	}
	if len(shallowOut) > 0 || len(unshallowOut) > 0 {
		si := []byte{}
		si = append(si, pktString("shallow-info\n")...)
		for _, o := range shallowOut {
			si = append(si, pktString("shallow "+o+"\n")...)
		}
		for _, o := range unshallowOut {
			si = append(si, pktString("unshallow "+o+"\n")...)
		}
		sections = append(sections, si)
	}
	pf := []byte{}
	pf = append(pf, pktString("packfile\n")...)
	pf = append(pf, sidebandPack(pack)...)
	sections = append(sections, pf)
	var buf []byte
	for i, sec := range sections {
		buf = append(buf, sec...)
		if i < len(sections)-1 {
			buf = append(buf, pktDelim...)
		} else {
			buf = append(buf, pktFlush...)
		}
	}
	return buf
}

func (s *Store) fetchResponseV0(r *Repo, req fetchRequest) []byte {
	oids, shallowOut, unshallowOut := r.selectFetch(req)
	pack := s.cachedPack(r, req, oids)
	var buf []byte
	buf = append(buf, pktString("NAK\n")...)
	for _, o := range shallowOut {
		buf = append(buf, pktString("shallow "+o+"\n")...)
	}
	for _, o := range unshallowOut {
		buf = append(buf, pktString("unshallow "+o+"\n")...)
	}
	buf = append(buf, sidebandPack(pack)...)
	buf = append(buf, pktFlush...)
	return buf
}

// cachedPack returns the packfile for this exact fetch, building and caching it.
// Callers must hold at least a read lock on the store (the repo objects are read).
// The cache itself has its own mutex so concurrent clones do not serialize.
func (s *Store) cachedPack(r *Repo, req fetchRequest, oids []string) []byte {
	key := r.Owner + "/" + r.Name + "|" + strings.Join(req.wants, ",") + "|" +
		strings.Join(req.haves, ",") + "|" + strings.Join(req.shallow, ",") + "|" +
		intToString(req.deepen) + "|" + strings.Join(oids, ",")
	s.packCacheMu.Lock()
	if pack, ok := s.packCache[key]; ok {
		s.packCacheMu.Unlock()
		return pack
	}
	s.packCacheMu.Unlock()
	pack := buildPack(r.Objects, oids)
	s.packCacheMu.Lock()
	if len(s.packCache) > 16 {
		s.packCache = make(map[string][]byte)
	}
	s.packCache[key] = pack
	s.packCacheMu.Unlock()
	return pack
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------------------------------------------------------------- receive-pack (push)

type pushCommand struct {
	oldOID string
	newOID string
	ref    string
}

func (s *Store) receivePack(r *Repo, body []byte) []byte {
	lines, tail := splitPktBody(body)
	var cmds []pushCommand
	sideband := false
	for _, p := range lines {
		if p.kind != "data" {
			continue
		}
		text := string(p.payload)
		if nul := strings.IndexByte(text, 0); nul >= 0 {
			// capabilities on the first line
			for _, cap := range strings.Fields(text[nul+1:]) {
				if cap == "side-band-64k" {
					sideband = true
				}
			}
			text = text[:nul]
		}
		text = strings.TrimRight(text, "\n")
		parts := strings.SplitN(text, " ", 3)
		if len(parts) != 3 {
			continue
		}
		cmds = append(cmds, pushCommand{oldOID: parts[0], newOID: parts[1], ref: parts[2]})
	}
	// parse the pack (absent for delete-only pushes)
	var incoming map[string]*parsedObject
	if len(tail) > 0 {
		parsed, err := parsePack(r, tail)
		if err == nil {
			incoming = parsed
		}
	}
	results := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		results = append(results, s.applyPush(r, cmd, incoming))
	}
	// The report-status stream is itself pkt-lined inside the band-1 sideband
	// payload (and pkt-lined directly when side-band-64k is not negotiated):
	// each status line is its own pkt-line, and the inner stream ends with
	// its own flush packet (as real git sends it) before the outer flush.
	var inner []byte
	inner = append(inner, pktString("unpack ok\n")...)
	for i, cmd := range cmds {
		if results[i] == "" {
			inner = append(inner, pktString("ok "+cmd.ref+"\n")...)
		} else {
			inner = append(inner, pktString("ng "+cmd.ref+" "+results[i]+"\n")...)
		}
	}
	inner = append(inner, pktFlush...)
	var buf []byte
	if sideband {
		buf = append(buf, sidebandPack(inner)...)
	} else {
		buf = append(buf, inner...)
	}
	buf = append(buf, pktFlush...)
	return buf
}

// applyPush validates and applies one ref update; "" means ok, else the ng reason.
func (s *Store) applyPush(r *Repo, cmd pushCommand, incoming map[string]*parsedObject) string {
	current, exists := r.Refs[cmd.ref]
	if cmd.newOID == zeroOID {
		// delete
		if !exists {
			return "no such ref"
		}
		if cmd.oldOID != zeroOID && cmd.oldOID != current {
			return "non-fast-forward"
		}
		delete(r.Refs, cmd.ref)
		if strings.HasPrefix(cmd.ref, "refs/heads/") {
			delete(r.BranchFile, strings.TrimPrefix(cmd.ref, "refs/heads/"))
		}
		return ""
	}
	// new oid must resolve (in the pack or already stored)
	if incoming != nil {
		for oid, obj := range incoming {
			if _, ok := r.Objects[oid]; !ok {
				r.Objects[oid] = &GitObject{Type: obj.Type, Data: obj.Data}
			}
		}
	}
	if _, ok := r.Objects[cmd.newOID]; !ok {
		return "object not found"
	}
	if cmd.oldOID == zeroOID {
		// create
		if exists {
			return "already exists"
		}
	} else {
		// update: must be a fast-forward from old
		if !exists {
			return "no such ref"
		}
		if cmd.oldOID != current {
			return "non-fast-forward"
		}
		if !r.isAncestor(cmd.oldOID, cmd.newOID) {
			return "non-fast-forward"
		}
	}
	r.Refs[cmd.ref] = cmd.newOID
	if strings.HasPrefix(cmd.ref, "refs/heads/") {
		branch := strings.TrimPrefix(cmd.ref, "refs/heads/")
		if obj, ok := r.Objects[cmd.newOID]; ok && obj.Type == "commit" {
			files := r.filesFromTree(parseCommitTree(obj.Data))
			cp := make(map[string]string, len(files))
			for k, v := range files {
				cp[k] = v
			}
			r.BranchFile[branch] = cp
			r.Snapshots[cmd.newOID] = cp
		}
	}
	_, now := s.tick()
	r.PushedAt = now
	r.UpdatedAt = now
	return ""
}

// ---------------------------------------------------------------- HTTP entry

func (s *Store) gitHTTP(w http.ResponseWriter, r *http.Request, owner, repoName string) {
	rest := strings.TrimPrefix(r.URL.Path, "/"+owner+"/"+repoName+".git/")
	// Pushes mutate: exclusive lock. Reads (advertisement, ls-refs, fetch)
	// share a read lock so concurrent clones run in parallel.
	if rest == "git-receive-pack" && r.Method == http.MethodPost {
		s.mu.Lock()
		defer s.mu.Unlock()
	} else {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	repo := s.getRepo(owner, repoName)
	if repo == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch {
	case rest == "info/refs":
		service := r.URL.Query().Get("service")
		if service != "git-upload-pack" && service != "git-receive-pack" {
			http.Error(w, "unknown service", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Git-Protocol") == "version=2" && service == "git-upload-pack" {
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			w.WriteHeader(http.StatusOK)
			w.Write(repo.advertisementV2())
			return
		}
		if service == "git-upload-pack" {
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		} else {
			w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		}
		w.WriteHeader(http.StatusOK)
		w.Write(repo.advertisementV0(service))
	case rest == "git-upload-pack" && r.Method == http.MethodPost:
		body := readBody(r)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		w.WriteHeader(http.StatusOK)
		if r.Header.Get("Git-Protocol") == "version=2" {
			lines := parsePktLines(body)
			if len(lines) > 0 && isCommand(lines, "ls-refs") {
				w.Write(repo.lsRefsV2(lines))
				return
			}
			if len(lines) > 0 && isCommand(lines, "fetch") {
				w.Write(s.fetchResponseV2(repo, parseFetchV2(lines)))
				return
			}
			// unknown v2 command: empty response with flush
			w.Write(pktFlush)
			return
		}
		req, _ := parseFetchV0(body)
		w.Write(s.fetchResponseV0(repo, req))
	case rest == "git-receive-pack" && r.Method == http.MethodPost:
		body := readBody(r)
		w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
		w.WriteHeader(http.StatusOK)
		w.Write(s.receivePack(repo, body))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func isCommand(lines []pkt, cmd string) bool {
	for _, p := range lines {
		if p.kind == "data" {
			return strings.TrimRight(string(p.payload), "\n") == "command="+cmd
		}
	}
	return false
}
