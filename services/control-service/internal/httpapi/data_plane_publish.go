package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Catalog-derived routes bridge a model's Credential to a conventional
// Kubernetes Secret the reconciler resolves at sync time. The control plane
// never writes Kubernetes and never emits Credential values; the deployment
// Secret system provisions these Secrets out of band.
const (
	dataPlaneCredentialSecretPrefix    = "aep-credential-"
	dataPlaneCredentialSecretKey       = "api-key"
	dataPlaneCredentialSecretNamespace = "higress-system"
	dataPlaneDerivedRevisionPrefix     = "catalog-"
)

type dataPlanePublishRequest struct {
	Revision string `json:"revision"`
}

// deriveDataPlaneRoutes projects publishable catalog models into gateway
// routes: enabled gateway models with an OpenAI-compatible protocol and a
// complete endpoint and upstream model. The catalog is the single source of
// truth, so derivation is total and deterministic.
func deriveDataPlaneRoutes(models []modelRecord) []dataPlaneRoute {
	routes := make([]dataPlaneRoute, 0, len(models))
	for _, model := range models {
		if !model.Enabled || model.SourceType != "gateway" || model.Protocol != "openai-compatible" {
			continue
		}
		if !model.Endpoint.Valid || strings.TrimSpace(model.Endpoint.String) == "" || !model.UpstreamModel.Valid || strings.TrimSpace(model.UpstreamModel.String) == "" {
			continue
		}
		route := dataPlaneRoute{
			ModelID:       model.ID,
			Enabled:       true,
			Endpoint:      model.Endpoint.String,
			UpstreamModel: model.UpstreamModel.String,
			Protocol:      "openai-compatible",
		}
		if model.CredentialID.Valid && strings.TrimSpace(model.CredentialID.String) != "" {
			namespace := dataPlaneCredentialSecretNamespace
			route.CredentialRef = &dataPlaneSecretReference{
				Name:      dataPlaneCredentialSecretPrefix + model.CredentialID.String,
				Key:       dataPlaneCredentialSecretKey,
				Namespace: &namespace,
			}
		}
		routes = append(routes, route)
	}
	return routes
}

// deriveCatalogState normalizes the catalog-derived route set and assigns the
// revision: the caller's explicit one, or a content-addressed catalog revision
// that makes republishing an unchanged catalog idempotent.
func deriveCatalogState(models []modelRecord, revision string) (dataPlaneDesiredStateWrite, bool) {
	normalized, valid := normalizeDataPlaneState(dataPlaneDesiredStateWrite{Revision: dataPlaneDerivedRevisionPrefix, Routes: deriveDataPlaneRoutes(models)})
	if !valid {
		return dataPlaneDesiredStateWrite{}, false
	}
	if revision != "" {
		normalized.Revision = revision
		return normalized, true
	}
	encoded, _ := json.Marshal(normalized.Routes)
	digest := sha256.Sum256(encoded)
	normalized.Revision = dataPlaneDerivedRevisionPrefix + hex.EncodeToString(digest[:])
	return normalized, true
}

