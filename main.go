package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------------------------------------------------------------- HTTP helpers

func readBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return nil
	}
	return body
}

// writeJSON writes a JSON response with the request-id header every REST reply needs.
func writeJSON(w http.ResponseWriter, status int, v any, s *Store) {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(`{"message":"internal error"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	if s != nil {
		w.Header().Set("X-GitHub-Request-Id", s.nextReqID())
	}
	w.WriteHeader(status)
	w.Write(data)
}

// ---------------------------------------------------------------- router + auth

func (s *Store) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+s.token
}

func (s *Store) handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/healthz" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true}, s)
		return
	}
	// Protected surfaces: REST, GraphQL, MCP, git smart HTTP.
	protected := strings.HasPrefix(path, "/api/v3") || path == "/api/graphql" ||
		path == "/mcp" || strings.Contains(path, ".git/")
	if protected && !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"}, s)
		return
	}
	switch {
	case path == "/api/graphql":
		s.graphqlHTTP(w, r)
	case path == "/mcp":
		s.mcpHTTP(w, r)
	case strings.HasPrefix(path, "/api/v3"):
		s.restHTTP(w, r)
	default:
		// /{owner}/{repo}.git/...
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(segs) >= 2 && strings.HasSuffix(segs[1], ".git") {
			owner := segs[0]
			repo := strings.TrimSuffix(segs[1], ".git")
			s.gitHTTP(w, r, owner, repo)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"}, s)
	}
}

// ---------------------------------------------------------------- lifecycle

func main() {
	token := os.Getenv("MAKEFUN_SHOP_TOKEN")
	envURL := strings.TrimRight(os.Getenv("MAKEFUN_ENV_URL"), "/")
	store := newStore(token)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[forge] listen: %v\n", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Fprintf(os.Stderr, "[forge] viewer=%s port=%d\n", store.viewer.Login, port)

	server := &http.Server{Handler: http.HandlerFunc(store.handler)}
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "[forge] serve: %v\n", err)
			os.Exit(1)
		}
	}()

	if envURL == "" {
		// No env: serve forever (local development).
		select {}
	}

	if err := register(envURL, port); err != nil {
		fmt.Fprintf(os.Stderr, "[forge] register: %v\n", err)
		os.Exit(1)
	}
	waitForDone(envURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
	os.Exit(0)
}

func register(envURL string, port int) error {
	client := &http.Client{Timeout: 30 * time.Second}
	body, _ := json.Marshal(map[string]int{"port": port})
	delay := 250 * time.Millisecond
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequest(http.MethodPost, envURL+"/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 || res.StatusCode == 409 {
				return nil
			}
			if res.StatusCode == 400 || res.StatusCode == 405 {
				return fmt.Errorf("register answered HTTP %d", res.StatusCode)
			}
		}
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		time.Sleep(delay)
		delay *= 2
		if attempt > 100 {
			return fmt.Errorf("register never succeeded")
		}
	}
}

func waitForDone(envURL string) {
	client := &http.Client{Timeout: 30 * time.Second}
	delay := 250 * time.Millisecond
	for {
		res, err := client.Get(envURL + "/done")
		if err == nil {
			var payload struct {
				Done bool `json:"done"`
			}
			if err := json.NewDecoder(res.Body).Decode(&payload); err == nil && payload.Done {
				res.Body.Close()
				return
			}
			res.Body.Close()
		}
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		time.Sleep(delay)
		delay *= 2
	}
}
