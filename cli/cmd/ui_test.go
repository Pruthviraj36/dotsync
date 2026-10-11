package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUISessionGuard(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	guard := uiSessionGuard("session", "4040", next)

	tests := []struct {
		name   string
		host   string
		path   string
		header string
		status int
	}{
		{name: "page allows loopback host", host: "127.0.0.1:4040", path: "/", status: http.StatusNoContent},
		{name: "api requires session", host: "127.0.0.1:4040", path: "/api/me", status: http.StatusForbidden},
		{name: "api accepts session", host: "127.0.0.1:4040", path: "/api/me", header: "session", status: http.StatusNoContent},
		{name: "wrong host rejected", host: "evil.test:4040", path: "/", status: http.StatusForbidden},
		{name: "cross site rejected", host: "127.0.0.1:4040", path: "/", header: "session", status: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+test.host+test.path, nil)
			req.Host = test.host
			if test.header != "" {
				req.Header.Set("X-DotSync-Session", test.header)
			}
			if test.name == "cross site rejected" {
				req.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			res := httptest.NewRecorder()
			guard.ServeHTTP(res, req)
			if res.Code != test.status {
				t.Fatalf("status = %d, want %d", res.Code, test.status)
			}
		})
	}
}
