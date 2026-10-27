package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Pruthviraj36/dotsync/cli/config"
)

// newTestClient points a Client at an httptest server. Fields are package
// private, which is why this test lives in the api package.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	cfg := &config.GlobalConfig{
		ServerURL:   srv.URL,
		AccessToken: "test-access-token",
	}
	c := &Client{
		baseURL:    srv.URL,
		httpClient: srv.Client(),
		cfg:        cfg,
	}
	return c, srv.Close
}

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
		t.Errorf("Authorization = %q, want bearer test token", got)
	}
}

func writeJSONBody(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// Collections must survive the round trip: a previous one-line return evaluated
// the (nil) result before decodeResponse populated it, which is undefined by the
// spec and a footgun for any future compiler.
func TestListResponsesAreDecoded(t *testing.T) {
	c, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch r.URL.Path {
		case "/api/projects":
			writeJSONBody(t, w, []map[string]any{{"slug": "demo", "name": "Demo"}})
		case "/api/projects/demo/team":
			writeJSONBody(t, w, []map[string]any{{"username": "alice", "role": "owner"}})
		case "/api/projects/demo/audit":
			writeJSONBody(t, w, []map[string]any{{"action": "push"}})
		case "/api/auth/me":
			writeJSONBody(t, w, map[string]any{"username": "alice", "id": "u1"})
		default:
			http.NotFound(w, r)
		}
	})
	defer closeFn()

	projects, err := c.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0]["slug"] != "demo" {
		t.Fatalf("ListProjects = %#v, want one demo project", projects)
	}

	members, err := c.ListTeamMembers("demo")
	if err != nil {
		t.Fatalf("ListTeamMembers: %v", err)
	}
	if len(members) != 1 || members[0]["username"] != "alice" {
		t.Fatalf("ListTeamMembers = %#v, want alice", members)
	}

	logs, err := c.AuditLogs("demo")
	if err != nil {
		t.Fatalf("AuditLogs: %v", err)
	}
	if len(logs) != 1 || logs[0]["action"] != "push" {
		t.Fatalf("AuditLogs = %#v, want one push entry", logs)
	}

	me, err := c.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me["username"] != "alice" {
		t.Fatalf("GetMe = %#v, want username alice", me)
	}
}

func TestDecodeResponseSurfacesServerErrors(t *testing.T) {
	c, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	})
	defer closeFn()

	if _, err := c.ListProjects(); err == nil {
		t.Fatal("expected an error for a 403 response")
	}
}
