package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- JSON shapes

type repoOut struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Owner         *User  `json:"owner"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	PushedAt      string `json:"pushed_at"`
	Fork          bool   `json:"fork"`
	URL           string `json:"url"`
	HTMLURL       string `json:"html_url"`
}

func (s *Store) repoJSON(r *Repo, base string) repoOut {
	return repoOut{
		ID: r.ID, Name: r.Name, FullName: r.Owner + "/" + r.Name,
		Owner: &User{ID: s.viewer.ID, Login: r.Owner, Name: s.viewer.Name, Email: s.viewer.Email, Type: "User"},
		Private: r.Private, Description: r.Description, DefaultBranch: r.DefaultBranch,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, PushedAt: r.PushedAt,
		URL:     base + "/api/v3/repos/" + r.Owner + "/" + r.Name,
		HTMLURL: base + "/" + r.Owner + "/" + r.Name,
	}
}

type issueOut struct {
	ID        int64    `json:"id"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	State     string   `json:"state"`
	Labels    []*Label `json:"labels"`
	User      *User    `json:"user"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	URL       string   `json:"url"`
}

func (s *Store) issueJSON(r *Repo, is *Issue, base string) issueOut {
	labels := is.Labels
	if labels == nil {
		labels = []*Label{}
	}
	return issueOut{
		ID: is.ID, Number: is.Number, Title: is.Title, Body: is.Body, State: is.State,
		Labels: labels, User: s.viewer, CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt,
		URL: base + "/api/v3/repos/" + r.Owner + "/" + r.Name + "/issues/" + strconv.Itoa(is.Number),
	}
}

type refSide struct {
	Ref   string `json:"ref"`
	SHA   string `json:"sha"`
	Label string `json:"label"`
}

type pullOut struct {
	ID             int64   `json:"id"`
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Body           string  `json:"body"`
	State          string  `json:"state"`
	Head           refSide `json:"head"`
	Base           refSide `json:"base"`
	Merged         bool    `json:"merged"`
	MergedAt       string  `json:"merged_at"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	User           *User   `json:"user"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	URL            string  `json:"url"`
}

func (s *Store) pullJSON(r *Repo, pr *Pull, base string) pullOut {
	return pullOut{
		ID: pr.ID, Number: pr.Number, Title: pr.Title, Body: pr.Body, State: pr.State,
		Head:           refSide{Ref: pr.HeadRef, SHA: pr.HeadSHA, Label: r.Owner + ":" + pr.HeadRef},
		Base:           refSide{Ref: pr.BaseRef, SHA: pr.BaseSHA, Label: r.Owner + ":" + pr.BaseRef},
		Merged:         pr.Merged, MergedAt: pr.MergedAt, MergeCommitSHA: pr.MergeCommitSHA,
		User:           s.viewer, CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt,
		URL:            base + "/api/v3/repos/" + r.Owner + "/" + r.Name + "/pulls/" + strconv.Itoa(pr.Number),
	}
}

type commentOut struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      *User  `json:"user"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type branchOut struct {
	Name   string `json:"name"`
	Commit struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commit"`
	Protected bool `json:"protected"`
}

func (s *Store) branchJSON(r *Repo, name, sha string) branchOut {
	var out branchOut
	out.Name = name
	out.Commit.SHA = sha
	if obj, ok := r.Objects[sha]; ok && obj.Type == "commit" {
		out.Commit.Commit.Message = parseCommitMessage(obj.Data)
	}
	return out
}

