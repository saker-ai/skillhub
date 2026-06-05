package repository

import (
	"testing"
	"time"

	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/google/uuid"
)

func newTestSkill(slug string) *model.SkillWithOwner {
	return &model.SkillWithOwner{
		Skill: model.Skill{
			ID:   uuid.New(),
			Slug: slug,
		},
		OwnerHandle: "alice",
	}
}

// TestSkillCache_NilSafe locks in the contract that all SkillCache methods
// are no-ops on nil receivers — SkillRepo relies on this so callers don't
// need to do `if r.cache != nil` everywhere.
func TestSkillCache_NilSafe(t *testing.T) {
	t.Parallel()

	var c *SkillCache
	c.Set("k", newTestSkill("k"))
	c.Invalidate("k")
	c.Purge()

	if v, ok := c.Get("k"); ok || v != nil {
		t.Fatalf("nil cache Get must return (nil, false); got (%v, %v)", v, ok)
	}
}

// TestSkillCache_DisabledOnZeroSize verifies that a non-positive size returns
// nil rather than panicking — config-disabled cache must be representable.
func TestSkillCache_DisabledOnZeroSize(t *testing.T) {
	t.Parallel()

	if c := NewSkillCache(0, time.Minute, nil, nil); c != nil {
		t.Fatalf("NewSkillCache(0, ...) must return nil, got %v", c)
	}
	if c := NewSkillCache(-1, time.Minute, nil, nil); c != nil {
		t.Fatalf("NewSkillCache(-1, ...) must return nil, got %v", c)
	}
}

// TestSkillCache_HitMiss verifies basic put/get and counter wiring.
func TestSkillCache_HitMiss(t *testing.T) {
	t.Parallel()

	c := NewSkillCache(8, time.Minute, nil, nil)
	if c == nil {
		t.Fatal("NewSkillCache returned nil")
	}

	if v, ok := c.Get("missing"); ok || v != nil {
		t.Fatalf("miss should return (nil, false); got (%v, %v)", v, ok)
	}

	skill := newTestSkill("hello")
	c.Set("hello", skill)

	got, ok := c.Get("hello")
	if !ok || got == nil {
		t.Fatalf("hit should return (skill, true); got (%v, %v)", got, ok)
	}
	if got.Slug != "hello" {
		t.Fatalf("expected slug=hello, got %q", got.Slug)
	}
}

// TestSkillCache_GetReturnsCopy is the load-bearing test — service layer
// reassigns scalar/pointer fields on the returned struct (PublishVersion
// does `skill.DisplayName = &req.DisplayName`); if Get returned the cached
// pointer directly, those assignments would leak into the next cache hit.
func TestSkillCache_GetReturnsCopy(t *testing.T) {
	t.Parallel()

	c := NewSkillCache(8, time.Minute, nil, nil)
	original := newTestSkill("foo")
	originalName := "Original"
	original.DisplayName = &originalName
	c.Set("foo", original)

	first, _ := c.Get("foo")
	mutated := "Mutated"
	first.DisplayName = &mutated
	first.Slug = "tampered"

	second, ok := c.Get("foo")
	if !ok {
		t.Fatal("second Get should still hit")
	}
	if second.Slug != "foo" {
		t.Fatalf("cache leaked Slug mutation: got %q", second.Slug)
	}
	if second.DisplayName == nil || *second.DisplayName != "Original" {
		t.Fatalf("cache leaked DisplayName mutation: got %v", second.DisplayName)
	}
}

// TestSkillCache_Invalidate ensures Invalidate purges the listed keys and
// silently skips keys that aren't present.
func TestSkillCache_Invalidate(t *testing.T) {
	t.Parallel()

	c := NewSkillCache(8, time.Minute, nil, nil)
	c.Set("a", newTestSkill("a"))
	c.Set("b", newTestSkill("b"))

	c.Invalidate("a", "missing")

	if _, ok := c.Get("a"); ok {
		t.Fatal("Invalidate should evict 'a'")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("Invalidate must not touch unrelated keys")
	}
}

// TestSkillCache_SetNilNoop guards the "don't cache absence" rule:
// repo passes nil into Set when slug doesn't exist — caching nil would
// hide newly published skills for the TTL window.
func TestSkillCache_SetNilNoop(t *testing.T) {
	t.Parallel()

	c := NewSkillCache(8, time.Minute, nil, nil)
	c.Set("ghost", nil)

	if v, ok := c.Get("ghost"); ok || v != nil {
		t.Fatalf("Set(nil) must not populate cache; got (%v, %v)", v, ok)
	}
}
