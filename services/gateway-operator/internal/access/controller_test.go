package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture() *gateway.ModelAccessGrant {
	now := time.Now().UTC().Truncate(time.Second)
	g := &gateway.ModelAccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "aep-system", UID: "grant-a", Generation: 1},
		Spec: gateway.ModelAccessGrantSpec{DeploymentID: "probe", Subject: gateway.ModelAccessSubject{Kind: "human", ID: "alice"},
			Binding: gateway.ModelAccessBinding{Kind: "session", SessionID: "session-a"},
			Models:  []string{"model-a", "model-b"}, RoleIDs: []string{"role-a"}, TeamIDs: []string{"team-a"},
			IssuedAt: metav1.NewTime(now), ValidUntil: metav1.NewTime(now.Add(5 * time.Minute)),
			Publication: gateway.PublicationReference{Sequence: 1, OperationID: "11111111-1111-1111-1111-111111111111", Revision: "one", ContentHash: strings.Repeat("a", 64)}}}
	g.Name = "access-" + bindingKey(g.Spec)
	markFixture(g)
	return g
}

func markFixture(g *gateway.ModelAccessGrant) {
	g.Annotations = markers(g.Spec.Publication)
	g.Annotations[BindingKey] = bindingKey(g.Spec)
	sum := sha256.Sum256([]byte(g.Spec.DeploymentID))
	g.Labels = map[string]string{gateway.PublicationDeploymentLabel: hex.EncodeToString(sum[:16])}
}

func options() Options {
	return Options{IssuerURL: "http://introspection:8080/oidc", PolicyBaseURL: "http://policies:8080", RefreshSeconds: 1}
}

func newClient(t *testing.T, g *gateway.ModelAccessGrant) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	deployment := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: "deployment"},
		Spec: gateway.DeploymentConfigSpec{DeploymentID: g.Spec.DeploymentID}}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(g).WithObjects(g, deployment).Build()
}

func TestProjectionRenewalAndSemanticChange(t *testing.T) {
	ctx := context.Background()
	g := fixture()
	c := newClient(t, g)
	r := &Reconciler{Client: c, Reader: c, Options: options()}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
	reconcile := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	first := authConfig()
	if err := c.Get(ctx, request.NamespacedName, first); err != nil {
		t.Fatal(err)
	}
	var stored gateway.ModelAccessGrant
	if err := c.Get(ctx, request.NamespacedName, &stored); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(stored.Status.Conditions, "Applied") || !meta.IsStatusConditionFalse(stored.Status.Conditions, "Ready") || len(stored.Status.Replicas) != 0 {
		t.Fatalf("resource writes fabricated adoption: %#v", stored.Status)
	}
	old := stored.DeepCopy()
	stored.Spec.Publication.Sequence++
	stored.Spec.Publication.Revision = "renewal"
	stored.Spec.ValidUntil = metav1.NewTime(stored.Spec.ValidUntil.Add(time.Minute))
	markFixture(&stored)
	if err := c.Update(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	reconcile()
	renewed := authConfig()
	if err := c.Get(ctx, request.NamespacedName, renewed); err != nil {
		t.Fatal(err)
	}
	if renewed.GetResourceVersion() != first.GetResourceVersion() {
		t.Fatal("renewal rewrote AuthConfig/cache")
	}
	_, desired, err := Render(old, options())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.writePolicy(ctx, old, desired); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("late old publication: %v", err)
	}
	if err := c.Get(ctx, request.NamespacedName, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Publication.Sequence++
	stored.Spec.Models = []string{"model-b"}
	stored.Spec.Publication.ContentHash = strings.Repeat("b", 64)
	markFixture(&stored)
	if err := c.Update(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	reconcile()
	changed := authConfig()
	if err := c.Get(ctx, request.NamespacedName, changed); err != nil {
		t.Fatal(err)
	}
	if changed.GetAnnotations()[SemanticHash] == first.GetAnnotations()[SemanticHash] {
		t.Fatal("semantic change retained old policy")
	}
	rv := changed.GetResourceVersion()
	reconcile()
	if err := c.Get(ctx, request.NamespacedName, changed); err != nil {
		t.Fatal(err)
	}
	if changed.GetResourceVersion() != rv {
		t.Fatal("idempotent reconcile rewrote native CR")
	}
}

func TestProjectionRejectsForeignResourcesAndWrongDeployment(t *testing.T) {
	for _, mode := range []string{"policy-owner", "config-owner", "newer-policy", "binary-data", "same-version-data", "wrong-deployment", "invalid-marker", "workload"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := fixture()
			c := newClient(t, g)
			r := &Reconciler{Client: c, Reader: c, Options: options()}
			config, policy, err := Render(g, options())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "policy-owner":
				policy.OwnerReferences[0].UID = "foreign"
				err = c.Create(ctx, policy)
			case "config-owner":
				config.GetOwnerReferences()[0].UID = "foreign"
				config.SetOwnerReferences([]metav1.OwnerReference{{UID: "foreign", Controller: boolPtr()}})
				err = c.Create(ctx, config)
			case "newer-policy":
				policy.Annotations[gateway.PublicationSequence] = "2"
				err = c.Create(ctx, policy)
			case "binary-data":
				policy.BinaryData = map[string][]byte{"unexpected": []byte("not an authorized policy")}
				err = c.Create(ctx, policy)
			case "same-version-data":
				policy.Data[g.Name+".rego"] = "allow { true }"
				err = c.Create(ctx, policy)
			case "wrong-deployment":
				var deployment gateway.DeploymentConfig
				if err = c.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: "deployment"}, &deployment); err == nil {
					deployment.Spec.DeploymentID = "other"
					err = c.Update(ctx, &deployment)
				}
			case "invalid-marker":
				g.Annotations[BindingKey] = "wrong"
				err = c.Update(ctx, g)
			case "workload":
				g.Spec.Binding = gateway.ModelAccessBinding{Kind: "kubernetes"}
				err = c.Update(ctx, g)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)})
			if strings.Contains(mode, "owner") || mode == "newer-policy" || mode == "binary-data" || mode == "same-version-data" {
				if !errors.Is(err, ErrOwnership) {
					t.Fatalf("foreign/replayed resource accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var stored gateway.ModelAccessGrant
			if err := c.Get(ctx, client.ObjectKeyFromObject(g), &stored); err != nil {
				t.Fatal(err)
			}
			if !meta.IsStatusConditionFalse(stored.Status.Conditions, "Ready") || meta.IsStatusConditionTrue(stored.Status.Conditions, "Applied") {
				t.Fatalf("unsafe status %#v", stored.Status)
			}
		})
	}
}

func TestExpiredPublicationClearsUnverifiedReplicaClaims(t *testing.T) {
	ctx := context.Background()
	g := fixture()
	g.Spec.IssuedAt = metav1.NewTime(time.Now().Add(-10 * time.Minute))
	g.Spec.ValidUntil = metav1.NewTime(time.Now().Add(-time.Minute))
	g.Status.Replicas = []gateway.ModelAccessReplicaObservation{{PodUID: "unverified", InstanceID: "stale"}}
	c := newClient(t, g)
	r := &Reconciler{Client: c, Reader: c, Options: options()}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}); err != nil {
		t.Fatal(err)
	}
	var stored gateway.ModelAccessGrant
	if err := c.Get(ctx, client.ObjectKeyFromObject(g), &stored); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "PublicationExpired" || len(stored.Status.Replicas) != 0 {
		t.Fatalf("expired publication fabricated adoption: %#v", stored.Status)
	}
}

func boolPtr() *bool { value := true; return &value }
