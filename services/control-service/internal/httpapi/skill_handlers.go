package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Server) listSkills(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT id, name, description, enabled, created_at, updated_at FROM skills ORDER BY id`)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, description string
		var enabled bool
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &name, &description, &enabled, &createdAt, &updatedAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "description": description, "enabled": enabled, "createdAt": createdAt, "updatedAt": updatedAt})
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createSkill(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     *bool  `json:"enabled"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	var createdAt, updatedAt time.Time
	err := s.app.Pool.QueryRow(request.Context(), `INSERT INTO skills (id,name,description,enabled) VALUES ($1,$2,$3,$4) RETURNING created_at,updated_at`, input.ID, input.Name, input.Description, enabled).Scan(&createdAt, &updatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "SKILL_ALREADY_EXISTS", "The Skill already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": input.ID, "name": input.Name, "description": input.Description, "enabled": enabled, "createdAt": createdAt, "updatedAt": updatedAt})
}

func (s *Server) getSkill(response http.ResponseWriter, request *http.Request) {
	var id, name, description string
	var enabled bool
	var createdAt, updatedAt time.Time
	err := s.app.Pool.QueryRow(request.Context(), `SELECT id,name,description,enabled,created_at,updated_at FROM skills WHERE id=$1`, chi.URLParam(request, "skillId")).Scan(&id, &name, &description, &enabled, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"id": id, "name": name, "description": description, "enabled": enabled, "createdAt": createdAt, "updatedAt": updatedAt})
}

func (s *Server) updateSkill(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	var id, name, description string
	var enabled bool
	var createdAt, updatedAt time.Time
	err := s.app.Pool.QueryRow(request.Context(), `UPDATE skills SET name=COALESCE($2,name), description=COALESCE($3,description), enabled=COALESCE($4,enabled), updated_at=now() WHERE id=$1 RETURNING id,name,description,enabled,created_at,updated_at`, chi.URLParam(request, "skillId"), input.Name, input.Description, input.Enabled).Scan(&id, &name, &description, &enabled, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"id": id, "name": name, "description": description, "enabled": enabled, "createdAt": createdAt, "updatedAt": updatedAt})
}

func (s *Server) deleteSkill(response http.ResponseWriter, request *http.Request) {
	result, err := s.app.Pool.Exec(request.Context(), `DELETE FROM skills WHERE id=$1`, chi.URLParam(request, "skillId"))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill was not found.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadSkillVersion(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, 34<<20)
	if err := request.ParseMultipartForm(33 << 20); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The multipart Skill upload is invalid or too large.")
		return
	}
	version := request.FormValue("version")
	file, _, err := request.FormFile("package")
	if err != nil || version == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "Version and package are required.")
		return
	}
	defer file.Close()
	archive, err := io.ReadAll(io.LimitReader(file, 32<<20+1))
	if err != nil || len(archive) > 32<<20 {
		writeProblem(response, request, http.StatusRequestEntityTooLarge, "PACKAGE_TOO_LARGE", "The Skill package exceeds 32 MiB.")
		return
	}
	digest := sha256.Sum256(archive)
	sha := hex.EncodeToString(digest[:])
	skillID := chi.URLParam(request, "skillId")
	objectKey := strings.Join([]string{"skills", skillID, version, sha + ".zip"}, "/")
	if err := s.app.Blobs.Put(request.Context(), objectKey, archive); err != nil {
		databaseFailure(response, request, err)
		return
	}
	_, err = s.app.Pool.Exec(request.Context(), `INSERT INTO skill_versions (skill_id,version,object_key,sha256,size_bytes) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (skill_id,version) DO UPDATE SET object_key=EXCLUDED.object_key,sha256=EXCLUDED.sha256,size_bytes=EXCLUDED.size_bytes,published=false,published_at=NULL`, skillID, version, objectKey, sha, len(archive))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"skillId": skillID, "version": version, "sha256": sha, "size": len(archive), "published": false})
}

