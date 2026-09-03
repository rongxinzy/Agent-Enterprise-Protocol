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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const maxJWKSBytes = 1 << 20

var ErrEntitlementInactive = errors.New("enterprise entitlement is inactive")

type ModelClaims struct {
	Tenant        string   `json:"tenant"`
	AgentID       string   `json:"agent_id"`
	LicenseID     string   `json:"license_id,omitempty"`
	LicenseDigest string   `json:"license_digest,omitempty"`
	DeploymentID  string   `json:"deployment_id,omitempty"`
	Features      []string `json:"features,omitempty"`
	ModelScopes   []string `json:"model_scopes"`
	TokenUse      string   `json:"token_use"`
	jwt.RegisteredClaims
}

type Verifier struct {
	url    string
	issuer string
	ttl    time.Duration
	client *http.Client

	refreshMu          sync.Mutex
	mu                 sync.RWMutex
	keys               map[string]ed25519.PublicKey
	fetchedAt          time.Time
	licenseStatusURL   string
	licenseStatusToken string
	licenseStatusTTL   time.Duration
	statusMu           sync.Mutex
	statusCache        map[string]statusCacheEntry
}

type statusCacheEntry struct {
	active    bool
	expiresAt time.Time
}

func NewVerifier(url, issuer string, ttl, timeout time.Duration) *Verifier {
	return &Verifier{
		url: url, issuer: issuer, ttl: ttl,
		client:      &http.Client{Timeout: timeout},
		keys:        make(map[string]ed25519.PublicKey),
		statusCache: make(map[string]statusCacheEntry),
	}
}

func (v *Verifier) ConfigureLicenseStatus(endpoint, token string, ttl time.Duration) {
	v.statusMu.Lock()
	defer v.statusMu.Unlock()
	v.licenseStatusURL, v.licenseStatusToken, v.licenseStatusTTL = strings.TrimRight(endpoint, "/"), token, ttl
	v.statusCache = make(map[string]statusCacheEntry)
}

func (v *Verifier) CheckEntitlement(ctx context.Context, claims *ModelClaims) error {
	if v.licenseStatusURL == "" || v.licenseStatusToken == "" {
		return errors.New("license status endpoint is not configured")
	}
	key := claims.LicenseID + "\x00" + claims.LicenseDigest + "\x00" + claims.DeploymentID
	now := time.Now()
	v.statusMu.Lock()
	if cached, ok := v.statusCache[key]; ok && now.Before(cached.expiresAt) {
		v.statusMu.Unlock()
		if !cached.active {
			return ErrEntitlementInactive
		}
		return nil
	}
	v.statusMu.Unlock()
	endpoint, err := url.JoinPath(v.licenseStatusURL, claims.LicenseID)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-AEP-Gateway-Token", v.licenseStatusToken)
	request.Header.Set("X-AEP-Tenant-ID", claims.Tenant)
	response, err := v.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("license status endpoint returned %d", response.StatusCode)
	}
	var document struct {
		Active       bool   `json:"active"`
		Digest       string `json:"digest"`
		DeploymentID string `json:"deploymentId"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&document); err != nil {
		return err
	}
	active := document.Active && document.Digest == claims.LicenseDigest && document.DeploymentID == claims.DeploymentID
	v.statusMu.Lock()
	v.statusCache[key] = statusCacheEntry{active: active, expiresAt: now.Add(v.licenseStatusTTL)}
	v.statusMu.Unlock()
	if !active {
		return ErrEntitlementInactive
	}
	return nil
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
		jwt.WithAudience("model-gateway", "aep-entitlement"),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil || !token.Valid {
		return nil, errors.New("model token is invalid or expired")
	}
	if (claims.TokenUse != "model" && claims.TokenUse != "entitlement") || claims.Subject == "" || claims.Tenant == "" || claims.AgentID == "" {
		return nil, errors.New("model token has invalid AEP claims")
	}
	if claims.TokenUse == "entitlement" && (claims.LicenseID == "" || claims.LicenseDigest == "" || claims.DeploymentID == "" || claims.Audience == nil || !containsAudience(claims.Audience, "aep-entitlement")) {
		return nil, errors.New("entitlement token has invalid AEP claims")
	}
	return claims, nil
}

func containsAudience(audience jwt.ClaimStrings, expected string) bool {
	for _, value := range audience {
		if value == expected {
			return true
		}
	}
	return false
}

func (v *Verifier) Ready(ctx context.Context) error {
	return v.refresh(ctx, true)
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
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	v.mu.RLock()
	fresh := !v.fetchedAt.IsZero() && time.Since(v.fetchedAt) < v.ttl
	v.mu.RUnlock()
	if !force && fresh {
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
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}
