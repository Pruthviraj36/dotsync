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
<title>DotSync</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@400;500;600;700&family=IBM+Plex+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<style>
:root {
  --bg-deep: #0a0e17;
  --bg-surface: #111827;
  --bg-elevated: #1f2937;
  --bg-input: #374151;
  --border-subtle: #1f2937;
  --border-default: #374151;
  --border-focus: #60a5fa;
  --text-primary: #f9fafb;
  --text-secondary: #9ca3af;
  --text-tertiary: #6b7280;
  --accent-primary: #3b82f6;
  --accent-secondary: #8b5cf6;
  --accent-glow: rgba(59,130,246,0.15);
  --success: #10b981;
  --success-subtle: rgba(16,185,129,0.1);
  --warning: #f59e0b;
  --warning-subtle: rgba(245,158,11,0.1);
  --error: #ef4444;
  --error-subtle: rgba(239,68,68,0.1);
  --font-display: 'Space Grotesk', system-ui, sans-serif;
  --font-mono: 'IBM Plex Mono', ui-monospace, monospace;
  --radius-xs: 2px;
  --radius-sm: 4px;
  --radius-md: 6px;
  --radius-lg: 8px;
  --shadow-subtle: 0 1px 3px rgba(0,0,0,0.3);
  --shadow-medium: 0 4px 6px rgba(0,0,0,0.4);
  --shadow-elevated: 0 10px 25px rgba(0,0,0,0.5);
  --header-height: 56px;
  --sidebar-width: 200px;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

html, body {
  height: 100%;
  background: var(--bg-deep);
  color: var(--text-primary);
  font-family: var(--font-display);
  font-size: 13px;
  line-height: 1.6;
  -webkit-font-smoothing: antialiased;
}

body {
  background: 
    linear-gradient(180deg, rgba(59,130,246,0.03) 0%, transparent 30%),
    var(--bg-deep);
}

::-webkit-scrollbar { width: 6px; height: 6px; }
::-webkit-scrollbar-track { background: var(--bg-deep); }
::-webkit-scrollbar-thumb { background: var(--border-default); border-radius: 2px; }
::-webkit-scrollbar-thumb:hover { background: var(--text-tertiary); }

/* Header */
.header {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  height: var(--header-height);
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 16px;
  z-index: 100;
}

.logo {
  font-family: var(--font-mono);
  font-weight: 600;
  font-size: 14px;
  color: var(--accent-primary);
  letter-spacing: -0.02em;
  display: flex;
  align-items: center;
  gap: 6px;
}

.logo-icon {
  width: 24px;
  height: 24px;
  background: linear-gradient(135deg, var(--accent-primary), var(--accent-secondary));
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 700;
  color: white;
}

.header-divider {
  width: 1px;
  height: 16px;
  background: var(--border-subtle);
  flex-shrink: 0;
}

.project-select {
  background: var(--bg-input);
  border: 1px solid var(--border-default);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 6px 10px;
  border-radius: var(--radius-xs);
  cursor: pointer;
  outline: none;
  transition: border-color 0.15s;
  max-width: 160px;
}

.project-select:focus {
  border-color: var(--accent-primary);
}

.project-select option {
  background: var(--bg-surface);
}

.env-tabs {
  display: flex;
  gap: 2px;
  overflow-x: auto;
  scrollbar-width: none;
}

.env-tabs::-webkit-scrollbar { display: none; }

.env-tab {
  padding: 4px 10px;
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.15s;
  border: 1px solid transparent;
  white-space: nowrap;
}

.env-tab:hover {
  color: var(--text-primary);
  background: var(--bg-elevated);
  border-color: var(--border-default);
}

.env-tab.active {
  color: var(--accent-primary);
  background: var(--accent-glow);
  border-color: var(--accent-primary);
}

.header-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}

.status-badge {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 500;
  background: var(--bg-elevated);
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
  transition: all 0.2s;
}

.status-badge.connected {
  color: var(--success);
  border-color: var(--success);
  background: var(--success-subtle);
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-tertiary);
  transition: all 0.2s;
}

.status-badge.connected .status-dot {
  background: var(--success);
  box-shadow: 0 0 6px var(--success);
}

