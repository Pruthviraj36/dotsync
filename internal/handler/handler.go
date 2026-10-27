package handler

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/db"
	mw "github.com/Pruthviraj36/dotsync/internal/middleware"
	"github.com/Pruthviraj36/dotsync/internal/model"
	"github.com/Pruthviraj36/dotsync/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
)

const (
	maxProjectNameLen = 100
	maxDescriptionLen = 500
	maxCiphertextLen  = 3 << 20 // 3 MB of ciphertext per version

	// Password policy is enforced server-side as well as in the CLI.
	minProjectPasswordLen = 12
	maxProjectPasswordLen = 4096
)

// slugPattern is enforced on the server, not just the CLI: the slug is used in
// URL paths and audit logs, so it must stay in a strict, predictable alphabet.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,37}[a-z0-9]$`)

// reservedSlugs would collide with API routes or look like system resources.
var reservedSlugs = map[string]bool{
	"api": true, "auth": true, "admin": true, "health": true,
	"install": true, "install.sh": true, "install.ps1": true,
	"www": true, "assets": true, "static": true, "docs": true,
}

func validSlug(slug string) bool {
	if !slugPattern.MatchString(slug) || strings.Contains(slug, "--") {
		return false
	}
	return !reservedSlugs[slug]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// realIP returns the client address without its ephemeral port. RemoteAddr is
// the socket address (spoofable headers are handled by mw.RealIP, which only
// trusts them when a reverse proxy is explicitly configured).
func realIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// queryInt reads a bounded positive integer query parameter.
func queryInt(r *http.Request, name string, def, min, max int) int {
	return parseQueryInt(r.URL.Query().Get(name), def, min, max)
}

func parseQueryInt(raw string, def, min, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return def
	}
	return n
}

