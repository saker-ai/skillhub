package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/google/uuid"
)

func TestExtractFrontmatter_WithFrontmatter(t *testing.T) {
	content := []byte("---\ntitle: My Skill\nauthor: test\n---\n# Hello World")
	result := extractFrontmatter(content)

	var parsed map[string]string
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, ok := parsed["raw"]
	if !ok {
		t.Fatal("expected 'raw' key in parsed result")
	}
	if raw == "" {
		t.Error("expected non-empty raw frontmatter")
	}
	if len(raw) == 0 {
		t.Error("frontmatter should contain content")
	}
}

func TestExtractFrontmatter_NoFrontmatter(t *testing.T) {
	content := []byte("# Just markdown\nNo frontmatter here")
	result := extractFrontmatter(content)

	if string(result) != "{}" {
		t.Errorf("got %s, want {}", string(result))
	}
}

func TestExtractFrontmatter_OnlyOpeningDelimiter(t *testing.T) {
	content := []byte("---\nsome content without closing")
	result := extractFrontmatter(content)

	if string(result) != "{}" {
		t.Errorf("got %s, want {}", string(result))
	}
}

func TestExtractFrontmatter_Empty(t *testing.T) {
	result := extractFrontmatter([]byte(""))
	if string(result) != "{}" {
		t.Errorf("got %s, want {}", string(result))
	}
}

func TestDerefStr(t *testing.T) {
	s := "hello"
	if got := derefStr(&s); got != "hello" {
		t.Errorf("derefStr(&%q) = %q", s, got)
	}
	if got := derefStr(nil); got != "" {
		t.Errorf("derefStr(nil) = %q, want empty", got)
	}
}

// TestAuthorizeSkillWrite 覆盖 authorizeSkillWrite 的全部判定分支：
// personal token (tokenNS == nil) 走 owner 检查；team token (tokenNS != nil)
// 跳过 owner 检查但要求 namespace 一致。
func TestAuthorizeSkillWrite(t *testing.T) {
	t.Parallel()

	owner := uuid.New()
	other := uuid.New()
	nsA := uuid.New()
	nsB := uuid.New()

	regular := &model.User{ID: owner, Role: "user"}
	notOwner := &model.User{ID: other, Role: "user"}
	admin := &model.User{ID: other, Role: "admin"}

	cases := []struct {
		name    string
		skillNS *uuid.UUID
		ownerID uuid.UUID
		actor   *model.User
		tokenNS *uuid.UUID
		wantErr bool
	}{
		// === personal token (tokenNS == nil) ===
		{"personal: owner OK", nil, owner, regular, nil, false},
		{"personal: non-owner forbidden", nil, owner, notOwner, nil, true},
		{"personal: system admin OK", nil, owner, admin, nil, false},

		// === team token (tokenNS != nil) ===
		{"team: same namespace OK regardless of owner", &nsA, owner, notOwner, &nsA, false},
		{"team: namespace mismatch forbidden", &nsB, owner, notOwner, &nsA, true},
		{"team: skill has no namespace forbidden", nil, owner, notOwner, &nsA, true},
	}

	s := &SkillService{}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := s.authorizeSkillWrite(context.Background(), tc.skillNS, tc.ownerID, tc.actor, tc.tokenNS)
			if tc.wantErr && err == nil {
				t.Fatalf("authorizeSkillWrite: want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("authorizeSkillWrite: unexpected error %v", err)
			}
		})
	}
}
