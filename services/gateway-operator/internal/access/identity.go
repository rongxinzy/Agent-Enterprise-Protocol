package access

import (
	"encoding/base64"
	"slices"
	"strings"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

// ingressIdentity renders native OPA output into ext_authz dynamic metadata.
// Only the model depends on the request; membership always comes from the CR.
// A tiny Envoy Lua adapter copies this map after native prefix sanitation; it
// performs no JWT verification, authorization, caching or quota arithmetic.
func ingressIdentity(spec gateway.ModelAccessGrantSpec) (string, error) {
	if _, err := gatewaypolicy.Headers("model", spec.Subject.ID, spec.TeamIDs, spec.RoleIDs); err != nil {
		return "", err
	}
	subjects := []map[string]string{{"kind": "global", "id": ""}, {"kind": "user", "id": spec.Subject.ID}}
	for _, group := range []struct {
		kind string
		ids  []string
	}{{"team", spec.TeamIDs}, {"role", spec.RoleIDs}} {
		for _, id := range group.ids {
			subjects = append(subjects, map[string]string{"kind": group.kind, "id": id})
		}
	}
	identity := map[string]string{
		"x-mse-consumer":      gatewaysource.Consumer(spec.DeploymentID, spec.Subject.ID),
		"x-aep-deployment-id": spec.DeploymentID,
		"x-aep-user-id":       spec.Subject.ID,
		"x-aep-session-id":    spec.Binding.SessionID,
		"x-aep-team-ids":      membership(spec.TeamIDs),
		"x-aep-role-ids":      membership(spec.RoleIDs),
	}
	return "\nrequested_model = body.model { body := json.unmarshal(input.context.request.http.body) }\n" +
		"policy_subjects = " + jsonValue(subjects) + "\n" +
		"identity_headers = " + jsonValue(identity) + "\n" + gatewaypolicy.PresenceRego + `
trusted_headers = object.union(object.union(identity_headers, policy_headers), {"x-aep-model-id": requested_model})
`, nil
}

func membership(ids []string) string {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
	}
	return "|" + strings.Join(parts, "|") + "|"
}
