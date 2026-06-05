package search

import (
	"context"
	"testing"

	"github.com/saker-ai/skillhub/pkg/config"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(config.SearchConfig{IndexPath: t.TempDir() + "/skills.bleve"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func publicSearchFilters(docType string) []Filter {
	filters := []Filter{
		{Field: "visibility", Value: "public"},
		{Field: "moderationStatus", Value: "approved"},
		{Field: "isDeleted", Value: false},
	}
	if docType != "" {
		filters = append(filters, Filter{Field: "docType", Value: docType})
	}
	return filters
}

func TestClientSkillSearchFiltersSortAndDelete(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	docs := []*SkillDocument{
		{
			ID:               "private",
			Slug:             "private-browser",
			Summary:          "browser automation private",
			OwnerHandle:      "alice",
			Visibility:       "private",
			ModerationStatus: "approved",
			Downloads:        100,
			CreatedAt:        1,
			UpdatedAt:        1,
		},
		{
			ID:               "deleted",
			Slug:             "deleted-browser",
			Summary:          "browser automation deleted",
			OwnerHandle:      "alice",
			Visibility:       "public",
			ModerationStatus: "approved",
			IsDeleted:        true,
			Downloads:        50,
			CreatedAt:        2,
			UpdatedAt:        2,
		},
		{
			ID:               "low",
			Slug:             "low-browser",
			Summary:          "browser automation low",
			OwnerHandle:      "bob",
			Visibility:       "public",
			ModerationStatus: "approved",
			Downloads:        1,
			CreatedAt:        3,
			UpdatedAt:        3,
		},
		{
			ID:               "high",
			Slug:             "high-browser",
			Summary:          "browser automation high",
			OwnerHandle:      "carol",
			Visibility:       "public",
			ModerationStatus: "approved",
			Downloads:        9,
			CreatedAt:        4,
			UpdatedAt:        4,
		},
	}
	for _, doc := range docs {
		if err := c.IndexSkill(ctx, doc); err != nil {
			t.Fatalf("IndexSkill(%s): %v", doc.ID, err)
		}
	}

	result, err := c.Search(ctx, "browser", 10, 0, []string{"downloads:desc"}, publicSearchFilters(""))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.EstimatedTotal != 2 {
		t.Fatalf("EstimatedTotal = %d, want 2; hits=%v", result.EstimatedTotal, result.Hits)
	}
	if got := result.Hits[0]["slug"]; got != "high-browser" {
		t.Fatalf("first slug = %v, want high-browser", got)
	}
	if got := result.Hits[0]["ownerHandleExact"]; got != "carol" {
		t.Fatalf("ownerHandleExact = %v, want carol", got)
	}

	if err := c.DeleteSkill(ctx, "high"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	result, err = c.Search(ctx, "browser", 10, 0, nil, publicSearchFilters(""))
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if result.EstimatedTotal != 1 || result.Hits[0]["slug"] != "low-browser" {
		t.Fatalf("after delete hits = total %d %#v, want only low-browser", result.EstimatedTotal, result.Hits)
	}
}

func TestClientPluginSearchAndDelete(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.IndexPlugin(ctx, &PluginDocument{
		ID:               "plug",
		Slug:             "demo-plugin",
		Summary:          "plugin browser integration",
		OwnerHandle:      "dana",
		Visibility:       "public",
		ModerationStatus: "approved",
		Downloads:        3,
		CreatedAt:        1,
		UpdatedAt:        1,
	}); err != nil {
		t.Fatalf("IndexPlugin: %v", err)
	}
	if err := c.IndexSkill(ctx, &SkillDocument{
		ID:               "skill",
		Slug:             "demo-skill",
		Summary:          "plugin browser skill",
		OwnerHandle:      "erin",
		Visibility:       "public",
		ModerationStatus: "approved",
		CreatedAt:        2,
		UpdatedAt:        2,
	}); err != nil {
		t.Fatalf("IndexSkill: %v", err)
	}

	result, err := c.Search(ctx, "plugin browser", 10, 0, nil, publicSearchFilters("plugin"))
	if err != nil {
		t.Fatalf("Search plugin: %v", err)
	}
	if result.EstimatedTotal != 1 {
		t.Fatalf("EstimatedTotal = %d, want 1; hits=%v", result.EstimatedTotal, result.Hits)
	}
	if got := result.Hits[0]["slug"]; got != "demo-plugin" {
		t.Fatalf("hit slug = %v, want demo-plugin", got)
	}
	if got := result.Hits[0]["docType"]; got != "plugin" {
		t.Fatalf("docType = %v, want plugin", got)
	}

	if err := c.DeletePlugin(ctx, "plug"); err != nil {
		t.Fatalf("DeletePlugin: %v", err)
	}
	result, err = c.Search(ctx, "plugin browser", 10, 0, nil, publicSearchFilters("plugin"))
	if err != nil {
		t.Fatalf("Search plugin after delete: %v", err)
	}
	if result.EstimatedTotal != 0 {
		t.Fatalf("EstimatedTotal after delete = %d, want 0; hits=%v", result.EstimatedTotal, result.Hits)
	}
}
