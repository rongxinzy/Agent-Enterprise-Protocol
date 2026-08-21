package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const maxJWKSBytes = 1 << 20

type ModelClaims struct {
	Tenant      string   `json:"tenant"`
	AgentID     string   `json:"agent_id"`
	ModelScopes []string `json:"model_scopes"`
	TokenUse    string   `json:"token_use"`
	jwt.RegisteredClaims
}

type Verifier struct {
	url    string
	issuer string
	ttl    time.Duration
	client *http.Client

	mu        sync.RWMutex
	keys      map[string]ed25519.PublicKey
	fetchedAt time.Time
}

func NewVerifier(url, issuer string, ttl, timeout time.Duration) *Verifier {
	return &Verifier{
		url: url, issuer: issuer, ttl: ttl,
		client: &http.Client{Timeout: timeout},
		keys:   make(map[string]ed25519.PublicKey),
	}
}

func (v *Verifier) Verify(ctx context.Context, raw string) (*ModelClaims, error) {
	claims := &ModelClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing algorithm %q", token.Method.Alg())
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token kid is required")
		}
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithAudience("model-gateway"),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil || !token.Valid {
		return nil, errors.New("model token is invalid or expired")
	}
	if claims.TokenUse != "model" || claims.Subject == "" || claims.Tenant == "" || claims.AgentID == "" {
		return nil, errors.New("model token has invalid AEP claims")
	}
	return claims, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	v.mu.RLock()
	key, found := v.keys[kid]
	fresh := time.Since(v.fetchedAt) < v.ttl
	v.mu.RUnlock()
	if found && fresh {
		return key, nil
	}
	if err := v.refresh(ctx, !found); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	key, found = v.keys[kid]
	if !found {
		return nil, errors.New("token kid is not present in the trusted JWKS")
	}
	return key, nil
}

func (v *Verifier) refresh(ctx context.Context, force bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !force && !v.fetchedAt.IsZero() && time.Since(v.fetchedAt) < v.ttl {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return fmt.Errorf("create JWKS request: %w", err)
	}
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: unexpected status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxJWKSBytes+1))
	if err != nil {
		return fmt.Errorf("read JWKS: %w", err)
	}
	if len(data) > maxJWKSBytes {
		return errors.New("JWKS response is too large")
	}
	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Crv string `json:"crv"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}
	keys := make(map[string]ed25519.PublicKey)
	for _, item := range document.Keys {
		if item.Kty != "OKP" || item.Crv != "Ed25519" || item.Alg != "EdDSA" || item.Use != "sig" || item.Kid == "" {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(item.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			continue
		}
		keys[item.Kid] = ed25519.PublicKey(decoded)
	}
	if len(keys) == 0 {
		return errors.New("JWKS contains no usable Ed25519 signing keys")
	}
	v.keys = keys
	v.fetchedAt = time.Now()
	return nil
}
