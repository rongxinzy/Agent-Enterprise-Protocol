package access

import (
	"fmt"
	"strings"
	"unicode"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
	"k8s.io/apimachinery/pkg/util/validation"
)

func validNativeBinding(spec gateway.ModelAccessGrantSpec, options Options) bool {
	b := spec.Binding
	if !gatewayaccess.ValidWorkloadPods(b.Kind, spec.WorkloadPodUIDs) {
		return false
	}
	if b.Kind == "session" {
		return b.SessionID != "" && b.Workload == nil
	}
	w := b.Workload
	license, validLicense := workloadLicense(spec)
	if b.Kind != "kubernetes" || spec.Subject.Kind != "agent" || b.SessionID != "" || w == nil ||
		options.WorkloadAudience == "" || w.Audience != options.WorkloadAudience || !validLicense ||
		options.RequireEntitlement && !spec.Disabled && license == nil {
		// SA identity alone is never enterprise entitlement. Explicit, exact
		// trusted license evidence is required; disabled tombstones still apply.
		return false
	}
	uid := func(s string) bool {
		return s != "" && len(s) <= 128 && strings.IndexFunc(s, unicode.IsSpace) == -1 && strings.IndexFunc(s, unicode.IsControl) == -1
	}
	return len(validation.IsDNS1123Label(w.Namespace)) == 0 && len(validation.IsDNS1123Subdomain(w.ServiceAccountName)) == 0 &&
		uid(string(w.EmployeeUID)) && uid(string(w.ServiceAccountUID))
}

// Identity is the native TokenReview result, not an unverified JWT/header.
// TokenReview is cached under the exact bearer; the uncached bounds below
// separately enforce token expiry and publication freshness on every request.
func workloadAccessRego(spec gateway.ModelAccessGrantSpec, hash string) string {
	w := spec.Binding.Workload
	return fmt.Sprintf(`allow {
not %t
input.auth.identity.authenticated == true
input.auth.identity.user.username == %s
input.auth.identity.user.uid == %s
input.auth.identity.audiences[_] == %s
}
models = %s
policy_hash = %s
`, spec.Disabled, jsonValue("system:serviceaccount:"+w.Namespace+":"+w.ServiceAccountName),
		jsonValue(w.ServiceAccountUID), jsonValue(w.Audience), jsonValue(spec.Models), jsonValue(hash))
}

// io.jwt.decode is a native OPA parser, not a replacement signature verifier.
// The exact bearer has already passed native TokenReview. Parsing its claims
// here prevents a cached successful review from outliving exp. No result cache,
// cryptography, business DB lookup or token lifetime arithmetic is implemented.
func workloadBoundsRego(spec gateway.ModelAccessGrantSpec, hash string, options Options) string {
	w := spec.Binding.Workload
	pods := spec.WorkloadPodUIDs
	if pods == nil {
		pods = []string{}
	}
	return fmt.Sprintf(`allow {
input.auth.authorization.access.policy_hash == %s
workload_license_current
now := time.now_ns() / 1000000000
%d <= now
%d > now
header := input.context.request.http.headers.authorization
startswith(header, "Bearer ")
[_, claims, _] := io.jwt.decode(trim_prefix(header, "Bearer "))
is_number(claims.exp)
claims.exp > now
is_number(claims.nbf)
claims.nbf <= now
is_number(claims.iat)
claims.iat <= now
claims.sub == %s
claims.aud[_] == %s
kubernetes := claims["kubernetes.io"]
kubernetes.namespace == %s
kubernetes.serviceaccount.name == %s
kubernetes.serviceaccount.uid == %s
is_string(kubernetes.pod.uid)
kubernetes.pod.uid != ""
workload_pod_uids[_] == kubernetes.pod.uid
body := json.unmarshal(input.context.request.http.body)
is_object(body)
is_string(body.model)
input.auth.authorization.access.models[_] == body.model
}
workload_pod_uids = %s
publication = %s
`, jsonValue(hash), spec.IssuedAt.Unix(), spec.ValidUntil.Unix(),
		jsonValue("system:serviceaccount:"+w.Namespace+":"+w.ServiceAccountName), jsonValue(w.Audience), jsonValue(w.Namespace),
		jsonValue(w.ServiceAccountName), jsonValue(w.ServiceAccountUID), jsonValue(pods), jsonValue(spec.Publication)) + workloadLicenseRego(spec, options)
}
