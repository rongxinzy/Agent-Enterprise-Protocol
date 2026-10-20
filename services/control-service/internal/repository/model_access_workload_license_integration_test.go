package repository

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedWorkloadLicense(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// Synthetic persisted verified-license metadata. Signature verification is
	// exercised by the existing license/import tests, not bypassed at runtime.
	sourceExec(t, pool, `UPDATE licenses SET features=ARRAY['enterprise.models'] WHERE license_id='perpetual'`)
}

func TestWorkloadLicenseRemovalAndRestorationWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	w := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := captureBindings(t, store, "employee")
	_, denied := workloadPolicy(t, first)
	if !denied.Disabled || denied.Retired || denied.WorkloadEntitlement != nil {
		t.Fatal("active license without exact enterprise.models feature granted access")
	}
	seedWorkloadLicense(t, pool)
	licensed := captureBindings(t, store, "employee")
	p, active := workloadPolicy(t, licensed)
	if active.Disabled || active.WorkloadEntitlement == nil || active.WorkloadEntitlement.LicenseID != "perpetual" || p.BindingKey != key {
		t.Fatal("eligible source license could not recover the workload")
	}
	for _, change := range []string{
		`UPDATE licenses SET features=ARRAY['model'] WHERE license_id='perpetual'`,
		`UPDATE licenses SET status='revoked',revoked_at=now() WHERE license_id='perpetual'`,
		`UPDATE licenses SET status='active',revoked_at=now() WHERE license_id='perpetual'`,
		`UPDATE licenses SET expires_at=now()-interval '2 seconds',grace_ends_at=now()-interval '1 second' WHERE license_id='perpetual'`,
	} {
		sourceExec(t, pool, change)
		paused := captureBindings(t, store, "employee")
		_, policy := workloadPolicy(t, paused)
		if !policy.Disabled || policy.Retired || policy.WorkloadEntitlement != nil || len(policy.Licenses) != 0 || policy.BindingKey() != key {
			t.Fatal("license withdrawal retained allow or destroyed recoverable identity")
		}
		sourceExec(t, pool, `UPDATE licenses SET features=ARRAY['enterprise.models'],status='active',revoked_at=NULL,expires_at=NULL,grace_ends_at=NULL WHERE license_id='perpetual'`)
		_, policy = workloadPolicy(t, captureBindings(t, store, "employee"))
		if policy.Disabled || policy.WorkloadEntitlement == nil || policy.BindingKey() != key {
			t.Fatal("valid license restoration could not recover the original workload")
		}
	}
	// Every capture acknowledges exactly the transaction that supplies evidence.
	if v, a := captureAcknowledgement(t, pool, "employee"); v != a {
		t.Fatal("licensed capture left a mismatched source acknowledgement")
	}
}

func TestWorkloadLicenseNaturalGraceExpiryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	seedWorkloadLicense(t, pool)
	sourceExec(t, pool, `UPDATE licenses SET expires_at=clock_timestamp()-interval '1 minute',grace_ends_at=clock_timestamp()+interval '4 seconds' WHERE license_id='perpetual'`)
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", workloadObservation(), time.Minute); err != nil {
		t.Fatal(err)
	}
	first := captureBindings(t, store, "employee")
	p, policy := workloadPolicy(t, first)
	if policy.Disabled || policy.WorkloadEntitlement == nil || len(policy.Licenses) != 1 || policy.Licenses[0].ValidUntil == nil ||
		!p.ValidUntil.Equal(policy.Licenses[0].ValidUntil.Time) || first.NextCaptureAt == nil || first.NextCaptureAt.After(p.ValidUntil) {
		t.Fatal("selected license did not constrain delivery and durable recapture")
	}
	select {
	case <-time.After(time.Until(p.ValidUntil.Add(time.Second))):
	case <-t.Context().Done():
		t.Fatal("cancelled waiting for actual license grace expiry")
	}
	// No DB write: durable timer and current DB time must derive a deny.
	if v, a := captureAcknowledgement(t, pool, "employee"); v != first.SourceVersion || a != v {
		t.Fatal("natural expiry unexpectedly depended on a source mutation")
	}
	next := captureBindings(t, store, "employee")
	_, policy = workloadPolicy(t, next)
	if next.SourceVersion != first.SourceVersion+1 || !policy.Disabled || policy.Retired || policy.WorkloadEntitlement != nil || next.NextCaptureAt != nil {
		t.Fatal("license naturally expired without a bounded recoverable deny")
	}
}

func TestWorkloadLicenseUnrelatedChangePreservesPublicationWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	seedWorkloadLicense(t, pool)
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", workloadObservation(), time.Minute); err != nil {
		t.Fatal(err)
	}
	p, _ := workloadPolicy(t, captureBindings(t, store, "employee"))
	sourceExec(t, pool, `UPDATE licenses SET features=ARRAY['enterprise.models'] WHERE license_id='grace'`)
	unchanged, policy := workloadPolicy(t, captureBindings(t, store, "employee"))
	if unchanged.OperationID != p.OperationID || unchanged.ContentHash != p.ContentHash || policy.WorkloadEntitlement.LicenseID != "perpetual" {
		t.Fatal("unrelated eligible license reset a valid exact workload publication")
	}
	sourceExec(t, pool, `UPDATE licenses SET status='revoked',revoked_at=now() WHERE license_id='perpetual'`)
	replaced, policy := workloadPolicy(t, captureBindings(t, store, "employee"))
	if replaced.Sequence != p.Sequence+1 || replaced.ContentHash == p.ContentHash || policy.Disabled || policy.WorkloadEntitlement.LicenseID != "grace" || len(policy.Licenses) != 1 {
		t.Fatal("replacement license did not require a new exact publication")
	}
}
