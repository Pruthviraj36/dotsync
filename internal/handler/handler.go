package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/model"
	"github.com/Pruthviraj36/dotsync/internal/service"
	"github.com/go-chi/chi/v5"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func realIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return strings.Split(ip, ",")[0]
	}
	return r.RemoteAddr
}

// ============================================================
// Auth Handlers
// ============================================================

type AuthHandler struct {
	authSvc *auth.Service
	db      *db.DB
}

func NewAuthHandler(authSvc *auth.Service, database *db.DB) *AuthHandler {
	return &AuthHandler{authSvc: authSvc, db: database}
}

// POST /api/auth/github — exchange GitHub OAuth code for DotSync tokens
func (h *AuthHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}

	// Exchange code for GitHub access token
	ghToken, err := exchangeGitHubCode(req.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, "github oauth failed: "+err.Error())
		return
	}

	// Fetch GitHub user
	ghUser, err := auth.FetchGitHubUser(ghToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "github user fetch failed")
		return
	}

	// Upsert in DB
	user, err := auth.UpsertUser(r.Context(), h.db, ghUser)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user upsert failed")
		return
	}

	// Issue tokens
	accessToken, err := h.authSvc.IssueAccessToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}

	refreshToken, err := h.authSvc.IssueRefreshToken(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "refresh token failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"user":          user,
	})
}

// GET /api/auth/config — public config the CLI needs before starting OAuth.
// The GitHub client ID is not a secret (GitHub's own docs treat it as public,
// since it's embedded in the browser-visible authorize URL anyway) — only
// the client SECRET must stay server-side, and this endpoint never returns it.
func (h *AuthHandler) Config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"github_client_id": os.Getenv("GITHUB_CLIENT_ID"),
	})
}

