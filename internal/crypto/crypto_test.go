package crypto

import (
	"bytes"
	"testing"
)

func testKey() []byte {
	return DeriveKey("correct horse battery staple", "demo-project")
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey()
	plaintext := []byte("DATABASE_URL=postgres://localhost\nAPI_KEY=secret")

	ct, nonce, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(nonce) != 12 {
		t.Fatalf("nonce length = %d, want 12", len(nonce))
	}

	got, err := Decrypt(key, ct, nonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: %q != %q", got, plaintext)
	}
}

func TestAADBindingRejectsWrongContext(t *testing.T) {
	key := testKey()
	aad := []byte("dotsync-env-v2:demo-project:production")
	plaintext := []byte("SECRET=value")

	ct, nonce, err := EncryptWithAAD(key, plaintext, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := DecryptWithAAD(key, ct, nonce, aad); err != nil {
		t.Fatalf("decrypt with correct AAD: %v", err)
	}

	// A server that moves the ciphertext to another environment must fail.
	wrong := []byte("dotsync-env-v2:demo-project:dev")
	if _, err := DecryptWithAAD(key, ct, nonce, wrong); err == nil {
		t.Fatal("decrypt with wrong AAD succeeded; binding is not enforced")
	}
	if _, err := DecryptWithAAD(key, ct, nonce, nil); err == nil {
		t.Fatal("decrypt without AAD succeeded for an AAD-bound ciphertext")
	}
}

func TestDecryptRejectsBadNonceLength(t *testing.T) {
	key := testKey()
	ct, _, err := Encrypt(key, []byte("x"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := Decrypt(key, ct, []byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short nonce")
	}
}

func TestDifferentProjectsDeriveDifferentKeys(t *testing.T) {
	a := DeriveKey("same password", "project-a")
	b := DeriveKey("same password", "project-b")
	if bytes.Equal(a, b) {
		t.Fatal("per-project keys must differ")
	}
}

func TestHMACVerifyIsExact(t *testing.T) {
	secret := []byte("k")
	sig := HMACSign(secret, []byte("payload"))
	if !HMACVerify(secret, []byte("payload"), sig) {
		t.Fatal("valid HMAC rejected")
	}
	if HMACVerify(secret, []byte("payload2"), sig) {
		t.Fatal("HMAC accepted for modified payload")
	}
}
