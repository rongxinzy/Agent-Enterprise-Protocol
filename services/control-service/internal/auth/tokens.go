package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type Claims struct {
	Tenant                 string   `json:"tenant"`
	AgentID                string   `json:"agent_id"`
	LicenseID              string   `json:"license_id,omitempty"`
	LicenseDigest          string   `json:"license_digest,omitempty"`
	DeploymentID           string   `json:"deployment_id,omitempty"`
	Features               []string `json:"features,omitempty"`
	Admin                  bool     `json:"admin,omitempty"`
	Roles                  []string `json:"roles,omitempty"`
	ModelScopes            []string `json:"model_scopes,omitempty"`
	PasswordChangeRequired bool     `json:"password_change_required,omitempty"`
	TokenUse               string   `json:"token_use"`
	jwt.RegisteredClaims
}

type Service struct {
	issuer    string
	keyID     string
	private   ed25519.PrivateKey
	public    ed25519.PublicKey
	accessTTL time.Duration
	modelTTL  time.Duration
}

func NewService(issuer, encodedSeed string, accessTTL, modelTTL time.Duration) (*Service, error) {
	var private ed25519.PrivateKey
	if encodedSeed == "" {
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		private = generated
	} else {
		seed, err := base64.StdEncoding.DecodeString(encodedSeed)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("AEP_SIGNING_KEY_BASE64 must contain a base64-encoded 32-byte Ed25519 seed")
		}
		private = ed25519.NewKeyFromSeed(seed)
	}
	public := private.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(public)
	return &Service{issuer: issuer, keyID: base64.RawURLEncoding.EncodeToString(digest[:8]), private: private, public: public, accessTTL: accessTTL, modelTTL: modelTTL}, nil
}

func (s *Service) Issue(userID, enterpriseID, agentID string, admin, passwordChangeRequired bool, roles []string, modelScopes []string) (string, string, error) {
	access, err := s.sign(userID, enterpriseID, agentID, admin, passwordChangeRequired, roles, nil, "aep", "aep-control", s.accessTTL)
	if err != nil {
		return "", "", err
	}
	model, err := s.sign(userID, enterpriseID, agentID, false, passwordChangeRequired, nil, modelScopes, "model", "model-gateway", s.modelTTL)
	return access, model, err
}

func (s *Service) ParseAccess(raw string) (*Claims, error) {
	return s.parse(raw, "aep-control", "aep")
}

func (s *Service) ParseModel(raw string) (*Claims, error) {
	return s.parse(raw, "model-gateway", "model")
}

func (s *Service) IssueEntitlement(userID, enterpriseID, agentID, deploymentID, licenseID, licenseDigest string, features, modelScopes []string, licenseExpiresAt time.Time) (string, time.Time, error) {
	now := time.Now().UTC()
	if !licenseExpiresAt.After(now) || deploymentID == "" || licenseID == "" || licenseDigest == "" {
		return "", time.Time{}, errors.New("invalid enterprise license activation")
	}
	// Entitlements are deliberately short-lived. The license expiry remains the
	// upper bound, while refresh requires another locally verified activation.
	expiresAt := licenseExpiresAt.UTC()
	if maximum := now.Add(24 * time.Hour); expiresAt.After(maximum) {
		expiresAt = maximum
	}
	claims := Claims{
		Tenant: enterpriseID, AgentID: agentID, DeploymentID: deploymentID, LicenseID: licenseID, LicenseDigest: licenseDigest,
		Features: append([]string(nil), features...), ModelScopes: append([]string(nil), modelScopes...), TokenUse: "entitlement",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: s.issuer, Subject: userID, Audience: jwt.ClaimStrings{"aep-entitlement"},
			ExpiresAt: jwt.NewNumericDate(expiresAt), IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = s.keyID
	raw, err := token.SignedString(s.private)
	return raw, expiresAt, err
}

func (s *Service) ParseEntitlement(raw string) (*Claims, error) {
	return s.parse(raw, "aep-entitlement", "entitlement")
}

func (s *Service) parse(raw, audience, tokenUse string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
			return nil, fmt.Errorf("unexpected signing algorithm %s", token.Method.Alg())
		}
		return s.public, nil
	}, jwt.WithAudience(audience), jwt.WithIssuer(s.issuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid || claims.TokenUse != tokenUse {
		return nil, errors.New("invalid token use")
	}
	return claims, nil
}

func (s *Service) JWKS() map[string]any {
	return map[string]any{"keys": []map[string]string{{"kty": "OKP", "kid": s.keyID, "use": "sig", "alg": "EdDSA", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(s.public)}}}
}

func (s *Service) sign(userID, enterpriseID, agentID string, admin, passwordChangeRequired bool, roles, modelScopes []string, tokenUse, audience string, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := Claims{
		Tenant: enterpriseID, AgentID: agentID, Admin: admin, Roles: roles, ModelScopes: modelScopes, PasswordChangeRequired: passwordChangeRequired, TokenUse: tokenUse,
		RegisteredClaims: jwt.RegisteredClaims{Issuer: s.issuer, Subject: userID, Audience: jwt.ClaimStrings{audience}, ExpiresAt: jwt.NewNumericDate(now.Add(ttl)), IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString()},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = s.keyID
	return token.SignedString(s.private)
}

func NewRefreshToken() (string, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	raw := base64.RawURLEncoding.EncodeToString(bytes)
	return raw, HashRefreshToken(raw), nil
}

func HashRefreshToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
