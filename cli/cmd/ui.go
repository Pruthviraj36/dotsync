package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
)

const (
	uiMaxBodyBytes = 1 << 20 // 1 MB — enough for any .env file
	uiReadTimeout  = 10 * time.Second
	uiWriteTimeout = 0 // disabled for SSE streaming
	uiIdleTimeout  = 60 * time.Second
)

func validUISlug(slug string) bool {
	switch strings.TrimSpace(slug) {
	case "", "null", "undefined":
		return false
	default:
		return true
	}
}

func uiCmd() *cobra.Command {
	var portFlag string
	var noOpenFlag bool

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the DotSync web dashboard",
		Long: `Launches a local web server and opens the DotSync dashboard
in your browser. All operations run locally — secrets are encrypted
on your machine, same as the CLI. The server never sees plaintext.`,
		Example: `  dotsync ui
  dotsync ui --port 4041
  dotsync ui --no-open`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, _ := config.LoadProject()

			listener, err := net.Listen("tcp", "127.0.0.1:"+portFlag)
			if err != nil {
				return fmt.Errorf("port %s is in use — try: dotsync ui --port 4041", portFlag)
			}

			client := api.New(cfg)
			mux := http.NewServeMux()

			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy",
					"default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline' https://fonts.googleapis.com https://fonts.gstatic.com; font-src 'self' https://fonts.gstatic.com data:; img-src 'self' data: https://avatars.githubusercontent.com; connect-src 'self'")
				w.Header().Set("X-Frame-Options", "DENY")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Write([]byte(dashboardHTML))
			})

			mux.HandleFunc("/api/me", uiHandler(func(r *http.Request) (any, error) {
				me, err := client.GetMe()
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"user":       me,
					"server_url": cfg.ServerURL,
					"project":    projCfg,
				}, nil
			}))

			mux.HandleFunc("/api/projects", uiHandler(func(r *http.Request) (any, error) {
				projects, err := client.ListProjects()
				if err != nil {
					return nil, err
				}
				return map[string]any{"projects": projects}, nil
			}))

			mux.HandleFunc("/api/project/", uiHandler(func(r *http.Request) (any, error) {
				slug := r.URL.Query().Get("slug")
				if !validUISlug(slug) && projCfg != nil {
					slug = projCfg.ProjectSlug
				}
				if !validUISlug(slug) {
					return nil, fmt.Errorf("no project slug — run dotsync init first")
				}
				envs, _ := client.ListEnvironments(slug)
				members, _ := client.ListTeamMembers(slug)
				tokens, _ := client.ListServiceTokens(slug)
				logs, _ := client.AuditLogs(slug)
				return map[string]any{
					"slug":    slug,
					"envs":    envs,
					"members": members,
					"tokens":  tokens,
					"logs":    logs,
				}, nil
			}))

			mux.HandleFunc("/api/history", uiHandler(func(r *http.Request) (any, error) {
				slug := r.URL.Query().Get("slug")
				env := r.URL.Query().Get("env")
				if !validUISlug(slug) && projCfg != nil {
					slug = projCfg.ProjectSlug
				}
				if env == "" && projCfg != nil {
					env = projCfg.DefaultEnv
				}
				if !validUISlug(slug) {
					return nil, fmt.Errorf("no project slug — run dotsync init first")
				}
				history, err := client.History(slug, env)
				if err != nil {
					return nil, err
				}
				return map[string]any{"history": history}, nil
			}))

			mux.HandleFunc("/api/pull", uiHandler(func(r *http.Request) (any, error) {
				slug := r.URL.Query().Get("slug")
				env := r.URL.Query().Get("env")
				if !validUISlug(slug) {
					return nil, fmt.Errorf("missing slug parameter")
				}
				if env == "" || env == "null" || env == "undefined" {
					return nil, fmt.Errorf("missing env parameter")
				}
				result, err := client.Pull(slug, env)
				if err != nil {
					return nil, err
				}
				password, err := resolvePassword(client, slug)
				if err != nil {
					return nil, err
				}
				plaintext, err := cliCrypto.DecryptEnvFile(
					result.EncryptedData, result.Nonce, password, slug,
				)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				keys := cliCrypto.ParseEnvFile(plaintext)
				return map[string]any{
					"content": plaintext,
					"version": result.Version,
					"keys":    len(keys),
					"by":      result.PushedBy,
				}, nil
			}))

			mux.HandleFunc("/api/push", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug    string `json:"slug"`
					Env     string `json:"env"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.Env == "" {
					return nil, fmt.Errorf("slug and env are required")
				}
				password, err := resolvePassword(client, req.Slug)
				if err != nil {
					return nil, err
				}
				keys := cliCrypto.ParseEnvFile(req.Content)
				ciphertext, nonce, err := cliCrypto.EncryptEnvFile(req.Content, password, req.Slug)
				if err != nil {
					return nil, fmt.Errorf("encryption failed: %w", err)
				}
				result, err := client.Push(req.Slug, req.Env, api.PushRequest{
					EncryptedData: ciphertext,
					Nonce:         nonce,
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"version": result.Version, "keys": len(keys)}, nil
			}))

			mux.HandleFunc("/api/team/add", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug     string `json:"slug"`
					Username string `json:"username"`
					Role     string `json:"role"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.Username == "" {
					return nil, fmt.Errorf("slug and username are required")
				}
				if err := client.AddTeamMember(req.Slug, req.Username); err != nil {
					return nil, err
				}
				if req.Role != "" && req.Role != "member" {
					_ = client.UpdateTeamRole(req.Slug, req.Username, req.Role)
				}
				return map[string]any{"ok": true}, nil
			}))

			mux.HandleFunc("/api/team/remove", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug     string `json:"slug"`
					Username string `json:"username"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.Username == "" {
					return nil, fmt.Errorf("slug and username are required")
				}
				if err := client.RemoveTeamMember(req.Slug, req.Username); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			}))

			mux.HandleFunc("/api/tokens/create", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug string `json:"slug"`
					Env  string `json:"env"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.Name == "" {
					return nil, fmt.Errorf("slug and name are required")
				}
				if req.Env == "" {
					req.Env = "*"
				}
				token, err := client.CreateServiceToken(req.Slug, req.Env, req.Name)
				if err != nil {
					return nil, err
				}
				return map[string]any{"token": token}, nil
			}))

			mux.HandleFunc("/api/tokens/revoke", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug    string `json:"slug"`
					TokenID string `json:"token_id"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.TokenID == "" {
					return nil, fmt.Errorf("slug and token_id are required")
				}
				if err := client.RevokeServiceToken(req.Slug, req.TokenID); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			}))

			mux.HandleFunc("/api/rollback", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug    string `json:"slug"`
					Env     string `json:"env"`
					Version int    `json:"version"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.Env == "" || req.Version < 1 {
					return nil, fmt.Errorf("slug, env, and version are required")
				}
				old, err := client.PullVersion(req.Slug, req.Env, req.Version)
				if err != nil {
					return nil, fmt.Errorf("fetch v%d: %w", req.Version, err)
				}
				password, err := resolvePassword(client, req.Slug)
				if err != nil {
					return nil, err
				}
				plaintext, err := cliCrypto.DecryptEnvFile(
					old.EncryptedData, old.Nonce, password, req.Slug,
				)
				if err != nil {
					return nil, fmt.Errorf("decrypt v%d: %w", req.Version, err)
				}
				ciphertext, nonce, err := cliCrypto.EncryptEnvFile(plaintext, password, req.Slug)
				if err != nil {
					return nil, fmt.Errorf("re-encrypt: %w", err)
				}
				result, err := client.Push(req.Slug, req.Env, api.PushRequest{
					EncryptedData: ciphertext,
					Nonce:         nonce,
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"version": result.Version}, nil
			}))

			mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Accel-Buffering", "no")

				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "streaming not supported", http.StatusInternalServerError)
					return
				}

				ticker := time.NewTicker(30 * time.Second)
				defer ticker.Stop()

				fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
				flusher.Flush()

				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
						flusher.Flush()
					}
				}
			})

			url := fmt.Sprintf("http://localhost:%s", portFlag)
			srv := &http.Server{
				Handler:      mux,
				ReadTimeout:  uiReadTimeout,
				WriteTimeout: uiWriteTimeout,
				IdleTimeout:  uiIdleTimeout,
			}

			blank()
			fmt.Println(prog("ui", boldCyan(url)))
			fmt.Println(ok(dim("all operations run locally — secrets never leave your machine")))
			blank()
			hint("ctrl+c to stop")
			blank()

			if !noOpenFlag {
				go func() {
					time.Sleep(300 * time.Millisecond)
					openBrowser(url)
				}()
			}

			quit := make(chan os.Signal, 1)
			signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-quit
				blank()
				fmt.Println(ok(dim("shutting down...")))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(ctx)
			}()

			if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("server error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&portFlag, "port", "4040", "local port to listen on")
	cmd.Flags().BoolVar(&noOpenFlag, "no-open", false, "don't open browser automatically")
	return cmd
}

