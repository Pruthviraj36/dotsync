// Package identity manages the local ed25519 keypair each dotsync client
// uses to sign what it pushes. AES-256-GCM already gives every push
// confidentiality and tamper-evidence (decryption fails if the ciphertext
// was altered) — this adds authenticity: proof of *which teammate* pushed
// a given version, verifiable by anyone else on the team without ever
// touching the private key, which never leaves this machine.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	dotsyncCrypto "github.com/Pruthviraj36/dotsync/internal/crypto"
)

// Dir returns ~/.dotsync, creating it if necessary.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".dotsync"), nil
}

func privPath(dir string) string           { return filepath.Join(dir, "id_ed25519") }
func pubPath(dir string) string            { return filepath.Join(dir, "id_ed25519.pub") }
func encryptionPrivPath(dir string) string { return filepath.Join(dir, "id_x25519") }
func encryptionPubPath(dir string) string  { return filepath.Join(dir, "id_x25519.pub") }

func projectKeyPath(dir, slug string) string {
	return filepath.Join(dir, "project-key-"+hex.EncodeToString([]byte(slug)))
}

// SaveProjectKey stores a project key encrypted with this device's X25519 identity.
func SaveProjectKey(slug string, projectKey []byte) error {
	if len(projectKey) != dotsyncCrypto.ProjectKeySize {
		return fmt.Errorf("project key must be %d bytes", dotsyncCrypto.ProjectKeySize)
	}
	privateKey, _, _, err := EnsureEncryption()
	if err != nil {
		return err
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	storageKey := sha256.Sum256(append([]byte("dotsync-local-project-key-v1:"), privateKey...))
	ciphertext, nonce, err := dotsyncCrypto.Encrypt(storageKey[:], projectKey)
	if err != nil {
		return fmt.Errorf("encrypt project key: %w", err)
	}
	if err := os.WriteFile(projectKeyPath(dir, slug), append(nonce, ciphertext...), 0600); err != nil {
		return fmt.Errorf("write project key: %w", err)
	}
	return nil
}

// LoadProjectKey loads a project key previously saved on this device.
func LoadProjectKey(slug string) ([]byte, error) {
	privateKey, _, _, err := EnsureEncryption()
	if err != nil {
		return nil, err
	}
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(projectKeyPath(dir, slug))
	if err != nil {
		return nil, fmt.Errorf("read project key: %w", err)
	}
	if len(data) < 12 {
		return nil, fmt.Errorf("project key file is malformed")
	}
	storageKey := sha256.Sum256(append([]byte("dotsync-local-project-key-v1:"), privateKey...))
	projectKey, err := dotsyncCrypto.Decrypt(storageKey[:], data[12:], data[:12])
	if err != nil || len(projectKey) != dotsyncCrypto.ProjectKeySize {
		return nil, fmt.Errorf("decrypt project key: authentication failed")
	}
	return projectKey, nil
}

// EnsureEncryption loads or creates the local X25519 key used to unwrap
// project keys. It is separate from the signing identity by design.
func EnsureEncryption() (privateKey, publicKey []byte, created bool, err error) {
	dir, err := Dir()
	if err != nil {
		return nil, nil, false, err
	}
	if data, readErr := os.ReadFile(encryptionPrivPath(dir)); readErr == nil && len(data) == dotsyncCrypto.X25519KeySize {
		pub, deriveErr := dotsyncCrypto.PublicKeyFromPrivate(data)
		if deriveErr != nil {
			return nil, nil, false, deriveErr
		}
		return data, pub, false, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, false, fmt.Errorf("create %s: %w", dir, err)
	}
	privateKey, publicKey, err = dotsyncCrypto.GenerateX25519Keypair()
	if err != nil {
		return nil, nil, false, err
	}
	if err := os.WriteFile(encryptionPrivPath(dir), privateKey, 0600); err != nil {
		return nil, nil, false, fmt.Errorf("write encryption private key: %w", err)
	}
	if err := os.WriteFile(encryptionPubPath(dir), []byte(hex.EncodeToString(publicKey)+"\n"), 0644); err != nil {
		return nil, nil, false, fmt.Errorf("write encryption public key: %w", err)
	}
	return privateKey, publicKey, true, nil
}

// Ensure loads the existing identity from disk, or generates and persists a
// new one if none exists yet. `created` is true only the first time this
// runs on a given machine, so callers can print a one-time notice rather
// than repeating it on every command.
func Ensure() (priv ed25519.PrivateKey, pub ed25519.PublicKey, created bool, err error) {
	dir, err := Dir()
	if err != nil {
		return nil, nil, false, err
	}

	if data, rErr := os.ReadFile(privPath(dir)); rErr == nil && len(data) == ed25519.PrivateKeySize {
		priv = ed25519.PrivateKey(data)
		return priv, priv.Public().(ed25519.PublicKey), false, nil
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, false, fmt.Errorf("create %s: %w", dir, err)
	}

	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, false, fmt.Errorf("generate ed25519 keypair: %w", err)
	}

	if err := os.WriteFile(privPath(dir), priv, 0600); err != nil {
		return nil, nil, false, fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath(dir), []byte(hex.EncodeToString(pub)+"\n"), 0644); err != nil {
		return nil, nil, false, fmt.Errorf("write public key: %w", err)
	}

	return priv, pub, true, nil
}

// PubKeyPath returns the path to the public key file, for display purposes
// (e.g. "✓ ed25519 identity created — ~/.dotsync/id_ed25519.pub").
func PubKeyPath() string {
	dir, err := Dir()
	if err != nil {
		return "~/.dotsync/id_ed25519.pub"
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		return filepath.Join("~", ".dotsync", "id_ed25519.pub")
	}
	return pubPath(dir)
}

// Hex encodes a public key as lowercase hex, the format stored server-side
// and embedded in signature-verification output.
func Hex(pub ed25519.PublicKey) string {
	return hex.EncodeToString(pub)
}

// FromHex decodes a hex-encoded public key back into an ed25519.PublicKey.
func FromHex(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid public key encoding: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: got %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}
