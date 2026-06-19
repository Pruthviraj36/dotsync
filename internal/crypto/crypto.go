package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	keySize    = 32 // AES-256
	nonceSize  = 12 // GCM standard nonce
	saltSize   = 32
)

// DeriveKey derives a 256-bit AES key from a secret + project salt using Argon2id.
// This is the client-side key derivation — the server never sees the raw key.
// Argon2id provides military-grade memory-hard protection against brute force.
func DeriveKey(secret, projectID string) []byte {
	salt := sha256.Sum256([]byte("dotsync-salt-v1:" + projectID))
	
	// time=3, memory=64MB, threads=4, keyLen=32
	// These are recommended parameters for Argon2id for a good balance of security and speed.
	key := argon2.IDKey([]byte(secret), salt[:], 3, 64*1024, 4, keySize)
	return key
}

// Encrypt encrypts plaintext with AES-256-GCM. Returns (ciphertext, nonce, error).
// A fresh random nonce is generated per encryption — never reuse nonces.
func Encrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext + auth tag to dst (nil here)
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Decrypt decrypts AES-256-GCM ciphertext using key + nonce.
func Decrypt(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("decryption failed: invalid key or corrupted data")
	}

	return plaintext, nil
}

// GenerateRandomToken generates a cryptographically secure random hex token.
func GenerateRandomToken(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken hashes a token with SHA-256 for storage (never store raw refresh tokens).
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// HMACSign computes HMAC-SHA256 of payload using secret key.
func HMACSign(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// HMACVerify verifies HMAC-SHA256 in constant time.
func HMACVerify(secret, payload []byte, signature string) bool {
	expected := HMACSign(secret, payload)
	return hmac.Equal([]byte(expected), []byte(signature))
}