func uiHandler(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:4040 http://127.0.0.1:4040")
		w.Header().Set("Cache-Control", "no-store")

		data, err := fn(r)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(data)
	}
}

func uiPostHandler(fn func(body []byte) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, uiMaxBodyBytes))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "failed to read body"})
			return
		}

		data, err := fn(body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(data)
	}
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("cmd", "/c", "start", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	c.Stderr = os.Stderr
	_ = c.Start()
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DotSync Dashboard</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<style>
:root {
  --bg-primary: #09090b;
  --bg-secondary: #18181b;
  --bg-tertiary: #27272a;
  --bg-hover: #3f3f46;
  --border: #3f3f46;
  --border-hover: #52525b;
  --text-primary: #fafafa;
  --text-secondary: #a1a1aa;
  --text-muted: #71717a;
  --accent: #06b6d4;
  --accent-hover: #22d3ee;
  --accent-bg: rgba(6,182,212,0.1);
  --success: #10b981;
  --success-bg: rgba(16,185,129,0.1);
  --warning: #f59e0b;
  --warning-bg: rgba(245,158,11,0.1);
  --error: #ef4444;
  --error-bg: rgba(239,68,68,0.1);
  --radius-sm: 6px;
  --radius-md: 8px;
  --radius-lg: 12px;
  --shadow-sm: 0 1px 2px rgba(0,0,0,0.3);
  --shadow-md: 0 4px 6px rgba(0,0,0,0.4);
  --shadow-lg: 0 10px 15px rgba(0,0,0,0.5);
  --font-sans: 'Inter', system-ui, -apple-system, sans-serif;
  --font-mono: 'JetBrains Mono', ui-monospace, monospace;
  --header-height: 60px;
  --sidebar-width: 240px;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

html, body {
  height: 100%;
  background: var(--bg-primary);
  color: var(--text-primary);
  font-family: var(--font-sans);
  font-size: 14px;
  line-height: 1.5;
  -webkit-font-smoothing: antialiased;
}

body {
  background: 
    radial-gradient(ellipse 800px 400px at 0% 0%, rgba(6,182,212,0.08), transparent 50%),
    radial-gradient(ellipse 600px 300px at 100% 100%, rgba(139,92,246,0.06), transparent 50%),
    var(--bg-primary);
}

::-webkit-scrollbar { width: 8px; height: 8px; }
::-webkit-scrollbar-track { background: transparent; }
::-webkit-scrollbar-thumb { background: var(--border); border-radius: 4px; }
::-webkit-scrollbar-thumb:hover { background: var(--border-hover); }

/* Header */
.header {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  height: var(--header-height);
  background: rgba(9,9,11,0.95);
  backdrop-filter: blur(20px);
  border-bottom: 1px solid var(--border);
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 0 20px;
  z-index: 100;
}

.logo {
  font-family: var(--font-mono);
  font-weight: 700;
  font-size: 18px;
  color: var(--accent);
  letter-spacing: -0.5px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.logo-icon {
  width: 32px;
  height: 32px;
  background: linear-gradient(135deg, var(--accent), #8b5cf6);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 16px;
  font-weight: 700;
  color: white;
}

.header-divider {
  width: 1px;
  height: 24px;
  background: var(--border);
  flex-shrink: 0;
}

.project-select {
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 13px;
  padding: 8px 12px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  outline: none;
  transition: all 0.2s;
  max-width: 200px;
}

.project-select:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-bg);
}

.project-select option {
  background: var(--bg-secondary);
}

.env-tabs {
  display: flex;
  gap: 4px;
  overflow-x: auto;
  scrollbar-width: none;
}

.env-tabs::-webkit-scrollbar { display: none; }

.env-tab {
  padding: 6px 14px;
  border-radius: 20px;
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.2s;
  border: 1px solid transparent;
  white-space: nowrap;
}

.env-tab:hover {
  color: var(--text-primary);
  background: var(--bg-secondary);
  border-color: var(--border);
}

.env-tab.active {
  color: var(--accent);
  background: var(--accent-bg);
  border-color: var(--accent);
}

.header-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 12px;
}

.status-badge {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: 20px;
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 500;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  color: var(--text-secondary);
  transition: all 0.3s;
}

.status-badge.connected {
  color: var(--success);
  border-color: var(--success);
  background: var(--success-bg);
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--text-muted);
  transition: all 0.3s;
}

