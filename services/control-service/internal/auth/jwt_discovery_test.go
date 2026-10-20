package auth

import (
	"reflect"
	"testing"
	"time"
)

func TestVerifierConfigurationUsesSignerIssuerAndAlgorithm(t *testing.T) {
	for _, issuer := range []string{"https://issuer.example", "http://localhost:8080", "https://issuer.example/aep/"} {
		t.Run(issuer, func(t *testing.T) {
			signer, err := NewService(issuer, "", time.Minute, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			configuration, err := signer.VerifierConfiguration()
			if err != nil || configuration.Issuer != issuer || !reflect.DeepEqual(configuration.SigningAlgorithms, []string{"EdDSA"}) {
				t.Fatalf("invalid public metadata: %#v, %v", configuration, err)
			}
			_, model, err := signer.IssueWithDeploymentSession("user", "deployment", "session", false, false, nil, []string{"model"})
			if err != nil {
				t.Fatal("cannot issue test model token")
			}
			claims, err := signer.ParseModel(model)
			if err != nil || claims.Issuer != configuration.Issuer {
				t.Fatal("discovery and signed token issuers differ")
			}
		})
	}
}

func TestVerifierConfigurationRejectsUnpublishableIssuer(t *testing.T) {
	for _, issuer := range []string{"", "/relative", "ftp://issuer.example", "https://user:private@issuer.example",
		"https://issuer.example?private=value", "https://issuer.example?", "https://issuer.example#fragment", "https://issuer.example#"} {
		signer, err := NewService(issuer, "", time.Minute, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := signer.VerifierConfiguration(); err == nil {
			t.Fatal("unpublishable issuer accepted")
		}
	}
	var absent *Service
	if _, err := absent.VerifierConfiguration(); err == nil {
		t.Fatal("absent signer accepted")
	}
}
