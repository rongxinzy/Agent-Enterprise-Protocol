package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type dataPlaneSecretReference struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	// Namespace is accepted for compatibility with earlier clients and ignored:
	// the namespace is resolved server-side (routes read the dedicated credential
	// namespace), so a route cannot pick where it reads from.
	//
	// The tag deliberately has no omitempty. Stored routes are hashed for the
	// desired state and the reconciler re-hashes what it decodes; rows written
	// before the namespace was pinned serialize an explicit null here, so
	// emitting null (rather than dropping the key) keeps one canonical form for
	// new and existing rows and avoids a hash migration.
	Namespace *string `json:"namespace"`
}

type dataPlaneRoute struct {
	ModelID       string                    `json:"modelId"`
	Enabled       bool                      `json:"enabled"`
	Endpoint      string                    `json:"endpoint"`
	UpstreamModel string                    `json:"upstreamModel"`
	Protocol      string                    `json:"protocol"`
	ProviderType  string                    `json:"providerType,omitempty"`
	CredentialRef *dataPlaneSecretReference `json:"credentialRef,omitempty"`
}

type dataPlaneDesiredStateWrite struct {
	Revision string           `json:"revision"`
	Routes   []dataPlaneRoute `json:"routes"`
}

type dataPlaneDesiredState struct {
	dataPlaneDesiredStateWrite
	DeploymentID string    `json:"deploymentId"`
	PublishedAt  time.Time `json:"publishedAt"`
	ContentHash  string    `json:"contentHash"`
}

type dataPlaneStatus struct {
	State            string     `json:"state"`
	ObservedRevision *string    `json:"observedRevision"`
	ContentHash      *string    `json:"contentHash"`
	LastAppliedAt    *time.Time `json:"lastAppliedAt"`
	ErrorCode        *string    `json:"errorCode"`
	Message          *string    `json:"message"`
	ResourceCount    int        `json:"resourceCount"`
}

func normalizeDataPlaneState(input dataPlaneDesiredStateWrite) (dataPlaneDesiredStateWrite, bool) {
	if strings.TrimSpace(input.Revision) == "" || len(input.Revision) > 200 || len(input.Routes) > 500 {
		return dataPlaneDesiredStateWrite{}, false
	}
	for index := range input.Routes {
		route := &input.Routes[index]
		if strings.TrimSpace(route.ModelID) == "" || strings.TrimSpace(route.Endpoint) == "" || strings.TrimSpace(route.UpstreamModel) == "" || (route.Protocol != "openai-compatible" && route.Protocol != "anthropic") {
			return dataPlaneDesiredStateWrite{}, false
		}
		if route.Protocol == "anthropic" {
			// The passthrough renderer derives its cluster, host rewrite, and
			// path rewrite from the endpoint, so it must be absolute; ai-proxy
			// provider types do not apply to passthrough routes.
			if route.ProviderType != "" || !absoluteHTTPURL(route.Endpoint) {
				return dataPlaneDesiredStateWrite{}, false
			}
		} else {
			if route.ProviderType == "" {
				route.ProviderType = "openai"
			}
			if route.ProviderType != "openai" && route.ProviderType != "deepseek" {
				return dataPlaneDesiredStateWrite{}, false
			}
		}
		if route.CredentialRef != nil && (strings.TrimSpace(route.CredentialRef.Name) == "" || strings.TrimSpace(route.CredentialRef.Key) == "") {
			return dataPlaneDesiredStateWrite{}, false
		}
		if route.CredentialRef != nil && route.CredentialRef.Namespace != nil {
			// The namespace comes from the server (the dedicated credential
			// namespace), never from the request: keeping the sent value would
			// store a namespace the reconciler is about to ignore.
			route.CredentialRef.Namespace = nil
		}
	}
	routes := make([]dataPlaneRoute, len(input.Routes))
	copy(routes, input.Routes)
	copyState := dataPlaneDesiredStateWrite{Revision: input.Revision, Routes: routes}
	sort.Slice(copyState.Routes, func(i, j int) bool { return copyState.Routes[i].ModelID < copyState.Routes[j].ModelID })
	for i := 1; i < len(copyState.Routes); i++ {
		if copyState.Routes[i-1].ModelID == copyState.Routes[i].ModelID {
			return dataPlaneDesiredStateWrite{}, false
		}
	}
	return copyState, true
}

