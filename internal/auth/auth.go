package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/Pruthviraj36/dotsync/internal/crypto"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour

	// refreshReuseGrace is how long a just-rotated token is still accepted.
	// Mobile/CLI clients can fire two requests at once; both see a 401 and both
	// attempt a refresh. Without a grace window the loser of the race looks
	// exactly like a stolen-token replay and every session gets nuked.
	refreshReuseGrace = 20 * time.Second

	maxGitHubResponseBytes = 1 << 20
)

// ErrInvalidRefreshToken is deliberately generic: refresh failures must not
// tell an attacker whether a token exists, was rotated, or expired.
var ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

// githubHTTPClient is shared so OAuth calls reuse connections instead of doing
// a fresh TCP + TLS handshake on every login.
var githubHTTPClient = &http.Client{Timeout: 15 * time.Second}

var refreshTokenFormat = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Plan     string `json:"plan"`
	jwt.RegisteredClaims
}

type Service struct {
	db        *db.DB
	jwtSecret []byte
}

func NewService(database *db.DB, jwtSecret string) *Service {
	return &Service{db: database, jwtSecret: []byte(jwtSecret)}
}

// IssueAccessToken creates a short-lived signed JWT.
func (s *Service) IssueAccessToken(user *model.User) (string, error) {
	claims := Claims{
		UserID:   user.ID,
		Username: user.Username,
		Plan:     user.Plan,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "dotsync",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// IssueRefreshToken generates a secure random token, stores hash in DB, returns raw token.
func (s *Service) IssueRefreshToken(ctx context.Context, userID string) (string, error) {
	raw, err := crypto.GenerateRandomToken(32)
	if err != nil {
		return "", err
	}

	tokenHash := crypto.HashToken(raw)
	id := uuid.New().String()
	expiresAt := time.Now().Add(RefreshTokenTTL)

	// Opportunistically prune this user's expired tokens so the table does not
	// grow without bound. Indexed by user_id, so this is cheap.
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE user_id = $1 AND expires_at < NOW()`, userID)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, revoked, created_at)
		VALUES ($1, $2, $3, $4, false, NOW())`,
		id, userID, tokenHash, expiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}

	return raw, nil
}

// RotateRefreshToken validates the old token, revokes it, and issues a new pair.
// This is refresh token rotation — compromised tokens are invalidated on first use.
//
// Uses an atomic transaction with FOR UPDATE so two concurrent requests cannot
// both pass validation. A token presented again inside refreshReuseGrace is
// treated as a client race and rotated again; a genuine replay after the grace
// window revokes every session for that user.
func (s *Service) RotateRefreshToken(ctx context.Context, rawToken string) (*model.User, string, string, error) {
	if !refreshTokenFormat.MatchString(rawToken) {
		return nil, "", "", ErrInvalidRefreshToken
	}
	tokenHash := crypto.HashToken(rawToken)

	// Start atomic transaction with row-level lock
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var rt model.RefreshToken
	var user model.User
	var revokedAt *time.Time

	// FOR UPDATE locks this row until transaction ends, preventing concurrent updates
	err = tx.QueryRowContext(ctx, `
		SELECT rt.id, rt.user_id, rt.expires_at, rt.revoked, rt.revoked_at,
		       u.id, u.username, u.email, u.plan, u.github_id, u.avatar_url,
		       u.created_at, u.updated_at
		FROM refresh_tokens rt
		JOIN users u ON u.id = rt.user_id
		WHERE rt.token_hash = $1
		FOR UPDATE OF rt`,
		tokenHash,
	).Scan(
		&rt.ID, &rt.UserID, &rt.ExpiresAt, &rt.Revoked, &revokedAt,
		&user.ID, &user.Username, &user.Email, &user.Plan, &user.GitHubID, &user.AvatarURL,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, "", "", ErrInvalidRefreshToken
	}

	if time.Now().After(rt.ExpiresAt) {
		return nil, "", "", ErrInvalidRefreshToken
	}

	if rt.Revoked {
		withinGrace := revokedAt != nil && time.Since(*revokedAt) <= refreshReuseGrace
		if !withinGrace {
			// Genuine replay outside the grace window: invalidate the whole
			// family. Keep the write in the same transaction as the lock.
			_, _ = tx.ExecContext(ctx,
				`UPDATE refresh_tokens SET revoked = true, revoked_at = COALESCE(revoked_at, NOW())
				 WHERE user_id = $1`, rt.UserID)
			_ = tx.Commit()
			return nil, "", "", ErrInvalidRefreshToken
		}
		// Concurrent-refresh race: fall through and mint a fresh pair. The old
		// row is already revoked, so this cannot extend the grace window.
	} else {
		// Revoke the old token (within transaction, after lock acquired)
		if _, err = tx.ExecContext(ctx,
			`UPDATE refresh_tokens SET revoked = true, revoked_at = NOW() WHERE id = $1`, rt.ID); err != nil {
			return nil, "", "", fmt.Errorf("revoke old token: %w", err)
		}
	}

	// Commit transaction to release lock before issuing new tokens.
	if err := tx.Commit(); err != nil {
		return nil, "", "", fmt.Errorf("commit transaction: %w", err)
	}

	// Issue new pair (outside transaction for better performance)
	accessToken, err := s.IssueAccessToken(&user)
	if err != nil {
		return nil, "", "", fmt.Errorf("issue access token: %w", err)
	}

	newRefresh, err := s.IssueRefreshToken(ctx, user.ID)
	if err != nil {
		return nil, "", "", fmt.Errorf("issue refresh token: %w", err)
	}

	return &user, accessToken, newRefresh, nil
}

// ValidateAccessToken parses and validates a JWT, returning claims.
func (s *Service) ValidateAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	},
		// Pin the algorithm and issuer: never let a token signed with a
		// different HMAC variant or issued for another service through.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("dotsync"),
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// RevokeAllSessions ends every session for a user (logout across devices).
//
// Rows are deleted rather than marked revoked: the refresh path accepts a
// revoked token for a short grace window to survive concurrent client
// refreshes, and a logout must not be undoable by replaying an old token
// inside that window. A deleted token simply does not exist.
func (s *Service) RevokeAllSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
	return err
}

