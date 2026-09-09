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
			projCfg, err := config.LoadProject()
			if err != nil {
				return fmt.Errorf("cannot open the web UI: this folder is not linked to a DotSync project — run: dotsync init")
			}
			if !validUISlug(projCfg.ProjectSlug) {
				return fmt.Errorf("cannot open the web UI: the local project link is invalid — run: dotsync init")
			}
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
				logs = filterUIAuditLogs(logs)
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
					if err := client.UpdateTeamRole(req.Slug, req.Username, req.Role); err != nil {
						return nil, err
					}
				}
				return map[string]any{"ok": true}, nil
			}))

			mux.HandleFunc("/api/team/role", uiPostHandler(func(body []byte) (any, error) {
				var req struct {
					Slug     string `json:"slug"`
					Username string `json:"username"`
					Role     string `json:"role"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("invalid request: %w", err)
				}
				if !validUISlug(req.Slug) || req.Username == "" || req.Role == "" {
					return nil, fmt.Errorf("slug, username, and role are required")
				}
				if req.Role == "owner" {
					return nil, fmt.Errorf("the owner role cannot be assigned")
				}
				if err := client.UpdateTeamRole(req.Slug, req.Username, req.Role); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true, "role": req.Role}, nil
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
			json.NewEncoder(w).Encode(map[string]string{"error": uiSafeError(err)})
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
			json.NewEncoder(w).Encode(map[string]string{"error": uiSafeError(err)})
			return
		}
		json.NewEncoder(w).Encode(data)
	}
}

// uiSafeError keeps implementation details out of the browser. In particular,
// the local dashboard should describe a missing encryption setup as an action,
// never expose the internal password-fetch wording used by the CLI.
func uiSafeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	lower := strings.ToLower(message)
	if strings.Contains(lower, "request failed") || strings.Contains(lower, "connection refused") {
		return "DotSync could not reach the server. Check your connection and try again."
	}
	if strings.Contains(lower, "session expired") || strings.Contains(lower, "unauthorized") {
		return "Your DotSync session has expired. Run dotsync login, then reopen the UI."
	}
	if strings.Contains(lower, "no project slug") || strings.Contains(lower, "not a dotsync project") {
		return "This folder is not linked to a DotSync project. Run dotsync init, then reopen the UI."
	}
	if strings.Contains(lower, "read local .env") || strings.Contains(lower, "no such file or directory") {
		return "No local .env file was found. Pull the current environment first, then compare again."
	}
	if strings.Contains(lower, "password") || strings.Contains(lower, "decrypt") {
		return "Secrets are unavailable for this project. Ask a project owner to publish the first version, then try again."
	}
	return message
}

func filterUIAuditLogs(logs []map[string]any) []map[string]any {
	filtered := make([]map[string]any, 0, len(logs))
	for _, log := range logs {
		action := strings.ToLower(fmt.Sprint(log["action"]))
		action = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(action)
		passwordRead := strings.Contains(action, "password") && (strings.Contains(action, "fetch") || strings.Contains(action, "get") || strings.Contains(action, "read") || strings.Contains(action, "retriev"))
		internalPasswordEvent := action == "password_fetch" || action == "fetch_project_password" || action == "project_password_read"
		if passwordRead || internalPasswordEvent {
			continue
		}
		filtered = append(filtered, log)
	}
	return filtered
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

const legacyDashboardHTML = `<!DOCTYPE html>
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
  if (message && message.includes('Secrets are unavailable')) {
    toast('Secrets are unavailable. Ask a project owner to publish the first version, then try again.', 'error');
    return true;
  }
  if (message && message.includes('no password set for this project')) {
    toast('Secrets are unavailable. Ask a project owner to publish the first version, then try again.', 'error');
    return true;
  }
  if (message && message.includes('decryption failed')) {
    toast('Secrets could not be opened. Ask a project owner to verify the project setup.', 'error');
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
    document.getElementById('editor').placeholder = 'Secrets unavailable — ask a project owner to publish the first version.';
    document.getElementById('statVersion').textContent = 'unavailable';
    document.getElementById('statVersion').classList.add('warning');
    document.getElementById('statAuthor').textContent = 'setup required';
    document.getElementById('pushBtn').disabled = true;
    document.getElementById('pullBtn').disabled = false; // Allow pull to retry
    document.getElementById('editorNote').classList.add('warning');
    document.getElementById('editorNote').textContent = '⚠ Secrets are unavailable — ask a project owner to publish the first version.';
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
    toast('Cannot push yet. Ask a project owner to publish the first version, then try again.', 'error');
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
        actionBadge = '<span class="badge badge-muted" style="opacity: 0.5;">setup required</span>';
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
      body.innerHTML = '<div class="empty"><div class="empty-icon">◷</div>Secrets are unavailable — publish the first version to enable history.</div>';
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

// dashboardHTML is intentionally a standalone browser application. The CLI
// serves it from localhost, so it can keep the visual experience rich without
// moving any plaintext secret off this machine.
const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>DotSync · local control room</title>
<style>
:root{--ink:#e9edf5;--muted:#8993a8;--faint:#566075;--bg:#090d16;--panel:#101725;--panel2:#151f30;--line:#263249;--blue:#6ea8fe;--violet:#a78bfa;--green:#63d6a2;--amber:#f5c76b;--red:#ff7f91;--mono:ui-monospace,SFMono-Regular,Menlo,monospace;--sans:Inter,ui-sans-serif,system-ui,sans-serif}
*{box-sizing:border-box}body{margin:0;background:radial-gradient(900px 500px at 85% -10%,#18284e 0,transparent 65%),var(--bg);color:var(--ink);font:14px/1.55 var(--sans)}button,input,select,textarea{font:inherit}button{cursor:pointer}.shell{min-height:100vh;display:grid;grid-template-columns:248px 1fr}.rail{border-right:1px solid var(--line);background:rgba(8,12,20,.78);padding:28px 18px;display:flex;flex-direction:column;gap:28px}.brand{display:flex;align-items:center;gap:12px;font-weight:750;font-size:17px;letter-spacing:-.03em}.mark{width:30px;height:30px;border-radius:9px;background:linear-gradient(135deg,var(--blue),var(--violet));display:grid;place-items:center;color:#07101f;font-weight:900}.caption{font:11px var(--mono);color:var(--faint);text-transform:uppercase;letter-spacing:.14em}.nav{display:grid;gap:5px}.nav button{border:0;background:transparent;color:var(--muted);padding:11px 12px;border-radius:10px;text-align:left;display:flex;gap:10px;align-items:center}.nav button:hover,.nav button.active{background:var(--panel2);color:var(--ink)}.nav button.active{box-shadow:inset 3px 0 var(--blue)}.rail-foot{margin-top:auto;color:var(--faint);font:11px/1.7 var(--mono)}.main{min-width:0}.top{height:76px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:12px;padding:0 34px;position:sticky;top:0;background:rgba(9,13,22,.84);backdrop-filter:blur(16px);z-index:2}.top select,.control{background:var(--panel);border:1px solid var(--line);color:var(--ink);border-radius:8px;padding:9px 11px}.envs{display:flex;gap:5px;overflow:auto}.env{border:1px solid transparent;background:transparent;color:var(--muted);padding:8px 11px;border-radius:8px;font:12px var(--mono)}.env.active{border-color:#3e65a0;background:#12223d;color:var(--blue)}.top-right{margin-left:auto;display:flex;align-items:center;gap:10px;color:var(--muted);font:11px var(--mono)}.dot{width:7px;height:7px;border-radius:50%;background:var(--green);box-shadow:0 0 12px var(--green)}.content{max-width:1180px;padding:34px; margin:0 auto}.view{display:none}.view.active{display:block}.hero{display:flex;justify-content:space-between;align-items:flex-end;gap:20px;margin-bottom:28px}.eyebrow{color:var(--blue);font:11px var(--mono);letter-spacing:.12em;text-transform:uppercase}.hero h1{font-size:31px;line-height:1.1;margin:8px 0 0;letter-spacing:-.05em}.hero p{color:var(--muted);margin:9px 0 0}.actions{display:flex;gap:8px;flex-wrap:wrap}.btn{border:1px solid var(--line);background:var(--panel);color:var(--ink);border-radius:8px;padding:9px 13px}.btn:hover{border-color:#5475a9;background:var(--panel2)}.btn.primary{background:var(--blue);border-color:var(--blue);color:#07101f;font-weight:700}.btn.good{background:#153b31;border-color:#2e8065;color:var(--green)}.btn.danger{color:var(--red)}.grid{display:grid;gap:16px}.stats{grid-template-columns:repeat(4,minmax(0,1fr));margin-bottom:16px}.card{background:linear-gradient(145deg,rgba(16,23,37,.96),rgba(13,19,31,.92));border:1px solid var(--line);border-radius:14px;padding:20px;box-shadow:0 18px 50px #0002}.stat-label{color:var(--faint);font:11px var(--mono);text-transform:uppercase;letter-spacing:.1em}.stat-value{font-size:26px;font-weight:700;margin-top:7px}.stat-note{color:var(--muted);font-size:12px;margin-top:3px}.card-head{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:16px}.card-title{font-size:16px;font-weight:700}.muted{color:var(--muted)}.editor{width:100%;min-height:420px;resize:vertical;background:#080c14;color:#d7e2f4;border:1px solid var(--line);border-radius:10px;padding:18px;font:13px/1.7 var(--mono);outline:0}.editor:focus{border-color:#5276ac}.toolbar{display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:10px}.search{flex:1;max-width:300px}.input{background:#0b111d;border:1px solid var(--line);color:var(--ink);border-radius:8px;padding:10px 12px;outline:0}.input:focus{border-color:var(--blue)}.form{display:flex;gap:8px;flex-wrap:wrap}.form .input,.form select{flex:1;min-width:160px}.list{display:grid;gap:8px}.row{border:1px solid var(--line);border-radius:10px;padding:13px 14px;display:flex;align-items:center;gap:13px;background:#0d1421}.row-main{min-width:0;flex:1}.row-title{font-weight:650}.row-sub{color:var(--muted);font-size:12px;margin-top:2px}.pill{font:11px var(--mono);padding:4px 8px;border-radius:99px;background:#1b2940;color:var(--blue)}.pill.good{background:#153b31;color:var(--green)}.pill.warn{background:#40351b;color:var(--amber)}.code{font:12px var(--mono);color:var(--blue);word-break:break-all}.empty{border:1px dashed var(--line);border-radius:10px;padding:32px;text-align:center;color:var(--muted)}.notice{padding:12px 14px;border-radius:9px;background:#152238;color:var(--muted);margin-bottom:14px}.notice.warn{background:#382d16;color:var(--amber)}.toast{position:fixed;right:24px;bottom:24px;z-index:5;background:#172338;border:1px solid #46638f;border-radius:9px;padding:12px 15px;box-shadow:0 16px 45px #0007}.toast.error{border-color:#a04457;color:#ffb0bc}.run-output{min-height:120px;max-height:280px;overflow:auto;white-space:pre-wrap;background:#080c14;border:1px solid var(--line);border-radius:9px;padding:14px;font:12px/1.6 var(--mono);color:#c4d1e6}@media(max-width:900px){.shell{grid-template-columns:1fr}.rail{position:static;border-right:0;border-bottom:1px solid var(--line);padding:16px;gap:14px}.nav{display:flex;overflow:auto}.rail-foot{display:none}.top{padding:0 16px}.content{padding:22px 16px}.stats{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:560px){.hero{display:block}.hero .actions{margin-top:18px}.stats{grid-template-columns:1fr}.top{height:auto;min-height:70px;flex-wrap:wrap;padding:12px 16px}.top-right{margin-left:0}.editor{min-height:340px}}
</style></head><body>
<div class="shell"><aside class="rail"><div class="brand"><span class="mark">D</span> DotSync</div><div><div class="caption">Control room</div><nav class="nav"><button class="active" data-view="secrets">◆ <span>Secrets</span></button><button data-view="history">◷ <span>History</span></button><button data-view="team">◎ <span>Team</span></button><button data-view="tokens">⌘ <span>Tokens</span></button><button data-view="audit">≡ <span>Audit</span></button><button data-view="workspace">⌂ <span>Workspace</span></button><button data-view="guide">? <span>Guide</span></button></nav></div><div class="rail-foot">Encrypted locally<br>Values never leave this machine<br><br><span id="serverText">—</span></div></aside>
<main class="main"><header class="top"><select id="project"></select><div class="envs" id="envs"></div><div class="top-right"><span class="dot"></span><span id="user">local</span></div></header><section class="content">
<div class="view active" id="view-secrets"><div class="hero"><div><div class="eyebrow">Encrypted workspace</div><h1>Secrets, under control.</h1><p id="secretMeta">Choose a project to begin.</p></div><div class="actions"><button class="btn" id="pull">↓ Pull</button><button class="btn good" id="push">↑ Push</button></div></div><div class="grid stats"><div class="card"><div class="stat-label">Version</div><div class="stat-value" id="version">—</div><div class="stat-note" id="author">—</div></div><div class="card"><div class="stat-label">Keys</div><div class="stat-value" id="keys">—</div><div class="stat-note">decrypted locally</div></div><div class="card"><div class="stat-label">Environment</div><div class="stat-value" id="envStat">—</div><div class="stat-note">active</div></div><div class="card"><div class="stat-label">State</div><div class="stat-value" id="state">clean</div><div class="stat-note">editor changes</div></div></div><div class="card"><div class="card-head"><div class="card-title">Environment editor</div><div class="actions"><input class="input search" id="find" placeholder="Find a key…"><button class="btn" id="copy">Copy</button></div></div><div class="notice">Plaintext is decrypted and encrypted on this machine. The server receives ciphertext only.</div><textarea class="editor" id="editor" spellcheck="false" placeholder="DATABASE_URL=…\nAPI_KEY=…"></textarea></div></div>
<div class="view" id="view-history"><div class="hero"><div><div class="eyebrow">Immutable timeline</div><h1>History</h1><p>Inspect or restore a previous encrypted version.</p></div><button class="btn" id="refreshHistory">↻ Refresh</button></div><div class="card"><div class="list" id="historyList"><div class="empty">Loading history…</div></div></div></div>
<div class="view" id="view-team"><div class="hero"><div><div class="eyebrow">Access control</div><h1>Team</h1><p>Manage who can access this project.</p></div></div><div class="card"><div class="form"><input class="input" id="memberName" placeholder="github-username"><select class="input" id="memberRole"><option>member</option><option>admin</option><option>viewer</option></select><button class="btn primary" id="addMember">Add member</button></div><div class="list" id="memberList" style="margin-top:16px"></div></div></div>
<div class="view" id="view-tokens"><div class="hero"><div><div class="eyebrow">Automation</div><h1>Service tokens</h1><p>Scoped credentials for CI/CD. Raw tokens are shown once.</p></div></div><div class="card"><div class="form"><input class="input" id="tokenName" placeholder="token name"><select class="input" id="tokenEnv"></select><button class="btn primary" id="createToken">Create token</button></div><div id="tokenReveal"></div><div class="list" id="tokenList" style="margin-top:16px"></div></div></div>
<div class="view" id="view-audit"><div class="hero"><div><div class="eyebrow">Traceability</div><h1>Audit log</h1><p>Every push, pull, and membership change.</p></div><button class="btn" id="refreshAudit">↻ Refresh</button></div><div class="card"><div class="list" id="auditList"></div></div></div>
<div class="view" id="view-workspace"><div class="hero"><div><div class="eyebrow">Local project</div><h1>Workspace</h1><p>Compare, link, and run without writing secrets to disk.</p></div></div><div class="grid" style="grid-template-columns:repeat(auto-fit,minmax(280px,1fr))"><div class="card"><div class="card-head"><div class="card-title">Local link</div><span class="pill" id="linkState">—</span></div><div class="form"><select class="input" id="linkProject"></select><select class="input" id="linkEnv"></select><button class="btn primary" id="linkWorkspace">Link folder</button></div></div><div class="card"><div class="card-head"><div class="card-title">Compare .env</div><button class="btn" id="compare">⇄ Compare</button></div><div id="diffList" class="list"><div class="empty">Values are never shown.</div></div></div></div><div class="card" style="margin-top:16px"><div class="card-head"><div class="card-title">Run with secrets</div><span class="pill good">zero disk</span></div><div class="form"><input class="input" id="runCommand" placeholder="npm test"><button class="btn good" id="run">▶ Run</button></div><pre class="run-output" id="runOutput">Output will appear here.</pre></div></div>
</section></main></div><div id="toast" class="toast" hidden></div>
<script>
const design=document.createElement('style');design.textContent=':root{--ink:#e8e6dc;--muted:#a6a99d;--faint:#70766c;--bg:#11130f;--panel:#191c16;--panel2:#20251c;--line:#343a30;--blue:#e8b04a;--violet:#b8c2ad;--green:#82d0a0;--amber:#e8b04a;--red:#ef8b79}body{background:#11130f;font-family:Inter,ui-sans-serif,system-ui,sans-serif}.rail{background:#151812;border-right-color:#343a30}.mark{background:#e8b04a;color:#17150d;border-radius:3px}.caption,.eyebrow{color:#e8b04a}.nav button:hover,.nav button.active{background:#20251c}.nav button.active{box-shadow:inset 3px 0 #e8b04a}.top{background:rgba(17,19,15,.96);border-bottom-color:#343a30}.top select,.control,.input,.btn{background:#191c16;border-color:#343a30}.env.active{border-color:#8d6d2f;background:#2b2517;color:#e8b04a}.dot{background:#82d0a0;box-shadow:0 0 10px #82d0a0}.btn:hover{border-color:#8d6d2f;background:#20251c}.btn.primary{background:#e8b04a;border-color:#e8b04a;color:#17150d}.btn.good{background:#173326;border-color:#397255;color:#82d0a0}.card{background:#191c16;border-color:#343a30;border-radius:4px;box-shadow:0 12px 30px #0003}.editor,.run-output{background:#0d100c;border-color:#343a30;border-radius:3px}.row{background:#151812;border-color:#343a30;border-radius:3px}.pill{background:#292518;color:#e8b04a}.pill.good{background:#173326;color:#82d0a0}.notice{background:#20251c}.empty{border-radius:3px;border-color:#4a5145}.toast{background:#20251c;border-color:#8d6d2f;border-radius:3px}';document.head.appendChild(design);
const boot=document.createElement('div');boot.id='boot';boot.innerHTML='<div class="boot-mark">D</div><div class="boot-title">Opening your workspace</div><div class="boot-copy">Checking your local link and encrypted project state…</div><div class="boot-track"><i></i></div>';document.body.appendChild(boot);const showBootError=message=>{boot.innerHTML='<div class="boot-mark">!</div><div class="boot-title">DotSync could not open this workspace</div><div class="boot-copy">'+String(message||'Something went wrong.')+'</div><button class="boot-retry" onclick="location.reload()">Try again</button>';boot.classList.add('error')};const hideBoot=()=>{boot.classList.add('done');setTimeout(()=>boot.remove(),260)};
const S={project:'',env:'dev',data:null,baseline:'',view:'secrets',loading:false,ready:false};const $=id=>document.getElementById(id);const valid=x=>x&&x!=='null'&&x!=='undefined';const dirty=()=>!!$('editor')&&$('editor').value!==S.baseline;const leaveEditor=()=>!dirty()||confirm('You have unsaved edits. Leave without pushing them?');
async function api(path){const r=await fetch(path);const d=await r.json();if(d.error)throw Error(d.error);return d}async function post(path,body){const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const d=await r.json();if(d.error)throw Error(d.error);return d}function q(){return 'slug='+encodeURIComponent(S.project)+'&env='+encodeURIComponent(S.env)}function toast(message,error=false){const t=$('toast');t.textContent=message;t.className='toast'+(error?' error':'');t.hidden=false;setTimeout(()=>t.hidden=true,4200)}function esc(x){return String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#039;'}[c]))}function time(x){if(!x)return 'unknown';const d=(Date.now()-new Date(x))/1000;if(d<60)return 'just now';if(d<3600)return Math.floor(d/60)+'m ago';if(d<86400)return Math.floor(d/3600)+'h ago';return new Date(x).toISOString().slice(0,10)}function count(){const n=($('editor').value.match(/^\s*[^#\s][^=]*=/gm)||[]).length;$('keys').textContent=n;$('state').textContent=$('editor').value===S.baseline?'clean':'edited'}
function nav(view){if(!['secrets','history','team','tokens','audit','workspace','guide'].includes(view))view='secrets';if(view!==S.view&&!leaveEditor())return;S.view=view;document.querySelectorAll('.nav button').forEach(b=>b.classList.toggle('active',b.dataset.view===view));document.querySelectorAll('.view').forEach(v=>v.classList.toggle('active',v.id==='view-'+view));location.hash=view;if(view==='history')history();if(view==='workspace')workspace();if(view==='audit')audit()}
async function load(){try{const me=await api('/api/me');$('user').textContent=me.user?.username?'@'+me.user.username:'connected';$('serverText').textContent=me.server_url||'local';const ps=await api('/api/projects');const list=(ps.projects||[]).filter(p=>valid(p.slug));$('project').innerHTML=list.map(p=>'<option value="'+esc(p.slug)+'">'+esc(p.slug)+'</option>').join('');if(!list.length){$('project').innerHTML='<option>no projects</option>';toast('No projects found — run dotsync init',true);return}S.project=me.project?.project_slug||list[0].slug;$('project').value=S.project;S.env=me.project?.default_env||'dev';await project();nav((location.hash||'#secrets').slice(1))}catch(e){toast(e.message,true)}}
async function project(){S.data=await api('/api/project/?slug='+encodeURIComponent(S.project));const envs=(S.data.envs||[]).map(e=>typeof e==='string'?e:e.name).filter(Boolean);if(!envs.length)envs.push('dev','staging','production');if(!envs.includes(S.env))S.env=envs[0];$('envs').innerHTML=envs.map(e=>'<button class="env '+(e===S.env?'active':'')+'" data-env="'+esc(e)+'">'+esc(e)+'</button>').join('');$('envStat').textContent=S.env;$('tokenEnv').innerHTML=envs.map(e=>'<option value="'+esc(e)+'">'+esc(e)+'</option>').join('');document.querySelectorAll('.env').forEach(b=>b.onclick=async()=>{if(!leaveEditor())return;S.contextChange=true;S.env=b.dataset.env;await project()});renderTeam();renderTokens();renderAudit();await pull(false)}
async function pull(notify=true){if(S.loading)return;S.loading=true;S.pullError=false;$('pull').disabled=true;try{const r=await api('/api/pull?'+q());$('editor').value=r.content||'';S.baseline=$('editor').value;$('version').textContent='v'+r.version;$('author').textContent=r.by?'@'+r.by:'';$('secretMeta').textContent='Encrypted remote state · last updated '+time(r.created_at);count();if(notify)toast('Pulled v'+r.version+' · '+r.keys+' keys')}catch(e){S.pullError=true;$('version').textContent='—';$('author').textContent='unavailable';toast(e.message,true)}finally{S.loading=false;$('pull').disabled=false}}
async function push(){const content=$('editor').value.trim();if(!content){toast('Editor is empty',true);return}if(S.loading)return;S.loading=true;$('push').disabled=true;try{const r=await post('/api/push',{slug:S.project,env:S.env,content});S.baseline=$('editor').value;count();$('version').textContent='v'+r.version;toast('Pushed v'+r.version+' · '+r.keys+' keys')}catch(e){toast(e.message,true)}finally{S.loading=false;$('push').disabled=false}}
function renderTeam(){const ms=S.data?.members||[];$('memberList').innerHTML=ms.length?ms.map((m,i)=>{const role=m.role||'member',owner=role==='owner';const roleControl=owner?'<span class="pill">owner</span>':'<select class="input member-role" id="member-role-'+i+'" data-username="'+esc(m.username)+'"><option value="admin" '+(role==='admin'?'selected':'')+'>admin</option><option value="member" '+(role==='member'?'selected':'')+'>member</option><option value="viewer" '+(role==='viewer'?'selected':'')+'>viewer</option></select>';return '<div class="row"><div class="row-main"><div class="row-title">@'+esc(m.username)+'</div><div class="row-sub">joined '+time(m.joined_at)+'</div></div>'+roleControl+(owner?'':'<button class="btn danger" data-remove="'+esc(m.username)+'">Remove</button>')+'</div>'}).join(''):'<div class="empty">No members yet.</div>';document.querySelectorAll('[data-remove]').forEach(b=>b.onclick=async()=>{if(!confirm('Remove @'+b.dataset.remove+'?'))return;try{await post('/api/team/remove',{slug:S.project,username:b.dataset.remove});await project();toast('Member removed')}catch(e){toast(e.message,true)}});document.querySelectorAll('.member-role').forEach(select=>{select.onchange=async()=>{const username=select.dataset.username,role=select.value;showBusy('Updating access…');try{await post('/api/team/role',{slug:S.project,username,role});await project();toast('@'+username+' is now '+role)}catch(e){toast(e.message,true)}finally{hideBusy()}};makeSelectMenu(select.id)})}
async function addMember(){const username=$('memberName').value.trim().replace(/^@/,'');if(!username){toast('Enter a GitHub username',true);return}try{await post('/api/team/add',{slug:S.project,username,role:$('memberRole').value});$('memberName').value='';await project();toast('Member added')}catch(e){toast(e.message,true)}}
function renderTokens(){const ts=S.data?.tokens||[];$('tokenList').innerHTML=ts.length?ts.map(t=>'<div class="row"><div class="row-main"><div class="row-title">'+esc(t.name)+'</div><div class="row-sub">'+esc(t.env)+' · created '+time(t.created_at)+' · last used '+time(t.last_used_at)+'</div></div><span class="code">'+esc(t.id)+'</span><button class="btn danger" data-revoke="'+esc(t.id)+'">Revoke</button></div>').join(''):'<div class="empty">No service tokens yet.</div>';document.querySelectorAll('[data-revoke]').forEach(b=>b.onclick=async()=>{if(!confirm('Revoke this token?'))return;try{await post('/api/tokens/revoke',{slug:S.project,token_id:b.dataset.revoke});await project();toast('Token revoked')}catch(e){toast(e.message,true)}})}
async function createToken(){const name=$('tokenName').value.trim();if(!name){toast('Enter a token name',true);return}try{const r=await post('/api/tokens/create',{slug:S.project,env:$('tokenEnv').value,name});$('tokenReveal').innerHTML='<div class="token-card"><div class="token-card-head"><strong>Token created</strong><span>shown once</span></div><div class="token-value">'+esc(r.token)+'</div><button class="btn" id="copyNewToken" type="button">Copy token</button></div>';$('copyNewToken').onclick=async()=>{try{await navigator.clipboard.writeText(r.token);const b=$('copyNewToken');b.textContent='Copied';toast('Token copied');setTimeout(()=>{if(b.isConnected)b.textContent='Copy token'},1600)}catch(e){toast('Clipboard access was denied',true)}};$('tokenName').value='';await project();toast('Token created — copy it now')}catch(e){toast(e.message,true)}}
function renderAudit(){const hidden=a=>{const action=String(a||'').toLowerCase().replace(/[.\- ]/g,'_');return action==='password_fetch'||action==='fetch_project_password'||action==='project_password_read'||(action.includes('password')&&(action.includes('fetch')||action.includes('get')||action.includes('read')||action.includes('retriev')))};const ls=(S.data?.logs||[]).filter(l=>!hidden(l.action));$('auditList').innerHTML=ls.length?ls.map(l=>'<div class="row"><div class="row-main"><div class="row-title">'+esc(l.action||'event')+'</div><div class="row-sub">@'+esc(l.username||'unknown')+(l.env?' · '+esc(l.env):'')+(l.created_at?' · '+time(l.created_at):'')+'</div></div><span class="pill">'+esc(l.ip||'local')+'</span></div>').join(''):'<div class="empty">No audit events to show.</div>'}
async function history(){try{const r=await api('/api/history?'+q());$('historyList').innerHTML=r.history?.length?r.history.map((h,i)=>'<div class="row"><div class="row-main"><div class="row-title">v'+h.version+(i===0?' · current':'')+'</div><div class="row-sub">@'+esc(h.pushed_by)+' · '+time(h.created_at)+'</div></div><button class="btn" data-view-version="'+h.version+'">View</button>'+(i?' <button class="btn" data-restore="'+h.version+'">Restore</button>':'<span class="pill good">active</span>')+'</div>').join(''):'<div class="empty">No history yet.</div>';document.querySelectorAll('[data-view-version]').forEach(b=>b.onclick=async()=>{try{const r=await api('/api/version?'+q()+'&version='+b.dataset.viewVersion);$('editor').value=r.content;count();nav('secrets');toast('Previewing v'+b.dataset.viewVersion+' — push to publish')}catch(e){toast(e.message,true)}});document.querySelectorAll('[data-restore]').forEach(b=>b.onclick=async()=>{if(!confirm('Restore v'+b.dataset.restore+' as a new current version?'))return;try{const r=await post('/api/rollback',{slug:S.project,env:S.env,version:Number(b.dataset.restore)});await pull(false);await history();toast('Restored as v'+r.version)}catch(e){toast(e.message,true)}})}catch(e){toast(e.message,true)}}
async function audit(){try{const r=await api('/api/project/?slug='+encodeURIComponent(S.project));S.data=r;renderAudit()}catch(e){toast(e.message,true)}}async function workspace(){const ps=[...$('project').options];$('linkProject').innerHTML=ps.map(o=>'<option value="'+esc(o.value)+'">'+esc(o.textContent)+'</option>').join('');$('linkProject').value=S.project;$('linkEnv').innerHTML=[...document.querySelectorAll('.env')].map(b=>'<option>'+esc(b.dataset.env)+'</option>').join('');$('linkEnv').value=S.env;try{const r=await api('/api/workspace');$('linkState').textContent=r.linked?'linked':'unlinked'}catch(e){}}async function diff(){try{const r=await api('/api/diff?'+q());const all=[...(r.added||[]).map(k=>['added',k]),...(r.removed||[]).map(k=>['removed',k]),...(r.changed||[]).map(k=>['changed',k])];$('diffList').innerHTML=all.length?all.map(x=>'<div class="row"><div class="row-main"><div class="row-title">'+esc(x[1])+'</div><div class="row-sub">'+x[0]+' · remote v'+r.version+'</div></div><span class="pill">key only</span></div>').join(''):'<div class="empty">Local .env is in sync with remote.</div>'}catch(e){toast(e.message,true)}}
async function run(){const raw=$('runCommand').value.trim();if(!raw){toast('Enter a command',true);return}const args=raw.match(/(?:[^\s"']+|"[^"]*"|'[^']*')+/g).map(x=>x.replace(/^['"]|['"]$/g,''));if(!(await askYesNo('Run '+args.join(' ')+' with '+S.project+'/'+S.env+' secrets?'))){toast('Command not run');return}$('runOutput').textContent='Running…\n\n$ '+args.join(' ');try{const r=await post('/api/run',{slug:S.project,env:S.env,args});const output=(r.output||'').trimEnd();const status=r.run_error?'\n\n✕ '+r.run_error:'\n\n✓ exited with code '+(r.exit_code??0);$('runOutput').textContent='$ '+args.join(' ')+'\n\n'+(output||'(no output)')+status;toast(r.run_error?'Command failed':'Command completed',!!r.run_error)}catch(e){$('runOutput').textContent='$ '+args.join(' ')+'\n\n✕ '+e.message;toast(e.message,true)}}
document.querySelectorAll('.nav button').forEach(b=>b.onclick=()=>nav(b.dataset.view));$('project').onchange=async e=>{S.project=e.target.value;await project()};$('editor').oninput=count;$('pull').onclick=()=>pull(true);$('push').onclick=push;$('copy').onclick=()=>navigator.clipboard.writeText($('editor').value).then(()=>toast('Copied'));$('refreshHistory').onclick=history;$('refreshAudit').onclick=audit;$('addMember').onclick=addMember;$('createToken').onclick=createToken;$('compare').onclick=diff;$('linkWorkspace').onclick=async()=>{try{await post('/api/workspace/link',{slug:$('linkProject').value,env:$('linkEnv').value});$('linkState').textContent='linked';toast('Workspace linked')}catch(e){toast(e.message,true)}};$('run').onclick=run;load();
</script><script>
// Interaction polish lives in a second small layer so the core data handlers
// stay easy to audit: protect edits when changing context, add keyboard-first
// save/pull, and make clipboard/search failures explicit.
const theme=document.createElement('style');theme.textContent='body{background:#f2f2ee;color:#171814}.shell{background:#f2f2ee}.rail{background:#fff;border-right:1px solid #d8d8d0;padding:24px 14px}.brand{color:#171814}.mark{background:#171814;color:#fff;border-radius:3px}.caption,.eyebrow{color:#171814}.nav button{color:#64685e}.nav button:hover,.nav button.active{background:#f0f0eb;color:#171814}.nav button.active{box-shadow:inset 3px 0 #171814}.rail-foot{color:#8b8e84}.top{background:rgba(255,255,255,.95);border-bottom:1px solid #d8d8d0}.top select,.control,.input,.btn{background:#fff;border-color:#cfcfc6;color:#171814}.env{color:#64685e}.env.active{border-color:#171814;background:#171814;color:#fff}.dot{background:#3b8d61;box-shadow:none}.hero h1{color:#171814}.hero p,.muted,.row-sub,.stat-note{color:#696d63}.btn:hover{border-color:#171814;background:#f0f0eb}.btn.primary{background:#171814;border-color:#171814;color:#fff}.btn.good{background:#e5f1e9;border-color:#93bca2;color:#286642}.btn.danger{color:#a13b2d}.card{background:#fff;border-color:#d8d8d0;border-radius:4px;box-shadow:0 8px 22px #20251a0c}.editor,.run-output{background:#fafaf7;color:#171814;border-color:#cfcfc6;border-radius:3px}.row{background:#fff;border-color:#d8d8d0;border-radius:3px}.pill{background:#ecece6;color:#4c5148}.pill.good{background:#e5f1e9;color:#286642}.notice{background:#f0f0eb;color:#696d63}.empty{border-color:#bfc1b8}.toast{background:#171814;color:#fff;border-color:#171814;border-radius:3px}.spinner{display:inline-block;width:11px;height:11px;border:2px solid currentColor;border-right-color:transparent;border-radius:50%;animation:spin .7s linear infinite;vertical-align:-1px;margin-right:6px}@keyframes spin{to{transform:rotate(360deg)}}@media(prefers-reduced-motion:reduce){.spinner{animation:none}}';document.head.appendChild(theme);
const sidebarStyle=document.createElement('style');sidebarStyle.textContent='.shell{grid-template-columns:184px 1fr}.rail{padding:22px 10px 14px;gap:24px}.brand{padding:0 9px 18px;border-bottom:1px solid #e1e1da;font-size:15px;letter-spacing:-.02em}.mark{width:8px;height:8px;font-size:0;border-radius:50%;background:#171814}.caption{display:none}.nav{gap:2px}.nav button{font-size:0;padding:10px 11px;border-radius:3px;min-height:36px}.nav button span{font-size:13px}.nav button.active{box-shadow:none;background:#171814;color:#fff}.nav button:hover:not(.active){background:#f0f0eb}.rail-foot{border-top:1px solid #e1e1da;padding:15px 9px 0;font-size:10px;line-height:1.55;word-break:break-word}.rail-foot br{display:none}.rail-foot span{display:block;margin-top:5px;color:#696d63}@media(max-width:900px){.shell{grid-template-columns:1fr}.rail{padding:14px 16px;gap:12px}.brand{padding:0 0 12px}.nav button{padding:9px 12px}.rail-foot{display:none}}';document.head.appendChild(sidebarStyle);
const layoutStyle=document.createElement('style');layoutStyle.textContent='.rail{position:sticky;top:0;align-self:start;height:100vh;overflow-y:auto;overscroll-behavior:contain}.main{min-width:0;min-height:100vh}.content{min-height:calc(100vh - 76px)}@media(max-width:900px){.rail{position:static;height:auto;overflow:visible}}';document.head.appendChild(layoutStyle);
const controlStyle=document.createElement('style');controlStyle.textContent='.top select{appearance:none;min-width:190px;height:38px;padding:0 36px 0 12px;border:1px solid #cfcfc6;border-radius:3px;background-color:#fff;background-image:url("data:image/svg+xml,%3Csvg xmlns=\'http://www.w3.org/2000/svg\' width=\'12\' height=\'12\' viewBox=\'0 0 12 12\'%3E%3Cpath d=\'M2.5 4.5 6 8l3.5-3.5\' fill=\'none\' stroke=\'%23171814\' stroke-width=\'1.4\' stroke-linecap=\'square\'/%3E%3C/svg%3E");background-repeat:no-repeat;background-position:right 12px center;color:#171814;font-weight:650;letter-spacing:-.01em;box-shadow:0 1px 2px #1718140a}.top select:hover{border-color:#8d9186}.top select:focus{border-color:#171814;box-shadow:0 0 0 2px #17181418}.env{height:32px;padding:0 12px;line-height:30px;letter-spacing:.01em}.env:not(.active):hover{background:#f0f0eb;color:#171814}';document.head.appendChild(controlStyle);
const pickerStyle=document.createElement('style');pickerStyle.textContent='.project-picker{position:relative;min-width:190px}.project-picker select{position:absolute;inset:0;width:1px;height:1px;opacity:0;pointer-events:none}.project-trigger{width:100%;height:38px;padding:0 32px 0 12px;border:1px solid #cfcfc6;border-radius:3px;background:#fff;color:#171814;text-align:left;font-weight:650;position:relative;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.project-trigger:after{content:"";position:absolute;right:12px;top:14px;width:7px;height:7px;border-right:1.5px solid #171814;border-bottom:1.5px solid #171814;transform:rotate(45deg);transition:transform .16s}.project-picker.open .project-trigger{border-color:#171814;box-shadow:0 0 0 2px #17181418}.project-picker.open .project-trigger:after{top:17px;transform:rotate(225deg)}.project-menu{display:none;position:absolute;left:0;right:0;top:calc(100% + 5px);z-index:12;padding:4px;background:#fff;border:1px solid #cfcfc6;box-shadow:0 12px 28px #17181420}.project-picker.open .project-menu{display:grid}.project-option{border:0;background:transparent;color:#555a50;text-align:left;padding:9px 10px;border-radius:2px;cursor:pointer}.project-option:hover,.project-option.selected{background:#171814;color:#fff}.project-option.selected{font-weight:650}@media(max-width:560px){.project-picker{min-width:160px;flex:1}}';document.head.appendChild(pickerStyle);
const selectStyle=document.createElement('style');selectStyle.textContent='.select-picker{position:relative;flex:1;min-width:160px}.select-picker select{position:absolute;inset:0;width:1px;height:1px;opacity:0;pointer-events:none}.select-trigger{width:100%;height:40px;padding:0 32px 0 12px;border:1px solid #cfcfc6;border-radius:3px;background:#fff;color:#171814;text-align:left;position:relative}.select-trigger:after{content:"";position:absolute;right:12px;top:14px;width:7px;height:7px;border-right:1.5px solid #171814;border-bottom:1.5px solid #171814;transform:rotate(45deg)}.select-picker.open .select-trigger{border-color:#171814;box-shadow:0 0 0 2px #17181418}.select-picker.open .select-trigger:after{top:17px;transform:rotate(225deg)}.select-menu{display:none;position:absolute;left:0;right:0;top:calc(100% + 5px);z-index:12;padding:4px;background:#fff;border:1px solid #cfcfc6;box-shadow:0 12px 28px #17181420}.select-picker.open .select-menu{display:grid}.select-option{border:0;background:transparent;color:#555a50;text-align:left;padding:9px 10px;border-radius:2px;cursor:pointer}.select-option:hover,.select-option.selected{background:#171814;color:#fff}.select-option.selected{font-weight:650}';document.head.appendChild(selectStyle);
const tokenStyle=document.createElement('style');tokenStyle.textContent='.token-card{margin-top:16px;padding:16px;border:1px solid #cfcfc6;background:#fafaf7;border-radius:3px}.token-card-head{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:10px;color:#171814}.token-card-head span{font:11px ui-monospace,SFMono-Regular,Menlo,monospace;color:#8a5c16}.token-value{padding:12px;border:1px solid #d8d8d0;background:#fff;color:#171814;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace;word-break:break-all;margin-bottom:10px;user-select:all}';document.head.appendChild(tokenStyle);
const monochrome=document.createElement('style');monochrome.textContent='::selection{background:#171814;color:#fff}.dot{background:#171814;box-shadow:none}.env.active{border-color:#171814;background:#171814;color:#fff}.btn.good,.pill.good{background:#f0f0eb;border-color:#bfc1b8;color:#171814}.btn.danger{color:#171814}.pill.warn,.notice.warn{background:#f0f0eb;color:#171814}.eyebrow,.code{color:#171814}.token-card-head span{color:#696d63}.token-value{background:#fff;color:#171814}.toast.error{border-color:#171814;color:#fff}.toast.ok .toast-symbol,.toast.error .toast-symbol{color:#fff}.run-output{color:#171814}';document.head.appendChild(monochrome);
const keyFilterStyle=document.createElement('style');keyFilterStyle.textContent='.key-filter-results{display:grid;gap:4px;margin-top:10px;max-height:180px;overflow:auto}.key-filter-meta{color:#696d63;font:11px ui-monospace,SFMono-Regular,Menlo,monospace;padding:4px 0}.key-filter-item{border:1px solid #d8d8d0;background:#fff;color:#171814;text-align:left;padding:7px 9px;border-radius:2px;cursor:pointer;font:12px ui-monospace,SFMono-Regular,Menlo,monospace;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.key-filter-item:hover{background:#f0f0eb;border-color:#171814}';document.head.appendChild(keyFilterStyle);
const motionStyle=document.createElement('style');motionStyle.textContent='.btn,.nav button,.env,.row,.input,.top select{transition:background-color .16s ease,border-color .16s ease,color .16s ease,transform .16s ease,box-shadow .16s ease}.btn:active{transform:translateY(1px)}.row:hover{border-color:#aeb1a6;transform:translateY(-1px)}.view.active{animation:viewIn .18s ease both}@keyframes viewIn{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:none}}#toast{display:flex;align-items:flex-start;gap:9px;max-width:360px;line-height:1.4}#toast .toast-symbol{font-weight:800;flex:0 0 auto}#toast.error .toast-symbol{color:#ef8b79}#toast.ok .toast-symbol{color:#82d0a0}@media(prefers-reduced-motion:reduce){.btn:active,.row:hover{transform:none}.view.active{animation:none}}';document.head.appendChild(motionStyle);
const a11y=document.createElement('style');a11y.textContent='.btn:focus-visible,.input:focus-visible,.top select:focus-visible,.env:focus-visible,.nav button:focus-visible{outline:2px solid #171814;outline-offset:2px}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto!important;transition:none!important}}';document.head.appendChild(a11y);
const toastCore=toast;let toastTimer;toast=(message,error=false)=>{clearTimeout(toastTimer);toastCore(message,error);const t=$('toast');t.setAttribute('role',error?'alert':'status');t.setAttribute('aria-live',error?'assertive':'polite');t.classList.toggle('ok',!error);t.innerHTML='<span class="toast-symbol">'+(error?'!':'✓')+'</span><span>'+esc(message)+'</span>';toastTimer=setTimeout(()=>{t.hidden=true},2400);t.onclick=()=>{t.hidden=true}};
const overlay=document.createElement('div');overlay.id='busy';overlay.innerHTML='<span class="spinner"></span><span id="busyText">Working…</span>';document.body.appendChild(overlay);const showBusy=message=>{$('busyText').textContent=message;overlay.classList.add('on')};const hideBusy=()=>overlay.classList.remove('on');const busyStyle=document.createElement('style');busyStyle.textContent='#boot{position:fixed;inset:0;z-index:30;background:#f2f2ee;color:#171814;display:grid;place-content:center;justify-items:center;gap:12px;text-align:center;padding:28px;transition:opacity .24s}.boot-mark{width:52px;height:52px;display:grid;place-items:center;background:#171814;color:#fff;font-weight:800;font-size:24px;border-radius:3px}.boot-title{font-size:20px;font-weight:750}.boot-copy{color:#696d63;max-width:360px}.boot-track{width:180px;height:3px;background:#d8d8d0;overflow:hidden}.boot-track i{display:block;width:60px;height:100%;background:#171814;animation:boot 1.1s ease-in-out infinite}@keyframes boot{0%{transform:translateX(-60px)}100%{transform:translateX(180px)}}#boot.error{background:#171814;color:#fff}.boot-retry{border:1px solid #fff;background:transparent;color:#fff;padding:9px 14px;border-radius:3px;cursor:pointer}#boot.done{opacity:0;pointer-events:none}#busy{position:fixed;inset:0;z-index:25;background:#171814d9;color:#fff;display:none;place-content:center;justify-items:center;gap:10px;font:13px ui-monospace,SFMono-Regular,Menlo,monospace}#busy.on{display:grid}';document.head.appendChild(busyStyle);
$('project').setAttribute('aria-label','Project');$('editor').setAttribute('aria-label','Environment secrets editor');const pullCore=pull;pull=async notify=>{const b=$('pull'),label=b.textContent;b.innerHTML='<span class="spinner"></span>Pulling';try{return await pullCore(notify)}finally{b.textContent=label;S.ready=true;hideBoot()}};const pushCore=push;push=async()=>{const b=$('push'),label=b.textContent;b.innerHTML='<span class="spinner"></span>Pushing';try{return await pushCore()}finally{b.textContent=label}};$('pull').onclick=()=>pull(true);$('push').onclick=push;
const nativeProject=$('project'),picker=document.createElement('div');picker.className='project-picker';nativeProject.parentNode.insertBefore(picker,nativeProject);picker.appendChild(nativeProject);const projectTrigger=document.createElement('button');projectTrigger.type='button';projectTrigger.className='project-trigger';projectTrigger.setAttribute('aria-haspopup','listbox');projectTrigger.setAttribute('aria-expanded','false');const projectMenu=document.createElement('div');projectMenu.className='project-menu';projectMenu.setAttribute('role','listbox');picker.insertBefore(projectTrigger,nativeProject);picker.appendChild(projectMenu);const syncPicker=()=>{const options=[...nativeProject.options];const selected=options.find(o=>o.value===nativeProject.value)||options[0];projectTrigger.textContent=selected?selected.textContent:'Choose project';projectMenu.innerHTML=options.map(o=>'<button type="button" class="project-option '+(o===selected?'selected':'')+'" role="option" aria-selected="'+(o===selected)+'" data-value="'+esc(o.value)+'">'+esc(o.textContent)+'</button>').join('')};const togglePicker=open=>{picker.classList.toggle('open',open);projectTrigger.setAttribute('aria-expanded',String(open))};projectTrigger.onclick=()=>togglePicker(!picker.classList.contains('open'));projectMenu.onclick=e=>{const option=e.target.closest('.project-option');if(!option)return;nativeProject.value=option.dataset.value;nativeProject.dispatchEvent(new Event('change',{bubbles:true}));syncPicker();togglePicker(false)};document.addEventListener('click',e=>{if(!picker.contains(e.target))togglePicker(false)});new MutationObserver(syncPicker).observe(nativeProject,{childList:true});syncPicker();
function makeSelectMenu(id){const native=$(id);if(!native)return;const wrap=document.createElement('div');wrap.className='select-picker';native.parentNode.insertBefore(wrap,native);wrap.appendChild(native);const trigger=document.createElement('button');trigger.type='button';trigger.className='select-trigger';trigger.setAttribute('aria-haspopup','listbox');trigger.setAttribute('aria-expanded','false');const menu=document.createElement('div');menu.className='select-menu';menu.setAttribute('role','listbox');wrap.insertBefore(trigger,native);wrap.appendChild(menu);const sync=()=>{const options=[...native.options],selected=options.find(o=>o.value===native.value)||options[0];trigger.textContent=selected?selected.textContent:'Choose an option';menu.innerHTML=options.map(o=>'<button type="button" class="select-option '+(o===selected?'selected':'')+'" role="option" aria-selected="'+(o===selected)+'" data-value="'+esc(o.value)+'">'+esc(o.textContent)+'</button>').join('')};const close=()=>{wrap.classList.remove('open');trigger.setAttribute('aria-expanded','false')};trigger.onclick=()=>{const open=!wrap.classList.contains('open');wrap.classList.toggle('open',open);trigger.setAttribute('aria-expanded',String(open))};menu.onclick=e=>{const option=e.target.closest('.select-option');if(!option)return;native.value=option.dataset.value;native.dispatchEvent(new Event('change',{bubbles:true}));sync();close()};document.addEventListener('click',e=>{if(!wrap.contains(e.target))close()});new MutationObserver(sync).observe(native,{childList:true});sync()};makeSelectMenu('memberRole');makeSelectMenu('tokenEnv');
const linkControl=$('linkProject');setTimeout(()=>{if(linkControl){const linkCard=linkControl.closest('.card');if(linkCard)linkCard.remove()}},0);workspace=async()=>{showBusy('Loading workspace comparison…');try{return await diff()}finally{hideBusy()}};
$('editor').insertAdjacentHTML('afterend','<div id="keyFilterResults" class="key-filter-results" hidden></div>');const selectSearchMatch=term=>{const needle=term.trim().toLowerCase();if(!needle)return false;const lines=$('editor').value.split('\n');for(let line=0;line<lines.length;line++){const at=lines[line].toLowerCase().indexOf(needle);if(at<0)continue;const start=lines.slice(0,line).join('\n').length+(line?1:0)+at;$('editor').focus();$('editor').setSelectionRange(start,start+needle.length);return true}return false};const renderKeyFilter=term=>{const box=$('keyFilterResults'),needle=term.trim().toLowerCase();if(!needle){box.hidden=true;box.innerHTML='';return}const lines=$('editor').value.split('\n'),matches=lines.map((line,index)=>({line,index})).filter(x=>{const key=x.line.split('=')[0].trim().toLowerCase();return key&&key.includes(needle)});box.hidden=false;box.innerHTML='<div class="key-filter-meta">'+matches.length+' matching key'+(matches.length===1?'':'s')+' · press Enter to select</div>'+matches.slice(0,80).map(x=>'<button type="button" class="key-filter-item" data-line="'+x.index+'">'+esc(x.line.split('=')[0].trim())+'</button>').join('');box.querySelectorAll('[data-line]').forEach(b=>b.onclick=()=>{const line=Number(b.dataset.line),lines=$('editor').value.split('\n'),start=lines.slice(0,line).join('\n').length+(line?1:0);$('editor').focus();$('editor').setSelectionRange(start,start+lines[line].length)})};
$('view-tokens').querySelector('.hero p').textContent='Create a narrowly scoped credential for GitHub Actions, deployment jobs, or another automated process. It can pull secrets without using your personal login; the raw value is shown once, so copy it into your CI secret store immediately.';
const historyCore=history;history=async()=>{const box=$('historyList');box.innerHTML='<div class="empty"><span class="spinner"></span>Loading history</div>';try{return await historyCore()}finally{}};$('refreshHistory').onclick=history;const auditCore=audit;audit=async()=>{const box=$('auditList');box.innerHTML='<div class="empty"><span class="spinner"></span>Loading audit events</div>';try{return await auditCore()}finally{}};$('refreshAudit').onclick=audit;const diffCore=diff;diff=async()=>{const box=$('diffList');box.innerHTML='<div class="empty"><span class="spinner"></span>Comparing local and remote keys</div>';return diffCore()};
const addCore=addMember;addMember=async()=>{const b=$('addMember'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Adding';try{return await addCore()}finally{b.disabled=false;b.textContent=label}};$('addMember').onclick=addMember;const tokenCore=createToken;createToken=async()=>{const b=$('createToken'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Creating';try{return await tokenCore()}finally{b.disabled=false;b.textContent=label}};$('createToken').onclick=createToken;
const modalStyle=document.createElement('style');modalStyle.textContent='.choice-backdrop{position:fixed;inset:0;z-index:40;background:#17181480;display:grid;place-items:center;padding:20px}.choice{width:min(390px,100%);background:#fff;border:1px solid #cfcfc6;box-shadow:0 20px 50px #17181432;padding:22px}.choice h2{font-size:16px;margin:0 0 7px;color:#171814}.choice p{margin:0;color:#696d63;line-height:1.5}.choice-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}.choice-actions button{border:1px solid #cfcfc6;background:#fff;color:#171814;padding:9px 14px;border-radius:3px;cursor:pointer}.choice-actions .yes{background:#171814;color:#fff;border-color:#171814}';document.head.appendChild(modalStyle);function askYesNo(message){return new Promise(resolve=>{const backdrop=document.createElement('div');backdrop.className='choice-backdrop';backdrop.innerHTML='<div class="choice" role="dialog" aria-modal="true" aria-labelledby="choice-title"><h2 id="choice-title">Run command?</h2><p>'+esc(message)+'</p><div class="choice-actions"><button class="no">No</button><button class="yes">Yes</button></div></div>';document.body.appendChild(backdrop);const finish=value=>{backdrop.remove();resolve(value)};backdrop.querySelector('.no').onclick=()=>finish(false);backdrop.querySelector('.yes').onclick=()=>finish(true);backdrop.onclick=e=>{if(e.target===backdrop)finish(false)};backdrop.querySelector('.yes').focus()})}const runCore=run;run=async()=>{const b=$('run'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Running';try{return await runCore()}finally{b.disabled=false;b.textContent=label}};$('run').onclick=run;
$('compare').onclick=diff;const linkCore=$('linkWorkspace').onclick;$('linkWorkspace').onclick=async()=>{const b=$('linkWorkspace'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Linking';try{return await linkCore()}finally{b.disabled=false;b.textContent=label}};
document.addEventListener('click',e=>{const b=e.target.closest('[data-remove],[data-revoke],[data-view-version],[data-restore]');if(!b)return;b.disabled=true;b.dataset.label=b.textContent;b.innerHTML='<span class="spinner"></span>Working';setTimeout(()=>{if(b.isConnected){b.disabled=false;b.textContent=b.dataset.label}},12000)},true);
$('project').onchange=async e=>{if(!leaveEditor()){$('project').value=S.project;return}S.contextChange=true;S.project=e.target.value;await project()};
const projectCore=project;project=async()=>{const contextChange=!!S.contextChange;const target=S.project;const message=contextChange?'Switching project…':'Refreshing project data…';if(contextChange){$('editor').value='';S.baseline='';$('version').textContent='—';$('author').textContent='Loading…';$('secretMeta').textContent='Loading '+target+'…';$('keys').textContent='—';$('state').textContent='loading';$('tokenReveal').innerHTML='';$('find').value='';$('keyFilterResults').hidden=true;$('keyFilterResults').innerHTML=''}S.contextChange=false;showBusy(message);try{const result=await projectCore();if(contextChange)toast(S.pullError?'Project loaded, but secrets could not be pulled':'Loaded '+target+'/'+S.env,!!S.pullError);return result}catch(e){if(contextChange)toast('Could not load '+target,true);throw e}finally{hideBusy()}};const bootWatch=setInterval(()=>{if(S.ready){hideBoot();clearInterval(bootWatch)}},80);setTimeout(()=>{if(!S.ready){showBootError('The local workspace could not be loaded. Check that you are logged in and that this folder is linked with dotsync init.')}},15000);
$('runCommand').addEventListener('keydown',e=>{if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();run()}});const clearRun=document.createElement('button');clearRun.type='button';clearRun.className='btn';clearRun.textContent='Clear output';clearRun.onclick=()=>{$('runOutput').textContent='Output will appear here.';toast('Output cleared')};$('run').parentNode.appendChild(clearRun);
$('copy').onclick=()=>navigator.clipboard.writeText($('editor').value).then(()=>toast('Copied')).catch(()=>toast('Clipboard access was denied',true));
$('find').oninput=e=>renderKeyFilter(e.target.value);$('find').onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();if(!selectSearchMatch(e.target.value))toast('No matching text')}};$('find').onkeyup=e=>{if(e.key==='Escape'){e.target.value='';renderKeyFilter('')}};$('editor').addEventListener('input',()=>{const find=$('find');if(find&&find.value)renderKeyFilter(find.value)});

document.addEventListener('keydown',e=>{const meta=e.metaKey||e.ctrlKey;if(meta&&e.key.toLowerCase()==='s'){e.preventDefault();push()}if(meta&&e.key.toLowerCase()==='l'){e.preventDefault();pull(true)}});window.addEventListener('beforeunload',e=>{if(dirty()){e.preventDefault();e.returnValue=''}});
const guideMarkup='<div class="view" id="view-guide"><div class="hero"><div><div class="eyebrow">Built-in guide</div><h1>How DotSync works</h1><p>A practical map of every workflow in this control room.</p></div></div><div class="grid" style="grid-template-columns:repeat(auto-fit,minmax(280px,1fr))"><div class="card"><div class="card-title">Secrets and environments</div><p class="muted">Use the editor for environment values such as database URLs and API keys. Pull loads the selected project and environment locally; Push encrypts edits before sending them. Keep development, staging, and production separate.</p></div><div class="card"><div class="card-title">When to use service tokens</div><p class="muted">Create a token for CI jobs, deployment runners, scheduled tasks, or integrations that need secrets without a personal login. Give each job its own environment-scoped token, copy it into the provider secret store immediately, and revoke it when retired or exposed. The raw value is shown once.</p></div><div class="card"><div class="card-title">Team access</div><p class="muted">Owners manage membership and can change non-owner roles. Admins manage trusted project operations, members have normal access, and viewers are read-only. The owner cannot be removed or reassigned.</p></div><div class="card"><div class="card-title">History and rollback</div><p class="muted">History keeps encrypted versions for inspection. Restore creates a new current version and preserves the original timeline, making reversals safe and auditable.</p></div><div class="card"><div class="card-title">Workspace commands</div><p class="muted">Run commands with selected secrets temporarily. Arguments are passed directly without a shell, output is shown here, and no secret file is written. Confirm the command, press Enter to run, and clear output when done.</p></div><div class="card"><div class="card-title">Audit and privacy</div><p class="muted">Audit shows pushes, pulls, access changes, and token activity. Filter by action, user, environment, or free text. Password-fetch internals and secret values are intentionally omitted.</p></div></div></div>';$('view-workspace').insertAdjacentHTML('beforebegin',guideMarkup);
const auditControls=document.createElement('div');auditControls.className='form';auditControls.innerHTML='<input class="input" id="auditSearch" placeholder="Search audit events"><select class="input" id="auditAction"><option value="">All actions</option></select><select class="input" id="auditUser"><option value="">All users</option></select><select class="input" id="auditEnv"><option value="">All environments</option></select>';$('auditList').parentNode.insertBefore(auditControls,$('auditList'));const auditBase=renderAudit;renderAudit=()=>{const logs=(S.data?.logs||[]).filter(l=>{const a=String(l.action||'').toLowerCase().replace(/[.\- ]/g,'_');return !(a.includes('password')&&(a.includes('fetch')||a.includes('get')||a.includes('read')||a.includes('retriev')))});const fill=(id,label,values)=>{const select=$(id),current=select.value;select.innerHTML='<option value="">'+label+'</option>'+values.sort().map(v=>'<option value="'+esc(v)+'">'+esc(v)+'</option>').join('');select.value=values.includes(current)?current:''};fill('auditAction','All actions',[...new Set(logs.map(l=>String(l.action||'event')))]);fill('auditUser','All users',[...new Set(logs.map(l=>String(l.username||'unknown')))]);fill('auditEnv','All environments',[...new Set(logs.map(l=>String(l.env||'' )).filter(Boolean))]);const q=String($('auditSearch').value||'').toLowerCase(),action=$('auditAction').value,user=$('auditUser').value,env=$('auditEnv').value;const filtered=logs.filter(l=>(!q||JSON.stringify(l).toLowerCase().includes(q))&&(!action||String(l.action||'event')===action)&&(!user||String(l.username||'unknown')===user)&&(!env||String(l.env||'')===env));$('auditList').innerHTML=filtered.length?filtered.map(l=>'<div class="row"><div class="row-main"><div class="row-title">'+esc(l.action||'event')+'</div><div class="row-sub">@'+esc(l.username||'unknown')+(l.env?' · '+esc(l.env):'')+(l.created_at?' · '+time(l.created_at):'')+'</div></div><span class="pill">'+esc(l.ip||'local')+'</span></div>').join(''):'<div class="empty">No matching audit events.</div>'};['auditSearch','auditAction','auditUser','auditEnv'].forEach(id=>$(id).addEventListener(id==='auditSearch'?'input':'change',renderAudit));makeSelectMenu('auditAction');makeSelectMenu('auditUser');makeSelectMenu('auditEnv');renderAudit();
</script></body></html>`