func dataPlaneHash(state dataPlaneDesiredStateWrite) string {
	encoded, _ := json.Marshal(state)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (s *Server) getDataPlaneDesiredState(response http.ResponseWriter, request *http.Request) {
	var state dataPlaneDesiredStateWrite
	var enterpriseID, revision, contentHash string
	var publishedAt time.Time
	err := s.app.Database().QueryRow(request.Context(), `SELECT deployment_id,revision,routes,content_hash,published_at FROM data_plane_desired_states WHERE deployment_id=$1`, claimsFrom(request).DeploymentID).Scan(&enterpriseID, &revision, &state.Routes, &contentHash, &publishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(response, http.StatusOK, map[string]any{"deploymentId": claimsFrom(request).DeploymentID, "revision": "", "routes": []dataPlaneRoute{}, "publishedAt": time.Time{}, "contentHash": dataPlaneHash(dataPlaneDesiredStateWrite{Revision: "", Routes: []dataPlaneRoute{}})})
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	state.Revision = revision
	writeJSON(response, http.StatusOK, dataPlaneDesiredState{dataPlaneDesiredStateWrite: state, DeploymentID: enterpriseID, PublishedAt: publishedAt, ContentHash: contentHash})
}

func (s *Server) putDataPlaneDesiredState(response http.ResponseWriter, request *http.Request) {
	var input dataPlaneDesiredStateWrite
	if !decodeJSON(response, request, &input) {
		return
	}
	normalized, valid := normalizeDataPlaneState(input)
	if !valid {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATE", "The desired data-plane state is invalid.")
		return
	}
	tenant := claimsFrom(request).DeploymentID
	// The desired state is a projection of the model catalog, and the raw write
	// must not be a way around that: with a free-form route an operator could
	// name any Secret in the gateway namespace and point its value at an
	// arbitrary upstream. Every route is checked against the catalog-derived
	// route for the same model, so endpoint and credential always come from the
	// catalog and its credential binding.
	models, err := s.app.Store.Deployment(tenant).ListModels(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	derived := make(map[string]dataPlaneRoute)
	for _, route := range deriveDataPlaneRoutes(models) {
		derived[route.ModelID] = route
	}
	for _, route := range normalized.Routes {
		expected, known := derived[route.ModelID]
		if !known {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATE", fmt.Sprintf("The route for model %q is not a publishable gateway model in this catalog.", route.ModelID))
			return
		}
		// The deprecated manual escape hatch may decide how a route renders
		// (provider type, upstream model, protocol, enabled), but never where a
		// credential is read from or where its value is sent.
		if route.Endpoint != expected.Endpoint {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATE", fmt.Sprintf("The route for model %q must use the catalog endpoint %q.", route.ModelID, expected.Endpoint))
			return
		}
		if credentialRefNameKey(route.CredentialRef) != credentialRefNameKey(expected.CredentialRef) {
			// The catalog may bind no credential at all, in which case the route
			// must carry none either - naming it in the message dereferences
			// nothing.
			bound := "none"
			if expected.CredentialRef != nil {
				bound = expected.CredentialRef.Name
			}
			writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATE", fmt.Sprintf("The route for model %q must use the credential Secret bound in the catalog (%s).", route.ModelID, bound))
			return
		}
	}
	hash := dataPlaneHash(normalized)
	var publishedAt time.Time
	err = s.app.Database().QueryRow(request.Context(), `INSERT INTO data_plane_desired_states (deployment_id,revision,routes,content_hash)
VALUES ($1,$2,$3::jsonb,$4)
ON CONFLICT (deployment_id) DO UPDATE SET revision=EXCLUDED.revision,routes=EXCLUDED.routes,content_hash=EXCLUDED.content_hash,published_at=now(),updated_at=now()
RETURNING published_at`, tenant, normalized.Revision, normalized.Routes, hash).Scan(&publishedAt)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	_, _ = s.app.Database().Exec(request.Context(), `INSERT INTO data_plane_statuses (deployment_id,state,resource_count,updated_at) VALUES ($1,'pending',$2,now()) ON CONFLICT (deployment_id) DO UPDATE SET state='pending',error_code=NULL,message=NULL,updated_at=now()`, tenant, len(normalized.Routes))
	writeJSON(response, http.StatusOK, dataPlaneDesiredState{dataPlaneDesiredStateWrite: normalized, DeploymentID: tenant, PublishedAt: publishedAt, ContentHash: hash})
}

func (s *Server) getDataPlaneStatus(response http.ResponseWriter, request *http.Request) {
	var status dataPlaneStatus
	err := s.app.Database().QueryRow(request.Context(), `SELECT state,observed_revision,content_hash,last_applied_at,error_code,message,resource_count FROM data_plane_statuses WHERE deployment_id=$1`, claimsFrom(request).DeploymentID).Scan(&status.State, &status.ObservedRevision, &status.ContentHash, &status.LastAppliedAt, &status.ErrorCode, &status.Message, &status.ResourceCount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		databaseFailure(response, request, err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		status.State = "pending"
	}
	comparison, comparisonErr := s.catalogComparison(request)
	if comparisonErr != nil {
		databaseFailure(response, request, comparisonErr)
		return
	}
	writeJSON(response, http.StatusOK, dataPlaneStatusView{dataPlaneStatus: status, CatalogComparison: comparison})
}

func (s *Server) putInternalDataPlaneStatus(response http.ResponseWriter, request *http.Request) {
	var input dataPlaneStatus
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.State != "pending" && input.State != "applying" && input.State != "ready" && input.State != "degraded" && input.State != "error" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATUS", "The data-plane status is invalid.")
		return
	}
	if input.Message != nil && len(*input.Message) > 2000 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATUS", "The data-plane status message is too long.")
		return
	}
	_, err := s.app.Database().Exec(request.Context(), `INSERT INTO data_plane_statuses (deployment_id,state,observed_revision,content_hash,last_applied_at,error_code,message,resource_count,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT (deployment_id) DO UPDATE SET state=EXCLUDED.state,observed_revision=EXCLUDED.observed_revision,content_hash=EXCLUDED.content_hash,last_applied_at=EXCLUDED.last_applied_at,error_code=EXCLUDED.error_code,message=EXCLUDED.message,resource_count=EXCLUDED.resource_count,updated_at=now()`, claimsFrom(request).DeploymentID, input.State, input.ObservedRevision, input.ContentHash, input.LastAppliedAt, input.ErrorCode, input.Message, input.ResourceCount)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, input)
}
