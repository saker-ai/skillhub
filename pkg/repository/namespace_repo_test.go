package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/model"
)

func TestEnsurePersonalNamespace(t *testing.T) {
	tmp := t.TempDir()
	db, err := NewDB(config.DatabaseConfig{
		Driver:      "sqlite",
		URL:         filepath.Join(tmp, "test.db"),
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	ctx := context.Background()
	userRepo := NewUserRepo(db)
	nsRepo := NewNamespaceRepo(db)

	user := &model.User{ID: uuid.New(), Handle: "testuser", Role: "user"}
	if err := userRepo.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// First call: creates namespace
	ns1, err := nsRepo.EnsurePersonalNamespace(ctx, user)
	if err != nil {
		t.Fatalf("EnsurePersonalNamespace (first): %v", err)
	}
	if ns1 == nil {
		t.Fatal("expected non-nil namespace")
	}
	if ns1.Slug != user.Handle {
		t.Errorf("slug = %q, want %q", ns1.Slug, user.Handle)
	}
	if ns1.Type != "personal" {
		t.Errorf("type = %q, want personal", ns1.Type)
	}

	// Second call: returns same namespace (idempotent)
	ns2, err := nsRepo.EnsurePersonalNamespace(ctx, user)
	if err != nil {
		t.Fatalf("EnsurePersonalNamespace (second): %v", err)
	}
	if ns2.ID != ns1.ID {
		t.Errorf("second call returned different ID: %s vs %s", ns2.ID, ns1.ID)
	}

	// Verify member was created
	role, err := nsRepo.GetMemberRole(ctx, ns1.ID, user.ID)
	if err != nil {
		t.Fatalf("GetMemberRole: %v", err)
	}
	if role != "owner" {
		t.Errorf("member role = %q, want owner", role)
	}
}

func TestGetPersonalByOwnerID_NotFound(t *testing.T) {
	tmp := t.TempDir()
	db, err := NewDB(config.DatabaseConfig{
		Driver:      "sqlite",
		URL:         filepath.Join(tmp, "test.db"),
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	nsRepo := NewNamespaceRepo(db)
	ns, err := nsRepo.GetPersonalByOwnerID(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetPersonalByOwnerID: %v", err)
	}
	if ns != nil {
		t.Error("expected nil for non-existent user")
	}
}
