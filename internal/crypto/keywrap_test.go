package crypto

import (
	"bytes"
	"testing"
)

func TestProjectKeyEnvelopeRoundTrip(t *testing.T) {
	recipientPrivate, recipientPublic, err := GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	projectKey := bytes.Repeat([]byte{0x42}, ProjectKeySize)
	envelope, err := WrapProjectKey("project-1", projectKey, recipientPublic)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnwrapProjectKey("project-1", envelope, recipientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, projectKey) {
		t.Fatal("unwrapped project key differs")
	}
	if _, err := UnwrapProjectKey("project-2", envelope, recipientPrivate); err == nil {
		t.Fatal("envelope decrypted with the wrong project context")
	}
}

func TestProjectKeyEnvelopeRejectsWrongRecipient(t *testing.T) {
	recipientPrivate, recipientPublic, err := GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	otherPrivate, _, err := GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := WrapProjectKey("project-1", bytes.Repeat([]byte{1}, ProjectKeySize), recipientPublic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapProjectKey("project-1", envelope, otherPrivate); err == nil {
		t.Fatal("wrong recipient decrypted envelope")
	}
	_ = recipientPrivate
}