func serviceTokenProjectAllowed(r *http.Request, projectID string) bool {
	tokenProjectID := mw.ServiceTokenProjectID(r.Context())
	return tokenProjectID == "" || tokenProjectID == projectID
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
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token — run: dotsync login")
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
	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))
	req.Description = strings.TrimSpace(req.Description)
	if req.Name == "" || len(req.Name) > maxProjectNameLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("name must be 1-%d characters", maxProjectNameLen))
		return
	}
	if !validSlug(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must be 2-39 characters: lowercase letters, digits, and single hyphens, starting with a letter or digit")
		return
	}
	if len(req.Description) > maxDescriptionLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be at most %d characters", maxDescriptionLen))
		return
	}

	proj, err := h.projectSvc.Create(r.Context(), claims.UserID, req.Name, req.Slug, req.Description)
	if err != nil {
		// Never echo raw SQL/driver errors back to the client — they leak schema
		// details. A unique-violation is the one case worth surfacing clearly.
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			writeError(w, http.StatusConflict, "that slug is already taken")
			return
		}
		log.Printf("ERROR create project [user=%s slug=%s]: %v", claims.Username, req.Slug, err)
		writeError(w, http.StatusInternalServerError, "could not create project")
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
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens are read-only")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")

	proj, role, err := h.projectSvc.GetBySlugWithRole(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Only owner/admin/member can push
	if role == "viewer" {
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
		Signature     []byte `json:"signature,omitempty"`
		SignerPubKey  string `json:"signer_pubkey,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.EncryptedData) == 0 || len(req.Nonce) == 0 {
		writeError(w, http.StatusBadRequest, "encrypted_data and nonce required")
		return
	}
	if len(req.EncryptedData) > maxCiphertextLen {
		writeError(w, http.StatusRequestEntityTooLarge, "encrypted payload exceeds the 3 MB limit")
		return
	}
	if len(req.Nonce) != 12 {
		writeError(w, http.StatusBadRequest, "nonce must be exactly 12 bytes (AES-GCM)")
		return
	}
	if len(req.Signature) > 0 {
		if req.SignerPubKey == "" {
			writeError(w, http.StatusBadRequest, "signer_pubkey required when signature is provided")
			return
		}
		if len(req.Signature) != ed25519.SignatureSize {
			writeError(w, http.StatusBadRequest, "signature must be exactly 64 bytes")
			return
		}
		pubKey, decodeErr := hex.DecodeString(req.SignerPubKey)
		if decodeErr != nil || len(pubKey) != ed25519.PublicKeySize {
			writeError(w, http.StatusBadRequest, "signer_pubkey must be a valid Ed25519 public key")
			return
		}
		digest := sha256.Sum256(req.EncryptedData)
		if !ed25519.Verify(ed25519.PublicKey(pubKey), digest[:], req.Signature) {
			writeError(w, http.StatusBadRequest, "signature does not match encrypted_data")
			return
		}
		req.SignerPubKey = strings.ToLower(req.SignerPubKey)
	}

	secret, err := h.secretSvc.PushSecrets(r.Context(), env.ID, claims.UserID, req.EncryptedData, req.Nonce, req.Signature, req.SignerPubKey)
	if err != nil {
		log.Printf("ERROR push [project=%s env=%s user=%s]: %v", slug, envName, claims.Username, err)
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

	if mw.IsServiceTokenRequest(r.Context()) {
		scope := mw.ServiceTokenEnv(r.Context())
		if scope != "*" && scope != envName {
			writeError(w, http.StatusForbidden, fmt.Sprintf("service token is scoped to %q, cannot pull %q", scope, envName))
			return
		}
	}

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !serviceTokenProjectAllowed(r, proj.ID) {
		writeError(w, http.StatusForbidden, "service token is not scoped to this project")
		return
	}

	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	secret, err := h.secretSvc.PullLatest(r.Context(), env.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no secrets found for this environment")
		return
	}

	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, env.ID, "pull", realIP(r),
		map[string]any{"version": secret.Version, "env": envName})

	writeJSON(w, http.StatusOK, map[string]any{
		"encrypted_data":   secret.EncryptedData,
		"nonce":            secret.DataNonce,
		"signature":        secret.Signature,
		"version":          secret.Version,
		"pushed_by":        secret.PushedByUsername,
		"pushed_by_pubkey": secret.PushedByPubKey,
		"created_at":       secret.CreatedAt,
	})
}

// GET /api/projects/{slug}/envs/{env}/history
func (h *SecretsHandler) History(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")
	limits := model.Plans[claims.Plan]

	if mw.IsServiceTokenRequest(r.Context()) {
		scope := mw.ServiceTokenEnv(r.Context())
		if scope != "*" && scope != envName {
			writeError(w, http.StatusForbidden, fmt.Sprintf("service token is scoped to %q, cannot view history for %q", scope, envName))
			return
		}
	}

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !serviceTokenProjectAllowed(r, proj.ID) {
		writeError(w, http.StatusForbidden, "service token is not scoped to this project")
		return
	}

	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	limit := queryInt(r, "limit", 100, 1, 100)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)
	history, err := h.secretSvc.GetHistory(r.Context(), env.ID, limits.HistoryDays, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history fetch failed")
		return
	}

	writeJSON(w, http.StatusOK, history)
}

// GET /api/projects/{slug}/envs/{env}/head — cheap "what is the latest version?"
// endpoint. Pull clients use this instead of downloading the full history just
// to read history[0], which turns an O(versions) query into an O(1) lookup.
func (h *SecretsHandler) Head(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")

	if mw.IsServiceTokenRequest(r.Context()) {
		scope := mw.ServiceTokenEnv(r.Context())
		if scope != "*" && scope != envName {
			writeError(w, http.StatusForbidden, fmt.Sprintf("service token is scoped to %q, cannot access %q", scope, envName))
			return
		}
	}

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !serviceTokenProjectAllowed(r, proj.ID) {
		writeError(w, http.StatusForbidden, "service token is not scoped to this project")
		return
	}
	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	head, err := h.secretSvc.GetHead(r.Context(), env.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "head fetch failed")
		return
	}
	if head == nil {
		writeJSON(w, http.StatusOK, map[string]any{"version": 0, "pushed_by": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    head.Version,
		"pushed_by":  head.PushedBy,
		"created_at": head.CreatedAt,
	})
}

// ============================================================
// Team Handlers
// ============================================================

type TeamHandler struct {
	projectSvc *service.ProjectService
	teamSvc    *service.TeamService
	db         *db.DB
	auditSvc   *service.AuditService
}

func NewTeamHandler(ps *service.ProjectService, ts *service.TeamService, database *db.DB, auditSvc *service.AuditService) *TeamHandler {
	return &TeamHandler{projectSvc: ps, teamSvc: ts, db: database, auditSvc: auditSvc}
}

// POST /api/projects/{slug}/team
func (h *TeamHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot manage team members")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Owners and admins can invite. Admins may only grant roles below their own,
	// matching the rules for removing members and changing roles.
	role, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (role != "owner" && role != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can invite members")
		return
	}

	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeError(w, http.StatusBadRequest, "username required")
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}
	if req.Role != "admin" && req.Role != "member" && req.Role != "viewer" {
		writeError(w, http.StatusBadRequest, "role must be admin, member, or viewer")
		return
	}
	if role == "admin" && req.Role == "admin" {
		writeError(w, http.StatusForbidden, "admins can only invite members and viewers")
		return
	}

	// Find user by username
	var targetUser model.User
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id, username FROM users WHERE username = $1`, req.Username,
	).Scan(&targetUser.ID, &targetUser.Username)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found in dotsync (they must log in at least once)")
		return
	}

	// Enforce plan member limit on the project owner
	var ownerPlan string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT plan FROM users WHERE id = $1`, proj.OwnerID,
	).Scan(&ownerPlan)
	limits := model.Plans[ownerPlan]
	if limits.MaxMembers != -1 {
		count, _ := h.teamSvc.CountMembers(r.Context(), proj.ID)
		if count >= limits.MaxMembers {
			writeError(w, http.StatusForbidden,
				fmt.Sprintf("member limit (%d) reached for the %s plan", limits.MaxMembers, ownerPlan))
			return
		}
	}

	err = h.teamSvc.InviteMember(r.Context(), proj.ID, targetUser.ID, req.Role, claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "team_add", realIP(r), map[string]any{"username": targetUser.Username, "role": req.Role})

	writeJSON(w, http.StatusOK, map[string]string{
		"message":  "user added successfully",
		"username": targetUser.Username,
	})
}

// GET /api/projects/{slug}/team — list all team members with roles
func (h *TeamHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	members, err := h.teamSvc.ListMembers(r.Context(), proj.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	writeJSON(w, http.StatusOK, members)
}

// DELETE /api/projects/{slug}/team/{username} — remove a member
func (h *TeamHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot manage team members")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	username := chi.URLParam(r, "username")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	callerRole, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can remove members")
		return
	}

	var targetUser model.User
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id FROM users WHERE username = $1`, username,
	).Scan(&targetUser.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	// Admins cannot remove other admins or the owner — only the owner can
	if callerRole == "admin" {
		targetRole, _ := h.teamSvc.GetRole(r.Context(), proj.ID, targetUser.ID)
		if targetRole == "owner" || targetRole == "admin" {
			writeError(w, http.StatusForbidden, "admins can only remove members and viewers")
			return
		}
	}

	if err := h.teamSvc.RemoveMember(r.Context(), proj.ID, targetUser.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "team_remove", realIP(r), map[string]any{"username": username})

	writeJSON(w, http.StatusOK, map[string]string{
		"message":  "member removed",
		"username": username,
	})
}