// GET /api/auth/github/callback — display the code to the user during CLI login
func (h *AuthHandler) GitHubCallbackPage(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	errorDesc := r.URL.Query().Get("error_description")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if errorDesc != "" {
		w.Write([]byte(`
			<!DOCTYPE html>
			<html lang="en">
			<head>
				<meta charset="UTF-8">
				<meta name="viewport" content="width=device-width, initial-scale=1.0">
				<title>DotSync - Authorization Failed</title>
				<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
				<style>
					body { 
						font-family: 'Inter', system-ui, -apple-system, sans-serif; 
						display: flex; justify-content: center; align-items: center; 
						height: 100vh; margin: 0;
						background: linear-gradient(135deg, #0f172a 0%, #1e1b4b 100%);
						color: #e2e8f0;
					}
					.box { 
						background: rgba(30, 41, 59, 0.7); 
						backdrop-filter: blur(16px);
						-webkit-backdrop-filter: blur(16px);
						padding: 3rem; 
						border-radius: 24px; 
						border: 1px solid rgba(244, 63, 94, 0.3); 
						text-align: center; 
						box-shadow: 0 25px 50px -12px rgba(0,0,0,0.5); 
						max-width: 450px; 
						width: 100%;
						animation: fadeIn 0.5s cubic-bezier(0.4, 0, 0.2, 1);
					}
					@keyframes fadeIn {
						from { opacity: 0; transform: scale(0.95) translateY(10px); }
						to { opacity: 1; transform: scale(1) translateY(0); }
					}
					.icon { 
						width: 80px; height: 80px; 
						background: linear-gradient(135deg, #f43f5e 0%, #e11d48 100%);
						color: white; border-radius: 50%; display: flex; 
						align-items: center; justify-content: center; font-size: 2.5rem; 
						margin: 0 auto 1.5rem auto;
						box-shadow: 0 10px 25px -5px rgba(225, 29, 72, 0.5);
					}
					h2 { margin-top: 0; color: #fff; font-size: 2rem; font-weight: 700; margin-bottom: 0.5rem; }
					p { color: #94a3b8; line-height: 1.6; font-size: 1.1rem; }
				</style>
			</head>
			<body>
				<div class="box">
					<div class="icon">✕</div>
					<h2>Auth Failed</h2>
					<p>` + errorDesc + `</p>
					<p style="font-size: 0.9em; margin-top: 2rem; color: #64748b;">You can close this window and try again from the CLI.</p>
				</div>
			</body>
			</html>
		`))
		return
	}

	if code == "" {
		writeError(w, http.StatusBadRequest, "missing code in query")
		return
	}

	w.Write([]byte(`
		<!DOCTYPE html>
		<html lang="en">
		<head>
			<meta charset="UTF-8">
			<meta name="viewport" content="width=device-width, initial-scale=1.0">
			<title>DotSync - Authorization Successful</title>
			<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
			<style>
				body { 
					font-family: 'Inter', system-ui, -apple-system, sans-serif; 
					display: flex; justify-content: center; align-items: center; 
					height: 100vh; margin: 0;
					background: linear-gradient(135deg, #0f172a 0%, #064e3b 100%);
					color: #e2e8f0;
				}
				.box { 
					background: rgba(30, 41, 59, 0.6); 
					backdrop-filter: blur(20px);
					-webkit-backdrop-filter: blur(20px);
					padding: 3rem; border-radius: 24px; 
					border: 1px solid rgba(16, 185, 129, 0.2); 
					text-align: center; 
					box-shadow: 0 25px 50px -12px rgba(0,0,0,0.5), inset 0 1px 0 rgba(255,255,255,0.1); 
					max-width: 450px; width: 100%;
					animation: slideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1);
				}
				@keyframes slideUp {
					from { opacity: 0; transform: translateY(40px) scale(0.95); }
					to { opacity: 1; transform: translateY(0) scale(1); }
				}
				.icon { 
					width: 80px; height: 80px; 
					background: linear-gradient(135deg, #34d399 0%, #10b981 100%);
					color: white; border-radius: 50%; display: flex; 
					align-items: center; justify-content: center; font-size: 2.5rem; 
					margin: 0 auto 1.5rem auto;
					box-shadow: 0 10px 25px -5px rgba(16, 185, 129, 0.5);
					animation: pulse 2s infinite;
				}
				@keyframes pulse {
					0% { box-shadow: 0 0 0 0 rgba(16, 185, 129, 0.7); }
					70% { box-shadow: 0 0 0 20px rgba(16, 185, 129, 0); }
					100% { box-shadow: 0 0 0 0 rgba(16, 185, 129, 0); }
				}
				h2 { margin-top: 0; color: #fff; font-size: 2rem; font-weight: 700; margin-bottom: 0.75rem; letter-spacing: -0.025em; }
				p { color: #94a3b8; line-height: 1.6; margin-bottom: 2.5rem; font-size: 1.05rem; }
				
				.code-container {
					display: flex; align-items: center; justify-content: space-between;
					background: rgba(15, 23, 42, 0.8); 
					border: 1px solid rgba(255,255,255,0.1); border-radius: 12px;
					padding: 0.75rem; margin-bottom: 2rem; 
					transition: all 0.3s ease;
					box-shadow: inset 0 2px 4px rgba(0,0,0,0.3);
				}
				.code-container:hover { border-color: rgba(52, 211, 153, 0.5); box-shadow: 0 0 15px rgba(16, 185, 129, 0.2), inset 0 2px 4px rgba(0,0,0,0.3); }
				
				.code { 
					font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", Menlo, monospace; 
					font-size: 1.4rem; color: #34d399; padding-left: 1rem;
					letter-spacing: 2px; user-select: all; overflow-x: auto; font-weight: 600;
				}
				
				.copy-btn {
					background: linear-gradient(135deg, #10b981 0%, #059669 100%); 
					color: white; border: none;
					padding: 0.85rem 1.5rem; border-radius: 8px; font-size: 1rem; font-weight: 600;
					cursor: pointer; transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1); 
					display: flex; align-items: center; gap: 0.5rem;
					box-shadow: 0 4px 6px -1px rgba(16, 185, 129, 0.3);
				}
				.copy-btn:hover { transform: translateY(-2px); box-shadow: 0 6px 10px -1px rgba(16, 185, 129, 0.4); }
				.copy-btn:active { transform: translateY(0); }
				.copy-btn.copied { background: linear-gradient(135deg, #059669 0%, #047857 100%); }
				
				.footer { color: #64748b; font-size: 0.9rem; font-weight: 500; }
			</style>
		</head>
		<body>
			<div class="box">
				<div class="icon">✓</div>
				<h2>Authorization Successful!</h2>
				<p>Your GitHub account has been linked successfully. Copy the authorization code below and paste it back into your DotSync CLI.</p>
				
				<div class="code-container">
					<div class="code" id="authCode">` + code + `</div>
					<button class="copy-btn" id="copyBtn" onclick="copyCode()">
						<svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M0 6.75C0 5.784.784 5 1.75 5h1.5a.75.75 0 010 1.5h-1.5a.25.25 0 00-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 00.25-.25v-1.5a.75.75 0 011.5 0v1.5A1.75 1.75 0 019.25 16h-7.5A1.75 1.75 0 010 14.25v-7.5z"></path><path fill-rule="evenodd" d="M5 1.75C5 .784 5.784 0 6.75 0h7.5C15.216 0 16 .784 16 1.75v7.5A1.75 1.75 0 0114.25 11h-7.5A1.75 1.75 0 015 9.25v-7.5zm1.75-.25a.25.25 0 00-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 00.25-.25v-7.5a.25.25 0 00-.25-.25h-7.5z"></path></svg>
						Copy
					</button>
				</div>
				
				<div class="footer">You can safely close this window after copying the code.</div>
			</div>

			<script>
				function copyCode() {
					const code = document.getElementById('authCode').innerText;
					const btn = document.getElementById('copyBtn');
					
					navigator.clipboard.writeText(code).then(() => {
						const originalContent = btn.innerHTML;
						btn.innerHTML = '<svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M13.78 4.22a.75.75 0 010 1.06l-7.25 7.25a.75.75 0 01-1.06 0L2.22 9.28a.75.75 0 011.06-1.06L6 10.94l6.72-6.72a.75.75 0 011.06 0z"></path></svg> Copied!';
						btn.classList.add('copied');
						
						setTimeout(() => {
							btn.innerHTML = originalContent;
							btn.classList.remove('copied');
						}, 2000);
					}).catch(err => {
						console.error('Failed to copy: ', err);
					});
				}
			</script>
		</body>
		</html>
	`))
}

