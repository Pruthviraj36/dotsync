package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Pruthviraj36/dotsync/internal/crypto"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/model"
	"github.com/google/uuid"
)

type SecretService struct {
	db *db.DB
}

func NewSecretService(database *db.DB) *SecretService {
	return &SecretService{db: database}
}

// PushSecrets stores a new version of encrypted secrets for an environment.
// The ciphertext, nonce, and signature are already produced client-side —
// we store blobs only and never see plaintext or private key material.
func (s *SecretService) PushSecrets(ctx context.Context, envID, pushedBy string, encryptedData, nonce, signature []byte) (*model.Secret, error) {
	// Get current version
	var currentVersion int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM secrets WHERE environment_id = $1`, envID,
	).Scan(&currentVersion)
	if err != nil {
		return nil, fmt.Errorf("get version: %w", err)
	}

	secret := &model.Secret{
		ID:            uuid.New().String(),
		EnvironmentID: envID,
		EncryptedData: encryptedData,
		DataNonce:     nonce,
		Signature:     signature,
		Version:       currentVersion + 1,
		PushedBy:      pushedBy,
		CreatedAt:     time.Now(),
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO secrets (id, environment_id, encrypted_data, data_nonce, signature, version, pushed_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		secret.ID, secret.EnvironmentID, secret.EncryptedData,
		secret.DataNonce, secret.Signature, secret.Version, secret.PushedBy, secret.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert secret: %w", err)
	}

	return secret, nil
}

// PulledSecret bundles a secret with the pusher's resolved username and
// ed25519 public key (if they've ever set one), so callers can hand the
// CLI everything it needs to verify the push's signature in one call.
type PulledSecret struct {
	model.Secret
	PushedByUsername string
	PushedByPubKey   string // hex-encoded ed25519 public key, "" if none on file
}

// PullLatest returns the most recent encrypted secret blob for an environment,
// along with the pusher's username and public key for signature verification.
func (s *SecretService) PullLatest(ctx context.Context, envID string) (*PulledSecret, error) {
	var sec PulledSecret
	var pubkey sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.environment_id, s.encrypted_data, s.data_nonce, s.signature,
		       s.version, s.pushed_by, s.created_at, u.username, u.ed25519_pubkey
		FROM secrets s
		JOIN users u ON u.id = s.pushed_by
		WHERE s.environment_id = $1
		ORDER BY s.version DESC
		LIMIT 1`, envID,
	).Scan(
		&sec.ID, &sec.EnvironmentID, &sec.EncryptedData,
		&sec.DataNonce, &sec.Signature, &sec.Version, &sec.PushedBy, &sec.CreatedAt,
		&sec.PushedByUsername, &pubkey,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no secrets found for this environment")
	}
	sec.PushedByPubKey = pubkey.String
	return &sec, err
}

// HistoryEntry bundles a secret's metadata with the pusher's resolved
// username, so callers never have to display a raw user-ID UUID.
type HistoryEntry struct {
	Version   int       `json:"version"`
	PushedBy  string    `json:"pushed_by"` // username, resolved via join — never a raw user ID
	CreatedAt time.Time `json:"created_at"`
}

