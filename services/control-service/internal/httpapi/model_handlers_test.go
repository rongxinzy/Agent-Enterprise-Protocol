package httpapi

import "testing"

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
