package license

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"
)

const formatV1 = "zhiyuan-license-v1"

var timestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

type Limits struct {
	Users         int `json:"users"`
	Agents        int `json:"agents"`
	Organizations int `json:"organizations,omitempty"`
}

type Claims struct {
	LicenseID        string   `json:"licenseId"`
	CustomerID       string   `json:"customerId"`
	DeploymentID     string   `json:"deploymentId"`
	Edition          string   `json:"edition"`
	IssuedAt         string   `json:"issuedAt"`
	NotBefore        string   `json:"notBefore,omitempty"`
	ExpiresAt        string   `json:"expiresAt"`
	MaintenanceUntil string   `json:"maintenanceUntil,omitempty"`
	GraceDays        int      `json:"graceDays"`
	Limits           Limits   `json:"limits"`
	Features         []string `json:"features"`
}

type Envelope struct {
	Format    string          `json:"format"`
	KeyID     string          `json:"keyId"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

type Verified struct {
	Envelope    Envelope
	Claims      Claims
	Digest      string
	Status      string
	GraceEndsAt time.Time
}

type Verifier struct {
	TrustedKeys        map[string]ed25519.PublicKey
	ExpectedDeployment string
	Now                func() time.Time
}

func NewVerifier(encoded map[string]string, expectedDeployment string) (*Verifier, error) {
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for keyID, value := range encoded {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid Ed25519 public key %q", keyID)
		}
		keys[keyID] = ed25519.PublicKey(decoded)
	}
	return &Verifier{TrustedKeys: keys, ExpectedDeployment: expectedDeployment}, nil
}

func (v Verifier) Verify(raw []byte) (Verified, error) {
	var envelope Envelope
	if len(raw) == 0 || decodeStrict(raw, &envelope) != nil || envelope.Format != formatV1 || envelope.KeyID == "" || len(envelope.Payload) == 0 || envelope.Signature == "" {
		return Verified{}, errors.New("malformed license envelope")
	}
	publicKey, ok := v.TrustedKeys[envelope.KeyID]
	if !ok {
		return Verified{}, errors.New("unknown license signing key")
	}
	var claims Claims
	if err := decodeStrict(envelope.Payload, &claims); err != nil || !validClaims(claims) {
		return Verified{}, errors.New("malformed license claims")
	}
	canonicalPayload, err := canonicalizeJSON(envelope.Payload)
	if err != nil || !ed25519.Verify(publicKey, []byte(canonicalPayload), decodeSignature(envelope.Signature)) {
		return Verified{}, errors.New("invalid license signature")
	}
	if v.ExpectedDeployment != "" && claims.DeploymentID != v.ExpectedDeployment {
		return Verified{}, errors.New("license deployment mismatch")
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	notBefore, _ := time.Parse(time.RFC3339Nano, claims.IssuedAt)
	if claims.NotBefore != "" {
		notBefore, _ = time.Parse(time.RFC3339Nano, claims.NotBefore)
	}
	expiresAt, _ := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	graceEnds := expiresAt.Add(time.Duration(claims.GraceDays) * 24 * time.Hour)
	if now.Before(notBefore) {
		return Verified{}, errors.New("license is not yet valid")
	}
	status := "enterprise-expired"
	if !now.After(expiresAt) {
		status = "enterprise-active"
	} else if !now.After(graceEnds) {
		status = "enterprise-grace"
	}
	digest, err := envelopeDigest(envelope, claims)
	if err != nil {
		return Verified{}, err
	}
	return Verified{Envelope: envelope, Claims: claims, Digest: digest, Status: status, GraceEndsAt: graceEnds}, nil
}

func validClaims(c Claims) bool {
	if c.LicenseID == "" || c.CustomerID == "" || c.DeploymentID == "" || c.Edition != "enterprise" || !validTimestamp(c.IssuedAt) || !validTimestamp(c.ExpiresAt) || c.GraceDays < 0 || c.Limits.Users <= 0 || c.Limits.Agents <= 0 || (c.Limits.Organizations < 0) {
		return false
	}
	if c.NotBefore != "" && !validTimestamp(c.NotBefore) || c.MaintenanceUntil != "" && !validTimestamp(c.MaintenanceUntil) {
		return false
	}
	issued, _ := time.Parse(time.RFC3339Nano, c.IssuedAt)
	expires, _ := time.Parse(time.RFC3339Nano, c.ExpiresAt)
	if expires.Before(issued) || len(c.Features) > 256 {
		return false
	}
	for _, feature := range c.Features {
		if feature == "" {
			return false
		}
	}
	return true
}

func validTimestamp(value string) bool {
	if !timestampPattern.MatchString(value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func decodeSignature(value string) []byte {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return nil
	}
	return decoded
}

func envelopeDigest(envelope Envelope, claims Claims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	value := map[string]any{"format": envelope.Format, "keyId": envelope.KeyID, "payload": json.RawMessage(payload), "signature": envelope.Signature}
	canonical, err := canonicalizeValue(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalizeJSON(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	return canonicalizeValue(value)
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func canonicalizeValue(value any) (string, error) {
	switch current := value.(type) {
	case nil:
		return "null", nil
	case string, bool:
		encoded, _ := json.Marshal(current)
		return string(encoded), nil
	case json.Number:
		if current.String() == "" {
			return "", errors.New("empty JSON number")
		}
		return current.String(), nil
	case json.RawMessage:
		return canonicalizeJSON(current)
	case []any:
		parts := make([]string, len(current))
		for i, item := range current {
			part, err := canonicalizeValue(item)
			if err != nil {
				return "", err
			}
			parts[i] = part
		}
		return "[" + join(parts) + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			part, err := canonicalizeValue(current[key])
			if err != nil {
				return "", err
			}
			encodedKey, _ := json.Marshal(key)
			parts = append(parts, string(encodedKey)+":"+part)
		}
		return "{" + join(parts) + "}", nil
	default:
		return "", fmt.Errorf("unsupported JSON value %T", value)
	}
}

func join(values []string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += ","
		}
		result += value
	}
	return result
}