// --- GitHub OAuth ---

type GitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

// FetchGitHubUser calls the GitHub API with an OAuth access token.
func FetchGitHubUser(accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("create github request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dotsync-server")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned %d", resp.StatusCode)
	}

	// Bound the body: a hostile/broken upstream must not be able to make the
	// server allocate arbitrarily large buffers.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read github response: %w", err)
	}

	var u GitHubUser
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("decode github response: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return nil, errors.New("github returned an incomplete user profile")
	}

	return &u, nil
}

// UpsertUser creates or updates a user from GitHub OAuth data.
func UpsertUser(ctx context.Context, database *db.DB, ghUser *GitHubUser) (*model.User, error) {
	var user model.User
	err := database.QueryRowContext(ctx, `
		INSERT INTO users (id, github_id, username, email, avatar_url, plan, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'free', NOW(), NOW())
		ON CONFLICT (github_id) DO UPDATE
		SET username = EXCLUDED.username,
		    email = EXCLUDED.email,
		    avatar_url = EXCLUDED.avatar_url,
		    updated_at = NOW()
		RETURNING id, github_id, username, email, avatar_url, plan, created_at, updated_at`,
		uuid.New().String(), ghUser.ID, ghUser.Login, ghUser.Email, ghUser.AvatarURL,
	).Scan(
		&user.ID, &user.GitHubID, &user.Username, &user.Email,
		&user.AvatarURL, &user.Plan, &user.CreatedAt, &user.UpdatedAt,
	)
	return &user, err
}

// contextKey is unexported to avoid collisions.
type contextKey string

const UserClaimsKey contextKey = "user_claims"

func ClaimsFromCtx(ctx context.Context) *Claims {
	c, _ := ctx.Value(UserClaimsKey).(*Claims)
	return c
}
