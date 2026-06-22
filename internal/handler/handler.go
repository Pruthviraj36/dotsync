package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

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

// POST /api/auth/github/device — exchange a verified GitHub access token
// (obtained by the CLI via GitHub's own OAuth Device Flow) for DotSync tokens.
//
// Security: this endpoint never trusts the client's claimed identity.
// The GitHub access token is independently verified by calling GitHub's own
// /user API — a forged or expired token simply fails to resolve to an account.
// The server never sees or participates in the device code exchange; that
// happens entirely between the CLI and GitHub. This eliminates the CSRF
// risk inherent in the web redirect flow and drops the requirement for
// GITHUB_CLIENT_SECRET to exist on the server at all.
func (h *AuthHandler) GitHubDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GitHubAccessToken string `json:"github_access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GitHubAccessToken == "" {
		writeError(w, http.StatusBadRequest, "missing github_access_token")
		return
	}

	ghUser, err := auth.FetchGitHubUser(req.GitHubAccessToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "could not verify github token")
		return
	}

	user, err := auth.UpsertUser(r.Context(), h.db, ghUser)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user upsert failed")
		return
	}

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
