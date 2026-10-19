package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAccessPublicationInput    = errors.New("invalid model access publication input")
	ErrAccessPublicationStale    = errors.New("model access source version is stale")
	ErrAccessPublicationConflict = errors.New("model access binding or source version conflicts")
	accessDigestPattern          = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// ModelAccessIntent is an internal, already-derived, credential-free snapshot.
// BindingKey must hash the full immutable subject/session or workload binding.
// The producer owns authorization/schema validation; this ledger never derives
// grants, accepts HTTP input, or reads credentials. SourceVersion fences replay.
type ModelAccessIntent struct {
	BindingKey    string
	SubjectID     string
	SourceVersion int64
	Snapshot      PublicationSnapshot
	IssuedAt      time.Time
	ValidUntil    time.Time
}

type ModelAccessPublication struct {
	DeploymentID  string
	BindingKey    string
	SubjectID     string
	OperationID   string
	Sequence      int64
	SourceVersion int64
	Revision      string
	ContentHash   string
	Payload       json.RawMessage
	IssuedAt      time.Time
	ValidUntil    time.Time
	Phase         string
	ErrorCode     *string
}

type ModelAccessDelivery struct {
	ModelAccessPublication
	ClaimToken string
}

func (s *DeploymentStore) validateAccessIntent(in ModelAccessIntent) bool {
	payload := bytes.TrimSpace(in.Snapshot.Payload)
	digest := sha256.Sum256(in.Snapshot.Payload)
	return accessDigestPattern.MatchString(in.BindingKey) && len(in.SubjectID) > 0 && len(in.SubjectID) <= 256 &&
		in.SourceVersion > 0 && len(in.Snapshot.Revision) > 0 && len(in.Snapshot.Revision) <= 200 &&
		(in.Snapshot.DeploymentID == "" || in.Snapshot.DeploymentID == s.deploymentID) &&
		len(payload) >= 2 && len(in.Snapshot.Payload) <= 1048576 && payload[0] == '{' && json.Valid(payload) &&
		in.Snapshot.ContentHash == hex.EncodeToString(digest[:]) &&
		!in.IssuedAt.IsZero() && in.ValidUntil.After(in.IssuedAt) && in.ValidUntil.Sub(in.IssuedAt) <= time.Hour
}

