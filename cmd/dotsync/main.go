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

	"github.com/Pruthviraj36/dotsync/internal/assets"
	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/handler"
	mw "github.com/Pruthviraj36/dotsync/internal/middleware"
	"github.com/Pruthviraj36/dotsync/internal/payment"
	"github.com/Pruthviraj36/dotsync/internal/service"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env in development — Render injects env vars directly in production
	_ = godotenv.Load()

	// ── Startup env validation ─────────────────────────────────────────────
	// Fail immediately and loudly on missing config, rather than starting
	// successfully and failing deep inside a request later (e.g. GitHub
	// OAuth silently sending an empty client_id, or Stripe signature
	// verification always failing with no clear cause).
	// GITHUB_CLIENT_SECRET is intentionally NOT required — the device flow
	// exchanges tokens directly between the CLI and GitHub; this server is
	// never involved in that exchange and never needs the client secret.
	requireEnv(
		"DATABASE_URL",
		"JWT_SECRET",
		"GITHUB_CLIENT_ID",
		"STRIPE_SECRET_KEY",
		"STRIPE_WEBHOOK_SECRET",
		"SERVER_MASTER_KEY",
	)

	// ── Database ────────────────────────────────────────────────────────────
	// DATABASE_URL: pooled connection (Neon PgBouncer) — used for normal app queries.
	// DATABASE_URL_DIRECT: unpooled connection — required for migrations
	// (DDL/advisory locks don't work reliably through a pooler). Falls back to
	// DATABASE_URL if DATABASE_URL_DIRECT isn't set (e.g. plain Render Postgres).
	dsn := mustEnv("DATABASE_URL")
	migrationDSN := getEnv("DATABASE_URL_DIRECT", dsn)

	database, err := db.New(dsn)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	migrationsPath := getEnv("MIGRATIONS_PATH", "./migrations")
	if err := db.RunMigrations(migrationDSN, migrationsPath); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	// ── Services ────────────────────────────────────────────────────────────
	jwtSecret := mustEnv("JWT_SECRET")
	authSvc := auth.NewService(database, jwtSecret)
	projectSvc := service.NewProjectService(database)
	secretSvc := service.NewSecretService(database)
	teamSvc := service.NewTeamService(database)
	auditSvc := service.NewAuditService(database)

	// SERVER_MASTER_KEY must decode to exactly 32 bytes (AES-256) — generate
	// one with: openssl rand -hex 32
	masterKey, err := hex.DecodeString(mustEnv("SERVER_MASTER_KEY"))
	if err != nil || len(masterKey) != 32 {
		log.Fatalf("SERVER_MASTER_KEY must be a 64-character hex string (32 bytes) — generate one with: openssl rand -hex 32")
	}
	passwordSvc := service.NewPasswordService(database, masterKey)

	// ── Handlers ────────────────────────────────────────────────────────────
	authHandler := handler.NewAuthHandler(authSvc, database)
	projectHandler := handler.NewProjectHandler(projectSvc, teamSvc)
	secretsHandler := handler.NewSecretsHandler(secretSvc, projectSvc, teamSvc, auditSvc)
	teamHandler := handler.NewTeamHandler(projectSvc, teamSvc, database)
	passwordHandler := handler.NewPasswordHandler(passwordSvc, projectSvc, teamSvc, auditSvc)
	identityHandler := handler.NewIdentityHandler(database)

	paymentProvider, err := payment.New()
	if err != nil {
		log.Fatalf("payment provider: %v", err)
	}
	billingHandler := handler.NewBillingHandler(paymentProvider, database)

	// ── Router ──────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Global middleware stack (order matters)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(mw.SecurityHeaders)
	r.Use(mw.RequestID)

	// CORS — restrict to your frontend domain in production
	frontendURL := getEnv("FRONTEND_URL", "http://localhost:3000")
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{frontendURL},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-DotSync-Signature"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Global rate limit: 200 req/min per IP (brute force protection)
	r.Use(mw.RateLimitByIP(200, time.Minute))

	// Health check (unauthenticated)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"dotsync"}`))
	})

	// Install script — `curl -fsSL https://dotsync.onrender.com/install.sh | bash`
	// Served straight out of the binary via go:embed (see internal/assets),
	// so there's nothing extra to deploy alongside the server.
	r.Get("/install.sh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-sh; charset=utf-8")
		w.Write(assets.InstallScript)
	})

	// Stripe/LemonSqueezy/PayPal webhook — raw body required, no auth middleware.
	// The provider's own signature verification (inside WebhookHandler) is
	// what actually authenticates these requests.
	r.Post("/api/payment/webhook", handler.WebhookHandler(paymentProvider, database))

	// ── Public billing route (unauthenticated) ──
	r.Get("/api/billing/plans", billingHandler.Plans)

	// ── Public auth routes ──
	r.Route("/api/auth", func(r chi.Router) {
		r.Use(mw.RateLimitByIP(20, time.Minute))
		r.Get("/config", authHandler.Config)
		r.Post("/github/device", authHandler.GitHubDeviceLogin)
		r.Post("/refresh", authHandler.RefreshToken)
	})

	// ── Protected routes ──
	r.Route("/api", func(r chi.Router) {
		r.Use(mw.Authenticate(authSvc))
		r.Use(mw.RateLimitByUser(300, time.Minute))

		// Auth
		r.Post("/auth/logout", authHandler.Logout)
		r.Get("/auth/me", authHandler.Me)

		// Identity (ed25519 pubkey for verifying signed pushes)
		r.Put("/me/pubkey", identityHandler.SetPubKey)

		// Billing
		r.Post("/billing/checkout", billingHandler.Checkout)
		r.Post("/billing/portal", billingHandler.Portal)
		r.Get("/billing/status", billingHandler.Status)

		// Projects
		r.Post("/projects", projectHandler.Create)
		r.Get("/projects", projectHandler.List)

		// Teams
		r.Post("/projects/{slug}/team", teamHandler.AddMember)
		r.Get("/projects/{slug}/team", teamHandler.ListMembers)
		r.Delete("/projects/{slug}/team/{username}", teamHandler.RemoveMember)
		r.Patch("/projects/{slug}/team/{username}", teamHandler.UpdateRole)

		// Audit logs (business plan)
		r.Get("/projects/{slug}/audit", secretsHandler.AuditLogs)

		// Project password (server-side encrypted, see PasswordService)
		r.Put("/projects/{slug}/password", passwordHandler.Set)
		r.Get("/projects/{slug}/password", passwordHandler.Get)

		// Secrets (stricter rate limit for push/pull)
		r.Get("/projects/{slug}/envs", projectHandler.ListEnvironments)
		r.Route("/projects/{slug}/envs/{env}", func(r chi.Router) {
			r.Use(mw.RateLimitByUser(100, time.Minute))
			r.Post("/push", secretsHandler.Push)
			r.Get("/pull", secretsHandler.Pull)
			r.Get("/pull/version", secretsHandler.PullVersion)
			r.Get("/history", secretsHandler.History)
		})
	})

	// ── Server ──────────────────────────────────────────────────────────────
	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	done := make(chan struct{})
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Println("shutting down server...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Fatalf("graceful shutdown failed: %v", err)
		}
		close(done)
	}()

	log.Printf("🚀 DotSync server running on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}

	<-done
	log.Println("server stopped")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s not set", key)
	}
	return v
}

// requireEnv checks all given env vars are set, and fails fast at startup
// listing every missing one at once — rather than discovering them one at a
// time, deep inside unrelated requests, after the server has already booted.
func requireEnv(keys ...string) {
	var missing []string
	for _, k := range keys {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		log.Fatalf(
			"missing required env var(s): %s\n"+
				"Set these before starting the server — see .env.example for details.",
			strings.Join(missing, ", "),
		)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
