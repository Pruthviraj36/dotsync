package cmd

import (
	"bytes"
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
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/identity"
)

const (
	uiMaxBodyBytes = 1 << 20 // 1 MB — enough for any .env file
	uiReadTimeout  = 10 * time.Second
	uiWriteTimeout = 0 // disabled for SSE streaming
	uiIdleTimeout  = 60 * time.Second
	uiMaxRunOutput = 1 << 20 // 1 MB; prevents a command from exhausting the UI server
)

type uiLimitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *uiLimitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *uiLimitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	output := b.buf.String()
	if b.truncated {
		output += "\n[output truncated at 1 MB]"
	}
	return output
}

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

			// Diff is deliberately key-only, like `dotsync diff`: values never
			// appear in this response even though both sides are decrypted locally.
			mux.HandleFunc("/api/diff", uiHandler(func(r *http.Request) (any, error) {
				slug, env := r.URL.Query().Get("slug"), r.URL.Query().Get("env")
				if !validUISlug(slug) || strings.TrimSpace(env) == "" {
					return nil, fmt.Errorf("slug and env are required")
				}
				local, err := os.ReadFile(".env")
				if err != nil {
					return nil, fmt.Errorf("read local .env: %w", err)
				}
				remote, err := client.Pull(slug, env)
				if err != nil {
					return nil, err
				}
				if _, err := verifySignature(remote.EncryptedData, remote.Signature, remote.PushedByPubKey); err != nil {
					return nil, fmt.Errorf("signature verification failed: %w", err)
				}
				password, err := resolvePassword(client, slug)
				if err != nil {
					return nil, err
				}
				plain, err := cliCrypto.DecryptEnvFile(remote.EncryptedData, remote.Nonce, password, slug)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				added, removed, changed := cliCrypto.DiffEnvFiles(cliCrypto.ParseEnvFile(plain), cliCrypto.ParseEnvFile(string(local)))
				return map[string]any{"version": remote.Version, "added": added, "removed": removed, "changed": changed}, nil
			}))

			mux.HandleFunc("/api/version", uiHandler(func(r *http.Request) (any, error) {
				slug, env := r.URL.Query().Get("slug"), r.URL.Query().Get("env")
				var version int
				if _, err := fmt.Sscanf(r.URL.Query().Get("version"), "%d", &version); err != nil || !validUISlug(slug) || env == "" || version < 1 {
					return nil, fmt.Errorf("slug, env, and a positive version are required")
				}
				remote, err := client.PullVersion(slug, env, version)
				if err != nil {
					return nil, err
				}
				if _, err := verifySignature(remote.EncryptedData, remote.Signature, remote.PushedByPubKey); err != nil {
					return nil, fmt.Errorf("signature verification failed: %w", err)
				}
				password, err := resolvePassword(client, slug)
				if err != nil {
					return nil, err
				}
				plain, err := cliCrypto.DecryptEnvFile(remote.EncryptedData, remote.Nonce, password, slug)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				return map[string]any{"content": plain, "keys": len(cliCrypto.ParseEnvFile(plain)), "version": remote.Version, "by": remote.PushedBy}, nil
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
				if verified, verifyErr := verifySignature(result.EncryptedData, result.Signature, result.PushedByPubKey); verifyErr != nil {
					return nil, fmt.Errorf("signature verification failed: %w", verifyErr)
				} else if !verified && len(result.Signature) > 0 {
					return nil, fmt.Errorf("refusing to decrypt an unverified version")
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
				signature, _, pub, err := ensureIdentityAndSign(ciphertext)
				if err != nil {
					return nil, fmt.Errorf("sign encryption: %w", err)
				}
				// A failed public-key sync should not discard a valid local push; it
				// will be retried by the next signed operation, matching `dotsync push`.
				_ = client.SetPubKey(identity.Hex(pub))
				result, err := client.Push(req.Slug, req.Env, api.PushRequest{
					EncryptedData: ciphertext,
					Nonce:         nonce,
					Signature:     signature,
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
				if verified, verifyErr := verifySignature(old.EncryptedData, old.Signature, old.PushedByPubKey); verifyErr != nil {
					return nil, fmt.Errorf("signature verification failed: %w", verifyErr)
				} else if !verified && len(old.Signature) > 0 {
					return nil, fmt.Errorf("refusing to roll back to an unverified version")
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
				signature, _, pub, err := ensureIdentityAndSign(ciphertext)
				if err != nil {
					return nil, fmt.Errorf("sign encryption: %w", err)
				}
				_ = client.SetPubKey(identity.Hex(pub))
				result, err := client.Push(req.Slug, req.Env, api.PushRequest{
					EncryptedData: ciphertext,
					Nonce:         nonce,
					Signature:     signature,
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"version": result.Version}, nil
			}))

			// Keep the UI's project context in sync with the CLI's .dotsync.json.
			// This is intentionally explicit: selecting a project in the dashboard
			// does not silently relink the folder.
			mux.HandleFunc("/api/workspace", uiHandler(func(r *http.Request) (any, error) {
				project, err := config.LoadProject()
				if err != nil {
					return map[string]any{"linked": false}, nil
				}
				return map[string]any{"linked": true, "project": project}, nil
			}))

			mux.HandleFunc("/api/workspace/link", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug string `json:"slug"`
					Env  string `json:"env"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || strings.TrimSpace(req.Env) == "" {
					return nil, fmt.Errorf("slug and environment are required")
				}
				if err := config.SaveProject(&config.ProjectConfig{ProjectSlug: strings.TrimSpace(req.Slug), DefaultEnv: strings.TrimSpace(req.Env)}); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			}))

			// This is the browser counterpart of `dotsync run -- <command>`.
			// It never writes the decrypted environment to disk; it is passed only
			// to the child process. A bounded context prevents a stale dashboard
			// request from leaving a command running forever.
			mux.HandleFunc("/api/run", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug string   `json:"slug"`
					Env  string   `json:"env"`
					Args []string `json:"args"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || strings.TrimSpace(req.Env) == "" || len(req.Args) == 0 || strings.TrimSpace(req.Args[0]) == "" {
					return nil, fmt.Errorf("project, environment, and command are required")
				}
				remote, err := client.Pull(req.Slug, req.Env)
				if err != nil {
					return nil, err
				}
				if _, err := verifySignature(remote.EncryptedData, remote.Signature, remote.PushedByPubKey); err != nil {
					return nil, fmt.Errorf("signature verification failed: %w", err)
				}
				password, err := resolvePassword(client, req.Slug)
				if err != nil {
					return nil, err
				}
				plain, err := cliCrypto.DecryptEnvFile(remote.EncryptedData, remote.Nonce, password, req.Slug)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				env := os.Environ()
				for key, value := range cliCrypto.ParseEnvFile(plain) {
					env = append(env, key+"="+value)
				}
				runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				command := exec.CommandContext(runCtx, req.Args[0], req.Args[1:]...)
				command.Env = env
				output := &uiLimitedBuffer{limit: uiMaxRunOutput}
				command.Stdout, command.Stderr = output, output
				runErr := command.Run()
				result := map[string]any{"output": output.String(), "exit_code": 0}
				if runErr != nil {
					result["exit_code"] = 1
					result["run_error"] = runErr.Error()
					if runCtx.Err() != nil {
						result["run_error"] = "command timed out after 2 minutes"
					}
				}
				return result, nil
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


/* ---------------------------------------------------------------------------
   DotSync visual system: "local operator console"
   Visual-only pass. No markup, JavaScript, handlers, or API behavior changed.
   --------------------------------------------------------------------------- */

:root {
  --bg-deep: #11110f;
  --bg-surface: #191916;
  --bg-elevated: #20201c;
  --bg-input: #0d0d0b;

  --border-subtle: #2c2c27;
  --border-default: #414139;
  --border-focus: #d56b3d;

  --text-primary: #ece9df;
  --text-secondary: #aaa89e;
  --text-tertiary: #706f68;

  --accent-primary: #d56b3d;
  --accent-secondary: #e29a62;
  --accent-glow: rgba(213, 107, 61, 0.11);

  --success: #72c4a5;
  --success-subtle: rgba(114, 196, 165, 0.10);
  --warning: #d6ae61;
  --warning-subtle: rgba(214, 174, 97, 0.10);
  --error: #d36a62;
  --error-subtle: rgba(211, 106, 98, 0.10);

  --radius-xs: 0;
  --radius-sm: 2px;
  --radius-md: 2px;
  --radius-lg: 3px;

  --shadow-subtle: none;
  --shadow-medium: 0 8px 24px rgba(0, 0, 0, 0.18);
  --shadow-elevated: 0 18px 45px rgba(0, 0, 0, 0.28);

  --header-height: 58px;
  --sidebar-width: 214px;

  --text-muted: var(--text-tertiary);
  --accent: var(--accent-primary);
}

/* Base */
html,
body {
  background: var(--bg-deep);
  color: var(--text-primary);
  font-family: var(--font-display);
  font-size: 13px;
  line-height: 1.55;
}

body {
  background:
    linear-gradient(90deg, rgba(255,255,255,0.012) 1px, transparent 1px) 0 0 / 32px 32px,
    var(--bg-deep);
}

::selection {
  background: var(--accent-primary);
  color: #17120f;
}

::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

::-webkit-scrollbar-track {
  background: #0e0e0c;
}

::-webkit-scrollbar-thumb {
  background: #383832;
  border: 2px solid #0e0e0c;
  border-radius: 0;
}

::-webkit-scrollbar-thumb:hover {
  background: #56564e;
}

/* Header: compact command strip */
.header {
  height: var(--header-height);
  padding: 0 18px;
  gap: 14px;
  background: rgba(17, 17, 15, 0.97);
  border-bottom: 1px solid var(--border-default);
  box-shadow: 0 1px 0 rgba(255,255,255,0.025);
}

.logo {
  min-width: 112px;
  color: var(--text-primary);
  font-family: var(--font-display);
  font-size: 15px;
  font-weight: 700;
  letter-spacing: -0.035em;
  gap: 9px;
}

.logo::after {
  content: "LOCAL";
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 8px;
  font-weight: 500;
  letter-spacing: 0.12em;
  margin-left: 2px;
}

.logo-icon {
  width: 20px;
  height: 20px;
  border-radius: 1px;
  background: var(--accent-primary);
  color: #17120f;
  font-size: 8px;
  box-shadow: none;
}

.header-divider {
  height: 22px;
  background: var(--border-default);
}

.project-select {
  min-width: 145px;
  max-width: 190px;
  padding: 7px 28px 7px 9px;
  border: 1px solid var(--border-default);
  border-radius: 1px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 11px;
}

.project-select:hover {
  border-color: #595951;
}

.project-select:focus {
  border-color: var(--accent-primary);
  box-shadow: 0 0 0 1px var(--accent-primary);
}

.env-tabs {
  align-self: stretch;
  align-items: center;
  gap: 0;
}

.env-tab {
  height: 100%;
  display: inline-flex;
  align-items: center;
  padding: 0 11px;
  border: 0;
  border-bottom: 2px solid transparent;
  border-radius: 0;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 10px;
  letter-spacing: 0.015em;
}

.env-tab:hover {
  color: var(--text-primary);
  background: rgba(255,255,255,0.025);
  border-color: transparent;
}

.env-tab.active {
  color: var(--accent-secondary);
  background: transparent;
  border-color: var(--accent-primary);
}

.header-right {
  gap: 12px;
}

.status-badge {
  padding: 5px 8px;
  border-radius: 1px;
  background: transparent;
  border: 1px solid var(--border-default);
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 9px;
  text-transform: lowercase;
}

.status-badge.connected {
  border-color: rgba(114,196,165,0.45);
  background: var(--success-subtle);
  color: var(--success);
}

.status-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
}

.status-badge.connected .status-dot {
  box-shadow: 0 0 0 3px rgba(114,196,165,0.08);
}

.user-avatar {
  width: 28px;
  height: 28px;
  border-radius: 1px;
  background: #25251f;
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
}

/* Main shell */
.container {
  height: calc(100vh - var(--header-height));
}

.sidebar {
  width: var(--sidebar-width);
  padding: 18px 10px 12px;
  background: #151512;
  border-right: 1px solid var(--border-default);
  gap: 24px;
}

.nav-section {
  gap: 1px;
}

.nav-section-title {
  padding: 7px 11px 7px;
  color: #5e5d56;
  font-family: var(--font-mono);
  font-size: 9px;
  font-weight: 500;
  letter-spacing: 0.06em;
  text-transform: none;
}

.nav-item {
  min-height: 34px;
  padding: 7px 11px;
  border: 1px solid transparent;
  border-radius: 1px;
  color: #929087;
  font-size: 12px;
  font-weight: 500;
  gap: 9px;
}

.nav-item:hover {
  background: #1c1c18;
  color: var(--text-primary);
  border-color: #292923;
}

.nav-item.active {
  padding-left: 10px;
  color: var(--text-primary);
  background: #211d19;
  border: 1px solid #493026;
  border-left: 2px solid var(--accent-primary);
}

.nav-icon {
  width: 16px;
  color: var(--text-tertiary);
  opacity: 1;
}

.nav-item.active .nav-icon {
  color: var(--accent-secondary);
}

.nav-badge {
  padding: 1px 5px;
  border: 1px solid #36362f;
  border-radius: 1px;
  background: #181814;
  color: #85837a;
  font-size: 8px;
}

.sidebar-footer {
  padding: 13px 11px 0;
  border-top: 1px solid var(--border-subtle);
  color: #64635d;
  font-size: 9px;
}

.main {
  padding: 26px 30px 40px;
  background:
    linear-gradient(180deg, rgba(255,255,255,0.012), transparent 170px),
    var(--bg-deep);
}

.page {
  animation: none;
}

.page.active {
  max-width: 1180px;
  margin: 0 auto;
}

/* Stats: numbers first, almost no card chrome */
.stats-grid {
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 1px;
  margin-bottom: 22px;
  background: var(--border-default);
  border: 1px solid var(--border-default);
}

.stat-card {
  min-height: 116px;
  padding: 17px 18px;
  background: #181815;
  border: 0;
  border-radius: 0;
}

.stat-card::before {
  display: none;
}

.stat-card:hover {
  border-color: transparent;
  background: #1b1b17;
}

.stat-label {
  margin-bottom: 13px;
  color: #6f6e67;
  font-family: var(--font-mono);
  font-size: 9px;
  font-weight: 500;
  letter-spacing: 0.035em;
  text-transform: none;
}

.stat-value {
  font-size: 26px;
  letter-spacing: -0.04em;
  line-height: 1;
}

.stat-sub {
  margin-top: 10px;
  color: #68675f;
  font-size: 10px;
}

/* Content surfaces */
.card {
  margin-bottom: 14px;
  overflow: hidden;
  background: #181815;
  border: 1px solid var(--border-default);
  border-radius: 2px;
  box-shadow: none;
}

.card-header {
  min-height: 52px;
  padding: 11px 15px;
  background: #1d1d19;
  border-bottom: 1px solid var(--border-default);
}

.card-title {
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: -0.01em;
}

.card-actions {
  gap: 5px;
}

.card-body {
  padding: 17px;
}

/* Buttons: tactile, not pill-shaped */
.btn {
  min-height: 30px;
  padding: 6px 10px;
  border: 1px solid #45453d;
  border-radius: 1px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: -0.005em;
  transition: background-color 100ms ease, border-color 100ms ease, color 100ms ease;
}

.btn:hover {
  transform: none;
}

.btn:active {
  transform: translateY(1px);
}

.btn:focus-visible,
.input:focus-visible,
.select:focus-visible,
.search-input:focus-visible,
.project-select:focus-visible,
.editor:focus-visible {
  outline: 2px solid var(--accent-primary);
  outline-offset: 1px;
}

.btn-primary {
  background: var(--accent-primary);
  border-color: var(--accent-primary);
  color: #17120f;
}

.btn-primary:hover {
  background: #e07b4a;
  border-color: #e07b4a;
}

.btn-secondary {
  background: #20201c;
  border-color: #42423a;
  color: #d4d1c7;
}

.btn-secondary:hover {
  background: #292923;
  border-color: #5a5a50;
  color: #f0ede3;
}

.btn-success {
  background: var(--success);
  border-color: var(--success);
  color: #101713;
}

.btn-success:hover {
  filter: none;
  background: #87cfb2;
}

.btn-danger {
  border-color: rgba(211,106,98,0.25);
  color: var(--error);
}

.btn-danger:hover {
  background: var(--error-subtle);
  border-color: rgba(211,106,98,0.55);
}

/* Forms */
.input,
.select,
.search-input {
  min-height: 32px;
  background: #0e0e0c;
  border: 1px solid #3a3a33;
  border-radius: 1px;
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 11px;
}

.input,
.select {
  padding: 7px 9px;
}

.search-input {
  padding: 7px 10px;
}

.input::placeholder,
.search-input::placeholder {
  color: #55544e;
}

.input:hover,
.select:hover,
.search-input:hover {
  border-color: #52524a;
}

.input:focus,
.select:focus,
.search-input:focus {
  border-color: var(--accent-primary);
  box-shadow: none;
}

/* Editor: make the primary workflow visually dominant */
.editor-container {
  position: relative;
}

.editor-toolbar {
  min-height: 32px;
  margin-bottom: 8px;
  gap: 5px;
}

.editor-toolbar .search-input {
  background: #10100e;
}

.editor {
  min-height: 480px;
  padding: 17px 18px;
  background:
    linear-gradient(90deg, rgba(255,255,255,0.025) 1px, transparent 1px) 0 0 / 48px 24px,
    #0b0b09;
  border: 1px solid #3a3a32;
  border-radius: 1px;
  color: #d8d5cb;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.75;
  tab-size: 2;
  caret-color: var(--accent-secondary);
}

.editor::placeholder {
  color: #4d4c46;
}

.editor:focus {
  border-color: #655247;
  box-shadow: inset 3px 0 0 var(--accent-primary);
}

.editor-note {
  position: relative;
  margin-bottom: 11px;
  padding: 7px 10px 7px 11px;
  border-left: 2px solid var(--success);
  background: var(--success-subtle);
  color: #9db9ad;
  font-family: var(--font-mono);
  font-size: 9px;
}

.dirty-indicator {
  padding: 2px 5px;
  border: 1px solid rgba(214,174,97,0.35);
  border-radius: 1px;
  background: var(--warning-subtle);
  color: var(--warning);
  font-family: var(--font-mono);
  font-size: 8px;
}

/* Badges */
.badge {
  padding: 2px 5px;
  border: 1px solid #3b3b34;
  border-radius: 1px;
  font-family: var(--font-mono);
  font-size: 8px;
  font-weight: 500;
}

.badge-accent {
  background: var(--accent-glow);
  border-color: rgba(213,107,61,0.35);
  color: var(--accent-secondary);
}

.badge-muted {
  background: #151512;
  color: #77766f;
}

.badge-success {
  background: var(--success-subtle);
  border-color: rgba(114,196,165,0.3);
  color: var(--success);
}

.version-badge {
  padding: 2px 5px;
  border-radius: 1px;
  background: #22221d;
  border: 1px solid #3b3b34;
  color: #9b998f;
  font-family: var(--font-mono);
  font-size: 9px;
}

/* Lists and history */
.list-item,
.history-item {
  border-color: var(--border-subtle);
  border-radius: 0;
  background: transparent;
}

.list-item:hover,
.history-item:hover {
  background: #1d1d19;
}

.history-item {
  padding: 12px 10px;
}

.avatar-small {
  border-radius: 1px;
}

.token-reveal {
  margin-bottom: 12px;
  padding: 12px;
  background: #11110e;
  border: 1px solid rgba(213,107,61,0.35);
  border-left: 3px solid var(--accent-primary);
  border-radius: 1px;
  box-shadow: none;
}

.token-reveal code {
  color: #e3b08d;
}

/* Empty / loading states */
.empty {
  min-height: 170px;
  padding: 32px 20px;
  border: 1px dashed #34342e;
  border-radius: 1px;
  background: #151512;
  color: #76756d;
}

.empty-icon {
  color: #56554f;
  font-size: 25px;
}

.loading-overlay {
  background: rgba(9, 9, 8, 0.82);
  backdrop-filter: blur(3px);
}

.spinner {
  width: 25px;
  height: 25px;
  border-radius: 50%;
  border: 2px solid #393932;
  border-top-color: var(--accent-primary);
}

.loading-text {
  color: #aaa79d;
  font-family: var(--font-mono);
  font-size: 10px;
}

/* Toasts */
.toast-container {
  right: 18px;
  bottom: 18px;
}

.toast {
  border: 1px solid #44443b;
  border-radius: 1px;
  background: #1b1b17;
  box-shadow: var(--shadow-medium);
  font-family: var(--font-display);
}

/* Responsive */
@media (max-width: 900px) {
  .sidebar {
    width: 190px;
  }

  .main {
    padding: 22px 20px 32px;
  }
}

@media (max-width: 768px) {
  .header {
    padding: 0 12px;
  }

  .logo {
    min-width: auto;
  }

  .logo::after,
  .header-divider {
    display: none;
  }

  .sidebar {
    box-shadow: 14px 0 30px rgba(0,0,0,0.3);
  }

  .main {
    padding: 18px 14px 28px;
  }

  .stats-grid {
    grid-template-columns: 1fr;
  }

  .stat-card {
    min-height: 92px;
  }

  .card-header {
    align-items: flex-start;
  }

  .card-actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .editor {
    min-height: 390px;
  }
}

@media (max-width: 560px) {
  .header {
    gap: 8px;
  }

  .project-select {
    min-width: 105px;
    max-width: 125px;
  }

  .env-tabs {
    max-width: 35vw;
  }

  .header-right .status-badge {
    display: none;
  }

  .card-header {
    flex-direction: column;
  }

  .card-actions {
    width: 100%;
    justify-content: flex-start;
  }

  .form-row {
    flex-wrap: wrap;
  }

  .form-row .input {
    min-width: 100%;
  }

  .editor {
    min-height: 330px;
    font-size: 11px;
  }
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.001ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.001ms !important;
    scroll-behavior: auto !important;
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
      <div class="nav-item" data-page="workspace" onclick="navigate(this, 'workspace')">
        <span class="nav-icon">⌘</span>
        Workspace
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

    <!-- Workspace Page -->
    <div class="page" id="page-workspace">
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-label">Local state</div>
          <div class="stat-value" id="workspaceState">—</div>
          <div class="stat-sub">.dotsync.json</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Remote comparison</div>
          <div class="stat-value" id="diffSummary">—</div>
          <div class="stat-sub">key names only</div>
        </div>
      </div>
      <div class="card">
        <div class="card-header">
          <div class="card-title">Local .env vs remote</div>
          <div class="card-actions"><button class="btn btn-secondary" onclick="loadDiff()">⇄ Compare</button></div>
        </div>
        <div class="card-body" id="diffBody"><div class="empty">Compare your on-disk <code>.env</code> with the selected remote environment. Values are never displayed.</div></div>
      </div>
      <div class="card">
        <div class="card-header"><div class="card-title">Link this workspace</div></div>
        <div class="card-body">
          <div class="form-row">
            <select class="select" id="workspaceProject" style="flex: 1;"></select>
            <select class="select" id="workspaceEnv" style="width: 150px;"></select>
            <button class="btn btn-primary" onclick="linkWorkspace()">Link folder</button>
          </div>
          <p class="editor-note" style="margin-top: 12px;">Updates only this folder’s <code>.dotsync.json</code>; it does not change the selected dashboard project.</p>
        </div>
      </div>
      <div class="card">
        <div class="card-header"><div class="card-title">Run with secrets</div><span class="badge badge-muted">zero disk</span></div>
        <div class="card-body">
          <div class="form-row">
            <input class="input" id="runCommand" placeholder="npm test" style="flex:1; font-family:var(--font-mono);" onkeydown="if(event.key==='Enter')runWithSecrets()">
            <button class="btn btn-success" onclick="runWithSecrets()">▶ Run</button>
          </div>
          <div class="editor-note" style="margin-top:12px;">The selected environment is injected into the child process only. Commands run in this workspace and stop after 2 minutes.</div>
          <pre id="runOutput" class="editor" style="min-height:80px;max-height:260px;margin-top:12px;white-space:pre-wrap;" aria-live="polite">Output will appear here.</pre>
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
      const inspect = '<button class="btn btn-secondary" style="font-size: 11px; padding: 6px 10px;" onclick="inspectVersion(' + entry.version + ')">View</button>';
      if (isCurrent) {
        actionBadge = inspect + '<span class="badge badge-success">current</span>';
      } else if (canRollback) {
        actionBadge = inspect + '<button class="btn btn-secondary" style="font-size: 11px; padding: 6px 10px;" onclick="rollback(' + entry.version + ')">↩ Restore</button>';
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

async function inspectVersion(version) {
  if (!confirmDiscard()) return;
  showLoading('Decrypting v' + version + ' locally...');
  try {
    const result = await apiCall('/api/version?' + buildQuery(state.project, state.env) + '&version=' + encodeURIComponent(version));
    document.getElementById('editor').value = result.content || '';
    document.getElementById('statVersion').textContent = 'v' + result.version + ' preview';
    document.getElementById('statAuthor').textContent = result.by ? '@' + result.by : '';
    countKeys();
    // A preview must never be mistaken for the current editor baseline.
    state.baseline = '__preview__';
    setDirtyUI();
    navigate(document.querySelector('.nav-item[data-page="secrets"]'), 'secrets');
    toast('Viewing v' + version + ' — push to make a new current version', 'info');
  } catch (error) { toast(error.message, 'error'); }
  finally { hideLoading(); }
}

async function loadWorkspace() {
  const projectSelect = document.getElementById('workspaceProject');
  const envSelect = document.getElementById('workspaceEnv');
  const projects = Array.from(document.getElementById('projectSelect').options).filter(o => isValidSlug(o.value));
  projectSelect.innerHTML = projects.map(o => '<option value="' + escapeHtml(o.value) + '">' + escapeHtml(o.textContent) + '</option>').join('');
  projectSelect.value = state.project || '';
  const envs = normalizeEnvs(state.data && state.data.envs);
  envSelect.innerHTML = envs.map(e => '<option value="' + escapeHtml(e) + '">' + escapeHtml(e) + '</option>').join('');
  envSelect.value = state.env;
  try {
    const workspace = await apiCall('/api/workspace');
    const linked = workspace.linked && workspace.project;
    document.getElementById('workspaceState').textContent = linked ? 'linked' : 'unlinked';
    if (linked) {
      projectSelect.value = workspace.project.project_slug || state.project;
      envSelect.value = workspace.project.default_env || state.env;
    }
  } catch (error) { toast(error.message, 'error'); }
}

async function linkWorkspace() {
  const slug = document.getElementById('workspaceProject').value;
  const env = document.getElementById('workspaceEnv').value;
  try {
    await apiPost('/api/workspace/link', { slug, env });
    document.getElementById('workspaceState').textContent = 'linked';
    toast('Workspace linked to ' + slug + '/' + env, 'success');
  } catch (error) { toast(error.message, 'error'); }
}

async function loadDiff() {
  const body = document.getElementById('diffBody');
  body.innerHTML = '<div class="empty"><span class="spinner"></span></div>';
  try {
    const result = await apiCall('/api/diff?' + buildQuery(state.project, state.env));
    const rows = [];
    (result.added || []).forEach(k => rows.push('<span style="color:var(--success)">+ ' + escapeHtml(k) + '</span>'));
    (result.removed || []).forEach(k => rows.push('<span style="color:var(--error)">− ' + escapeHtml(k) + '</span>'));
    (result.changed || []).forEach(k => rows.push('<span style="color:var(--warning)">~ ' + escapeHtml(k) + '</span>'));
    document.getElementById('diffSummary').textContent = rows.length ? rows.length + ' changes' : 'in sync';
    body.innerHTML = rows.length ? '<div style="display:grid;gap:6px;font-family:var(--font-mono);font-size:12px;">' + rows.join('') + '</div>' : '<div class="empty">✓ Local .env is in sync with remote v' + result.version + '.</div>';
  } catch (error) {
    document.getElementById('diffSummary').textContent = 'unavailable';
    body.innerHTML = '<div class="empty">' + escapeHtml(error.message) + '</div>';
  }
}

// Simple shell-like argument splitting deliberately supports quoted paths and
// arguments, but does not invoke a shell. That keeps the run action equivalent to the
// CLI argv execution and avoids shell interpolation surprises.
function commandArgs(input) {
  const args = [];
  let token = '', quote = null, escaped = false;
  for (const char of input.trim()) {
    if (escaped) { token += char; escaped = false; continue; }
    if (char === '\\') { escaped = true; continue; }
    if (quote) { if (char === quote) quote = null; else token += char; continue; }
    if (char === '"' || char === "'") { quote = char; continue; }
    if (/\s/.test(char)) { if (token) { args.push(token); token = ''; } continue; }
    token += char;
  }
  if (escaped || quote) throw new Error('Command has an unfinished quote or escape');
  if (token) args.push(token);
  return args;
}

async function runWithSecrets() {
  const output = document.getElementById('runOutput');
  try {
    const args = commandArgs(document.getElementById('runCommand').value);
    if (!args.length) throw new Error('Enter a command to run');
    if (!confirm('Run "' + args.join(' ') + '" with ' + state.project + '/' + state.env + ' secrets?')) return;
    output.textContent = 'Running…';
    const result = await apiPost('/api/run', { slug: state.project, env: state.env, args });
    output.textContent = (result.output || '') + (result.run_error ? '\n' + result.run_error : '');
    toast(result.run_error ? 'Command exited with an error' : 'Command completed', result.run_error ? 'error' : 'success');
  } catch (error) { output.textContent = error.message; toast(error.message, 'error'); }
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
  if (page === 'workspace') loadWorkspace();
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
