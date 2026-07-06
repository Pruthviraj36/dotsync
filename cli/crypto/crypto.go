package cliCrypto

import (
	"fmt"
	"regexp"
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

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnvFileStrict parses a .env file and rejects any line that is not:
//   - blank
//   - a comment (starts with '#')
//   - a KEY=VALUE pair with a valid key name
func ParseEnvFileStrict(content string) (map[string]string, error) {
	result := make(map[string]string)
	lines := strings.Split(content, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.Index(line, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("line %d: only comments and KEY=VALUE are allowed", i+1)
		}

		key := strings.TrimSpace(line[:idx])
		if !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d: invalid key %q", i+1, key)
		}

		value := strings.TrimSpace(line[idx+1:])
		result[key] = stripOptionalQuotes(value)
	}
	return result, nil
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
		result[key] = stripOptionalQuotes(value)
	}
	return result
}

func stripOptionalQuotes(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
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
