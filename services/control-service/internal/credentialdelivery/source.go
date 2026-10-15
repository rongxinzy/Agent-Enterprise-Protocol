package credentialdelivery

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// DatabaseSource needs SELECT on credentials and models only. It performs no
// migration, bootstrap, audit writes, or access to identity/session tables.
type DatabaseSource struct {
	DB         *gorm.DB
	Deployment string
}

func (s DatabaseSource) eligible(ctx context.Context) *gorm.DB {
	return s.DB.WithContext(ctx).Model(&repository.Credential{}).
		Where("credentials.deployment_id = ? AND credentials.enabled = ? AND credentials.delivery_mode = ? AND credentials.type = ?", s.Deployment, true, "server_only", "api_key").
		Where(`EXISTS (SELECT 1 FROM models m WHERE m.deployment_id = credentials.deployment_id
AND m.credential_id = credentials.id AND m.source_type = 'gateway' AND m.enabled = true
AND m.protocol IN ('openai-compatible', 'anthropic'))`)
}

func (s DatabaseSource) IDs(ctx context.Context, cursor string, limit int) ([]string, error) {
	ids := make([]string, 0)
	err := s.eligible(ctx).Where("credentials.id > ?", cursor).Order("credentials.id").Limit(limit).Pluck("credentials.id", &ids).Error
	return ids, err
}

func (s DatabaseSource) Read(ctx context.Context, id string) (credential.Envelope, bool, error) {
	var value repository.Credential
	err := s.eligible(ctx).Where("credentials.id = ?", id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return credential.Envelope{}, false, nil
	}
	if err != nil {
		return credential.Envelope{}, false, err
	}
	return credential.Envelope{KeyID: value.KeyID, Nonce: value.Nonce, Ciphertext: value.EncryptedValue}, true, nil
}
