package access

import (
	"fmt"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func accessRego(spec gateway.ModelAccessGrantSpec, hash string, options Options) string {
	licenses := make([]map[string]any, 0, len(spec.Licenses))
	for _, license := range spec.Licenses {
		deadline := int64(0)
		if license.ValidUntil != nil {
			deadline = license.ValidUntil.Unix()
		}
		licenses = append(licenses, map[string]any{"id": license.ID, "digest": license.Digest, "deadline": deadline})
	}
	return fmt.Sprintf(`allow {
not %t
input.auth.identity.iss == %s
input.auth.identity.deployment_id == %s
input.auth.identity.sub == %s
input.auth.identity.session_id == %s
object.get(input.auth.identity, "password_change_required", false) == false
credential_allowed
}
audience_model { is_array(input.auth.identity.aud); input.auth.identity.aud[_] == "model-gateway" }
audience_model { input.auth.identity.aud == "model-gateway" }
audience_entitlement { is_array(input.auth.identity.aud); input.auth.identity.aud[_] == "aep-entitlement" }
audience_entitlement { input.auth.identity.aud == "aep-entitlement" }
credential_allowed { not %t; input.auth.identity.token_use == "model"; audience_model }
credential_allowed { input.auth.identity.token_use == "entitlement"; audience_entitlement; license_match }
license_match {
license := %s[_]
license.id == input.auth.identity.license_id
license.digest == input.auth.identity.license_digest
}
models = %s
policy_hash = %s
`, spec.Disabled, jsonValue(options.IssuerURL), jsonValue(spec.DeploymentID), jsonValue(spec.Subject.ID),
		jsonValue(spec.Binding.SessionID), options.RequireEntitlement, jsonValue(licenses), jsonValue(spec.Models), jsonValue(hash))
}

func boundsRego(spec gateway.ModelAccessGrantSpec, hash string) string {
	licenses := make([]map[string]any, 0, len(spec.Licenses))
	for _, license := range spec.Licenses {
		deadline := int64(0)
		if license.ValidUntil != nil {
			deadline = license.ValidUntil.Unix()
		}
		licenses = append(licenses, map[string]any{"id": license.ID, "digest": license.Digest, "deadline": deadline})
	}
	return fmt.Sprintf(`allow {
input.auth.authorization.access.policy_hash == %s
now := time.now_ns() / 1000000000
%d <= now
%d > now
is_number(input.auth.identity.exp)
input.auth.identity.exp > now
nbf := object.get(input.auth.identity, "nbf", 0)
is_number(nbf)
nbf <= now
iat := object.get(input.auth.identity, "iat", 0)
is_number(iat)
iat <= now
license_current
body := json.unmarshal(input.context.request.http.body)
is_object(body)
model := body.model
is_string(model)
input.auth.authorization.access.models[_] == model
input.auth.identity.model_scopes[_] == model
}
license_current { input.auth.identity.token_use == "model" }
license_current {
input.auth.identity.token_use == "entitlement"
license := licenses[_]
license.id == input.auth.identity.license_id
license.digest == input.auth.identity.license_digest
license.deadline == 0
}
license_current {
input.auth.identity.token_use == "entitlement"
license := licenses[_]
license.id == input.auth.identity.license_id
license.digest == input.auth.identity.license_digest
license.deadline > time.now_ns() / 1000000000
}
licenses = %s
publication = %s
`, jsonValue(hash), spec.IssuedAt.Unix(), spec.ValidUntil.Unix(), jsonValue(licenses), jsonValue(spec.Publication))
}
