package deployment_test

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPolicySourceIsSeparateReadOnlyAndTLSOnly(t *testing.T) {
	objects := render(t, "deploy/kubernetes/access-policy-source")
	role := objects["Role/aep-system/aep-access-policy-source"]
	deployment := objects["Deployment/aep-system/aep-access-policy-source"]
	service := objects["Service/aep-system/aep-access-policy-source"]
	if role == nil || deployment == nil || service == nil {
		t.Fatal("source deployment escaped its explicit namespace")
	}
	for _, object := range objects {
		if object.GetKind() == "Secret" || object.GetKind() == "ClusterRoleBinding" || object.GetKind() == "ClusterRole" {
			t.Fatal("source must not ship private inputs or global permissions")
		}
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	if len(rules) != 1 {
		t.Fatal("unexpected source privileges")
	}
	resources, _, _ := unstructured.NestedStringSlice(rules[0].(map[string]any), "resources")
	if len(resources) != 1 || resources[0] != "configmaps" {
		t.Fatal("source may only read ConfigMaps")
	}
	verbs, _, _ := unstructured.NestedStringSlice(rules[0].(map[string]any), "verbs")
	for _, verb := range verbs {
		if verb != "get" && verb != "list" && verb != "watch" {
			t.Fatalf("source gained mutation privilege: %s", verb)
		}
	}
	ports, _, _ := unstructured.NestedSlice(service.Object, "spec", "ports")
	if len(ports) != 1 || ports[0].(map[string]any)["port"] != int64(443) {
		t.Fatal("source exposes an unencrypted service port")
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if len(containers) != 2 {
		t.Fatal("source needs a watcher and isolated static server")
	}
	for _, raw := range containers {
		container := raw.(map[string]any)
		image, _, _ := unstructured.NestedString(container, "image")
		if !strings.Contains(image, "@sha256:") {
			t.Fatal("unpinned third-party source image")
		}
		mounts, _, _ := unstructured.NestedSlice(container, "volumeMounts")
		for _, raw := range mounts {
			mount := raw.(map[string]any)
			if container["name"] == "serve" && (mount["name"] == "api-access" || mount["name"] == "policies" && mount["readOnly"] != true) {
				t.Fatal("static server received API credentials or writable policy storage")
			}
			if container["name"] == "sync" && mount["name"] == "tls" {
				t.Fatal("watcher received server private key")
			}
		}
	}
	for _, dir := range []string{"services/gateway-operator/config/default", "deploy/kubernetes/operator", "deploy/kubernetes/production"} {
		if render(t, dir)["Deployment/aep-system/aep-access-policy-source"] != nil {
			t.Fatalf("source was enabled without opt-in in %s", dir)
		}
	}
}
