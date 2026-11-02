package auth

import (
	"testing"
)

func TestAccountPasswordHashAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	encoded, err := HashAccountPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAccountPassword(password, encoded); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if err := VerifyAccountPassword("wrong password", encoded); err != ErrInvalidPassword {
		t.Fatalf("wrong password error = %v, want %v", err, ErrInvalidPassword)
	}
}

func TestAccountPasswordMinimumLength(t *testing.T) {
	if _, err := HashAccountPassword("too-short"); err == nil {
		t.Fatal("short password was accepted")
	}
}
