package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/httprate"
	"github.com/yourusername/dotsync/internal/auth"
	"github.com/yourusername/dotsync/internal/crypto"
)

// JSON error helper
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Authenticate validates the Bearer JWT on every protected route.
func Authenticate(authSvc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			tokenStr := strings.TrimPrefix(header, "Bearer ")
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

// VerifyHMAC checks the X-DotSync-Signature header on CLI requests.
// The CLI signs the request body with the user's HMAC secret (derived from access token).
func VerifyHMAC(hmacSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sig := r.Header.Get("X-DotSync-Signature")
			if sig == "" {
				writeError(w, http.StatusBadRequest, "missing X-DotSync-Signature header")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB max
			if err != nil {
				writeError(w, http.StatusBadRequest, "failed to read body")
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))

			if !crypto.HMACVerify(hmacSecret, body, sig) {
				writeError(w, http.StatusUnauthorized, "invalid request signature")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
