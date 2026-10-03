package httpapi

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func catalogModel(id string, enabled bool, sourceType, endpoint, upstreamModel, credentialID string) modelRecord {
	text := func(value string) pgtype.Text {
		return pgtype.Text{String: value, Valid: value != ""}
	}
	return modelRecord{
		ID: id, DisplayName: id, SourceType: sourceType, Protocol: "openai-compatible",
		Endpoint: text(endpoint), UpstreamModel: text(upstreamModel), CredentialID: text(credentialID),
		Enabled: enabled,
	}
}

func TestDeriveDataPlaneRoutesFiltersCatalogAndMapsCredentials(t *testing.T) {
	models := []modelRecord{
		catalogModel("chat-a", true, "gateway", "http://provider-a/v1", "provider-a-chat", "credential-a"),
		catalogModel("chat-b", true, "gateway", "/v1", "provider-b-chat", ""),
		catalogModel("disabled", false, "gateway", "http://provider-c/v1", "provider-c-chat", "credential-c"),
		catalogModel("local", true, "local", "", "", ""),
		catalogModel("open-source", true, "enterprise_open_source", "http://provider-d/v1", "provider-d-chat", ""),
		catalogModel("no-endpoint", true, "gateway", "", "provider-e-chat", ""),
		catalogModel("no-upstream", true, "gateway", "http://provider-f/v1", "", ""),
		catalogModel("blank-endpoint", true, "gateway", "  ", "provider-g-chat", ""),
	}
	routes := deriveDataPlaneRoutes(models)
	if len(routes) != 3 {
		t.Fatalf("derived routes = %#v", routes)
	}
	if routes[0].ModelID != "chat-a" || !routes[0].Enabled || routes[0].Endpoint != "http://provider-a/v1" || routes[0].UpstreamModel != "provider-a-chat" || routes[0].Protocol != "openai-compatible" {
		t.Fatalf("derived route = %#v", routes[0])
	}
	reference := routes[0].CredentialRef
	if reference == nil || reference.Name != "aep-credential-credential-a" || reference.Key != "api-key" || reference.Namespace == nil || *reference.Namespace != "higress-system" {
		t.Fatalf("credential reference = %#v", reference)
	}
	if routes[1].ModelID != "chat-b" || routes[1].CredentialRef != nil {
		t.Fatalf("credential-less route = %#v", routes[1])
	}
	// Disabled models ride along as enabled=false so the reconciler deletes
	// whatever the route previously owned.
	if routes[2].ModelID != "disabled" || routes[2].Enabled {
		t.Fatalf("disabled route = %#v", routes[2])
	}
}

func TestDeriveDataPlaneRoutesHandlesEmptyCatalog(t *testing.T) {
	routes := deriveDataPlaneRoutes(nil)
	if routes == nil || len(routes) != 0 {
		t.Fatalf("empty catalog routes = %#v", routes)
	}
}

func TestDeriveDataPlaneRoutesAnthropicModels(t *testing.T) {
	anthropic := catalogModel("bench-anthropic", true, "gateway", "https://open.bigmodel.cn/api/anthropic", "glm-5.3-flash", "credential-bigmodel")
	anthropic.Protocol = "anthropic"
	bare := catalogModel("local-anthropic", true, "gateway", "/v1", "upstream", "")
	bare.Protocol = "anthropic"
	routes := deriveDataPlaneRoutes([]modelRecord{anthropic, bare})
	if len(routes) != 1 {
		t.Fatalf("derived routes = %#v", routes)
	}
	route := routes[0]
	if route.ModelID != "bench-anthropic" || route.Protocol != "anthropic" || route.ProviderType != "" {
		t.Fatalf("anthropic derived route = %#v", route)
	}
	if route.CredentialRef == nil || route.CredentialRef.Name != "aep-credential-credential-bigmodel" {
		t.Fatalf("anthropic credential reference = %#v", route.CredentialRef)
	}
}