.status-badge.connected .status-dot {
  background: var(--success);
  box-shadow: 0 0 8px var(--success);
}

.user-avatar {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: linear-gradient(135deg, #8b5cf6, var(--accent));
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  font-weight: 600;
  color: white;
  border: 2px solid var(--border);
  overflow: hidden;
}

.user-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

/* Layout */
.container {
  display: flex;
  height: calc(100vh - var(--header-height));
  margin-top: var(--header-height);
}

.sidebar {
  width: var(--sidebar-width);
  background: rgba(9,9,11,0.5);
  border-right: 1px solid var(--border);
  padding: 20px 12px;
  display: flex;
  flex-direction: column;
  gap: 24px;
  overflow-y: auto;
}

.nav-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.nav-section-title {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 1.5px;
  color: var(--text-muted);
  padding: 0 12px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.2s;
  font-weight: 500;
  font-size: 13px;
}

.nav-item:hover {
  color: var(--text-primary);
  background: var(--bg-secondary);
}

.nav-item.active {
  color: var(--accent);
  background: var(--accent-bg);
}

.nav-icon {
  font-size: 16px;
  width: 20px;
  text-align: center;
  opacity: 0.8;
}

.nav-badge {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 600;
  padding: 2px 8px;
  border-radius: 10px;
  background: var(--bg-tertiary);
  color: var(--text-muted);
}

.sidebar-footer {
  margin-top: auto;
  padding-top: 16px;
  border-top: 1px solid var(--border);
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-muted);
  line-height: 1.6;
}

.main {
  flex: 1;
  overflow-y: auto;
  padding: 24px;
}

.page {
  display: none;
  animation: fadeIn 0.3s ease;
}

.page.active {
  display: block;
}

@keyframes fadeIn {
  from { opacity: 0; transform: translateY(8px); }
  to { opacity: 1; transform: translateY(0); }
}

/* Stats Grid */
.stats-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
  margin-bottom: 24px;
}

.stat-card {
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  padding: 20px;
  transition: all 0.2s;
}

.stat-card:hover {
  border-color: var(--border-hover);
  transform: translateY(-2px);
}

.stat-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 1px;
  color: var(--text-muted);
  margin-bottom: 8px;
}

.stat-value {
  font-family: var(--font-mono);
  font-size: 28px;
  font-weight: 700;
  color: var(--text-primary);
  line-height: 1;
}

.stat-sub {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 8px;
}

/* Card */
.card {
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  overflow: hidden;
  margin-bottom: 16px;
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
}

.card-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  gap: 8px;
}

.card-actions {
  display: flex;
  gap: 8px;
}

.card-body {
  padding: 20px;
}

/* Buttons */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 16px;
  border-radius: var(--radius-sm);
  font-size: 13px;
  font-weight: 500;
  font-family: var(--font-sans);
  cursor: pointer;
  border: none;
  transition: all 0.2s;
  white-space: nowrap;
}

.btn-primary {
  background: var(--accent);
  color: #0c4a6e;
}

.btn-primary:hover {
  background: var(--accent-hover);
  transform: translateY(-1px);
}

.btn-secondary {
  background: var(--bg-tertiary);
  color: var(--text-primary);
  border: 1px solid var(--border);
}

.btn-secondary:hover {
  background: var(--bg-hover);
  border-color: var(--border-hover);
}

.btn-success {
  background: var(--success);
  color: #064e3b;
}

.btn-success:hover {
  filter: brightness(1.1);
  transform: translateY(-1px);
}

.btn-danger {
  background: transparent;
  color: var(--error);
  border: 1px solid var(--error-bg);
}

.btn-danger:hover {
  background: var(--error-bg);
  border-color: var(--error);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
  transform: none;
}

/* Editor */
.editor-container {
  position: relative;
}

.editor-toolbar {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.search-input {
  flex: 1;
  min-width: 200px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 13px;
  padding: 8px 12px;
  border-radius: var(--radius-sm);
  outline: none;
  transition: all 0.2s;
}

.search-input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-bg);
}

.dirty-indicator {
  display: none;
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 600;
  padding: 4px 10px;
  border-radius: 12px;
  background: var(--warning-bg);
  color: var(--warning);
  border: 1px solid var(--warning);
}

.dirty-indicator.visible {
  display: inline-flex;
}

.editor {
  width: 100%;
  min-height: 400px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.6;
  padding: 16px;
  border-radius: var(--radius-md);
  resize: vertical;
  outline: none;
  transition: all 0.2s;
  tab-size: 2;
}

.editor:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-bg);
}

.editor.dirty {
  border-color: var(--warning);
  box-shadow: 0 0 0 3px var(--warning-bg);
}

.editor-note {
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 12px;
  font-family: var(--font-mono);
}

/* Form Elements */
.input, .select {
  background: var(--bg-tertiary);
  border: 1px solid var(--border);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 13px;
  padding: 8px 12px;
  border-radius: var(--radius-sm);
  outline: none;
  transition: all 0.2s;
}

.input:focus, .select:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-bg);
}

.select {
  cursor: pointer;
}

.select option {
  background: var(--bg-tertiary);
}

.form-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

/* List Items */
.list-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 0;
  border-bottom: 1px solid var(--border);
  transition: background 0.2s;
}

.list-item:last-child {
  border-bottom: none;
}

.list-item:hover {
  background: var(--bg-secondary);
  margin: 0 -12px;
  padding: 14px 12px;
  border-radius: var(--radius-sm);
}

/* Badges */
.badge {
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 600;
  padding: 4px 10px;
  border-radius: 12px;
}

.badge-accent {
  background: var(--accent-bg);
  color: var(--accent);
  border: 1px solid var(--accent);
}

.badge-success {
  background: var(--success-bg);
  color: var(--success);
  border: 1px solid var(--success);
}

.badge-purple {
  background: rgba(139,92,246,0.1);
  color: #8b5cf6;
  border: 1px solid rgba(139,92,246,0.3);
}

.badge-muted {
  background: var(--bg-tertiary);
  color: var(--text-muted);
  border: 1px solid var(--border);
}

/* Avatar */
.avatar-small {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: var(--bg-tertiary);
  border: 1px solid var(--border);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  font-family: var(--font-mono);
  flex-shrink: 0;
}

/* History Items */
.history-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 12px;
  border-radius: var(--radius-md);
  border-bottom: 1px solid var(--border);
  transition: all 0.2s;
}

.history-item:last-child {
  border-bottom: none;
}

