// Package credentialdelivery supplies model provider Secrets from AEP's sealed
// Credential store. It neither publishes routes nor renders Higress resources.
package credentialdelivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

const (
	Namespace            = "aep-credentials"
	SecretPrefix         = "aep-credential-"
	SecretKey            = "api-key"
	manager              = "aep-credential-publisher"
	managerLabel         = "app.kubernetes.io/managed-by"
	deploymentLabel      = "credentials.aep.zhiyuan.io/deployment"
	deploymentAnnotation = "credentials.aep.zhiyuan.io/deployment-id"
	credentialAnnotation = "credentials.aep.zhiyuan.io/credential-id"
	pageSize             = 100
	operationTimeout     = 10 * time.Second
)

// All errors are deliberately static: Kubernetes errors can echo Secret data,
// database errors can contain parameters, and a key provider can expose paths.
var (
	ErrSource    = errors.New("credential source unavailable")
	ErrDecrypt   = errors.New("credential decryption failed")
	ErrTarget    = errors.New("credential Secret operation failed")
	ErrOwnership = errors.New("credential Secret is managed by another owner")
	ErrIdentity  = errors.New("credential identity is invalid")
)

type Source interface {
	// IDs returns a deployment-scoped, ordered page of currently eligible IDs.
	IDs(context.Context, string, int) ([]string, error)
	// Read rechecks current eligibility. found=false withdraws only owned Secrets.
	Read(context.Context, string) (envelope credential.Envelope, found bool, err error)
}

type Opener interface {
	Open(context.Context, credential.Envelope, []byte) ([]byte, error)
}

type Publisher struct {
	source         Source
	opener         Opener
	secrets        v1.SecretInterface
	deployment     string
	selector       string
	deploymentHash string
	interval       time.Duration
	ready          atomic.Bool
}

func New(source Source, opener Opener, secrets v1.SecretInterface, deployment string, interval time.Duration) (*Publisher, error) {
	if source == nil || opener == nil || secrets == nil || deployment == "" || interval < time.Second || interval > time.Hour {
		return nil, errors.New("invalid credential publisher configuration")
	}
	digest := sha256.Sum256([]byte(deployment))
	hash := hex.EncodeToString(digest[:16])
	return &Publisher{source: source, opener: opener, secrets: secrets, deployment: deployment,
		deploymentHash: hash, selector: managerLabel + "=" + manager + "," + deploymentLabel + "=" + hash, interval: interval}, nil
}

// NeedLeaderElection makes controller-runtime start and drain this worker under
// its standard leader lifecycle. Followers do not decrypt or write credentials.
func (*Publisher) NeedLeaderElection() bool { return true }

func (p *Publisher) Ready() bool { return p.ready.Load() }

