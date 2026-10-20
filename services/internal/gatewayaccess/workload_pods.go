package gatewayaccess

import "regexp"

var podUID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ValidWorkloadPods validates mutable lifecycle evidence, never identity or grants.
// Canonical publication additionally requires a sorted list. Missing evidence is
// transport-compatible with older snapshots, but the native bounds must deny it.
func ValidWorkloadPods(kind string, pods []string) bool {
	if kind == "session" {
		return len(pods) == 0
	}
	if kind != "kubernetes" || len(pods) > 512 {
		return false
	}
	seen := make(map[string]bool, len(pods))
	for _, uid := range pods {
		if len(uid) > 128 || !podUID.MatchString(uid) || seen[uid] {
			return false
		}
		seen[uid] = true
	}
	return true
}
