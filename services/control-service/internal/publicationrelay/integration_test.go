package publicationrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

// The real API Server suite runs in ordinary CI using its pinned envtest assets.
// Setting AEP_PUBLICATION_RELAY_TEST_DATABASE_URL additionally exercises the
// real PostgreSQL outbox; use an empty disposable database, never customer data.
func TestPublicationRelayWithEnvtest(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("set KUBEBUILDER_ASSETS to CI-pinned envtest binaries")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets, CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "gateway-operator", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := kube.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "aep-system"}}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "aep-system"}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "demo"}}); err != nil {
		t.Fatal(err)
	}
	p := &Projector{Client: kube, Namespace: "aep-system"}
	t.Run("late update is rejected by actual API server", func(t *testing.T) {
		initial := delivery(1, []gatewayrender.Route{testRoute("fenced")}, nil)
		if err := p.Project(ctx, initial); err != nil {
			t.Fatal(err)
		}
		old := delivery(2, []gatewayrender.Route{}, []gatewayrender.Route{testRoute("fenced")})
		newer := delivery(3, []gatewayrender.Route{testRoute("fenced")}, nil)
		paused := &Projector{Client: &faultClient{Client: kube, update: func(ctx context.Context, obj client.Object) error {
			if _, ok := obj.(*gateway.ModelRoute); ok {
				if err := p.Project(ctx, newer); err != nil {
					t.Fatal(err)
				}
			}
			return kube.Update(ctx, obj)
		}}, Namespace: p.Namespace}
		if err := paused.Project(ctx, old); !apierrors.IsConflict(err) {
			t.Fatalf("stale update = %v", err)
		}
		if err := p.Project(ctx, old); !errors.Is(err, ErrSuperseded) {
			t.Fatalf("old replay = %v", err)
		}
	})
	t.Run("late create is rejected after removed-route guard", func(t *testing.T) {
		old := delivery(4, []gatewayrender.Route{testRoute("removed")}, nil)
		newer := delivery(5, []gatewayrender.Route{}, []gatewayrender.Route{testRoute("removed")})
		paused := &Projector{Client: &faultClient{Client: kube, create: func(ctx context.Context, obj client.Object) error {
			if err := p.Project(ctx, newer); err != nil {
				t.Fatal(err)
			}
			return kube.Create(ctx, obj)
		}}, Namespace: p.Namespace}
		if err := paused.Project(ctx, old); !apierrors.IsAlreadyExists(err) {
			t.Fatalf("stale create = %v", err)
		}
	})
	url := os.Getenv("AEP_PUBLICATION_RELAY_TEST_DATABASE_URL")
	if url == "" {
		t.Log("PostgreSQL acceptance requires an explicit disposable database URL")
		return
	}
	t.Run("PostgreSQL restart recovery and claims", func(t *testing.T) { testPostgresRelay(t, kube, url) })
}