// GetHistory returns the version history within the plan's history window.
func (s *SecretService) GetHistory(ctx context.Context, envID string, historyDays int) ([]HistoryEntry, error) {
	// historyDays <= 0 means "no limit" — don't filter by date at all.
	// (Naively doing time.Now().AddDate(0, 0, -historyDays) with a negative
	// historyDays used as a sentinel would compute a cutoff in the FUTURE,
	// silently hiding everything instead of showing everything.)
	query := `
		SELECT s.version, u.username, s.created_at
		FROM secrets s
		JOIN users u ON u.id = s.pushed_by
		WHERE s.environment_id = $1`
	args := []any{envID}

	if historyDays > 0 {
		since := time.Now().AddDate(0, 0, -historyDays)
		query += ` AND s.created_at >= $2`
		args = append(args, since)
	}
	query += ` ORDER BY s.version DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []HistoryEntry
	for rows.Next() {
		var entry HistoryEntry
		if err := rows.Scan(&entry.Version, &entry.PushedBy, &entry.CreatedAt); err != nil {
			return nil, err
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// --- Project Service ---

type ProjectService struct {
	db *db.DB
}

func NewProjectService(database *db.DB) *ProjectService {
	return &ProjectService{db: database}
}

func (s *ProjectService) Create(ctx context.Context, ownerID, name, slug, description string) (*model.Project, error) {
	proj := &model.Project{
		ID:          uuid.New().String(),
		OwnerID:     ownerID,
		Name:        name,
		Slug:        slug,
		Description: description,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		proj.ID, proj.OwnerID, proj.Name, proj.Slug, proj.Description, proj.CreatedAt, proj.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}

	// Auto-create owner as team member
	memberID := uuid.New().String()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO team_members (id, project_id, user_id, role, invited_by, created_at)
		VALUES ($1, $2, $3, 'owner', $3, NOW())`,
		memberID, proj.ID, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("add owner member: %w", err)
	}

	// Auto-create default environments
	for _, env := range []string{"dev", "staging", "production"} {
		_, _ = s.db.ExecContext(ctx, `
			INSERT INTO environments (id, project_id, name, created_at)
			VALUES ($1, $2, $3, NOW())`,
			uuid.New().String(), proj.ID, env,
		)
	}

	return proj, nil
}

func (s *ProjectService) GetBySlug(ctx context.Context, slug, userID string) (*model.Project, error) {
	var proj model.Project
	err := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.owner_id, p.name, p.slug, p.description, p.created_at, p.updated_at
		FROM projects p
		JOIN team_members tm ON tm.project_id = p.id
		WHERE p.slug = $1 AND tm.user_id = $2`,
		slug, userID,
	).Scan(&proj.ID, &proj.OwnerID, &proj.Name, &proj.Slug, &proj.Description, &proj.CreatedAt, &proj.UpdatedAt)
	return &proj, err
}

func (s *ProjectService) ListForUser(ctx context.Context, userID string) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.owner_id, p.name, p.slug, p.description, p.created_at, p.updated_at
		FROM projects p
		JOIN team_members tm ON tm.project_id = p.id
		WHERE tm.user_id = $1
		ORDER BY p.created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []model.Project
	for rows.Next() {
		var p model.Project
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *ProjectService) CountForUser(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_members WHERE user_id = $1 AND role = 'owner'`, userID,
	).Scan(&count)
	return count, err
}

func (s *ProjectService) GetEnvironment(ctx context.Context, projectID, envName string) (*model.Environment, error) {
	var env model.Environment
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, name, created_at FROM environments WHERE project_id = $1 AND name = $2`,
		projectID, envName,
	).Scan(&env.ID, &env.ProjectID, &env.Name, &env.CreatedAt)
	return &env, err
}

// --- Audit Log Service ---

type AuditService struct {
	db *db.DB
}

func NewAuditService(database *db.DB) *AuditService {
	return &AuditService{db: database}
}

func (s *AuditService) Log(ctx context.Context, userID, projectID, envID, action, ip string, meta map[string]any) {
	metaJSON, _ := json.Marshal(meta)

	// environment_id is a nullable FK — actions like password rotation
	// aren't scoped to a single environment, so pass NULL rather than an
	// empty string (which would fail the foreign key check silently).
	var envIDArg any
	if envID != "" {
		envIDArg = envID
	}

	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (id, user_id, project_id, environment_id, action, metadata, ip_address, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())`,
		uuid.New().String(), userID, projectID, envIDArg, action, string(metaJSON), ip,
	)
}