.history-item:hover {
  background: var(--bg-secondary);
}

.version-badge {
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 700;
  padding: 4px 10px;
  border-radius: 8px;
  width: 50px;
  text-align: center;
  flex-shrink: 0;
}

.version-badge.current {
  background: var(--success-bg);
  color: var(--success);
  border: 1px solid var(--success);
}

.version-badge.old {
  background: var(--bg-tertiary);
  color: var(--text-muted);
  border: 1px solid var(--border);
}

/* Token Reveal */
.token-reveal {
  font-family: var(--font-mono);
  font-size: 12px;
  word-break: break-all;
  background: var(--success-bg);
  border: 1px solid var(--success);
  color: var(--success);
  padding: 16px;
  border-radius: var(--radius-md);
  margin-top: 12px;
  position: relative;
  cursor: pointer;
  transition: all 0.2s;
}

.token-reveal:hover {
  background: rgba(16,185,129,0.15);
  border-color: var(--success);
}

.token-reveal::after {
  content: 'click to copy';
  position: absolute;
  right: 16px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 10px;
  color: rgba(16,185,129,0.6);
}

/* Empty State */
.empty {
  text-align: center;
  padding: 48px 24px;
  color: var(--text-muted);
  font-size: 13px;
}

.empty-icon {
  font-size: 40px;
  margin-bottom: 16px;
  opacity: 0.5;
}

/* Toast */
.toast-container {
  position: fixed;
  bottom: 24px;
  right: 24px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  z-index: 1000;
}

.toast {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-radius: var(--radius-md);
  font-size: 13px;
  font-family: var(--font-mono);
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  box-shadow: var(--shadow-lg);
  max-width: 400px;
  animation: slideIn 0.3s ease;
}

.toast-success {
  border-color: var(--success);
  background: var(--success-bg);
  color: var(--success);
}

.toast-error {
  border-color: var(--error);
  background: var(--error-bg);
  color: var(--error);
}

.toast-info {
  border-color: var(--accent);
  background: var(--accent-bg);
  color: var(--accent);
}

@keyframes slideIn {
  from { transform: translateX(20px); opacity: 0; }
  to { transform: translateX(0); opacity: 1; }
}

/* Loading */
.loading-overlay {
  display: none;
  position: fixed;
  inset: 0;
  background: rgba(9,9,11,0.9);
  z-index: 200;
  backdrop-filter: blur(8px);
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 16px;
}

.loading-overlay.visible {
  display: flex;
}

.spinner {
  width: 40px;
  height: 40px;
  border: 3px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.loading-text {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--text-secondary);
  animation: pulse 1.5s ease-in-out infinite;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}

/* Mobile Menu */
.menu-btn {
  display: none;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--border);
  background: var(--bg-secondary);
  color: var(--text-primary);
  cursor: pointer;
  font-size: 18px;
  transition: all 0.2s;
}

.menu-btn:hover {
  background: var(--bg-tertiary);
  border-color: var(--border-hover);
}

.overlay {
  display: none;
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,0.5);
  z-index: 90;
  backdrop-filter: blur(4px);
}

.overlay.visible {
  display: block;
}

/* Responsive */
@media (max-width: 768px) {
  .menu-btn {
    display: flex;
  }

  .sidebar {
    position: fixed;
    top: var(--header-height);
    left: 0;
    bottom: 0;
    z-index: 95;
    transform: translateX(-100%);
    transition: transform 0.3s ease;
    width: 280px;
    background: var(--bg-primary);
    padding: 20px 16px;
  }

  .sidebar.open {
    transform: translateX(0);
  }

  .stats-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .main {
    padding: 16px;
  }

  .env-tabs {
    max-width: 150px;
  }

  .project-select {
    max-width: 120px;
  }

  .header {
    padding: 0 12px;
  }

  .toast-container {
    left: 12px;
    right: 12px;
    bottom: 12px;
  }

  .toast {
    max-width: calc(100vw - 24px);
  }
}

@media (max-width: 480px) {
  .stat-value {
    font-size: 24px;
  }

  .card-header {
    flex-direction: column;
    align-items: flex-start;
  }

  .card-actions {
    width: 100%;
    justify-content: flex-start;
  }

  .btn {
    padding: 8px 12px;
    font-size: 12px;
  }
}
</style>
</head>
<body>

<div class="overlay" id="overlay" onclick="closeSidebar()"></div>

<header class="header">
  <button class="menu-btn" id="menuBtn" onclick="toggleSidebar()" aria-label="Menu">☰</button>
  <div class="logo">
    <div class="logo-icon">●</div>
    dotsync
  </div>
  <div class="header-divider"></div>
  <select class="project-select" id="projectSelect" onchange="onProjectChange(this.value)">
    <option>loading...</option>
  </select>
  <div class="env-tabs" id="envTabs"></div>
  <div class="header-right">
    <div class="status-badge" id="statusBadge">
      <span class="status-dot"></span>
      <span>local</span>
    </div>
    <div class="user-avatar" id="userAvatar">?</div>
  </div>
</header>

