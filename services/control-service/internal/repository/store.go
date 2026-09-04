package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var (
	ErrNotFound          = gorm.ErrRecordNotFound
	ErrUnknownPermission = errors.New("unknown permission")
	ErrUnknownRole       = errors.New("unknown role")
	ErrUnknownTeam       = errors.New("unknown team")
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Deployment(deploymentID string) *DeploymentStore {
	return &DeploymentStore{db: s.db, deploymentID: deploymentID}
}

func (s *Store) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	var deployment Deployment
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&deployment).Error
	return deployment, err
}

func (s *Store) UpsertDeployment(ctx context.Context, deployment Deployment) (Deployment, error) {
	err := s.db.WithContext(ctx).Exec(`
INSERT INTO deployments (id, name) VALUES (?, ?)
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name`, deployment.ID, deployment.Name).Error
	if err != nil {
		return Deployment{}, err
	}
	return s.GetDeployment(ctx, deployment.ID)
}

type DeploymentStore struct {
	db           *gorm.DB
	deploymentID string
}

func (s *DeploymentStore) transaction(ctx context.Context, operation func(*DeploymentStore) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return operation(&DeploymentStore{db: tx, deploymentID: s.deploymentID})
	})
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func resultError(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
