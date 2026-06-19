package cliCrypto

import (
	"fmt"
	"strings"

	"github.com/Pruthviraj36/dotsync/internal/crypto"
)

// EncryptEnvFile encrypts the contents of a .env file for transmission.
// The key is derived from the provided password (project password or access token)
// + project slug (zero-knowledge).
func EncryptEnvFile(envContent, password, projectSlug string) (ciphertext, nonce []byte, err error) {
	key := crypto.DeriveKey(password, projectSlug)
	return crypto.Encrypt(key, []byte(envContent))
}

// DecryptEnvFile decrypts the encrypted blob received from the server.
func DecryptEnvFile(ciphertext, nonce []byte, password, projectSlug string) (string, error) {
	key := crypto.DeriveKey(password, projectSlug)
	plain, err := crypto.Decrypt(key, ciphertext, nonce)
	if err != nil {
		return "", fmt.Errorf("decryption failed — wrong password or corrupted data: %w", err)
	}
	return string(plain), nil
}

// ParseEnvFile parses a .env file into key-value pairs.
// Supports comments (#), blank lines, quoted values, and KEY=VALUE format.
func ParseEnvFile(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		result[key] = value
	}
	return result
}

// DiffEnvFiles returns keys that were added, removed, or changed between two env maps.
func DiffEnvFiles(old, new map[string]string) (added, removed, changed []string) {
	for k := range new {
		if _, ok := old[k]; !ok {
			added = append(added, k)
		} else if old[k] != new[k] {
			changed = append(changed, k)
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			removed = append(removed, k)
		}
	}
	return
}