// --- Project Password Service ---
//
// Holds the E2EE project password server-side so authenticated team members
// can fetch it instead of re-typing it on every new machine. The password is
// never stored in plaintext: it's encrypted with a per-project subkey derived
// from SERVER_MASTER_KEY (see internal/crypto.DeriveServerSubkey). Only the
// server process holding that master key can ever decrypt it.

type PasswordService struct {
	db        *db.DB
	masterKey []byte
}

func NewPasswordService(database *db.DB, masterKey []byte) *PasswordService {
	return &PasswordService{db: database, masterKey: masterKey}
}

// SetPassword encrypts and upserts the password for a project.
func (s *PasswordService) SetPassword(ctx context.Context, projectID, updatedBy, password string) error {
	key := crypto.DeriveServerSubkey(s.masterKey, projectID)
	ciphertext, nonce, err := crypto.Encrypt(key, []byte(password))
	if err != nil {
		return fmt.Errorf("encrypt password: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO project_passwords (id, project_id, encrypted_password, password_nonce, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			encrypted_password = EXCLUDED.encrypted_password,
			password_nonce     = EXCLUDED.password_nonce,
			updated_by         = EXCLUDED.updated_by,
			updated_at         = NOW()`,
		uuid.New().String(), projectID, ciphertext, nonce, updatedBy,
	)
	if err != nil {
		return fmt.Errorf("store password: %w", err)
	}
	return nil
}

// GetPassword decrypts and returns the stored password for a project.
func (s *PasswordService) GetPassword(ctx context.Context, projectID string) (string, error) {
	var ciphertext, nonce []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT encrypted_password, password_nonce FROM project_passwords WHERE project_id = $1`,
		projectID,
	).Scan(&ciphertext, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no password set for this project yet — run: dotsync init --rotate-password")
	}
	if err != nil {
		return "", fmt.Errorf("fetch password: %w", err)
	}

	key := crypto.DeriveServerSubkey(s.masterKey, projectID)
	plaintext, err := crypto.Decrypt(key, ciphertext, nonce)
	if err != nil {
		return "", fmt.Errorf("decrypt password: %w", err)
	}
	return string(plaintext), nil
}

// --- Team Service ---

type TeamService struct {
	db *db.DB
}

func NewTeamService(database *db.DB) *TeamService {
	return &TeamService{db: database}
}

func (s *TeamService) IsProjectMember(ctx context.Context, projectID, userID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&count)
	return count > 0, err
}

func (s *TeamService) GetRole(ctx context.Context, projectID, userID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM team_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&role)
	return role, err
}

func (s *TeamService) CountMembers(ctx context.Context, projectID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_members WHERE project_id = $1`, projectID,
	).Scan(&count)
	return count, err
}

func (s *TeamService) InviteMember(ctx context.Context, projectID, userID, role, invitedBy string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO team_members (id, project_id, user_id, role, invited_by, created_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (project_id, user_id) DO NOTHING`,
		uuid.New().String(), projectID, userID, role, invitedBy,
	)
	return err
}

// ListMembers returns all team members for a project with their usernames and roles.
func (s *TeamService) ListMembers(ctx context.Context, projectID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.username, u.avatar_url, tm.role, tm.created_at
		FROM team_members tm
		JOIN users u ON u.id = tm.user_id
		WHERE tm.project_id = $1
		ORDER BY
			CASE tm.role
				WHEN 'owner'  THEN 1
				WHEN 'admin'  THEN 2
				WHEN 'member' THEN 3
				WHEN 'viewer' THEN 4
			END, tm.created_at ASC`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []map[string]any
	for rows.Next() {
		var username, avatarURL, role string
		var createdAt time.Time
		if err := rows.Scan(&username, &avatarURL, &role, &createdAt); err != nil {
			return nil, err
		}
		members = append(members, map[string]any{
			"username":   username,
			"avatar_url": avatarURL,
			"role":       role,
			"joined_at":  createdAt,
		})
	}
	return members, rows.Err()
}

// RemoveMember removes a user from a project team.
// Cannot remove the owner.
func (s *TeamService) RemoveMember(ctx context.Context, projectID, targetUserID string) error {
	// Prevent removing the owner
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM team_members WHERE project_id = $1 AND user_id = $2`,
		projectID, targetUserID,
	).Scan(&role)
	if err != nil {
		return fmt.Errorf("member not found")
	}
	if role == "owner" {
		return fmt.Errorf("cannot remove the project owner")
	}

	_, err = s.db.ExecContext(ctx,
		`DELETE FROM team_members WHERE project_id = $1 AND user_id = $2`,
		projectID, targetUserID,
	)
	return err
}

