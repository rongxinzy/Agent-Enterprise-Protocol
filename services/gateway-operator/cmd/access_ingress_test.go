package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIngressCacheIsOptInAndNamespaceScoped(t *testing.T) {
	for _, host := range []string{"", "shared.access.test"} {
		t.Setenv("AEP_GATEWAY_ACCESS_INGRESS_HOST", host)
		options := managerOptions()
		if len(options.Cache.DefaultNamespaces) != 1 {
			t.Fatal("session routes expanded default informer namespaces")
		}
		if _, ok := options.Cache.DefaultNamespaces["aep-system"]; !ok {
			t.Fatal("business resource watch escaped aep-system")
		}
		found := false
		for object, scope := range options.Cache.ByObject {
			if _, secret := object.(*corev1.Secret); secret {
				if len(scope.Namespaces) != 1 {
					t.Fatal("session routes expanded credential informer scope")
				}
				if _, ok := scope.Namespaces["aep-credentials"]; !ok {
					t.Fatal("credential informer lost its dedicated namespace")
				}
				continue
			}
			if u, ok := object.(*unstructured.Unstructured); !ok || u.GetKind() != "EnvoyFilter" {
				t.Fatal("unexpected additional informer")
			}
			found = true
			if len(scope.Namespaces) != 1 {
				t.Fatal("native route informer must be namespaced")
			}
			if _, ok := scope.Namespaces["higress-system"]; !ok {
				t.Fatal("native route informer escaped Higress namespace")
			}
		}
		if found != (host != "") {
			t.Fatal("native route informer is not opt-in")
		}
	}
}
