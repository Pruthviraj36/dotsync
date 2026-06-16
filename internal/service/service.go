package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/model"
)

type SecretService struct {
	db *db.DB
}

func NewSecretService(database *db.DB) *SecretService {
	return &SecretService{db: database}
}

// PushSecrets stores a new version of encrypted secrets for an environment.
// The ciphertext and nonce are already encrypted on the client — we store blobs only.
func (s *SecretService) PushSecrets(ctx context.Context, envID, pushedBy string, encryptedData, nonce []byte) (*model.Secret, error) {
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
		Version:       currentVersion + 1,
		PushedBy:      pushedBy,
		CreatedAt:     time.Now(),
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO secrets (id, environment_id, encrypted_data, data_nonce, version, pushed_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		secret.ID, secret.EnvironmentID, secret.EncryptedData,
		secret.DataNonce, secret.Version, secret.PushedBy, secret.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert secret: %w", err)
	}

	return secret, nil
}

// PullLatest returns the most recent encrypted secret blob for an environment.
func (s *SecretService) PullLatest(ctx context.Context, envID string) (*model.Secret, error) {
	var sec model.Secret
	err := s.db.QueryRowContext(ctx, `
		SELECT id, environment_id, encrypted_data, data_nonce, version, pushed_by, created_at
		FROM secrets
		WHERE environment_id = $1
		ORDER BY version DESC
		LIMIT 1`, envID,
	).Scan(
		&sec.ID, &sec.EnvironmentID, &sec.EncryptedData,
		&sec.DataNonce, &sec.Version, &sec.PushedBy, &sec.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no secrets found for this environment")
	}
	return &sec, err
}

// GetHistory returns the version history within the plan's history window.
func (s *SecretService) GetHistory(ctx context.Context, envID string, historyDays int) ([]model.Secret, error) {
	since := time.Now().AddDate(0, 0, -historyDays)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, environment_id, version, pushed_by, created_at
		FROM secrets
		WHERE environment_id = $1 AND created_at >= $2
		ORDER BY version DESC`, envID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []model.Secret
	for rows.Next() {
		var sec model.Secret
		if err := rows.Scan(&sec.ID, &sec.EnvironmentID, &sec.Version, &sec.PushedBy, &sec.CreatedAt); err != nil {
			return nil, err
		}
		history = append(history, sec)
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
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (id, user_id, project_id, environment_id, action, metadata, ip_address, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())`,
		uuid.New().String(), userID, projectID, envID, action, string(metaJSON), ip,
	)
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
