package cliCrypto

import "testing"

func TestEnvEncryptionRoundTrip(t *testing.T) {
	content := "DATABASE_URL=postgres://localhost\nAPI_KEY=abc123\n"
	ct, nonce, err := EncryptEnvFileForEnv(content, "a-strong-password", "my-project", "production")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := DecryptEnvFileForEnv(ct, nonce, "a-strong-password", "my-project", "production")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != content {
		t.Fatalf("round trip mismatch: %q != %q", got, content)
	}
}

func TestEnvEncryptionRejectsWrongEnvironment(t *testing.T) {
	ct, nonce, err := EncryptEnvFileForEnv("SECRET=1", "a-strong-password", "my-project", "production")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := DecryptEnvFileForEnv(ct, nonce, "a-strong-password", "my-project", "dev"); err == nil {
		t.Fatal("ciphertext from production decrypted as dev; AAD binding is not enforced")
	}
	if _, err := DecryptEnvFileForEnv(ct, nonce, "a-strong-password", "other-project", "production"); err == nil {
		t.Fatal("ciphertext decrypted under a different project")
	}
}

func TestLegacyCiphertextStillDecrypts(t *testing.T) {
	// Blobs written before AAD binding must remain readable after upgrade.
	legacyCT, legacyNonce, err := EncryptEnvFile("OLD=1", "a-strong-password", "my-project")
	if err != nil {
		t.Fatalf("legacy encrypt: %v", err)
	}
	got, err := DecryptEnvFileForEnv(legacyCT, legacyNonce, "a-strong-password", "my-project", "dev")
	if err != nil {
		t.Fatalf("legacy blob no longer decrypts: %v", err)
	}
	if got != "OLD=1" {
		t.Fatalf("got %q, want OLD=1", got)
	}
}

func TestParseEnvFileStrictRejectsMalformedKeys(t *testing.T) {
	bad := []string{
		"FOO BAR=1",
		"1KEY=value",
		"NOEQUALS",
		"=value",
		"export KEY=value",
	}
	for _, content := range bad {
		if _, err := ParseEnvFileStrict(content); err == nil {
			t.Errorf("ParseEnvFileStrict(%q) accepted malformed input", content)
		}
	}

	good := "# comment\n\nA=1\nB='two'\nC=\"three\"\n"
	got, err := ParseEnvFileStrict(good)
	if err != nil {
		t.Fatalf("rejected valid env: %v", err)
	}
	if got["A"] != "1" || got["B"] != "two" || got["C"] != "three" {
		t.Fatalf("unexpected parse result: %#v", got)
	}
}
