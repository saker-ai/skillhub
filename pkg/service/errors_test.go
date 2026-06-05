package service

import (
	"errors"
	"strings"
	"testing"
)

func TestAmbiguousSlugError_Is(t *testing.T) {
	err := &AmbiguousSlugError{
		Slug: "my-skill",
		Candidates: []AmbiguousCandidate{
			{Namespace: "alice", Slug: "my-skill", OwnerHandle: "alice", SkillID: "1"},
			{Namespace: "bob", Slug: "my-skill", OwnerHandle: "bob", SkillID: "2"},
		},
	}

	if !errors.Is(err, ErrConflict) {
		t.Error("AmbiguousSlugError should match ErrConflict")
	}
	if errors.Is(err, ErrForbidden) {
		t.Error("AmbiguousSlugError should not match ErrForbidden")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("AmbiguousSlugError should not match ErrNotFound")
	}
}

func TestAmbiguousSlugError_Error(t *testing.T) {
	err := &AmbiguousSlugError{Slug: "demo"}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should return non-empty string")
	}
	if !strings.Contains(msg, "demo") {
		t.Errorf("Error() = %q, want it to contain 'demo'", msg)
	}
}