// POST /api/auth/refresh — rotate refresh token
func (h *AuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "missing refresh_token")
		return
	}

	user, accessToken, newRefresh, err := h.authSvc.RotateRefreshToken(r.Context(), req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": newRefresh,
		"user":          user,
	})
}

// POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	if err := h.authSvc.RevokeAllSessions(r.Context(), claims.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "logout failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out from all devices"})
}

// GET /api/auth/me
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	var user model.User
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, github_id, username, email, avatar_url, plan, created_at, updated_at FROM users WHERE id = $1`,
		claims.UserID,
	).Scan(&user.ID, &user.GitHubID, &user.Username, &user.Email, &user.AvatarURL, &user.Plan, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// ============================================================
// Project Handlers
// ============================================================

type ProjectHandler struct {
	projectSvc *service.ProjectService
	teamSvc    *service.TeamService
}

func NewProjectHandler(ps *service.ProjectService, ts *service.TeamService) *ProjectHandler {
	return &ProjectHandler{projectSvc: ps, teamSvc: ts}
}

// POST /api/projects
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	limits := model.Plans[claims.Plan]

	// Enforce plan project limit
	if limits.MaxProjects != -1 {
		count, _ := h.projectSvc.CountForUser(r.Context(), claims.UserID)
		if count >= limits.MaxProjects {
			writeError(w, http.StatusForbidden, "project limit reached for your plan")
			return
		}
	}

	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name == "" || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug required")
		return
	}

	proj, err := h.projectSvc.Create(r.Context(), claims.UserID, req.Name, req.Slug, req.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create project failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, proj)
}

// GET /api/projects
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	projects, err := h.projectSvc.ListForUser(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list projects failed")
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

// ============================================================
// Secrets Handlers
// ============================================================

type SecretsHandler struct {
	secretSvc  *service.SecretService
	projectSvc *service.ProjectService
	teamSvc    *service.TeamService
	auditSvc   *service.AuditService
}

func NewSecretsHandler(
	ss *service.SecretService,
	ps *service.ProjectService,
	ts *service.TeamService,
	as *service.AuditService,
) *SecretsHandler {
	return &SecretsHandler{secretSvc: ss, projectSvc: ps, teamSvc: ts, auditSvc: as}
}

// POST /api/projects/{slug}/envs/{env}/push
func (h *SecretsHandler) Push(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Only owner/admin/member can push
	role, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || role == "viewer" {
		writeError(w, http.StatusForbidden, "insufficient permissions to push")
		return
	}

	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	var req struct {
		EncryptedData []byte `json:"encrypted_data"` // base64 decoded by json.Unmarshal
		Nonce         []byte `json:"nonce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.EncryptedData) == 0 || len(req.Nonce) == 0 {
		writeError(w, http.StatusBadRequest, "encrypted_data and nonce required")
		return
	}

	secret, err := h.secretSvc.PushSecrets(r.Context(), env.ID, claims.UserID, req.EncryptedData, req.Nonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "push failed")
		return
	}

	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, env.ID, "push", realIP(r),
		map[string]any{"version": secret.Version, "env": envName})

	writeJSON(w, http.StatusOK, map[string]any{
		"version":    secret.Version,
		"created_at": secret.CreatedAt,
	})
}

// GET /api/projects/{slug}/envs/{env}/pull
func (h *SecretsHandler) Pull(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	isMember, _ := h.teamSvc.IsProjectMember(r.Context(), proj.ID, claims.UserID)
	if !isMember {
		writeError(w, http.StatusForbidden, "not a project member")
		return
	}

	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	secret, err := h.secretSvc.PullLatest(r.Context(), env.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, env.ID, "pull", realIP(r),
		map[string]any{"version": secret.Version, "env": envName})

	writeJSON(w, http.StatusOK, map[string]any{
		"encrypted_data": secret.EncryptedData,
		"nonce":          secret.DataNonce,
		"version":        secret.Version,
		"pushed_by":      secret.PushedBy,
		"created_at":     secret.CreatedAt,
	})
}

// GET /api/projects/{slug}/envs/{env}/history
func (h *SecretsHandler) History(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")
	limits := model.Plans[claims.Plan]

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	history, err := h.secretSvc.GetHistory(r.Context(), env.ID, limits.HistoryDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history fetch failed")
		return
	}

	writeJSON(w, http.StatusOK, history)
}

// exchangeGitHubCode calls GitHub token endpoint. Reads client credentials from env.
func exchangeGitHubCode(code string) (string, error) {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")

	req, _ := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(
		"client_id="+clientID+"&client_secret="+clientSecret+"&code="+code,
	))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != "" {
		return "", &exchangeError{result.Error}
	}
	return result.AccessToken, nil
}

type exchangeError struct{ msg string }

func (e *exchangeError) Error() string { return e.msg }
