package handler

import "testing"

func TestValidSlug(t *testing.T) {
	valid := []string{"ab", "my-project", "a1", "team-secrets-2026", "x0"}
	for _, slug := range valid {
		if !validSlug(slug) {
			t.Errorf("validSlug(%q) = false, want true", slug)
		}
	}

	invalid := []string{
		"", "a", "-leading", "trailing-", "double--hyphen",
		"UPPER", "has space", "has_underscore", "api", "admin",
		"../etc", "a/b", "a.b", "a--b",
	}
	for _, slug := range invalid {
		if validSlug(slug) {
			t.Errorf("validSlug(%q) = true, want false", slug)
		}
	}
}

func TestQueryIntBounds(t *testing.T) {
	// A malformed or out-of-range value falls back to the default rather than
	// being passed to SQL as an unbounded LIMIT/OFFSET.
	if got := parseQueryInt("abc", 50, 1, 200); got != 50 {
		t.Errorf("got %d, want default 50", got)
	}
	if got := parseQueryInt("1000", 50, 1, 200); got != 50 {
		t.Errorf("got %d, want default 50 for out-of-range", got)
	}
	if got := parseQueryInt("25", 50, 1, 200); got != 25 {
		t.Errorf("got %d, want 25", got)
	}
}
