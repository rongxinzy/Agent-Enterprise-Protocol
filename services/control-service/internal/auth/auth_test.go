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

func TestPasswordPolicyAndDummyVerification(t *testing.T) {
	for _, password := range []string{"short", string(make([]byte, 1025))} {
		if err := ValidatePassword(password); err == nil {
			t.Fatalf("ValidatePassword(%d characters) succeeded", len(password))
		}
		if _, err := HashPassword(password); err == nil {
			t.Fatalf("HashPassword(%d characters) succeeded", len(password))
		}
	}
	if err := ValidatePassword("十二个字符的安全密码测试值"); err != nil {
		t.Fatalf("Unicode password was rejected: %v", err)
	}
	if VerifyPasswordOrDummy("", "not-a-real-password") {
		t.Fatal("dummy password verification returned true")
	}
}

func TestAccessAndModelClaims(t *testing.T) {
	service, err := NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, model, err := service.Issue("user-1", "tenant-1", "agent-1", true, false, []string{"admin"}, []string{"model-a"})
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

func TestPasswordChangeRequiredClaim(t *testing.T) {
	service, err := NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, model, err := service.Issue("user-1", "tenant-1", "agent-1", false, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, parse := range map[string]func(string) (*Claims, error){"access": service.ParseAccess, "model": service.ParseModel} {
		claims, err := parse(map[string]string{"access": access, "model": model}[name])
		if err != nil || !claims.PasswordChangeRequired {
			t.Fatalf("%s token omitted password-change claim: %#v, %v", name, claims, err)
		}
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
