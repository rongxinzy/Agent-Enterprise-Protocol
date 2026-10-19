package auth

import (
	"errors"
	"net/url"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// VerifierConfiguration is verification-only discovery metadata, not a full
// OpenID Provider configuration or an advertisement of ID-token issuance.
type VerifierConfiguration struct {
	Issuer            string   `json:"issuer"`
	JWKSURI           string   `json:"jwks_uri"`
	SigningAlgorithms []string `json:"id_token_signing_alg_values_supported"`
}

// VerifierConfiguration derives public metadata from the same issuer and
// algorithm used to sign tokens. Request headers and business state are unused.
func (s *Service) VerifierConfiguration() (VerifierConfiguration, error) {
	if s == nil {
		return VerifierConfiguration{}, errors.New("JWT verifier discovery unavailable")
	}
	issuer, err := url.Parse(s.issuer)
	if err != nil || issuer.Host == "" || (issuer.Scheme != "http" && issuer.Scheme != "https") ||
		issuer.User != nil || strings.ContainsAny(s.issuer, "?#") {
		return VerifierConfiguration{}, errors.New("JWT verifier discovery unavailable")
	}
	return VerifierConfiguration{
		Issuer: s.issuer, JWKSURI: strings.TrimRight(s.issuer, "/") + "/.well-known/jwks.json",
		SigningAlgorithms: []string{jwt.SigningMethodEdDSA.Alg()},
	}, nil
}
