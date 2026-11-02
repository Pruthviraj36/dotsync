package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const ReauthTokenTTL = 15 * time.Minute

var ErrInvalidReauthToken = errors.New("invalid or expired reauthentication token")

type ReauthClaims struct {
	UserID string `json:"user_id"`
	Scope  string `json:"scope"`
	jwt.RegisteredClaims
}

func (s *Service) IssueReauthToken(userID, scope string) (string, error) {
	if userID == "" || scope == "" {
		return "", errors.New("reauthentication user and scope are required")
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, ReauthClaims{
		UserID: userID,
		Scope:  scope,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ReauthTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "dotsync-reauth",
		},
	})
	return token.SignedString(s.jwtSecret)
}

func (s *Service) ValidateReauthToken(raw, userID, scope string) error {
	var claims ReauthClaims
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidReauthToken
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid || claims.UserID != userID || claims.Scope != scope {
		return fmt.Errorf("%w", ErrInvalidReauthToken)
	}
	return nil
}
