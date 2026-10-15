package repository

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func TestReferenceInvalidationOnlyWithdrawsPreviouslySelectedIntent(t *testing.T) {
	limits := []gatewaypolicy.Limit{{ID: "removed-role", Version: 1, Configuration: gatewaypolicy.Configuration{Enabled: true, Maximum: 7}}, {ID: "other", Version: 4, Configuration: gatewaypolicy.Configuration{Enabled: true, Maximum: 11}}}
	state := invalidationSnapshot{Revision: "original", Routes: []gatewayrender.Route{{ModelID: "deleted", Enabled: true, Endpoint: "https://old.invalid/v1"}, {ModelID: "reused", Enabled: true}, {ModelID: "kept", Enabled: true}}, GatewayLimits: &limits}
	before, _ := json.Marshal(state)
	fences := []GatewayLimit{{ID: "removed-role", Version: 2}, {ID: "unpublished", Version: 3}, {ID: "other", Version: 3}}
	if !invalidateSelected(&state, []string{"reused", "kept", "unpublished-model"}, []string{"reused"}, fences) {
		t.Fatal("missing invalidation")
	}
	if state.Routes[0].Enabled || state.Routes[1].Enabled || !state.Routes[2].Enabled || len(state.Routes) != 3 {
		t.Fatal("model removal/reuse fencing failed")
	}
	if len(limits) != 2 || limits[0].Configuration.Enabled || limits[0].Version != 2 || limits[0].Configuration.Maximum != 7 || !limits[1].Configuration.Enabled || limits[1].Version != 4 {
		t.Fatal("new/unrelated drafts were selected or rule identity changed")
	}
	if invalidateSelected(&state, []string{"reused", "kept"}, []string{"reused"}, fences) {
		t.Fatal("replay changed already fenced intent")
	}
	var original invalidationSnapshot
	_ = json.Unmarshal(before, &original)
	if !original.Routes[0].Enabled || !(*original.GatewayLimits)[0].Configuration.Enabled {
		t.Fatal("original captured intent was mutated")
	}
	if state.Routes[0].Endpoint != original.Routes[0].Endpoint {
		t.Fatal("invalidation rewrote configuration")
	}
	if invalidateSelected(&invalidationSnapshot{}, nil, nil, nil) {
		t.Fatal("empty route-only intent changed")
	}
}

func TestReferenceInvalidationRetainsSignalOnDatabaseFailure(t *testing.T) {
	for _, stage := range []string{"busy", "lock-error", "native-error", "signal-error", "idle", "history-error", "invalid-history"} {
		t.Run(stage, func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectBegin()
			locked := mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`)
			failure := errors.New("unavailable")
			if stage == "lock-error" {
				locked.WillReturnError(failure)
			} else {
				locked.WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(stage != "busy"))
			}
			if stage != "busy" && stage != "lock-error" {
				native := mock.ExpectExec(`SELECT pg_advisory_xact_lock`)
				if stage == "native-error" {
					native.WillReturnError(failure)
				} else {
					native.WillReturnResult(sqlmock.NewResult(0, 1))
					signal := mock.ExpectQuery(`SELECT version FROM gateway_reference_invalidations`)
					if stage == "signal-error" {
						signal.WillReturnError(failure)
					} else {
						version := int64(1)
						if stage == "idle" {
							version = 0
						}
						signal.WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
						if stage != "idle" {
							history := mock.ExpectQuery(`SELECT s.payload,p.sequence`)
							if stage == "history-error" {
								history.WillReturnError(failure)
							} else {
								history.WillReturnRows(sqlmock.NewRows([]string{"payload", "sequence"}).AddRow([]byte(`{invalid`), 1))
							}
						}
					}
				}
			}
			ok := stage == "busy" || stage == "idle"
			if ok {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := store.Deployment("deployment-a").QueueReferenceInvalidations(t.Context())
			if (err == nil) != ok {
				t.Fatalf("stage %s: %v", stage, err)
			}
		})
	}
}