.user-avatar {
  width: 28px;
  height: 28px;
  border-radius: var(--radius-sm);
  background: linear-gradient(135deg, var(--accent-secondary), var(--accent-primary));
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-weight: 600;
  color: white;
  border: 1px solid var(--border-default);
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
  background: var(--bg-surface);
  border-right: 1px solid var(--border-subtle);
  padding: 16px 8px;
  display: flex;
  flex-direction: column;
  gap: 20px;
  overflow-y: auto;
}

.nav-section {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.nav-section-title {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-tertiary);
  padding: 8px 12px 4px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-radius: var(--radius-sm);
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.15s;
  font-weight: 500;
  font-size: 12px;
  font-family: var(--font-display);
}

.nav-item:hover {
  color: var(--text-primary);
  background: var(--bg-elevated);
}

.nav-item.active {
  color: var(--accent-primary);
  background: var(--accent-glow);
  border-left: 2px solid var(--accent-primary);
  padding-left: 10px;
}

.nav-icon {
  font-size: 14px;
  width: 16px;
  text-align: center;
  opacity: 0.7;
}

.nav-badge {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: 9px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  background: var(--bg-elevated);
  color: var(--text-tertiary);
}

.sidebar-footer {
  margin-top: auto;
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--text-tertiary);
  line-height: 1.5;
}

.main {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
  background: var(--bg-deep);
}

.page {
  display: none;
  animation: slideIn 0.2s ease;
}

.page.active {
  display: block;
}

@keyframes slideIn {
  from { opacity: 0; transform: translateX(8px); }
  to { opacity: 1; transform: translateX(0); }
}

/* Stats Grid */
.stats-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
  margin-bottom: 20px;
}

.stat-card {
  background: var(--bg-surface);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  padding: 16px;
  transition: all 0.15s;
  position: relative;
  overflow: hidden;
}

.stat-card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  background: linear-gradient(90deg, var(--accent-primary), var(--accent-secondary));
  opacity: 0;
  transition: opacity 0.15s;
}

.stat-card:hover::before {
  opacity: 1;
}

.stat-card:hover {
  border-color: var(--border-default);
}

.stat-label {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-tertiary);
  margin-bottom: 8px;
  font-family: var(--font-mono);
}

.stat-value {
  font-family: var(--font-mono);
  font-size: 24px;
  font-weight: 600;
  color: var(--text-primary);
  line-height: 1;
}

.stat-sub {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-tertiary);
  margin-top: 6px;
}

.stat-value.warning {
  color: var(--warning);
  animation: pulse 2s ease-in-out infinite;
}

.stat-value.warning {
  color: var(--warning);
  animation: pulse 2s ease-in-out infinite;
}

/* Card */
.card {
  background: var(--bg-surface);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  overflow: hidden;
  margin-bottom: 12px;
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-elevated);
}

.card-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  gap: 8px;
  font-family: var(--font-display);
}

.card-actions {
  display: flex;
  gap: 6px;
}

.card-body {
  padding: 16px;
}

/* Buttons */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: var(--radius-xs);
  font-size: 12px;
  font-weight: 500;
  font-family: var(--font-display);
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.15s;
  white-space: nowrap;
}

.btn-primary {
  background: var(--accent-primary);
  color: white;
  border-color: var(--accent-primary);
}

.btn-primary:hover {
  background: var(--accent-secondary);
  border-color: var(--accent-secondary);
}

.btn-secondary {
  background: var(--bg-elevated);
  color: var(--text-primary);
  border-color: var(--border-default);
}

.btn-secondary:hover {
  background: var(--bg-input);
  border-color: var(--text-tertiary);
}

.btn-success {
  background: var(--success);
  color: white;
  border-color: var(--success);
}

.btn-success:hover {
  filter: brightness(1.1);
}

.btn-danger {
  background: transparent;
  color: var(--error);
  border-color: var(--error-subtle);
}

.btn-danger:hover {
  background: var(--error-subtle);
  border-color: var(--error);
}

.btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

/* Editor */
.editor-container {
  position: relative;
}

.editor-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

.search-input {
  flex: 1;
  min-width: 180px;
  background: var(--bg-input);
  border: 1px solid var(--border-default);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 6px 10px;
  border-radius: var(--radius-xs);
  outline: none;
  transition: border-color 0.15s;
}

.search-input:focus {
  border-color: var(--accent-primary);
}