func testPostgresRelay(t *testing.T, kube client.Client, url string) {
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.New(orm)
	if _, err := repo.UpsertDeployment(ctx, repository.Deployment{ID: "demo", Name: "Relay acceptance"}); err != nil {
		t.Fatal(err)
	}
	store := repo.Deployment("demo")
	// Reset this suite's API fixtures, including the monotonically stamped
	// aggregate. It has no running operator/finalizers or external resources.
	var routes gateway.ModelRouteList
	if err := kube.List(ctx, &routes, client.InNamespace("aep-system")); err != nil {
		t.Fatal(err)
	}
	for i := range routes.Items {
		if err := kube.Delete(ctx, &routes.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
	var dc gateway.DeploymentConfig
	key := client.ObjectKey{Namespace: "aep-system", Name: "deployment"}
	if err := kube.Get(ctx, key, &dc); err != nil {
		t.Fatal(err)
	}
	dc.Annotations = nil
	dc.Labels = nil
	if err := kube.Update(ctx, &dc); err != nil {
		t.Fatal(err)
	}
	queue := func(seq int64, models []gatewayrender.Route) repository.DataPlanePublication {
		t.Helper()
		fixture := delivery(seq, models, nil)
		value, err := store.QueueDataPlanePublication(ctx, publicationHash(fixture.OperationID), publicationHash("{}"), "", func([]repository.Model) (repository.PublicationSnapshot, error) {
			return repository.PublicationSnapshot{Revision: fixture.Revision, ContentHash: fixture.ContentHash, Payload: fixture.Payload}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := queue(1, []gatewayrender.Route{testRoute("chat")})
	var group sync.WaitGroup
	claims := make(chan *repository.PublicationDelivery, 10)
	for i := 0; i < 10; i++ {
		group.Go(func() {
			item, err := store.ClaimDataPlanePublication(ctx)
			if err != nil {
				t.Error(err)
			}
			if item != nil {
				claims <- item
			}
		})
	}
	group.Wait()
	close(claims)
	var old *repository.PublicationDelivery
	count := 0
	for item := range claims {
		old = item
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent claims = %d", count)
	}
	p := &Projector{Client: kube, Namespace: "aep-system"}
	if err := p.Project(ctx, *old); err != nil {
		t.Fatal(err)
	}
	// Simulate crash after CR projection but before DB acknowledgement, then
	// a restarted replica acquires a different token after the lease expires.
	if err := orm.Exec(`UPDATE data_plane_publication_outbox SET lease_until=now()-interval '1 second'`).Error; err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.Deployment("demo").ClaimDataPlanePublication(ctx)
	if err != nil || recovered == nil || recovered.ClaimToken == old.ClaimToken {
		t.Fatalf("reclaim = %+v %v", recovered, err)
	}
	if err := store.FinishDataPlanePublication(ctx, old.OperationID, old.ClaimToken, "projected", "", 0); !errors.Is(err, repository.ErrPublicationClaimLost) {
		t.Fatalf("old acknowledgement = %v", err)
	}
	if err := p.Project(ctx, *recovered); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDataPlanePublication(ctx, recovered.OperationID, recovered.ClaimToken, "projected", "", 0); err != nil {
		t.Fatal(err)
	}
	if item, err := store.ClaimDataPlanePublication(ctx); err != nil || item != nil {
		t.Fatalf("terminal work reclaimed: %+v %v", item, err)
	}
	second := queue(2, []gatewayrender.Route{testRoute("old")})
	stale, err := store.ClaimDataPlanePublication(ctx)
	if err != nil || stale == nil {
		t.Fatal(err)
	}
	third := queue(3, []gatewayrender.Route{})
	latest, err := store.ClaimDataPlanePublication(ctx)
	if err != nil || latest == nil {
		t.Fatal(err)
	}
	if err := p.Project(ctx, *latest); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDataPlanePublication(ctx, stale.OperationID, stale.ClaimToken, "projected", "", 0); !errors.Is(err, repository.ErrPublicationClaimLost) {
		t.Fatal("superseded claim was acknowledged")
	}
	if err := p.Project(ctx, *stale); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("stale projection = %v", err)
	}
	if err := store.FinishDataPlanePublication(ctx, latest.OperationID, latest.ClaimToken, "", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if item, err := store.ClaimDataPlanePublication(ctx); err != nil || item != nil {
		t.Fatal("durable backoff ignored")
	}
	if err := orm.Exec(`UPDATE data_plane_publication_outbox SET next_attempt_at=now() WHERE operation_id=?`, third.OperationID).Error; err != nil {
		t.Fatal(err)
	}
	if err := New(repo.Deployment("demo"), p).Step(ctx); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ id, phase string }{{first.OperationID, "projected"}, {second.OperationID, "superseded"}, {third.OperationID, "projected"}} {
		value, err := store.GetDataPlanePublication(ctx, check.id)
		if err != nil || value.Phase != check.phase {
			t.Fatalf("operation = %+v %v", value, err)
		}
	}
	var live gateway.ModelRoute
	if err := kube.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: "model-chat"}, &live); err != nil || live.Spec.Enabled {
		t.Fatal("historical route not disabled")
	}
	// A failed invalid revision must not hide the last valid definition when
	// a later snapshot removes the model. Exercise the actual JSONB history.
	queue(4, []gatewayrender.Route{testRoute("history")})
	if err := New(store, p).Step(ctx); err != nil {
		t.Fatal(err)
	}
	invalid := testRoute("history")
	invalid.Endpoint = "/v1"
	invalidOperation := queue(5, []gatewayrender.Route{invalid})
	if err := New(store, p).Step(ctx); err != nil {
		t.Fatal(err)
	}
	failed, err := store.GetDataPlanePublication(ctx, invalidOperation.OperationID)
	if err != nil || failed.Phase != "failed" || failed.ErrorCode == nil || *failed.ErrorCode != "INVALID_OPERATOR_SNAPSHOT" {
		t.Fatalf("invalid historical operation = %+v %v", failed, err)
	}
	queue(6, []gatewayrender.Route{})
	if err := New(store, p).Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: "model-history"}, &live); err != nil || live.Spec.Enabled {
		t.Fatal("invalid later revision hid an active historical route")
	}
	t.Log("PostgreSQL + API Server acceptance passed: concurrent claims, crash/reclaim, token fencing, immutable replay, supersession, tombstones and durable retry")
}

func publicationHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
