package repository

import (
	"testing"
	"time"
)

func TestMixedCaptureNaturalObservationExpiryWithoutWriteWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	seedWorkloadLicense(t, pool)
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", workloadObservation(), 3*time.Second); err != nil {
		t.Fatal(err)
	}
	first := captureBindings(t, store, "employee")
	p, _ := workloadPolicy(t, first)
	if first.NextCaptureAt == nil || first.NextCaptureAt.After(p.ValidUntil) || !first.NextCaptureAt.After(p.IssuedAt) {
		t.Fatal("lifecycle observation did not schedule bounded future work")
	}
	// No source-table update or synthetic invalidation: passage of time alone
	// must pause the workload while preserving its recoverable registry identity.
	select {
	case <-time.After(time.Until(p.ValidUntil.Add(time.Second))):
	case <-t.Context().Done():
		t.Fatal("cancelled while waiting for real observation expiry")
	}
	if v, a := captureAcknowledgement(t, pool, "employee"); v != first.SourceVersion || a != v {
		t.Fatal("expiry test unexpectedly depended on a write trigger")
	}
	second := captureBindings(t, store, "employee")
	_, policy := workloadPolicy(t, second)
	if second.SourceVersion != first.SourceVersion+1 || !policy.Disabled || policy.Retired || second.NextCaptureAt != nil {
		t.Fatal("natural observation expiry retained allow, retired identity or lost durable work")
	}
}

func TestMixedCaptureSAReplacementAndBindingDeletionWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	seedWorkloadLicense(t, pool)
	w := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	captureBindings(t, store, "employee")
	w.Identity.ServiceAccountUID = "replacement-sa-uid"
	newKey, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	replaced := captureBindings(t, store, "employee")
	if len(replaced.Publications) != 2 {
		t.Fatal("replacement failed to publish allow AND old-identity tombstone atomically")
	}
	for _, publication := range replaced.Publications {
		policy := capturedPolicy(t, publication)
		if publication.BindingKey == key && (!policy.Retired || !policy.Disabled) ||
			publication.BindingKey == newKey && (policy.Retired || policy.Disabled) {
			t.Fatal("SA replacement reused or kept the old authority")
		}
	}
	sourceExec(t, pool, `DELETE FROM users WHERE id='employee'`)
	deleted := captureBindings(t, store, "employee")
	if len(deleted.Publications) != 2 {
		t.Fatal("deleted agent lost binding history")
	}
	for _, publication := range deleted.Publications {
		if policy := capturedPolicy(t, publication); !policy.Retired || !policy.Disabled || len(policy.Models) != 0 {
			t.Fatal("deleted agent retained a workload grant")
		}
	}
}

func TestMixedCaptureContinuousRenewalPreservesPolicyWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	seedWorkloadLicense(t, pool)
	w := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := captureBindings(t, store, "employee")
	p, _ := workloadPolicy(t, first)
	if first.NextCaptureAt == nil || !first.NextCaptureAt.Before(p.ValidUntil) {
		t.Fatal("healthy workload cannot refresh before its observation deadline")
	}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, a := captureAcknowledgement(t, pool, "employee"); v != first.SourceVersion || a != v {
		t.Fatal("continuous observation renewal invalidated permissions")
	}
	// Make the durable pre-deadline timer due; do not create a business change.
	sourceExec(t, pool, `UPDATE model_access_changes SET next_capture_at=clock_timestamp() WHERE deployment_id='source' AND user_id='employee'`)
	second := captureBindings(t, store, "employee")
	renewed, _ := workloadPolicy(t, second)
	if renewed.ContentHash != p.ContentHash || renewed.Sequence != p.Sequence+1 || !renewed.ValidUntil.After(p.ValidUntil) ||
		second.NextCaptureAt == nil || !second.NextCaptureAt.After(renewed.IssuedAt) || !second.NextCaptureAt.Before(renewed.ValidUntil) {
		t.Fatal("freshness delivery changed semantic policy, missed renewal or scheduled a busy loop")
	}
}
