package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

var ErrKeyUnavailable = errors.New("credential master key is unavailable")

type MasterKey struct {
	ID    string
	Bytes []byte
}

type MasterKeyProvider interface {
	Active(context.Context) (MasterKey, error)
	ByID(context.Context, string) (MasterKey, error)
}

type Envelope struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

type Sealer struct {
	provider MasterKeyProvider
}

func NewSealer(provider MasterKeyProvider) *Sealer {
	return &Sealer{provider: provider}
}

func (s *Sealer) Seal(ctx context.Context, plaintext, associatedData []byte) (Envelope, error) {
	key, err := s.provider.Active(ctx)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := newAEAD(key.Bytes)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("generate credential nonce: %w", err)
	}
	return Envelope{KeyID: key.ID, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, associatedData)}, nil
}

func (s *Sealer) Open(ctx context.Context, envelope Envelope, associatedData []byte) ([]byte, error) {
	key, err := s.provider.ByID(ctx, envelope.KeyID)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key.Bytes)
	if err != nil {
		return nil, err
	}
	if len(envelope.Nonce) != aead.NonceSize() {
		return nil, errors.New("credential nonce has an invalid size")
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, associatedData)
	if err != nil {
		return nil, errors.New("credential ciphertext authentication failed")
	}
	return plaintext, nil
}

func AssociatedData(enterpriseID, credentialID string) []byte {
	return []byte(enterpriseID + "\x00" + credentialID)
}

func Mask(value string) string {
	runes := []rune(value)
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

func NewProvider(encodedKey, keyFile string) (MasterKeyProvider, bool, error) {
	if strings.TrimSpace(encodedKey) != "" && strings.TrimSpace(keyFile) != "" {
		return nil, false, errors.New("configure only one credential master key source")
	}
	if strings.TrimSpace(encodedKey) != "" {
		key, err := decodeKey("", encodedKey)
		if err != nil {
			return nil, false, fmt.Errorf("decode AEP_CREDENTIAL_MASTER_KEY_BASE64: %w", err)
		}
		return &staticProvider{key: key}, true, nil
	}
	if strings.TrimSpace(keyFile) != "" {
		provider := &fileProvider{path: keyFile}
		if _, err := provider.Active(context.Background()); err != nil {
			return nil, false, fmt.Errorf("load AEP_CREDENTIAL_MASTER_KEY_FILE: %w", err)
		}
		return provider, true, nil
	}
	return nil, false, nil
}

type staticProvider struct {
	key MasterKey
}

func (p *staticProvider) Active(context.Context) (MasterKey, error) {
	return cloneKey(p.key), nil
}

func (p *staticProvider) ByID(_ context.Context, id string) (MasterKey, error) {
	if id != p.key.ID {
		return MasterKey{}, ErrKeyUnavailable
	}
	return cloneKey(p.key), nil
}

type fileProvider struct {
	path string
}

type keyringFile struct {
	ActiveKeyID string            `json:"activeKeyId"`
	Keys        map[string]string `json:"keys"`
}

func (p *fileProvider) Active(_ context.Context) (MasterKey, error) {
	ring, err := p.load()
	if err != nil {
		return MasterKey{}, err
	}
	key, ok := ring.keys[ring.active]
	if !ok {
		return MasterKey{}, ErrKeyUnavailable
	}
	return cloneKey(key), nil
}

func (p *fileProvider) ByID(_ context.Context, id string) (MasterKey, error) {
	ring, err := p.load()
	if err != nil {
		return MasterKey{}, err
	}
	key, ok := ring.keys[id]
	if !ok {
		return MasterKey{}, ErrKeyUnavailable
	}
	return cloneKey(key), nil
}

type loadedKeyring struct {
	active string
	keys   map[string]MasterKey
}

func (p *fileProvider) load() (loadedKeyring, error) {
	content, err := os.ReadFile(p.path)
	if err != nil {
		return loadedKeyring{}, err
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return loadedKeyring{}, errors.New("credential key file is empty")
	}
	if !strings.HasPrefix(trimmed, "{") {
		key, err := decodeKey("", trimmed)
		if err != nil {
			return loadedKeyring{}, err
		}
		return loadedKeyring{active: key.ID, keys: map[string]MasterKey{key.ID: key}}, nil
	}
	var document keyringFile
	if err := json.Unmarshal([]byte(trimmed), &document); err != nil {
		return loadedKeyring{}, fmt.Errorf("decode credential keyring: %w", err)
	}
	if document.ActiveKeyID == "" || len(document.Keys) == 0 {
		return loadedKeyring{}, errors.New("credential keyring requires activeKeyId and keys")
	}
	loaded := loadedKeyring{active: document.ActiveKeyID, keys: make(map[string]MasterKey, len(document.Keys))}
	for id, encoded := range document.Keys {
		key, err := decodeKey(id, encoded)
		if err != nil {
			return loadedKeyring{}, fmt.Errorf("decode credential key %q: %w", id, err)
		}
		loaded.keys[id] = key
	}
	if _, ok := loaded.keys[loaded.active]; !ok {
		return loadedKeyring{}, errors.New("credential keyring activeKeyId is not present in keys")
	}
	return loaded, nil
}

func decodeKey(id, encoded string) (MasterKey, error) {
	value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return MasterKey{}, err
	}
	if len(value) != 32 {
		return MasterKey{}, fmt.Errorf("AES-256 key must be exactly 32 bytes, got %d", len(value))
	}
	if id == "" {
		digest := sha256.Sum256(value)
		id = "sha256:" + hex.EncodeToString(digest[:8])
	}
	return MasterKey{ID: id, Bytes: value}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("credential master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func cloneKey(key MasterKey) MasterKey {
	return MasterKey{ID: key.ID, Bytes: append([]byte(nil), key.Bytes...)}
}