// QueueModelAccessPublication atomically captures immutable bytes, per-binding
// intent and its outbox. Called within WithTransaction it joins the producer's
// transaction, allowing signal acknowledgement only after durable capture.
// No Kubernetes/network operations may run inside that transaction.
func (s *DeploymentStore) QueueModelAccessPublication(ctx context.Context, in ModelAccessIntent) (ModelAccessPublication, error) {
	if !s.validateAccessIntent(in) {
		return ModelAccessPublication{}, ErrAccessPublicationInput
	}
	// PostgreSQL timestamps have microsecond precision. Normalize before equality
	// checks so an identical retry cannot mint a new publication through rounding.
	in.IssuedAt = in.IssuedAt.UTC().Truncate(time.Microsecond)
	in.ValidUntil = in.ValidUntil.UTC().Truncate(time.Microsecond)
	var value ModelAccessPublication
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		var timely bool
		if err := tx.db.Raw(`SELECT ?::timestamptz<=clock_timestamp() AND ?::timestamptz>clock_timestamp()`, in.IssuedAt, in.ValidUntil).Scan(&timely).Error; err != nil {
			return err
		}
		if !timely {
			return ErrAccessPublicationInput
		}
		if err := tx.db.Exec(`INSERT INTO model_access_streams(deployment_id,binding_key,subject_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.deploymentID, in.BindingKey, in.SubjectID).Error; err != nil {
			return err
		}
		var stream struct {
			SubjectID                       string
			LastSourceVersion, LastSequence int64
		}
		if err := tx.db.Raw(`SELECT subject_id,last_source_version,last_sequence FROM model_access_streams WHERE deployment_id=? AND binding_key=? FOR UPDATE`, s.deploymentID, in.BindingKey).Scan(&stream).Error; err != nil {
			return err
		}
		if stream.SubjectID != in.SubjectID {
			return ErrAccessPublicationConflict
		}
		if in.SourceVersion < stream.LastSourceVersion {
			return ErrAccessPublicationStale
		}
		if stream.LastSequence > 0 {
			result := tx.db.Raw(`SELECT * FROM model_access_publications WHERE deployment_id=? AND binding_key=? AND sequence=?`, s.deploymentID, in.BindingKey, stream.LastSequence).Scan(&value)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrNotFound
			}
			same := value.ContentHash == in.Snapshot.ContentHash && value.ValidUntil.Equal(in.ValidUntil)
			if in.SourceVersion == stream.LastSourceVersion && !same {
				return ErrAccessPublicationConflict
			}
			if same && (value.Phase != "failed" || in.SourceVersion == stream.LastSourceVersion) {
				// Confirm new source work without republishing unchanged policy. A
				// changed validity bound is real delivery work, not a cosmetic retry.
				return tx.db.Exec(`UPDATE model_access_streams SET last_source_version=? WHERE deployment_id=? AND binding_key=?`, in.SourceVersion, s.deploymentID, in.BindingKey).Error
			}
		}
		value = ModelAccessPublication{DeploymentID: s.deploymentID, BindingKey: in.BindingKey, SubjectID: in.SubjectID,
			OperationID: uuid.NewString(), Sequence: stream.LastSequence + 1, SourceVersion: in.SourceVersion,
			Revision: in.Snapshot.Revision, ContentHash: in.Snapshot.ContentHash, Payload: in.Snapshot.Payload,
			IssuedAt: in.IssuedAt, ValidUntil: in.ValidUntil, Phase: "queued"}
		if err := tx.db.Exec(`INSERT INTO model_access_publications(deployment_id,binding_key,subject_id,operation_id,sequence,source_version,revision,content_hash,payload,issued_at,valid_until)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.deploymentID, value.BindingKey, value.SubjectID, value.OperationID, value.Sequence, value.SourceVersion, value.Revision, value.ContentHash, []byte(value.Payload), value.IssuedAt, value.ValidUntil).Error; err != nil {
			return err
		}
		if err := tx.db.Exec(`INSERT INTO model_access_publication_outbox(deployment_id,operation_id) VALUES(?,?)`, s.deploymentID, value.OperationID).Error; err != nil {
			return err
		}
		if err := tx.db.Exec(`UPDATE model_access_streams SET last_source_version=?,last_sequence=? WHERE deployment_id=? AND binding_key=?`, in.SourceVersion, value.Sequence, s.deploymentID, in.BindingKey).Error; err != nil {
			return err
		}
		if err := tx.db.Exec(`UPDATE model_access_publications SET phase='superseded',error_code=NULL,updated_at=now() WHERE deployment_id=? AND binding_key=? AND sequence<? AND phase IN ('queued','projecting')`, s.deploymentID, value.BindingKey, value.Sequence).Error; err != nil {
			return err
		}
		return tx.db.Exec(`UPDATE model_access_publication_outbox o SET completed_at=now(),claim_token=NULL,lease_until=NULL FROM model_access_publications p
WHERE o.deployment_id=p.deployment_id AND o.operation_id=p.operation_id AND p.deployment_id=? AND p.binding_key=? AND p.phase='superseded' AND o.completed_at IS NULL`, s.deploymentID, value.BindingKey).Error
	})
	if err != nil {
		return ModelAccessPublication{}, err
	}
	return value, nil
}

func (s *DeploymentStore) LatestModelAccessPublication(ctx context.Context, bindingKey string) (ModelAccessPublication, error) {
	var value ModelAccessPublication
	result := s.db.WithContext(ctx).Raw(`SELECT p.* FROM model_access_publications p JOIN model_access_streams s ON s.deployment_id=p.deployment_id AND s.binding_key=p.binding_key AND s.last_sequence=p.sequence WHERE p.deployment_id=? AND p.binding_key=?`, s.deploymentID, bindingKey).Scan(&value)
	if result.Error != nil {
		return ModelAccessPublication{}, result.Error
	}
	if result.RowsAffected == 0 {
		return ModelAccessPublication{}, ErrNotFound
	}
	return value, nil
}
