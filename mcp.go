package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// ---------------------------------------------------------------- tool definitions

type mcpToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func mcpTools() []mcpToolDef {
	return []mcpToolDef{
		{
			Name:        "create_repo",
			Description: "Create a repository owned by the authenticated user.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        strProp("Repository name."),
					"description": strProp("Repository description."),
					"private":     boolProp("Whether the repository is private."),
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "get_repo",
			Description: "Get a repository by owner and name.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"owner": strProp("Repository owner login."),
					"repo":  strProp("Repository name."),
				},
				"required": []string{"owner", "repo"},
			},
		},
		{
			Name:        "create_issue",
			Description: "Create an issue in a repository.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"owner": strProp("Repository owner login."),
					"repo":  strProp("Repository name."),
					"title": strProp("Issue title."),
					"body":  strProp("Issue body."),
				},
				"required": []string{"owner", "repo", "title"},
			},
		},
		{
			Name:        "list_issues",
			Description: "List issues in a repository.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"owner": strProp("Repository owner login."),
					"repo":  strProp("Repository name."),
					"state": strProp("Issue state filter: open, closed or all."),
				},
				"required": []string{"owner", "repo"},
			},
		},
		{
			Name:        "create_pull_request",
			Description: "Create a pull request in a repository.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"owner": strProp("Repository owner login."),
					"repo":  strProp("Repository name."),
					"title": strProp("Pull request title."),
					"body":  strProp("Pull request body."),
					"head":  strProp("Head branch name."),
					"base":  strProp("Base branch name."),
				},
				"required": []string{"owner", "repo", "title", "head", "base"},
			},
		},
		{
			Name:        "merge_pull_request",
			Description: "Merge a pull request with a squash merge.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"owner":       strProp("Repository owner login."),
					"repo":        strProp("Repository name."),
					"pull_number": map[string]any{"type": "integer", "description": "Pull request number."},
				},
				"required": []string{"owner", "repo", "pull_number"},
			},
		},
	}
}

// ---------------------------------------------------------------- JSON-RPC helpers

