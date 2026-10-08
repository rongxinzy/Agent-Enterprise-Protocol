package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

// Active model health probing. Routing-level checks (gateway logs, Envoy
// health checks) can only see transport semantics; the failure modes that
// actually degrade employees — a dead endpoint, a credential the upstream no
// longer accepts, and an upstream model id that moved — are only observable
// by issuing a minimal real call. The prober does exactly that with the
// credential stored on the model, persists the classification on the model
// row, and logs transitions so the audit pipeline carries them.

// Model health statuses persisted on models.health_status.
const (
	ModelHealthUnknown           = "unknown"
	ModelHealthHealthy           = "healthy"
	ModelHealthCredentialInvalid = "credential_invalid"
	ModelHealthModelMissing      = "model_missing"
	ModelHealthUnreachable       = "unreachable"
	ModelHealthError             = "error"
)

// ModelHealthResult summarizes one probing cycle.
type ModelHealthResult struct {
	Checked   int
	Changed   int
	Healthy   int
	Unhealthy int
}

type modelProbeRow struct {
	deploymentID  string
	modelID       string
	protocol      string
	endpoint      string
	upstreamModel string
	credentialID  *string
	currentStatus string
}

type probeOutcome struct {
	Status string
	Detail string
}

// CheckModelHealth probes every enabled gateway model that has a complete
// endpoint/upstream pair and persists the outcome. One model failing to probe
// never aborts the cycle; database failures do.
func (a *App) CheckModelHealth(ctx context.Context, now time.Time, client *http.Client) (ModelHealthResult, error) {
	var result ModelHealthResult
	database := a.database()
	if database == nil {
		return result, errors.New("model health database is unavailable")
	}
	if client == nil {
		client = &http.Client{Timeout: a.Config.ModelHealthTimeout}
	}
	rows, err := database.Query(ctx, `SELECT deployment_id,id,protocol,endpoint,upstream_model,credential_id,health_status
FROM models
WHERE enabled AND source_type='gateway' AND endpoint IS NOT NULL AND endpoint<>''
  AND upstream_model IS NOT NULL AND upstream_model<>''
ORDER BY deployment_id,id`)
	if err != nil {
		return result, fmt.Errorf("list models for health check: %w", err)
	}
	defer rows.Close()
	var models []modelProbeRow
	for rows.Next() {
		var row modelProbeRow
		if err := rows.Scan(&row.deploymentID, &row.modelID, &row.protocol, &row.endpoint, &row.upstreamModel, &row.credentialID, &row.currentStatus); err != nil {
			return result, fmt.Errorf("scan model for health check: %w", err)
		}
		models = append(models, row)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate models for health check: %w", err)
	}

	for _, row := range models {
		var outcome probeOutcome
		secret, credentialErr := a.modelCredential(ctx, row.deploymentID, row.credentialID)
		if credentialErr != nil {
			outcome = probeOutcome{Status: ModelHealthError, Detail: "credential resolution failed: " + credentialErr.Error()}
		} else {
			outcome = probeModel(ctx, client, row.protocol, row.endpoint, row.upstreamModel, secret)
		}
		if err := a.recordModelHealth(ctx, row, outcome, now); err != nil {
			return result, err
		}
		result.Checked++
		if outcome.Status == ModelHealthHealthy {
			result.Healthy++
		} else {
			result.Unhealthy++
		}
		if row.currentStatus != outcome.Status {
			result.Changed++
			level := slog.LevelWarn
			if outcome.Status == ModelHealthHealthy {
				level = slog.LevelInfo
			}
			slog.Log(ctx, level, "model health changed",
				"model", row.modelID, "deployment", row.deploymentID,
				"from", row.currentStatus, "to", outcome.Status, "detail", outcome.Detail)
		}
	}
	return result, nil
}

