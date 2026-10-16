package publicationrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func testRoute(id string) gatewayrender.Route {
	return gatewayrender.Route{ModelID: id, Enabled: true, Endpoint: "https://provider.invalid/v1", UpstreamModel: id, Protocol: "openai-compatible", ProviderType: "deepseek", CredentialRef: &gatewayrender.SecretReference{Name: "provider-ref", Key: "api-key"}}
}

func delivery(seq int64, routes, history []gatewayrender.Route) repository.PublicationDelivery {
	state := snapshot{Revision: fmt.Sprintf("rev-%d", seq), Routes: routes}
	raw, _ := json.Marshal(state)
	historical, _ := json.Marshal(history)
	digest := sha256.Sum256(raw)
	return repository.PublicationDelivery{DataPlanePublication: repository.DataPlanePublication{
		DeploymentID: "demo", OperationID: uuid.NewString(), Sequence: seq, Revision: state.Revision, ContentHash: hex.EncodeToString(digest[:])},
		ClaimToken: uuid.NewString(), Payload: raw, HistoricalRoutes: historical}
}

func testProjector(t *testing.T) *Projector {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dc := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "aep-system", Name: "deployment"},
		Spec: gateway.DeploymentConfigSpec{DeploymentID: "demo", NativeGateway: &gateway.NativeGatewaySpec{Enabled: true, RedisService: "redis", RedisPort: 6379}}}
	return &Projector{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&gateway.ModelRoute{}, &gateway.DeploymentConfig{}).WithObjects(dc).Build(), Namespace: "aep-system"}
}