// UpdateRole changes a team member's role.
// Cannot change the owner's role or promote someone to owner.
func (s *TeamService) UpdateRole(ctx context.Context, projectID, targetUserID, newRole string) error {
	validRoles := map[string]bool{"admin": true, "member": true, "viewer": true}
	if !validRoles[newRole] {
		return fmt.Errorf("invalid role: must be admin, member, or viewer")
	}

	var currentRole string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM team_members WHERE project_id = $1 AND user_id = $2`,
		projectID, targetUserID,
	).Scan(&currentRole)
	if err != nil {
		return fmt.Errorf("member not found")
	}
	if currentRole == "owner" {
		return fmt.Errorf("cannot change the project owner's role")
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE team_members SET role = $1 WHERE project_id = $2 AND user_id = $3`,
		newRole, projectID, targetUserID,
	)
	return err
}

// --- Secret version pull ---

// PullVersion returns a specific version of secrets for an environment,
// along with the pusher's username and public key for signature verification.
func (s *SecretService) PullVersion(ctx context.Context, envID string, version int) (*PulledSecret, error) {
	var sec PulledSecret
	var pubkey sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.environment_id, s.encrypted_data, s.data_nonce, s.signature,
		       s.version, s.pushed_by, s.created_at, u.username, u.ed25519_pubkey
		FROM secrets s
		JOIN users u ON u.id = s.pushed_by
		WHERE s.environment_id = $1 AND s.version = $2`,
		envID, version,
	).Scan(
		&sec.ID, &sec.EnvironmentID, &sec.EncryptedData,
		&sec.DataNonce, &sec.Signature, &sec.Version, &sec.PushedBy, &sec.CreatedAt,
		&sec.PushedByUsername, &pubkey,
	)
	if err != nil {
		return nil, fmt.Errorf("version %d not found", version)
	}
	sec.PushedByPubKey = pubkey.String
	return &sec, nil
}

// --- Audit read ---

// GetAuditLogs returns audit log entries for a project.
func (s *AuditService) GetLogs(ctx context.Context, projectID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT al.action, al.metadata, al.ip_address, al.created_at,
		       u.username, e.name as env_name
		FROM audit_logs al
		JOIN users u ON u.id = al.user_id
		LEFT JOIN environments e ON e.id = al.environment_id
		WHERE al.project_id = $1
		ORDER BY al.created_at DESC
		LIMIT $2`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []map[string]any
	for rows.Next() {
		var action, metadata, ip, username string
		var envName *string
		var createdAt time.Time
		if err := rows.Scan(&action, &metadata, &ip, &createdAt, &username, &envName); err != nil {
			return nil, err
		}
		entry := map[string]any{
			"action":     action,
			"username":   username,
			"ip":         ip,
			"created_at": createdAt,
			"metadata":   metadata,
		}
		if envName != nil {
			entry["env"] = *envName
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

// GetEnvironments returns all environments for a project.
func (s *ProjectService) GetEnvironments(ctx context.Context, projectID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, created_at FROM environments WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var envs []map[string]any
	for rows.Next() {
		var id, name string
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &createdAt); err != nil {
			continue
		}
		envs = append(envs, map[string]any{
			"id": id, "name": name, "created_at": createdAt,
		})
	}
	return envs, rows.Err()
}