// PATCH /api/projects/{slug}/team/{username} — update a member's role
func (h *TeamHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot manage team members")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	username := chi.URLParam(r, "username")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	callerRole, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can change roles")
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Role == "" {
		writeError(w, http.StatusBadRequest, "role required")
		return
	}

	var targetUser model.User
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id FROM users WHERE username = $1`, username,
	).Scan(&targetUser.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	// Admins manage members and viewers only — never the owner or other admins,
	// and they cannot promote anyone into their own tier.
	if callerRole == "admin" {
		targetRole, targetErr := h.teamSvc.GetRole(r.Context(), proj.ID, targetUser.ID)
		if targetErr != nil {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		if targetRole == "owner" || targetRole == "admin" {
			writeError(w, http.StatusForbidden, "admins cannot change the role of the owner or another admin")
			return
		}
		if req.Role == "admin" {
			writeError(w, http.StatusForbidden, "admins cannot promote someone to admin")
			return
		}
	}

	if err := h.teamSvc.UpdateRole(r.Context(), proj.ID, targetUser.ID, req.Role); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "team_role", realIP(r), map[string]any{"username": username, "role": req.Role})

	writeJSON(w, http.StatusOK, map[string]string{
		"message":  "role updated",
		"username": username,
		"role":     req.Role,
	})
}

// ============================================================
// Password Handlers
// ============================================================
//
// Stores/retrieves the E2EE project password server-side (encrypted with
// SERVER_MASTER_KEY — see internal/service.PasswordService). This trades
// pure end-to-end encryption for convenience: any authorized team member
// can fetch the password on a new machine instead of it being re-typed by
// hand every time. Every set/get is audit logged.

type PasswordHandler struct {
	passwordSvc *service.PasswordService
	projectSvc  *service.ProjectService
	teamSvc     *service.TeamService
	auditSvc    *service.AuditService
}

func NewPasswordHandler(
	pwSvc *service.PasswordService,
	ps *service.ProjectService,
	ts *service.TeamService,
	as *service.AuditService,
) *PasswordHandler {
	return &PasswordHandler{passwordSvc: pwSvc, projectSvc: ps, teamSvc: ts, auditSvc: as}
}

// PUT /api/projects/{slug}/password — set or rotate the project password
func (h *PasswordHandler) Set(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot modify passwords")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Only owner/admin can set or rotate the shared password.
	role, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (role != "owner" && role != "admin") {
		writeError(w, http.StatusForbidden, "only an owner or admin can set the project password")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeError(w, http.StatusBadRequest, "password required")
		return
	}
	// The CLI enforces this too, but the API is a public surface: a weak shared
	// password would undermine every environment in the project.
	if len(req.Password) < minProjectPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minProjectPasswordLen))
		return
	}
	if len(req.Password) > maxProjectPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at most %d characters", maxProjectPasswordLen))
		return
	}

	if err := h.passwordSvc.SetPassword(r.Context(), proj.ID, claims.UserID, req.Password); err != nil {
		log.Printf("ERROR set password [project=%s user=%s]: %v", slug, claims.Username, err)
		writeError(w, http.StatusInternalServerError, "failed to store password")
		return
	}

	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "password_rotate", realIP(r), map[string]any{})

	writeJSON(w, http.StatusOK, map[string]string{"message": "password saved"})
}

// GET /api/projects/{slug}/password — fetch the project password
func (h *PasswordHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !serviceTokenProjectAllowed(r, proj.ID) {
		writeError(w, http.StatusForbidden, "service token is not scoped to this project")
		return
	}

	password, err := h.passwordSvc.GetPassword(r.Context(), proj.ID)
	if err != nil {
		if service.IsNoPasswordError(err) {
			writeError(w, http.StatusNotFound, "no password set for this project yet — run: dotsync password rotate")
			return
		}
		log.Printf("ERROR fetch password [project=%s user=%s]: %v", slug, claims.Username, err)
		writeError(w, http.StatusInternalServerError, "could not fetch project password")
		return
	}

	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "password_fetch", realIP(r), map[string]any{})

	writeJSON(w, http.StatusOK, map[string]string{"password": password})
}

// GET /api/projects/{slug}/envs/{env}/pull?version=N — pull a specific version
func (h *SecretsHandler) PullVersion(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	envName := chi.URLParam(r, "env")

	if mw.IsServiceTokenRequest(r.Context()) {
		scope := mw.ServiceTokenEnv(r.Context())
		if scope != "*" && scope != envName {
			writeError(w, http.StatusForbidden, fmt.Sprintf("service token is scoped to %q, cannot pull %q", scope, envName))
			return
		}
	}

	versionStr := r.URL.Query().Get("version")
	if versionStr == "" {
		writeError(w, http.StatusBadRequest, "version query param required")
		return
	}
	version, parseErr := strconv.Atoi(versionStr)
	if parseErr != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !serviceTokenProjectAllowed(r, proj.ID) {
		writeError(w, http.StatusForbidden, "service token is not scoped to this project")
		return
	}

	env, err := h.projectSvc.GetEnvironment(r.Context(), proj.ID, envName)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	secret, err := h.secretSvc.PullVersion(r.Context(), env.ID, version)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("version %d not found", version))
		return
	}

	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, env.ID, "pull", realIP(r),
		map[string]any{"version": secret.Version, "env": envName, "specific_version": true})

	writeJSON(w, http.StatusOK, map[string]any{
		"encrypted_data":   secret.EncryptedData,
		"nonce":            secret.DataNonce,
		"signature":        secret.Signature,
		"version":          secret.Version,
		"pushed_by":        secret.PushedByUsername,
		"pushed_by_pubkey": secret.PushedByPubKey,
		"created_at":       secret.CreatedAt,
	})
}

// GET /api/projects/{slug}/audit — read audit logs (available to all users)
func (h *SecretsHandler) AuditLogs(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	callerRole, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can view audit logs")
		return
	}

	logs, err := h.auditSvc.GetLogs(r.Context(), proj.ID,
		queryInt(r, "limit", 50, 1, 200), queryInt(r, "offset", 0, 0, 1_000_000))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch audit logs")
		return
	}

	writeJSON(w, http.StatusOK, logs)
}

// ============================================================
// Identity Handler
// ============================================================
//
// Stores each user's ed25519 public key so teammates can verify who
// actually pushed a given version (see cli/identity and cli/cmd/manifest.go
// for the client-side signing/verification). Only the public key is ever
// stored here — private keys never leave the machine that generated them.

type IdentityHandler struct {
	db *db.DB
}

func NewIdentityHandler(database *db.DB) *IdentityHandler {
	return &IdentityHandler{db: database}
}

// PUT /api/me/pubkey — upload/update this account's ed25519 public key
func (h *IdentityHandler) SetPubKey(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot change identity keys")
		return
	}
	claims := auth.ClaimsFromCtx(r.Context())

	var req struct {
		PubKey string `json:"pubkey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	decoded, err := hex.DecodeString(req.PubKey)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("pubkey must be %d bytes hex-encoded", ed25519.PublicKeySize))
		return
	}
	req.PubKey = strings.ToLower(req.PubKey)

	_, err = h.db.ExecContext(r.Context(),
		`UPDATE users SET ed25519_pubkey = $1, updated_at = NOW() WHERE id = $2`,
		req.PubKey, claims.UserID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store public key")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "public key saved"})
}