func TestProjectionReplayTombstonesAndMonotonicFence(t *testing.T) {
	p := testProjector(t)
	first := delivery(1, []gatewayrender.Route{testRoute("chat")}, []gatewayrender.Route{testRoute("chat")})
	if err := p.Project(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	var route gateway.ModelRoute
	key := client.ObjectKey{Namespace: p.Namespace, Name: "model-chat"}
	if err := p.Client.Get(t.Context(), key, &route); err != nil {
		t.Fatal(err)
	}
	if route.Spec.Protocol != "openai" || route.Spec.ProviderType != "deepseek" || route.Spec.CredentialRef.Name != "provider-ref" || route.Annotations[auditResourceKey] != first.OperationID {
		t.Fatalf("projection lost fields: %+v", route)
	}
	rv := route.ResourceVersion
	if err := p.Project(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := p.Client.Get(t.Context(), key, &route); err != nil {
		t.Fatal(err)
	}
	if route.ResourceVersion != rv {
		t.Fatal("idempotent retry rewrote identical route")
	}
	second := delivery(2, []gatewayrender.Route{}, []gatewayrender.Route{testRoute("chat")})
	if err := p.Project(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := p.Client.Get(t.Context(), key, &route); err != nil {
		t.Fatal(err)
	}
	if route.Spec.Enabled || route.Annotations[sequenceKey] != "2" {
		t.Fatal("removed route was not fenced by disabled tombstone")
	}
	if err := p.Project(t.Context(), first); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old intent = %v", err)
	}
	third := delivery(3, []gatewayrender.Route{testRoute("chat")}, []gatewayrender.Route{testRoute("chat")})
	if err := p.Project(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	if err := p.Client.Get(t.Context(), key, &route); err != nil || !route.Spec.Enabled {
		t.Fatalf("reactivation = %+v %v", route.Spec, err)
	}
	var dc gateway.DeploymentConfig
	if err := p.Client.Get(t.Context(), client.ObjectKey{Namespace: p.Namespace, Name: "deployment"}, &dc); err != nil {
		t.Fatal(err)
	}
	if dc.Spec.NativeGateway.RedisService != "redis" || dc.Annotations[operationKey] != third.OperationID {
		t.Fatal("aggregate settings or completion marker lost")
	}
}

type faultClient struct {
	client.Client
	get    func(context.Context, client.ObjectKey, client.Object) error
	list   func(context.Context, client.ObjectList) error
	create func(context.Context, client.Object) error
	update func(context.Context, client.Object) error
}

func (c *faultClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.get != nil {
		if err := c.get(ctx, key, obj); err != nil {
			return err
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
func (c *faultClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.create != nil {
		return c.create(ctx, obj)
	}
	return c.Client.Create(ctx, obj, opts...)
}
func (c *faultClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.list != nil {
		return c.list(ctx, list)
	}
	return c.Client.List(ctx, list, opts...)
}
func (c *faultClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.update != nil {
		return c.update(ctx, obj)
	}
	return c.Client.Update(ctx, obj, opts...)
}

func TestLostResponseAndForeignObjectAreSafe(t *testing.T) {
	t.Run("response lost after create", func(t *testing.T) {
		p := testProjector(t)
		base := p.Client
		p.Client = &faultClient{Client: base, create: func(ctx context.Context, obj client.Object) error {
			if err := base.Create(ctx, obj); err != nil {
				return err
			}
			return errors.New("lost response")
		}}
		item := delivery(1, []gatewayrender.Route{testRoute("chat")}, nil)
		if err := p.Project(t.Context(), item); err == nil {
			t.Fatal("uncertain response acknowledged")
		}
		p.Client = base
		if err := p.Project(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unowned name collision", func(t *testing.T) {
		p := testProjector(t)
		foreign := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Name: "model-chat", Namespace: p.Namespace}}
		if err := p.Client.Create(t.Context(), foreign); err != nil {
			t.Fatal(err)
		}
		before := foreign.DeepCopy()
		if err := p.Project(t.Context(), delivery(1, []gatewayrender.Route{testRoute("chat")}, nil)); !errors.Is(err, ErrOwnership) {
			t.Fatalf("ownership = %v", err)
		}
		if err := p.Client.Get(t.Context(), client.ObjectKeyFromObject(foreign), foreign); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, foreign) {
			t.Fatal("foreign object changed")
		}
	})
}

func TestProjectionResumesAfterPartialBudgetExhaustion(t *testing.T) {
	p := testProjector(t)
	base := p.Client
	routes := make([]gatewayrender.Route, 7)
	for i := range routes {
		routes[i] = testRoute(fmt.Sprintf("batch-%d", i))
	}
	item := delivery(1, routes, nil)
	writes, cycle := 0, 0
	p.Client = &faultClient{Client: base, create: func(ctx context.Context, obj client.Object) error {
		if cycle == 3 {
			return context.DeadlineExceeded
		}
		cycle++
		writes++
		return base.Create(ctx, obj)
	}, update: func(ctx context.Context, obj client.Object) error {
		if _, ok := obj.(*gateway.ModelRoute); ok {
			t.Fatal("retry rewrote an already verified route")
		}
		return base.Update(ctx, obj)
	}}
	for attempt := 0; attempt < 3; attempt++ {
		cycle = 0
		err := p.Project(t.Context(), item)
		if attempt < 2 && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("partial attempt = %v", err)
		}
		if attempt == 2 && err != nil {
			t.Fatalf("resumed projection = %v", err)
		}
		var dc gateway.DeploymentConfig
		if err := base.Get(t.Context(), client.ObjectKey{Namespace: p.Namespace, Name: "deployment"}, &dc); err != nil {
			t.Fatal(err)
		}
		if (dc.Annotations[operationKey] == item.OperationID) != (attempt == 2) {
			t.Fatal("aggregate completion did not match verified projection")
		}
	}
	if writes != len(routes) {
		t.Fatalf("created %d routes, want %d", writes, len(routes))
	}
}

func TestLateCreateCannotResurrectRemovedRoute(t *testing.T) {
	p := testProjector(t)
	base := p.Client
	old := delivery(1, []gatewayrender.Route{testRoute("chat")}, nil)
	invalid := testRoute("chat")
	invalid.Endpoint = "/v1"
	// A later invalid version must not hide an earlier representable definition.
	newer := delivery(3, []gatewayrender.Route{}, []gatewayrender.Route{testRoute("chat"), invalid})
	called := false
	p.Client = &faultClient{Client: base, create: func(ctx context.Context, obj client.Object) error {
		called = true
		if err := (&Projector{Client: base, Namespace: p.Namespace}).Project(ctx, newer); err != nil {
			t.Fatal(err)
		}
		return base.Create(ctx, obj)
	}}
	if err := p.Project(t.Context(), old); !apierrors.IsAlreadyExists(err) || !called {
		t.Fatalf("late create = %v", err)
	}
	var live gateway.ModelRoute
	if err := base.Get(t.Context(), client.ObjectKey{Namespace: p.Namespace, Name: "model-chat"}, &live); err != nil {
		t.Fatal(err)
	}
	if live.Spec.Enabled || live.Annotations[sequenceKey] != "3" {
		t.Fatal("late create revived removed route")
	}
}

func TestLateUpdateCannotOverwriteNewerIntent(t *testing.T) {
	p := testProjector(t)
	base := p.Client
	initial := delivery(1, []gatewayrender.Route{testRoute("chat")}, nil)
	if err := p.Project(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	old := delivery(2, []gatewayrender.Route{}, []gatewayrender.Route{testRoute("chat")})
	newer := delivery(3, []gatewayrender.Route{testRoute("chat")}, nil)
	p.Client = &faultClient{Client: base, update: func(ctx context.Context, obj client.Object) error {
		if _, ok := obj.(*gateway.ModelRoute); ok {
			if err := (&Projector{Client: base, Namespace: p.Namespace}).Project(ctx, newer); err != nil {
				t.Fatal(err)
			}
		}
		return base.Update(ctx, obj)
	}}
	if err := p.Project(t.Context(), old); !apierrors.IsConflict(err) {
		t.Fatalf("late update = %v", err)
	}
}

func TestInvalidSnapshotDoesNotWrite(t *testing.T) {
	for _, kind := range []string{"digest", "revision", "relative", "protocol", "namespace", "duplicate", "json", "sequence"} {
		t.Run(kind, func(t *testing.T) {
			p := testProjector(t)
			r := testRoute("chat")
			switch kind {
			case "relative":
				r.Endpoint = "/v1"
			case "protocol":
				r.Protocol = "unknown"
			case "namespace":
				r.CredentialRef.Namespace = new("foreign")
			}
			item := delivery(1, []gatewayrender.Route{r}, nil)
			switch kind {
			case "digest":
				item.ContentHash = strings.Repeat("0", 64)
			case "revision":
				item.Revision = "other"
			case "json":
				item.Payload = []byte("{")
			case "sequence":
				item.Sequence = 0
			case "duplicate":
				item = delivery(1, []gatewayrender.Route{r, r}, nil)
			}
			if err := p.Project(t.Context(), item); !errors.Is(err, ErrSnapshot) {
				t.Fatalf("invalid = %v", err)
			}
			var list gateway.ModelRouteList
			if err := p.Client.List(t.Context(), &list); err != nil || len(list.Items) != 0 {
				t.Fatal("invalid snapshot wrote CRs")
			}
		})
	}
}

func TestProjectionFailuresAndMarkers(t *testing.T) {
	for _, stage := range []string{"deployment-read", "route-read", "create", "route-verify", "stamp", "stamp-verify"} {
		t.Run(stage, func(t *testing.T) {
			p := testProjector(t)
			base := p.Client
			reads := map[string]int{}
			lists := 0
			failure := errors.New("injected API outage")
			p.Client = &faultClient{Client: base, get: func(_ context.Context, key client.ObjectKey, _ client.Object) error {
				reads[key.Name]++
				if (stage == "deployment-read" && key.Name == "deployment" && reads[key.Name] == 1) || (stage == "stamp-verify" && key.Name == "deployment" && reads[key.Name] == 3) {
					return failure
				}
				return nil
			}, list: func(ctx context.Context, list client.ObjectList) error {
				lists++
				if (stage == "route-read" && lists == 1) || (stage == "route-verify" && lists == 2) {
					return failure
				}
				return base.List(ctx, list, client.InNamespace(p.Namespace))
			}, create: func(ctx context.Context, obj client.Object) error {
				if stage == "create" {
					return failure
				}
				return base.Create(ctx, obj)
			}, update: func(ctx context.Context, obj client.Object) error {
				if stage == "stamp" {
					return failure
				}
				return base.Update(ctx, obj)
			}}
			if err := p.Project(t.Context(), delivery(1, []gatewayrender.Route{testRoute("chat")}, nil)); err == nil {
				t.Fatal("failed API operation acknowledged")
			}
		})
	}
	item := delivery(5, nil, nil)
	for _, test := range []struct {
		label, seq, op, hash string
		want                 error
	}{
		{"foreign", "1", "", "", ErrOwnership}, {owner("demo"), "bad", "", "", ErrOwnership}, {owner("demo"), "0", "", "", ErrOwnership},
		{owner("demo"), "6", "", "", ErrSuperseded}, {owner("demo"), "5", "wrong", item.ContentHash, ErrOwnership},
		{owner("demo"), "5", item.OperationID, "wrong", ErrOwnership},
	} {
		obj := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{ownerLabel: test.label}, Annotations: map[string]string{sequenceKey: test.seq, operationKey: test.op, hashKey: test.hash}}}
		if err := checkMarker(obj, item, true); !errors.Is(err, test.want) {
			t.Fatalf("marker = %v", err)
		}
	}
}
