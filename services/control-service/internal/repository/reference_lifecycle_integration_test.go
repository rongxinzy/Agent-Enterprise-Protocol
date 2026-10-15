package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Uses an isolated database on an explicitly provided disposable server. Unit
// coverage alone cannot prove trigger locking, rollback or JSONB semantics.
func TestGatewayReferenceLifecycleWithPostgres(t *testing.T) {
	url := os.Getenv("AEP_REFERENCE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AEP_REFERENCE_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "reference_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := admin.Exec(context.Background(), `DROP DATABASE `+schema)
		if err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO deployments(id,name) VALUES('reference-test','Test')`)
	exec(`INSERT INTO roles(deployment_id,id,name) VALUES('reference-test','role','Role')`)
	exec(`INSERT INTO teams(deployment_id,id,name) VALUES('reference-test','team','Team')`)
	exec(`INSERT INTO users(id,deployment_id,username,display_name,password_hash) VALUES('reference-user','reference-test','user','User','test-only-unused')`)
	for _, id := range []string{"gone", "kept"} {
		exec(`INSERT INTO models(deployment_id,id,display_name,source_type,protocol,endpoint,upstream_model) VALUES('reference-test',$1,$1,'gateway','openai-compatible','http://mock.invalid/v1','chat')`, id)
	}
	for _, scope := range []string{"user", "team", "role", "model", "global"} {
		c := gatewaypolicy.Configuration{Kind: "requests", ScopeType: scope, Maximum: 7, Interval: "minute", Enabled: true}
		id := map[string]string{"user": "reference-user", "team": "team", "role": "role", "model": "gone"}[scope]
		if id != "" {
			c.ScopeID = &id
		}
		b, _ := json.Marshal(c)
		exec(`INSERT INTO gateway_limits(deployment_id,id,version,configuration) VALUES('reference-test',$1,1,$2)`, "rule-"+scope, b)
	}
	store := New(orm).Deployment("reference-test")
	queue := func(key string) DataPlanePublication {
		t.Helper()
		value, err := store.QueueGatewayPublication(ctx, invalidationDigest([]byte(key)), invalidationDigest([]byte("request")), "", true, func(models []Model, limits *[]gatewaypolicy.Limit) (PublicationSnapshot, error) {
			state := invalidationSnapshot{Revision: key, GatewayLimits: limits, Routes: []gatewayrender.Route{}}
			for _, m := range models {
				state.Routes = append(state.Routes, gatewayrender.Route{ModelID: m.ID, Enabled: m.Enabled, Endpoint: m.Endpoint.String, Protocol: m.Protocol, UpstreamModel: m.UpstreamModel.String})
			}
			b, e := json.Marshal(state)
			return PublicationSnapshot{Revision: key, ContentHash: invalidationDigest(b), Payload: b}, e
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := queue("selected")
	// An unrelated draft must not be selected by the automatic safety update.
	exec(`INSERT INTO gateway_limits(deployment_id,id,version,configuration) VALUES('reference-test','unpublished',1,'{"kind":"requests","scopeType":"global","maximum":99,"interval":"minute","enabled":true}')`)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM roles WHERE deployment_id='reference-test' AND id='role'`); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	var version int64
	var deleted bool
	if err := pool.QueryRow(ctx, `SELECT version,deleted FROM gateway_limits WHERE deployment_id='reference-test' AND id='rule-role'`).Scan(&version, &deleted); err != nil {
		t.Fatal(err)
	}
	if version != 1 || deleted {
		t.Fatal("rolled-back deletion invalidated the rule")
	}
	for _, table := range []string{"roles", "teams", "users", "models"} {
		id := map[string]string{"roles": "role", "teams": "team", "users": "reference-user", "models": "gone"}[table]
		exec(`DELETE FROM `+table+` WHERE deployment_id='reference-test' AND id=$1`, id)
	}
	// Recreate before consuming the signal: old model intent must remain fenced.
	exec(`INSERT INTO models(deployment_id,id,display_name,source_type,protocol,endpoint,upstream_model) VALUES('reference-test','gone','Recreated','gateway','openai-compatible','http://new.invalid/v1','new')`)
	exec(`INSERT INTO roles(deployment_id,id,name) VALUES('reference-test','role','Recreated role')`)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Go(func() {
			if err := store.QueueReferenceInvalidations(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var latest DataPlanePublication
	if err := orm.Where("deployment_id=?", "reference-test").Order("sequence DESC").Take(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.Sequence <= original.Sequence {
		t.Fatal("no automatic replacement was queued")
	}
	input, err := store.ReadDataPlanePublicationInputs(ctx, latest)
	if err != nil {
		t.Fatal(err)
	}
	var state invalidationSnapshot
	if err = json.Unmarshal(input.Payload, &state); err != nil {
		t.Fatal(err)
	}
	for _, route := range state.Routes {
		if route.Enabled != (route.ModelID == "kept") {
			t.Fatal("model ID reuse reopened old route")
		}
	}
	if state.GatewayLimits == nil || len(*state.GatewayLimits) != 5 {
		t.Fatal("unpublished rule draft was captured")
	}
	for _, rule := range *state.GatewayLimits {
		if rule.Configuration.Enabled != (rule.ID == "rule-global") {
			t.Fatal("reference rule was not invalidated")
		}
	}
	if err := store.QueueReferenceInvalidations(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	orm.Model(&DataPlanePublication{}).Where("deployment_id=?", "reference-test").Count(&count)
	if count != 2 {
		t.Fatalf("concurrent/repeated consumers created %d publications", count)
	}
	replay, err := store.QueueGatewayPublication(ctx, invalidationDigest([]byte("selected")), invalidationDigest([]byte("request")), "", true, func([]Model, *[]gatewaypolicy.Limit) (PublicationSnapshot, error) {
		t.Fatal("replay rederived current references")
		return PublicationSnapshot{}, nil
	})
	if err != nil || replay.OperationID != original.OperationID {
		t.Fatal("immutable replay lost")
	}
	// Explicit administrator selection may publish the new model, while the old
	// rule remains a tombstone despite reusing the role identifier.
	newSelection := queue("explicit-new-selection")
	input, err = store.ReadDataPlanePublicationInputs(ctx, newSelection)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(input.Payload, &state)
	for _, route := range state.Routes {
		if !route.Enabled {
			t.Fatal("explicit replacement model could not be selected")
		}
	}
	for _, rule := range *state.GatewayLimits {
		if rule.ID == "rule-role" && rule.Configuration.Enabled {
			t.Fatal("recreated role inherited its predecessor's rule")
		}
	}
}
