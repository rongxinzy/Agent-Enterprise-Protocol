package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

// The native-gateway render functions (RenderGatewayLimits/RenderGatewayQuota)
// live in the shared gatewayrender package; this file keeps the reconciler's
// control-plane sync: fetch the publication, resolve secrets, render, apply,
// acknowledge.

func (r *Reconciler) syncGatewayLimits(ctx context.Context, desired DesiredState) (syncErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.config.ControlURL+"/internal/data-plane/gateway-limits", nil)
	if err != nil {
		return err
	}
	r.addHeaders(request, desired.TenantID())
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return errors.New("native gateway publication unavailable")
	}
	var publication gatewaypolicy.Publication
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&publication); err != nil {
		return err
	}
	if publication.Revision == "" && r.config.NativeGateway.QuotaAdminCredentialRef == nil {
		return nil
	}
	state := "error"
	defer func() {
		if publication.Revision == "" {
			return
		}
		body, _ := json.Marshal(map[string]string{"revision": publication.Revision, "state": state})
		ack, ackErr := http.NewRequestWithContext(ctx, http.MethodPut, r.config.ControlURL+"/internal/data-plane/gateway-limits/status", bytes.NewReader(body))
		if ackErr == nil {
			r.addHeaders(ack, desired.TenantID())
			ack.Header.Set("Content-Type", "application/json")
			result, sendErr := r.config.HTTPClient.Do(ack)
			if sendErr == nil {
				_ = result.Body.Close()
				if result.StatusCode != http.StatusOK && syncErr == nil {
					syncErr = errors.New("native gateway status acknowledgment failed")
				}
			} else if syncErr == nil {
				syncErr = errors.New("native gateway status acknowledgment unavailable")
			}
		}
	}()
	password := ""
	if ref := r.config.NativeGateway.RedisPasswordRef; ref != nil {
		if r.config.CredentialFetcher == nil {
			return errors.New("native Redis secret resolver unavailable")
		}
		password, err = r.config.CredentialFetcher(ctx, *ref)
		if err != nil || password == "" {
			return errors.New("native Redis secret unavailable")
		}
	}
	resources := make([]gatewayrender.RenderedResource, 0)
	input := gatewayrender.Input{Deployment: desired.TenantID(), Revision: desired.Revision, Routes: desired.Routes}
	if publication.Revision != "" {
		resources, err = gatewayrender.RenderGatewayLimits(input, publication, r.config.NativeGateway, password)
	}
	if err != nil {
		return err
	}
	if ref := r.config.NativeGateway.QuotaAdminCredentialRef; ref != nil {
		if r.config.CredentialFetcher == nil {
			return errors.New("native quota secret resolver unavailable")
		}
		credential, fetchErr := r.config.CredentialFetcher(ctx, *ref)
		if fetchErr != nil {
			return errors.New("native quota secret unavailable")
		}
		quota, renderErr := gatewayrender.RenderGatewayQuota(input, r.config.NativeGateway, password, credential)
		if renderErr != nil {
			return renderErr
		}
		resources = append(resources, quota...)
	}
	applier, ok := r.config.Applier.(interface {
		ApplyGatewayNative(context.Context, []gatewayrender.RenderedResource) error
	})
	if !ok {
		return errors.New("native gateway requires a Kubernetes applier")
	}
	if err = applier.ApplyGatewayNative(ctx, resources); err != nil {
		return err
	}
	state = "applied"
	return nil
}
