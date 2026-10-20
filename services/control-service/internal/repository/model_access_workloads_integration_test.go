package repository

import (
	"errors"
	"slices"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func workloadObservation() ObservedModelWorkload {
	return ObservedModelWorkload{EmployeeName: "sales-helper", PodUIDs: []string{"pod-first"}, Identity: gateway.ModelAccessWorkload{
		Namespace: "digital-employees", EmployeeUID: "employee-uid", ServiceAccountName: "de-model-employee",
		ServiceAccountUID: "sa-first-uid", Audience: "urn:zhiyuan:model-gateway"}}
}

func TestModelAccessWorkloadSourceAndRenewalWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	w := workloadObservation()
	aliceVersion, _ := captureAcknowledgement(t, pool, "alice")
	before, _ := captureAcknowledgement(t, pool, "employee")
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	version, _ := captureAcknowledgement(t, pool, "employee")
	if version <= before {
		t.Fatal("binding did not queue the affected agent")
	}
	source := readSource(t, store, "employee")
	if len(source.Workloads) != 1 || source.Workloads[0].Identity != w.Identity || source.Workloads[0].EmployeeName != w.EmployeeName ||
		!source.Workloads[0].VerifiedUntil.After(source.ObservedAt) || len(source.Sessions) != 0 ||
		!slices.Equal(source.RoleIDs, []string{"direct", "inherited"}) || len(source.Models) != 2 {
		t.Fatal("workload observation lost identity, depended on a human session, or changed role-derived resources")
	}
	if next := source.NextExpiry(); next == nil || !next.Equal(source.Workloads[0].VerifiedUntil) {
		t.Fatal("workload verification expiry is not scheduled")
	}
	// Extending a live observation must not invalidate permissions/cache.
	if renewed, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 6*time.Minute); err != nil || renewed != key {
		t.Fatal(err)
	}
	if after, _ := captureAcknowledgement(t, pool, "employee"); after != version {
		t.Fatal("observation renewal caused a policy change")
	}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if after, _ := captureAcknowledgement(t, pool, "employee"); after <= version {
		t.Fatal("shorter verification deadline was not queued")
	}
	_, ack := captureAcknowledgement(t, pool, "employee")
	if _, err := store.CaptureModelAccessSessions(t.Context(), "employee", time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("session-only producer acknowledged an unsupported workload")
	}
	if _, after := captureAcknowledgement(t, pool, "employee"); after != ack {
		t.Fatal("unsupported workload advanced the source acknowledgement")
	}
	if after, _ := captureAcknowledgement(t, pool, "alice"); after != aliceVersion {
		t.Fatal("binding changes invalidated an unrelated human")
	}
	if len(readSource(t, store, "alice").Workloads) != 0 || len(readSource(t, New(store.db).Deployment("foreign"), "employee").Workloads) != 0 {
		t.Fatal("binding leaked into another account or deployment")
	}
}

func TestModelAccessWorkloadReplacementAndTerminalRetirementWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	first := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", first, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	replacement := first
	replacement.Identity.ServiceAccountUID = "sa-replacement-uid"
	next, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, replacement, time.Minute)
	if err != nil || next == key {
		t.Fatal("SA recreation did not replace identity", err)
	}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, first, time.Minute); !errors.Is(err, ErrAccessWorkloadConflict) {
		t.Fatal("stale observer replaced a newer SA")
	}
	if err := store.RetireModelAccessWorkload(t.Context(), "employee", key); err != nil {
		t.Fatal("exact old retirement is not idempotent", err)
	}
	if got := readSource(t, store, "employee").Workloads; len(got) != 1 || got[0].Identity.ServiceAccountUID != replacement.Identity.ServiceAccountUID {
		t.Fatal("late deletion retired the replacement")
	}
	// The expected-current key is correct, but the old SA's historical fence
	// still rejects resurrection. The failed replacement must roll back.
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", next, first, time.Minute); !errors.Is(err, ErrAccessWorkloadConflict) {
		t.Fatal("retired identity was resurrected")
	}
	if got := readSource(t, store, "employee").Workloads; len(got) != 1 || got[0].Identity.ServiceAccountUID != replacement.Identity.ServiceAccountUID {
		t.Fatal("failed insert retired the current binding")
	}
	if err := store.RetireModelAccessWorkload(t.Context(), "employee", next); err != nil {
		t.Fatal(err)
	}
	if got := readSource(t, store, "employee").Workloads; len(got) != 0 {
		t.Fatal("retired workload is still live")
	}
	for _, sql := range []string{
		`UPDATE model_access_workloads SET retired_at=NULL WHERE service_account_uid='sa-first-uid'`,
		`UPDATE model_access_workloads SET user_id='alice' WHERE service_account_uid='sa-first-uid'`,
		`DELETE FROM model_access_workloads WHERE service_account_uid='sa-first-uid'`,
	} {
		if _, err := pool.Exec(t.Context(), sql); err == nil {
			t.Fatal("terminal identity fence was mutable")
		}
	}
}

func TestModelAccessWorkloadExpiredObservationRecoveryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	w := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sourceExec(t, pool, `UPDATE model_access_workloads SET verified_at=clock_timestamp()-interval '2 minutes',verified_until=clock_timestamp()-interval '1 minute'`)
	if len(readSource(t, store, "employee").Workloads) != 0 {
		t.Fatal("expired verification remains live")
	}
	version, _ := captureAcknowledgement(t, pool, "employee")
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, time.Minute); err != nil {
		t.Fatal(err)
	}
	if after, _ := captureAcknowledgement(t, pool, "employee"); after <= version || len(readSource(t, store, "employee").Workloads) != 1 {
		t.Fatal("recovery did not queue re-evaluation")
	}
}
