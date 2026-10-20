package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// Real API read-back and RV fences run whenever envtest assets are available.
// Optional pinned Authorino admission also exercises its native defaulting.
func TestNativeProjectionWithAPIServer(t *testing.T) {
	assets, crdPath := os.Getenv("KUBEBUILDER_ASSETS"), os.Getenv("AEP_AUTHORINO_CRD_PATH")
	if assets == "" {
		t.Skip("set KUBEBUILDER_ASSETS for access projection API tests")
	}
	// Schema-neutral test storage is sufficient for real resourceVersion and
	// status semantics. It makes no claim about native admission/defaults.
	crd := apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "authconfigs.authorino.kuadrant.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "authorino.kuadrant.io", Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "authconfigs", Singular: "authconfig", Kind: "AuthConfig", ListKind: "AuthConfigList"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta3", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object", XPreserveUnknownFields: ptr.To(true)}}}}}}}}
	if crdPath != "" {
		data, err := os.ReadFile(crdPath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(strings.ReplaceAll(string(data), "\r\n", "\n")))
		if hex.EncodeToString(sum[:]) != "ccaf7e85c8e678d42dc87050f79e1cd2bd92fefe1a75511577c3f3513fb0b15c" {
			t.Fatal("pinned Authorino CRD integrity mismatch")
		}
		if err := yaml.Unmarshal(data, &crd); err != nil {
			t.Fatal(err)
		}
		t.Log("using pinned native Authorino admission, including defaults")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets, CRDs: []*apiextensionsv1.CustomResourceDefinition{&crd},
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}}
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
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "aep-system"}}); err != nil {
		t.Fatal(err)
	}
	deployment := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "aep-system"}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "probe"}}
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	g := fixture()
	g.UID = ""
	g.Generation = 0
	if err := c.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, Reader: c, Options: options()}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	first := authConfig()
	if err := c.Get(ctx, req.NamespacedName, first); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, g); err != nil {
		t.Fatal(err)
	}
	g.Spec.Publication.Sequence++
	g.Spec.ValidUntil = metav1.NewTime(g.Spec.ValidUntil.Add(time.Minute))
	markFixture(g)
	if err := c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	renewed := authConfig()
	if err := c.Get(ctx, req.NamespacedName, renewed); err != nil {
		t.Fatal(err)
	}
	if renewed.GetResourceVersion() != first.GetResourceVersion() {
		t.Fatal("admission defaults caused renewal to rebuild native AuthConfig")
	}
	stale := renewed.DeepCopy()
	if err := c.Get(ctx, req.NamespacedName, g); err != nil {
		t.Fatal(err)
	}
	g.Spec.Publication.Sequence++
	g.Spec.Models = []string{"model-b"}
	g.Spec.Publication.ContentHash = strings.Repeat("b", 64)
	markFixture(g)
	if err := c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	stale.Object["spec"].(map[string]any)["hosts"] = []any{"replay.invalid"}
	if err := c.Update(ctx, stale); !apierrors.IsConflict(err) {
		t.Fatalf("late write did not conflict: %v", err)
	}
	// Replaying the old source cannot create an equivalent delayed renewal.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, g); err != nil {
		t.Fatal(err)
	}
	for _, condition := range g.Status.Conditions {
		if condition.Type == "Ready" && condition.Status != metav1.ConditionFalse {
			t.Fatal("native API writes fabricated serving adoption")
		}
	}
}
