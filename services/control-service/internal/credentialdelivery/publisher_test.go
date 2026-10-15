package credentialdelivery

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

type memorySource struct {
	ids     []string
	values  map[string]credential.Envelope
	err     error
	readErr error
}

func (s *memorySource) IDs(_ context.Context, cursor string, limit int) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := []string{}
	for _, id := range s.ids {
		if id > cursor && len(out) < limit {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *memorySource) Read(_ context.Context, id string) (credential.Envelope, bool, error) {
	value, ok := s.values[id]
	return value, ok, s.readErr
}

func testSealer(t *testing.T) *credential.Sealer {
	t.Helper()
	provider, _, err := credential.NewProvider(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	return credential.NewSealer(provider)
}

func seal(t *testing.T, sealer *credential.Sealer, id, value string) credential.Envelope {
	t.Helper()
	sealed, err := sealer.Seal(context.Background(), []byte(value), credential.AssociatedData("deployment-a", id))
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func newTestPublisher(t *testing.T, source *memorySource, client *fake.Clientset) *Publisher {
	t.Helper()
	p, err := New(source, testSealer(t), client.CoreV1().Secrets(Namespace), "deployment-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLifecycleAndRestart(t *testing.T) {
	ctx := context.Background()
	sealer := testSealer(t)
	source := &memorySource{ids: []string{"credential-a"}, values: map[string]credential.Envelope{"credential-a": seal(t, sealer, "credential-a", "disposable-first")}}
	client := fake.NewClientset()
	p := newTestPublisher(t, source, client)
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	get := func() *corev1.Secret {
		value, err := client.CoreV1().Secrets(Namespace).Get(ctx, SecretPrefix+"credential-a", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if value := get(); string(value.Data[SecretKey]) != "disposable-first" || !p.owns(value, "credential-a") {
		t.Fatal("credential was not delivered with ownership")
	}
	client.ClearActions()
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" || action.GetVerb() == "create" {
			t.Fatal("unchanged credential caused a write")
		}
	}
	source.values["credential-a"] = seal(t, sealer, "credential-a", "disposable-rotated")
	// A fresh worker reconstructs state without an in-memory event or cursor.
	p = newTestPublisher(t, source, client)
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if string(get().Data[SecretKey]) != "disposable-rotated" {
		t.Fatal("rotation was not delivered")
	}
	if err := client.CoreV1().Secrets(Namespace).Delete(ctx, SecretPrefix+"credential-a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if string(get().Data[SecretKey]) != "disposable-rotated" {
		t.Fatal("deleted Secret was not recreated")
	}
	// A missing/disabled/unreferenced Credential disappears from the source;
	// owned inventory retains enough identity to revoke it after a restart.
	source.ids = nil
	delete(source.values, "credential-a")
	if err := newTestPublisher(t, source, client).Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Secrets(Namespace).Get(ctx, SecretPrefix+"credential-a", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("withdrawn credential still exists")
	}
}

func TestForeignOwnersAreNeverAdoptedOrDeleted(t *testing.T) {
	for _, kind := range []string{"unmanaged", "eso", "other-deployment", "other-id", "terminating"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			client := fake.NewClientset()
			source := &memorySource{ids: []string{"credential-a"}, values: map[string]credential.Envelope{"credential-a": seal(t, testSealer(t), "credential-a", "disposable-key")}}
			p := newTestPublisher(t, source, client)
			if err := p.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			value, _ := client.CoreV1().Secrets(Namespace).Get(ctx, SecretPrefix+"credential-a", metav1.GetOptions{})
			switch kind {
			case "unmanaged":
				value.Labels = nil
			case "eso":
				value.OwnerReferences = []metav1.OwnerReference{{APIVersion: "external-secrets.io/v1", Kind: "ExternalSecret", Name: "external", UID: "foreign"}}
			case "other-deployment":
				value.Annotations[deploymentAnnotation] = "deployment-b"
			case "other-id":
				value.Annotations[credentialAnnotation] = "credential-b"
			case "terminating":
				now := metav1.Now()
				value.DeletionTimestamp = &now
			}
			if _, err := client.CoreV1().Secrets(Namespace).Update(ctx, value, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.ClearActions()
			if err := p.Sync(ctx); err != ErrOwnership {
				t.Fatalf("expected ownership conflict, got %v", err)
			}
			source.values = nil
			if err := p.Sync(ctx); err != ErrOwnership {
				t.Fatalf("expected deletion ownership conflict, got %v", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "update" || action.GetVerb() == "delete" {
					t.Fatal("foreign Secret was modified")
				}
			}
		})
	}
}

func TestSourceFailuresNeverWithdrawAndErrorsDoNotLeak(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	source := &memorySource{ids: []string{"credential-a"}, values: map[string]credential.Envelope{"credential-a": seal(t, testSealer(t), "credential-a", "disposable-key")}}
	p := newTestPublisher(t, source, client)
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"list", "read", "decrypt", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			source.err, source.readErr = nil, nil
			source.values["credential-a"] = seal(t, testSealer(t), "credential-a", "disposable-key")
			sensitive := errors.New("sensitive-test-error-should-not-escape")
			switch mode {
			case "list":
				source.err = sensitive
			case "read":
				source.readErr = sensitive
			case "decrypt":
				source.values["credential-a"] = credential.Envelope{KeyID: "wrong"}
			case "kubernetes":
				client.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, sensitive })
			}
			client.ClearActions()
			p.ready.Store(true)
			err := p.Sync(ctx)
			if err == nil || strings.Contains(err.Error(), sensitive.Error()) {
				t.Fatal("failure was hidden or leaked underlying error")
			}
			if p.Ready() {
				t.Fatal("failed synchronization retained stale readiness")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "delete" {
					t.Fatal("source failure deleted a Secret")
				}
			}
		})
	}
}

