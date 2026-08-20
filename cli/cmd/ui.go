package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
)

func uiCmd() *cobra.Command {
	var portFlag string
	var noOpenFlag bool

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the DotSync web dashboard",
		Long: `Launches a local web server and opens the DotSync dashboard
in your browser. All operations run locally — secrets are encrypted
on your machine, same as the CLI.`,
		Example: `  dotsync ui
  dotsync ui --port 4040
  dotsync ui --no-open`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, _ := config.LoadProject()

			listener, err := net.Listen("tcp", ":"+portFlag)
			if err != nil {
				return fmt.Errorf("port %s is in use — try: dotsync ui --port 4041", portFlag)
			}

			client := api.New(cfg)
			mux := http.NewServeMux()

			// ── Static UI ─────────────────────────────────────────────────────
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write([]byte(dashboardHTML))
			})

			// ── /api/me ───────────────────────────────────────────────────────
			mux.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
				me, err := client.GetMe()
				if err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{
					"user":       me,
					"server_url": cfg.ServerURL,
					"project":    projCfg,
				})
			})

			// ── /api/projects ─────────────────────────────────────────────────
			mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
				projects, err := client.ListProjects()
				if err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{"projects": projects})
			})

			// ── /api/project/ ─────────────────────────────────────────────────
			mux.HandleFunc("/api/project/", func(w http.ResponseWriter, r *http.Request) {
				slug := r.URL.Query().Get("slug")
				if slug == "" && projCfg != nil {
					slug = projCfg.ProjectSlug
				}
				if slug == "" {
					jsonError(w, fmt.Errorf("no project slug"))
					return
				}
				envs, _ := client.ListEnvironments(slug)
				members, _ := client.ListTeamMembers(slug)
				tokens, _ := client.ListServiceTokens(slug)
				logs, _ := client.AuditLogs(slug)
				jsonOK(w, map[string]any{
					"slug":    slug,
					"envs":    envs,
					"members": members,
					"tokens":  tokens,
					"logs":    logs,
				})
			})

			// ── /api/history ──────────────────────────────────────────────────
			mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
				slug := r.URL.Query().Get("slug")
				env := r.URL.Query().Get("env")
				if slug == "" && projCfg != nil {
					slug = projCfg.ProjectSlug
				}
				if env == "" && projCfg != nil {
					env = projCfg.DefaultEnv
				}
				history, err := client.History(slug, env)
				if err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{"history": history})
			})

			// ── /api/pull ─────────────────────────────────────────────────────
			mux.HandleFunc("/api/pull", func(w http.ResponseWriter, r *http.Request) {
				slug := r.URL.Query().Get("slug")
				env := r.URL.Query().Get("env")
				result, err := client.Pull(slug, env)
				if err != nil {
					jsonError(w, err)
					return
				}
				password, err := resolvePassword(client, slug)
				if err != nil {
					jsonError(w, err)
					return
				}
				plaintext, err := cliCrypto.DecryptEnvFile(
					result.EncryptedData, result.Nonce, password, slug,
				)
				if err != nil {
					jsonError(w, err)
					return
				}
				keys := cliCrypto.ParseEnvFile(plaintext)
				jsonOK(w, map[string]any{
					"content": plaintext,
					"version": result.Version,
					"keys":    len(keys),
					"by":      result.PushedBy,
				})
			})

			// ── /api/push ─────────────────────────────────────────────────────
			mux.HandleFunc("/api/push", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var req struct {
					Slug    string `json:"slug"`
					Env     string `json:"env"`
					Content string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					jsonError(w, err)
					return
				}
				password, err := resolvePassword(client, req.Slug)
				if err != nil {
					jsonError(w, err)
					return
				}
				keys := cliCrypto.ParseEnvFile(req.Content)
				ciphertext, nonce, err := cliCrypto.EncryptEnvFile(req.Content, password, req.Slug)
				if err != nil {
					jsonError(w, err)
					return
				}
				result, err := client.Push(req.Slug, req.Env, api.PushRequest{
					EncryptedData: ciphertext,
					Nonce:         nonce,
				})
				if err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{
					"version": result.Version,
					"keys":    len(keys),
				})
			})

			// ── /api/team/add ─────────────────────────────────────────────────
			mux.HandleFunc("/api/team/add", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var req struct {
					Slug     string `json:"slug"`
					Username string `json:"username"`
					Role     string `json:"role"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if err := client.AddTeamMember(req.Slug, req.Username); err != nil {
					jsonError(w, err)
					return
				}
				if req.Role != "" && req.Role != "member" {
					client.UpdateTeamRole(req.Slug, req.Username, req.Role)
				}
				jsonOK(w, map[string]any{"ok": true})
			})

			// ── /api/team/remove ──────────────────────────────────────────────
			mux.HandleFunc("/api/team/remove", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var req struct {
					Slug     string `json:"slug"`
					Username string `json:"username"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if err := client.RemoveTeamMember(req.Slug, req.Username); err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{"ok": true})
			})

			// ── /api/tokens/create ────────────────────────────────────────────
			mux.HandleFunc("/api/tokens/create", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var req struct {
					Slug string `json:"slug"`
					Env  string `json:"env"`
					Name string `json:"name"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				token, err := client.CreateServiceToken(req.Slug, req.Env, req.Name)
				if err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{"token": token})
			})

			// ── /api/tokens/revoke ────────────────────────────────────────────
			mux.HandleFunc("/api/tokens/revoke", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var req struct {
					Slug    string `json:"slug"`
					TokenID string `json:"token_id"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if err := client.RevokeServiceToken(req.Slug, req.TokenID); err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{"ok": true})
			})

			// ── /api/rollback ─────────────────────────────────────────────────
			mux.HandleFunc("/api/rollback", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var req struct {
					Slug    string `json:"slug"`
					Env     string `json:"env"`
					Version int    `json:"version"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				old, err := client.PullVersion(req.Slug, req.Env, req.Version)
				if err != nil {
					jsonError(w, err)
					return
				}
				password, err := resolvePassword(client, req.Slug)
				if err != nil {
					jsonError(w, err)
					return
				}
				plaintext, err := cliCrypto.DecryptEnvFile(
					old.EncryptedData, old.Nonce, password, req.Slug,
				)
				if err != nil {
					jsonError(w, err)
					return
				}
				ciphertext, nonce, err := cliCrypto.EncryptEnvFile(plaintext, password, req.Slug)
				if err != nil {
					jsonError(w, err)
					return
				}
				result, err := client.Push(req.Slug, req.Env, api.PushRequest{
					EncryptedData: ciphertext,
					Nonce:         nonce,
				})
				if err != nil {
					jsonError(w, err)
					return
				}
				jsonOK(w, map[string]any{"version": result.Version})
			})

			// ── Launch ────────────────────────────────────────────────────────
			url := fmt.Sprintf("http://localhost:%s", portFlag)

			blank()
			fmt.Println(prog("ui", boldCyan(url)))
			fmt.Println(ok(dim("all operations run locally — secrets never leave your machine")))
			blank()
			hint("press ctrl+c to stop")
			blank()

			if !noOpenFlag {
				time.Sleep(200 * time.Millisecond)
				openBrowser(url)
			}

			return http.Serve(listener, mux)
		},
	}

	cmd.Flags().StringVar(&portFlag, "port", "4040", "local port to listen on")
	cmd.Flags().BoolVar(&noOpenFlag, "no-open", false, "don't open browser automatically")
	return cmd
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(500)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
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
	c.Start()
}
