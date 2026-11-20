package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- data types

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Type  string `json:"type"`
}

type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type Comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      *User  `json:"user"`
}

type Issue struct {
	ID        int64      `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"` // open | closed
	Labels    []*Label   `json:"labels"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	Comments  []*Comment `json:"-"`
	repo      *Repo      `json:"-"`
	// cached is the marshaled issueJSON for GET /issues/{n}, built lazily.
	// The read workload GETs the same issue 2,000x on one connection; the
	// JSON never changes except on PATCH (which clears it). Base URL is
	// constant for the server's lifetime, so the cache stays valid.
	cached []byte `json:"-"`
}

type Pull struct {
	ID             int64  `json:"id"`
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	HeadRef        string `json:"-"`
	BaseRef        string `json:"-"`
	HeadSHA        string `json:"-"`
	BaseSHA        string `json:"-"`
	State          string `json:"state"` // open | closed
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	repo           *Repo  `json:"-"`
}

// GitObject is one git object: type + raw payload (no header).
type GitObject struct {
	Type string
	Data []byte
}

type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Owner         string `json:"-"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	PushedAt      string `json:"pushed_at"`

	// git state
	Objects    map[string]*GitObject       // oid -> object
	Refs       map[string]string           // full refname -> commit oid
	BranchFile map[string]map[string]string // branch -> path -> blob oid
	Snapshots  map[string]map[string]string // commit oid -> path -> blob oid (lazy)

	Labels     map[string]*Label
	LabelOrder []string
	Issues     []*Issue
	IssueByNum map[int]*Issue
	IssueSeq   int
	Pulls      []*Pull
	PullByNum  map[int]*Pull
	PullSeq    int
}

type Store struct {
	mu       sync.RWMutex
	viewer   *User
	token    string
	authHeader string // "Bearer "+token, precomputed (per-request concat was pure alloc)
	repos    map[string]*Repo // "owner/name" -> repo
	repoSeq  int64
	idSeq    int64 // global object ids (issues, comments, pulls)
	clock    int64 // logical seconds since scenario epoch
	reqSeq      int64
	packCache   map[string][]byte
	packCacheMu sync.Mutex
	// packFlight deduplicates concurrent builds of the same pack (the
	// clone workload fires 50 identical shallow clones at once; without
	// this all 50 build the same pack on 1.5 CPUs).
	packFlight   map[string]*packCall
	packFlightMu sync.Mutex
}

type packCall struct {
	done chan struct{}
	pack []byte
}

func newStore(token string) *Store {
	s := &Store{
		viewer:     deriveViewer(),
		token:      token,
		repos:      make(map[string]*Repo),
		packCache:  make(map[string][]byte),
		packFlight: make(map[string]*packCall),
	}
	if token != "" {
		s.authHeader = "Bearer " + token
	}
	return s
}

// ---------------------------------------------------------------- logical clock

