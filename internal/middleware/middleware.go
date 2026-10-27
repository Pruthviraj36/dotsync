package middleware

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/crypto"
	"github.com/Pruthviraj36/dotsync/internal/service"
	"github.com/go-chi/httprate"
)

// ctxKey is unexported so no other package can collide with our context keys.
type ctxKey int

const requestIDKey ctxKey = iota

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

			tokenStr := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			if tokenStr == "" {
				writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			// Service tokens are prefixed "dst_" for fast-path detection without
			// attempting JWT parsing first.
			if strings.HasPrefix(tokenStr, "dst_") {
				// Reject malformed service tokens before touching the database —
				// the prefix is always followed by 64 hex characters.
				if !validServiceTokenFormat(tokenStr) {
					writeError(w, http.StatusUnauthorized, "invalid service token")
					return
				}
				if !serviceTokenRouteAllowed(r) {
					writeError(w, http.StatusForbidden, "service tokens are read-only and limited to secret retrieval")
					return
				}
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

var serviceTokenFormat = regexp.MustCompile(`^dst_[0-9a-f]{64}$`)

func validServiceTokenFormat(tok string) bool {
	return serviceTokenFormat.MatchString(tok)
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

// serviceTokenPath matches exactly the read-only endpoints a CI token may use.
// Matching the whole path (not a suffix) prevents a token from reaching a new
// endpoint that merely happens to end in "/history" or "/password".
var serviceTokenPath = regexp.MustCompile(`^/api/projects/[^/]+/(?:envs/[^/]+/(?:pull|pull/version|history|head)|password)$`)

func serviceTokenRouteAllowed(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return serviceTokenPath.MatchString(r.URL.Path)
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

// RealIP rewrites r.RemoteAddr from X-Forwarded-For / X-Real-IP ONLY when the
// deployment explicitly declares that it sits behind a trusted reverse proxy.
// chi's stock RealIP middleware trusts those headers unconditionally, which lets
// any client spoof its address and evade per-IP rate limits. When trustProxy is
// false (the default) the socket address is authoritative.
func RealIP(trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !trustProxy {
				next.ServeHTTP(w, r)
				return
			}

			// X-Forwarded-For is a comma-separated chain. Each proxy appends the
			// address of the peer it received the request from, so with exactly
			// one trusted proxy in front of us the RIGHT-most entry is the real
			// client while anything to its left was supplied by the client and
			// can be forged. (Taking the left-most value — a common mistake —
			// lets an attacker choose their own rate-limit key.)
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				for i := len(parts) - 1; i >= 0; i-- {
					client := strings.TrimSpace(parts[i])
					if ip := net.ParseIP(client); ip != nil {
						r.RemoteAddr = ip.String()
						break
					}
				}
			} else if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
				if ip := net.ParseIP(xr); ip != nil {
					r.RemoteAddr = ip.String()
				}
			}
			next.ServeHTTP(w, r)
		})
	}
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
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// RequestID injects a unique request ID for tracing.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := crypto.GenerateRandomToken(8)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromCtx returns the request ID injected by RequestID, if any.
func RequestIDFromCtx(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