// GET /api/projects/{slug}/envs — list environments for a project
func (h *ProjectHandler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	rows, err := h.projectSvc.GetEnvironments(r.Context(), proj.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list environments")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"environments": rows})
}

// ============================================================
// Service Token Handlers
// ============================================================

type ServiceTokenHandler struct {
	tokenSvc   *service.ServiceTokenService
	projectSvc *service.ProjectService
	teamSvc    *service.TeamService
	auditSvc   *service.AuditService
}

func NewServiceTokenHandler(ts *service.ServiceTokenService, ps *service.ProjectService, tms *service.TeamService, auditSvc *service.AuditService) *ServiceTokenHandler {
	return &ServiceTokenHandler{tokenSvc: ts, projectSvc: ps, teamSvc: tms, auditSvc: auditSvc}
}

// POST /api/projects/{slug}/tokens — create a service token (owners and admins only)
func (h *ServiceTokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot manage service tokens")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	callerRole, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can create service tokens")
		return
	}

	var req struct {
		Env  string `json:"env"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Env == "" {
		req.Env = "*"
	}
	if req.Env != "*" && req.Env != "dev" && req.Env != "staging" && req.Env != "production" {
		writeError(w, http.StatusBadRequest, "env must be dev, staging, production, or * for all")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "ci-" + req.Env
	}
	if len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "token name must be at most 100 characters")
		return
	}

	rawToken, record, err := h.tokenSvc.Create(r.Context(), proj.ID, req.Env, req.Name, claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create service token")
		return
	}
	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "token_create", realIP(r), map[string]any{"token_id": record.ID, "env": record.Env, "name": record.Name})

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      rawToken, // shown once, never stored
		"id":         record.ID,
		"env":        record.Env,
		"name":       record.Name,
		"created_at": record.CreatedAt,
	})
}

// GET /api/projects/{slug}/tokens — list service tokens for a project
func (h *ServiceTokenHandler) List(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot manage service tokens")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	callerRole, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can list service tokens")
		return
	}

	tokens, err := h.tokenSvc.List(r.Context(), proj.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list service tokens")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

// DELETE /api/projects/{slug}/tokens/{tokenID} — revoke a service token
func (h *ServiceTokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if mw.IsServiceTokenRequest(r.Context()) {
		writeError(w, http.StatusForbidden, "service tokens cannot manage service tokens")
		return
	}

	claims := auth.ClaimsFromCtx(r.Context())
	slug := chi.URLParam(r, "slug")
	tokenID := chi.URLParam(r, "tokenID")

	proj, err := h.projectSvc.GetBySlug(r.Context(), slug, claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	callerRole, err := h.teamSvc.GetRole(r.Context(), proj.ID, claims.UserID)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeError(w, http.StatusForbidden, "only owners and admins can revoke service tokens")
		return
	}

	if err := h.tokenSvc.Revoke(r.Context(), proj.ID, tokenID); err != nil {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	h.auditSvc.Log(r.Context(), claims.UserID, proj.ID, "", "token_revoke", realIP(r), map[string]any{"token_id": tokenID})

	writeJSON(w, http.StatusOK, map[string]string{"message": "token revoked"})
}