// tick advances the logical clock by one second and returns (unix seconds, RFC3339).
func (s *Store) tick() (int64, string) {
	s.clock++
	sec := scenarioEpoch + s.clock
	return sec, time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func (s *Store) nextReqID() string {
	s.reqSeq++
	return "req-" + strconv.FormatInt(s.reqSeq, 10)
}

func (s *Store) nextID() int64 {
	s.idSeq++
	return s.idSeq
}

// ---------------------------------------------------------------- repos

func (s *Store) createRepo(name string, private bool, description string) (*Repo, bool) {
	key := s.viewer.Login + "/" + name
	if _, exists := s.repos[key]; exists {
		return nil, false
	}
	s.repoSeq++
	_, now := s.tick()
	r := &Repo{
		ID:            s.repoSeq,
		Name:          name,
		Owner:         s.viewer.Login,
		Private:       private,
		Description:   description,
		DefaultBranch: "main",
		CreatedAt:     now,
		UpdatedAt:     now,
		PushedAt:      now,
		Objects:       make(map[string]*GitObject),
		Refs:          make(map[string]string),
		BranchFile:    make(map[string]map[string]string),
		Snapshots:     make(map[string]map[string]string),
		Labels:        make(map[string]*Label),
		IssueByNum:    make(map[int]*Issue),
		PullByNum:     make(map[int]*Pull),
	}
	// NOTE: labels are NOT pre-seeded. A fresh repository starts with no
	// labels; the checks create the ones they need through POST /labels
	// (and expect 201 for that first creation). Pre-seeding the GitHub
	// defaults made that creation answer 422 and failed every setup that
	// depends on it.
	s.repos[key] = r
	return r, true
}

func (s *Store) getRepo(owner, name string) *Repo {
	return s.repos[owner+"/"+name]
}

func (s *Store) deleteRepo(owner, name string) bool {
	key := owner + "/" + name
	if _, ok := s.repos[key]; !ok {
		return false
	}
	delete(s.repos, key)
	return true
}

func (s *Store) viewerRepos() []*Repo {
	out := make([]*Repo, 0, len(s.repos))
	for _, r := range s.repos {
		if r.Owner == s.viewer.Login {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---------------------------------------------------------------- git objects

func gitOID(objType string, data []byte) string {
	h := sha1.New()
	h.Write([]byte(objType))
	h.Write([]byte(" "))
	h.Write(strconv.AppendInt(nil, int64(len(data)), 10))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Repo) putObject(objType string, data []byte) string {
	oid := gitOID(objType, data)
	if _, ok := r.Objects[oid]; !ok {
		r.Objects[oid] = &GitObject{Type: objType, Data: data}
	}
	return oid
}

type treeNode struct {
	dirs  map[string]*treeNode
	files map[string]string // name -> blob oid
}

func newTreeNode() *treeNode {
	return &treeNode{dirs: make(map[string]*treeNode), files: make(map[string]string)}
}

func insertFile(n *treeNode, parts []string, oid string) {
	if len(parts) == 1 {
		n.files[parts[0]] = oid
		return
	}
	child, ok := n.dirs[parts[0]]
	if !ok {
		child = newTreeNode()
		n.dirs[parts[0]] = child
	}
	insertFile(child, parts[1:], oid)
}

// writeTreeNode serializes one tree node (recursively) and returns its oid.
func (r *Repo) writeTreeNode(n *treeNode) string {
	type entry struct {
		sortKey string
		mode    string
		name    string
		oid     string
	}
	entries := []entry{}
	for name, oid := range n.files {
		entries = append(entries, entry{sortKey: name, mode: "100644", name: name, oid: oid})
	}
	for name, child := range n.dirs {
		entries = append(entries, entry{sortKey: name + "/", mode: "40000", name: name, oid: r.writeTreeNode(child)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortKey < entries[j].sortKey })
	var buf []byte
	for _, e := range entries {
		buf = append(buf, []byte(e.mode+" "+e.name+"\x00")...)
		raw, _ := hex.DecodeString(e.oid)
		buf = append(buf, raw...)
	}
	return r.putObject("tree", buf)
}

// buildTree builds tree objects for a flat path->blob map and returns the root tree oid.
func (r *Repo) buildTree(files map[string]string) string {
	root := newTreeNode()
	for p, oid := range files {
		insertFile(root, strings.Split(p, "/"), oid)
	}
	return r.writeTreeNode(root)
}

// treeOIDsForDir returns the tree oid of every directory in a file map ("" = root).
func (r *Repo) treeOIDsForDir(files map[string]string) map[string]string {
	root := newTreeNode()
	for p, oid := range files {
		insertFile(root, strings.Split(p, "/"), oid)
	}
	out := make(map[string]string)
	var walk func(n *treeNode, prefix string) string
	walk = func(n *treeNode, prefix string) string {
		oid := r.writeTreeNode(n)
		out[prefix] = oid
		for name, child := range n.dirs {
			childPrefix := name
			if prefix != "" {
				childPrefix = prefix + "/" + name
			}
			walk(child, childPrefix)
		}
		return oid
	}
	walk(root, "")
	return out
}

// parseCommitAuthor extracts (name, email, unix seconds) from a commit object.
// Byte-scanning: the old strings.Split(string(data)) allocated a full copy
// plus one string per line on every call, and this runs on the push hot path.
func parseCommitAuthor(data []byte) (string, string, int64) {
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		var line []byte
		if nl < 0 {
			line, data = data, nil
		} else {
			line, data = data[:nl], data[nl+1:]
		}
		if len(line) == 0 {
			break
		}
		if !bytes.HasPrefix(line, []byte("author ")) {
			continue
		}
		rest := line[7:]
		lt := bytes.IndexByte(rest, '<')
		gt := bytes.IndexByte(rest, '>')
		if lt < 0 || gt < 0 {
			return string(rest), "", 0
		}
		name := strings.TrimSpace(string(rest[:lt]))
		email := string(rest[lt+1 : gt])
		var ts int64
		for _, c := range rest[gt+1:] {
			if c >= '0' && c <= '9' {
				ts = ts*10 + int64(c-'0')
			} else if ts > 0 {
				break
			}
		}
		return name, email, ts
	}
	return "", "", 0
}

func formatRFC3339(ts int64) string {
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// filesFromTree reconstructs the flat path->blob map for a tree oid.
func (r *Repo) filesFromTree(treeOID string) map[string]string {
	out := make(map[string]string)
	var walk func(treeOID, prefix string)
	walk = func(treeOID, prefix string) {
		obj, ok := r.Objects[treeOID]
		if !ok || obj.Type != "tree" {
			return
		}
		data := obj.Data
		for len(data) > 0 {
			sp := bytes.IndexByte(data, ' ')
			if sp < 0 {
				return
			}
			mode := string(data[:sp])
			data = data[sp+1:]
			nul := bytes.IndexByte(data, 0)
			if nul < 0 || len(data) < nul+21 {
				return
			}
			name := string(data[:nul])
			oid := hex.EncodeToString(data[nul+1 : nul+21])
			data = data[nul+21:]
			if mode == "40000" {
				walk(oid, prefix+name+"/")
			} else {
				out[prefix+name] = oid
			}
		}
	}
	walk(treeOID, "")
	return out
}

// snapshotFor returns the file map of a commit, computing and caching it.
func (r *Repo) snapshotFor(commitOID string) map[string]string {
	if snap, ok := r.Snapshots[commitOID]; ok {
		return snap
	}
	obj, ok := r.Objects[commitOID]
	if !ok || obj.Type != "commit" {
		return map[string]string{}
	}
	tree := parseCommitTree(obj.Data)
	snap := r.filesFromTree(tree)
	r.Snapshots[commitOID] = snap
	return snap
}

func parseCommitTree(data []byte) string {
	// first line: "tree <hex>"
	if len(data) < 5 || string(data[:5]) != "tree " {
		return ""
	}
	end := bytes.IndexByte(data, '\n')
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(string(data[5:end]))
}

func parseCommitParents(data []byte) []string {
	var parents []string
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		var line []byte
		if nl < 0 {
			line, data = data, nil
		} else {
			line, data = data[:nl], data[nl+1:]
		}
		if len(line) == 0 {
			break
		}
		if bytes.HasPrefix(line, []byte("parent ")) {
			parents = append(parents, strings.TrimSpace(string(line[7:])))
		}
	}
	return parents
}

func parseCommitMessage(data []byte) string {
	idx := bytes.Index(data, []byte("\n\n"))
	if idx < 0 {
		return ""
	}
	return strings.TrimRight(string(data[idx+2:]), "\n")
}

// createCommitOnBranch creates a commit with the given full file map on branch.
func (s *Store) createCommitOnBranch(r *Repo, branch, message string, files map[string]string) string {
	treeOID := r.buildTree(files)
	sec, _ := s.tick()
	var b strings.Builder
	fmt.Fprintf(&b, "tree %s\n", treeOID)
	parent := r.Refs["refs/heads/"+branch]
	if parent != "" {
		fmt.Fprintf(&b, "parent %s\n", parent)
	}
	author := fmt.Sprintf("%s <%s>", s.viewer.Name, s.viewer.Email)
	fmt.Fprintf(&b, "author %s %d +0000\n", author, sec)
	fmt.Fprintf(&b, "committer %s %d +0000\n\n", author, sec)
	b.WriteString(message)
	b.WriteString("\n")
	oid := r.putObject("commit", []byte(b.String()))
	r.Refs["refs/heads/"+branch] = oid
	cp := make(map[string]string, len(files))
	for k, v := range files {
		cp[k] = v
	}
	r.Snapshots[oid] = cp
	r.BranchFile[branch] = cp
	_, now := s.tick()
	r.UpdatedAt = now
	r.PushedAt = now
	return oid
}

// ancestors returns the set of commit oids reachable from tip (including tip).
func (r *Repo) ancestors(tip string) map[string]bool {
	seen := make(map[string]bool)
	if tip == "" {
		return seen
	}
	stack := []string{tip}
	for len(stack) > 0 {
		oid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		obj, ok := r.Objects[oid]
		if !ok || obj.Type != "commit" {
			continue
		}
		stack = append(stack, parseCommitParents(obj.Data)...)
	}
	return seen
}

func (r *Repo) isAncestor(ancestor, tip string) bool {
	if ancestor == tip {
		return true
	}
	// DFS from the tip with early exit. The old version built the FULL
	// ancestor set of the tip on every push (O(chain) commit parses per
	// push, O(chain^2) per 200-push round); a linear push chain only needs
	// to walk until it finds the ancestor, usually 1 step.
	seen := make(map[string]bool)
	stack := []string{tip}
	for len(stack) > 0 {
		oid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		obj, ok := r.Objects[oid]
		if !ok || obj.Type != "commit" {
			continue
		}
		for _, p := range parseCommitParents(obj.Data) {
			if p == ancestor {
				return true
			}
			stack = append(stack, p)
		}
	}
	return false
}

// mergeBase finds a common ancestor of a and b (BFS from b, first hit in a's set).
func (r *Repo) mergeBase(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	setA := r.ancestors(a)
	seen := make(map[string]bool)
	queue := []string{b}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if setA[oid] {
			return oid
		}
		obj, ok := r.Objects[oid]
		if !ok || obj.Type != "commit" {
			continue
		}
		queue = append(queue, parseCommitParents(obj.Data)...)
	}
	return ""
}

type fileChange struct {
	Path   string
	OldOID string // "" when added
	NewOID string // "" when removed
	Status string // added | modified | removed
}

func diffFiles(oldMap, newMap map[string]string) []fileChange {
	paths := make(map[string]bool)
	for p := range oldMap {
		paths[p] = true
	}
	for p := range newMap {
		paths[p] = true
	}
	var out []fileChange
	for p := range paths {
		oldOID, inOld := oldMap[p]
		newOID, inNew := newMap[p]
		switch {
		case !inOld:
			out = append(out, fileChange{Path: p, NewOID: newOID, Status: "added"})
		case !inNew:
			out = append(out, fileChange{Path: p, OldOID: oldOID, Status: "removed"})
		case oldOID != newOID:
			out = append(out, fileChange{Path: p, OldOID: oldOID, NewOID: newOID, Status: "modified"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// blobContent returns the raw bytes of a blob oid (nil when missing).
func (r *Repo) blobContent(oid string) []byte {
	obj, ok := r.Objects[oid]
	if !ok || obj.Type != "blob" {
		return nil
	}
	return obj.Data
}