.dirty-indicator {
  display: none;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 600;
  padding: 3px 8px;
  border-radius: var(--radius-xs);
  background: var(--warning-subtle);
  color: var(--warning);
  border: 1px solid var(--warning);
}

.dirty-indicator.visible {
  display: inline-flex;
}

.editor {
  width: 100%;
  min-height: 360px;
  background: var(--bg-input);
  border: 1px solid var(--border-default);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.7;
  padding: 12px;
  border-radius: var(--radius-sm);
  resize: vertical;
  outline: none;
  transition: border-color 0.15s;
  tab-size: 2;
}

.editor:focus {
  border-color: var(--accent-primary);
}

.editor.dirty {
  border-color: var(--warning);
}

.editor.disabled {
  opacity: 0.5;
  cursor: not-allowed;
  background: var(--bg-elevated);
}

.editor.disabled::placeholder {
  color: var(--text-tertiary);
}

.editor-note {
  font-size: 11px;
  color: var(--text-tertiary);
  margin-bottom: 8px;
  font-family: var(--font-mono);
}

.editor-note.warning {
  color: var(--warning);
  background: var(--warning-subtle);
  padding: 8px 10px;
  border-radius: var(--radius-xs);
  border: 1px solid var(--warning);
}

/* Form Elements */
.input, .select {
  background: var(--bg-input);
  border: 1px solid var(--border-default);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 6px 10px;
  border-radius: var(--radius-xs);
  outline: none;
  transition: border-color 0.15s;
}

.input:focus, .select:focus {
  border-color: var(--accent-primary);
}

.select {
  cursor: pointer;
}

.select option {
  background: var(--bg-surface);
}

.form-row {
  display: flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
}

/* List Items */
.list-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 0;
  border-bottom: 1px solid var(--border-subtle);
  transition: background 0.15s;
}

.list-item:last-child {
  border-bottom: none;
}

.list-item:hover {
  background: var(--bg-elevated);
  margin: 0 -8px;
  padding: 10px 8px;
  border-radius: var(--radius-sm);
}

/* Badges */
.badge {
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 600;
  padding: 3px 8px;
  border-radius: var(--radius-xs);
}

.badge-accent {
  background: var(--accent-glow);
  color: var(--accent-primary);
  border: 1px solid var(--accent-primary);
}

.badge-success {
  background: var(--success-subtle);
  color: var(--success);
  border: 1px solid var(--success);
}

.badge-purple {
  background: rgba(139,92,246,0.1);
  color: var(--accent-secondary);
  border: 1px solid rgba(139,92,246,0.3);
}

.badge-muted {
  background: var(--bg-elevated);
  color: var(--text-tertiary);
  border: 1px solid var(--border-default);
}

/* Avatar */
.avatar-small {
  width: 28px;
  height: 28px;
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  border: 1px solid var(--border-default);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  flex-shrink: 0;
}

/* History Items */
.history-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 8px;
  border-radius: var(--radius-sm);
  border-bottom: 1px solid var(--border-subtle);
  transition: all 0.15s;
}

.history-item:last-child {
  border-bottom: none;
}

.history-item:hover {
  background: var(--bg-elevated);
}

.version-badge {
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 600;
  padding: 3px 8px;
  border-radius: var(--radius-xs);
  width: 44px;
  text-align: center;
  flex-shrink: 0;
}

.version-badge.current {
  background: var(--success-subtle);
  color: var(--success);
  border: 1px solid var(--success);
}

.version-badge.old {
  background: var(--bg-elevated);
  color: var(--text-tertiary);
  border: 1px solid var(--border-default);
}

/* Token Reveal */
.token-reveal {
  font-family: var(--font-mono);
  font-size: 11px;
  word-break: break-all;
  background: var(--success-subtle);
  border: 1px solid var(--success);
  color: var(--success);
  padding: 12px;
  border-radius: var(--radius-sm);
  margin-top: 10px;
  position: relative;
  cursor: pointer;
  transition: all 0.15s;
}

.token-reveal:hover {
  background: rgba(16,185,129,0.15);
  border-color: var(--success);
}

.token-reveal::after {
  content: 'click to copy';
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 9px;
  color: rgba(16,185,129,0.6);
}

/* Empty State */
.empty {
  text-align: center;
  padding: 40px 20px;
  color: var(--text-tertiary);
  font-size: 12px;
}

.empty-icon {
  font-size: 32px;
  margin-bottom: 12px;
  opacity: 0.4;
}

