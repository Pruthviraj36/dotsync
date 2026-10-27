package main

import (
	"context"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"

	"github.com/Pruthviraj36/dotsync/internal/assets"
	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/handler"
	mw "github.com/Pruthviraj36/dotsync/internal/middleware"
	"github.com/Pruthviraj36/dotsync/internal/service"
)

func main() {
	// Load .env in development — production deployments inject env vars directly.
	_ = godotenv.Load()

	// ── Startup validation ───────────────────────────────────────────────────
	// Fail fast and loudly on missing config. All required vars must be set
	// before any request is accepted. See README.md § Self-hosting for values.
	requireEnv(
		"DATABASE_URL",      // postgres://user:pass@host:5432/dbname
		"JWT_SECRET",        // openssl rand -hex 32
		"GITHUB_CLIENT_ID",  // from https://github.com/settings/developers
		"SERVER_MASTER_KEY", // openssl rand -hex 32  (AES-256 for password encryption)
	)

	// A short JWT secret is brute-forceable offline: anyone who observes one
	// signed token can mount a dictionary attack against HS256. Require at
	// least 32 bytes of key material (64 hex chars, or any 32-char string).
	if len([]byte(mustEnv("JWT_SECRET"))) < 32 {
		log.Fatal("JWT_SECRET must be at least 32 bytes — generate with: openssl rand -hex 32")
	}
	if mustEnv("JWT_SECRET") == mustEnv("SERVER_MASTER_KEY") {
		log.Fatal("JWT_SECRET and SERVER_MASTER_KEY must be different values")
	}

	// ── Database ─────────────────────────────────────────────────────────────
	// DATABASE_URL_DIRECT is used for migrations (DDL/advisory locks don't
	// work reliably through a connection pooler like Neon PgBouncer).
	// Falls back to DATABASE_URL if not set (plain Postgres is fine).
	dsn := mustEnv("DATABASE_URL")
	migrationDSN := getEnv("DATABASE_URL_DIRECT", dsn)

	database, err := db.New(dsn)
	if err != nil {
		log.Fatalf("database connect: %v", err)
	}
	defer database.Close()

	migrationsPath := getEnv("MIGRATIONS_PATH", "./migrations")
	if err := db.RunMigrations(migrationDSN, migrationsPath); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	// ── Services ─────────────────────────────────────────────────────────────
	jwtSecret := mustEnv("JWT_SECRET")
	authSvc := auth.NewService(database, jwtSecret)
	projectSvc := service.NewProjectService(database)
	secretSvc := service.NewSecretService(database)
	teamSvc := service.NewTeamService(database)
	auditSvc := service.NewAuditService(database)
	serviceTokenSvc := service.NewServiceTokenService(database)

	// SERVER_MASTER_KEY must be exactly 64 hex chars (32 bytes / AES-256).
	// Generate: openssl rand -hex 32
	masterKey, err := hex.DecodeString(mustEnv("SERVER_MASTER_KEY"))
	if err != nil || len(masterKey) != 32 {
		log.Fatal("SERVER_MASTER_KEY must be a 64-character hex string — generate with: openssl rand -hex 32")
	}
	passwordSvc := service.NewPasswordService(database, masterKey)

	// ── Handlers ─────────────────────────────────────────────────────────────
	authHandler := handler.NewAuthHandler(authSvc, database)
	projectHandler := handler.NewProjectHandler(projectSvc, teamSvc)
	secretsHandler := handler.NewSecretsHandler(secretSvc, projectSvc, teamSvc, auditSvc)
	teamHandler := handler.NewTeamHandler(projectSvc, teamSvc, database, auditSvc)
	passwordHandler := handler.NewPasswordHandler(passwordSvc, projectSvc, teamSvc, auditSvc)
	identityHandler := handler.NewIdentityHandler(database)
	serviceTokenHandler := handler.NewServiceTokenHandler(serviceTokenSvc, projectSvc, teamSvc, auditSvc)

	// ── Router ───────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Only trust X-Forwarded-For when the operator says a reverse proxy is in
	// front of us. Otherwise every client can spoof its IP and defeat rate
	// limiting by sending a random header on each request.
	r.Use(mw.RealIP(getEnvBool("TRUST_PROXY", false)))
	r.Use(mw.RequestID)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(mw.SecurityHeaders)
	r.Use(mw.LimitBody(4 << 20))

	// CORS — disabled unless FRONTEND_URL is explicitly configured. The old
	// default (http://localhost:3000 with credentials) silently allowed any
	// local process to make credentialed cross-origin calls to production.
	if frontendURL := strings.TrimSpace(os.Getenv("FRONTEND_URL")); frontendURL != "" {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   []string{frontendURL},
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type", "X-DotSync-Signature", "X-Request-ID"},
			AllowCredentials: true,
			MaxAge:           300,
		}))
	}

	// Global per-IP ceiling. Authenticated routes carry tighter per-user limits
	// on top of this. It is configurable because dozens of developers behind one
	// corporate NAT (or a deployment that cannot see client IPs) would otherwise
	// share a single 200/min bucket and throttle each other.
	r.Use(mw.RateLimitByIP(getEnvInt("RATE_LIMIT_PER_MIN", 600), time.Minute))

	// ── Public endpoints ─────────────────────────────────────────────────────

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"dotsync"}`))
	})

	// Install scripts — curl -fsSL https://your-server/install.sh | sh
	installHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-sh; charset=utf-8")
		w.Write(assets.InstallScript)
	}
	r.Get("/install.sh", installHandler)
	r.Get("/install", installHandler)

	r.Get("/install.ps1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(assets.InstallScriptPS1)
	})

	// ── Auth routes ──────────────────────────────────────────────────────────
	// All /api/auth/* must live under this single Route. Chi's radix tree
	// mounts Route("/api/auth") as a sub-router, so registering /auth/me under
	// a separate /api group would be shadowed and return 404.
	r.Route("/api/auth", func(r chi.Router) {
		r.Use(mw.RateLimitByIP(20, time.Minute))
		r.Get("/config", authHandler.Config)
		r.Post("/github/device", authHandler.GitHubDeviceLogin)
		r.Post("/refresh", authHandler.RefreshToken)

		r.Group(func(r chi.Router) {
			r.Use(mw.Authenticate(authSvc, serviceTokenSvc))
			r.Post("/logout", authHandler.Logout)
			r.Get("/me", authHandler.Me)
		})
	})

	// ── Protected routes ─────────────────────────────────────────────────────
	r.Route("/api", func(r chi.Router) {
		r.Use(mw.Authenticate(authSvc, serviceTokenSvc))
		r.Use(mw.RateLimitByUser(300, time.Minute))

		// Identity (ed25519 pubkey for verifying signed pushes)
		r.Put("/me/pubkey", identityHandler.SetPubKey)

		// Projects
		r.Post("/projects", projectHandler.Create)
		r.Get("/projects", projectHandler.List)

		// Team management
		r.Post("/projects/{slug}/team", teamHandler.AddMember)
		r.Get("/projects/{slug}/team", teamHandler.ListMembers)
		r.Delete("/projects/{slug}/team/{username}", teamHandler.RemoveMember)
		r.Patch("/projects/{slug}/team/{username}", teamHandler.UpdateRole)

		// Audit logs (all users, no plan gate)
		r.Get("/projects/{slug}/audit", secretsHandler.AuditLogs)

		// Service tokens (CI/CD integrations)
		r.Post("/projects/{slug}/tokens", serviceTokenHandler.Create)
		r.Get("/projects/{slug}/tokens", serviceTokenHandler.List)
		r.Delete("/projects/{slug}/tokens/{tokenID}", serviceTokenHandler.Revoke)

		// Project password (server-side AES-256-GCM encrypted)
		r.Put("/projects/{slug}/password", passwordHandler.Set)
		r.Get("/projects/{slug}/password", passwordHandler.Get)

		// Secrets — stricter rate limit
		r.Get("/projects/{slug}/envs", projectHandler.ListEnvironments)
		r.Route("/projects/{slug}/envs/{env}", func(r chi.Router) {
			r.Use(mw.RateLimitByUser(100, time.Minute))
			r.Post("/push", secretsHandler.Push)
			r.Get("/pull", secretsHandler.Pull)
			r.Get("/pull/version", secretsHandler.PullVersion)
			r.Get("/history", secretsHandler.History)
			r.Get("/head", secretsHandler.Head)
		})
	})

	// ── HTTP server ──────────────────────────────────────────────────────────
	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
		// ReadHeaderTimeout bounds slowloris attacks, which ReadTimeout alone
		// does not stop: an attacker can dribble headers forever.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB; default is 1 MB but be explicit
	}

	done := make(chan struct{})
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Fatalf("graceful shutdown failed: %v", err)
		}
		close(done)
	}()

	log.Printf("[INFO] DotSync server listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
	<-done
	log.Println("server stopped")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set — see .env.example", key)
	}
	return v
}

// requireEnv checks all vars at startup and prints every missing one at once.
func requireEnv(keys ...string) {
	var missing []string
	for _, k := range keys {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		log.Fatalf("missing required env var(s): %s\nSee .env.example for setup instructions.",
			strings.Join(missing, ", "))
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvInt reads a positive integer env var, falling back on anything invalid.
func getEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
