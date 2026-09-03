package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

type licenseImportRequest struct {
	License json.RawMessage `json:"license"`
}

type licenseRecord struct {
	LicenseID    string
	EnterpriseID string
	CustomerID   string
	DeploymentID string
	Digest       string
	KeyID        string
	Status       string
	IssuedAt     time.Time
	ExpiresAt    time.Time
	GraceEndsAt  time.Time
	UserLimit    int
	AgentLimit   int
	Features     []string
	Payload      []byte
	RevokedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ActiveAgents int
	ActiveUsers  int
}

func scanLicense(row interface{ Scan(...any) error }) (licenseRecord, error) {
	var value licenseRecord
	err := row.Scan(&value.LicenseID, &value.EnterpriseID, &value.CustomerID, &value.DeploymentID,
		&value.Digest, &value.KeyID, &value.Status, &value.IssuedAt, &value.ExpiresAt, &value.GraceEndsAt,
		&value.UserLimit, &value.AgentLimit, &value.Features, &value.Payload, &value.RevokedAt,
		&value.CreatedAt, &value.UpdatedAt, &value.ActiveAgents, &value.ActiveUsers)
	return value, err
}

const licenseColumns = `l.license_id,l.enterprise_id,l.customer_id,l.deployment_id,l.digest,l.key_id,l.status,
 l.issued_at,l.expires_at,l.grace_ends_at,l.user_limit,l.agent_limit,l.features,l.payload,l.revoked_at,
 l.created_at,l.updated_at,
 (SELECT count(*) FROM license_activations a WHERE a.license_id=l.license_id AND a.revoked_at IS NULL),
 (SELECT count(DISTINCT a.user_id) FROM license_activations a WHERE a.license_id=l.license_id AND a.revoked_at IS NULL)`

func licenseJSON(value licenseRecord, includePayload bool) map[string]any {
	result := map[string]any{
		"licenseId": value.LicenseID, "enterpriseId": value.EnterpriseID, "customerId": value.CustomerID,
		"deploymentId": value.DeploymentID, "digest": value.Digest, "keyId": value.KeyID, "status": value.Status,
		"issuedAt": value.IssuedAt, "expiresAt": value.ExpiresAt, "graceEndsAt": value.GraceEndsAt,
		"limits":   map[string]int{"users": value.UserLimit, "agents": value.AgentLimit},
		"features": value.Features, "activeAgents": value.ActiveAgents, "activeUsers": value.ActiveUsers,
		"revokedAt": value.RevokedAt, "createdAt": value.CreatedAt, "updatedAt": value.UpdatedAt,
	}
	if includePayload {
		var payload any
		if json.Unmarshal(value.Payload, &payload) == nil {
			result["payload"] = payload
		}
	}
	return result
}

func (s *Server) listLicenses(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	query := `SELECT ` + licenseColumns + ` FROM licenses l WHERE l.enterprise_id=$1`
	args := []any{claims.Tenant}
	if cursor := request.URL.Query().Get("cursor"); cursor != "" {
		query += ` AND l.license_id>$2`
		args = append(args, cursor)
	}
	query += ` ORDER BY l.license_id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit(request))
	rows, err := s.app.Pool.Query(request.Context(), query, args...)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		value, scanErr := scanLicense(rows)
		if scanErr != nil {
			databaseFailure(response, request, scanErr)
			return
		}
		items = append(items, licenseJSON(value, false))
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	var next any
	if len(items) == int(limit(request)) {
		next = items[len(items)-1]["licenseId"]
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (s *Server) getLicense(response http.ResponseWriter, request *http.Request) {
	value, err := s.findLicense(request, chi.URLParam(request, "licenseId"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The license was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, licenseJSON(value, true))
}

func (s *Server) findLicense(request *http.Request, id string) (licenseRecord, error) {
	return scanLicense(s.app.Pool.QueryRow(request.Context(), `SELECT `+licenseColumns+` FROM licenses l WHERE l.enterprise_id=$1 AND l.license_id=$2`, claimsFrom(request).Tenant, id))
}

func (s *Server) importLicense(response http.ResponseWriter, request *http.Request) {
	var input licenseImportRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if len(input.License) == 0 || s.app.LicenseVerifier == nil || s.app.Config.LicenseEnterpriseID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_LICENSE", "A complete signed license envelope is required.")
		return
	}
	verified, err := s.app.LicenseVerifier.Verify(input.License)
	if err != nil || verified.Status == "enterprise-expired" {
		writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_LICENSE", "The signed license could not be verified or is expired.")
		return
	}
	if expected := s.app.Config.LicenseCustomerID; expected != "" && verified.Claims.CustomerID != expected {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_CUSTOMER_MISMATCH", "The license is bound to another customer.")
		return
	}
	if err := s.app.RegisterLicense(request.Context(), verified); err != nil {
		if errors.Is(err, app.ErrLicenseConflict) {
			writeProblem(response, request, http.StatusConflict, "LICENSE_CONFLICT", "The license identifier is already registered with different contents.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	s.app.SetLicense(verified)
	if err := s.recordLicenseAudit(request, verified.Claims.LicenseID, "import", "success", nil); err != nil {
		databaseFailure(response, request, err)
		return
	}
	value, err := s.findLicense(request, verified.Claims.LicenseID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, licenseJSON(value, false))
}

func (s *Server) revokeLicense(response http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "licenseId")
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	var status string
	err = tx.QueryRow(request.Context(), `SELECT status FROM licenses WHERE enterprise_id=$1 AND license_id=$2 FOR UPDATE`, claimsFrom(request).Tenant, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The license was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if status == "active" {
		if _, err = tx.Exec(request.Context(), `UPDATE licenses SET status='revoked',revoked_at=now(),updated_at=now() WHERE enterprise_id=$1 AND license_id=$2`, claimsFrom(request).Tenant, id); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if _, err = tx.Exec(request.Context(), `UPDATE license_activations SET revoked_at=COALESCE(revoked_at,now()) WHERE enterprise_id=$1 AND license_id=$2`, claimsFrom(request).Tenant, id); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	if _, err = tx.Exec(request.Context(), `INSERT INTO license_audit_events (id,enterprise_id,license_id,actor_user_id,action,outcome) VALUES ($1,$2,$3,$4,'revoke','success')`, uuid.NewString(), claimsFrom(request).Tenant, id, claimsFrom(request).Subject); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err = tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"licenseId": id, "status": "revoked"})
}

func (s *Server) recordLicenseAudit(request *http.Request, licenseID, action, outcome string, reason *string) error {
	_, err := s.app.Pool.Exec(request.Context(), `INSERT INTO license_audit_events (id,enterprise_id,license_id,actor_user_id,action,outcome,reason) VALUES ($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), claimsFrom(request).Tenant, licenseID, claimsFrom(request).Subject, action, outcome, reason)
	return err
}
