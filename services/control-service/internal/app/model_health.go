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
	"regexp"
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

// Model health statuses persisted on models.health_status. credential_invalid
// (HTTP 401) and denied (HTTP 403 — often throttling, occasionally a revoked
// grant; either way the model cannot serve right now) are hard failures that
// warrant immediate failover; unreachable/error may be transient and are left
// to the consumer's hysteresis.
const (
	ModelHealthUnknown           = "unknown"
	ModelHealthHealthy           = "healthy"
	ModelHealthCredentialInvalid = "credential_invalid"
	ModelHealthDenied            = "denied"
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

	skipped := 0
	var skipReason error
	for index, row := range models {
		if ctx.Err() != nil {
			// The round budget is exhausted (or the service is shutting
			// down): stop quietly instead of misclassifying every remaining
			// model as unreachable.
			skipped, skipReason = len(models)-index, ctx.Err()
			break
		}
		var outcome probeOutcome
		secret, credentialErr := a.modelCredential(ctx, row.deploymentID, row.credentialID)
		if credentialErr != nil {
			outcome = probeOutcome{Status: ModelHealthError, Detail: "credential resolution failed: " + credentialErr.Error()}
		} else {
			outcome = probeModel(ctx, client, row.protocol, row.endpoint, row.upstreamModel, secret)
		}
		if err := a.recordModelHealth(ctx, row, outcome, now); err != nil {
			if ctx.Err() != nil {
				skipped, skipReason = len(models)-index, ctx.Err()
				break
			}
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
			a.notifyModelHealthChange(ctx, row.modelID, row.deploymentID, row.currentStatus, outcome.Status, outcome.Detail)
		}
	}
	if skipped > 0 {
		slog.Warn("model health round ended early", "probed", result.Checked, "skipped", skipped, "reason", skipReason)
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
			if ctx.Err() != nil || checkCtx.Err() != nil {
				// Shutdown or an exhausted round budget; the checker already
				// accounted for the skipped models. Not an operational error.
				return
			}
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
	_, err := a.database().Exec(ctx, `UPDATE models SET health_status=$3,health_checked_at=$4,health_detail=$5,health_since=CASE WHEN health_status<>$3 OR health_since IS NULL THEN $4 ELSE health_since END WHERE deployment_id=$1 AND id=$2`,
		row.deploymentID, row.modelID, outcome.Status, now, detail)
	if err != nil {
		return fmt.Errorf("record model health for %s: %w", row.modelID, err)
	}
	return nil
}

// modelHealthNotificationLimit caps the notification text (runes) so an
// upstream response echoed into the probe detail cannot exceed the receiver's
// message limit.
const modelHealthNotificationLimit = 800

// notifyModelHealthChange pushes a best-effort notification when a model
// changes health state. The payload is the WeCom group-robot text format (the
// deployment's alert channel); an unset webhook is a no-op and delivery
// failures are logged only — the persisted health columns and the audit trail
// remain the source of truth. The notification runs synchronously with the
// round context detached: a canceled probe round must not swallow the alert,
// while a dead receiver delays the round by at most the notification timeout.
// It fires only on transitions (the caller gates on a status change), so an
// ongoing outage does not re-notify every round.
func (a *App) notifyModelHealthChange(ctx context.Context, modelID, deploymentID, from, to, detail string) {
	endpoint := strings.TrimSpace(a.Config.ModelHealthWebhookURL)
	if endpoint == "" {
		return
	}
	content := fmt.Sprintf("model health alert: %s (%s) %s -> %s", modelID, deploymentID, from, to)
	if detail != "" {
		content += ": " + detail
	}
	if runes := []rune(content); len(runes) > modelHealthNotificationLimit {
		content = string(runes[:modelHealthNotificationLimit]) + "…"
	}
	payload, err := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": content}})
	if err != nil {
		slog.Warn("model health notification skipped", "error", err)
		return
	}
	postContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(postContext, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		slog.Warn("model health notification skipped", "model", modelID, "error", err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		slog.Warn("model health notification failed", "model", modelID, "error", err)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
	}()
	if response.StatusCode >= 300 {
		slog.Warn("model health notification rejected", "model", modelID, "status", response.StatusCode)
	}
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
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		// A relative endpoint cannot be probed directly; classify it as a
		// configuration error rather than an upstream outage so it does not
		// read as a transient network failure.
		return probeOutcome{Status: ModelHealthError, Detail: "endpoint " + base + " is not an absolute URL; active probing requires one"}
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
	body = scrubCredential(body, credentialValue)
	switch response.StatusCode {
	case http.StatusOK:
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
	case http.StatusUnauthorized:
		return probeOutcome{Status: ModelHealthCredentialInvalid, Detail: statusDetail(response.StatusCode, body)}
	case http.StatusForbidden:
		return probeOutcome{Status: ModelHealthDenied, Detail: statusDetail(response.StatusCode, body)}
	case http.StatusNotFound, http.StatusMethodNotAllowed:
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
	body = scrubCredential(body, credentialValue)
	switch response.StatusCode {
	case http.StatusOK:
		if !validOpenAICompletion(body) {
			return probeOutcome{Status: ModelHealthError, Detail: "HTTP 200 without a completion payload (an intercepting proxy or login page answered)"}
		}
		return probeOutcome{Status: ModelHealthHealthy, Detail: "the upstream answered a minimal completion"}
	case http.StatusUnauthorized:
		return probeOutcome{Status: ModelHealthCredentialInvalid, Detail: statusDetail(response.StatusCode, body)}
	case http.StatusForbidden:
		return probeOutcome{Status: ModelHealthDenied, Detail: statusDetail(response.StatusCode, body)}
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
	body = scrubCredential(body, credentialValue)
	switch response.StatusCode {
	case http.StatusOK:
		if !validAnthropicMessage(body) {
			return probeOutcome{Status: ModelHealthError, Detail: "HTTP 200 without a messages payload (an intercepting proxy or login page answered)"}
		}
		return probeOutcome{Status: ModelHealthHealthy, Detail: "the upstream answered a minimal messages call"}
	case http.StatusUnauthorized:
		return probeOutcome{Status: ModelHealthCredentialInvalid, Detail: statusDetail(response.StatusCode, body)}
	case http.StatusForbidden:
		return probeOutcome{Status: ModelHealthDenied, Detail: statusDetail(response.StatusCode, body)}
	case http.StatusNotFound:
		// Heuristic: a 404 usually means the upstream model id is gone, but
		// a misconfigured endpoint path (e.g. one already ending in /v1)
		// also lands here — the detail keeps the status code for triage.
		return probeOutcome{Status: ModelHealthModelMissing, Detail: statusDetail(response.StatusCode, body)}
	default:
		outcome := classifyHTTPStatus(response.StatusCode, body)
		if outcome.Status == ModelHealthError && response.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "model") {
			outcome.Status = ModelHealthModelMissing
		}
		return outcome
	}
}

// validOpenAICompletion reports whether a 200 body is an actual completion.
// Intercepting proxies (SSO portals, captive gateways, session-expiry
// redirects followed to a login page) answer 200 with HTML that would
// otherwise masquerade as a healthy model.
func validOpenAICompletion(body []byte) bool {
	var payload struct {
		Choices []json.RawMessage `json:"choices"`
	}
	return json.Unmarshal(body, &payload) == nil && len(payload.Choices) >= 1
}

// validAnthropicMessage reports whether a 200 body is an actual messages
// response ("type" or "content" must be present — see validOpenAICompletion
// for the failure mode this guards against).
func validAnthropicMessage(body []byte) bool {
	var payload struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}
	return json.Unmarshal(body, &payload) == nil && (payload.Type != "" || payload.Content != nil)
}

// classifyHTTPStatus maps every remaining status to error. 429/5xx mean "we
// cannot get an answer right now" without proving the credential or model
// invalid, so they ride the consumer's hysteresis instead of triggering an
// immediate failover.
func classifyHTTPStatus(status int, body []byte) probeOutcome {
	return probeOutcome{Status: ModelHealthError, Detail: statusDetail(status, body)}
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

// scrubCredential removes exact (case-insensitive) echoes of the probe
// credential from a response snippet. Some providers quote the presented key
// in their error bodies; health details are persisted and shown in the
// console, so the value must never survive into them. Boundary: this only
// covers byte-exact echoes — partial reveals (last four characters), JSON
// escape forms and URL-encoded forms are not scrubbed, so details must not
// be treated as a leak-proof channel.
func scrubCredential(body []byte, credentialValue string) []byte {
	if credentialValue == "" {
		return body
	}
	// (?i) folding via regexp is Unicode-aware; bytes.ToLower can change
	// byte lengths (e.g. U+0130), which would corrupt index arithmetic.
	pattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(credentialValue))
	return pattern.ReplaceAll(body, []byte("***"))
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
