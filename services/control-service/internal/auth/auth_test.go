package auth

import (
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("wrong password verified")
	}
}

func TestAccessAndModelClaims(t *testing.T) {
	service, err := NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, model, err := service.Issue("user-1", "tenant-1", "agent-1", true, []string{"admin"}, []string{"model-a"})
	if err != nil || model == "" {
		t.Fatalf("issue failed: %v", err)
	}
	claims, err := service.ParseAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Tenant != "tenant-1" || claims.AgentID != "agent-1" || !claims.Admin {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	modelClaims, err := service.ParseModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if modelClaims.Tenant != "tenant-1" || modelClaims.AgentID != "agent-1" || len(modelClaims.ModelScopes) != 1 || modelClaims.ModelScopes[0] != "model-a" {
		t.Fatalf("unexpected model claims: %#v", modelClaims)
	}
	if _, err := service.ParseAccess(model); err == nil {
		t.Fatal("model token was accepted as an AEP access token")
	}
	if _, err := service.ParseModel(access); err == nil {
		t.Fatal("AEP access token was accepted as a model token")
	}
}

func TestRefreshTokenHashesAreStableAndUnique(t *testing.T) {
	first, firstHash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || firstHash == secondHash || HashRefreshToken(first) != firstHash {
		t.Fatal("refresh token generation invariants failed")
	}
}
