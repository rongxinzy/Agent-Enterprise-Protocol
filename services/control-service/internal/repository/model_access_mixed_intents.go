package repository

import (
	"slices"
	"strconv"
	"strings"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func mixedAccessIntents(source ModelAccessSource, prior []ModelAccessPublication, version int64, freshness time.Duration, renewal bool) ([]ModelAccessIntent, error) {
	var sessions, workloads []ModelAccessPublication
	for _, publication := range prior {
		policy, err := gatewayaccess.Decode(publication.Payload, publication.ContentHash)
		if err != nil {
			return nil, ErrAccessBinding
		}
		switch policy.Binding.Kind {
		case "session":
			sessions = append(sessions, publication)
		case "kubernetes":
			workloads = append(workloads, publication)
		default:
			return nil, ErrAccessBinding
		}
	}
	intents, err := sessionAccessIntents(source, sessions, version, freshness, renewal)
	if err != nil {
		return nil, err
	}
	additional, err := workloadAccessIntents(source, workloads, version, freshness, renewal)
	if err != nil {
		return nil, err
	}
	intents = append(intents, additional...)
	slices.SortFunc(intents, func(a, b ModelAccessIntent) int {
		return strings.Compare(a.BindingKey, b.BindingKey)
	})
	return intents, nil
}

func workloadAccessIntents(source ModelAccessSource, prior []ModelAccessPublication, version int64, freshness time.Duration, renewal bool) ([]ModelAccessIntent, error) {
	policies := map[string]gatewayaccess.Snapshot{}
	old := map[string]ModelAccessPublication{}
	deadlines := map[string]time.Time{}
	issuedAt := source.ObservedAt.UTC().Truncate(time.Second)
	for _, publication := range prior {
		policy, err := gatewayaccess.Decode(publication.Payload, publication.ContentHash)
		if err != nil || publication.DeploymentID != source.DeploymentID || publication.SubjectID != source.SubjectID ||
			policy.DeploymentID != source.DeploymentID || policy.Subject.ID != source.SubjectID || policy.BindingKey() != publication.BindingKey || policy.Binding.Kind != "kubernetes" {
			return nil, ErrAccessBinding
		}
		if _, duplicate := old[publication.BindingKey]; duplicate {
			return nil, ErrAccessBinding
		}
		old[publication.BindingKey] = publication
		policy.Disabled, policy.Retired = true, true
		policy.Models, policy.RoleIDs, policy.TeamIDs, policy.Licenses = []string{}, nil, nil, nil
		policy.WorkloadEntitlement = nil
		policies[publication.BindingKey] = policy
		deadlines[publication.BindingKey] = issuedAt.Add(freshness)
	}
	// The registry permits one current identity per agent. An expired current
	// observation remains known but denied; only confirmed retirement removes it.
	if len(source.Workloads)+len(source.UnverifiedWorkloads) > 1 {
		return nil, ErrAccessBinding
	}
	for _, current := range []struct {
		values   []ObservedModelWorkload
		verified bool
	}{{source.Workloads, true}, {source.UnverifiedWorkloads, false}} {
		for _, workload := range current.values {
			if !source.Exists || source.Kind != "agent" || !validObservedWorkload(workload) || workload.VerifiedUntil.IsZero() ||
				workload.VerifiedUntil.After(source.ObservedAt) != current.verified {
				return nil, ErrAccessBinding
			}
			binding := workload.Identity
			state := source
			state.Disabled = state.Disabled || !current.verified
			policy := modelAccessSnapshot(state, gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &binding})
			key := policy.BindingKey()
			var preferred *gateway.ModelAccessWorkloadEntitlement
			if previous, exists := old[key]; exists {
				previousPolicy, err := gatewayaccess.Decode(previous.Payload, previous.ContentHash)
				if err != nil || previousPolicy.Retired {
					return nil, ErrAccessBinding
				}
				preferred = previousPolicy.WorkloadEntitlement
			}
			state, evidence := workloadLicenseSource(state, preferred)
			policy = modelAccessSnapshot(state, policy.Binding)
			policy.WorkloadEntitlement = evidence
			deadline, err := modelAccessDeadline(state, workload.VerifiedUntil, freshness)
			if err != nil {
				return nil, err
			}
			policies[key], deadlines[key] = policy, deadline
		}
	}
	return canonicalAccessIntents(source, policies, old, deadlines, version, renewal)
}

func canonicalAccessIntents(source ModelAccessSource, policies map[string]gatewayaccess.Snapshot, old map[string]ModelAccessPublication, deadlines map[string]time.Time, version int64, renewal bool) ([]ModelAccessIntent, error) {
	intents := make([]ModelAccessIntent, 0, len(policies))
	for _, key := range sortedAccessKeys(policies) {
		policy := policies[key]
		payload, hash, err := policy.Canonical()
		if err != nil {
			return nil, err
		}
		issue, deadline := source.ObservedAt.UTC().Truncate(time.Second), deadlines[key]
		previous, exists := old[key]
		if exists && previous.ContentHash == hash && previous.ValidUntil.After(source.ObservedAt) &&
			!previous.ValidUntil.After(deadline) && (!renewal || policy.Disabled || len(policy.Models) == 0) {
			// Source-only changes preserve unchanged effective policy and caches.
			issue, deadline = previous.IssuedAt, previous.ValidUntil
		}
		intents = append(intents, ModelAccessIntent{BindingKey: key, SubjectID: source.SubjectID, SourceVersion: version,
			IssuedAt: issue, ValidUntil: deadline, Snapshot: PublicationSnapshot{DeploymentID: source.DeploymentID,
				Revision: "source-" + strconv.FormatInt(version, 10), ContentHash: hash, Payload: payload}})
	}
	return intents, nil
}
