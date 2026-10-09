package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

type gatewayIdentity struct {
	DeploymentID string    `json:"deploymentId"`
	UserID       string    `json:"userId"`
	Consumer     string    `json:"consumer"`
	Roles        []string  `json:"roleIds"`
	Teams        []string  `json:"teamIds"`
	ObservedAt   time.Time `json:"observedAt"`
}

func (h *Handler) identity(ctx context.Context, claims *ModelClaims, model string) (gatewayIdentity, error) {
	identity := gatewayIdentity{DeploymentID: claims.DeploymentID, UserID: claims.Subject, Consumer: gatewaysource.Consumer(claims.DeploymentID, claims.Subject)}
	if h.identityURL == "" {
		return identity, nil
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, h.identityURL, nil)
	if err != nil {
		return gatewayIdentity{}, err
	}
	r.Header.Set("X-AEP-Gateway-Token", h.identityToken)
	r.Header.Set("X-AEP-Deployment-ID", claims.DeploymentID)
	r.Header.Set("X-AEP-User-ID", claims.Subject)
	r.Header.Set("X-AEP-Session-ID", claims.SessionID)
	r.Header.Set("X-AEP-Model-ID", model)
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		return gatewayIdentity{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusForbidden {
		return gatewayIdentity{}, ErrEntitlementInactive
	}
	if response.StatusCode != http.StatusOK {
		return gatewayIdentity{}, errors.New("identity source unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if err != nil || len(data) > 32<<10 {
		return gatewayIdentity{}, errors.New("invalid identity source")
	}
	var result gatewayIdentity
	if json.Unmarshal(data, &result) != nil || result.DeploymentID != identity.DeploymentID || result.UserID != identity.UserID || result.Consumer != identity.Consumer || result.ObservedAt.Before(time.Now().Add(-time.Minute)) || result.ObservedAt.After(time.Now().Add(time.Minute)) || len(result.Teams) > 100 || len(result.Roles) > 100 {
		return gatewayIdentity{}, errors.New("invalid identity source")
	}
	if _, err := membershipHeader(result.Teams); err != nil {
		return gatewayIdentity{}, err
	}
	if _, err := membershipHeader(result.Roles); err != nil {
		return gatewayIdentity{}, err
	}
	return result, nil
}

func membershipHeader(ids []string) (string, error) {
	ids = append([]string(nil), ids...)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 256 {
			return "", errors.New("invalid membership")
		}
		parts = append(parts, base64.RawURLEncoding.EncodeToString([]byte(id)))
	}
	return "|" + strings.Join(parts, "|") + "|", nil
}
