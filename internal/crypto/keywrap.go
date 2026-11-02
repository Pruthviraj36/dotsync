package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	X25519KeySize  = 32
	ProjectKeySize = 32
)

// KeyEnvelope is a project key encrypted for one X25519 recipient.
type KeyEnvelope struct {
	RecipientPubKey string `json:"recipient_pubkey"`
	EphemeralPubKey string `json:"ephemeral_pubkey"`
	Nonce           []byte `json:"nonce"`
	Ciphertext      []byte `json:"ciphertext"`
}

// GenerateX25519Keypair creates a private/public key pair for key agreement.
func GenerateX25519Keypair() (privateKey, publicKey []byte, err error) {
	privateKey = make([]byte, X25519KeySize)
	if _, err := io.ReadFull(rand.Reader, privateKey); err != nil {
		return nil, nil, fmt.Errorf("generate x25519 private key: %w", err)
	}

	publicKey, err = curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("derive x25519 public key: %w", err)
	}
	return privateKey, publicKey, nil
}

// PublicKeyFromPrivate derives the public key for an X25519 private key.
func PublicKeyFromPrivate(privateKey []byte) ([]byte, error) {
	if len(privateKey) != X25519KeySize {
		return nil, fmt.Errorf("x25519 private key must be 32 bytes")
	}
	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive x25519 public key: %w", err)
	}
	return publicKey, nil
}

// WrapProjectKey encrypts a 32-byte project key for a recipient.
func WrapProjectKey(projectID string, projectKey, recipientPublicKey []byte) (*KeyEnvelope, error) {
	if len(projectKey) != ProjectKeySize || len(recipientPublicKey) != X25519KeySize {
		return nil, fmt.Errorf("project and recipient keys must be 32 bytes")
	}
	ephemeralPrivate, ephemeralPublic, err := GenerateX25519Keypair()
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(ephemeralPrivate, recipientPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive envelope secret: %w", err)
	}
	key, err := deriveEnvelopeKey(shared, projectID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate envelope nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, projectKey, []byte(projectID))
	return &KeyEnvelope{
		RecipientPubKey: hex.EncodeToString(recipientPublicKey),
		EphemeralPubKey: hex.EncodeToString(ephemeralPublic),
		Nonce:           nonce,
		Ciphertext:      ciphertext,
	}, nil
}

// UnwrapProjectKey decrypts an envelope using the recipient private key.
func UnwrapProjectKey(projectID string, envelope *KeyEnvelope, recipientPrivateKey []byte) ([]byte, error) {
	if len(recipientPrivateKey) != X25519KeySize {
		return nil, fmt.Errorf("recipient private key must be 32 bytes")
	}
	recipientPublicKey, err := hex.DecodeString(envelope.RecipientPubKey)
	if err != nil || len(recipientPublicKey) != X25519KeySize {
		return nil, fmt.Errorf("invalid envelope recipient public key")
	}
	ephemeralPublicKey, err := hex.DecodeString(envelope.EphemeralPubKey)
	if err != nil || len(ephemeralPublicKey) != X25519KeySize {
		return nil, fmt.Errorf("invalid envelope ephemeral public key")
	}
	derivedPublicKey, err := curve25519.X25519(recipientPrivateKey, curve25519.Basepoint)
	if err != nil || subtle.ConstantTimeCompare(derivedPublicKey, recipientPublicKey) != 1 {
		return nil, fmt.Errorf("envelope is for a different recipient")
	}
	shared, err := curve25519.X25519(recipientPrivateKey, ephemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive envelope secret: %w", err)
	}
	key, err := deriveEnvelopeKey(shared, projectID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	projectKey, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, []byte(projectID))
	if err != nil || len(projectKey) != ProjectKeySize {
		return nil, fmt.Errorf("decrypt project-key envelope: authentication failed")
	}
	return projectKey, nil
}

func deriveEnvelopeKey(shared []byte, projectID string) ([]byte, error) {
	reader := hkdf.New(sha256.New, shared, nil, []byte("dotsync/project-key/"+projectID))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive envelope key: %w", err)
	}
	return key, nil
}
