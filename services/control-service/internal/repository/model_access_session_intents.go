package repository

import (
	"slices"
	"strconv"
	"strings"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func sessionSnapshot(source ModelAccessSource, session ModelAccessSession) gatewayaccess.Snapshot {
	policy := gatewayaccess.Snapshot{Schema: "aep-model-access/v1", DeploymentID: source.DeploymentID,
		Subject:  gateway.ModelAccessSubject{Kind: source.Kind, ID: source.SubjectID},
		Binding:  gateway.ModelAccessBinding{Kind: "session", SessionID: session.ID},
		Disabled: source.Disabled, Models: []string{}}
	if source.Disabled {
		return policy
	}
	policy.RoleIDs, policy.TeamIDs = slices.Clone(source.RoleIDs), slices.Clone(source.TeamIDs)
	slices.Sort(policy.RoleIDs)
	slices.Sort(policy.TeamIDs)
	for _, model := range source.Models {
		policy.Models = append(policy.Models, model.ID)
	}
	slices.Sort(policy.Models)
	for _, license := range source.Licenses {
		binding := gateway.ModelAccessLicense{ID: license.ID, Digest: license.Digest}
		if license.ExpiresAt != nil {
			deadline := metav1.NewTime(license.ExpiresAt.UTC().Truncate(time.Second))
			binding.ValidUntil = &deadline
		}
		policy.Licenses = append(policy.Licenses, binding)
	}
	slices.SortFunc(policy.Licenses, func(a, b gateway.ModelAccessLicense) int { return strings.Compare(a.ID, b.ID) })
	return policy
}

func sessionDeadline(source ModelAccessSource, session ModelAccessSession, freshness time.Duration) (time.Time, error) {
	// metav1.Time's JSON contract has second precision. Never round a security
	// deadline up; too-close boundaries are retried after expiry, not extended.
	deadline := source.ObservedAt.UTC().Truncate(time.Second).Add(freshness).Truncate(time.Second)
	consider := func(value *time.Time) {
		if value != nil && value.Before(deadline) {
			deadline = value.UTC().Truncate(time.Second)
		}
	}
	if !source.Disabled {
		consider(&session.ExpiresAt)
		consider(source.SubjectExpiresAt)
		for _, model := range source.Models {
			consider(model.ExpiresAt)
		}
		for _, license := range source.Licenses {
			consider(license.ExpiresAt)
		}
	}
	if !deadline.After(source.ObservedAt) {
		return time.Time{}, ErrAccessExpiring
	}
	return deadline, nil
}

// sessionAccessIntents covers the UNION of current sessions and prior streams.
// Removed sessions retain permanent terminal tombstones; mutable user/session
// tables cannot resurrect an old signed binding by reusing deleted identifiers.
func sessionAccessIntents(source ModelAccessSource, prior []ModelAccessPublication, version int64, freshness time.Duration, renewal bool) ([]ModelAccessIntent, error) {
	policies := map[string]gatewayaccess.Snapshot{}
	old := map[string]ModelAccessPublication{}
	oldSessions := map[string]string{}
	deadlines := map[string]time.Time{}
	issuedAt := source.ObservedAt.UTC().Truncate(time.Second)
	for _, publication := range prior {
		policy, err := gatewayaccess.Decode(publication.Payload, publication.ContentHash)
		if err != nil || publication.DeploymentID != source.DeploymentID || publication.SubjectID != source.SubjectID ||
			policy.DeploymentID != source.DeploymentID || policy.Subject.ID != source.SubjectID || policy.BindingKey() != publication.BindingKey || policy.Binding.Kind != "session" {
			return nil, ErrAccessBinding
		}
		if _, duplicate := oldSessions[policy.Binding.SessionID]; duplicate {
			return nil, ErrAccessBinding
		}
		oldSessions[policy.Binding.SessionID] = publication.BindingKey
		old[publication.BindingKey] = publication
		policy.Disabled, policy.Retired = true, true
		policy.Models, policy.RoleIDs, policy.TeamIDs, policy.Licenses = []string{}, nil, nil, nil
		policies[publication.BindingKey] = policy
		deadlines[publication.BindingKey] = issuedAt.Add(freshness).Truncate(time.Second)
	}
	for _, session := range source.Sessions {
		if !source.Exists || session.ExpiresAt.IsZero() || !session.ExpiresAt.After(source.ObservedAt) {
			return nil, ErrAccessBinding
		}
		policy := sessionSnapshot(source, session)
		key := policy.BindingKey()
		if oldKey, ok := oldSessions[session.ID]; ok {
			previous := old[oldKey]
			previousPolicy, err := gatewayaccess.Decode(previous.Payload, previous.ContentHash)
			if err != nil || oldKey != key || previousPolicy.Retired {
				return nil, ErrAccessBinding
			}
		}
		deadline, err := sessionDeadline(source, session, freshness)
		if err != nil {
			return nil, err
		}
		policies[key], deadlines[key] = policy, deadline
	}
	intents := make([]ModelAccessIntent, 0, len(policies))
	for _, key := range sortedAccessKeys(policies) {
		policy := policies[key]
		payload, hash, err := policy.Canonical()
		if err != nil {
			return nil, err
		}
		issue, deadline := issuedAt, deadlines[key]
		previous, exists := old[key]
		if exists && previous.ContentHash == hash && previous.ValidUntil.After(source.ObservedAt) &&
			!previous.ValidUntil.After(deadline) && (!renewal || policy.Disabled || len(policy.Models) == 0) {
			// Advance the source fence without resending unchanged effective policy.
			// Neither metadata/token rotation nor a redundant role write is a flush.
			issue, deadline = previous.IssuedAt, previous.ValidUntil
		}
		intents = append(intents, ModelAccessIntent{BindingKey: key, SubjectID: source.SubjectID, SourceVersion: version,
			IssuedAt: issue, ValidUntil: deadline, Snapshot: PublicationSnapshot{DeploymentID: source.DeploymentID,
				Revision: "source-" + strconv.FormatInt(version, 10), ContentHash: hash, Payload: payload}})
	}
	return intents, nil
}
