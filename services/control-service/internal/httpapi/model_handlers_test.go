package httpapi

import (
	"encoding/json"
	"testing"
)

func TestValidReasoningCompatibility(t *testing.T) {
	valid := &modelReasoningCompatibility{
		ThinkingFormat: "deepseek", SupportsReasoningEffort: true, RequiresReasoningContentOnAssistantMessages: true,
	}
	if !validReasoningCompatibility(valid) || !validReasoningCompatibility(nil) {
		t.Fatal("valid reasoning compatibility was rejected")
	}
	low := "low"
	if !validReasoningCompatibility(&modelReasoningCompatibility{
		ThinkingFormat: "zai", SupportsReasoningEffort: true, RequiresReasoningContentOnAssistantMessages: true,
		ThinkingLevelMap: map[string]*string{"off": nil, "low": &low, "high": &low, "max": &low},
	}) {
		t.Fatal("zai reasoning level map was rejected")
	}
	for _, invalid := range []*modelReasoningCompatibility{
		{ThinkingFormat: "provider-by-model-name", SupportsReasoningEffort: true, RequiresReasoningContentOnAssistantMessages: true},
		{ThinkingFormat: "deepseek", RequiresReasoningContentOnAssistantMessages: true},
		{ThinkingFormat: "deepseek", SupportsReasoningEffort: true},
		{ThinkingFormat: "zai", RequiresReasoningContentOnAssistantMessages: true, ThinkingLevelMap: map[string]*string{"unsupported": nil}},
	} {
		if validReasoningCompatibility(invalid) {
			t.Fatalf("invalid reasoning compatibility was accepted: %#v", invalid)
		}
	}
}

func TestModelPatchNullableFieldsAndWriteValidation(t *testing.T) {
	var patch modelPatch
	if err := json.Unmarshal([]byte(`{"credentialId":null,"reasoningCompatibility":null}`), &patch); err != nil {
		t.Fatal(err)
	}
	if !patch.CredentialID.Set || patch.CredentialID.Value != nil || !patch.ReasoningCompatibility.Set || patch.ReasoningCompatibility.Value != nil {
		t.Fatalf("nullable patch = %#v", patch)
	}
	if err := json.Unmarshal([]byte(`{"credentialId":123}`), &patch); err == nil {
		t.Fatal("nullable string patch accepted a non-string value")
	}
	if err := json.Unmarshal([]byte(`{"reasoningCompatibility":{"thinkingFormat":"deepseek"}}`), &patch); err != nil || patch.ReasoningCompatibility.Value == nil {
		t.Fatalf("reasoning patch = %#v, %v", patch, err)
	}
	valid := true
	capabilities := []string{"text"}
	base := modelWrite{ID: "chat", DisplayName: "Chat", SourceType: "gateway", Protocol: "openai-compatible", Capabilities: &capabilities, IsDefault: &valid, Enabled: &valid}
	if !validModelWrite(base) {
		t.Fatal("valid model descriptor was rejected")
	}
	for _, invalid := range []modelWrite{
		{ID: "chat", DisplayName: "Chat", SourceType: "unknown", Protocol: "openai-compatible", Capabilities: &capabilities, IsDefault: &valid, Enabled: &valid},
		{ID: "chat", DisplayName: "Chat", SourceType: "gateway", Protocol: "http", Capabilities: &capabilities, IsDefault: &valid, Enabled: &valid},
		{ID: "chat", DisplayName: "Chat", SourceType: "gateway", Protocol: "openai-compatible", Capabilities: &capabilities, ContextWindow: ptrInt32(0), IsDefault: &valid, Enabled: &valid},
	} {
		if validModelWrite(invalid) {
			t.Fatalf("invalid model descriptor accepted: %#v", invalid)
		}
	}
}

func ptrInt32(value int32) *int32 { return &value }
