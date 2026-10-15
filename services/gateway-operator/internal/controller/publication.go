package controller

import (
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
