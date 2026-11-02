package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordSaltSize = 16
	passwordKeySize  = 32
	passwordTime     = 3
	passwordMemory   = 64 * 1024
	passwordThreads  = 4
)

var (
	ErrPasswordNotSet   = errors.New("account password is not set")
	ErrInvalidPassword  = errors.New("invalid account password")
	ErrPasswordRequired = errors.New("account password is required")
)

func HashAccountPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("account password must be at least 12 characters")
	}
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemory, passwordThreads, passwordKeySize)
	encode := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		passwordMemory, passwordTime, passwordThreads, encode.EncodeToString(salt), encode.EncodeToString(hash)), nil
}

func VerifyAccountPassword(password, encoded string) error {
	if encoded == "" {
		return ErrPasswordNotSet
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return ErrInvalidPassword
	}
	var memory, timeCost, threads int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil ||
		memory <= 0 || timeCost <= 0 || threads <= 0 {
		return ErrInvalidPassword
	}
	decode := base64.RawStdEncoding
	salt, err := decode.DecodeString(parts[4])
	if err != nil || len(salt) != passwordSaltSize {
		return ErrInvalidPassword
	}
	expected, err := decode.DecodeString(parts[5])
	if err != nil || len(expected) != passwordKeySize {
		return ErrInvalidPassword
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(timeCost), uint32(memory), uint8(threads), uint32(len(expected)))
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return ErrInvalidPassword
	}
	return nil
}

func (s *Service) SetAccountPassword(ctx context.Context, userID, current, next string) error {
	var existing string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(account_password_hash, '') FROM users WHERE id = $1`, userID).Scan(&existing); err != nil {
		return fmt.Errorf("load account password: %w", err)
	}
	if existing != "" {
		if err := VerifyAccountPassword(current, existing); err != nil {
			return ErrInvalidPassword
		}
	}
	hash, err := HashAccountPassword(next)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET account_password_hash = $1, updated_at = NOW() WHERE id = $2`, hash, userID); err != nil {
		return fmt.Errorf("save account password: %w", err)
	}
	return nil
}

func (s *Service) VerifyAccountPassword(ctx context.Context, userID, password string) error {
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(account_password_hash, '') FROM users WHERE id = $1`, userID).Scan(&encoded); err != nil {
		return fmt.Errorf("load account password: %w", err)
	}
	return VerifyAccountPassword(password, encoded)
}