type commitOut struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Author    gitActor `json:"author"`
		Committer gitActor `json:"committer"`
	} `json:"commit"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

type gitActor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

func (s *Store) commitJSON(r *Repo, sha string) commitOut {
	var out commitOut
	out.SHA = sha
	obj, ok := r.Objects[sha]
	if !ok || obj.Type != "commit" {
		return out
	}
	out.Commit.Message = parseCommitMessage(obj.Data)
	name, email, ts := parseCommitAuthor(obj.Data)
	date := ""
	if ts > 0 {
		date = formatRFC3339(ts)
	}
	out.Commit.Author = gitActor{Name: name, Email: email, Date: date}
	out.Commit.Committer = gitActor{Name: name, Email: email, Date: date}
	for _, p := range parseCommitParents(obj.Data) {
		out.Parents = append(out.Parents, struct {
			SHA string `json:"sha"`
		}{SHA: p})
	}
	if out.Parents == nil {
		out.Parents = []struct {
			SHA string `json:"sha"`
		}{}
	}
	return out
}

// ---------------------------------------------------------------- pagination

type pageParams struct {
	page    int
	perPage int
}

func parsePage(r *http.Request) pageParams {
	p := pageParams{page: 1, perPage: 30}
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.page = n
		}
	}
	if v := r.URL.Query().Get("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 100 {
				n = 100
			}
			p.perPage = n
		}
	}
	return p
}

// paginate slices items and sets the Link header (next/prev/first/last).
func paginate[T any](w http.ResponseWriter, r *http.Request, items []T) []T {
	p := parsePage(r)
	total := len(items)
	start := (p.page - 1) * p.perPage
	if start >= total {
		setLinkHeader(w, r, p, total)
		return []T{}
	}
	end := start + p.perPage
	if end > total {
		end = total
	}
	setLinkHeader(w, r, p, total)
	return items[start:end]
}

func setLinkHeader(w http.ResponseWriter, r *http.Request, p pageParams, total int) {
	lastPage := (total + p.perPage - 1) / p.perPage
	if lastPage < 1 {
		lastPage = 1
	}
	var links []string
	base := "http://" + r.Host + r.URL.Path
	q := r.URL.Query()
	link := func(page int, rel string) string {
		qq := url.Values{}
		for k, vs := range q {
			if k == "page" {
				continue
			}
			qq[k] = vs
		}
		qq.Set("page", strconv.Itoa(page))
		return "<" + base + "?" + qq.Encode() + ">; rel=\"" + rel + "\""
	}
	if p.page < lastPage {
		links = append(links, link(p.page+1, "next"), link(lastPage, "last"))
	}
	if p.page > 1 {
		links = append(links, link(p.page-1, "prev"), link(1, "first"))
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
}

// ---------------------------------------------------------------- helpers

func readJSONBody(r *http.Request, v any) error {
	body := readBody(r)
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

func parseLabelNames(raw any) []string {
	var out []string
	if arr, ok := raw.([]any); ok {
		for _, e := range arr {
			switch t := e.(type) {
			case string:
				out = append(out, t)
			case map[string]any:
				if name, ok := t["name"].(string); ok {
					out = append(out, name)
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- merge logic

// mergeable returns MERGEABLE, CONFLICTING or UNKNOWN for a pull request.
func (r *Repo) mergeable(pr *Pull) string {
	baseTip := r.Refs["refs/heads/"+pr.BaseRef]
	headTip := r.Refs["refs/heads/"+pr.HeadRef]
	if baseTip == "" || headTip == "" {
		return "UNKNOWN"
	}
	mb := r.mergeBase(baseTip, headTip)
	if mb == "" {
		return "UNKNOWN"
	}
	if r.hasConflict(mb, baseTip, headTip) {
		return "CONFLICTING"
	}
	return "MERGEABLE"
}

func (r *Repo) hasConflict(mergeBase, baseTip, headTip string) bool {
	b0 := r.snapshotFor(mergeBase)
	b1 := r.snapshotFor(baseTip)
	h1 := r.snapshotFor(headTip)
	paths := make(map[string]bool)
	for p := range b0 {
		paths[p] = true
	}
	for p := range b1 {
		paths[p] = true
	}
	for p := range h1 {
		paths[p] = true
	}
	for p := range paths {
		changedBase := b0[p] != b1[p]
		changedHead := b0[p] != h1[p]
		if changedBase && changedHead && b1[p] != h1[p] {
			return true
		}
	}
	return false
}

// mergePull performs a squash merge onto the base branch. Returns (sha, ok).
func (s *Store) mergePull(r *Repo, pr *Pull) (string, bool) {
	baseTip := r.Refs["refs/heads/"+pr.BaseRef]
	headTip := r.Refs["refs/heads/"+pr.HeadRef]
	if baseTip == "" || headTip == "" {
		return "", false
	}
	mb := r.mergeBase(baseTip, headTip)
	if mb == "" {
		return "", false
	}
	if r.hasConflict(mb, baseTip, headTip) {
		return "", false
	}
	b0 := r.snapshotFor(mb)
	h1 := r.snapshotFor(headTip)
	result := make(map[string]string, len(r.snapshotFor(baseTip)))
	for k, v := range r.snapshotFor(baseTip) {
		result[k] = v
	}
	for p, hv := range h1 {
		if b0[p] != hv {
			result[p] = hv
		}
	}
	for p := range b0 {
		if _, inHead := h1[p]; !inHead && b0[p] != "" {
			// deleted on the head side (only when it changed there)
			if _, inBase := r.snapshotFor(baseTip)[p]; inBase {
				// keep base version unless head deleted it AND base did not change it
				if b0[p] == r.snapshotFor(baseTip)[p] {
					delete(result, p)
				}
			}
		}
	}
	msg := fmt.Sprintf("Merge pull request #%d from %s/%s", pr.Number, r.Owner, pr.HeadRef)
	oid := s.createCommitOnBranch(r, pr.BaseRef, msg, result)
	_, now := s.tick()
	pr.State = "closed"
	pr.Merged = true
	pr.MergedAt = now
	pr.MergeCommitSHA = oid
	pr.UpdatedAt = now
	return oid, true
}

func (r *Repo) pullFiles(pr *Pull) []map[string]any {
	baseTip := r.Refs["refs/heads/"+pr.BaseRef]
	headTip := r.Refs["refs/heads/"+pr.HeadRef]
	mb := r.mergeBase(baseTip, headTip)
	changes := diffFiles(r.snapshotFor(mb), r.snapshotFor(headTip))
	out := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		add, del := 0, 0
		if c.NewOID != "" {
			add = countLines(r.blobContent(c.NewOID))
		}
		if c.OldOID != "" {
			del = countLines(r.blobContent(c.OldOID))
		}
		out = append(out, map[string]any{
			"filename":  c.Path,
			"status":    c.Status,
			"additions": add,
			"deletions": del,
			"changes":   add + del,
		})
	}
	return out
}

// ---------------------------------------------------------------- REST router

func (s *Store) restHTTP(w http.ResponseWriter, r *http.Request) {
	// Mutations take the write lock; reads share the read lock.
	if r.Method == http.MethodPost || r.Method == http.MethodPut ||
		r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		s.mu.Lock()
		defer s.mu.Unlock()
	} else {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	path = strings.TrimPrefix(path, "/")
	segs := []string{}
	if path != "" {
		segs = strings.Split(path, "/")
	}
	base := "http://" + r.Host

	writeErr := func(status int, msg string) {
		writeJSON(w, status, map[string]string{"message": msg}, s)
	}

	// Root and meta
	if len(segs) == 0 {
		if r.Method != http.MethodGet {
			writeErr(http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("X-OAuth-Scopes", "repo, read:org")
		writeJSON(w, http.StatusOK, map[string]any{}, s)
		return
	}
	if len(segs) == 1 && segs[0] == "meta" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]string{"installed_version": "0.3.0-go"}, s)
		return
	}
	if len(segs) == 1 && segs[0] == "user" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.viewer, s)
		return
	}
	if len(segs) == 2 && segs[0] == "user" && segs[1] == "repos" {
		if r.Method == http.MethodGet {
			repos := s.viewerRepos()
			outs := make([]repoOut, 0, len(repos))
			for _, rp := range repos {
				outs = append(outs, s.repoJSON(rp, base))
			}
			writeJSON(w, http.StatusOK, paginate(w, r, outs), s)
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				Name        string `json:"name"`
				Private     bool   `json:"private"`
				Description string `json:"description"`
			}
			if err := readJSONBody(r, &body); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			if body.Name == "" {
				writeErr(http.StatusBadRequest, "name is required")
				return
			}
			repo, ok := s.createRepo(body.Name, body.Private, body.Description)
			if !ok {
				writeErr(422, "Repository already exists")
				return
			}
			writeJSON(w, http.StatusCreated, s.repoJSON(repo, base), s)
			return
		}
		writeErr(http.StatusNotFound, "not found")
		return
	}

	// Repo-scoped routes
	if len(segs) >= 3 && segs[0] == "repos" {
		s.restRepo(w, r, segs, base, writeErr)
		return
	}
	writeErr(http.StatusNotFound, "not found")
}

func (s *Store) restRepo(w http.ResponseWriter, r *http.Request, segs []string, base string, writeErr func(int, string)) {
	owner, name := segs[1], segs[2]
	repo := s.getRepo(owner, name)
	rest := segs[3:]

	// Repository itself
	if len(rest) == 0 {
		if repo == nil {
			writeErr(http.StatusNotFound, "Not Found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.repoJSON(repo, base), s)
		case http.MethodPatch:
			var body struct {
				Description *string `json:"description"`
			}
			if err := readJSONBody(r, &body); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			if body.Description != nil {
				repo.Description = *body.Description
			}
			_, now := s.tick()
			repo.UpdatedAt = now
			writeJSON(w, http.StatusOK, s.repoJSON(repo, base), s)
		case http.MethodDelete:
			s.deleteRepo(owner, name)
			w.Header().Set("X-GitHub-Request-Id", s.nextReqID())
			w.WriteHeader(http.StatusNoContent)
		default:
			writeErr(http.StatusNotFound, "not found")
		}
		return
	}
	if repo == nil {
		writeErr(http.StatusNotFound, "Not Found")
		return
	}

	switch rest[0] {
	case "branches":
		s.restBranches(w, r, repo, rest[1:], base, writeErr)
	case "commits":
		if r.Method != http.MethodGet || len(rest) != 1 {
			writeErr(http.StatusNotFound, "not found")
			return
		}
		ref := r.URL.Query().Get("sha")
		tip := repo.Refs["refs/heads/"+repo.DefaultBranch]
		if ref != "" {
			if strings.HasPrefix(ref, "refs/heads/") {
				tip = repo.Refs[ref]
			} else if v, ok := repo.Refs["refs/heads/"+ref]; ok {
				tip = v
			} else if _, ok := repo.Objects[ref]; ok {
				tip = ref
			} else {
				tip = ""
			}
		}
		var outs []commitOut
		seen := make(map[string]bool)
		for cur := tip; cur != "" && !seen[cur]; {
			seen[cur] = true
			outs = append(outs, s.commitJSON(repo, cur))
			parents := []string{}
			if obj, ok := repo.Objects[cur]; ok && obj.Type == "commit" {
				parents = parseCommitParents(obj.Data)
			}
			if len(parents) == 0 {
				break
			}
			cur = parents[0]
		}
		writeJSON(w, http.StatusOK, paginate(w, r, outs), s)
	case "git":
		s.restGitData(w, r, repo, rest[1:], base, writeErr)
	case "labels":
		s.restLabels(w, r, repo, writeErr)
	case "issues":
		s.restIssues(w, r, repo, rest[1:], base, writeErr)
	case "pulls":
		s.restPulls(w, r, repo, rest[1:], base, writeErr)
	case "contents":
		s.restContents(w, r, repo, rest[1:], base, writeErr)
	default:
		writeErr(http.StatusNotFound, "not found")
	}
}

func (s *Store) restBranches(w http.ResponseWriter, r *http.Request, repo *Repo, rest []string, base string, writeErr func(int, string)) {
	if r.Method != http.MethodGet {
		writeErr(http.StatusNotFound, "not found")
		return
	}
	if len(rest) == 0 {
		var names []string
		for ref := range repo.Refs {
			if strings.HasPrefix(ref, "refs/heads/") {
				names = append(names, strings.TrimPrefix(ref, "refs/heads/"))
			}
		}
		sort.Strings(names)
		outs := make([]branchOut, 0, len(names))
		for _, n := range names {
			outs = append(outs, s.branchJSON(repo, n, repo.Refs["refs/heads/"+n]))
		}
		writeJSON(w, http.StatusOK, outs, s)
		return
	}
	name := strings.Join(rest, "/")
	sha, ok := repo.Refs["refs/heads/"+name]
	if !ok {
		writeErr(http.StatusNotFound, "Branch not found")
		return
	}
	writeJSON(w, http.StatusOK, s.branchJSON(repo, name, sha), s)
}

func (s *Store) restGitData(w http.ResponseWriter, r *http.Request, repo *Repo, rest []string, base string, writeErr func(int, string)) {
	if len(rest) >= 1 && rest[0] == "refs" {
		if r.Method == http.MethodGet && len(rest) >= 2 {
			refName := strings.Join(rest[1:], "/")
			if !strings.HasPrefix(refName, "refs/") {
				refName = "refs/" + refName
			}
			sha, ok := repo.Refs[refName]
			if !ok {
				writeErr(http.StatusNotFound, "Not Found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ref":    refName,
				"object": map[string]string{"sha": sha, "type": "commit"},
			}, s)
			return
		}
		if r.Method == http.MethodPost && len(rest) == 1 {
			var body struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}
			if err := readJSONBody(r, &body); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			if body.Ref == "" || body.SHA == "" {
				writeErr(http.StatusBadRequest, "ref and sha are required")
				return
			}
			if _, exists := repo.Refs[body.Ref]; exists {
				writeErr(422, "Reference already exists")
				return
			}
			if _, ok := repo.Objects[body.SHA]; !ok {
				writeErr(422, "Object does not exist")
				return
			}
			repo.Refs[body.Ref] = body.SHA
			if strings.HasPrefix(body.Ref, "refs/heads/") {
				branch := strings.TrimPrefix(body.Ref, "refs/heads/")
				files := repo.snapshotFor(body.SHA)
				cp := make(map[string]string, len(files))
				for k, v := range files {
					cp[k] = v
				}
				repo.BranchFile[branch] = cp
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"ref":    body.Ref,
				"object": map[string]string{"sha": body.SHA, "type": "commit"},
			}, s)
			return
		}
	}
	writeErr(http.StatusNotFound, "not found")
}

func (s *Store) restLabels(w http.ResponseWriter, r *http.Request, repo *Repo, writeErr func(int, string)) {
	switch r.Method {
	case http.MethodGet:
		outs := make([]*Label, 0, len(repo.LabelOrder))
		for _, n := range repo.LabelOrder {
			outs = append(outs, repo.Labels[n])
		}
		writeJSON(w, http.StatusOK, outs, s)
	case http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Color       string `json:"color"`
			Description string `json:"description"`
		}
		if err := readJSONBody(r, &body); err != nil {
			writeErr(http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Name == "" {
			writeErr(http.StatusBadRequest, "name is required")
			return
		}
		if _, exists := repo.Labels[body.Name]; exists {
			writeErr(422, "Label already exists")
			return
		}
		l := &Label{Name: body.Name, Color: body.Color, Description: body.Description}
		if l.Color == "" {
			l.Color = "ededed"
		}
		repo.Labels[l.Name] = l
		repo.LabelOrder = append(repo.LabelOrder, l.Name)
		writeJSON(w, http.StatusCreated, l, s)
	default:
		writeErr(http.StatusNotFound, "not found")
	}
}

func (s *Store) restIssues(w http.ResponseWriter, r *http.Request, repo *Repo, rest []string, base string, writeErr func(int, string)) {
	// /issues
	if len(rest) == 0 {
		if r.Method == http.MethodGet {
			q := r.URL.Query()
			state := q.Get("state")
			if state == "" {
				state = "open"
			}
			sortOrder := q.Get("sort")
			direction := q.Get("direction")
			if direction == "" {
				direction = "desc"
			}
			var list []*Issue
			for _, is := range repo.Issues {
				if state == "all" || is.State == state {
					list = append(list, is)
				}
			}
			if sortOrder == "created" && direction == "asc" {
				sort.Slice(list, func(i, j int) bool { return list[i].Number < list[j].Number })
			} else {
				sort.Slice(list, func(i, j int) bool { return list[i].Number > list[j].Number })
			}
			outs := make([]issueOut, 0, len(list))
			for _, is := range list {
				outs = append(outs, s.issueJSON(repo, is, base))
			}
			writeJSON(w, http.StatusOK, paginate(w, r, outs), s)
			return
		}
		if r.Method == http.MethodPost {
			var raw map[string]any
			if err := readJSONBody(r, &raw); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			title, _ := raw["title"].(string)
			body, _ := raw["body"].(string)
			if title == "" {
				writeErr(http.StatusBadRequest, "title is required")
				return
			}
			var labels []*Label
			for _, ln := range parseLabelNames(raw["labels"]) {
				l, ok := repo.Labels[ln]
				if !ok {
					writeErr(422, "Validation Failed: label does not exist")
					return
				}
				labels = append(labels, l)
			}
			_, now := s.tick()
			repo.IssueSeq++
			is := &Issue{
				ID: s.nextID(), Number: repo.IssueSeq, Title: title, Body: body,
				State: "open", Labels: labels, CreatedAt: now, UpdatedAt: now, repo: repo,
			}
			if is.Labels == nil {
				is.Labels = []*Label{}
			}
			repo.Issues = append(repo.Issues, is)
			repo.IssueByNum[is.Number] = is
			writeJSON(w, http.StatusCreated, s.issueJSON(repo, is, base), s)
			return
		}
		writeErr(http.StatusNotFound, "not found")
		return
	}
	// /issues/{n}[/comments]
	num, err := strconv.Atoi(rest[0])
	if err != nil {
		writeErr(http.StatusNotFound, "not found")
		return
	}
	is, ok := repo.IssueByNum[num]
	if !ok {
		writeErr(http.StatusNotFound, "Not Found")
		return
	}
	if len(rest) == 1 {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, s.issueJSON(repo, is, base), s)
			return
		}
		if r.Method == http.MethodPatch {
			var body struct {
				State *string `json:"state"`
				Title *string `json:"title"`
				Body  *string `json:"body"`
			}
			if err := readJSONBody(r, &body); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			if body.State != nil {
				is.State = *body.State
			}
			if body.Title != nil {
				is.Title = *body.Title
			}
			if body.Body != nil {
				is.Body = *body.Body
			}
			_, now := s.tick()
			is.UpdatedAt = now
			writeJSON(w, http.StatusOK, s.issueJSON(repo, is, base), s)
			return
		}
		writeErr(http.StatusNotFound, "not found")
		return
	}
	if len(rest) == 2 && rest[1] == "comments" {
		if r.Method == http.MethodGet {
			outs := make([]commentOut, 0, len(is.Comments))
			for _, c := range is.Comments {
				outs = append(outs, commentOut{ID: c.ID, Body: c.Body, User: c.User, CreatedAt: c.CreatedAt, UpdatedAt: c.CreatedAt})
			}
			writeJSON(w, http.StatusOK, outs, s)
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				Body string `json:"body"`
			}
			if err := readJSONBody(r, &body); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			_, now := s.tick()
			c := &Comment{ID: s.nextID(), Body: body.Body, CreatedAt: now, User: s.viewer}
			is.Comments = append(is.Comments, c)
			writeJSON(w, http.StatusCreated, commentOut{ID: c.ID, Body: c.Body, User: c.User, CreatedAt: c.CreatedAt, UpdatedAt: c.CreatedAt}, s)
			return
		}
	}
	writeErr(http.StatusNotFound, "not found")
}

func (s *Store) restPulls(w http.ResponseWriter, r *http.Request, repo *Repo, rest []string, base string, writeErr func(int, string)) {
	if len(rest) == 0 {
		if r.Method == http.MethodGet {
			state := r.URL.Query().Get("state")
			if state == "" {
				state = "open"
			}
			var list []*Pull
			for _, pr := range repo.Pulls {
				if state == "all" || pr.State == state {
					list = append(list, pr)
				}
			}
			outs := make([]pullOut, 0, len(list))
			for _, pr := range list {
				outs = append(outs, s.pullJSON(repo, pr, base))
			}
			writeJSON(w, http.StatusOK, paginate(w, r, outs), s)
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				Title string `json:"title"`
				Body  string `json:"body"`
				Head  string `json:"head"`
				Base  string `json:"base"`
			}
			if err := readJSONBody(r, &body); err != nil {
				writeErr(http.StatusBadRequest, "invalid JSON")
				return
			}
			if body.Title == "" || body.Head == "" || body.Base == "" {
				writeErr(http.StatusBadRequest, "title, head and base are required")
				return
			}
			headSHA, okH := repo.Refs["refs/heads/"+body.Head]
			baseSHA, okB := repo.Refs["refs/heads/"+body.Base]
			if !okH || !okB {
				writeErr(422, "Validation Failed: head or base branch does not exist")
				return
			}
			_, now := s.tick()
			repo.PullSeq++
			pr := &Pull{
				ID: s.nextID(), Number: repo.PullSeq, Title: body.Title, Body: body.Body,
				HeadRef: body.Head, BaseRef: body.Base, HeadSHA: headSHA, BaseSHA: baseSHA,
				State: "open", CreatedAt: now, UpdatedAt: now, repo: repo,
			}
			repo.Pulls = append(repo.Pulls, pr)
			repo.PullByNum[pr.Number] = pr
			writeJSON(w, http.StatusCreated, s.pullJSON(repo, pr, base), s)
			return
		}
		writeErr(http.StatusNotFound, "not found")
		return
	}
	num, err := strconv.Atoi(rest[0])
	if err != nil {
		writeErr(http.StatusNotFound, "not found")
		return
	}
	pr, ok := repo.PullByNum[num]
	if !ok {
		writeErr(http.StatusNotFound, "Not Found")
		return
	}
	if len(rest) == 1 {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, s.pullJSON(repo, pr, base), s)
			return
		}
		writeErr(http.StatusNotFound, "not found")
		return
	}
	if len(rest) == 2 && rest[1] == "files" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, repo.pullFiles(pr), s)
		return
	}
	if len(rest) == 2 && rest[1] == "merge" && r.Method == http.MethodPut {
		if pr.Merged {
			writeErr(405, "Pull request already merged")
			return
		}
		sha, ok := s.mergePull(repo, pr)
		if !ok {
			writeErr(405, "Merge conflict")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sha": sha, "merged": true, "message": "Pull request successfully merged",
		}, s)
		return
	}
	writeErr(http.StatusNotFound, "not found")
}

func (s *Store) restContents(w http.ResponseWriter, r *http.Request, repo *Repo, rest []string, base string, writeErr func(int, string)) {
	path := strings.Join(rest, "/")
	branch := r.URL.Query().Get("ref")
	if branch == "" {
		branch = repo.DefaultBranch
	}
	// Resolve the branch's file map; an unborn repo has none.
	files, branchExists := repo.BranchFile[branch]
	if !branchExists {
		// fall back to the default branch's files when ref names it
		files = repo.BranchFile[repo.DefaultBranch]
	}

	switch r.Method {
	case http.MethodGet:
		if len(repo.Refs) == 0 {
			writeErr(http.StatusNotFound, "Not Found")
			return
		}
		if path == "" {
			// root directory listing
			writeJSON(w, http.StatusOK, s.dirListing(repo, files, "", base), s)
			return
		}
		if blobOID, ok := files[path]; ok {
			content := repo.blobContent(blobOID)
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "file", "encoding": "base64", "size": len(content),
				"name": lastSegment(path), "path": path, "sha": blobOID,
				"content": base64.StdEncoding.EncodeToString(content),
			}, s)
			return
		}
		// directory?
		prefix := path + "/"
		found := false
		for p := range files {
			if strings.HasPrefix(p, prefix) {
				found = true
				break
			}
		}
		if found {
			writeJSON(w, http.StatusOK, s.dirListing(repo, files, path, base), s)
			return
		}
		writeErr(http.StatusNotFound, "Not Found")
	case http.MethodPut:
		var body struct {
			Message string `json:"message"`
			Content string `json:"content"`
			SHA     string `json:"sha"`
			Branch  string `json:"branch"`
		}
		if err := readJSONBody(r, &body); err != nil {
			writeErr(http.StatusBadRequest, "invalid JSON")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			writeErr(http.StatusBadRequest, "content is not valid base64")
			return
		}
		targetBranch := branch
		if body.Branch != "" {
			targetBranch = body.Branch
		}
		targetFiles, exists := repo.BranchFile[targetBranch]
		if !exists {
			// create the branch from the default tip (or empty for an unborn repo)
			if defTip, ok := repo.Refs["refs/heads/"+repo.DefaultBranch]; ok {
				repo.Refs["refs/heads/"+targetBranch] = defTip
				cp := make(map[string]string, len(repo.snapshotFor(defTip)))
				for k, v := range repo.snapshotFor(defTip) {
					cp[k] = v
				}
				repo.BranchFile[targetBranch] = cp
				targetFiles = cp
			} else {
				targetFiles = make(map[string]string)
				repo.BranchFile[targetBranch] = targetFiles
			}
		}
		blobOID := repo.putObject("blob", raw)
		current, fileExists := targetFiles[path]
		status := http.StatusCreated
		if fileExists {
			if body.SHA == "" || body.SHA != current {
				writeErr(http.StatusConflict, "sha does not match")
				return
			}
			status = http.StatusOK
		} else if body.SHA != "" {
			writeErr(http.StatusConflict, "sha does not match")
			return
		}
		next := make(map[string]string, len(targetFiles)+1)
		for k, v := range targetFiles {
			next[k] = v
		}
		next[path] = blobOID
		msg := body.Message
		if msg == "" {
			msg = "Update " + path
		}
		commitOID := s.createCommitOnBranch(repo, targetBranch, msg, next)
		writeJSON(w, status, map[string]any{
			"content": map[string]any{
				"type": "file", "encoding": "base64", "size": len(raw),
				"name": lastSegment(path), "path": path, "sha": blobOID,
				"content": body.Content,
			},
			"commit": map[string]string{"sha": commitOID, "message": msg},
		}, s)
	case http.MethodDelete:
		var body struct {
			Message string `json:"message"`
			SHA     string `json:"sha"`
			Branch  string `json:"branch"`
		}
		if err := readJSONBody(r, &body); err != nil {
			writeErr(http.StatusBadRequest, "invalid JSON")
			return
		}
		targetBranch := branch
		if body.Branch != "" {
			targetBranch = body.Branch
		}
		targetFiles, exists := repo.BranchFile[targetBranch]
		if !exists {
			writeErr(http.StatusNotFound, "Not Found")
			return
		}
		current, fileExists := targetFiles[path]
		if !fileExists {
			writeErr(http.StatusNotFound, "Not Found")
			return
		}
		if body.SHA != "" && body.SHA != current {
			writeErr(http.StatusConflict, "sha does not match")
			return
		}
		next := make(map[string]string, len(targetFiles))
		for k, v := range targetFiles {
			if k != path {
				next[k] = v
			}
		}
		msg := body.Message
		if msg == "" {
			msg = "Delete " + path
		}
		commitOID := s.createCommitOnBranch(repo, targetBranch, msg, next)
		writeJSON(w, http.StatusOK, map[string]any{
			"commit": map[string]string{"sha": commitOID, "message": msg},
		}, s)
	default:
		writeErr(http.StatusNotFound, "not found")
	}
}

// dirListing lists the immediate children of dir ("" = root) in a file map.
func (s *Store) dirListing(repo *Repo, files map[string]string, dir string, base string) []map[string]any {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	children := make(map[string]bool)
	isDir := make(map[string]bool)
	for p := range files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		seg := rest
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			seg = rest[:idx]
			isDir[seg] = true
		}
		children[seg] = true
	}
	names := make([]string, 0, len(children))
	for n := range children {
		names = append(names, n)
	}
	sort.Strings(names)
	dirOIDs := repo.treeOIDsForDir(files)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		full := prefix + n
		if isDir[n] {
			out = append(out, map[string]any{
				"type": "dir", "name": n, "path": full, "sha": dirOIDs[full],
			})
		} else {
			content := repo.blobContent(files[full])
			out = append(out, map[string]any{
				"type": "file", "name": n, "path": full, "sha": files[full], "size": len(content),
			})
		}
	}
	return out
}

func lastSegment(p string) string {
	if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
		return p[idx+1:]
	}
	return p
}
