package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/crypto"
	"github.com/Pruthviraj36/dotsync/internal/service"
	"github.com/go-chi/httprate"
)

// serviceTokenKey is the unexported context key for service token metadata.
type serviceTokenKey struct{}

// serviceTokenMeta holds the minimal fields downstream handlers need from a
// service token request — avoids importing model here and keeps context values simple.
type serviceTokenMeta struct {
	ProjectID string
	Env       string // "dev" | "staging" | "production" | "*" (all)
}

// JSON error helper
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Authenticate validates the Bearer token on every protected route.
// It accepts two token formats:
//
//   - JWT access tokens (issued at login): validated by authSvc.
//   - Service tokens (issued for CI/CD): prefixed with "dst_", looked up by
//     SHA-256 hash in the database. They inject synthetic Claims so all
//     downstream handlers work unmodified, but are read-only (Push rejects them)
//     and scoped to a specific environment (or "*" for all envs).
func Authenticate(authSvc *auth.Service, tokenSvc *service.ServiceTokenService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			tokenStr := strings.TrimPrefix(header, "Bearer ")

			// Service tokens are prefixed "dst_" for fast-path detection without
			// attempting JWT parsing first.
			if strings.HasPrefix(tokenStr, "dst_") {
				st, err := tokenSvc.LookupByRawToken(r.Context(), tokenStr)
				if err != nil {
					writeError(w, http.StatusUnauthorized, "invalid service token")
					return
				}
				// Synthetic Claims — UserID is the creator's ID so audit logs
				// attribute actions to the right human; Username records that this
				// was a token-based request.
				claims := &auth.Claims{
					UserID:   st.CreatedBy,
					Username: "service-token:" + st.Name,
					Plan:     "free",
				}
				if !serviceTokenRouteAllowed(r) {
					writeError(w, http.StatusForbidden, "service tokens are read-only and limited to secret retrieval")
					return
				}
				ctx := context.WithValue(r.Context(), auth.UserClaimsKey, claims)
				ctx = context.WithValue(ctx, serviceTokenKey{}, serviceTokenMeta{
					ProjectID: st.ProjectID,
					Env:       st.Env,
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Standard JWT path
			claims, err := authSvc.ValidateAccessToken(tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), auth.UserClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// IsServiceTokenRequest returns true when the request was authenticated with a
// service token rather than a user JWT.
func IsServiceTokenRequest(ctx context.Context) bool {
	_, ok := ctx.Value(serviceTokenKey{}).(serviceTokenMeta)
	return ok
}

// ServiceTokenEnv returns the environment scope of the service token ("*" for all),
// or "" for normal user JWT requests.
func ServiceTokenEnv(ctx context.Context) string {
	if meta, ok := ctx.Value(serviceTokenKey{}).(serviceTokenMeta); ok {
		return meta.Env
	}
	return ""
}

// ServiceTokenProjectID returns the project bound to a service token, or ""
// for a normal user request.
func ServiceTokenProjectID(ctx context.Context) string {
	if meta, ok := ctx.Value(serviceTokenKey{}).(serviceTokenMeta); ok {
		return meta.ProjectID
	}
	return ""
}

func serviceTokenRouteAllowed(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	path := r.URL.Path
	return strings.HasSuffix(path, "/pull") ||
		strings.HasSuffix(path, "/pull/version") ||
		strings.HasSuffix(path, "/history") ||
		strings.HasSuffix(path, "/password")
}

// RateLimitByIP rate-limits per remote IP address.
func RateLimitByIP(limit int, window time.Duration) func(http.Handler) http.Handler {
	return httprate.Limit(limit, window, httprate.WithKeyFuncs(httprate.KeyByIP))
}

// RateLimitByUser rate-limits per authenticated user ID.
func RateLimitByUser(limit int, window time.Duration) func(http.Handler) http.Handler {
	return httprate.Limit(limit, window, httprate.WithKeyFuncs(func(r *http.Request) (string, error) {
		claims := auth.ClaimsFromCtx(r.Context())
		if claims != nil {
			return "user:" + claims.UserID, nil
		}
		return httprate.KeyByIP(r)
	}))
}

// LimitBody bounds request payloads before handlers decode JSON or allocate
// encrypted secret blobs. The limit includes base64 JSON overhead.
func LimitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets standard security headers on every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// RequestID injects a unique request ID for tracing.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := crypto.GenerateRandomToken(8)
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), "request_id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
