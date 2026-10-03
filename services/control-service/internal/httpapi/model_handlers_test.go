package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
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
	base := modelWrite{DisplayName: "Chat", SourceType: "gateway", Protocol: "openai-compatible", Capabilities: &capabilities, IsDefault: &valid, Enabled: &valid}
	if !validModelWrite(base) {
		t.Fatal("valid model descriptor was rejected")
	}
	for _, invalid := range []modelWrite{
		{DisplayName: "Chat", SourceType: "unknown", Protocol: "openai-compatible", Capabilities: &capabilities, IsDefault: &valid, Enabled: &valid},
		{DisplayName: "Chat", SourceType: "gateway", Protocol: "http", Capabilities: &capabilities, IsDefault: &valid, Enabled: &valid},
		{DisplayName: "Chat", SourceType: "gateway", Protocol: "openai-compatible", Capabilities: &capabilities, ContextWindow: ptrInt32(0), IsDefault: &valid, Enabled: &valid},
	} {
		if validModelWrite(invalid) {
			t.Fatalf("invalid model descriptor accepted: %#v", invalid)
		}
	}
}

func ptrInt32(value int32) *int32 { return &value }

func TestValidModelWriteAnthropicInvariants(t *testing.T) {
	enabled := true
	capabilities := []string{"text"}
	absolute := "https://open.bigmodel.cn/api/anthropic"
	base := modelWrite{DisplayName: "Bench", SourceType: "gateway", Protocol: "anthropic", Endpoint: &absolute, Capabilities: &capabilities, IsDefault: &enabled, Enabled: &enabled}
	if !validModelWrite(base) {
		t.Fatal("valid anthropic model descriptor was rejected")
	}
	for name, invalid := range map[string]modelWrite{
		"relative endpoint": func() modelWrite { m := base; m.Endpoint = ptrString("/v1"); return m }(),
		"missing endpoint":  func() modelWrite { m := base; m.Endpoint = nil; return m }(),
		"reasoning not allowed": func() modelWrite {
			m := base
			m.ReasoningCompatibility = &modelReasoningCompatibility{ThinkingFormat: "deepseek", SupportsReasoningEffort: true, RequiresReasoningContentOnAssistantMessages: true}
			return m
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if validModelWrite(invalid) {
				t.Fatalf("invalid anthropic descriptor accepted: %#v", invalid)
			}
		})
	}
}

func TestValidAnthropicModelStateRechecksStoredRow(t *testing.T) {
	// PATCH cannot change protocol, so the guard re-derives invariants from the
	// post-update row rather than trusting the patch payload.
	healthy := modelRecord{Protocol: "anthropic", Endpoint: pgtypeText("https://open.bigmodel.cn/api/anthropic")}
	if !validAnthropicModelState(healthy) {
		t.Fatal("healthy anthropic row was rejected")
	}
	for name, broken := range map[string]modelRecord{
		"patched relative endpoint": {Protocol: "anthropic", Endpoint: pgtypeText("/v1")},
		"patched null endpoint":     {Protocol: "anthropic"},
		"patched reasoning":         {Protocol: "anthropic", Endpoint: pgtypeText("https://open.bigmodel.cn/api/anthropic"), ReasoningCompatibility: []byte(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if validAnthropicModelState(broken) {
				t.Fatalf("broken anthropic row accepted: %#v", broken)
			}
		})
	}
	if !validAnthropicModelState(modelRecord{Protocol: "openai-compatible"}) {
		t.Fatal("openai-compatible rows are outside the anthropic guard")
	}
}

func TestAbsoluteHTTPURL(t *testing.T) {
	for value, valid := range map[string]bool{
		"https://open.bigmodel.cn/api/anthropic": true,
		"http://new-api.svc:3000/v1":             true,
		"/v1":                                    false,
		"open.bigmodel.cn":                       false,
		"ftp://host/path":                        false,
		"":                                       false,
	} {
		if absoluteHTTPURL(value) != valid {
			t.Fatalf("absoluteHTTPURL(%q) = %v, want %v", value, absoluteHTTPURL(value), valid)
		}
	}
}

func ptrString(value string) *string { return &value }

func pgtypeText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }
