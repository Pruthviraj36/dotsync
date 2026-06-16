package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/Pruthviraj36/dotsync/internal/crypto"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/model"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour
)

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
func (s *Service) RotateRefreshToken(ctx context.Context, rawToken string) (*model.User, string, string, error) {
	tokenHash := crypto.HashToken(rawToken)

	var rt model.RefreshToken
	var user model.User

	err := s.db.QueryRowContext(ctx, `
		SELECT rt.id, rt.user_id, rt.expires_at, rt.revoked,
		       u.id, u.username, u.email, u.plan, u.github_id, u.avatar_url
		FROM refresh_tokens rt
		JOIN users u ON u.id = rt.user_id
		WHERE rt.token_hash = $1`,
		tokenHash,
	).Scan(
		&rt.ID, &rt.UserID, &rt.ExpiresAt, &rt.Revoked,
		&user.ID, &user.Username, &user.Email, &user.Plan, &user.GitHubID, &user.AvatarURL,
	)
	if err != nil {
		return nil, "", "", fmt.Errorf("refresh token not found")
	}

	// If token already used (revoked), this is a replay attack — revoke ALL tokens for user
	if rt.Revoked {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE refresh_tokens SET revoked = true WHERE user_id = $1`, rt.UserID)
		return nil, "", "", fmt.Errorf("token reuse detected: all sessions invalidated")
	}

	if time.Now().After(rt.ExpiresAt) {
		return nil, "", "", fmt.Errorf("refresh token expired")
	}

	// Revoke the old token
	_, err = s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE id = $1`, rt.ID)
	if err != nil {
		return nil, "", "", err
	}

	// Issue new pair
	accessToken, err := s.IssueAccessToken(&user)
	if err != nil {
		return nil, "", "", err
	}

	newRefresh, err := s.IssueRefreshToken(ctx, user.ID)
	if err != nil {
		return nil, "", "", err
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
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// RevokeAllSessions revokes all refresh tokens for a user (logout all devices).
func (s *Service) RevokeAllSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE user_id = $1`, userID)
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
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned %d", resp.StatusCode)
	}

	var u GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
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
