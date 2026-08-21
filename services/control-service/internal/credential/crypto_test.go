package credential

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSealerRoundTripAndAuthentication(t *testing.T) {
	provider, enabled, err := NewProvider(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")), "")
	if err != nil || !enabled {
		t.Fatalf("NewProvider() = %v, %v", enabled, err)
	}
	sealer := NewSealer(provider)
	envelope, err := sealer.Seal(context.Background(), []byte("provider-secret"), AssociatedData("tenant-a", "credential-a"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := sealer.Open(context.Background(), envelope, AssociatedData("tenant-a", "credential-a"))
	if err != nil || string(plaintext) != "provider-secret" {
		t.Fatalf("Open() = %q, %v", plaintext, err)
	}
	if _, err := sealer.Open(context.Background(), envelope, AssociatedData("tenant-b", "credential-a")); err == nil {
		t.Fatal("Open() accepted ciphertext under a different tenant")
	}
	envelope.Ciphertext[0] ^= 0xff
	if _, err := sealer.Open(context.Background(), envelope, AssociatedData("tenant-a", "credential-a")); err == nil {
		t.Fatal("Open() accepted tampered ciphertext")
	}
}

func TestFileKeyringRetainsPreviousKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-keys.json")
	first := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	second := base64.StdEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyzABCDEF"))
	writeKeyring := func(active string) {
		t.Helper()
		content := `{"activeKeyId":"` + active + `","keys":{"first":"` + first + `","second":"` + second + `"}}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeKeyring("first")
	provider, enabled, err := NewProvider("", path)
	if err != nil || !enabled {
		t.Fatalf("NewProvider() = %v, %v", enabled, err)
	}
	sealer := NewSealer(provider)
	envelope, err := sealer.Seal(context.Background(), []byte("secret"), []byte("aad"))
	if err != nil || envelope.KeyID != "first" {
		t.Fatalf("Seal() key = %q, %v", envelope.KeyID, err)
	}
	writeKeyring("second")
	if plaintext, err := sealer.Open(context.Background(), envelope, []byte("aad")); err != nil || string(plaintext) != "secret" {
		t.Fatalf("Open() after rotation = %q, %v", plaintext, err)
	}
	rotated, err := sealer.Seal(context.Background(), []byte("new-secret"), []byte("aad"))
	if err != nil || rotated.KeyID != "second" {
		t.Fatalf("Seal() after rotation key = %q, %v", rotated.KeyID, err)
	}
}

func TestProviderValidationAndMasking(t *testing.T) {
	if provider, enabled, err := NewProvider("", ""); err != nil || enabled || provider != nil {
		t.Fatalf("disabled provider = %#v, %v, %v", provider, enabled, err)
	}
	_, _, err := NewProvider(base64.StdEncoding.EncodeToString([]byte("short")), "")
	if err == nil {
		t.Fatal("NewProvider() accepted a short key")
	}
	_, _, err = NewProvider(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")), "keys.json")
	if err == nil {
		t.Fatal("NewProvider() accepted two key sources")
	}
	_, err = (&staticProvider{key: MasterKey{ID: "one", Bytes: make([]byte, 32)}}).ByID(context.Background(), "two")
	if !errors.Is(err, ErrKeyUnavailable) {
		t.Fatal("ByID() did not return ErrKeyUnavailable")
	}
	if got := Mask("abcdef"); got != "****cdef" {
		t.Fatalf("Mask() = %q", got)
	}
	if got := Mask("abc"); got != "****" {
		t.Fatalf("Mask(short) = %q", got)
	}
}
