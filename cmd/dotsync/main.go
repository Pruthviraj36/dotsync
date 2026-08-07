package main

import (
	"context"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	// before any request is accepted. See .env.example for generation commands.
	requireEnv(
		"DATABASE_URL",      // postgres://user:pass@host:5432/dbname
		"JWT_SECRET",        // openssl rand -hex 32
		"GITHUB_CLIENT_ID",  // from https://github.com/settings/developers
		"SERVER_MASTER_KEY", // openssl rand -hex 32  (AES-256 for password encryption)
	)

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
	teamHandler := handler.NewTeamHandler(projectSvc, teamSvc, database)
	passwordHandler := handler.NewPasswordHandler(passwordSvc, projectSvc, teamSvc, auditSvc)
	identityHandler := handler.NewIdentityHandler(database)
	serviceTokenHandler := handler.NewServiceTokenHandler(serviceTokenSvc, projectSvc, teamSvc)

	// ── Router ───────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(mw.SecurityHeaders)

	// CORS — defaults to same origin; set FRONTEND_URL for web dashboards
	frontendURL := getEnv("FRONTEND_URL", "http://localhost:3000")
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{frontendURL},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-DotSync-Signature"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Global rate limit: 200 req/min per IP
	r.Use(mw.RateLimitByIP(200, time.Minute))

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

	// ── Public auth routes ───────────────────────────────────────────────────
	r.Route("/api/auth", func(r chi.Router) {
		r.Use(mw.RateLimitByIP(20, time.Minute))
		r.Get("/config", authHandler.Config)
		r.Post("/github/device", authHandler.GitHubDeviceLogin)
		r.Post("/refresh", authHandler.RefreshToken)
	})

	// ── Protected routes ─────────────────────────────────────────────────────
	r.Route("/api", func(r chi.Router) {
		r.Use(mw.Authenticate(authSvc, serviceTokenSvc))
		r.Use(mw.RateLimitByUser(300, time.Minute))

		// Auth
		r.Post("/auth/logout", authHandler.Logout)
		r.Get("/auth/me", authHandler.Me)

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
		})
	})

	// ── HTTP server ──────────────────────────────────────────────────────────
	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
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
