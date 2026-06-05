package model

import (
	"fmt"
	"strings"
)

// SkillRef is a parsed skill reference that may or may not be namespace-qualified.
//
// Examples:
//
//	"@alice/my-skill"  → SkillRef{Namespace: "alice", Slug: "my-skill"}
//	"my-skill"         → SkillRef{Namespace: "",      Slug: "my-skill"}
type SkillRef struct {
	Namespace string // empty = bare slug, resolved at runtime
	Slug      string
}

// ParseSkillRef parses a raw string into a SkillRef.
//
//   - "@namespace/slug" → qualified ref
//   - "slug"            → bare ref (resolved at runtime via disambiguation)
func ParseSkillRef(raw string) SkillRef {
	if strings.HasPrefix(raw, "@") {
		trimmed := raw[1:]
		if idx := strings.IndexByte(trimmed, '/'); idx > 0 && idx < len(trimmed)-1 {
			return SkillRef{
				Namespace: trimmed[:idx],
				Slug:      trimmed[idx+1:],
			}
		}
	}
	return SkillRef{Slug: raw}
}

// IsQualified returns true if the ref includes a namespace.
func (r SkillRef) IsQualified() bool {
	return r.Namespace != ""
}

// String returns the canonical string form: "@namespace/slug" or just "slug".
func (r SkillRef) String() string {
	if r.Namespace != "" {
		return fmt.Sprintf("@%s/%s", r.Namespace, r.Slug)
	}
	return r.Slug
}
