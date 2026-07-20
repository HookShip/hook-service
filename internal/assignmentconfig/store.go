package assignmentconfig

import (
	"context"
	"fmt"

	"github.com/hookship/hook-service/internal/runtime"
)

// Store is an immutable, in-memory assignment/credential source shared by every
// native profile. It resolves a credential to its assignment and lists
// assignments for worker fairness iteration. It never enters the delivery hot
// path beyond an O(1) map lookup.
type Store struct {
	byCredential map[string]runtime.Assignment
	ordered      []runtime.Assignment
}

// NewStore builds a store from resolved assignments.
func NewStore(assignments []runtime.Assignment) (*Store, error) {
	if len(assignments) == 0 {
		return nil, fmt.Errorf("at least one assignment is required")
	}
	store := &Store{byCredential: make(map[string]runtime.Assignment, len(assignments))}
	for _, assignment := range assignments {
		if _, ok := store.byCredential[assignment.CredentialID]; ok {
			return nil, fmt.Errorf("duplicate credential ID %q", assignment.CredentialID)
		}
		store.byCredential[assignment.CredentialID] = assignment
		store.ordered = append(store.ordered, assignment)
	}
	return store, nil
}

// ResolveCredential returns the assignment bound to a credential ID.
func (s *Store) ResolveCredential(_ context.Context, credentialID string) (runtime.Assignment, error) {
	assignment, ok := s.byCredential[credentialID]
	if !ok {
		return runtime.Assignment{}, runtime.ErrNotFound
	}
	return assignment, nil
}

// Assignments returns every configured assignment in declaration order.
func (s *Store) Assignments(_ context.Context) ([]runtime.Assignment, error) {
	out := make([]runtime.Assignment, len(s.ordered))
	copy(out, s.ordered)
	return out, nil
}

// WithSnapshotIDs returns a copy of the store with each assignment's
// EndpointSnapshotID set from resolver, used after a backend registers
// immutable snapshots.
func (s *Store) WithSnapshotIDs(resolve func(runtime.Assignment) string) *Store {
	clone := &Store{byCredential: make(map[string]runtime.Assignment, len(s.ordered))}
	for _, assignment := range s.ordered {
		assignment.EndpointSnapshotID = resolve(assignment)
		clone.byCredential[assignment.CredentialID] = assignment
		clone.ordered = append(clone.ordered, assignment)
	}
	return clone
}
