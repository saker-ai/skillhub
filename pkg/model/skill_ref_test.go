package model

import "testing"

func TestParseSkillRef(t *testing.T) {
	tests := []struct {
		input    string
		wantNS   string
		wantSlug string
		wantQual bool
		wantStr  string
	}{
		{"my-skill", "", "my-skill", false, "my-skill"},
		{"@alice/my-skill", "alice", "my-skill", true, "@alice/my-skill"},
		{"@team-x/lint-rules", "team-x", "lint-rules", true, "@team-x/lint-rules"},
		{"@ns/a", "ns", "a", true, "@ns/a"},
		// Edge cases: malformed refs treated as bare slugs
		{"@/slug", "", "@/slug", false, "@/slug"}, // empty namespace
		{"@ns/", "", "@ns/", false, "@ns/"},       // empty slug
		{"@ns", "", "@ns", false, "@ns"},          // no slash
		{"", "", "", false, ""},                   // empty
		{"plain-slug", "", "plain-slug", false, "plain-slug"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ref := ParseSkillRef(tt.input)
			if ref.Namespace != tt.wantNS {
				t.Errorf("Namespace = %q, want %q", ref.Namespace, tt.wantNS)
			}
			if ref.Slug != tt.wantSlug {
				t.Errorf("Slug = %q, want %q", ref.Slug, tt.wantSlug)
			}
			if ref.IsQualified() != tt.wantQual {
				t.Errorf("IsQualified() = %v, want %v", ref.IsQualified(), tt.wantQual)
			}
			if ref.String() != tt.wantStr {
				t.Errorf("String() = %q, want %q", ref.String(), tt.wantStr)
			}
		})
	}
}
