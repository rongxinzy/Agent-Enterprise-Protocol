package repository

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/types"
)

func TestModelAccessWorkloadRejectsInheritedStaleSnapshotWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	err := store.db.Transaction(func(tx *gorm.DB) error {
		_, err := New(tx).Deployment("source").RegisterModelAccessWorkload(t.Context(), "employee", "", workloadObservation(), time.Minute)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if !errors.Is(err, ErrAccessBinding) || len(readSource(t, store, "employee").Workloads) != 0 {
		t.Fatal("inherited stale snapshot could register a workload", err)
	}
}

func TestModelAccessWorkloadRejectsHumanAndForeignOwnershipWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	w := workloadObservation()
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "alice", "", w, time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("human owner accepted as employee identity")
	}
	sourceExec(t, pool, `INSERT INTO users(id,deployment_id,username,display_name,password_hash,require_password_change,kind)
VALUES('other-agent','source','other-agent','Other','test-only-unused',false,'agent'),('foreign-agent','foreign','foreign-agent','Foreign','test-only-unused',false,'agent')`)
	sourceExec(t, pool, `INSERT INTO teams(deployment_id,id,name,enabled) VALUES('foreign','team','Team',true)`)
	sourceExec(t, pool, `INSERT INTO agent_profiles(deployment_id,user_id,home_team_id)
VALUES('source','other-agent','team'),('foreign','foreign-agent','team')`)
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"other-agent", "foreign-agent"} {
		target := store
		if agent == "foreign-agent" {
			target = New(store.db).Deployment("foreign")
		}
		if _, err := target.RegisterModelAccessWorkload(t.Context(), agent, "", w, time.Minute); !errors.Is(err, ErrAccessWorkloadConflict) {
			t.Fatal("employee UID crossed principal or deployment")
		}
		// A forged employee UID must not bypass the global historical SA fence.
		forged := w
		forged.Identity.EmployeeUID = types.UID(agent + "-uid")
		if _, err := target.RegisterModelAccessWorkload(t.Context(), agent, "", forged, time.Minute); !errors.Is(err, ErrAccessWorkloadConflict) {
			t.Fatal("SA UID was reused by another principal")
		}
	}
	if err := store.RetireModelAccessWorkload(t.Context(), "other-agent", key); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign agent retired the owner's identity")
	}
	sourceExec(t, pool, `DELETE FROM users WHERE id='employee'`)
	if source := readSource(t, store, "employee"); source.Exists || !source.Disabled || len(source.Workloads) != 0 {
		t.Fatal("deleted employee remains authorized")
	}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "other-agent", "", w, time.Minute); !errors.Is(err, ErrAccessWorkloadConflict) {
		t.Fatal("account deletion discarded identity ownership history")
	}
}

func TestModelAccessWorkloadCompareAndSwapWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	w := workloadObservation()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			candidate := w
			candidate.Identity.ServiceAccountUID = types.UID([]string{"sa-a", "sa-b"}[i])
			_, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", candidate, time.Minute)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessWorkloadConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 || len(readSource(t, store, "employee").Workloads) != 1 {
		t.Fatal("concurrent first registration silently replaced a winner")
	}
}

func TestModelAccessWorkloadRejectsInvalidInput(t *testing.T) {
	var store *DeploymentStore
	for _, validFor := range []time.Duration{0, time.Second, time.Hour + time.Second} {
		if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", workloadObservation(), validFor); !errors.Is(err, ErrAccessBinding) {
			t.Fatal("invalid verification cap accepted")
		}
	}
	for _, alter := range []func(*ObservedModelWorkload){
		func(w *ObservedModelWorkload) { w.EmployeeName = "../foreign" },
		func(w *ObservedModelWorkload) { w.Identity.Namespace = "" },
		func(w *ObservedModelWorkload) { w.Identity.ServiceAccountName = "Wrong Case" },
		func(w *ObservedModelWorkload) { w.Identity.ServiceAccountUID = "" },
		func(w *ObservedModelWorkload) { w.Identity.EmployeeUID = "uid\n" },
		func(w *ObservedModelWorkload) { w.Identity.Audience = " " + w.Identity.Audience },
	} {
		w := workloadObservation()
		alter(&w)
		if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute); !errors.Is(err, ErrAccessBinding) {
			t.Fatal("invalid Kubernetes identity accepted")
		}
	}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "", "", workloadObservation(), time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("empty agent accepted")
	}
	if err := store.RetireModelAccessWorkload(t.Context(), "employee", ""); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("unconditional retirement accepted")
	}
}