<div class="container">
  <aside class="sidebar" id="sidebar">
    <div class="nav-section">
      <div class="nav-section-title">Secrets</div>
      <div class="nav-item active" data-page="secrets" onclick="navigate(this, 'secrets')">
        <span class="nav-icon">⬡</span>
        Secrets
      </div>
      <div class="nav-item" data-page="history" onclick="navigate(this, 'history')">
        <span class="nav-icon">◷</span>
        History
      </div>
    </div>
    <div class="nav-section">
      <div class="nav-section-title">Project</div>
      <div class="nav-item" data-page="team" onclick="navigate(this, 'team')">
        <span class="nav-icon">◎</span>
        Team
        <span class="nav-badge" id="teamBadge">0</span>
      </div>
      <div class="nav-item" data-page="tokens" onclick="navigate(this, 'tokens')">
        <span class="nav-icon">⌘</span>
        Tokens
        <span class="nav-badge" id="tokensBadge">0</span>
      </div>
      <div class="nav-item" data-page="audit" onclick="navigate(this, 'audit')">
        <span class="nav-icon">☰</span>
        Audit Log
      </div>
    </div>
    <div class="sidebar-footer">
      <div id="serverInfo">—</div>
      <div style="margin-top: 8px; opacity: 0.7;">
        ⌘/Ctrl+S to push<br>
        ⌘/Ctrl+Shift+L to pull
      </div>
    </div>
  </aside>

  <main class="main">
    <!-- Secrets Page -->
    <div class="page active" id="page-secrets">
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-label">Version</div>
          <div class="stat-value" id="statVersion" style="color: var(--success);">—</div>
          <div class="stat-sub" id="statAuthor">—</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Keys</div>
          <div class="stat-value" id="statKeys">—</div>
          <div class="stat-sub">encrypted locally</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Environment</div>
          <div class="stat-value" id="statEnv" style="color: var(--accent);">—</div>
          <div class="stat-sub">active</div>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div class="card-title">
            Editor
            <span class="badge badge-muted" id="keyCountBadge">0 keys</span>
            <span class="dirty-indicator" id="dirtyIndicator">unsaved</span>
          </div>
          <div class="card-actions">
            <button class="btn btn-secondary" onclick="copyEditor()" title="Copy all">⎘ Copy</button>
            <button class="btn btn-secondary" onclick="sortKeys()" title="Sort keys A–Z">A↓</button>
            <button class="btn btn-secondary" id="pullBtn" onclick="pullSecrets()">↓ Pull</button>
            <button class="btn btn-success" id="pushBtn" onclick="pushSecrets()">↑ Push</button>
          </div>
        </div>
        <div class="card-body">
          <div class="editor-note">Decrypted on this machine — server never sees values.</div>
          <div class="editor-container">
            <div class="editor-toolbar">
              <input class="search-input" id="searchInput" placeholder="Find key..." oninput="findKey(false)" onkeydown="if(event.key==='Enter'){event.preventDefault();findKey(true)}" autocomplete="off">
              <button class="btn btn-secondary" onclick="findKey(true)">Next</button>
            </div>
            <textarea class="editor" id="editor" placeholder="DATABASE_URL=postgres://...&#10;API_KEY=sk-live-...&#10;NODE_ENV=production" spellcheck="false"></textarea>
          </div>
        </div>
      </div>
    </div>

    <!-- History Page -->
    <div class="page" id="page-history">
      <div class="card">
        <div class="card-header">
          <div class="card-title">Version History</div>
          <div class="card-actions">
            <button class="btn btn-secondary" onclick="loadHistory()">↻ Refresh</button>
          </div>
        </div>
        <div class="card-body" id="historyBody">
          <div class="empty">
            <div class="empty-icon">◷</div>
            Open this page to load history
          </div>
        </div>
      </div>
    </div>

    <!-- Team Page -->
    <div class="page" id="page-team">
      <div class="card">
        <div class="card-header">
          <div class="card-title">Team Members</div>
        </div>
        <div class="card-body">
          <div class="form-row" style="margin-bottom: 16px;">
            <input class="input" id="memberInput" placeholder="github-username" style="flex: 1;" onkeydown="if(event.key==='Enter')addMember()">
            <select class="select" id="memberRole" style="width: 120px;">
              <option value="member">member</option>
              <option value="admin">admin</option>
              <option value="viewer">viewer</option>
            </select>
            <button class="btn btn-primary" onclick="addMember()">+ Add</button>
          </div>
          <div id="memberList"></div>
        </div>
      </div>
    </div>

    <!-- Tokens Page -->
    <div class="page" id="page-tokens">
      <div class="card">
        <div class="card-header">
          <div class="card-title">Service Tokens</div>
        </div>
        <div class="card-body">
          <div class="form-row" style="margin-bottom: 16px;">
            <input class="input" id="tokenName" placeholder="name (e.g. github-prod)" style="flex: 1;" onkeydown="if(event.key==='Enter')createToken()">
            <select class="select" id="tokenEnv" style="width: 140px;">
              <option value="*">all envs (*)</option>
            </select>
            <button class="btn btn-primary" onclick="createToken()">+ Create</button>
          </div>
          <div id="tokenReveal"></div>
          <div id="tokenList"></div>
        </div>
      </div>
    </div>

    <!-- Audit Page -->
    <div class="page" id="page-audit">
      <div class="card">
        <div class="card-header">
          <div class="card-title">Audit Log</div>
          <div class="card-actions">
            <button class="btn btn-secondary" onclick="refreshAudit()">↻ Refresh</button>
          </div>
        </div>
        <div class="card-body" id="auditBody">
          <div class="empty">
            <div class="empty-icon">☰</div>
            Loading...
          </div>
        </div>
      </div>
    </div>
  </main>
</div>

<div class="loading-overlay" id="loadingOverlay">
  <div class="spinner"></div>
  <div class="loading-text" id="loadingText">Loading...</div>
</div>

<div class="toast-container" id="toastContainer"></div>

<script>
const state = {
  project: null,
  env: 'dev',
  me: null,
  data: null,
  baseline: '',
  findIndex: -1
};

function isValidSlug(slug) {
  return !!(slug && slug !== 'null' && slug !== 'undefined');
}

function buildQuery(slug, env) {
  return 'slug=' + encodeURIComponent(slug) + '&env=' + encodeURIComponent(env || 'dev');
}

function isDirty() {
  const editor = document.getElementById('editor');
  return !!(editor && editor.value !== state.baseline);
}

function setDirtyUI() {
  const dirty = isDirty();
  const editor = document.getElementById('editor');
  const indicator = document.getElementById('dirtyIndicator');
  if (editor) editor.classList.toggle('dirty', dirty);
  if (indicator) indicator.classList.toggle('visible', dirty);
}

function confirmDiscard() {
  if (!isDirty()) return true;
  return confirm('You have unsaved edits. Discard them?');
}

function markClean(content) {
  state.baseline = content == null ? document.getElementById('editor').value : content;
  setDirtyUI();
}

function toggleSidebar() {
  document.getElementById('sidebar').classList.toggle('open');
  document.getElementById('overlay').classList.toggle('visible');
}

function closeSidebar() {
  document.getElementById('sidebar').classList.remove('open');
  document.getElementById('overlay').classList.remove('visible');
}

function showLoading(text = 'Loading...') {
  const overlay = document.getElementById('loadingOverlay');
  const textEl = document.getElementById('loadingText');
  textEl.textContent = text;
  overlay.classList.add('visible');
}

function hideLoading() {
  document.getElementById('loadingOverlay').classList.remove('visible');
}

function setStatus(connected) {
  const badge = document.getElementById('statusBadge');
  badge.classList.toggle('connected', connected);
}

