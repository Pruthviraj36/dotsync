package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Pruthviraj36/dotsync/internal/auth"
)

func serviceTokenRequest(tokenID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/projects/demo/envs/dev/pull", nil)
	ctx := context.WithValue(r.Context(), auth.UserClaimsKey, &auth.Claims{
		UserID:   "creator-user",
		Username: "service-token:ci",
	})
	ctx = context.WithValue(ctx, serviceTokenKey{}, serviceTokenMeta{
		ID:        tokenID,
		ProjectID: "project-1",
		Env:       "dev",
	})
	return r.WithContext(ctx)
}

func userRequest(userID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/projects/demo/envs/dev/pull", nil)
	ctx := context.WithValue(r.Context(), auth.UserClaimsKey, &auth.Claims{UserID: userID})
	return r.WithContext(ctx)
}

// Two CI tokens created by the same user must not share a rate-limit bucket:
// before this, every service token inherited the creator's synthetic claims and
// parallel pipelines throttled each other.
func TestRateLimitByUserSeparatesServiceTokens(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	limited := RateLimitByUser(1, time.Minute)(next)

	do := func(r *http.Request) int {
		rec := httptest.NewRecorder()
		limited.ServeHTTP(rec, r)
		return rec.Code
	}

	if code := do(serviceTokenRequest("token-a")); code != http.StatusOK {
		t.Fatalf("token-a first request: got %d, want 200", code)
	}
	if code := do(serviceTokenRequest("token-a")); code != http.StatusTooManyRequests {
		t.Fatalf("token-a second request: got %d, want 429", code)
	}
	if code := do(serviceTokenRequest("token-b")); code != http.StatusOK {
		t.Fatalf("token-b should have its own bucket: got %d, want 200", code)
	}
	if code := do(userRequest("creator-user")); code != http.StatusOK {
		t.Fatalf("the creating user should have a separate bucket: got %d, want 200", code)
	}
}

func TestServiceTokenAccessors(t *testing.T) {
	r := serviceTokenRequest("token-a")
	if got := ServiceTokenID(r.Context()); got != "token-a" {
		t.Fatalf("ServiceTokenID = %q, want token-a", got)
	}
	if got := ServiceTokenProjectID(r.Context()); got != "project-1" {
		t.Fatalf("ServiceTokenProjectID = %q, want project-1", got)
	}
	if got := ServiceTokenEnv(r.Context()); got != "dev" {
		t.Fatalf("ServiceTokenEnv = %q, want dev", got)
	}
	if IsServiceTokenRequest(r.Context()) != true {
		t.Fatal("IsServiceTokenRequest should be true for a service-token context")
	}

	plain := userRequest("u1")
	if ServiceTokenID(plain.Context()) != "" || IsServiceTokenRequest(plain.Context()) {
		t.Fatal("a user request must not be treated as a service token")
	}
}
