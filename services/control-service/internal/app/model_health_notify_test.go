package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// A status transition must reach the configured webhook exactly once, and the
// same status on the next round must not re-notify (the caller gates on a
// status change — an ongoing outage is one alert, not one per probe round).
func TestCheckModelHealthNotifiesWebhookOnTransition(t *testing.T) {
	application, pool, _ := newMockApplication(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
	}))
	defer upstream.Close()

	received := make(chan string, 4)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
	}))
	defer webhook.Close()
	application.Config.ModelHealthWebhookURL = webhook.URL

	now := time.Date(2026, 10, 9, 0, 30, 0, 0, time.UTC)
	probeRows := func(status string) *pgxmock.Rows {
		return pgxmock.NewRows([]string{"deployment_id", "id", "protocol", "endpoint", "upstream_model", "credential_id", "health_status"}).
			AddRow("demo", "bench-a", "openai-compatible", upstream.URL, "model-a", nil, status)
	}
	pool.ExpectQuery(`SELECT deployment_id,id,protocol,endpoint,upstream_model,credential_id,health_status`).WillReturnRows(probeRows("healthy"))
	pool.ExpectExec(`UPDATE models SET health_status`).
		WithArgs("demo", "bench-a", ModelHealthCredentialInvalid, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	result, err := application.CheckModelHealth(context.Background(), now, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed != 1 {
		t.Fatalf("healthy -> credential_invalid must count as a change: %#v", result)
	}
	select {
	case body := <-received:
		if !strings.Contains(body, "bench-a") || !strings.Contains(body, "credential_invalid") || !strings.Contains(body, "401") {
			t.Fatalf("notification payload must carry model, new status and detail: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a status transition must notify the configured webhook")
	}

	// Same failure on the next round: still probed and persisted, not re-notified.
	pool.ExpectQuery(`SELECT deployment_id,id,protocol,endpoint,upstream_model,credential_id,health_status`).WillReturnRows(probeRows(ModelHealthCredentialInvalid))
	pool.ExpectExec(`UPDATE models SET health_status`).
		WithArgs("demo", "bench-a", ModelHealthCredentialInvalid, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if _, err := application.CheckModelHealth(context.Background(), now.Add(time.Minute), upstream.Client()); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-received:
		t.Fatalf("unchanged status must not re-notify: %s", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// A dead receiver must not fail the probe round: the transition is persisted
// and counted, and only the notification is lost (logged).
func TestCheckModelHealthSurvivesNotificationFailure(t *testing.T) {
	application, pool, _ := newMockApplication(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"}]}`))
	}))
	defer upstream.Close()

	// Nothing listens on port 1: the notification attempts are refused.
	application.Config.ModelHealthWebhookURL = "http://127.0.0.1:1/hook"

	pool.ExpectQuery(`SELECT deployment_id,id,protocol,endpoint,upstream_model,credential_id,health_status`).
		WillReturnRows(pgxmock.NewRows([]string{"deployment_id", "id", "protocol", "endpoint", "upstream_model", "credential_id", "health_status"}).
			AddRow("demo", "bench-a", "openai-compatible", upstream.URL, "model-a", nil, "unknown"))
	pool.ExpectExec(`UPDATE models SET health_status`).
		WithArgs("demo", "bench-a", ModelHealthHealthy, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	result, err := application.CheckModelHealth(context.Background(), time.Date(2026, 10, 9, 0, 30, 0, 0, time.UTC), upstream.Client())
	if err != nil {
		t.Fatalf("notification transport failures must not fail the round: %v", err)
	}
	if result.Checked != 1 || result.Changed != 1 {
		t.Fatalf("the transition must still be persisted and counted: %#v", result)
	}
}
