package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestDeploymentConfigDeepCopy(t *testing.T) {
	original := &DeploymentConfig{
		Spec: DeploymentConfigSpec{DeploymentID: "test", NativeGateway: &NativeGatewaySpec{
			Enabled: true, RedisPasswordRef: &CredentialReference{Name: "redis", Key: "password"},
			QuotaAdminCredentialRef: &CredentialReference{Name: "quota", Key: "token"},
		}},
		Status: DeploymentConfigStatus{Conditions: []metav1.Condition{{Type: ConditionApplied}},
			AppliedResources: []AppliedResource{{Kind: "WasmPlugin", Name: "test"}}},
	}
	copy := original.DeepCopy()
	copy.Spec.NativeGateway.RedisPasswordRef.Name = "changed"
	copy.Spec.NativeGateway.QuotaAdminCredentialRef.Key = "changed"
	copy.Status.Conditions[0].Type = "changed"
	copy.Status.AppliedResources[0].Name = "changed"
	if original.Spec.NativeGateway.RedisPasswordRef.Name != "redis" ||
		original.Spec.NativeGateway.QuotaAdminCredentialRef.Key != "token" ||
		original.Status.Conditions[0].Type != ConditionApplied || original.Status.AppliedResources[0].Name != "test" {
		t.Fatal("DeploymentConfig deep copy shares mutable storage")
	}
	list := (&DeploymentConfigList{Items: []DeploymentConfig{*original}}).DeepCopyObject().(*DeploymentConfigList)
	list.Items[0].Spec.NativeGateway.RedisPasswordRef.Key = "changed"
	if original.Spec.NativeGateway.RedisPasswordRef.Key != "password" {
		t.Fatal("list shares storage")
	}
	var nilConfig *DeploymentConfig
	var nilList *DeploymentConfigList
	if nilConfig.DeepCopyObject() != nil || nilList.DeepCopyObject() != nil {
		t.Fatal("nil copies")
	}
	policy := &ModelPolicy{Spec: ModelPolicySpec{Subject: &PolicySubject{Type: "user", ID: "alice"}}}
	policy.DeepCopy().Spec.Subject.ID = "bob"
	if policy.Spec.Subject.ID != "alice" {
		t.Fatal("subject shares storage")
	}
}