func TestConflictsRetryWithoutForceOrDeletingReplacement(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	source := &memorySource{ids: []string{"credential-a"}, values: map[string]credential.Envelope{"credential-a": seal(t, testSealer(t), "credential-a", "disposable-key")}}
	p := newTestPublisher(t, source, client)
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	source.values["credential-a"] = seal(t, testSealer(t), "credential-a", "disposable-rotated")
	conflict := true
	client.PrependReactor("update", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		if conflict {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "redacted", errors.New("redacted"))
		}
		return false, nil, nil
	})
	if err := p.Sync(ctx); err != ErrTarget {
		t.Fatalf("expected retryable conflict, got %v", err)
	}
	conflict = false
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	source.ids, source.values = nil, nil
	client.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(ktesting.DeleteAction).GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil {
			t.Fatal("delete lacks identity preconditions")
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "redacted", errors.New("replacement"))
	})
	if err := p.Sync(ctx); err != ErrTarget {
		t.Fatalf("expected safe deletion conflict, got %v", err)
	}
}

func TestWorkerHealthAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newTestPublisher(t, &memorySource{}, fake.NewClientset())
	if p.StatusError() == nil || !p.NeedLeaderElection() {
		t.Fatal("worker must start unready and require leadership")
	}
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()
	deadline := time.After(3 * time.Second)
	for !p.Ready() {
		select {
		case <-deadline:
			t.Fatal("worker did not become ready")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if p.StatusError() != nil {
		t.Fatal("successful synchronization is not ready")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.Ready() {
		t.Fatal("stopped worker remained ready")
	}
}

func TestConfigurationAndInvalidIdentity(t *testing.T) {
	if _, err := New(nil, nil, nil, "", 0); err == nil {
		t.Fatal("invalid dependencies accepted")
	}
	source := &memorySource{ids: []string{"../escape"}}
	client := fake.NewClientset()
	p := newTestPublisher(t, source, client)
	if err := p.Sync(context.Background()); err != ErrIdentity {
		t.Fatalf("invalid name: %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "list" {
			t.Fatal("invalid name reached Secret operations")
		}
	}
}
