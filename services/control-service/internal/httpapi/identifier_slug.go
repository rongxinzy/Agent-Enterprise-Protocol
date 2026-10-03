package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// slugFor derives a resource identifier from a human name: lowercase, keep
// [a-z0-9-], everything else collapses to a single dash. The alphabet matches
// the anthropic passthrough path slug so a generated model id renders cleanly
// (`/<model-id>` prefix) without further sanitization.
func slugFor(name string) string {
	var builder strings.Builder
	previousDash := true // leading dashes are dropped
	for _, character := range strings.ToLower(name) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			previousDash = false
		default:
			if !previousDash {
				builder.WriteByte('-')
				previousDash = true
			}
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return slug
}

// uniqueIdentifier returns the slug with a numeric suffix appended while the
// candidate already exists. Suffixes -2..-99 cover realistic collisions; past
// that a random hex tail keeps the identifier bounded and unique. The exists
// probe is caller-supplied so transactions scope it to their own snapshot.
func uniqueIdentifier(ctx context.Context, base, fallbackKind string, exists func(ctx context.Context, candidate string) (bool, error)) (string, error) {
	if base == "" {
		base = fallbackKind
	}
	if taken, err := exists(ctx, base); err != nil {
		return "", err
	} else if !taken {
		return base, nil
	}
	for suffix := 2; suffix < 100; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if taken, err := exists(ctx, candidate); err != nil {
			return "", err
		} else if !taken {
			return candidate, nil
		}
	}
	tail := make([]byte, 4)
	if _, err := rand.Read(tail); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(tail)), nil
}

// generateIdentifier derives a server-side identifier for one resource kind.
// Skills are deployment-global; every other kind is scoped to the tenant.
func (s *Server) generateIdentifier(ctx context.Context, tenant, kind, name string) (string, error) {
	return uniqueIdentifier(ctx, slugFor(name), kind, func(ctx context.Context, candidate string) (bool, error) {
		return s.identifierExists(ctx, tenant, kind, candidate)
	})
}

func (s *Server) identifierExists(ctx context.Context, tenant, kind, candidate string) (bool, error) {
	if kind == "skill" {
		if _, err := s.app.Store.GetSkill(ctx, candidate); err == nil {
			return true, nil
		} else if !errors.Is(err, repository.ErrNotFound) {
			return false, err
		}
		return false, nil
	}
	store := s.app.Store.Deployment(tenant)
	var err error
	switch kind {
	case "model":
		return store.HasModel(ctx, candidate)
	case "role":
		_, err = store.GetRoleRecord(ctx, candidate)
	case "team":
		_, err = store.GetTeamRecord(ctx, candidate)
	case "source":
		_, err = store.GetIdentitySource(ctx, candidate)
	case "data-scope-rule":
		_, err = store.GetDataScopeRule(ctx, candidate)
	default:
		return false, fmt.Errorf("unknown identifier kind %q", kind)
	}
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
