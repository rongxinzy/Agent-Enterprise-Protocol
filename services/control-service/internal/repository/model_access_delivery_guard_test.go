package repository

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestAccessDeliveryChecksLiveExactClaimWithPostgres(t *testing.T) {
	store, pool := accessLedgerTestStore(t)
	in := accessTestIntent("alice")
	queueAccess(t, store, in)
	claim := claimAccess(t, store)
	if err := store.CheckModelAccessDelivery(t.Context(), *claim); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*ModelAccessDelivery){
		func(v *ModelAccessDelivery) { v.DeploymentID = "foreign" },
		func(v *ModelAccessDelivery) { v.ClaimToken = "invalid" },
		func(v *ModelAccessDelivery) { v.ClaimToken = uuid.NewString() },
		func(v *ModelAccessDelivery) { v.SubjectID = "bob" },
		func(v *ModelAccessDelivery) { v.Sequence++ },
		func(v *ModelAccessDelivery) { v.Revision = "other" },
		func(v *ModelAccessDelivery) { v.ContentHash = "other" },
	} {
		value := *claim
		edit(&value)
		if err := store.CheckModelAccessDelivery(t.Context(), value); !errors.Is(err, ErrPublicationClaimLost) {
			t.Fatalf("mismatched claim = %v", err)
		}
	}
	if err := New(store.db).Deployment("foreign").CheckModelAccessDelivery(t.Context(), *claim); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("cross-deployment claim accepted")
	}
	changeAccessModels(&in)
	queueAccess(t, store, in)
	if err := store.CheckModelAccessDelivery(t.Context(), *claim); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("superseded claim remained live")
	}
	current := claimAccess(t, store)
	ledgerExec(t, pool, `UPDATE model_access_publication_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE operation_id=$1`, current.OperationID)
	if err := store.CheckModelAccessDelivery(t.Context(), *current); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("expired claim remained live")
	}
	recovered := claimAccess(t, store)
	if err := store.CheckModelAccessDelivery(t.Context(), *recovered); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishModelAccessPublication(t.Context(), recovered.OperationID, recovered.ClaimToken, "projected", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckModelAccessDelivery(t.Context(), *recovered); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("completed claim reusable for another write")
	}
}
