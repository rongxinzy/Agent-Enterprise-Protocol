package deployment_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func render(t *testing.T, dir string) map[string]*unstructured.Unstructured {
	t.Helper()
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl is required for the explicit deployment rendering check")
	}
	_, file, _, _ := runtime.Caller(0)
	command := exec.Command("kubectl", "kustomize", filepath.Join(filepath.Dir(file), "../..", dir))
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("kustomize %s: %v: %s", dir, err, raw)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	objects := map[string]*unstructured.Unstructured{}
	for {
		value := &unstructured.Unstructured{}
		var document json.RawMessage
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if err := value.UnmarshalJSON(document); err != nil {
			t.Fatal(err)
		}
		key := value.GetKind() + "/" + value.GetNamespace() + "/" + value.GetName()
		if objects[key] != nil {
			t.Fatal("duplicate identity: " + key)
		}
		objects[key] = value
	}
	return objects
}

func TestOperatorTargetRetiresLegacyGrantsAndSeparatesWriters(t *testing.T) {
	for _, dir := range []string{"operator", "operator-observation"} {
		t.Run(dir, func(t *testing.T) {
			objects := render(t, "deploy/kubernetes/"+dir)
			get := func(key string) *unstructured.Unstructured {
				t.Helper()
				if objects[key] == nil {
					t.Fatal("missing " + key)
				}
				return objects[key]
			}
			legacy := get("Deployment/aep-system/aep-gateway-reconciler")
			if n, _, _ := unstructured.NestedInt64(legacy.Object, "spec", "replicas"); n != 0 {
				t.Fatal("legacy writer still running")
			}
			for _, key := range []string{"RoleBinding/higress-system/aep-gateway-reconciler", "RoleBinding/aep-system/aep-gateway-reconciler-leader", "RoleBinding/aep-credentials/aep-gateway-credentials"} {
				subjects, _, _ := unstructured.NestedSlice(get(key).Object, "subjects")
				if len(subjects) != 0 {
					t.Fatal("legacy grant retained: " + key)
				}
			}
			for _, object := range objects {
				if object.GetKind() == "ClusterRoleBinding" || object.GetKind() == "Secret" {
					t.Fatal("unexpected global grant or private input")
				}
			}
			config, _, _ := unstructured.NestedStringMap(get("ConfigMap/aep-system/aep-control-service-config").Object, "data")
			for _, flag := range []string{"AEP_GATEWAY_OPERATOR_MODE", "AEP_GATEWAY_PUBLICATIONS_ENABLED", "AEP_GATEWAY_PUBLICATION_RELAY_ENABLED"} {
				if config[flag] != "true" {
					t.Fatal("cutover flag missing: " + flag)
				}
			}
			for name, container := range map[string]string{"aep-control-service": "control-service", "aep-gateway-operator": "manager", "aep-credential-publisher": "publisher"} {
				deployment := get("Deployment/aep-system/" + name)
				spec, _, _ := unstructured.NestedMap(deployment.Object, "spec", "template", "spec")
				if spec["automountServiceAccountToken"] != false {
					t.Fatal("unbounded token mount: " + name)
				}
				volumes, _, _ := unstructured.NestedSlice(spec, "volumes")
				found := false
				for _, raw := range volumes {
					volume := raw.(map[string]any)
					if volume["name"] != "api-access" {
						continue
					}
					sources, _, _ := unstructured.NestedSlice(volume, "projected", "sources")
					for _, raw := range sources {
						token, ok, _ := unstructured.NestedMap(raw.(map[string]any), "serviceAccountToken")
						if ok && token["expirationSeconds"] == int64(3600) {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("short-lived API projection missing: " + container)
				}
			}
			if objects["ExternalSecret/aep-credentials/aep-credential-example"] != nil {
				t.Fatal("dual provider Secret provisioner")
			}
			observation := objects["RoleBinding/higress-system/aep-gateway-operator-observation"] != nil
			if observation != (dir == "operator-observation") {
				t.Fatal("sensitive observation is not opt-in")
			}
		})
	}
}

func TestLegacyDeploymentDefaultRemainsCompatible(t *testing.T) {
	objects := render(t, "deploy/kubernetes/production")
	legacy := objects["Deployment/aep-system/aep-gateway-reconciler"]
	if legacy == nil {
		t.Fatal("legacy deployment omitted")
	}
	if n, _, _ := unstructured.NestedInt64(legacy.Object, "spec", "replicas"); n != 2 {
		t.Fatal("legacy default changed")
	}
	if objects["Deployment/aep-system/aep-gateway-operator"] != nil {
		t.Fatal("operator enabled without opt-in")
	}
}
