package v1alpha1

import "testing"

// Alpha CRs contain optional credential references and observed inventories.
// Empty/nil snapshots and mutated copies must preserve that boundary too.
func TestConfigSnapshotCopySafety(t *testing.T) {
	var nilSpec *DeploymentConfigSpec
	var nilStatus *DeploymentConfigStatus
	var nilNative *NativeGatewaySpec
	var nilSubject *PolicySubject
	var nilResource *AppliedResource
	if nilSpec.DeepCopy() != nil || nilStatus.DeepCopy() != nil || nilNative.DeepCopy() != nil ||
		nilSubject.DeepCopy() != nil || nilResource.DeepCopy() != nil {
		t.Fatal("nil nested copy must remain nil")
	}
	empty := &DeploymentConfig{}
	copied := empty.DeepCopy()
	if copied.Spec.NativeGateway != nil || copied.Status.Conditions != nil || copied.Status.AppliedResources != nil {
		t.Fatal("empty snapshot gained values")
	}
	if (&DeploymentConfigList{}).DeepCopy().Items != nil {
		t.Fatal("empty list gained items")
	}
	native := (&NativeGatewaySpec{}).DeepCopy()
	if native.RedisPasswordRef != nil || native.QuotaAdminCredentialRef != nil {
		t.Fatal("empty native configuration gained credentials")
	}
	populated := &DeploymentConfigSpec{NativeGateway: &NativeGatewaySpec{Enabled: true, RedisPasswordRef: &CredentialReference{Name: "redis", Key: "password"}}}
	specCopy := populated.DeepCopy()
	specCopy.NativeGateway.RedisPasswordRef.Key = "changed"
	if populated.NativeGateway.RedisPasswordRef.Key != "password" {
		t.Fatal("spec copy shares credential reference")
	}
	status := &DeploymentConfigStatus{AppliedResources: []AppliedResource{{Name: "plugin"}}}
	status.DeepCopy().AppliedResources[0].Name = "changed"
	if status.AppliedResources[0].Name != "plugin" {
		t.Fatal("status copy shares inventory")
	}
	route := &ModelRoute{Status: ModelRouteStatus{AppliedResources: []AppliedResource{{Name: "ingress"}}}}
	route.DeepCopy().Status.AppliedResources[0].Name = "changed"
	if route.Status.AppliedResources[0].Name != "ingress" {
		t.Fatal("route copy shares inventory")
	}
	subject := &PolicySubject{Type: "user", ID: "alice"}
	subject.DeepCopy().ID = "bob"
	if subject.ID != "alice" {
		t.Fatal("subject copy shares state")
	}
	resource := &AppliedResource{Name: "ingress"}
	resource.DeepCopy().Name = "changed"
	if resource.Name != "ingress" {
		t.Fatal("resource copy shares state")
	}
}
