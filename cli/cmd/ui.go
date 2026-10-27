package cmd

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/identity"
	serverCrypto "github.com/Pruthviraj36/dotsync/internal/crypto"
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
	slug = strings.TrimSpace(slug)
	if len(slug) < 2 || len(slug) > 39 {
		return false
	}
	for i, c := range slug {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(slug)-1:
		default:
			return false
		}
	}
	return !strings.Contains(slug, "--")
}

// validEnvName keeps query-supplied environment names within the fixed schema
// vocabulary before they are interpolated into URLs and API calls.
func validEnvName(env string) bool {
	switch strings.TrimSpace(env) {
	case "dev", "staging", "production":
		return true
	default:
		return false
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
			sessionToken, err := serverCrypto.GenerateRandomToken(32)
			if err != nil {
				return fmt.Errorf("create dashboard session: %w", err)
			}
			mux := http.NewServeMux()

			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy",
					"default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data: https://avatars.githubusercontent.com; connect-src 'self'; font-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
				w.Header().Set("X-Frame-Options", "DENY")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Referrer-Policy", "no-referrer")
				sessionScript := fmt.Sprintf(`<meta name="dotsync-session" content="%s"><script>(function(){const token="%s";const nativeFetch=window.fetch.bind(window);window.fetch=function(input,init){init=init||{};const headers=new Headers(init.headers||{});headers.set("X-DotSync-Session",token);return nativeFetch(input,Object.assign({},init,{headers:headers}))}})();</script>`, sessionToken, sessionToken)
				dashboard := strings.Replace(dashboardHTML, "</head>", sessionScript+"</head>", 1)
				w.Write([]byte(dashboard))
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
				envs, err := client.ListEnvironments(slug)
				if err != nil {
					return nil, fmt.Errorf("load environments: %w", err)
				}
				// Sections below are role-gated server-side (tokens and audit logs
				// are owner/admin only). A viewer opening the dashboard should still
				// see environments and their own membership, so degrade gracefully
				// instead of failing the entire response.
				members, membersErr := client.ListTeamMembers(slug)
				if membersErr != nil {
					members = []map[string]any{}
				}
				tokens, tokensErr := client.ListServiceTokens(slug)
				if tokensErr != nil {
					tokens = []map[string]any{}
				}
				logs, logsErr := client.AuditLogs(slug)
				if logsErr != nil {
					logs = []map[string]any{}
				}
				logs = filterUIAuditLogs(logs)
				result := map[string]any{
					"slug":    slug,
					"envs":    envs,
					"members": members,
					"tokens":  tokens,
					"logs":    logs,
				}
				// Surface partial failures to the UI without discarding usable data.
				var warnings []string
				if membersErr != nil {
					warnings = append(warnings, "team members unavailable")
				}
				if tokensErr != nil {
					warnings = append(warnings, "service tokens unavailable")
				}
				if logsErr != nil {
					warnings = append(warnings, "audit log unavailable")
				}
				if len(warnings) > 0 {
					result["warnings"] = warnings
				}
				return result, nil
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
				plain, err := cliCrypto.DecryptEnvFileForEnv(remote.EncryptedData, remote.Nonce, password, slug, env)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				added, removed, changed := cliCrypto.DiffEnvFiles(cliCrypto.ParseEnvFile(plain), cliCrypto.ParseEnvFile(string(local)))
				return map[string]any{"version": remote.Version, "added": added, "removed": removed, "changed": changed}, nil
			}))

			mux.HandleFunc("/api/version", uiHandler(func(r *http.Request) (any, error) {
				slug, env := r.URL.Query().Get("slug"), r.URL.Query().Get("env")
				version, parseErr := strconv.Atoi(r.URL.Query().Get("version"))
				if parseErr != nil || !validUISlug(slug) || env == "" || version < 1 {
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
				plain, err := cliCrypto.DecryptEnvFileForEnv(remote.EncryptedData, remote.Nonce, password, slug, env)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				return map[string]any{"content": plain, "keys": len(cliCrypto.ParseEnvFile(plain)), "version": remote.Version, "by": remote.PushedBy}, nil
			}))

			// /api/latest is intentionally metadata-only. The browser uses it to
			// decide whether a pull needs the encrypted payload at all.
			mux.HandleFunc("/api/latest", uiHandler(func(r *http.Request) (any, error) {
				slug, env := r.URL.Query().Get("slug"), r.URL.Query().Get("env")
				if !validUISlug(slug) || strings.TrimSpace(env) == "" || env == "null" || env == "undefined" {
					return nil, fmt.Errorf("slug and env are required")
				}
				version, by, err := client.GetLatestVersion(slug, env)
				if err != nil {
					return nil, err
				}
				return map[string]any{"version": version, "by": by}, nil
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
				plaintext, err := cliCrypto.DecryptEnvFileForEnv(
					result.EncryptedData, result.Nonce, password, slug, env,
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
				ciphertext, nonce, err := cliCrypto.EncryptEnvFileForEnv(req.Content, password, req.Slug, req.Env)
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
					SignerPubKey:  identity.Hex(pub),
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
				if req.Role == "" {
					req.Role = "member"
				}
				if err := client.AddTeamMember(req.Slug, req.Username, req.Role); err != nil {
					return nil, err
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
				plaintext, err := cliCrypto.DecryptEnvFileForEnv(
					old.EncryptedData, old.Nonce, password, req.Slug, req.Env,
				)
				if err != nil {
					return nil, fmt.Errorf("decrypt v%d: %w", req.Version, err)
				}
				ciphertext, nonce, err := cliCrypto.EncryptEnvFileForEnv(plaintext, password, req.Slug, req.Env)
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
					SignerPubKey:  identity.Hex(pub),
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
				if !validUISlug(req.Slug) || !validEnvName(req.Env) {
					return nil, fmt.Errorf("a valid slug and environment (dev, staging, production) are required")
				}
				next := &config.ProjectConfig{
					ProjectSlug: strings.TrimSpace(req.Slug),
					DefaultEnv:  strings.TrimSpace(req.Env),
				}
				// Linking only changes the project binding — it must not discard
				// the pull-state cache (that is what makes no-op pulls cheap and
				// prevents a needless full download).
				if existing, err := config.LoadProject(); err == nil {
					next.LastPulledStates = existing.LastPulledStates
				}
				if err := config.SaveProject(next); err != nil {
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
				if !validUISlug(req.Slug) || !validEnvName(req.Env) || len(req.Args) == 0 || strings.TrimSpace(req.Args[0]) == "" {
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
				plain, err := cliCrypto.DecryptEnvFileForEnv(remote.EncryptedData, remote.Nonce, password, req.Slug, req.Env)
				if err != nil {
					return nil, fmt.Errorf("decryption failed: %w", err)
				}
				secrets, err := cliCrypto.ParseEnvFileStrict(plain)
				if err != nil {
					return nil, fmt.Errorf("refusing to run with malformed secrets: %w", err)
				}
				injected := make(map[string]bool, len(secrets))
				for k := range secrets {
					injected[k] = true
				}
				var procEnv []string
				for _, e := range os.Environ() {
					key := e
					if i := strings.IndexByte(e, '='); i > 0 {
						key = e[:i]
					}
					if !injected[key] {
						procEnv = append(procEnv, e)
					}
				}
				for k, v := range secrets {
					procEnv = append(procEnv, k+"="+v)
				}
				runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				command := exec.CommandContext(runCtx, req.Args[0], req.Args[1:]...)
				command.Env = procEnv
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

			url := fmt.Sprintf("http://localhost:%s", portFlag)
			srv := &http.Server{
				Handler:      uiSessionGuard(sessionToken, portFlag, mux),
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

func uiSessionGuard(sessionToken, port string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostAllowed := r.Host == "127.0.0.1:"+port || r.Host == "localhost:"+port
		if !hostAllowed || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/" {
			provided := []byte(r.Header.Get("X-DotSync-Session"))
			expected := []byte(sessionToken)
			if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func uiHandler(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")

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

		// Validate Content-Type to prevent CSRF attacks via form submissions
		contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		if mediaType, _, err := mime.ParseMediaType(contentType); err != nil || mediaType != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Content-Type must be application/json",
				"help":  "This endpoint only accepts JSON requests, not form submissions",
			})
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

// uiSafeError keeps implementation details out of the browser and provides
// helpful, actionable error messages. Transforms error messages to guide users
// towards solutions without exposing internal implementation details.
func uiSafeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	lower := strings.ToLower(message)

	// Network/Server connectivity
	if strings.Contains(lower, "connection refused") {
		return "DotSync server is not running or unreachable. Restart the UI with: dotsync ui"
	}
	if strings.Contains(lower, "request failed") || strings.Contains(lower, "i/o timeout") {
		return "Network request timed out. Check your internet connection and try again."
	}
	if strings.Contains(lower, "no such host") || strings.Contains(lower, "cannot resolve") {
		return "Could not connect to the DotSync server. Check the server URL and your internet connection."
	}

	// Authentication
	if strings.Contains(lower, "session expired") || strings.Contains(lower, "refresh failed") {
		return "Your DotSync session has expired. Run: dotsync login"
	}
	if strings.Contains(lower, "unauthorized") {
		return "Not authorized to perform this action. Your credentials may be invalid. Run: dotsync login"
	}
	if strings.Contains(lower, "401") || strings.Contains(lower, "invalid token") {
		return "Authentication failed. Please log in again: dotsync login"
	}

	// Project/Environment setup
	if strings.Contains(lower, "project slug") || strings.Contains(lower, "not a dotsync") || strings.Contains(lower, "no project linked") {
		return "This folder is not linked to a DotSync project. Set it up with: dotsync init"
	}
	if strings.Contains(lower, "project not found") || strings.Contains(lower, "no such project") {
		return "Project not found on server. Check the project name or create it first."
	}
	if strings.Contains(lower, "environment") && strings.Contains(lower, "not found") {
		return "This environment doesn't exist for the selected project. Select a different environment or ask an admin to create it."
	}

	// Encryption/Passwords
	if strings.Contains(lower, "decrypt") || strings.Contains(lower, "decryption failed") {
		return "Failed to decrypt secrets. The encryption password may be incorrect or secrets are corrupted."
	}
	if strings.Contains(lower, "password") && strings.Contains(lower, "unavailable") {
		return "Encryption key is not available for this project. Ask a project owner to set up encryption."
	}
	if strings.Contains(lower, "password") || (strings.Contains(lower, "encrypt") && !strings.Contains(lower, "to encrypt")) {
		return "Failed to handle encryption. The project may not have an encryption key set up yet."
	}

	// File/Directory issues
	if strings.Contains(lower, "read local") || strings.Contains(lower, "no such file") {
		return "Could not read the local .env file. Pull secrets first, then try again."
	}
	if strings.Contains(lower, "permission denied") {
		return "Permission denied. You don't have access to this project or environment."
	}

	// Signature/Data integrity
	if strings.Contains(lower, "signature") {
		return "Secret signature verification failed. The data may be corrupted or tampered with."
	}
	if strings.Contains(lower, "invalid json") || strings.Contains(lower, "unmarshal") {
		return "Received invalid data from server. Try refreshing the page."
	}

	// Version conflicts
	if strings.Contains(lower, "version") && strings.Contains(lower, "conflict") {
		return "This version has been updated by another user. Pull the latest version and try again."
	}

	// Team/Role issues
	if strings.Contains(lower, "insufficient") && (strings.Contains(lower, "permission") || strings.Contains(lower, "role")) {
		return "You don't have permission to perform this action. Ask an admin to upgrade your role."
	}
	if strings.Contains(lower, "owner cannot be removed") || strings.Contains(lower, "owner cannot be") {
		return "Cannot modify the project owner. Only another owner can make this change."
	}

	// Default fallback with hint
	return message + " — Contact support if this continues to happen."
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
const S={project:'',env:'dev',data:null,baseline:'',view:'secrets',loading:false,ready:false,remoteVersion:0};const $=id=>document.getElementById(id);const valid=x=>x&&x!=='null'&&x!=='undefined';const dirty=()=>!!$('editor')&&$('editor').value!==S.baseline;const leaveEditor=()=>!dirty()||confirm('You have unsaved edits. Leave without pushing them?');
async function requestJSON(path,options={}){const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),15000);try{const response=await fetch(path,{...options,signal:controller.signal});const data=await response.json();if(data.error)throw Error(data.error);return data}catch(error){if(error.name==="AbortError")throw Error("The request timed out. Check the server and try again.");throw error}finally{clearTimeout(timer)}}async function api(path){return requestJSON(path)}async function post(path,body){return requestJSON(path,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)})}function q(){return 'slug='+encodeURIComponent(S.project)+'&env='+encodeURIComponent(S.env)}function toast(message,error=false){const t=$('toast');t.textContent=message;t.className='toast'+(error?' error':'');t.hidden=false;setTimeout(()=>t.hidden=true,5000)}function esc(x){return String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#039;'}[c]))}function time(x){if(!x)return 'unknown';const d=(Date.now()-new Date(x))/1000;if(d<60)return 'just now';if(d<3600)return Math.floor(d/60)+'m ago';if(d<86400)return Math.floor(d/3600)+'h ago';return new Date(x).toISOString().slice(0,10)}function count(){const n=($('editor').value.match(/^\s*[^#\s][^=]*=/gm)||[]).length;$('keys').textContent=n;$('state').textContent=$('editor').value===S.baseline?'clean':'edited'}
function nav(view){if(!['secrets','history','team','tokens','audit','workspace','guide'].includes(view))view='secrets';if(view!==S.view&&!leaveEditor())return;S.view=view;document.querySelectorAll('.nav button').forEach(b=>b.classList.toggle('active',b.dataset.view===view));document.querySelectorAll('.view').forEach(v=>v.classList.toggle('active',v.id==='view-'+view));location.hash=view;if(view==='history')history();if(view==='workspace')workspace();if(view==='audit')audit()}
async function load(){try{const me=await api('/api/me');$('user').textContent=me.user?.username?'@'+me.user.username:'connected';$('serverText').textContent=me.server_url||'local';const ps=await api('/api/projects');const list=(ps.projects||[]).filter(p=>valid(p.slug));$('project').innerHTML=list.map(p=>'<option value="'+esc(p.slug)+'">'+esc(p.slug)+'</option>').join('');if(!list.length){$('project').innerHTML='<option>no projects</option>';toast('No projects found — run dotsync init',true);return}S.project=me.project?.project_slug||list[0].slug;$('project').value=S.project;S.env=me.project?.default_env||'dev';await project();nav((location.hash||'#secrets').slice(1))}catch(e){toast(e.message,true)}}
async function project(){S.data=await api('/api/project/?slug='+encodeURIComponent(S.project));const envs=(S.data.envs||[]).map(e=>typeof e==='string'?e:e.name).filter(Boolean);if(!envs.length)envs.push('dev','staging','production');if(!envs.includes(S.env))S.env=envs[0];$('envs').innerHTML=envs.map(e=>'<button class="env '+(e===S.env?'active':'')+'" data-env="'+esc(e)+'">'+esc(e)+'</button>').join('');$('envStat').textContent=S.env;$('tokenEnv').innerHTML=envs.map(e=>'<option value="'+esc(e)+'">'+esc(e)+'</option>').join('');document.querySelectorAll('.env').forEach(b=>b.onclick=async()=>{if(!leaveEditor())return;S.contextChange=true;S.env=b.dataset.env;await project()});renderTeam();renderTokens();renderAudit();await pull(false)}
async function pull(notify=true){if(S.loading)return;S.loading=true;S.pullError=false;$('pull').disabled=true;try{let latest=null;try{latest=await api('/api/latest?'+q())}catch(_){latest=null}const current=Number(S.remoteVersion||String($('version').textContent||'').replace(/^v/,''));if(latest&&Number(latest.version)>0&&current===Number(latest.version)){S.remoteVersion=Number(latest.version);if(notify)toast(dirty()?'Remote unchanged · local edits preserved':'Already up to date · v'+latest.version);return{unchanged:true,version:Number(latest.version)}}const r=await api('/api/pull?'+q());$('editor').value=r.content||'';S.baseline=$('editor').value;S.remoteVersion=Number(r.version)||0;$('version').textContent='v'+r.version;$('author').textContent=r.by?'@'+r.by:'';$('secretMeta').textContent='Encrypted remote state · last updated '+time(r.created_at);count();if(notify)toast('Pulled v'+r.version+' · '+r.keys+' keys');return r}catch(e){S.pullError=true;$('version').textContent='—';$('author').textContent='unavailable';toast(e.message,true)}finally{S.loading=false;$('pull').disabled=false}}
async function push(){const content=$('editor').value.trim();if(!content){toast('Editor is empty',true);return}if(S.loading)return;S.loading=true;$('push').disabled=true;try{const r=await post('/api/push',{slug:S.project,env:S.env,content});S.baseline=$('editor').value;count();$('version').textContent='v'+r.version;S.remoteVersion=Number(r.version)||0;toast('Pushed v'+r.version+' · '+r.keys+' keys')}catch(e){toast(e.message,true)}finally{S.loading=false;$('push').disabled=false}}
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
const monochrome=document.createElement('style');monochrome.textContent=':root{--ink:#171814;--muted:#696d63;--faint:#8b8e84;--bg:#f2f2ee;--panel:#fff;--panel2:#f0f0eb;--line:#d8d8d0;--blue:#171814;--violet:#696d63;--green:#171814;--amber:#171814;--red:#171814}::selection{background:#171814;color:#fff}.dot{background:#171814;box-shadow:none}.env.active{border-color:#171814;background:#171814;color:#fff}.btn.good,.pill.good{background:#f0f0eb;border-color:#bfc1b8;color:#171814}.btn.danger{color:#171814}.pill.warn,.notice.warn{background:#f0f0eb;color:#171814}.eyebrow,.code{color:#171814}.token-card-head span{color:#696d63}.token-value{background:#fff;color:#171814}.toast.error{border-color:#171814;color:#fff}.toast.ok .toast-symbol,.toast.error .toast-symbol{color:#fff}.run-output{color:#171814}';document.head.appendChild(monochrome);
const keyFilterStyle=document.createElement('style');keyFilterStyle.textContent='.key-filter-results{display:none!important}.editor-stage{position:relative;min-height:420px}.editor-stage .editor-mirror{position:absolute;inset:0;z-index:0;margin:0;overflow:hidden;pointer-events:none;white-space:pre-wrap;overflow-wrap:anywhere;color:#171814;background:#fafaf7;border:1px solid #cfcfc6;border-radius:3px;padding:18px;font:13px/1.7 ui-monospace,SFMono-Regular,Menlo,monospace}.editor-stage .editor-layer{position:relative;z-index:1;background:transparent;color:transparent;caret-color:#171814;border-color:#cfcfc6}.editor-stage .editor-layer::selection{background:#cfcfc8;color:#171814}.editor-stage .editor-layer::placeholder{color:#696d63}.editor-mirror mark{background:#d5d5cf;color:#171814;border-radius:2px;padding:1px 0}';keyFilterStyle.textContent+='.audit-controls{margin-bottom:14px;padding-bottom:14px;border-bottom:1px solid #d8d8d0}.audit-controls:before{content:"Filter audit log";display:block;width:100%;font-size:12px;font-weight:650;color:#171814;margin-bottom:4px}.audit-controls .input{min-width:150px}';document.head.appendChild(keyFilterStyle);
const motionStyle=document.createElement('style');motionStyle.textContent='.btn,.nav button,.env,.row,.input,.top select{transition:background-color .16s ease,border-color .16s ease,color .16s ease,transform .16s ease,box-shadow .16s ease}.btn:active{transform:translateY(1px)}.row:hover{border-color:#aeb1a6;transform:translateY(-1px)}.view.active{animation:viewIn .18s ease both}@keyframes viewIn{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:none}}#toast{display:flex}#toast[hidden]{display:none!important}align-items:flex-start;gap:9px;max-width:360px;line-height:1.4}#toast .toast-symbol{font-weight:800;flex:0 0 auto}#toast.error .toast-symbol{color:#ef8b79}#toast.ok .toast-symbol{color:#82d0a0}@media(prefers-reduced-motion:reduce){.btn:active,.row:hover{transform:none}.view.active{animation:none}}';document.head.appendChild(motionStyle);
const a11y=document.createElement('style');a11y.textContent='.btn:focus-visible,.input:focus-visible,.top select:focus-visible,.env:focus-visible,.nav button:focus-visible{outline:2px solid #171814;outline-offset:2px}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto!important;transition:none!important}}';document.head.appendChild(a11y);
const toastCore=toast;let toastTimer;toast=(message,error=false)=>{clearTimeout(toastTimer);toastCore(message,error);const t=$('toast');t.setAttribute('role',error?'alert':'status');t.setAttribute('aria-live',error?'assertive':'polite');t.classList.toggle('ok',!error);t.innerHTML='<span class="toast-symbol">'+(error?'!':'✓')+'</span><span>'+esc(message)+'</span>';toastTimer=setTimeout(()=>{t.hidden=true},5000);t.onclick=()=>{t.hidden=true}};
const overlay=document.createElement('div');overlay.id='busy';overlay.innerHTML='<span class="spinner"></span><span id="busyText">Working…</span>';document.body.appendChild(overlay);let busyTimer;const showBusy=message=>{clearTimeout(busyTimer);$('busyText').textContent=message;overlay.classList.add('on');busyTimer=setTimeout(()=>overlay.classList.remove('on'),15000)};const hideBusy=()=>{clearTimeout(busyTimer);overlay.classList.remove('on')};const busyStyle=document.createElement('style');busyStyle.textContent='#boot{position:fixed;inset:0;z-index:30;background:#f2f2ee;color:#171814;display:grid;place-content:center;justify-items:center;gap:12px;text-align:center;padding:28px;transition:opacity .24s}.boot-mark{width:52px;height:52px;display:grid;place-items:center;background:#171814;color:#fff;font-weight:800;font-size:24px;border-radius:3px}.boot-title{font-size:20px;font-weight:750}.boot-copy{color:#696d63;max-width:360px}.boot-track{width:180px;height:3px;background:#d8d8d0;overflow:hidden}.boot-track i{display:block;width:60px;height:100%;background:#171814;animation:boot 1.1s ease-in-out infinite}@keyframes boot{0%{transform:translateX(-60px)}100%{transform:translateX(180px)}}#boot.error{background:#171814;color:#fff}.boot-retry{border:1px solid #fff;background:transparent;color:#fff;padding:9px 14px;border-radius:3px;cursor:pointer}#boot.done{opacity:0;pointer-events:none}#busy{position:fixed;inset:0;z-index:25;background:#171814d9;color:#fff;display:none;place-content:center;justify-items:center;gap:10px;font:13px ui-monospace,SFMono-Regular,Menlo,monospace}#busy.on{display:grid}';document.head.appendChild(busyStyle);
$('project').setAttribute('aria-label','Project');$('editor').setAttribute('aria-label','Environment secrets editor');const pullCore=pull;pull=async notify=>{const b=$('pull'),label=b.textContent;b.innerHTML='<span class="spinner"></span>Pulling';try{return await pullCore(notify)}finally{b.textContent=label;S.ready=true;syncEditorMatches();hideBoot()}};const pushCore=push;push=async()=>{const b=$('push'),label=b.textContent;b.innerHTML='<span class="spinner"></span>Pushing';try{return await pushCore()}finally{b.textContent=label}};$('pull').onclick=()=>pull(true);$('push').onclick=push;
const nativeProject=$('project'),picker=document.createElement('div');picker.className='project-picker';nativeProject.parentNode.insertBefore(picker,nativeProject);picker.appendChild(nativeProject);const projectTrigger=document.createElement('button');projectTrigger.type='button';projectTrigger.className='project-trigger';projectTrigger.setAttribute('aria-haspopup','listbox');projectTrigger.setAttribute('aria-expanded','false');const projectMenu=document.createElement('div');projectMenu.className='project-menu';projectMenu.setAttribute('role','listbox');picker.insertBefore(projectTrigger,nativeProject);picker.appendChild(projectMenu);const syncPicker=()=>{const options=[...nativeProject.options];const selected=options.find(o=>o.value===nativeProject.value)||options[0];projectTrigger.textContent=selected?selected.textContent:'Choose project';projectMenu.innerHTML=options.map(o=>'<button type="button" class="project-option '+(o===selected?'selected':'')+'" role="option" aria-selected="'+(o===selected)+'" data-value="'+esc(o.value)+'">'+esc(o.textContent)+'</button>').join('')};const togglePicker=open=>{picker.classList.toggle('open',open);projectTrigger.setAttribute('aria-expanded',String(open))};projectTrigger.onclick=()=>togglePicker(!picker.classList.contains('open'));projectMenu.onclick=e=>{const option=e.target.closest('.project-option');if(!option)return;nativeProject.value=option.dataset.value;nativeProject.dispatchEvent(new Event('change',{bubbles:true}));syncPicker();togglePicker(false)};document.addEventListener('click',e=>{if(!picker.contains(e.target))togglePicker(false)});new MutationObserver(syncPicker).observe(nativeProject,{childList:true});syncPicker();
function makeSelectMenu(id){const native=$(id);if(!native)return;const wrap=document.createElement('div');wrap.className='select-picker';native.parentNode.insertBefore(wrap,native);wrap.appendChild(native);const trigger=document.createElement('button');trigger.type='button';trigger.className='select-trigger';trigger.setAttribute('aria-haspopup','listbox');trigger.setAttribute('aria-expanded','false');const menu=document.createElement('div');menu.className='select-menu';menu.setAttribute('role','listbox');wrap.insertBefore(trigger,native);wrap.appendChild(menu);const sync=()=>{const options=[...native.options],selected=options.find(o=>o.value===native.value)||options[0];trigger.textContent=selected?selected.textContent:'Choose an option';menu.innerHTML=options.map(o=>'<button type="button" class="select-option '+(o===selected?'selected':'')+'" role="option" aria-selected="'+(o===selected)+'" data-value="'+esc(o.value)+'">'+esc(o.textContent)+'</button>').join('')};const close=()=>{wrap.classList.remove('open');trigger.setAttribute('aria-expanded','false')};trigger.onclick=()=>{const open=!wrap.classList.contains('open');wrap.classList.toggle('open',open);trigger.setAttribute('aria-expanded',String(open))};menu.onclick=e=>{const option=e.target.closest('.select-option');if(!option)return;native.value=option.dataset.value;native.dispatchEvent(new Event('change',{bubbles:true}));sync();close()};document.addEventListener('click',e=>{if(!wrap.contains(e.target))close()});new MutationObserver(sync).observe(native,{childList:true});sync()};makeSelectMenu('memberRole');makeSelectMenu('tokenEnv');
const linkControl=$('linkProject');setTimeout(()=>{if(linkControl){const linkCard=linkControl.closest('.card');if(linkCard)linkCard.remove()}},0);workspace=async()=>{showBusy('Loading workspace comparison…');try{return await diff()}finally{hideBusy()}};
$('editor').insertAdjacentHTML('afterend','<div id="keyFilterResults" class="key-filter-results" hidden></div>');let searchTerm="",searchAnchor=0;const selectSearchMatch=(term,backwards=false)=>{const needle=term.trim().toLowerCase();if(!needle)return false;if(needle!==searchTerm){searchTerm=needle;searchAnchor=backwards?Number.MAX_SAFE_INTEGER:0}const source=document.getElementById("editor").value;const hits=[];let from=0;while((from=source.toLowerCase().indexOf(needle,from))>=0){hits.push(from);from+=Math.max(needle.length,1)}if(!hits.length)return false;let hit;if(backwards){const prior=hits.filter(x=>x<searchAnchor);hit=prior.length?prior[prior.length-1]:hits[hits.length-1]}else{const next=hits.find(x=>x>=searchAnchor);hit=next===undefined?hits[0]:next}const editor=document.getElementById("editor");editor.focus();editor.setSelectionRange(hit,hit+needle.length);searchAnchor=backwards?hit:hit+needle.length;return true};const renderKeyFilter=term=>{const box=$('keyFilterResults'),needle=term.trim().toLowerCase();if(!needle){box.hidden=true;box.innerHTML='';return}const lines=$('editor').value.split('\n'),matches=lines.map((line,index)=>({line,index})).filter(x=>{const key=x.line.split('=')[0].trim().toLowerCase();return key&&key.includes(needle)});box.hidden=false;box.innerHTML='<div class="key-filter-meta">'+matches.length+' matching key'+(matches.length===1?'':'s')+'</div>'+matches.slice(0,80).map(x=>'<button type="button" class="key-filter-item" data-line="'+x.index+'">'+esc(x.line.split('=')[0].trim())+'</button>').join('');box.querySelectorAll('[data-line]').forEach(b=>b.onclick=()=>{const line=Number(b.dataset.line),lines=$('editor').value.split('\n'),start=lines.slice(0,line).join('\n').length+(line?1:0);$('editor').focus();$('editor').setSelectionRange(start,start+lines[line].length)})};
const editorStage=document.createElement('div');editorStage.className='editor-stage';const editorMirror=document.createElement('pre');editorMirror.className='editor-mirror';const editorNode=$('editor');editorNode.parentNode.insertBefore(editorStage,editorNode);editorStage.appendChild(editorMirror);editorStage.appendChild(editorNode);editorNode.classList.add('editor-layer');const syncEditorMatches=()=>{const source=editorNode.value||'';const term=String($('find')?.value||'').trim();if(!term){editorMirror.textContent=source||' ';return}const pattern=new RegExp(term.replace(/[.*+?^${}()|[\]\\]/g,'\\$&'),'gi');let cursor=0,html='';for(const match of source.matchAll(pattern)){html+=esc(source.slice(cursor,match.index));html+='<mark>'+esc(match[0])+'</mark>';cursor=(match.index||0)+match[0].length}editorMirror.innerHTML=html+esc(source.slice(cursor))||' ';};editorNode.addEventListener('scroll',()=>{editorMirror.scrollTop=editorNode.scrollTop;editorMirror.scrollLeft=editorNode.scrollLeft});editorNode.addEventListener('input',syncEditorMatches);syncEditorMatches();
$('view-tokens').querySelector('.hero p').textContent='Create a narrowly scoped credential for GitHub Actions, deployment jobs, or another automated process. It can pull secrets without using your personal login; the raw value is shown once, so copy it into your CI secret store immediately.';
const historyCore=history;history=async()=>{const box=$('historyList');box.innerHTML='<div class="empty"><span class="spinner"></span>Loading history</div>';try{return await historyCore()}finally{}};$('refreshHistory').onclick=history;const auditCore=audit;audit=async()=>{const box=$('auditList');box.innerHTML='<div class="empty"><span class="spinner"></span>Loading audit events</div>';try{return await auditCore()}finally{}};$('refreshAudit').onclick=audit;const diffCore=diff;diff=async()=>{const box=$('diffList');box.innerHTML='<div class="empty"><span class="spinner"></span>Comparing local and remote keys</div>';return diffCore()};
const addCore=addMember;addMember=async()=>{const b=$('addMember'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Adding';try{return await addCore()}finally{b.disabled=false;b.textContent=label}};$('addMember').onclick=addMember;const tokenCore=createToken;createToken=async()=>{const b=$('createToken'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Creating';try{return await tokenCore()}finally{b.disabled=false;b.textContent=label}};$('createToken').onclick=createToken;
const modalStyle=document.createElement('style');modalStyle.textContent='.choice-backdrop{position:fixed;inset:0;z-index:40;background:#17181480;display:grid;place-items:center;padding:20px}.choice{width:min(390px,100%);background:#fff;border:1px solid #cfcfc6;box-shadow:0 20px 50px #17181432;padding:22px}.choice h2{font-size:16px;margin:0 0 7px;color:#171814}.choice p{margin:0;color:#696d63;line-height:1.5}.choice-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}.choice-actions button{border:1px solid #cfcfc6;background:#fff;color:#171814;padding:9px 14px;border-radius:3px;cursor:pointer}.choice-actions .yes{background:#171814;color:#fff;border-color:#171814}';document.head.appendChild(modalStyle);function askYesNo(message){return new Promise(resolve=>{const backdrop=document.createElement('div');backdrop.className='choice-backdrop';backdrop.innerHTML='<div class="choice" role="dialog" aria-modal="true" aria-labelledby="choice-title"><h2 id="choice-title">Run command?</h2><p>'+esc(message)+'</p><div class="choice-actions"><button class="no">No</button><button class="yes">Yes</button></div></div>';document.body.appendChild(backdrop);const finish=value=>{backdrop.remove();resolve(value)};backdrop.querySelector('.no').onclick=()=>finish(false);backdrop.querySelector('.yes').onclick=()=>finish(true);backdrop.onclick=e=>{if(e.target===backdrop)finish(false)};backdrop.querySelector('.yes').focus()})}const runCore=run;run=async()=>{const b=$('run'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Running';try{return await runCore()}finally{b.disabled=false;b.textContent=label}};$('run').onclick=run;
$('compare').onclick=diff;const linkCore=$('linkWorkspace').onclick;$('linkWorkspace').onclick=async()=>{const b=$('linkWorkspace'),label=b.textContent;b.disabled=true;b.innerHTML='<span class="spinner"></span>Linking';try{return await linkCore()}finally{b.disabled=false;b.textContent=label}};
document.addEventListener('click',e=>{const b=e.target.closest('[data-remove],[data-revoke],[data-view-version],[data-restore]');if(!b)return;b.disabled=true;b.dataset.label=b.textContent;b.innerHTML='<span class="spinner"></span>Working';setTimeout(()=>{if(b.isConnected){b.disabled=false;b.textContent=b.dataset.label}},12000)},true);
$('project').onchange=async e=>{if(!leaveEditor()){$('project').value=S.project;return}S.contextChange=true;S.project=e.target.value;await project()};
const projectCore=project;project=async()=>{const contextChange=!!S.contextChange;const target=S.project;const message=contextChange?'Loading project data…':'Refreshing project data…';if(contextChange){$('editor').value='';S.baseline='';$('version').textContent='—';S.remoteVersion=0;$('author').textContent='Loading…';$('secretMeta').textContent='Loading '+target+'…';$('keys').textContent='—';$('state').textContent='loading';$('tokenReveal').innerHTML='';$('find').value='';$('keyFilterResults').hidden=true;$('keyFilterResults').innerHTML='';syncEditorMatches()}S.contextChange=false;showBusy(message);try{const result=await projectCore();if(contextChange){$('runOutput').textContent='Output will appear here.';['auditSearch','auditAction','auditUser','auditEnv'].forEach(id=>{const node=$(id);if(node)node.value=''});renderAudit()}if(contextChange)toast(S.pullError?'Project loaded, but secrets could not be pulled':'Loaded '+target+'/'+S.env,!!S.pullError);return result}catch(e){if(contextChange)toast('Could not load '+target,true);throw e}finally{hideBusy()}};const bootWatch=setInterval(()=>{if(S.ready){hideBoot();clearInterval(bootWatch)}},80);setTimeout(()=>{if(!S.ready){showBootError('The local workspace could not be loaded. Check that you are logged in and that this folder is linked with dotsync init.')}},15000);
$('runCommand').addEventListener('keydown',e=>{if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();run()}});const clearRun=document.createElement('button');clearRun.type='button';clearRun.className='btn';clearRun.textContent='Clear output';clearRun.onclick=()=>{$('runOutput').textContent='Output will appear here.';toast('Output cleared')};$('run').parentNode.appendChild(clearRun);
$('copy').onclick=()=>navigator.clipboard.writeText($('editor').value).then(()=>toast('Copied')).catch(()=>toast('Clipboard access was denied',true));
$('find').oninput=e=>syncEditorMatches();$('find').onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();if(!selectSearchMatch(e.target.value,e.shiftKey))toast('No matching text')}};$('find').onkeyup=e=>{if(e.key==='Escape'){e.target.value='';searchTerm='';searchAnchor=0;syncEditorMatches()}};$('editor').addEventListener('input',()=>{const find=$('find');if(find&&find.value)syncEditorMatches()});

document.addEventListener('keydown',e=>{const meta=e.metaKey||e.ctrlKey;if(meta&&e.key.toLowerCase()==='s'){e.preventDefault();push()}if(meta&&e.key.toLowerCase()==='l'){e.preventDefault();pull(true)}});window.addEventListener('beforeunload',e=>{if(dirty()){e.preventDefault();e.returnValue=''}});
const guideMarkup='<div class="view" id="view-guide"><div class="hero"><div><div class="eyebrow">Built-in guide</div><h1>How DotSync works</h1><p>Complete documentation for managing secrets, team access, security, and operations in DotSync. Learn the concepts, workflows, and best practices used by successful teams.</p></div></div><div class="grid" style="grid-template-columns:repeat(auto-fit,minmax(280px,1fr))"><div class="card"><div class="card-title">🚀 Getting Started</div><p class="muted"><strong>Step 1: Authentication</strong> - Run <code>dotsync login</code> in your terminal. This creates a local credential file that authenticates future operations. Your credentials never leave your machine unencrypted.</p><p class="muted"><strong>Step 2: Project Setup</strong> - Navigate to your project folder and run <code>dotsync init</code>. This links your local directory to a DotSync project and environment. Choose from existing projects or create a new one.</p><p class="muted"><strong>Step 3: Open Dashboard</strong> - Run <code>dotsync ui</code> to launch the web dashboard. The browser will open automatically. All operations run on your local machine at <code>localhost:4040</code>. Close the terminal to stop the server.</p><p class="muted"><strong>Step 4: Load Secrets</strong> - Click the Secrets tab and press Pull to load your environment\'s secrets. Select a different environment from the header dropdown to switch contexts without reloading.</p></div><div class="card"><div class="card-title">🔐 Understanding Environments</div><p class="muted"><strong>What are environments?</strong> - Environments partition secrets by deployment context: development, staging, production, testing, etc. Each environment has its own set of secret values and access controls. A single project can have 10+ environments.</p><p class="muted"><strong>When to use multiple environments</strong> - Always separate development, staging, and production. This prevents accidental production changes and allows different access levels per environment. Your development environment might be accessible to all engineers, while production is restricted to ops.</p><p class="muted"><strong>Environment-specific configs</strong> - Database connection strings, API endpoints, and feature flags vary per environment. Database URLs point to development for dev, staging database for staging, and production database for production.</p><p class="muted"><strong>Switching contexts</strong> - The header shows your current project and environment. Click to switch. All subsequent pulls, pushes, and commands use the new environment. The UI preserves your editor state when switching environments of the same project, but clears when switching projects.</p></div><div class="card"><div class="card-title">📝 Secrets and the Editor</div><p class="muted"><strong>Pull operation</strong> - Fetches the latest encrypted secrets from the server, decrypts them in your browser, and displays them in the editor. Pull is safe to run anytime and doesn\'t affect the server. Use Pull before editing to ensure your baseline is current. Pulling while another change is in progress asks for confirmation.</p><p class="muted"><strong>Making changes</strong> - The editor shows one secret per line in <code>KEY=value</code> format. Modify values by clicking, use Find (Ctrl+F) to search by key name, and the editor highlights matching lines. The editor indicates unsaved changes with a blue dot next to the environment name.</p><p class="muted"><strong>Reviewing before push</strong> - Click the diff icon to see what changed. Diff shows added lines (green), removed lines (red), and modified values. This review step prevents accidental changes. DotSync hides the actual new secret value in the diff for security—you see only that a line changed, not what it changed to.</p><p class="muted"><strong>Push operation</strong> - Encrypts all secrets on your machine, signs the encrypted payload, and sends it to the server. The server verifies the signature before storing. Push creates a new version in history and increments the version counter. No temporary files are created. All encryption happens in your browser.</p></div><div class="card"><div class="card-title">🔑 Service Tokens Explained</div><p class="muted"><strong>When to use tokens</strong> - Use service tokens for any system that needs secrets without a human login: CI/CD pipelines, scheduled jobs, deployment runners, third-party integrations, and serverless functions. Each automated system should have its own token.</p><p class="muted"><strong>Creating tokens</strong> - Go to the Tokens tab and click Create. Give it a descriptive name like <code>github-actions-prod</code> or <code>scheduled-backup-staging</code>. Select which environment(s) this token can access. A token scoped to staging cannot pull production secrets even if a team member has production access.</p><p class="muted"><strong>One-time reveal</strong> - The token value displays once after creation. Copy it immediately to your secret store (GitHub Secrets, GitLab CI variables, etc.). The value is never shown again. If you lose the token, delete and create a new one.</p><p class="muted"><strong>Token lifecycle</strong> - Best practice: create the new token, update the external system to use it, verify the system works, then revoke the old token. This prevents downtime. Always name tokens descriptively so you know what revokes when. Audit will show which token was used for which operation.</p><p class="muted"><strong>Rotating tokens</strong> - If a token is exposed, revoke it immediately. The server stops accepting it within seconds. Create a replacement and update your systems. Audit logs show the old token\'s last use date and IP address.</p><p class="muted"><strong>Token permissions vs user roles</strong> - A token can only pull secrets; it cannot push, create versions, or access team controls. A token is read-only and environment-scoped. Personal tokens (your login) have broader permissions based on your role.</p></div><div class="card"><div class="card-title">👥 Team Access & Roles</div><p class="muted"><strong>Role types</strong> - <strong>Owner</strong>: Created the project, manages all settings, cannot be changed or removed. <strong>Admin</strong>: Can modify project settings, manage team members, approve trusted integrations. <strong>Member</strong>: Can pull, push, and view history. <strong>Viewer</strong>: Can only pull and view history, cannot modify secrets.</p><p class="muted"><strong>Managing membership</strong> - Owners add team members via the Team tab. Invite by email address. Members must be logged in and linked to the project before they can access secrets. After inviting, the user appears with a pending status until they first access the project.</p><p class="muted"><strong>Changing roles</strong> - Owners and admins can change roles for any non-owner member. Promote a viewer to member for push access, or demote a member to viewer for read-only access. Changes take effect immediately. Changing roles does not affect the member\'s audit history.</p><p class="muted"><strong>Removing members</strong> - Only the owner can remove team members. Removed members lose all access to the project including to its history, tokens, and audit logs. The audit log retains records of actions taken by that member before removal.</p><p class="muted"><strong>Default environment access</strong> - All team members get the same access to all environments. If someone is a member, they can access staging, production, etc. Use separate projects if you need to restrict access to specific environments.</p></div><div class="card"><div class="card-title">⏱️ History & Version Control</div><p class="muted"><strong>What history does</strong> - Every push creates a new version. History shows all past versions with metadata: who pushed, when, from which IP address, and the environment. Versions are encrypted and numbered sequentially.</p><p class="muted"><strong>Inspecting versions</strong> - Click a version to view what secrets were in that state. DotSync shows you the actual values (since you have access), the diff from the previous version, and the user who made the change. This helps answer "what were the DB credentials on Tuesday?"</p><p class="muted"><strong>Restoring a version</strong> - Click Restore to make a past version the new current version. Restore does NOT delete the version history. Instead, it creates a new current version identical to the one you chose. The timeline remains intact. You can always see what was restored and who restored it in audit logs.</p><p class="muted"><strong>Version safety</strong> - You cannot accidentally corrupt the version history. Restoring an old version doesn\'t change how it\'s stored—it just becomes the active version again. Users pulling after a restore get the old secrets. Users pulling before the restore saw the intermediate version.</p><p class="muted"><strong>Retention policy</strong> - All versions are retained indefinitely by default. Some deployments configure retention policies (e.g., keep 30 days, keep 100 versions). Check with your team\'s DotSync administrator for your project\'s retention policy.</p></div><div class="card"><div class="card-title">🖥️ Workspace Commands</div><p class="muted"><strong>What are workspace commands?</strong> - Commands let you run programs or scripts with selected secrets temporarily available, without writing them to disk. Ideal for local testing, one-off operations, and deployment verification.</p><p class="muted"><strong>Setting up</strong> - Go to Workspace tab. Pull secrets to load them. Enter a command in the prompt, e.g., <code>npm test</code> or <code>python deploy.py</code>. Confirm the command, press Enter to execute. The secrets are available only for the duration of that command.</p><p class="muted"><strong>How it works</strong> - DotSync spawns a subprocess with an isolated environment containing the secrets. Arguments are passed directly without shell interpretation. Output is captured and displayed. When the process exits, the environment is cleared and secrets are flushed from memory.</p><p class="muted"><strong>No temporary files</strong> - Unlike <code>.env</code> files, workspace commands never write secrets to disk, even temporarily. They never appear in process listings, swap, or core dumps of other processes. Only the subprocess sees them.</p><p class="muted"><strong>Exit codes and output</strong> - DotSync shows the full command you ran, all output (stdout and stderr), the exit code, and elapsed time. This makes debugging easier and provides an audit trail of what commands ran.</p><p class="muted"><strong>Common use cases</strong> - Running tests with production database connection (e.g., read-only backups), deploying with API keys, validating that secrets are correct before a production push, running schema migrations.</p></div><div class="card"><div class="card-title">📊 Audit & Compliance</div><p class="muted"><strong>What gets audited</strong> - Every meaningful action: pull, push, history restore, token creation/revocation, team changes, and role changes. Audit includes: who (user or token name), what action, when, from which IP, and which environment.</p><p class="muted"><strong>Privacy by design</strong> - Audit records actions, not secret values. Pushing a new database password records that someone pushed, not what the password is. Reading a secret (pull) is logged, but the actual values never appear in audit logs.</p><p class="muted"><strong>Filtering audit logs</strong> - Search by free text, filter by action (push, pull, etc.), filter by user name, or filter by environment. Combine multiple filters to answer specific questions: "which token pushed to production last week?", "what did alice do in staging?", "when was this API key last pulled?"</p><p class="muted"><strong>Using audit for compliance</strong> - Export audit logs for compliance reviews and incident investigations. DotSync provides immutable records with cryptographic signatures. Log entries cannot be edited or deleted—only new entries can be added.</p><p class="muted"><strong>Audit retention</strong> - By default, audit logs are kept indefinitely. Enterprise deployments may configure retention (e.g., keep 1 year of audit data). Check your organization\'s policy.</p></div><div class="card"><div class="card-title">🔐 The Security Model</div><p class="muted"><strong>End-to-end encryption</strong> - Secrets are encrypted on your machine before leaving your browser. The server never sees plaintext. Only someone with the decryption key (you, team members with access, and tokens scoped to that environment) can read values.</p><p class="muted"><strong>How encryption works</strong> - When you push, DotSync encrypts the entire environment as a single unit using your project\'s encryption key. The encrypted blob is signed with your authentication key. The server verifies the signature to confirm the push came from an authorized user or token.</p><p class="muted"><strong>Key management</strong> - Encryption keys are stored server-side but are never transmitted to your machine. Instead, your login credentials derive decryption capability. The server uses keys to encrypt/decrypt when you pull. This means your personal credentials authenticate you, and the server does the crypto work.</p><p class="muted"><strong>Browser security</strong> - Decrypted secrets only exist in your browser\'s memory while you\'re viewing them. They\'re never logged, never stored in the DOM after the page closes, and never cached. Closing the browser tab clears all decrypted data from memory.</p><p class="muted"><strong>Network security</strong> - All communication uses HTTPS. When connecting to a remote DotSync server, verify the certificate is valid. For self-hosted instances, ensure TLS is configured with a trusted certificate authority.</p></div><div class="card"><div class="card-title">💼 Daily Workflow Best Practices</div><p class="muted"><strong>Before you edit</strong> - Always pull first to get the latest state. This prevents working with stale data. If another team member pushed an update, your pull gets their changes.</p><p class="muted"><strong>Make focused changes</strong> - Change one or a few related secrets at a time. Use the search bar (Ctrl+F) to find the keys you need. Review the diff to confirm you\'re changing the right values.</p><p class="muted"><strong>Push only after review</strong> - The diff shows you exactly what changed. Verify it matches your intent. Pay attention to whether you\'re in the right environment (dev vs. staging vs. production).</p><p class="muted"><strong>After pushing</strong> - Tell your team what changed, especially if it affects shared infrastructure. Example: "Updated DATABASE_URL in staging for the new replica server." This helps others understand why their pulls might return different values.</p><p class="muted"><strong>Collaborating safely</strong> - If two people edit the same environment simultaneously, the first person\'s push succeeds and increments the version. The second person\'s screen shows the stale version was updated. They should pull again and re-apply their changes.</p><p class="muted"><strong>Emergency changes</strong> - If production is down and you need to change credentials urgently, pull, make the change, review the diff carefully, and push. Immediately notify your team and create a post-incident review to ensure it doesn\'t happen again.</p></div><div class="card"><div class="card-title">🔧 Troubleshooting & Recovery</div><p class="muted"><strong>Pull fails</strong> - Verify your login is still valid (check the version in Settings). Confirm the folder is still linked to the project (run <code>dotsync status</code>). Check your internet connection and server reachability. If the server is down, you cannot pull. Look at history from a previous pull if you need recent values.</p><p class="muted"><strong>Push fails</strong> - The same checks as pull apply. Additionally, ensure you have write permissions for the environment. Check if another team member pushed just now—your version might be out of date. Pull, review what changed, merge your edits, and push again.</p><p class="muted"><strong>Lost a secret value</strong> - Use History to find the version before you deleted it. Click Restore to make that version current. This is safe and creates a new version entry in the timeline.</p><p class="muted"><strong>Secrets aren\'t updating in CI/CD</strong> - Verify the token has access to the correct environment. Confirm the job is pulling (not using a cached version from an earlier run). Check audit logs to see if the token\'s last pull was recent.</p><p class="muted"><strong>Can\'t open the web UI</strong> - Run <code>dotsync ui --port 4041</code> to try a different port. Confirm no other service is listening on port 4040. Check if your firewall is blocking localhost connections. Verify the project is linked (<code>dotsync status</code>).</p></div><div class="card"><div class="card-title">🚢 CI/CD & Automation</div><p class="muted"><strong>GitHub Actions integration</strong> - Create a service token scoped to your production environment. Store the token in GitHub Secrets. In your workflow, run <code>dotsync pull --token ${{ secrets.DOTSYNC_TOKEN }}</code> to load secrets into the CI runner. Secrets are available as environment variables.</p><p class="muted"><strong>GitLab CI integration</strong> - Similar to GitHub, create a token and store it in GitLab CI variables. Use <code>dotsync pull --token</code> in your CI script. Secrets are available immediately after the pull completes.</p><p class="muted"><strong>Container deployments</strong> - For Docker or Kubernetes deployments, create a deployment-specific token. Use <code>dotsync pull</code> in your container initialization to fetch secrets at startup. This works for any container orchestration platform.</p><p class="muted"><strong>Scheduled tasks</strong> - Create a token for each scheduled job (backups, data syncs, reports, etc.). Use the token in the scheduler\'s credentials. Audit logs will show exactly which job accessed which secrets and when.</p><p class="muted"><strong>Zero-downtime deployments</strong> - Create the new token first and deploy code that uses it. Verify the new system works. Then revoke the old token. This prevents deployment failures and creates a clear audit trail.</p></div><div class="card"><div class="card-title">🏆 Security Best Practices</div><p class="muted"><strong>Naming conventions</strong> - Use descriptive names for tokens and environments. Bad: <code>token1</code>, <code>prod</code>. Good: <code>github-actions-deploy-prod</code>, <code>production-us-east</code>. Descriptive names make audit investigation easier and prevent mistakes.</p><p class="muted"><strong>Least privilege access</strong> - Give people the minimum role they need. Not everyone needs admin access. New team members start as viewers or members, not admins. Contractors or temporary team members can be viewers.</p><p class="muted"><strong>Token rotation schedule</strong> - Rotate tokens quarterly or more frequently if the secret store (GitHub, GitLab, AWS) is ever compromised. Keep a rotation log so you know when each token was created and last used.</p><p class="muted"><strong>Immediate revocation</strong> - If a token is exposed (committed to a public repository, leaked in a log), revoke it immediately. DotSync stops accepting it instantly. Create a replacement and update systems within minutes.</p><p class="muted"><strong>Access review</strong> - Periodically review who has access to each project and environment. Remove people who\'ve left the team. Verify that active members still need their roles. Document business justification for high-risk access (production admin).</p><p class="muted"><strong>Monitor audit logs</strong> - Regularly review audit logs for unusual activity: unexpected pushes, access from unfamiliar IP addresses, or token usage at odd times. Configure alerts if your organization supports them.</p></div><div class="card"><div class="card-title">📈 Advanced Concepts</div><p class="muted"><strong>Environment inheritance</strong> - Some organizations copy secrets from staging to production as a template, then override environment-specific values. This is supported through pull, edit, push. No automatic inheritance is performed—you control what differs between environments.</p><p class="muted"><strong>Nested secret structures</strong> - While DotSync stores flat <code>KEY=value</code> pairs, your application can parse JSON values. Example: <code>DATABASE_CONFIG={"host":"localhost","port":5432}</code>. DotSync does not validate JSON; your application does.</p><p class="muted"><strong>Cross-project secrets</strong> - Some applications need secrets from multiple projects. Use separate tokens or ask your DotSync administrator to grant your account access to multiple projects. Then open the web UI for each project separately.</p><p class="muted"><strong>Bulk secret operations</strong> - For large imports, use the CLI: <code>dotsync secrets import < secrets.env</code>. This reads from a file and pushes all values. Alternatively, copy-paste in the editor for smaller changes.</p><p class="muted"><strong>Secret validation</strong> - DotSync stores any key=value pair. Validate values in your application. A malformed database URL will be stored as-is and cause errors at runtime, not in DotSync.</p></div></div></div>';$('view-workspace').insertAdjacentHTML('beforebegin',guideMarkup);
const guideStyle=document.createElement('style');guideStyle.textContent='#view-guide{max-width:980px;margin:0 auto}#view-guide .hero{margin-bottom:0;padding:18px 0 42px;border-bottom:0}#view-guide .hero .eyebrow:before{content:"FIELD NOTES  /  01";display:block;color:var(--muted);margin-bottom:20px}#view-guide .hero .eyebrow{font-size:0}#view-guide .hero h1{max-width:700px;font-size:clamp(3rem,8vw,7.5rem);line-height:.88;letter-spacing:-.07em}#view-guide .hero p{font-size:16px;line-height:1.7;max-width:560px;margin-top:28px;color:var(--muted)}#view-guide .grid{display:block;max-width:720px;margin:0 auto 0 8%;counter-reset:chapter}#view-guide .guide-intro{position:relative;display:grid;grid-template-columns:150px 1fr;gap:26px;margin:0 0 70px;padding:26px 0 34px;background:transparent;color:var(--ink);border:0;border-top:1px solid var(--line);border-bottom:1px solid var(--line)}#view-guide .guide-intro:before{content:"START HERE";font:11px var(--mono);letter-spacing:.14em;color:var(--muted)}#view-guide .guide-intro strong{font:24px/1.1 var(--sans);font-weight:500}#view-guide .guide-intro p{grid-column:2;color:var(--muted);max-width:58ch;line-height:1.75;margin:0}#view-guide .guide-intro p+p{margin-top:12px}#view-guide .guide-card{position:relative;min-height:0!important;margin:0!important;padding:0 0 58px 74px!important;background:transparent!important;border:0!important}#view-guide .guide-card:before{counter-increment:chapter;content:"0" counter(chapter);position:absolute;left:0;top:2px;font:12px var(--mono);color:var(--muted);letter-spacing:.08em}#view-guide .guide-card .card-title{display:block;margin:0 0 18px;font:500 30px/1.05 var(--sans);letter-spacing:-.035em;text-transform:none;color:var(--ink)}#view-guide .guide-card p{max-width:62ch;margin:0 0 15px;color:var(--muted);font:15px/1.8 var(--sans)}#view-guide .guide-card p strong{color:var(--ink);font-weight:600}#view-guide .guide-card p:first-of-type:before{content:"";display:block;width:42px;height:2px;background:var(--ink);margin:0 0 18px}#view-guide .guide-card:nth-of-type(3){padding:28px 34px 34px!important;margin:0 0 58px!important;background:var(--panel)!important;border-left:2px solid var(--ink)!important}#view-guide .guide-card:nth-of-type(3) .card-title{font-size:23px}#view-guide .guide-card:nth-of-type(3) p{font-size:14px}#view-guide .guide-card:nth-of-type(3):before{left:34px;top:-24px}#view-guide code{background:var(--panel2);color:var(--ink);padding:2px 5px;border-radius:0;font:12px var(--mono)}#view-guide .guide-intro+.guide-card:after{content:"A good secret workflow is deliberately uneventful: know what you are changing, make the smallest useful move, and leave a clear trail.";display:block;margin:10px 0 0 -74px;padding:22px 0 0;border-top:1px solid var(--line);font:italic 20px/1.45 Georgia,serif;color:var(--ink);max-width:620px}@media(max-width:700px){#view-guide .grid{margin-left:0}#view-guide .guide-intro{display:block;margin-bottom:48px}#view-guide .guide-intro:before{display:block;margin-bottom:18px}#view-guide .guide-intro p{margin-top:18px}#view-guide .guide-card{padding-left:42px!important}#view-guide .guide-card:nth-of-type(3){padding:24px!important}#view-guide .guide-card:nth-of-type(3):before{left:24px;top:-22px}#view-guide .guide-intro+.guide-card:after{margin-left:-42px;font-size:18px}}';document.head.appendChild(guideStyle);$('view-guide .grid').insertAdjacentHTML('afterbegin','<div class="card guide-intro"><strong>Start with the workflow</strong><p>DotSync is easiest to use as a deliberate loop: authenticate and link the folder, select the project and environment, pull before editing, review the diff, push a focused change, and use History or Restore when you need a safe recovery. The sections below explain both the “how” and the “why” behind every surface in this dashboard.</p><p>For automation, use a separate environment-scoped service token. For people, use the least powerful team role that fits. For investigations, combine the Audit filters instead of scanning the entire timeline.</p></div>');
const changeReport='<div class="card"><div class="card-title">What changed in this UI</div><p class="muted">This dashboard was rebuilt around a monochrome, keyboard-first workflow. Project changes clear stale secrets before loading the new context and report success or failure. The editor now mirrors matches inline while you type, preserves focus in the search field, and selects an exact occurrence only when requested. The sidebar is independently scrollable, long operations use short dismissible toasts and busy states, and command output includes the command, output, exit status, and a clear action. Team controls protect the owner while allowing admin, member, and viewer role changes. Service-token reveals are one-time cards with clipboard feedback. Audit data hides password-fetch internals and supports combined action, user, environment, and text filters. These changes preserve the existing CLI/API behavior while making each state visible and recoverable.</p></div>';$('view-guide .grid').insertAdjacentHTML('beforeend',changeReport);
const auditControls=document.createElement('div');auditControls.className='form audit-controls';auditControls.innerHTML='<input class="input" id="auditSearch" placeholder="Search audit events"><select class="input" id="auditAction"><option value="">All actions</option></select><select class="input" id="auditUser"><option value="">All users</option></select><select class="input" id="auditEnv"><option value="">All environments</option></select>';$('auditList').parentNode.insertBefore(auditControls,$('auditList'));const auditBase=renderAudit;renderAudit=()=>{const logs=(S.data?.logs||[]).filter(l=>{const a=String(l.action||'').toLowerCase().replace(/[.\- ]/g,'_');return !(a.includes('password')&&(a.includes('fetch')||a.includes('get')||a.includes('read')||a.includes('retriev')))});const fill=(id,label,values)=>{const select=$(id),current=select.value;select.innerHTML='<option value="">'+label+'</option>'+values.sort().map(v=>'<option value="'+esc(v)+'">'+esc(v)+'</option>').join('');select.value=values.includes(current)?current:''};fill('auditAction','All actions',[...new Set(logs.map(l=>String(l.action||'event')))]);fill('auditUser','All users',[...new Set(logs.map(l=>String(l.username||'unknown')))]);fill('auditEnv','All environments',[...new Set(logs.map(l=>String(l.env||'' )).filter(Boolean))]);const q=String($('auditSearch').value||'').toLowerCase(),action=$('auditAction').value,user=$('auditUser').value,env=$('auditEnv').value;const filtered=logs.filter(l=>(!q||JSON.stringify(l).toLowerCase().includes(q))&&(!action||String(l.action||'event')===action)&&(!user||String(l.username||'unknown')===user)&&(!env||String(l.env||'')===env));$('auditList').innerHTML=filtered.length?filtered.map(l=>'<div class="row"><div class="row-main"><div class="row-title">'+esc(l.action||'event')+'</div><div class="row-sub">@'+esc(l.username||'unknown')+(l.env?' · '+esc(l.env):'')+(l.created_at?' · '+time(l.created_at):'')+'</div></div><span class="pill">'+esc(l.ip||'local')+'</span></div>').join(''):'<div class="empty">No matching audit events.</div>'};['auditSearch','auditAction','auditUser','auditEnv'].forEach(id=>$(id).addEventListener(id==='auditSearch'?'input':'change',renderAudit));makeSelectMenu('auditAction');makeSelectMenu('auditUser');makeSelectMenu('auditEnv');renderAudit();
</script><style id="noir-theme">
:root{--ink:#f1f1ef;--muted:#969694;--faint:#5f5f5d;--bg:#080808;--panel:#101010;--panel2:#171717;--line:#292929;--accent:#d8d8d3;--green:#b8c5ba;--red:#d6a7a7;--mono:"IBM Plex Mono",ui-monospace,SFMono-Regular,Menlo,monospace;--sans:"Arial Narrow","Helvetica Neue",Arial,sans-serif}
html,body{background:var(--bg)!important;color:var(--ink)!important;font-family:var(--sans)!important;letter-spacing:.01em}
body{background-image:linear-gradient(rgba(255,255,255,.025) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.018) 1px,transparent 1px)!important;background-size:48px 48px!important}
.shell{grid-template-columns:220px 1fr!important;background:transparent!important}
.rail{background:rgba(8,8,8,.9)!important;border-right:1px solid var(--line)!important;padding:30px 18px!important;gap:34px!important}
.brand{color:var(--ink)!important;font-family:var(--mono)!important;letter-spacing:.04em!important;text-transform:uppercase!important;font-size:13px!important}.mark{background:var(--ink)!important;color:var(--bg)!important;border-radius:0!important;width:24px!important;height:24px!important;font-size:12px!important}
.caption,.eyebrow{color:var(--faint)!important;font-family:var(--mono)!important;letter-spacing:.16em!important;font-size:10px!important}
.nav{gap:3px!important}.nav button{color:var(--muted)!important;border-radius:0!important;padding:11px 10px!important;border-left:2px solid transparent!important;font-family:var(--mono)!important;font-size:12px!important}.nav button:hover{background:var(--panel2)!important;color:var(--ink)!important}.nav button.active{background:transparent!important;color:var(--ink)!important;border-left-color:var(--ink)!important;box-shadow:none!important}
.rail-foot{color:var(--faint)!important;border-top:1px solid var(--line)!important;font-family:var(--mono)!important}
.top{background:rgba(8,8,8,.88)!important;border-bottom:1px solid var(--line)!important;height:70px!important;padding:0 42px!important}.top select,.project-trigger{background:var(--panel)!important;border:1px solid var(--line)!important;border-radius:0!important;color:var(--ink)!important}.envs{gap:18px!important}.env{border:0!important;border-bottom:1px solid transparent!important;border-radius:0!important;color:var(--muted)!important;font-family:var(--mono)!important;padding:9px 0!important}.env.active{background:transparent!important;border-color:var(--ink)!important;color:var(--ink)!important}.top-right{color:var(--muted)!important;font-family:var(--mono)!important}.dot{background:var(--green)!important;box-shadow:none!important}
.content{max-width:1440px!important;margin:0 auto!important;padding:44px 52px!important}.view.active{animation:viewIn .28s ease both!important}.hero{border-bottom:1px solid var(--line)!important;padding-bottom:28px!important;margin-bottom:30px!important}.hero h1{font-size:clamp(2rem,4vw,4.4rem)!important;letter-spacing:-.045em!important;font-weight:500!important;color:var(--ink)!important;max-width:9ch!important;line-height:.95!important}.hero p{color:var(--muted)!important;max-width:44ch!important}.actions{gap:8px!important}
.stats{gap:0!important;margin-bottom:42px!important}.stats .card{background:var(--panel)!important;border:0!important;border-right:1px solid var(--line)!important;border-radius:0!important;padding:18px 20px!important;min-height:104px!important}.stats .card:last-child{border-right:0!important}.stat-label{color:var(--faint)!important;font-family:var(--mono)!important}.stat-value{color:var(--ink)!important;font-weight:500!important}.stat-note{color:var(--muted)!important}
.card{background:transparent!important;border:0!important;border-radius:0!important;box-shadow:none!important;overflow:visible!important}.card-head{border-bottom:1px solid var(--line)!important;padding:0 0 12px!important}.card-title{color:var(--ink)!important;font-family:var(--mono)!important;font-size:12px!important;text-transform:uppercase!important;letter-spacing:.08em!important}.notice{background:transparent!important;border-left:2px solid var(--muted)!important;border-radius:0!important;color:var(--muted)!important;padding:10px 14px!important;margin:18px 0!important}.editor{background:#0d0d0d!important;border:1px solid #242424!important;border-radius:0!important;color:var(--ink)!important;min-height:430px!important;padding:22px!important;line-height:1.8!important}.editor:focus{border-color:#555!important}
.btn{background:transparent!important;border:1px solid #3a3a3a!important;border-radius:0!important;color:var(--ink)!important;font-family:var(--mono)!important;font-size:11px!important;text-transform:uppercase!important;letter-spacing:.04em!important;padding:9px 13px!important}.btn:hover{background:var(--ink)!important;border-color:var(--ink)!important;color:var(--bg)!important}.btn.primary,.btn.good{background:var(--ink)!important;border-color:var(--ink)!important;color:var(--bg)!important}.btn.danger{color:var(--red)!important;border-color:transparent!important}.btn.danger:hover{background:var(--red)!important;color:var(--bg)!important}
.input,.select-picker,.select-trigger{background:var(--panel)!important;border:1px solid var(--line)!important;border-radius:0!important;color:var(--ink)!important}.input:focus{border-color:#666!important}.row{background:transparent!important;border:0!important;border-bottom:1px solid var(--line)!important;border-radius:0!important;padding:16px 0!important}.row:hover{background:var(--panel)!important;border-color:var(--line)!important;transform:none!important}.row-title{color:var(--ink)!important}.row-sub{color:var(--muted)!important}.pill{background:var(--panel2)!important;border:0!important;border-radius:0!important;color:var(--muted)!important;font-family:var(--mono)!important}.empty{border:0!important;border-top:1px solid var(--line)!important;border-bottom:1px solid var(--line)!important;border-radius:0!important;color:var(--muted)!important;padding:38px 20px!important}
.run-output{background:#0d0d0d!important;border:1px solid #242424!important;border-radius:0!important;color:var(--muted)!important}.toast{background:var(--ink)!important;border:0!important;border-radius:0!important;color:var(--bg)!important}.token-card{background:var(--panel)!important;border:1px solid var(--line)!important;border-radius:0!important}.token-value{background:#0d0d0d!important;border:0!important;color:var(--ink)!important;border-radius:0!important}.choice-backdrop{background:rgba(0,0,0,.82)!important}.choice{background:var(--panel)!important;border:1px solid var(--line)!important;border-radius:0!important;color:var(--ink)!important}.choice p{color:var(--muted)!important}.choice-actions .yes{background:var(--ink)!important;color:var(--bg)!important;border-color:var(--ink)!important}
#busy{background:rgba(8,8,8,.94)!important;color:var(--ink)!important}.boot-track{background:var(--line)!important}.boot-track i{background:var(--ink)!important}.boot-mark{background:var(--ink)!important;color:var(--bg)!important;border-radius:0!important}.boot-copy{color:var(--muted)!important}.boot-retry{border-radius:0!important;background:transparent!important;color:var(--ink)!important;border-color:var(--ink)!important}
#view-guide .guide-intro,#view-guide .guide-card{background:transparent!important;border:0!important;border-bottom:1px solid var(--line)!important;border-radius:0!important;padding:18px 0!important}.muted{color:var(--muted)!important}code{background:var(--panel2)!important;color:var(--ink)!important;border-radius:0!important}.rail-foot #serverText{font-size:0}.rail-foot #serverText:after{content:'Workspace ready';font-size:10px}.notice{font-size:0}.notice:after{content:'Encrypted values stay protected on this device.';font-size:12px}
@media(max-width:900px){.shell{grid-template-columns:1fr!important}.rail{border-right:0!important;border-bottom:1px solid var(--line)!important;padding:16px!important}.content{padding:30px 20px!important}.top{padding:0 20px!important}.hero h1{font-size:3rem!important}.stats{grid-template-columns:repeat(2,1fr)!important}.stats .card:nth-child(2){border-right:0!important}}
@media(max-width:560px){.top{height:auto;min-height:70px;flex-wrap:wrap;padding:14px 16px!important}.top-right{margin-left:0!important}.content{padding:24px 16px!important}.stats{grid-template-columns:1fr!important}.stats .card{border-right:0!important;border-bottom:1px solid var(--line)!important}.hero{margin-bottom:24px!important}}
</style></body></html>`
