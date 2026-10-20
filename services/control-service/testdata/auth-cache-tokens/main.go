// Disposable component-test issuer. Stdout is captured privately by the probe;
// never run this helper with stdout connected to logs or a terminal.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/httpapi"
)

func main() {
	var options struct {
		ProductDiscovery bool `json:"productDiscovery"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&options); err != nil && err != io.EOF {
		panic("invalid disposable probe options")
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		panic("cannot create disposable signing material")
	}
	signer, err := auth.NewService("http://introspection:8080/oidc", base64.StdEncoding.EncodeToString(seed), 10*time.Minute, 10*time.Minute)
	if err != nil {
		panic("cannot create disposable issuer")
	}
	access, model, err := signer.IssueWithDeploymentSession("alice", "probe", "session-a", false, false, nil, []string{"model-a"})
	if err != nil {
		panic("cannot issue disposable session")
	}
	claims, err := signer.ParseModel(model)
	if err != nil {
		panic("cannot parse disposable session")
	}
	kid := signer.JWKS()["keys"].([]map[string]string)[0]["kid"]
	sign := func(c auth.Claims, key ed25519.PrivateKey) string {
		token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
		token.Header["kid"] = kid
		raw, err := token.SignedString(key)
		if err != nil {
			panic("cannot sign disposable boundary fixture")
		}
		return raw
	}
	tokens := map[string]string{"model": model, "access": access}
	mutations := map[string]func(*auth.Claims){
		"issuer":          func(c *auth.Claims) { c.Issuer = "other-issuer" },
		"audience":        func(c *auth.Claims) { c.Audience = jwt.ClaimStrings{"aep-control"} },
		"use":             func(c *auth.Claims) { c.TokenUse = "aep" },
		"deployment":      func(c *auth.Claims) { c.DeploymentID = "other-deployment" },
		"subject":         func(c *auth.Claims) { c.Subject = "bob" },
		"session":         func(c *auth.Claims) { c.SessionID = "revoked-session" },
		"missing-session": func(c *auth.Claims) { c.SessionID = "" },
		"scope":           func(c *auth.Claims) { c.ModelScopes = []string{"model-b"} },
		"password":        func(c *auth.Claims) { c.PasswordChangeRequired = true },
		"expired":         func(c *auth.Claims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute)) },
		"missing-exp":     func(c *auth.Claims) { c.ExpiresAt = nil },
		"future":          func(c *auth.Claims) { c.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour)) },
		"short":           func(c *auth.Claims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(40 * time.Second)) },
	}
	for name, mutate := range mutations {
		copy := *claims
		mutate(&copy)
		tokens[name] = sign(copy, ed25519.NewKeyFromSeed(seed))
	}
	_, foreign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("cannot create disposable foreign signer")
	}
	tokens["signature"] = sign(*claims, foreign)
	result := map[string]any{"jwks": signer.JWKS(), "tokens": tokens}
	if options.ProductDiscovery {
		handler := httpapi.NewEnterpriseAPI(&app.App{Tokens: signer}).Handler()
		for name, route := range map[string]string{"discovery": "/.well-known/openid-configuration", "jwks": "/.well-known/jwks.json"} {
			var resultValue any
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &resultValue) != nil {
				panic("required public verification endpoint unavailable")
			}
			result[name] = resultValue
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		panic("cannot deliver private fixture")
	}
}
