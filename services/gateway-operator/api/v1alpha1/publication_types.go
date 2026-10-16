package v1alpha1

// Publication markers identify relay intent, not operator observations. The
// operator copies validated markers to status only after processing that input.
const (
	PublicationDeploymentLabel = "gateway.aep.zhiyuan.io/publication-deployment"
	PublicationSequence        = "gateway.aep.zhiyuan.io/publication-sequence"
	PublicationOperationID     = "gateway.aep.zhiyuan.io/publication-operation-id"
	PublicationContentHash     = "gateway.aep.zhiyuan.io/publication-content-hash"
	PublicationRevision        = "gateway.aep.zhiyuan.io/publication-revision"
)

// PublicationReference correlates conditions with an immutable relay intent.
// It contains no credentials, snapshot payload or data-plane readiness proof.
type PublicationReference struct {
	// +kubebuilder:validation:Minimum=1
	Sequence int64 `json:"sequence"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	OperationID string `json:"operationId"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	Revision string `json:"revision"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	ContentHash string `json:"contentHash"`
}
