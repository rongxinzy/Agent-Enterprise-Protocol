package auth

import (
	"testing"
	"time"
)

func TestModelTestTokenCannotAuthorizeOtherSurfaces(t *testing.T) {
	s, err := NewService("https://issuer.example", "", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, expires, err := s.IssueModelTestAccess("user", "deployment", "session", "model")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ParseModel(raw)
	if err != nil || !expires.Equal(claims.ExpiresAt.Time) || expires.After(time.Now().Add(2*time.Minute)) || claims.Admin || len(claims.ModelScopes) != 1 {
		t.Fatal("test token binding")
	}
	if _, err := s.ParseAccess(raw); err == nil {
		t.Fatal("model test token used as control access")
	}
	if _, _, err := s.IssueModelTestAccess("", "deployment", "session", "model"); err == nil {
		t.Fatal("unbound test token")
	}
}
