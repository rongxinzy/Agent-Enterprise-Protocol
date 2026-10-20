package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func accessLedgerTestStore(t *testing.T) (*DeploymentStore, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("AEP_REFERENCE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AEP_REFERENCE_TEST_DATABASE_URL to a disposable PostgreSQL server")
	}
	admin, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "access_ledger_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP DATABASE `+name); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	ledgerExec(t, pool, `INSERT INTO deployments(id,name) VALUES('ledger','Test'),('foreign','Other')`)
	return New(orm).Deployment("ledger"), pool
}

func ledgerExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func accessTestHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func accessTestIntent(subject string) ModelAccessIntent {
	payload := json.RawMessage(`{ "models": ["chat-a"], "disabled": false }`)
	now := time.Now().UTC().Truncate(time.Microsecond)
	return ModelAccessIntent{BindingKey: accessTestHash([]byte(subject + "/session")), SubjectID: subject,
		SourceVersion: 1, IssuedAt: now.Add(-time.Second), ValidUntil: now.Add(20 * time.Minute),
		Snapshot: PublicationSnapshot{Revision: "test", ContentHash: accessTestHash(payload), Payload: payload}}
}

func queueAccess(t *testing.T, store *DeploymentStore, in ModelAccessIntent) ModelAccessPublication {
	t.Helper()
	value, err := store.QueueModelAccessPublication(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func claimAccess(t *testing.T, store *DeploymentStore) *ModelAccessDelivery {
	t.Helper()
	value, err := store.ClaimModelAccessPublication(t.Context())
	if err != nil || value == nil {
		t.Fatalf("expected a current delivery, error=%v", err)
	}
	return value
}

func changeAccessModels(in *ModelAccessIntent) {
	in.SourceVersion++
	in.Snapshot.Payload = json.RawMessage(`{"models":[],"disabled":true}`)
	in.Snapshot.ContentHash = accessTestHash(in.Snapshot.Payload)
	in.Snapshot.Revision = "revoked"
}

func TestAccessLedgerVersionsAndUnchangedOutputWithPostgres(t *testing.T) {
	store, _ := accessLedgerTestStore(t)
	in := accessTestIntent("alice")
	first := queueAccess(t, store, in)
	if first.Sequence != 1 {
		t.Fatal("first binding sequence is not one")
	}
	in.SourceVersion++
	in.Snapshot.Revision = "cosmetic-recapture"
	unchanged := queueAccess(t, store, in)
	if unchanged.OperationID != first.OperationID || unchanged.Sequence != first.Sequence {
		t.Fatal("unchanged effective output created a new publication")
	}
	in.SourceVersion--
	if _, err := store.QueueModelAccessPublication(t.Context(), in); !errors.Is(err, ErrAccessPublicationStale) {
		t.Fatal("old source version was replayed after unchanged acknowledgement")
	}
	in.SourceVersion++
	in.Snapshot.Payload = json.RawMessage(`{"models":[],"disabled":true}`)
	in.Snapshot.ContentHash = accessTestHash(in.Snapshot.Payload)
	if _, err := store.QueueModelAccessPublication(t.Context(), in); !errors.Is(err, ErrAccessPublicationConflict) {
		t.Fatal("same source version changed permissions")
	}
	in.SourceVersion++
	second := queueAccess(t, store, in)
	if second.Sequence != 2 {
		t.Fatal("changed output did not advance only its own stream")
	}
	bob := queueAccess(t, store, accessTestIntent("bob"))
	if bob.Sequence != 1 {
		t.Fatal("another subject inherited Alice's sequence")
	}
	if current, err := store.LatestModelAccessPublication(t.Context(), in.BindingKey); err != nil || current.OperationID != second.OperationID {
		t.Fatal("Bob's intent replaced Alice's latest publication")
	}
	in.SubjectID = "attacker"
	in.SourceVersion++
	if _, err := store.QueueModelAccessPublication(t.Context(), in); !errors.Is(err, ErrAccessPublicationConflict) {
		t.Fatal("stream was rebound to another subject")
	}
}

func TestAccessLedgerAtomicCaptureAndImmutableBytesWithPostgres(t *testing.T) {
	store, pool := accessLedgerTestStore(t)
	in := accessTestIntent("alice")
	abort := errors.New("abort source acknowledgement")
	if err := store.WithTransaction(t.Context(), func(tx *DeploymentStore) error {
		if _, err := tx.QueueModelAccessPublication(t.Context(), in); err != nil {
			return err
		}
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	if _, err := store.LatestModelAccessPublication(t.Context(), in.BindingKey); !errors.Is(err, ErrNotFound) {
		t.Fatal("rolled-back capture escaped its producer transaction")
	}
	var rows int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_access_streams)+(SELECT count(*) FROM model_access_publications)+(SELECT count(*) FROM model_access_publication_outbox)`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("rollback left a partial stream, snapshot or outbox")
	}
	first := queueAccess(t, store, in)
	read, err := store.LatestModelAccessPublication(t.Context(), in.BindingKey)
	if err != nil || !bytes.Equal(read.Payload, in.Snapshot.Payload) || accessTestHash(read.Payload) != first.ContentHash {
		t.Fatal("storage normalization changed immutable JSON bytes/hash")
	}
	for _, sql := range []string{
		`UPDATE model_access_publications SET payload=convert_to('{}','UTF8')`,
		`UPDATE model_access_publications SET valid_until=valid_until+interval '1 minute'`,
		`UPDATE model_access_publications SET subject_id='other'`,
		`UPDATE model_access_streams SET last_sequence=0`,
		`UPDATE model_access_streams SET last_source_version=0`,
		`DELETE FROM model_access_streams`,
	} {
		if _, err := pool.Exec(t.Context(), sql); err == nil {
			t.Fatalf("immutable fence accepted mutation: %s", sql)
		}
	}
	ledgerExec(t, pool, `DELETE FROM deployments WHERE id='ledger'`)
	if _, err := store.LatestModelAccessPublication(t.Context(), in.BindingKey); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed deployment retained stream artifacts")
	}
}

func TestAccessLedgerClaimsSupersessionAndCrashRecoveryWithPostgres(t *testing.T) {
	store, pool := accessLedgerTestStore(t)
	inputs := map[string]ModelAccessIntent{"alice": accessTestIntent("alice"), "bob": accessTestIntent("bob")}
	for _, in := range inputs {
		queueAccess(t, store, in)
	}
	old, other := claimAccess(t, store), claimAccess(t, store)
	if old.BindingKey == other.BindingKey {
		t.Fatal("live claim was assigned twice")
	}
	if value, err := store.ClaimModelAccessPublication(t.Context()); err != nil || value != nil {
		t.Fatal("third worker stole a live claim")
	}
	newInput := inputs[old.SubjectID]
	changeAccessModels(&newInput)
	newest := queueAccess(t, store, newInput)
	if err := store.FinishModelAccessPublication(t.Context(), old.OperationID, old.ClaimToken, "projected", "", 0); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("superseded worker acknowledged stale permissions")
	}
	if err := store.FinishModelAccessPublication(t.Context(), other.OperationID, other.ClaimToken, "projected", "", 0); err != nil {
		t.Fatal("unrelated binding's claim was invalidated", err)
	}
	if current, err := store.IsLatestModelAccessPublication(t.Context(), other.ModelAccessPublication); err != nil || !current {
		t.Fatal("unrelated projected stream lost current evidence")
	}
	claim := claimAccess(t, store)
	if claim.OperationID != newest.OperationID {
		t.Fatal("worker fell back to an older snapshot")
	}
	foreign := &DeploymentStore{db: store.db, deploymentID: "foreign"}
	if err := foreign.FinishModelAccessPublication(t.Context(), claim.OperationID, claim.ClaimToken, "projected", "", 0); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("another deployment acknowledged a claim")
	}
	if current, err := foreign.IsLatestModelAccessPublication(t.Context(), other.ModelAccessPublication); err != nil || current {
		t.Fatal("foreign publication counted as current")
	}
	ledgerExec(t, pool, `UPDATE model_access_publication_outbox SET lease_until=now()-interval '1 second' WHERE operation_id=$1`, claim.OperationID)
	if err := store.FinishModelAccessPublication(t.Context(), claim.OperationID, claim.ClaimToken, "projected", "", 0); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("expired worker acknowledged its former claim")
	}
	recovered := claimAccess(t, store)
	if recovered.OperationID != claim.OperationID || recovered.ClaimToken == claim.ClaimToken {
		t.Fatal("crash recovery did not retain input and replace claim ownership")
	}
	if err := store.FinishModelAccessPublication(t.Context(), recovered.OperationID, recovered.ClaimToken, "", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if value, err := store.ClaimModelAccessPublication(t.Context()); err != nil || value != nil {
		t.Fatal("retry bypassed backoff")
	}
	ledgerExec(t, pool, `UPDATE model_access_publication_outbox SET next_attempt_at=now() WHERE operation_id=$1`, recovered.OperationID)
	retry := claimAccess(t, store)
	if err := store.FinishModelAccessPublication(t.Context(), retry.OperationID, retry.ClaimToken, "failed", "CR_OWNERSHIP_CONFLICT", 0); err != nil {
		t.Fatal(err)
	}
	if same := queueAccess(t, store, newInput); same.OperationID != retry.OperationID {
		t.Fatal("identical source retry bypassed terminal result")
	}
	newInput.SourceVersion++
	repair := queueAccess(t, store, newInput)
	if repair.Sequence <= retry.Sequence {
		t.Fatal("new source work could not retry a terminally failed publication")
	}
	final := claimAccess(t, store)
	if err := store.FinishModelAccessPublication(t.Context(), final.OperationID, final.ClaimToken, "projected", "", 0); err != nil {
		t.Fatal(err)
	}
}

func TestAccessLedgerConcurrentIdempotencyAndIndependentClaimsWithPostgres(t *testing.T) {
	store, pool := accessLedgerTestStore(t)
	in := accessTestIntent("alice")
	var wg sync.WaitGroup
	operations := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			value, err := store.QueueModelAccessPublication(t.Context(), in)
			if err != nil {
				t.Error(err)
				return
			}
			operations <- value.OperationID
		})
	}
	wg.Wait()
	close(operations)
	unique := map[string]bool{}
	for operation := range operations {
		unique[operation] = true
	}
	if len(unique) != 1 {
		t.Fatal("concurrent identical captures minted duplicate publications")
	}
	queueAccess(t, store, accessTestIntent("bob"))
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM model_access_streams WHERE binding_key=$1 FOR UPDATE`, in.BindingKey); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	value, err := store.ClaimModelAccessPublication(ctx)
	if err != nil || value == nil || value.SubjectID != "bob" {
		t.Fatal("busy Alice stream blocked an unrelated binding", err)
	}
}

func TestAccessLedgerExpiredSnapshotWithPostgres(t *testing.T) {
	store, pool := accessLedgerTestStore(t)
	in := accessTestIntent("alice")
	in.ValidUntil = time.Now().UTC().Add(2 * time.Second).Truncate(time.Microsecond)
	queueAccess(t, store, in)
	claim := claimAccess(t, store)
	timer := time.NewTimer(time.Until(in.ValidUntil) + 30*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("expiry test cancelled")
	}
	if err := store.FinishModelAccessPublication(t.Context(), claim.OperationID, claim.ClaimToken, "projected", "", 0); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("expired input was acknowledged as current")
	}
	ledgerExec(t, pool, `UPDATE model_access_publication_outbox SET lease_until=now()-interval '1 second'`)
	if value, err := store.ClaimModelAccessPublication(t.Context()); err != nil || value != nil {
		t.Fatal("expired allow was redelivered")
	}
	latest, err := store.LatestModelAccessPublication(t.Context(), in.BindingKey)
	if err != nil || latest.Phase != "failed" || latest.ErrorCode == nil || *latest.ErrorCode != "ACCESS_SNAPSHOT_EXPIRED" {
		t.Fatal("expired input has no explicit failure evidence")
	}
	if current, err := store.IsLatestModelAccessPublication(t.Context(), claim.ModelAccessPublication); err != nil || current {
		t.Fatal("expired publication counted as current")
	}
}

func TestAccessLedgerRejectsInvalidInputsWithPostgres(t *testing.T) {
	store, pool := accessLedgerTestStore(t)
	for _, tt := range []struct {
		name   string
		mutate func(*ModelAccessIntent)
	}{
		{"hash mismatch", func(in *ModelAccessIntent) { in.Snapshot.ContentHash = strings.Repeat("0", 64) }},
		{"non object", func(in *ModelAccessIntent) {
			in.Snapshot.Payload = json.RawMessage(`[]`)
			in.Snapshot.ContentHash = accessTestHash(in.Snapshot.Payload)
		}},
		{"foreign snapshot", func(in *ModelAccessIntent) { in.Snapshot.DeploymentID = "foreign" }},
		{"invalid binding", func(in *ModelAccessIntent) { in.BindingKey = "not-a-binding-digest" }},
		{"missing subject", func(in *ModelAccessIntent) { in.SubjectID = "" }},
		{"missing revision", func(in *ModelAccessIntent) { in.Snapshot.Revision = "" }},
		{"zero source", func(in *ModelAccessIntent) { in.SourceVersion = 0 }},
		{"too long validity", func(in *ModelAccessIntent) { in.ValidUntil = in.IssuedAt.Add(2 * time.Hour) }},
		{"future issue", func(in *ModelAccessIntent) { in.IssuedAt = time.Now().Add(time.Minute) }},
		{"expired input", func(in *ModelAccessIntent) { in.ValidUntil = in.IssuedAt.Add(time.Millisecond) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := accessTestIntent("invalid")
			tt.mutate(&in)
			if _, err := store.QueueModelAccessPublication(t.Context(), in); !errors.Is(err, ErrAccessPublicationInput) {
				t.Fatalf("invalid input returned %v", err)
			}
		})
	}
	var rows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM model_access_streams`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("rejected input left a binding stream")
	}
	if _, err := store.LatestModelAccessPublication(t.Context(), strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing binding returned an intent")
	}
	if err := store.FinishModelAccessPublication(t.Context(), "not-an-operation", "not-a-token", "projected", "", 0); !errors.Is(err, ErrPublicationClaimLost) {
		t.Fatal("malformed claim identifiers accepted")
	}
	if err := store.FinishModelAccessPublication(t.Context(), uuid.NewString(), uuid.NewString(), "failed", "raw sensitive error", 0); err == nil {
		t.Fatal("unstructured error was accepted for persistent status")
	}
}
