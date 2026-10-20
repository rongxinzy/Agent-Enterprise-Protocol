package deployment_test

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestWorkloadObserverIsOptInWithReadOnlyEmployeeScope(t *testing.T) {
	objects := render(t, "deploy/kubernetes/workload-observer")
	role := objects["Role/digital-employees/aep-workload-observer"]
	deployment := objects["Deployment/aep-system/aep-workload-observer"]
	if role == nil || deployment == nil {
		t.Fatal("observer escaped its two explicit namespaces")
	}
	for _, object := range objects {
		if slices.Contains([]string{"Secret", "ClusterRole", "ClusterRoleBinding", "Service", "Ingress"}, object.GetKind()) {
			t.Fatal("observer must not ship private data, global grants or a public endpoint")
		}
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	if len(rules) != 2 {
		t.Fatal("unexpected employee observer privileges")
	}
	for _, raw := range rules {
		rule := raw.(map[string]any)
		resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
		groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
		verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
		if len(resources) != 1 || len(groups) != 1 {
			t.Fatal("observer must have exact resource and API group scope")
		}
		readable := resources[0] == "digitalemployees" && groups[0] == "digitalemployee.zhiyuan.io" || resources[0] == "serviceaccounts" && groups[0] == ""
		if !readable {
			t.Fatal("observer gained access to credentials, token reviews or unrelated resources")
		}
		for _, verb := range verbs {
			if !slices.Contains([]string{"get", "list", "watch"}, verb) {
				t.Fatal("observer gained employee mutation privilege")
			}
		}
	}
	leader := objects["Role/aep-system/aep-workload-observer-leader"]
	if leader == nil {
		t.Fatal("missing native leader role")
	}
	rules, _, _ = unstructured.NestedSlice(leader.Object, "rules")
	for _, raw := range rules {
		rule := raw.(map[string]any)
		resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
		groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
		verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
		names, _, _ := unstructured.NestedStringSlice(rule, "resourceNames")
		if slices.Equal(resources, []string{"events"}) && slices.Equal(groups, []string{""}) && slices.Equal(verbs, []string{"create", "patch"}) {
			continue
		}
		leaseOperation := slices.Equal(verbs, []string{"create"}) || slices.Equal(verbs, []string{"get", "update"}) && slices.Equal(names, []string{"aep-workload-observer"})
		if !slices.Equal(resources, []string{"leases"}) || !slices.Equal(groups, []string{"coordination.k8s.io"}) || !leaseOperation {
			t.Fatal("leader gained privileges beyond events and its fixed Lease")
		}
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatal("observer must remain isolated from credential consumers")
	}
	container := containers[0].(map[string]any)
	security, _, _ := unstructured.NestedMap(container, "securityContext")
	if security["allowPrivilegeEscalation"] != false || security["readOnlyRootFilesystem"] != true {
		t.Fatal("observer weakened container confinement")
	}
	for _, dir := range []string{"services/gateway-operator/config/default", "deploy/kubernetes/operator", "deploy/kubernetes/production"} {
		if render(t, dir)["Deployment/aep-system/aep-workload-observer"] != nil {
			t.Fatalf("observer enabled without opt-in in %s", dir)
		}
	}
}
