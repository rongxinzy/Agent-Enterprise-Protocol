package repository

import (
	"context"
	"errors"
	"testing"
)

// An assignment page is positioned by (created_at, id); a cursor this store did
// not issue cannot be placed in the list, so it must be refused before any
// query runs. No expectation is queued for these calls: a query here would fail
// the mock, which is what proves the refusal happens first.
func TestAssignmentPageRejectsUnusableCursors(t *testing.T) {
	store, _ := newMockStore(t)
	deployment := store.Deployment("deployment-a")
	for _, test := range []struct {
		name   string
		cursor string
		list   func(cursor string) error
	}{
		{"a cursor without a separator", "not-a-cursor", func(cursor string) error {
			_, err := deployment.ListSkillAssignmentsPage(context.Background(), cursor, 1)
			return err
		}},
		{"a cursor without an id", "2026-10-14T08:00:00Z|", func(cursor string) error {
			_, err := deployment.ListCredentialAssignmentsPage(context.Background(), cursor, 1)
			return err
		}},
		{"a cursor with an unreadable time", "not-a-time|assignment-a", func(cursor string) error {
			_, err := deployment.ListModelAssignmentsPage(context.Background(), cursor, 1)
			return err
		}},
	} {
		if err := test.list(test.cursor); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("%s: error = %v, want ErrInvalidCursor", test.name, err)
		}
	}
}