// RunModelHealth probes on the configured interval until ctx is done. A
// non-positive interval disables probing.
func (a *App) RunModelHealth(ctx context.Context) {
	interval := a.Config.ModelHealthInterval
	if interval <= 0 {
		slog.Info("model health probing disabled")
		return
	}
	client := &http.Client{Timeout: a.Config.ModelHealthTimeout}
	run := func() {
		checkCtx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()
		result, err := a.CheckModelHealth(checkCtx, time.Now().UTC(), client)
		if err != nil {
			slog.Warn("model health check failed", "error", err)
			return
		}
		if result.Checked > 0 {
			slog.Info("model health check completed",
				"checked", result.Checked, "healthy", result.Healthy,
				"unhealthy", result.Unhealthy, "changed", result.Changed)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// modelCredential resolves the plaintext value for a model's bound
// credential. Models without a credential probe unauthenticated (keyless
// local endpoints); a bound-but-unresolvable credential is reported to the
// caller so the outcome can distinguish it from a rejected credential.
func (a *App) modelCredential(ctx context.Context, deploymentID string, credentialID *string) (string, error) {
	if credentialID == nil || strings.TrimSpace(*credentialID) == "" {
		return "", nil
	}
	if a.Credentials == nil {
		return "", errors.New("credential store is unavailable")
	}
	var sealed, nonce []byte
	var keyID string
	err := a.database().QueryRow(ctx, `SELECT encrypted_value,nonce,key_id FROM credentials WHERE deployment_id=$1 AND id=$2`, deploymentID, *credentialID).Scan(&sealed, &nonce, &keyID)
	if err != nil {
		return "", fmt.Errorf("credential %s: %w", *credentialID, err)
	}
	plaintext, err := a.Credentials.Open(ctx, credential.Envelope{KeyID: keyID, Nonce: nonce, Ciphertext: sealed}, credential.AssociatedData(deploymentID, *credentialID))
	if err != nil {
		return "", fmt.Errorf("credential %s: %w", *credentialID, err)
	}
	return strings.TrimSpace(string(plaintext)), nil
}

func (a *App) recordModelHealth(ctx context.Context, row modelProbeRow, outcome probeOutcome, now time.Time) error {
	var detail any
	if outcome.Detail != "" {
		detail = outcome.Detail
	}
	_, err := a.database().Exec(ctx, `UPDATE models SET health_status=$3,health_checked_at=$4,health_detail=$5 WHERE deployment_id=$1 AND id=$2`,
		row.deploymentID, row.modelID, outcome.Status, now, detail)
	if err != nil {
		return fmt.Errorf("record model health for %s: %w", row.modelID, err)
	}
	return nil
}

// probeModel classifies one upstream call. openai-compatible endpoints are
// checked through the model catalog (GET /models) so upstream model id drift
// is detected exactly; servers without a catalog fall back to a minimal
// completion. Anthropic-protocol endpoints get a minimal /v1/messages call
// because the passthrough protocol exposes no catalog route.
func probeModel(ctx context.Context, client *http.Client, protocol, endpoint, upstreamModel, credentialValue string) probeOutcome {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if base == "" {
		return probeOutcome{Status: ModelHealthError, Detail: "the model has no endpoint"}
	}
	switch protocol {
	case "openai-compatible":
		return probeOpenAI(ctx, client, base, upstreamModel, credentialValue)
	case "anthropic":
		return probeAnthropic(ctx, client, base, upstreamModel, credentialValue)
	default:
		return probeOutcome{Status: ModelHealthError, Detail: "unsupported protocol " + protocol}
	}
}

func probeOpenAI(ctx context.Context, client *http.Client, base, upstreamModel, credentialValue string) probeOutcome {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return probeOutcome{Status: ModelHealthError, Detail: err.Error()}
	}
	request.Header.Set("Accept", "application/json")
	if credentialValue != "" {
		request.Header.Set("Authorization", "Bearer "+credentialValue)
	}
	response, err := client.Do(request)
	if err != nil {
		return probeOutcome{Status: ModelHealthUnreachable, Detail: transportDetail(err)}
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	_ = response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		var catalog struct {
			Data []struct {
				ID      string   `json:"id"`
				Aliases []string `json:"aliases"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &catalog); err == nil && len(catalog.Data) > 0 {
			target := strings.ToLower(strings.TrimSpace(upstreamModel))
			for _, entry := range catalog.Data {
				if strings.ToLower(entry.ID) == target {
					return probeOutcome{Status: ModelHealthHealthy, Detail: "upstream catalog lists " + upstreamModel}
				}
				for _, alias := range entry.Aliases {
					if strings.ToLower(alias) == target {
						return probeOutcome{Status: ModelHealthHealthy, Detail: "upstream catalog lists " + upstreamModel + " as an alias"}
					}
				}
			}
			return probeOutcome{Status: ModelHealthModelMissing, Detail: "the upstream catalog does not list " + upstreamModel}
		}
		// A 200 without a usable catalog still proves reachability and
		// credential acceptance; fall through to a completion probe for the
		// model id check.
		return probeOpenAICompletion(ctx, client, base, upstreamModel, credentialValue)
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return probeOutcome{Status: ModelHealthCredentialInvalid, Detail: statusDetail(response.StatusCode, body)}
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed:
		// Some OpenAI-compatible servers expose no catalog route; probe with
		// a minimal completion instead.
		return probeOpenAICompletion(ctx, client, base, upstreamModel, credentialValue)
	default:
		return classifyHTTPStatus(response.StatusCode, body)
	}
}

func probeOpenAICompletion(ctx context.Context, client *http.Client, base, upstreamModel, credentialValue string) probeOutcome {
	payload, _ := json.Marshal(map[string]any{
		"model":      upstreamModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return probeOutcome{Status: ModelHealthError, Detail: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	if credentialValue != "" {
		request.Header.Set("Authorization", "Bearer "+credentialValue)
	}
	response, err := client.Do(request)
	if err != nil {
		return probeOutcome{Status: ModelHealthUnreachable, Detail: transportDetail(err)}
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	_ = response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return probeOutcome{Status: ModelHealthHealthy, Detail: "the upstream answered a minimal completion"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return probeOutcome{Status: ModelHealthCredentialInvalid, Detail: statusDetail(response.StatusCode, body)}
	default:
		outcome := classifyHTTPStatus(response.StatusCode, body)
		if outcome.Status == ModelHealthError && (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound) && strings.Contains(strings.ToLower(string(body)), "model") {
			outcome.Status = ModelHealthModelMissing
		}
		return outcome
	}
}

func probeAnthropic(ctx context.Context, client *http.Client, base, upstreamModel, credentialValue string) probeOutcome {
	payload, _ := json.Marshal(map[string]any{
		"model":      upstreamModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return probeOutcome{Status: ModelHealthError, Detail: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	if credentialValue != "" {
		request.Header.Set("x-api-key", credentialValue)
	}
	response, err := client.Do(request)
	if err != nil {
		return probeOutcome{Status: ModelHealthUnreachable, Detail: transportDetail(err)}
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	_ = response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return probeOutcome{Status: ModelHealthHealthy, Detail: "the upstream answered a minimal messages call"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return probeOutcome{Status: ModelHealthCredentialInvalid, Detail: statusDetail(response.StatusCode, body)}
	case http.StatusNotFound:
		return probeOutcome{Status: ModelHealthModelMissing, Detail: statusDetail(response.StatusCode, body)}
	default:
		outcome := classifyHTTPStatus(response.StatusCode, body)
		if outcome.Status == ModelHealthError && response.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "model") {
			outcome.Status = ModelHealthModelMissing
		}
		return outcome
	}
}

func classifyHTTPStatus(status int, body []byte) probeOutcome {
	switch {
	case status == http.StatusTooManyRequests:
		return probeOutcome{Status: ModelHealthError, Detail: statusDetail(status, body)}
	case status >= 500:
		return probeOutcome{Status: ModelHealthError, Detail: statusDetail(status, body)}
	default:
		return probeOutcome{Status: ModelHealthError, Detail: statusDetail(status, body)}
	}
}

func statusDetail(status int, body []byte) string {
	snippet := snippetOf(body)
	if snippet == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, snippet)
}

func transportDetail(err error) string {
	message := err.Error()
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

func snippetOf(body []byte) string {
	snippet := strings.Join(strings.Fields(string(body)), " ")
	if snippet == "" {
		return ""
	}
	if utf8.RuneCountInString(snippet) > 160 {
		runes := []rune(snippet)
		snippet = string(runes[:160])
	}
	return snippet
}
