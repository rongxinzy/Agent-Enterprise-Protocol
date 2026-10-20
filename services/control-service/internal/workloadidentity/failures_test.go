package workloadidentity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type failedRegistry struct {
	*recordedRegistry
	operation string
}

func (r failedRegistry) Registrations(ctx context.Context, ns, name string) ([]Registration, error) {
	if r.operation == "lookup" {
		return nil, errors.New("private DB detail")
	}
	return r.recordedRegistry.Registrations(ctx, ns, name)
}
func (r failedRegistry) Register(ctx context.Context, agent, expected string, w repository.ObservedModelWorkload, validFor time.Duration) (string, error) {
	if r.operation == "register" {
		return "", errors.New("private DB detail")
	}
	return r.recordedRegistry.Register(ctx, agent, expected, w, validFor)
}
func (r failedRegistry) Retire(context.Context, string, string) error {
	return errors.New("private DB detail")
}

func TestObserverRegistryFailuresAreSanitizedAndNeverRenew(t *testing.T) {
	for _, operation := range []string{"lookup", "register", "replace principal", "retire deleted", "ambiguous"} {
		t.Run(operation, func(t *testing.T) {
			o, r := observerFixture(t, employeeFixture(), accountFixture())
			r.current = []Registration{{AgentID: "agent-1", BindingKey: "old-binding"}}
			if operation == "replace principal" {
				r.current[0].AgentID = "old-agent"
			}
			if operation == "retire deleted" {
				o, _ = observerFixture(t, nil, nil)
			}
			if operation == "ambiguous" {
				r.current = append(r.current, Registration{AgentID: "agent-1", BindingKey: "another-binding"})
			}
			o.registry = failedRegistry{recordedRegistry: r, operation: operation}
			if _, err := o.Reconcile(t.Context(), request()); err == nil || strings.Contains(err.Error(), "private DB") || len(r.registered) != 0 {
				t.Fatal("registry failure leaked details or renewed identity", err)
			}
		})
	}
}

type failedReader struct{ client.Reader }

func (failedReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("private API detail")
}

func TestObserverEmployeeReadFailureCannotRenewOrRetire(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	o.reader = failedReader{Reader: o.reader}
	if _, err := o.Reconcile(t.Context(), request()); err == nil || strings.Contains(err.Error(), "private API") || len(r.registered) != 0 || len(r.retired) != 0 {
		t.Fatal("API failure changed identity or leaked details", err)
	}
}

func TestObserverRequiresBoundedExplicitConfiguration(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	for _, values := range []struct {
		deployment, namespace, audience string
		freshness                       time.Duration
	}{
		{"", "employees", "audience", time.Minute}, {"deployment", "", "audience", time.Minute},
		{"deployment", "employees", "", time.Minute}, {"deployment", "employees", "audience", time.Second},
		{"deployment", "employees", "audience", 2 * time.Hour}, {"deployment", "employees", "audience", 2500 * time.Millisecond},
	} {
		if _, err := New(o.reader, r, values.deployment, values.namespace, values.audience, values.freshness); err == nil {
			t.Fatal("invalid observer configuration accepted")
		}
	}
	if _, err := New(nil, r, "deployment", "employees", "audience", time.Minute); err == nil {
		t.Fatal("missing live API reader accepted")
	}
}
