package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type dataPlaneSecretReference struct {
	Name      string  `json:"name"`
	Key       string  `json:"key"`
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
		if strings.TrimSpace(route.ModelID) == "" || strings.TrimSpace(route.Endpoint) == "" || strings.TrimSpace(route.UpstreamModel) == "" || route.Protocol != "openai-compatible" {
			return dataPlaneDesiredStateWrite{}, false
		}
		if route.ProviderType == "" {
			route.ProviderType = "openai"
		}
		if route.ProviderType != "openai" && route.ProviderType != "deepseek" {
			return dataPlaneDesiredStateWrite{}, false
		}
		if route.CredentialRef != nil && (strings.TrimSpace(route.CredentialRef.Name) == "" || strings.TrimSpace(route.CredentialRef.Key) == "") {
			return dataPlaneDesiredStateWrite{}, false
		}
	}
	copyState := dataPlaneDesiredStateWrite{Revision: input.Revision, Routes: append([]dataPlaneRoute(nil), input.Routes...)}
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
	err := s.app.Pool.QueryRow(request.Context(), `SELECT deployment_id,revision,routes,content_hash,published_at FROM data_plane_desired_states WHERE deployment_id=$1`, claimsFrom(request).Tenant).Scan(&enterpriseID, &revision, &state.Routes, &contentHash, &publishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(response, http.StatusOK, map[string]any{"deploymentId": claimsFrom(request).Tenant, "revision": "", "routes": []dataPlaneRoute{}, "publishedAt": time.Time{}, "contentHash": dataPlaneHash(dataPlaneDesiredStateWrite{Revision: "", Routes: []dataPlaneRoute{}})})
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
	hash := dataPlaneHash(normalized)
	tenant := claimsFrom(request).Tenant
	var publishedAt time.Time
	err := s.app.Pool.QueryRow(request.Context(), `INSERT INTO data_plane_desired_states (deployment_id,revision,routes,content_hash)
VALUES ($1,$2,$3::jsonb,$4)
ON CONFLICT (deployment_id) DO UPDATE SET revision=EXCLUDED.revision,routes=EXCLUDED.routes,content_hash=EXCLUDED.content_hash,published_at=now(),updated_at=now()
RETURNING published_at`, tenant, normalized.Revision, normalized.Routes, hash).Scan(&publishedAt)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	_, _ = s.app.Pool.Exec(request.Context(), `INSERT INTO data_plane_statuses (deployment_id,state,resource_count,updated_at) VALUES ($1,'pending',$2,now()) ON CONFLICT (deployment_id) DO UPDATE SET state='pending',error_code=NULL,message=NULL,updated_at=now()`, tenant, len(normalized.Routes))
	writeJSON(response, http.StatusOK, dataPlaneDesiredState{dataPlaneDesiredStateWrite: normalized, DeploymentID: tenant, PublishedAt: publishedAt, ContentHash: hash})
}

func (s *Server) getDataPlaneStatus(response http.ResponseWriter, request *http.Request) {
	var status dataPlaneStatus
	err := s.app.Pool.QueryRow(request.Context(), `SELECT state,observed_revision,content_hash,last_applied_at,error_code,message,resource_count FROM data_plane_statuses WHERE deployment_id=$1`, claimsFrom(request).Tenant).Scan(&status.State, &status.ObservedRevision, &status.ContentHash, &status.LastAppliedAt, &status.ErrorCode, &status.Message, &status.ResourceCount)
	if errors.Is(err, pgx.ErrNoRows) {
		status.State = "pending"
		writeJSON(response, http.StatusOK, status)
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, status)
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
	_, err := s.app.Pool.Exec(request.Context(), `INSERT INTO data_plane_statuses (deployment_id,state,observed_revision,content_hash,last_applied_at,error_code,message,resource_count,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT (deployment_id) DO UPDATE SET state=EXCLUDED.state,observed_revision=EXCLUDED.observed_revision,content_hash=EXCLUDED.content_hash,last_applied_at=EXCLUDED.last_applied_at,error_code=EXCLUDED.error_code,message=EXCLUDED.message,resource_count=EXCLUDED.resource_count,updated_at=now()`, claimsFrom(request).Tenant, input.State, input.ObservedRevision, input.ContentHash, input.LastAppliedAt, input.ErrorCode, input.Message, input.ResourceCount)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, input)
}
