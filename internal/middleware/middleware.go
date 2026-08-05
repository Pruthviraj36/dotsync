package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/httprate"
	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/crypto"
	"github.com/Pruthviraj36/dotsync/internal/service"
)

// serviceTokenKey is the unexported context key for service token metadata.
type serviceTokenKey struct{}

// serviceTokenMeta holds the minimal fields downstream handlers need from a
// service token request — avoids importing model here and keeps context values simple.
type serviceTokenMeta struct {
	Env      string // "dev" | "staging" | "production" | "*" (all)
	ReadOnly bool   // always true for service tokens
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
				ctx := context.WithValue(r.Context(), auth.UserClaimsKey, claims)
				ctx = context.WithValue(ctx, serviceTokenKey{}, serviceTokenMeta{
					Env:      st.Env,
					ReadOnly: true,
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

// VerifyHMAC checks the X-DotSync-Signature header on CLI requests.
// Service token requests skip HMAC verification — they authenticate by token
// value alone (the token is already a secret with sufficient entropy).
func VerifyHMAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Service tokens don't sign request bodies — they prove identity by
		// possession of the token itself.
		if IsServiceTokenRequest(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}

		sig := r.Header.Get("X-DotSync-Signature")
		if sig == "" {
			writeError(w, http.StatusBadRequest, "missing X-DotSync-Signature header")
			return
		}

		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")

		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB max
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read body")
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		if !crypto.HMACVerify([]byte(tokenStr), body, sig) {
			writeError(w, http.StatusUnauthorized, "invalid request signature")
			return
		}

		next.ServeHTTP(w, r)
	})
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