func (s *Server) publishSkillVersion(response http.ResponseWriter, request *http.Request) {
	skillID, version := chi.URLParam(request, "skillId"), chi.URLParam(request, "version")
	result, err := s.app.Pool.Exec(request.Context(), `UPDATE skill_versions SET published=true,published_at=now() WHERE skill_id=$1 AND version=$2`, skillID, version)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill version was not found.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"skillId": skillID, "version": version, "published": true})
}

func (s *Server) listSkillAssignments(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT id,skill_id,subject_type,subject_id,created_at FROM skill_assignments WHERE enterprise_id=$1 ORDER BY id`, claimsFrom(request).Tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, skillID, subjectType, subjectID string
		var createdAt time.Time
		if err := rows.Scan(&id, &skillID, &subjectType, &subjectID, &createdAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"id": id, "skillId": skillID, "subject": map[string]string{"type": subjectType, "id": subjectID}, "createdAt": createdAt})
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createSkillAssignment(response http.ResponseWriter, request *http.Request) {
	var input struct {
		SkillID string `json:"skillId"`
		Subject struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"subject"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.Subject.Type != "enterprise" && input.Subject.Type != "organization" && input.Subject.Type != "user" && input.Subject.Type != "agent" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_SUBJECT", "The assignment subject type is invalid.")
		return
	}
	id := uuid.NewString()
	claims := claimsFrom(request)
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	_, err = tx.Exec(request.Context(), `INSERT INTO skill_assignments (id,enterprise_id,skill_id,subject_type,subject_id) VALUES ($1,$2,$3,$4,$5)`, id, claims.Tenant, input.SkillID, input.Subject.Type, input.Subject.ID)
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "ASSIGNMENT_EXISTS", "The assignment already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	if err := createSkillAssignmentEvent(request.Context(), tx, claims.Tenant, claims.Subject, input.SkillID, input.Subject.Type, input.Subject.ID, "assigned:"+id); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": id, "skillId": input.SkillID, "subject": input.Subject})
}

func (s *Server) deleteSkillAssignment(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	assignmentID := chi.URLParam(request, "assignmentId")
	var skillID, subjectType, subjectID string
	err = tx.QueryRow(request.Context(), `DELETE FROM skill_assignments WHERE id=$1 AND enterprise_id=$2 RETURNING skill_id,subject_type,subject_id`, assignmentID, claims.Tenant).Scan(&skillID, &subjectType, &subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The assignment was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := createSkillAssignmentEvent(request.Context(), tx, claims.Tenant, claims.Subject, skillID, subjectType, subjectID, "revoked:"+assignmentID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func createSkillAssignmentEvent(ctx context.Context, tx pgx.Tx, enterpriseID, createdBy, skillID, subjectType, subjectID, revision string) error {
	scopeType, scopeID := skillAssignmentEventScope(subjectType, subjectID)
	eventID := uuid.NewString()
	supersedesKey := strings.Join([]string{"skill-manifest", skillID, scopeType, subjectID}, ":")
	_, err := tx.Exec(ctx, `INSERT INTO control_events (event_id,enterprise_id,type,scope_type,scope_id,resource_type,resource_id,resource_revision,task_type,supersedes_key,expires_at,created_by) VALUES ($1,$2,'skill.manifest.changed',$3,$4,'skill',$5,$6,'skill.reconcile',$7,$8,$9)`, eventID, enterpriseID, scopeType, scopeID, skillID, revision, supersedesKey, time.Now().UTC().Add(24*time.Hour), createdBy)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH old AS (UPDATE control_events SET state='superseded' WHERE enterprise_id=$1 AND supersedes_key=$2 AND event_id<>$3 AND state='active' RETURNING event_id) UPDATE control_deliveries SET state='superseded',updated_at=now() WHERE event_id IN (SELECT event_id FROM old) AND state='pending'`, enterpriseID, supersedesKey, eventID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO control_deliveries (delivery_id,event_id,agent_id)
SELECT gen_random_uuid()::text,$1,a.agent_id
FROM agents a JOIN users u ON u.id=a.user_id
WHERE a.enterprise_id=$2 AND ($3='global' OR ($3='agent' AND a.agent_id=$4) OR ($3='user' AND a.user_id=$4) OR ($3='organization' AND $4=ANY(u.organization_ids)))`, eventID, enterpriseID, scopeType, scopeID)
	return err
}

func skillAssignmentEventScope(subjectType, subjectID string) (string, *string) {
	if subjectType == "enterprise" {
		return "global", nil
	}
	return subjectType, &subjectID
}

func (s *Server) skillManifest(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	rows, err := s.app.Pool.Query(request.Context(), `WITH authorized AS (
SELECT DISTINCT sk.id,sk.name,sv.version,sv.sha256,sv.size_bytes
FROM skills sk JOIN skill_versions sv ON sv.skill_id=sk.id AND sv.published=true
JOIN skill_assignments sa ON sa.skill_id=sk.id AND sa.enterprise_id=$1
JOIN users u ON u.id=$2
WHERE sk.enabled=true AND ((sa.subject_type='enterprise' AND sa.subject_id=$1) OR (sa.subject_type='user' AND sa.subject_id=$2) OR (sa.subject_type='agent' AND sa.subject_id=$3) OR (sa.subject_type='organization' AND sa.subject_id=ANY(u.organization_ids)))
), latest AS (SELECT DISTINCT ON (id) * FROM authorized ORDER BY id,version DESC)
SELECT id,name,version,sha256,size_bytes FROM latest ORDER BY id`, claims.Tenant, claims.Subject, claims.AgentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, version, sha string
		var size int64
		if err := rows.Scan(&id, &name, &version, &sha, &size); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "version": version, "enabled": true, "package": map[string]any{"url": "/aep/v1/agent/skills/" + id + "/versions/" + version + "/package", "sha256": sha, "size": size}})
	}
	encoded, _ := json.Marshal(items)
	digest := sha256.Sum256(encoded)
	revision := hex.EncodeToString(digest[:16])
	etag := "\"" + revision + "\""
	response.Header().Set("ETag", etag)
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"revision": revision, "generatedAt": time.Now().UTC(), "skills": items})
}

func (s *Server) downloadSkillPackage(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	skillID, version := chi.URLParam(request, "skillId"), chi.URLParam(request, "version")
	var objectKey string
	err := s.app.Pool.QueryRow(request.Context(), `SELECT sv.object_key FROM skill_versions sv JOIN skill_assignments sa ON sa.skill_id=sv.skill_id JOIN users u ON u.id=$2 WHERE sv.skill_id=$4 AND sv.version=$5 AND sv.published=true AND sa.enterprise_id=$1 AND ((sa.subject_type='enterprise' AND sa.subject_id=$1) OR (sa.subject_type='user' AND sa.subject_id=$2) OR (sa.subject_type='agent' AND sa.subject_id=$3) OR (sa.subject_type='organization' AND sa.subject_id=ANY(u.organization_ids))) LIMIT 1`, claims.Tenant, claims.Subject, claims.AgentID, skillID, version).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusForbidden, "SKILL_NOT_ASSIGNED", "The Skill version is not assigned.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	object, err := s.app.Blobs.Get(request.Context(), objectKey)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer object.Close()
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.Copy(response, object)
}

func (s *Server) reportSkillSyncResult(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Revision string `json:"revision"`
		Status   string `json:"status"`
		Items    []struct {
			SkillID string `json:"skillId"`
			Status  string `json:"status"`
		} `json:"items"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	installed := make([]string, 0)
	for _, item := range input.Items {
		if item.Status == "installed" || item.Status == "updated" || item.Status == "unchanged" {
			installed = append(installed, item.SkillID)
		}
	}
	sort.Strings(installed)
	payload, _ := json.Marshal(input)
	claims := claimsFrom(request)
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	_, err = tx.Exec(request.Context(), "INSERT INTO skill_sync_results (id,enterprise_id,user_id,agent_id,revision,status,installed_skill_ids,payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)", uuid.NewString(), claims.Tenant, claims.Subject, claims.AgentID, input.Revision, input.Status, installed, payload)
	if err == nil {
		_, err = tx.Exec(request.Context(), "UPDATE agents SET applied_skill_revision=$2,installed_skill_ids=$3,last_seen_at=now() WHERE agent_id=$1", claims.AgentID, input.Revision, installed)
	}
	if err == nil {
		err = tx.Commit(request.Context())
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusAccepted)
}