/* Toast */
.toast-container {
  position: fixed;
  bottom: 20px;
  right: 20px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  z-index: 1000;
}

.toast {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-radius: var(--radius-sm);
  font-size: 12px;
  font-family: var(--font-mono);
  background: var(--bg-surface);
  border: 1px solid var(--border-default);
  box-shadow: var(--shadow-elevated);
  max-width: 380px;
  animation: slideIn 0.2s ease;
}

.toast-success {
  border-color: var(--success);
  background: var(--success-subtle);
  color: var(--success);
}

.toast-error {
  border-color: var(--error);
  background: var(--error-subtle);
  color: var(--error);
}

.toast-info {
  border-color: var(--accent-primary);
  background: var(--accent-glow);
  color: var(--accent-primary);
}

@keyframes slideIn {
  from { transform: translateX(16px); opacity: 0; }
  to { transform: translateX(0); opacity: 1; }
}

/* Loading */
.loading-overlay {
  display: none;
  position: fixed;
  inset: 0;
  background: rgba(10,14,23,0.9);
  z-index: 200;
  backdrop-filter: blur(4px);
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 12px;
}

.loading-overlay.visible {
  display: flex;
}

.spinner {
  width: 32px;
  height: 32px;
  border: 2px solid var(--border-default);
  border-top-color: var(--accent-primary);
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.loading-text {
  font-family: var(--font-mono);
  font-size: 12px;
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
  width: 32px;
  height: 32px;
  border-radius: var(--radius-xs);
  border: 1px solid var(--border-default);
  background: var(--bg-elevated);
  color: var(--text-primary);
  cursor: pointer;
  font-size: 16px;
  transition: all 0.15s;
}

.menu-btn:hover {
  background: var(--bg-input);
  border-color: var(--text-tertiary);
}

.overlay {
  display: none;
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,0.6);
  z-index: 90;
  backdrop-filter: blur(2px);
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
    transition: transform 0.25s ease;
    width: 240px;
    background: var(--bg-surface);
    padding: 16px 12px;
  }

  .sidebar.open {
    transform: translateX(0);
  }

  .stats-grid {
    grid-template-columns: 1fr;
    gap: 10px;
  }

  .main {
    padding: 16px;
  }

  .env-tabs {
    max-width: 140px;
  }

  .project-select {
    max-width: 110px;
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
    font-size: 20px;
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
    padding: 6px 10px;
    font-size: 11px;
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
          <div class="editor-note" id="editorNote">Decrypted on this machine — server never sees values.</div>
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

function handlePasswordError(message) {
  if (message && message.includes('could not fetch project password')) {
    toast('Password not set. Ask project owner to push first, or run: dotsync init --rotate-password', 'error');
    return true;
  }
  if (message && message.includes('no password set for this project')) {
    toast('Password not set. Ask project owner to push first, or run: dotsync init --rotate-password', 'error');
    return true;
  }
  if (message && message.includes('decryption failed')) {
    toast('Decryption failed. The password may have changed. Ask the owner to rotate it.', 'error');
    return true;
  }
  return false;
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
    
    try {
      await switchProject(slug);
    } catch (error) {
      // Continue even if project loading fails (e.g., password error)
      // The UI will still be usable for other operations
      if (!handlePasswordError(error.message)) {
        toast('Could not load project: ' + error.message, 'error');
      }
    }
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

  // Reset UI state before attempting pull
  document.getElementById('statVersion').classList.remove('warning');
  document.getElementById('editor').classList.remove('disabled');
  document.getElementById('pushBtn').disabled = false;
  document.getElementById('pullBtn').disabled = false;

  try {
    await pullSecrets(false);
  } catch (error) {
    // Password errors are handled in pullSecrets, but we want to continue
    // rendering the rest of the UI even if secrets can't be loaded
    // The editor and buttons will be disabled to prevent errors
  }
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
    document.getElementById('editor').classList.remove('disabled');
    document.getElementById('editor').placeholder = 'DATABASE_URL=postgres://...\nAPI_KEY=sk-live-...\nNODE_ENV=production';
    document.getElementById('statVersion').textContent = 'v' + result.version;
    document.getElementById('statVersion').classList.remove('warning');
    document.getElementById('statAuthor').textContent = result.by ? '@' + result.by : '';
    document.getElementById('pushBtn').disabled = false;
    document.getElementById('pullBtn').disabled = false;
    document.getElementById('editorNote').classList.remove('warning');
    document.getElementById('editorNote').textContent = 'Decrypted on this machine — server never sees values.';
    markClean(content);
    countKeys();
    if (showToast) toast('Pulled v' + result.version + ' — ' + result.keys + ' secrets', 'success');
  } catch (error) {
    document.getElementById('editor').value = '';
    document.getElementById('editor').classList.add('disabled');
    document.getElementById('editor').placeholder = 'Secrets unavailable — project password not set. Ask the project owner to push first.';
    document.getElementById('statVersion').textContent = 'unavailable';
    document.getElementById('statVersion').classList.add('warning');
    document.getElementById('statAuthor').textContent = 'password not set';
    document.getElementById('pushBtn').disabled = true;
    document.getElementById('pullBtn').disabled = false; // Allow pull to retry
    document.getElementById('editorNote').classList.add('warning');
    document.getElementById('editorNote').textContent = '⚠ Password not set — Ask the project owner to push at least once first.';
    markClean('');
    countKeys();
    if (!handlePasswordError(error.message) && showToast) {
      toast(error.message, 'error');
    }
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
  
  // Check if password is already known to be unavailable
  const currentVersion = document.getElementById('statVersion').textContent;
  if (currentVersion === 'unavailable') {
    toast('Cannot push: project password not set. Ask owner to push first or run: dotsync init --rotate-password', 'error');
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
    document.getElementById('statVersion').classList.remove('warning');
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
    if (!handlePasswordError(error.message)) {
      toast(error.message, 'error');
    }
    // Update UI to reflect password error state
    if (error.message && (error.message.includes('password') || error.message.includes('decrypt'))) {
      document.getElementById('statVersion').textContent = 'unavailable';
      document.getElementById('statVersion').classList.add('warning');
      document.getElementById('statAuthor').textContent = 'password error';
    }
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
  
  // Check if password is available
  const currentVersion = document.getElementById('statVersion').textContent;
  const passwordUnavailable = currentVersion === 'unavailable';
  
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
      const canRollback = !isCurrent && !passwordUnavailable;
      
      let actionBadge = '';
      if (isCurrent) {
        actionBadge = '<span class="badge badge-success">current</span>';
      } else if (canRollback) {
        actionBadge = '<button class="btn btn-secondary" style="font-size: 11px; padding: 6px 10px;" onclick="rollback(' + entry.version + ')">↩ Restore</button>';
      } else {
        actionBadge = '<span class="badge badge-muted" style="opacity: 0.5;">password required</span>';
      }
      
      return '<div class="history-item">' +
        '<div class="version-badge ' + (isCurrent ? 'current' : 'old') + '">v' + entry.version + '</div>' +
        '<div style="flex: 1;">' +
          '<div style="font-family: var(--font-mono); font-size: 13px;">@' + escapeHtml(entry.pushed_by) + '</div>' +
          '<div style="font-size: 12px; color: var(--text-muted);">' + timeAgo(entry.created_at) + '</div>' +
        '</div>' +
        actionBadge +
      '</div>';
    }).join('');
  } catch (error) {
    if (handlePasswordError(error.message)) {
      body.innerHTML = '<div class="empty"><div class="empty-icon">◷</div>Password not set — cannot load history</div>';
    } else {
      body.innerHTML = '<div class="empty">' + escapeHtml(error.message) + '</div>';
    }
  }
}

async function rollback(version) {
  if (!isValidSlug(state.project)) {
    toast('No project selected', 'error');
    return;
  }
  
  if (!confirm('Restore v' + version + ' as current?\n\nThis will create a NEW version with the same content as v' + version + '. The original v' + version + ' will remain in history.')) return;
  showLoading('Creating new version from v' + version + '...');
  try {
    const result = await apiPost('/api/rollback', {
      slug: state.project,
      env: state.env,
      version
    });
    hideLoading();
    toast('Rollback complete: v' + result.version + ' now active (content from v' + version + ')', 'success');
    loadHistory();
    await pullSecrets(false);
  } catch (error) {
    hideLoading();
    if (!handlePasswordError(error.message)) {
      toast(error.message, 'error');
    }
    // Reload history to update UI state
    loadHistory();
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