function toast(message, type = 'info') {
  const container = document.getElementById('toastContainer');
  const el = document.createElement('div');
  el.className = 'toast toast-' + type;
  const icon = type === 'success' ? '✓' : type === 'error' ? '✗' : '→';
  el.textContent = icon + ' ' + message;
  container.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

function escapeHtml(text) {
  return String(text || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function timeAgo(iso) {
  if (!iso) return 'never';
  const seconds = (Date.now() - new Date(iso).getTime()) / 1000;
  if (seconds < 60) return Math.floor(seconds) + 's ago';
  if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago';
  if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago';
  return new Date(iso).toISOString().slice(0, 10);
}

async function apiCall(url) {
  const response = await fetch(url);
  const data = await response.json();
  if (data.error) throw new Error(data.error);
  return data;
}

async function apiPost(url, body) {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  const data = await response.json();
  if (data.error) throw new Error(data.error);
  return data;
}

async function init() {
  try {
    showLoading('Connecting to server...');
    const me = await apiCall('/api/me');
    state.me = me;
    const user = me.user || {};
    
    const avatar = document.getElementById('userAvatar');
    if (user.avatar_url) {
      avatar.innerHTML = '<img src="' + escapeHtml(user.avatar_url) + '" alt="">';
    } else {
      avatar.textContent = (user.username || '?')[0].toUpperCase();
    }
    
    const server = me.server_url || '';
    document.getElementById('serverInfo').textContent = server || 'local';
    document.getElementById('serverInfo').title = server;

    showLoading('Loading projects...');
    const projects = await apiCall('/api/projects');
    const validProjects = (projects.projects || []).filter(p => isValidSlug(p && p.slug));
    const select = document.getElementById('projectSelect');

    if (!validProjects.length) {
      select.innerHTML = '<option value="">no projects — run dotsync init</option>';
      hideLoading();
      toast('No projects found — run: dotsync init', 'error');
      setStatus(true);
      return;
    }

    select.innerHTML = validProjects.map(p => 
      '<option value="' + escapeHtml(p.slug) + '">' + escapeHtml(p.slug) + '</option>'
    ).join('');

    const defaultSlug = me.project && me.project.project_slug;
    const defaultEnv = me.project && me.project.default_env;
    if (isValidSlug(defaultSlug)) select.value = defaultSlug;
    if (defaultEnv) state.env = defaultEnv;

    const slug = select.value;
    if (!isValidSlug(slug)) {
      hideLoading();
      toast('No project selected — run: dotsync init', 'error');
      return;
    }
    
    await switchProject(slug);
    hideLoading();

    const hash = (location.hash || '#secrets').slice(1);
    const navItem = document.querySelector('.nav-item[data-page="' + hash + '"]');
    if (navItem) navigate(navItem, hash);
  } catch (error) {
    hideLoading();
    toast('Connection failed: ' + error.message, 'error');
  }
}

async function onProjectChange(slug) {
  if (!confirmDiscard()) {
    document.getElementById('projectSelect').value = state.project || '';
    return;
  }
  await switchProject(slug);
}

async function switchProject(slug) {
  if (!isValidSlug(slug)) return;
  showLoading('Switching project...');
  state.project = slug;
  await loadProject();
  connectSSE();
  hideLoading();
}

async function loadProject() {
  if (!isValidSlug(state.project)) return;
  const data = await apiCall('/api/project/?slug=' + encodeURIComponent(state.project));
  state.data = data;

  const envs = normalizeEnvs(data.envs);
  if (envs.length && envs.indexOf(state.env) < 0) state.env = envs[0];

  document.getElementById('envTabs').innerHTML = envs.map(env =>
    '<div class="env-tab' + (env === state.env ? ' active' : '') + 
    '" onclick="onEnvClick(\'' + escapeHtml(env) + '\')">' + escapeHtml(env) + '</div>'
  ).join('');

  document.getElementById('tokenEnv').innerHTML =
    '<option value="*">all envs (*)</option>' +
    envs.map(env => '<option value="' + escapeHtml(env) + '"' + 
    (env === state.env ? ' selected' : '') + '>' + escapeHtml(env) + '</option>').join('');

  renderMembers(data.members || []);
  renderTokens(data.tokens || []);
  renderAudit(data.logs || []);
  document.getElementById('statEnv').textContent = state.env;
  document.getElementById('teamBadge').textContent = (data.members || []).length;
  document.getElementById('tokensBadge').textContent = (data.tokens || []).length;

  await pullSecrets(false);
}

function normalizeEnvs(envs) {
  if (!envs || !envs.length) return ['dev', 'staging', 'production'];
  if (typeof envs[0] === 'string') return envs;
  return envs.map(e => e.name || e).filter(Boolean);
}

function onEnvClick(env) {
  if (!confirmDiscard()) return;
  switchEnv(env);
}

function switchEnv(env) {
  if (!isValidSlug(state.project)) return;
  state.env = env;
  document.querySelectorAll('.env-tab').forEach(tab => 
    tab.classList.toggle('active', tab.textContent === env)
  );
  document.getElementById('statEnv').textContent = env;
  pullSecrets(false);
  if (document.getElementById('page-history').classList.contains('active')) loadHistory();
}

async function pullSecrets(showToast = true) {
  if (!isValidSlug(state.project)) return;
  if (!showToast) showLoading('Pulling secrets...');
  try {
    const result = await apiCall('/api/pull?' + buildQuery(state.project, state.env));
    const content = result.content || '';
    document.getElementById('editor').value = content;
    document.getElementById('statVersion').textContent = 'v' + result.version;
    document.getElementById('statAuthor').textContent = result.by ? '@' + result.by : '';
    markClean(content);
    countKeys();
    if (showToast) toast('Pulled v' + result.version + ' — ' + result.keys + ' secrets', 'success');
  } catch (error) {
    document.getElementById('editor').value = '';
    document.getElementById('statVersion').textContent = 'none';
    document.getElementById('statAuthor').textContent = 'no push yet';
    markClean('');
    countKeys();
  } finally {
    if (!showToast) hideLoading();
  }
}

function countKeys() {
  const lines = (document.getElementById('editor').value || '').split('\n');
  const count = lines.filter(line => 
    line.trim() && !line.trim().startsWith('#') && line.includes('=')
  ).length;
  document.getElementById('keyCountBadge').textContent = count + ' key' + (count !== 1 ? 's' : '');
  document.getElementById('statKeys').textContent = count;
  setDirtyUI();
}

async function pushSecrets() {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  const content = document.getElementById('editor').value.trim();
  if (!content) {
    toast('Editor is empty', 'error');
    return;
  }
  const btn = document.getElementById('pushBtn');
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> Pushing';
  try {
    const result = await apiPost('/api/push', {
      slug: state.project,
      env: state.env,
      content
    });
    document.getElementById('statVersion').textContent = 'v' + result.version;
    markClean(document.getElementById('editor').value);
    countKeys();
    toast('Pushed v' + result.version + ' — ' + result.keys + ' keys encrypted', 'success');
    
    try {
      const data = await apiCall('/api/project/?slug=' + encodeURIComponent(state.project));
      state.data = data;
      renderMembers(data.members || []);
      renderTokens(data.tokens || []);
      renderAudit(data.logs || []);
      document.getElementById('teamBadge').textContent = (data.members || []).length;
      document.getElementById('tokensBadge').textContent = (data.tokens || []).length;
    } catch (_) {}
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    btn.disabled = false;
    btn.innerHTML = '↑ Push';
  }
}

function copyEditor() {
  const value = document.getElementById('editor').value;
  if (!value) {
    toast('Editor is empty', 'error');
    return;
  }
  navigator.clipboard.writeText(value).then(() => toast('Copied to clipboard', 'success'))
    .catch(() => toast('Copy failed', 'error'));
}

function sortKeys() {
  const editor = document.getElementById('editor');
  const lines = editor.value.split('\n');
  const comments = [], keys = [], other = [];
  
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) {
      other.push(line);
      continue;
    }
    if (trimmed.startsWith('#')) {
      comments.push(line);
    } else if (trimmed.includes('=')) {
      keys.push(line);
    } else {
      other.push(line);
    }
  }
  
  keys.sort((a, b) => 
    a.split('=')[0].localeCompare(b.split('=')[0], undefined, { sensitivity: 'base' })
  );
  
  editor.value = [...comments, ...keys, ...other.filter(line => line.trim())].join('\n');
  countKeys();
  toast('Keys sorted', 'info');
}

function findKey(next) {
  const query = (document.getElementById('searchInput').value || '').trim().toLowerCase();
  const editor = document.getElementById('editor');
  if (!query) return;
  
  const text = editor.value;
  const lower = text.toLowerCase();
  let start = next ? (state.findIndex + 1) : 0;
  let index = lower.indexOf(query, start);
  
  if (index < 0 && start > 0) index = lower.indexOf(query, 0);
  if (index < 0) {
    toast('No match', 'info');
    return;
  }
  
  state.findIndex = index;
  if (next) {
    editor.focus();
    editor.setSelectionRange(index, index + query.length);
    const linesBefore = text.slice(0, index).split('\n').length;
    const lineHeight = 1.6 * 13;
    editor.scrollTop = Math.max(0, (linesBefore - 3) * lineHeight);
  }
}

async function loadHistory() {
  const body = document.getElementById('historyBody');
  if (!isValidSlug(state.project)) {
    body.innerHTML = '<div class="empty"><div class="empty-icon">◷</div>Select a project first</div>';
    return;
  }
  body.innerHTML = '<div class="empty"><span class="spinner"></span></div>';
  try {
    const result = await apiCall('/api/history?' + buildQuery(state.project, state.env));
    const history = result.history || [];
    if (!history.length) {
      body.innerHTML = '<div class="empty"><div class="empty-icon">◷</div>No history yet</div>';
      return;
    }
    body.innerHTML = history.map((entry, index) => {
      const isCurrent = index === 0;
      return '<div class="history-item">' +
        '<div class="version-badge ' + (isCurrent ? 'current' : 'old') + '">v' + entry.version + '</div>' +
        '<div style="flex: 1;">' +
          '<div style="font-family: var(--font-mono); font-size: 13px;">@' + escapeHtml(entry.pushed_by) + '</div>' +
          '<div style="font-size: 12px; color: var(--text-muted);">' + timeAgo(entry.created_at) + '</div>' +
        '</div>' +
        (isCurrent
          ? '<span class="badge badge-success">current</span>'
          : '<button class="btn btn-secondary" style="font-size: 11px; padding: 6px 10px;" onclick="rollback(' + entry.version + ')">↩ Restore</button>'
        ) +
      '</div>';
    }).join('');
  } catch (error) {
    body.innerHTML = '<div class="empty">' + escapeHtml(error.message) + '</div>';
  }
}

async function rollback(version) {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  if (!confirm('Restore v' + version + ' as new current version?')) return;
  showLoading('Rolling back to v' + version + '...');
  try {
    const result = await apiPost('/api/rollback', {
      slug: state.project,
      env: state.env,
      version
    });
    hideLoading();
    toast('v' + version + ' restored as v' + result.version, 'success');
    loadHistory();
    await pullSecrets(false);
  } catch (error) {
    hideLoading();
    toast(error.message, 'error');
  }
}

function renderMembers(members) {
  const list = document.getElementById('memberList');
  if (!members.length) {
    list.innerHTML = '<div class="empty"><div class="empty-icon">◎</div>No members yet</div>';
    return;
  }
  const me = state.me && state.me.user && state.me.user.username;
  const roleColors = {
    owner: 'badge-accent',
    admin: 'badge-purple',
    member: 'badge-success',
    viewer: 'badge-muted'
  };
  list.innerHTML = members.map(member => {
    const isMe = member.username === me;
    return '<div class="list-item">' +
      '<div class="avatar-small">' + (member.username || '?')[0].toUpperCase() + '</div>' +
      '<div style="flex: 1;">' +
        '<div style="font-family: var(--font-mono); font-size: 13px;' + 
        (isMe ? ' color: var(--accent);' : '') + '">@' + escapeHtml(member.username) + 
        (isMe ? ' <span style="color: var(--text-muted); font-size: 11px;">(you)</span>' : '') + '</div>' +
        '<div style="font-size: 12px; color: var(--text-muted); font-family: var(--font-mono);">' + 
        (member.joined_at || '').slice(0, 10) + '</div>' +
      '</div>' +
      '<span class="badge ' + (roleColors[member.role] || 'badge-muted') + '">' + member.role + '</span>' +
      (!isMe ? '<button class="btn btn-danger" style="font-size: 11px; padding: 6px 10px;" ' +
      'onclick="removeMember(\'' + escapeHtml(member.username) + '\')">Remove</button>' : '') +
    '</div>';
  }).join('');
}

async function addMember() {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  const username = document.getElementById('memberInput').value.trim().replace('@', '');
  const role = document.getElementById('memberRole').value;
  if (!username) {
    toast('Enter a username', 'error');
    return;
  }
  showLoading('Adding member...');
  try {
    await apiPost('/api/team/add', { slug: state.project, username, role });
    document.getElementById('memberInput').value = '';
    hideLoading();
    toast('@' + username + ' added as ' + role, 'success');
    await loadProject();
  } catch (error) {
    hideLoading();
    toast(error.message, 'error');
  }
}

async function removeMember(username) {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  if (!confirm('Remove @' + username + '?')) return;
  showLoading('Removing member...');
  try {
    await apiPost('/api/team/remove', { slug: state.project, username });
    hideLoading();
    toast('@' + username + ' removed', 'success');
    await loadProject();
  } catch (error) {
    hideLoading();
    toast(error.message, 'error');
  }
}

function renderTokens(tokens) {
  const list = document.getElementById('tokenList');
  if (!tokens.length) {
    list.innerHTML = '<div class="empty" style="padding-top: 12px;"><div class="empty-icon">⌘</div>No tokens. Create one for CI/CD.</div>';
    return;
  }
  list.innerHTML = tokens.map(token =>
    '<div class="list-item">' +
      '<div style="flex: 1; font-family: var(--font-mono); font-size: 13px;">' + escapeHtml(token.name) + '</div>' +
      '<span class="badge badge-accent" style="margin-right: 8px;">' + escapeHtml(token.env) + '</span>' +
      '<div style="font-size: 12px; color: var(--text-muted); font-family: var(--font-mono); margin-right: 12px;">' +
      'used ' + timeAgo(token.last_used_at) + '</div>' +
      '<button class="btn btn-danger" style="font-size: 11px; padding: 6px 10px;" ' +
      'onclick="revokeToken(\'' + escapeHtml(token.id) + '\', \'' + escapeHtml(token.name) + '\')">Revoke</button>' +
    '</div>'
  ).join('');
}

async function createToken() {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  const name = document.getElementById('tokenName').value.trim();
  const env = document.getElementById('tokenEnv').value;
  if (!name) {
    toast('Enter a token name', 'error');
    return;
  }
  showLoading('Creating token...');
  try {
    const result = await apiPost('/api/tokens/create', { slug: state.project, env, name });
    document.getElementById('tokenName').value = '';
    hideLoading();
    const reveal = document.getElementById('tokenReveal');
    reveal.innerHTML = '<div style="font-size: 12px; color: var(--warning); margin-bottom: 6px; font-family: var(--font-mono);">⚠ Copy now — shown once only</div>' +
      '<div class="token-reveal" onclick="navigator.clipboard.writeText(\'' + result.token + '\').then(() => toast(\'Copied!\', \'success\'))">' + 
      result.token + '</div>';
    toast('Token created — copy it now!', 'success');
    await loadProject();
  } catch (error) {
    hideLoading();
    toast(error.message, 'error');
  }
}

async function revokeToken(id, name) {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  if (!confirm('Revoke "' + name + '"? Cannot be undone.')) return;
  showLoading('Revoking token...');
  try {
    await apiPost('/api/tokens/revoke', { slug: state.project, token_id: id });
    hideLoading();
    toast('Token revoked', 'success');
    await loadProject();
  } catch (error) {
    hideLoading();
    toast(error.message, 'error');
  }
}

function renderAudit(logs) {
  const body = document.getElementById('auditBody');
  if (!logs.length) {
    body.innerHTML = '<div class="empty"><div class="empty-icon">☰</div>No events yet</div>';
    return;
  }
  const actionClass = action => ({
    push: 'badge-success',
    pull: 'badge-accent',
    invite: 'badge-purple',
    revoke: 'badge-muted',
    token_create: 'badge-purple',
    token_revoke: 'badge-muted'
  }[action] || 'badge-muted');
  
  body.innerHTML = logs.map(log =>
    '<div class="list-item">' +
      '<div style="font-family: var(--font-mono); font-size: 11px; color: var(--text-muted); width: 70px; flex-shrink: 0;">' + 
      timeAgo(log.created_at) + '</div>' +
      '<span class="badge ' + actionClass(log.action) + '" style="min-width: 60px; text-align: center; flex-shrink: 0;">' + 
      escapeHtml(log.action || '?') + '</span>' +
      '<div>' +
        '<div style="font-family: var(--font-mono); font-size: 13px;">@' + escapeHtml(log.username || '?') + '</div>' +
        (log.env ? '<div style="font-size: 12px; color: var(--accent); font-family: var(--font-mono);">' + 
        escapeHtml(log.env) + '</div>' : '') +
      '</div>' +
    '</div>'
  ).join('');
}

async function refreshAudit() {
  if (!isValidSlug(state.project)) return;
  showLoading('Refreshing audit log...');
  try {
    const data = await apiCall('/api/project/?slug=' + encodeURIComponent(state.project));
    state.data = data;
    renderAudit(data.logs || []);
    hideLoading();
    toast('Audit refreshed', 'success');
  } catch (error) {
    hideLoading();
    toast(error.message, 'error');
  }
}

function navigate(element, page) {
  document.querySelectorAll('.nav-item').forEach(item => item.classList.remove('active'));
  element.classList.add('active');
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.getElementById('page-' + page).classList.add('active');
  location.hash = page;
  closeSidebar();
  if (page === 'history') loadHistory();
  if (page === 'audit' && state.data) renderAudit(state.data.logs || []);
}

let sseConnection = null;
let sseBackoff = 1000;

function connectSSE() {
  if (sseConnection) {
    sseConnection.close();
    sseConnection = null;
  }
  if (document.visibilityState === 'hidden') return;

  sseConnection = new EventSource('/api/events');
  sseConnection.addEventListener('ping', () => {
    setStatus(true);
    sseBackoff = 1000;
  });
  sseConnection.onerror = () => {
    setStatus(false);
    if (sseConnection) {
      sseConnection.close();
      sseConnection = null;
    }
    const delay = sseBackoff;
    sseBackoff = Math.min(sseBackoff * 2, 60000);
    setTimeout(() => {
      if (document.visibilityState !== 'hidden') connectSSE();
    }, delay);
  };
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') {
    if (sseConnection) {
      sseConnection.close();
      sseConnection = null;
    }
    setStatus(false);
  } else {
    connectSSE();
  }
});

document.addEventListener('DOMContentLoaded', () => {
  const editor = document.getElementById('editor');
  if (editor) {
    editor.addEventListener('input', () => { countKeys(); });
    editor.addEventListener('keydown', (event) => {
      if (event.key === 'Tab') {
        event.preventDefault();
        const start = editor.selectionStart;
        const end = editor.selectionEnd;
        editor.value = editor.value.slice(0, start) + '  ' + editor.value.slice(end);
        editor.selectionStart = editor.selectionEnd = start + 2;
        countKeys();
      }
    });
  }
});

document.addEventListener('keydown', (event) => {
  const meta = event.metaKey || event.ctrlKey;
  if (meta && event.key.toLowerCase() === 's') {
    event.preventDefault();
    pushSecrets();
  }
  if (meta && event.shiftKey && event.key.toLowerCase() === 'l') {
    event.preventDefault();
    pullSecrets();
  }
});

window.addEventListener('beforeunload', (event) => {
  if (isDirty()) {
    event.preventDefault();
    event.returnValue = '';
  }
});

init();
</script>
</body>
</html>`