func (s *Server) publishDataPlaneRoutes(response http.ResponseWriter, request *http.Request) {
	var input dataPlanePublishRequest
	if request.ContentLength != 0 && !decodeJSON(response, request, &input) {
		return
	}
	input.Revision = strings.TrimSpace(input.Revision)
	if len(input.Revision) > 200 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATE", "The revision is too long.")
		return
	}
	tenant := claimsFrom(request).DeploymentID
	models, err := s.app.Store.Deployment(tenant).ListModels(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	normalized, valid := deriveCatalogState(models, input.Revision)
	if !valid {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_PLANE_STATE", "The catalog-derived data-plane state is invalid.")
		return
	}
	hash := dataPlaneHash(normalized)
	var storedHash string
	var publishedAt time.Time
	err = s.app.Database().QueryRow(request.Context(), `SELECT content_hash,published_at FROM data_plane_desired_states WHERE deployment_id=$1`, tenant).Scan(&storedHash, &publishedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		databaseFailure(response, request, err)
		return
	}
	// Publishing an unchanged catalog is a no-op: the content hash covers the
	// revision and canonical routes, so the derived state is the stored state
	// and the reconciliation status stays untouched.
	if err == nil && storedHash == hash {
		writeJSON(response, http.StatusOK, dataPlaneDesiredState{dataPlaneDesiredStateWrite: normalized, DeploymentID: tenant, PublishedAt: publishedAt, ContentHash: storedHash})
		return
	}
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

type dataPlaneRouteMismatch struct {
	ModelID string   `json:"modelId"`
	Fields  []string `json:"fields"`
}

// dataPlaneCatalogComparison is the drift between the route set the model
// catalog would publish and the routes currently stored as desired state.
type dataPlaneCatalogComparison struct {
	Missing    []string                 `json:"missing"`
	Extra      []string                 `json:"extra"`
	Mismatched []dataPlaneRouteMismatch `json:"mismatched"`
}

type dataPlaneStatusView struct {
	dataPlaneStatus
	CatalogComparison dataPlaneCatalogComparison `json:"catalogComparison"`
}

func (s *Server) catalogComparison(request *http.Request) (dataPlaneCatalogComparison, error) {
	tenant := claimsFrom(request).DeploymentID
	comparison := dataPlaneCatalogComparison{Missing: []string{}, Extra: []string{}, Mismatched: []dataPlaneRouteMismatch{}}
	models, err := s.app.Store.Deployment(tenant).ListModels(request.Context())
	if err != nil {
		return comparison, err
	}
	derived, valid := deriveCatalogState(models, "")
	if !valid {
		return comparison, errors.New("catalog-derived route set exceeds the data-plane limit")
	}
	storedRoutes := []dataPlaneRoute{}
	var rawRoutes json.RawMessage
	err = s.app.Database().QueryRow(request.Context(), `SELECT routes FROM data_plane_desired_states WHERE deployment_id=$1`, tenant).Scan(&rawRoutes)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return comparison, err
	}
	if len(rawRoutes) > 0 {
		if err := json.Unmarshal(rawRoutes, &storedRoutes); err != nil {
			return comparison, err
		}
	}
	return compareCatalogRoutes(storedRoutes, derived.Routes), nil
}

// compareCatalogRoutes diffs stored desired routes against catalog-derived
// ones. credentialRef comparison covers name and key, the parts the
// reconciler resolves; the namespace is currently always higress-system.
func compareCatalogRoutes(stored, derived []dataPlaneRoute) dataPlaneCatalogComparison {
	comparison := dataPlaneCatalogComparison{Missing: []string{}, Extra: []string{}, Mismatched: []dataPlaneRouteMismatch{}}
	storedByID := make(map[string]dataPlaneRoute, len(stored))
	for _, route := range stored {
		storedByID[route.ModelID] = route
	}
	derivedByID := make(map[string]dataPlaneRoute, len(derived))
	for _, route := range derived {
		derivedByID[route.ModelID] = route
		existing, ok := storedByID[route.ModelID]
		if !ok {
			comparison.Missing = append(comparison.Missing, route.ModelID)
			continue
		}
		if fields := routeDiffFields(existing, route); len(fields) > 0 {
			comparison.Mismatched = append(comparison.Mismatched, dataPlaneRouteMismatch{ModelID: route.ModelID, Fields: fields})
		}
	}
	for _, route := range stored {
		if _, ok := derivedByID[route.ModelID]; !ok {
			comparison.Extra = append(comparison.Extra, route.ModelID)
		}
	}
	return comparison
}

func routeDiffFields(stored, derived dataPlaneRoute) []string {
	fields := make([]string, 0, 5)
	if stored.Enabled != derived.Enabled {
		fields = append(fields, "enabled")
	}
	if stored.Endpoint != derived.Endpoint {
		fields = append(fields, "endpoint")
	}
	if stored.UpstreamModel != derived.UpstreamModel {
		fields = append(fields, "upstreamModel")
	}
	if stored.ProviderType != derived.ProviderType {
		fields = append(fields, "providerType")
	}
	if credentialRefNameKey(stored.CredentialRef) != credentialRefNameKey(derived.CredentialRef) {
		fields = append(fields, "credentialRef")
	}
	return fields
}

func credentialRefNameKey(reference *dataPlaneSecretReference) string {
	if reference == nil {
		return ""
	}
	return reference.Name + "\x00" + reference.Key
}