func TestDeriveCatalogStateAssignsContentAddressedRevision(t *testing.T) {
	models := []modelRecord{
		catalogModel("chat-b", true, "gateway", "/v1", "provider-b-chat", ""),
		catalogModel("chat-a", true, "gateway", "http://provider-a/v1", "provider-a-chat", "credential-a"),
	}
	first, ok := deriveCatalogState(models, "")
	if !ok || !strings.HasPrefix(first.Revision, "catalog-") || len(first.Revision) != len("catalog-")+64 {
		t.Fatalf("derived revision = %q, %v", first.Revision, ok)
	}
	second, ok := deriveCatalogState([]modelRecord{models[1], models[0]}, "")
	if !ok || first.Revision != second.Revision {
		t.Fatalf("derivation is not deterministic: %q != %q", first.Revision, second.Revision)
	}
	changed, ok := deriveCatalogState(models[:1], "")
	if !ok || changed.Revision == first.Revision {
		t.Fatalf("catalog change did not advance the revision: %q", changed.Revision)
	}
	explicit, ok := deriveCatalogState(models, "release-2026-09")
	if !ok || explicit.Revision != "release-2026-09" {
		t.Fatalf("explicit revision = %q, %v", explicit.Revision, ok)
	}
	if first.Routes[0].ModelID != "chat-a" || first.Routes[0].ProviderType != "openai" {
		t.Fatalf("derived routes were not normalized: %#v", first.Routes)
	}
}

func TestCompareCatalogRoutesReportsMissingExtraAndMismatched(t *testing.T) {
	namespace := "higress-system"
	derived := []dataPlaneRoute{
		{ModelID: "chat-a", Enabled: true, Endpoint: "http://provider-a/v1", UpstreamModel: "provider-a-chat", Protocol: "openai-compatible", ProviderType: "openai", CredentialRef: &dataPlaneSecretReference{Name: "aep-credential-credential-a", Key: "api-key", Namespace: &namespace}},
		{ModelID: "chat-b", Enabled: true, Endpoint: "/v1", UpstreamModel: "provider-b-chat", Protocol: "openai-compatible", ProviderType: "openai"},
	}
	inSync := compareCatalogRoutes(append([]dataPlaneRoute(nil), derived...), derived)
	if len(inSync.Missing) != 0 || len(inSync.Extra) != 0 || len(inSync.Mismatched) != 0 {
		t.Fatalf("in-sync comparison = %#v", inSync)
	}

	otherNamespace := "other"
	stored := []dataPlaneRoute{
		{ModelID: "chat-a", Enabled: true, Endpoint: "http://provider-a/v1", UpstreamModel: "stale-upstream", Protocol: "openai-compatible", ProviderType: "deepseek", CredentialRef: &dataPlaneSecretReference{Name: "aep-credential-credential-a", Key: "api-key", Namespace: &otherNamespace}},
		{ModelID: "ghost", Enabled: true, Endpoint: "/v1", UpstreamModel: "ghost", Protocol: "openai-compatible", ProviderType: "openai"},
	}
	comparison := compareCatalogRoutes(stored, derived)
	if len(comparison.Missing) != 1 || comparison.Missing[0] != "chat-b" {
		t.Fatalf("missing = %#v", comparison.Missing)
	}
	if len(comparison.Extra) != 1 || comparison.Extra[0] != "ghost" {
		t.Fatalf("extra = %#v", comparison.Extra)
	}
	// The reconciler resolves credentialRef by name and key only, so a
	// namespace-only difference is not drift.
	if len(comparison.Mismatched) != 1 || comparison.Mismatched[0].ModelID != "chat-a" {
		t.Fatalf("mismatched = %#v", comparison.Mismatched)
	}
	fields := strings.Join(comparison.Mismatched[0].Fields, ",")
	if fields != "upstreamModel,providerType" {
		t.Fatalf("mismatch fields = %q", fields)
	}

	disabled := append([]dataPlaneRoute(nil), derived...)
	disabled[0].Enabled = false
	comparison = compareCatalogRoutes(disabled, derived)
	if len(comparison.Mismatched) != 1 || strings.Join(comparison.Mismatched[0].Fields, ",") != "enabled" {
		t.Fatalf("disabled drift = %#v", comparison.Mismatched)
	}

	droppedCredential := append([]dataPlaneRoute(nil), derived...)
	droppedCredential[0].CredentialRef = nil
	comparison = compareCatalogRoutes(droppedCredential, derived)
	if len(comparison.Mismatched) != 1 || strings.Join(comparison.Mismatched[0].Fields, ",") != "credentialRef" {
		t.Fatalf("credential drift = %#v", comparison.Mismatched)
	}

	reprotocoled := append([]dataPlaneRoute(nil), derived...)
	reprotocoled[0].Protocol = "anthropic"
	reprotocoled[0].ProviderType = ""
	comparison = compareCatalogRoutes(reprotocoled, derived)
	if len(comparison.Mismatched) != 1 || strings.Join(comparison.Mismatched[0].Fields, ",") != "protocol,providerType" {
		t.Fatalf("protocol drift = %#v", comparison.Mismatched)
	}
}
