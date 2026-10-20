package repository

import (
	"testing"
	"time"
)

func TestModelAccessWorkloadLookupScopesAndRetiredHistoryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	w := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Observation expiry pauses permissions, but the current CAS key must remain
	// discoverable so direct positive evidence can safely renew that same owner.
	sourceExec(t, pool, `UPDATE model_access_workloads SET verified_at=clock_timestamp()-interval '2 minutes',verified_until=clock_timestamp()-interval '1 minute' WHERE deployment_id='source'`)
	rows, err := store.ModelAccessWorkloadRegistrations(t.Context(), w.Identity.Namespace, w.EmployeeName)
	if err != nil || len(rows) != 1 || rows[0].UserID != "employee" || rows[0].BindingKey != key {
		t.Fatal("expired current binding lost CAS identity", rows, err)
	}
	for _, scope := range []struct {
		store           *DeploymentStore
		namespace, name string
	}{
		{New(store.db).Deployment("foreign"), w.Identity.Namespace, w.EmployeeName},
		{store, "foreign", w.EmployeeName}, {store, w.Identity.Namespace, "foreign"},
	} {
		rows, err := scope.store.ModelAccessWorkloadRegistrations(t.Context(), scope.namespace, scope.name)
		if err != nil || len(rows) != 0 {
			t.Fatal("registry lookup crossed scope", rows, err)
		}
	}
	if err := store.RetireModelAccessWorkload(t.Context(), "employee", key); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ModelAccessWorkloadRegistrations(t.Context(), w.Identity.Namespace, w.EmployeeName)
	if err != nil || len(rows) != 0 {
		t.Fatal("retired history was offered for resurrection", rows, err)
	}
}
