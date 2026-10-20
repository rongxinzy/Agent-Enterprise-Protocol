package repository

import (
	"testing"
	"time"
)

func TestAccessCaptureDurableRefreshAndNaturalExpiryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	first := captureSessions(t, store, "alice")
	p := first.Publications[0]
	if !first.NextCaptureAt.Equal(p.IssuedAt.Add(150 * time.Second)) {
		t.Fatal("freshness work was not scheduled before the publication cap")
	}
	// Move to the next representable CR timestamp, then make the durable timer
	// due. No business-write trigger is used to manufacture a new source version.
	select {
	case <-time.After(time.Until(p.IssuedAt.Add(time.Second)) + 10*time.Millisecond):
	case <-t.Context().Done():
		t.Fatal("refresh test cancelled")
	}
	sourceExec(t, pool, `UPDATE model_access_changes SET next_capture_at=clock_timestamp() WHERE deployment_id='source' AND user_id='alice'`)
	renewed := captureSessions(t, store, "alice")
	if renewed.SourceVersion != first.SourceVersion+1 || renewed.Publications[0].Sequence != p.Sequence+1 ||
		renewed.Publications[0].ContentHash != p.ContentHash || !renewed.Publications[0].ValidUntil.After(p.ValidUntil) {
		t.Fatal("due freshness did not create a versioned delivery of the unchanged policy")
	}
	// This proves durable delivery scheduling only. Whether a native renewal can
	// preserve its warm authorization evaluator is a separate integration gate.
	version, ack := captureAcknowledgement(t, pool, "alice")
	sourceExec(t, pool, `UPDATE user_session_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE session_id='a-live'`)
	if v, a := captureAcknowledgement(t, pool, "alice"); v != version || a != ack {
		t.Fatal("test accidentally relied on refresh-token write invalidation")
	}
	sourceExec(t, pool, `UPDATE model_access_changes SET next_capture_at=clock_timestamp() WHERE deployment_id='source' AND user_id='alice'`)
	expired := captureSessions(t, store, "alice")
	policy := capturedPolicy(t, expired.Publications[0])
	if expired.SourceVersion != version+1 || !policy.Retired || !policy.Disabled || expired.NextCaptureAt != nil {
		t.Fatal("natural session expiry did not produce a terminal tombstone")
	}
}

func TestAccessCaptureDoesNotRenewNaturalGrantDeadlineWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	sourceExec(t, pool, `UPDATE model_assignments SET expires_at=clock_timestamp()+interval '30 seconds' WHERE id='direct-chat' OR id='inherited-chat'`)
	value := captureSessions(t, store, "alice")
	if len(value.Publications) != 1 || value.NextCaptureAt == nil || !value.NextCaptureAt.Equal(value.Publications[0].ValidUntil) {
		t.Fatal("natural model expiry was scheduled as a blind freshness renewal")
	}
	if time.Until(value.Publications[0].ValidUntil) > 30*time.Second {
		t.Fatal("publication extended a natural role grant deadline")
	}
	sourceExec(t, pool, `UPDATE model_assignments SET expires_at=clock_timestamp()-interval '1 second' WHERE deployment_id='source'`)
	withdrawn := captureSessions(t, store, "alice")
	if len(capturedPolicy(t, withdrawn.Publications[0]).Models) != 0 || withdrawn.NextCaptureAt != nil {
		t.Fatal("expired model grants retained access or an endless renewal")
	}
}
