package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A relay writes children before committing the aggregate marker. Never render
// a mixture of publications while delivery or informer caches are catching up.
// Unmarked CRs remain supported only in the existing direct-admin mode.
func consistentPublication(deployment *gateway.DeploymentConfig, routes []gateway.ModelRoute, policies []gateway.ModelPolicy) error {
	objects := make([]metav1.Object, 0, len(routes)+len(policies))
	for i := range routes {
		objects = append(objects, &routes[i])
	}
	for i := range policies {
		objects = append(objects, &policies[i])
	}
	marked := func(obj metav1.Object) bool {
		a := obj.GetAnnotations()
		return obj.GetLabels()[gateway.PublicationDeploymentLabel] != "" || a[gateway.PublicationSequence] != "" || a[gateway.PublicationOperationID] != "" || a[gateway.PublicationRevision] != "" || a[gateway.PublicationContentHash] != ""
	}
	if !marked(deployment) {
		for _, obj := range objects {
			if marked(obj) {
				return errPublicationPending
			}
		}
		return nil
	}
	ref := publicationReference(deployment)
	sum := sha256.Sum256([]byte(deployment.Spec.DeploymentID))
	owner := hex.EncodeToString(sum[:16])
	if ref == nil || deployment.Labels[gateway.PublicationDeploymentLabel] != owner {
		return errPublicationPending
	}
	for _, obj := range objects {
		other := publicationReference(obj)
		if other == nil || *other != *ref || obj.GetLabels()[gateway.PublicationDeploymentLabel] != owner || !obj.GetDeletionTimestamp().IsZero() {
			return errPublicationPending
		}
	}
	return nil
}

func publicationReference(obj metav1.Object) *gateway.PublicationReference {
	a := obj.GetAnnotations()
	sequence, err := strconv.ParseInt(a[gateway.PublicationSequence], 10, 64)
	op, uuidErr := uuid.Parse(a[gateway.PublicationOperationID])
	hash, hashErr := hex.DecodeString(a[gateway.PublicationContentHash])
	if err != nil || sequence < 1 || uuidErr != nil || op.String() != a[gateway.PublicationOperationID] ||
		hashErr != nil || len(hash) != 32 || strings.ToLower(a[gateway.PublicationContentHash]) != a[gateway.PublicationContentHash] ||
		strings.TrimSpace(a[gateway.PublicationRevision]) == "" || utf8.RuneCountInString(a[gateway.PublicationRevision]) > 200 {
		return nil
	}
	return &gateway.PublicationReference{Sequence: sequence, OperationID: op.String(), Revision: a[gateway.PublicationRevision], ContentHash: a[gateway.PublicationContentHash]}
}
