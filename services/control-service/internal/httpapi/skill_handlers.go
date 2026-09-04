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

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func (s *Server) listSkills(response http.ResponseWriter, request *http.Request) {
	pageSize := limit(request)
	skills, err := s.app.Store.ListSkillsPage(request.Context(), request.URL.Query().Get("cursor"), pageSize+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	skills, nextCursor := page(skills, pageSize, func(item repository.Skill) string { return item.ID })
	items := make([]map[string]any, 0, len(skills))
	for _, skill := range skills {
		versions, versionErr := s.app.Store.ListSkillVersions(request.Context(), skill.ID)
		if versionErr != nil {
			databaseFailure(response, request, versionErr)
			return
		}
		items = append(items, skillJSON(skill, versions))
	}
	writeJSON(response, http.StatusOK, map[string]any{"skills": items, "nextCursor": nextCursor})
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
	skill, err := s.app.Store.CreateSkill(request.Context(), repository.Skill{
		ID: input.ID, Name: input.Name, Description: input.Description, Enabled: enabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "SKILL_ALREADY_EXISTS", "The Skill already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, skillJSON(skill, nil))
}

func (s *Server) getSkill(response http.ResponseWriter, request *http.Request) {
	skill, err := s.app.Store.GetSkill(request.Context(), chi.URLParam(request, "skillId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	versions, versionErr := s.app.Store.ListSkillVersions(request.Context(), skill.ID)
	if versionErr != nil {
		databaseFailure(response, request, versionErr)
		return
	}
	writeJSON(response, http.StatusOK, skillJSON(skill, versions))
}

func (s *Server) updateSkill(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
		State       *string `json:"state"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	enabled, err := skillEnabledFromPatch(input.State, input.Enabled)
	if err != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_SKILL_STATE", err.Error())
		return
	}
	skill, err := s.app.Store.UpdateSkill(request.Context(), chi.URLParam(request, "skillId"), repository.UpdateSkillParams{
		Name: input.Name, Description: input.Description, Enabled: enabled,
	})
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	versions, versionErr := s.app.Store.ListSkillVersions(request.Context(), skill.ID)
	if versionErr != nil {
		databaseFailure(response, request, versionErr)
		return
	}
	writeJSON(response, http.StatusOK, skillJSON(skill, versions))
}

func (s *Server) deleteSkill(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.DeleteSkill(request.Context(), chi.URLParam(request, "skillId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func skillJSON(skill repository.Skill, versions []repository.SkillVersion) map[string]any {
	state := "withdrawn"
	if skill.Enabled {
		state = "active"
	}
	return map[string]any{
		"id": skill.ID, "name": skill.Name, "description": skill.Description,
		"state": state, "enabled": skill.Enabled, "createdAt": skill.CreatedAt, "updatedAt": skill.UpdatedAt,
		"versions": skillVersionsJSON(versions),
	}
}

func skillEnabledFromPatch(state *string, enabled *bool) (*bool, error) {
	if state == nil {
		return enabled, nil
	}
	var value bool
	switch *state {
	case "active":
		value = true
	case "withdrawn":
		value = false
	default:
		return nil, errors.New("Skill state must be active or withdrawn.")
	}
	if enabled != nil && *enabled != value {
		return nil, errors.New("Skill state and enabled fields conflict.")
	}
	return &value, nil
}

func skillVersionsJSON(versions []repository.SkillVersion) []map[string]any {
	items := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		state := "draft"
		if version.Published {
			state = "published"
		}
		items = append(items, map[string]any{
			"version":   version.Version,
			"state":     state,
			"sha256":    version.SHA256,
			"size":      version.SizeBytes,
			"createdAt": version.CreatedAt,
		})
	}
	return items
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
	err = s.app.Store.UpsertSkillVersion(request.Context(), repository.SkillVersion{
		SkillID: skillID, Version: version, ObjectKey: objectKey, SHA256: sha, SizeBytes: int64(len(archive)),
	})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"skillId": skillID, "version": version, "sha256": sha, "size": len(archive), "published": false})
}

func (s *Server) publishSkillVersion(response http.ResponseWriter, request *http.Request) {
	skillID, version := chi.URLParam(request, "skillId"), chi.URLParam(request, "version")
	err := s.app.Store.PublishSkillVersion(request.Context(), skillID, version)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill version was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"skillId": skillID, "version": version, "published": true})
}

func (s *Server) deleteSkillVersion(response http.ResponseWriter, request *http.Request) {
	skillID, version := chi.URLParam(request, "skillId"), chi.URLParam(request, "version")
	objectKey, err := s.app.Store.DeleteSkillVersion(request.Context(), skillID, version)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Skill version was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.Blobs.Delete(request.Context(), objectKey); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSkillAssignments(response http.ResponseWriter, request *http.Request) {
	assignments, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListSkillAssignments(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(assignments))
	for _, assignment := range assignments {
		items = append(items, map[string]any{
			"id": assignment.ID, "skillId": assignment.SkillID,
			"subject":   map[string]string{"type": assignment.SubjectType, "id": assignment.SubjectID},
			"createdAt": assignment.CreatedAt,
		})
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
	if input.Subject.Type != "user" && input.Subject.Type != "role" && input.Subject.Type != "team" {
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
	_, err = tx.Exec(request.Context(), `INSERT INTO skill_assignments (id,deployment_id,skill_id,subject_type,subject_id) VALUES ($1,$2,$3,$4,$5)`, id, claims.DeploymentID, input.SkillID, input.Subject.Type, input.Subject.ID)
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "ASSIGNMENT_EXISTS", "The assignment already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	if err := createSkillAssignmentEvent(request.Context(), tx, claims.DeploymentID, claims.Subject, input.SkillID, input.Subject.Type, input.Subject.ID, "assigned:"+id); err != nil {
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
	err = tx.QueryRow(request.Context(), `DELETE FROM skill_assignments WHERE id=$1 AND deployment_id=$2 RETURNING skill_id,subject_type,subject_id`, assignmentID, claims.DeploymentID).Scan(&skillID, &subjectType, &subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The assignment was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := createSkillAssignmentEvent(request.Context(), tx, claims.DeploymentID, claims.Subject, skillID, subjectType, subjectID, "revoked:"+assignmentID); err != nil {
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
	_, err := tx.Exec(ctx, `INSERT INTO control_events (event_id,deployment_id,type,scope_type,scope_id,resource_type,resource_id,resource_revision,task_type,supersedes_key,expires_at,created_by) VALUES ($1,$2,'skill.manifest.changed',$3,$4,'skill',$5,$6,'skill.reconcile',$7,$8,$9)`, eventID, enterpriseID, scopeType, scopeID, skillID, revision, supersedesKey, time.Now().UTC().Add(24*time.Hour), createdBy)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH old AS (UPDATE control_events SET state='superseded' WHERE deployment_id=$1 AND supersedes_key=$2 AND event_id<>$3 AND state='active' RETURNING event_id) UPDATE session_control_deliveries SET state='superseded',updated_at=now() WHERE event_id IN (SELECT event_id FROM old) AND state='pending'`, enterpriseID, supersedesKey, eventID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_control_deliveries (delivery_id,event_id,session_id)
SELECT gen_random_uuid()::text,$1,s.session_id
FROM user_sessions s JOIN users u ON u.id=s.user_id
WHERE s.deployment_id=$2 AND s.revoked_at IS NULL
  AND ($3='global' OR ($3='user' AND s.user_id=$4) OR ($3='team' AND EXISTS (SELECT 1 FROM user_team_bindings utb WHERE utb.deployment_id=$2 AND utb.user_id=s.user_id AND utb.team_id=$4)))
ON CONFLICT (event_id,session_id) DO NOTHING`, eventID, enterpriseID, scopeType, scopeID)
	if err != nil {
		return err
	}
	return nil
}

func skillAssignmentEventScope(subjectType, subjectID string) (string, *string) {
	return subjectType, &subjectID
}

func (s *Server) skillManifest(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	rows, err := s.app.Pool.Query(request.Context(), `WITH authorized AS (
SELECT DISTINCT sk.id,sk.name,sv.version,sv.sha256,sv.size_bytes
FROM skills sk JOIN skill_versions sv ON sv.skill_id=sk.id AND sv.published=true
JOIN skill_assignments sa ON sa.skill_id=sk.id AND sa.deployment_id=$1
JOIN users u ON u.id=$2
WHERE sk.enabled=true AND ((sa.subject_type='user' AND sa.subject_id=$2) OR (sa.subject_type='role' AND EXISTS (SELECT 1 FROM user_role_bindings urb JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true WHERE urb.deployment_id=$1 AND urb.user_id=u.id AND urb.role_id=sa.subject_id)) OR (sa.subject_type='team' AND EXISTS (SELECT 1 FROM user_team_bindings utb JOIN teams t ON t.deployment_id=utb.deployment_id AND t.id=utb.team_id AND t.enabled=true WHERE utb.deployment_id=$1 AND utb.user_id=u.id AND utb.team_id=sa.subject_id)))
), latest AS (SELECT DISTINCT ON (id) * FROM authorized ORDER BY id,version DESC)
SELECT id,name,version,sha256,size_bytes FROM latest ORDER BY id`, claims.DeploymentID, claims.Subject)
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
		items = append(items, map[string]any{"id": id, "name": name, "version": version, "enabled": true, "package": map[string]any{"url": "/aep/v1/user/skills/" + id + "/versions/" + version + "/package", "sha256": sha, "size": size}})
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
	err := s.app.Pool.QueryRow(request.Context(), `SELECT sv.object_key FROM skill_versions sv JOIN skill_assignments sa ON sa.skill_id=sv.skill_id JOIN users u ON u.id=$2 WHERE sv.skill_id=$3 AND sv.version=$4 AND sv.published=true AND sa.deployment_id=$1 AND ((sa.subject_type='user' AND sa.subject_id=$2) OR (sa.subject_type='role' AND EXISTS (SELECT 1 FROM user_role_bindings urb JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true WHERE urb.deployment_id=$1 AND urb.user_id=u.id AND urb.role_id=sa.subject_id)) OR (sa.subject_type='team' AND EXISTS (SELECT 1 FROM user_team_bindings utb JOIN teams t ON t.deployment_id=utb.deployment_id AND t.id=utb.team_id AND t.enabled=true WHERE utb.deployment_id=$1 AND utb.user_id=u.id AND utb.team_id=sa.subject_id))) LIMIT 1`, claims.DeploymentID, claims.Subject, skillID, version).Scan(&objectKey)
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
	if claims.SessionID == "" {
		writeProblem(response, request, http.StatusUnauthorized, "SESSION_REQUIRED", "A user session is required.")
		return
	}
	_, err = tx.Exec(request.Context(), "INSERT INTO skill_sync_results (id,deployment_id,user_id,session_id,revision,status,installed_skill_ids,payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)", uuid.NewString(), claims.DeploymentID, claims.Subject, claims.SessionID, input.Revision, input.Status, installed, payload)
	if err == nil {
		err = tx.Commit(request.Context())
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusAccepted)
}
