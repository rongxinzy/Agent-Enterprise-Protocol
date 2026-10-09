package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

func (s *Server) gatewayLimits(response http.ResponseWriter, request *http.Request) {
	items, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GatewayLimits(request.Context(), false)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if id := chi.URLParam(request, "ruleId"); id != "" {
		for _, item := range items {
			if item.ID == id {
				writeJSON(response, http.StatusOK, item)
				return
			}
		}
		writeProblem(response, request, 404, "RESOURCE_NOT_FOUND", "The gateway rule was not found.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) putGatewayLimit(response http.ResponseWriter, request *http.Request) {
	var cfg gatewaypolicy.Configuration
	if !decodeJSON(response, request, &cfg) {
		return
	}
	id, tenant := chi.URLParam(request, "ruleId"), claimsFrom(request).DeploymentID
	if !gatewaypolicy.ValidID(id) || !cfg.Valid() {
		writeProblem(response, request, 400, "INVALID_GATEWAY_RULE", "The native gateway rule configuration is invalid.")
		return
	}
	tx, err := s.app.Database().Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	if _, err = tx.Exec(request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,101))`, tenant); err != nil {
		databaseFailure(response, request, err)
		return
	}
	for _, subject := range []struct {
		kind   string
		target *string
	}{{cfg.ScopeType, cfg.ScopeID}, {"model", cfg.ModelID}} {
		kind, target := subject.kind, subject.target
		if target == nil {
			continue
		}
		table := map[string]string{"user": "users", "team": "teams", "role": "roles", "model": "models"}[kind]
		var exists bool
		if err = tx.QueryRow(request.Context(), `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE deployment_id=$1 AND id=$2)`, tenant, *target).Scan(&exists); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if !exists {
			writeProblem(response, request, 422, "GATEWAY_SUBJECT_NOT_FOUND", "The gateway rule subject is not in this deployment.")
			return
		}
	}
	if cfg.ExpectedVersion == 0 {
		var count int
		if err = tx.QueryRow(request.Context(), `SELECT count(*) FROM gateway_limits WHERE deployment_id=$1`, tenant).Scan(&count); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if count >= 100 {
			writeProblem(response, request, 409, "GATEWAY_RULE_LIMIT", "The deployment gateway rule limit has been reached.")
			return
		}
	}
	data, _ := json.Marshal(cfg)
	item := gatewaypolicy.Limit{ID: id, Configuration: cfg}
	if cfg.ExpectedVersion == 0 {
		err = tx.QueryRow(request.Context(), `INSERT INTO gateway_limits(deployment_id,id,version,configuration) VALUES($1,$2,1,$3::jsonb) ON CONFLICT DO NOTHING RETURNING version,updated_at`, tenant, id, data).Scan(&item.Version, &item.UpdatedAt)
	} else {
		err = tx.QueryRow(request.Context(), `UPDATE gateway_limits SET version=version+1,configuration=$3::jsonb,updated_at=now() WHERE deployment_id=$1 AND id=$2 AND version=$4 AND deleted=false RETURNING version,updated_at`, tenant, id, data, cfg.ExpectedVersion).Scan(&item.Version, &item.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, 409, "GATEWAY_RULE_VERSION_CONFLICT", "The gateway rule version has changed.")
		return
	}
	if err == nil {
		err = tx.Commit(request.Context())
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	slog.Info("gateway rule updated", "deployment_id", tenant, "user_id", claimsFrom(request).Subject, "rule_id", id, "version", item.Version)
	writeJSON(response, http.StatusOK, item)
}

func (s *Server) deleteGatewayLimit(response http.ResponseWriter, request *http.Request) {
	version, err := strconv.ParseInt(request.URL.Query().Get("expectedVersion"), 10, 64)
	if err != nil || version < 1 {
		writeProblem(response, request, 400, "INVALID_GATEWAY_RULE", "The observed rule version is required.")
		return
	}
	tenant := claimsFrom(request).DeploymentID
	tx, err := s.app.Database().Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	if _, err = tx.Exec(request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,101))`, tenant); err != nil {
		databaseFailure(response, request, err)
		return
	}
	result, err := tx.Exec(request.Context(), `UPDATE gateway_limits SET deleted=true,version=version+1,configuration=jsonb_set(configuration,'{enabled}','false'),updated_at=now() WHERE deployment_id=$1 AND id=$2 AND version=$3 AND deleted=false`, tenant, chi.URLParam(request, "ruleId"), version)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() != 1 {
		writeProblem(response, request, 409, "GATEWAY_RULE_VERSION_CONFLICT", "The gateway rule version has changed.")
		return
	}
	if err = tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) publishGatewayLimits(response http.ResponseWriter, request *http.Request) {
	tenant := claimsFrom(request).DeploymentID
	tx, err := s.app.Database().Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	if _, err = tx.Exec(request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,101))`, tenant); err != nil {
		databaseFailure(response, request, err)
		return
	}
	rows, err := tx.Query(request.Context(), `SELECT id,version,configuration,updated_at FROM gateway_limits WHERE deployment_id=$1 ORDER BY id`, tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]gatewaypolicy.Limit, 0)
	for rows.Next() {
		var item gatewaypolicy.Limit
		var data []byte
		err = rows.Scan(&item.ID, &item.Version, &data, &item.UpdatedAt)
		if err == nil {
			err = json.Unmarshal(data, &item.Configuration)
		}
		if err != nil {
			rows.Close()
			databaseFailure(response, request, err)
			return
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	publication := gatewaypolicy.Publication{Revision: gatewaypolicy.Revision(items), Items: items}
	data, _ := json.Marshal(items)
	err = tx.QueryRow(request.Context(), `INSERT INTO gateway_limit_publications(deployment_id,revision,items) VALUES($1,$2,$3::jsonb) ON CONFLICT(deployment_id) DO UPDATE SET revision=EXCLUDED.revision,items=EXCLUDED.items,published_at=CASE WHEN gateway_limit_publications.revision=EXCLUDED.revision THEN gateway_limit_publications.published_at ELSE now() END,state=CASE WHEN gateway_limit_publications.revision=EXCLUDED.revision THEN gateway_limit_publications.state ELSE 'pending' END RETURNING published_at`, tenant, publication.Revision, data).Scan(&publication.PublishedAt)
	if err == nil {
		err = tx.Commit(request.Context())
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publication)
}

func (s *Server) internalGatewayLimits(response http.ResponseWriter, request *http.Request) {
	var publication gatewaypolicy.Publication
	var data []byte
	err := s.app.Database().QueryRow(request.Context(), `SELECT revision,items,published_at FROM gateway_limit_publications WHERE deployment_id=$1`, claimsFrom(request).DeploymentID).Scan(&publication.Revision, &data, &publication.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(response, http.StatusOK, gatewaypolicy.Publication{Items: []gatewaypolicy.Limit{}})
		return
	}
	if err == nil {
		err = json.Unmarshal(data, &publication.Items)
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publication)
}

func (s *Server) gatewayLimitStatus(response http.ResponseWriter, request *http.Request) {
	var state, revision string
	var observed *string
	err := s.app.Database().QueryRow(request.Context(), `SELECT state,revision,observed_revision FROM gateway_limit_publications WHERE deployment_id=$1`, claimsFrom(request).DeploymentID).Scan(&state, &revision, &observed)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(response, http.StatusOK, map[string]any{"state": "unpublished", "revision": nil, "runtimeVerified": false})
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if state == "applied" && (observed == nil || *observed != revision) {
		state = "pending"
	}
	writeJSON(response, http.StatusOK, map[string]any{"state": state, "revision": revision, "runtimeVerified": false})
}

func (s *Server) internalGatewayLimitStatus(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Revision string `json:"revision"`
		State    string `json:"state"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.Revision == "" || (input.State != "applied" && input.State != "error") {
		writeProblem(response, request, 400, "INVALID_GATEWAY_STATUS", "The gateway publication status is invalid.")
		return
	}
	result, err := s.app.Database().Exec(request.Context(), `UPDATE gateway_limit_publications SET state=$3,observed_revision=$2,applied_at=$4 WHERE deployment_id=$1 AND revision=$2`, claimsFrom(request).DeploymentID, input.Revision, input.State, time.Now().UTC())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() != 1 {
		writeProblem(response, request, 409, "GATEWAY_PUBLICATION_CHANGED", "The gateway publication has changed.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"state": input.State, "revision": input.Revision, "runtimeVerified": false})
}
