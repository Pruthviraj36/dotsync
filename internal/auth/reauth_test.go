package auth

import "testing"

func TestReauthTokenRoundTrip(t *testing.T) {
	svc := NewService(nil, "test-secret")
	token, err := svc.IssueReauthToken("user-1", "sensitive")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateReauthToken(token, "user-1", "sensitive"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if err := svc.ValidateReauthToken(token, "user-2", "sensitive"); err == nil {
		t.Fatal("token accepted for another user")
	}
	if err := svc.ValidateReauthToken(token, "user-1", "team"); err == nil {
		t.Fatal("token accepted for another scope")
	}
}