func rpcResult(w http.ResponseWriter, id any, result any) {
	writeJSONRaw(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func rpcError(w http.ResponseWriter, status int, id any, code int, message string) {
	writeJSONRaw(w, status, map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

func writeJSONRaw(w http.ResponseWriter, status int, v any) {
	data, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}

func toolResultJSON(obj any) map[string]any {
	text, _ := json.Marshal(obj)
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": obj,
		"isError":           false,
	}
}

func toolErrorJSON(message string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": true,
	}
}

// ---------------------------------------------------------------- tool execution

func (s *Store) mcpCallTool(name string, args map[string]any, base string) (any, bool) {
	// returns (result, isError)
	strArg := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	numArg := func(k string) (int, bool) {
		switch v := args[k].(type) {
		case float64:
			return int(v), true
		case string:
			n, err := strconv.Atoi(v)
			return n, err == nil
		case int64:
			return int(v), true
		}
		return 0, false
	}
	switch name {
	case "create_repo":
		repoName := strArg("name")
		if repoName == "" {
			return toolErrorJSON("missing required argument: name"), true
		}
		repo, ok := s.createRepo(repoName, args["private"] == true, strArg("description"))
		if !ok {
			return toolErrorJSON("repository already exists: " + repoName), true
		}
		return toolResultJSON(s.repoJSON(repo, base)), false
	case "get_repo":
		repo := s.getRepo(strArg("owner"), strArg("repo"))
		if repo == nil {
			return toolErrorJSON("repository not found"), true
		}
		return toolResultJSON(s.repoJSON(repo, base)), false
	case "create_issue":
		repo := s.getRepo(strArg("owner"), strArg("repo"))
		if repo == nil {
			return toolErrorJSON("repository not found"), true
		}
		title := strArg("title")
		if title == "" {
			return toolErrorJSON("missing required argument: title"), true
		}
		_, now := s.tick()
		repo.IssueSeq++
		is := &Issue{
			ID: s.nextID(), Number: repo.IssueSeq, Title: title, Body: strArg("body"),
			State: "open", Labels: []*Label{}, CreatedAt: now, UpdatedAt: now, repo: repo,
		}
		repo.Issues = append(repo.Issues, is)
		repo.IssueByNum[is.Number] = is
		return toolResultJSON(s.issueJSON(repo, is, base)), false
	case "list_issues":
		repo := s.getRepo(strArg("owner"), strArg("repo"))
		if repo == nil {
			return toolErrorJSON("repository not found"), true
		}
		state := strArg("state")
		if state == "" {
			state = "open"
		}
		outs := []any{}
		for _, is := range repo.Issues {
			if state == "all" || is.State == state {
				outs = append(outs, s.issueJSON(repo, is, base))
			}
		}
		return toolResultJSON(map[string]any{"issues": outs}), false
	case "create_pull_request":
		repo := s.getRepo(strArg("owner"), strArg("repo"))
		if repo == nil {
			return toolErrorJSON("repository not found"), true
		}
		title, head, baseRef := strArg("title"), strArg("head"), strArg("base")
		if title == "" || head == "" || baseRef == "" {
			return toolErrorJSON("missing required argument: title, head or base"), true
		}
		headSHA, okH := repo.Refs["refs/heads/"+head]
		baseSHA, okB := repo.Refs["refs/heads/"+baseRef]
		if !okH || !okB {
			return toolErrorJSON("head or base branch does not exist"), true
		}
		_, now := s.tick()
		repo.PullSeq++
		pr := &Pull{
			ID: s.nextID(), Number: repo.PullSeq, Title: title, Body: strArg("body"),
			HeadRef: head, BaseRef: baseRef, HeadSHA: headSHA, BaseSHA: baseSHA,
			State: "open", CreatedAt: now, UpdatedAt: now, repo: repo,
		}
		repo.Pulls = append(repo.Pulls, pr)
		repo.PullByNum[pr.Number] = pr
		return toolResultJSON(s.pullJSON(repo, pr, base)), false
	case "merge_pull_request":
		repo := s.getRepo(strArg("owner"), strArg("repo"))
		if repo == nil {
			return toolErrorJSON("repository not found"), true
		}
		num, ok := numArg("pull_number")
		if !ok {
			return toolErrorJSON("missing required argument: pull_number"), true
		}
		pr := repo.PullByNum[num]
		if pr == nil {
			return toolErrorJSON("pull request not found"), true
		}
		if pr.Merged {
			return toolErrorJSON("pull request already merged"), true
		}
		if _, ok := s.mergePull(repo, pr); !ok {
			return toolErrorJSON("merge conflict"), true
		}
		return toolResultJSON(s.pullJSON(repo, pr, base)), false
	}
	return nil, false // unknown tool (handled by caller)
}

// ---------------------------------------------------------------- HTTP entry

func (s *Store) mcpHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONRaw(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := "http://" + r.Host
	body := readBody(r)
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		rpcError(w, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	id, hasID := raw["id"]
	method, _ := raw["method"].(string)
	params, _ := raw["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}

	// Notifications (no id) never carry a result.
	if !hasID {
		if method == "notifications/initialized" || method == "notifications/cancelled" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch method {
	case "server/discover":
		serverInfo := map[string]any{"name": "github-core-clone-forge", "version": "0.3.0-go"}
		rpcResult(w, id, map[string]any{
			"resultType":      "DiscoverResult",
			"supportedVersions": []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"serverInfo":        serverInfo,
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": serverInfo,
			},
		})
	case "initialize":
		version := "2025-11-25"
		if v, ok := params["protocolVersion"].(string); ok && v != "" {
			version = v
		}
		rpcResult(w, id, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "github-core-clone-forge", "version": "0.3.0-go"},
		})
	case "ping":
		rpcResult(w, id, map[string]any{})
	case "tools/list":
		rpcResult(w, id, map[string]any{"tools": mcpTools()})
	case "tools/call":
		name, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		known := false
		for _, t := range mcpTools() {
			if t.Name == name {
				known = true
				break
			}
		}
		if !known {
			rpcError(w, http.StatusOK, id, -32601, fmt.Sprintf("unknown tool: %s", name))
			return
		}
		result, _ := s.mcpCallTool(name, args, base)
		rpcResult(w, id, result)
	default:
		rpcError(w, http.StatusOK, id, -32601, fmt.Sprintf("method not found: %s", method))
	}
}