// Start retries from authoritative current state. No plaintext or transient
// event payload is persisted. A full pass also repairs deletion and drift after
// restart, without relying on notifications having been delivered.
func (p *Publisher) Start(ctx context.Context) error {
	defer p.ready.Store(false)
	for {
		err := p.Sync(ctx)
		p.ready.Store(err == nil)
		if err != nil && ctx.Err() == nil {
			slog.Error("credential synchronization failed", "reason", err.Error())
		}
		timer := time.NewTimer(p.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// Sync uses bounded pages and an independent current-state lookup per object.
// A failed item does not prevent unrelated rotations or revocations. Source
// failures are never interpreted as deletions.
func (p *Publisher) Sync(ctx context.Context) error {
	var result error
	cursor := ""
	for {
		readCtx, cancel := context.WithTimeout(ctx, operationTimeout)
		ids, err := p.source.IDs(readCtx, cursor, pageSize)
		cancel()
		if err != nil {
			p.ready.Store(false)
			return ErrSource
		}
		for _, id := range ids {
			if id <= cursor {
				p.ready.Store(false)
				return ErrIdentity
			}
			cursor = id
			if err := p.syncOne(ctx, id); err != nil {
				p.ready.Store(false)
				result = err
			}
		}
		if len(ids) < pageSize {
			break
		}
	}
	// List only our own inventory. Re-read each ID rather than deleting based on
	// an earlier page snapshot, which could race with a model being re-enabled.
	continuation := ""
	for {
		listCtx, cancel := context.WithTimeout(ctx, operationTimeout)
		items, err := p.secrets.List(listCtx, metav1.ListOptions{LabelSelector: p.selector, Limit: pageSize, Continue: continuation})
		cancel()
		if err != nil {
			p.ready.Store(false)
			return ErrTarget
		}
		for i := range items.Items {
			item := &items.Items[i]
			id := item.Annotations[credentialAnnotation]
			if !p.owns(item, id) {
				p.ready.Store(false)
				result = ErrOwnership
				continue
			}
			if err := p.syncOne(ctx, id); err != nil {
				p.ready.Store(false)
				result = err
			}
		}
		continuation = items.Continue
		if continuation == "" {
			break
		}
	}
	return result
}

func (p *Publisher) owns(secret *corev1.Secret, id string) bool {
	return id != "" && secret.Name == SecretPrefix+id && secret.Namespace == Namespace &&
		secret.Labels[managerLabel] == manager && secret.Labels[deploymentLabel] == p.deploymentHash &&
		secret.Annotations[deploymentAnnotation] == p.deployment && secret.Annotations[credentialAnnotation] == id &&
		len(secret.OwnerReferences) == 0 && secret.DeletionTimestamp == nil
}

func (p *Publisher) syncOne(parent context.Context, id string) error {
	if len(validation.IsDNS1123Subdomain(SecretPrefix+id)) != 0 {
		return ErrIdentity
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	envelope, found, err := p.source.Read(ctx, id)
	if err != nil {
		return ErrSource
	}
	current, err := p.secrets.Get(ctx, SecretPrefix+id, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return ErrTarget
	}
	exists := err == nil
	if exists && !p.owns(current, id) {
		return ErrOwnership
	}
	if !found {
		if !exists {
			return nil
		}
		// UID and resourceVersion prevent deleting a concurrent replacement or
		// metadata/ownership change. Conflicts retry on the next pass.
		uid, version := current.UID, current.ResourceVersion
		if err := p.secrets.Delete(ctx, current.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil && !apierrors.IsNotFound(err) {
			return ErrTarget
		}
		return nil
	}
	plaintext, err := p.opener.Open(ctx, envelope, credential.AssociatedData(p.deployment, id))
	if err != nil {
		return ErrDecrypt
	}
	defer clear(plaintext)
	if len(plaintext) == 0 {
		return ErrDecrypt
	}
	if !exists {
		_, err = p.secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: SecretPrefix + id, Namespace: Namespace,
			Labels:      map[string]string{managerLabel: manager, deploymentLabel: p.deploymentHash},
			Annotations: map[string]string{deploymentAnnotation: p.deployment, credentialAnnotation: id},
		}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{SecretKey: plaintext}}, metav1.CreateOptions{FieldManager: manager})
	} else if current.Type != corev1.SecretTypeOpaque || len(current.Data) != 1 || !bytes.Equal(current.Data[SecretKey], plaintext) {
		// Update carries resourceVersion. Never force apply or delete/recreate an
		// immutable or foreign Secret in order to make a rotation appear to work.
		current.Type = corev1.SecretTypeOpaque
		current.Data = map[string][]byte{SecretKey: plaintext}
		_, err = p.secrets.Update(ctx, current, metav1.UpdateOptions{FieldManager: manager})
	}
	if err != nil {
		return ErrTarget
	}
	return nil
}

// StatusError is suitable for the readiness endpoint; it contains no IDs,
// configuration values, ciphertext, or upstream error text.
func (p *Publisher) StatusError() error {
	if !p.Ready() {
		return errors.New("credential publisher has not completed a successful synchronization")
	}
	return nil
}
