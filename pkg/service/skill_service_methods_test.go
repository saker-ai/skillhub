package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"

	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/gitstore"
	"github.com/saker-ai/skillhub/pkg/metrics"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
	"github.com/saker-ai/skillhub/pkg/search"
	storegit "github.com/saker-ai/skillhub/pkg/store/git"
)

// skillFixtures bundles the dependencies the SkillService methods touch.
//
// Modeled after setupCascadeFixtures in namespace_cascade_test.go but adds
// the skill/version/download/star repos and a real git-backed Store so
// PublishVersion (which writes a commit) can run end-to-end.
//
// Tests must NOT share fixtures across t.Run cases — t.Parallel() means
// concurrent goroutines, and the underlying sqlite + git tempdir must be
// isolated to keep failures reproducible.
type skillFixtures struct {
	db          *gorm.DB
	svc         *SkillService
	skillRepo   *repository.SkillRepo
	versionRepo *repository.VersionRepo
	nsSvc       *NamespaceService
	owner       *model.User
	other       *model.User
	admin       *model.User
}

func setupSkillFixtures(t *testing.T) *skillFixtures {
	t.Helper()
	tmp := t.TempDir()

	db, err := repository.NewDB(config.DatabaseConfig{
		Driver:      "sqlite",
		URL:         filepath.Join(tmp, "skill.db"),
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
	userRepo := repository.NewUserRepo(db)
	skillRepo := repository.NewSkillRepo(db)
	versionRepo := repository.NewVersionRepo(db)
	downloadRepo := repository.NewDownloadRepo(db)
	starRepo := repository.NewStarRepo(db)
	auditRepo := repository.NewAuditRepo(db)

	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	other := &model.User{ID: uuid.New(), Handle: "bob", Role: "user"}
	admin := &model.User{ID: uuid.New(), Handle: "root", Role: "admin"}
	for _, u := range []*model.User{owner, other, admin} {
		if err := userRepo.Create(ctx, u); err != nil {
			t.Fatalf("create user %s: %v", u.Handle, err)
		}
	}

	// Real git store under t.TempDir so Publish creates an actual commit.
	// Going through the direct constructor (not store.Open) keeps the test
	// from depending on the driver-registry side effects in cmd/skillhub.
	gs, err := gitstore.New(filepath.Join(tmp, "repos"))
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	fileStore := storegit.New(gs)

	auditSvc := NewAuditService(auditRepo)

	nsRepo := repository.NewNamespaceRepo(db)
	nsSvc := NewNamespaceService(nsRepo, userRepo)

	svc := NewSkillService(db, skillRepo, versionRepo, userRepo, downloadRepo, starRepo, fileStore, nil, nil, auditSvc)
	svc.SetNamespaceService(nsSvc)
	// Private registry so SkillPublished counters don't leak across tests
	// running in parallel — same reason the cascade fixture uses one.
	svc.SetMetrics(metrics.New(prometheus.NewRegistry()))

	return &skillFixtures{
		db:          db,
		svc:         svc,
		skillRepo:   skillRepo,
		versionRepo: versionRepo,
		nsSvc:       nsSvc,
		owner:       owner,
		other:       other,
		admin:       admin,
	}
}

// publishBaseSkill primes a slug with a 1.0.0 version owned by `as` so
// follow-up tests (yank / deprecate / delete / request-public) have a
// real version to operate on.
func (fx *skillFixtures) publishBaseSkill(t *testing.T, slug string, as *model.User) *model.SkillWithOwner {
	t.Helper()
	skill, _, err := fx.svc.PublishVersion(context.Background(), as, PublishRequest{
		Slug:    slug,
		Version: "1.0.0",
		Files:   map[string][]byte{"SKILL.md": []byte("---\nname: " + slug + "\n---\n# " + slug + "\n")},
	})
	if err != nil {
		t.Fatalf("publishBaseSkill(%s): %v", slug, err)
	}
	return skill
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }

func (fx *skillFixtures) publishNamespaceSkill(t *testing.T, nsSlug, slug string, memberRole string) *model.SkillWithOwner {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.nsSvc.Create(ctx, fx.owner, nsSlug, "", "", "team"); err != nil {
		t.Fatalf("create namespace %s: %v", nsSlug, err)
	}
	if memberRole != "" {
		if err := fx.nsSvc.AddMember(ctx, fx.owner, nsSlug, fx.other.Handle, memberRole); err != nil {
			t.Fatalf("add %s as %s: %v", fx.other.Handle, memberRole, err)
		}
	}
	skill, _, err := fx.svc.PublishVersion(ctx, fx.owner, PublishRequest{
		Slug:          slug,
		Version:       "1.0.0",
		NamespaceSlug: nsSlug,
		Files:         map[string][]byte{"SKILL.md": []byte("---\nname: " + slug + "\n---\n# " + slug + "\n")},
	})
	if err != nil {
		t.Fatalf("publish namespace skill %s/%s: %v", nsSlug, slug, err)
	}
	return skill
}

// TestSkillService_PublishVersion exercises the validation and authorization
// gates inside PublishVersion. Each subtest gets a fresh fixture so DB
// state and the git tempdir don't bleed between cases (PublishVersion
// writes both).
func TestSkillService_PublishVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		setup   func(t *testing.T, fx *skillFixtures)
		as      func(fx *skillFixtures) *model.User
		req     PublishRequest
		wantErr string // substring match; "" means no error expected
	}{
		{
			name: "happy: owner publishes new skill 1.0.0",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "demo",
				Version: "1.0.0",
				Files:   map[string][]byte{"SKILL.md": []byte("# demo\n")},
			},
		},
		{
			name: "reject: empty slug",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "",
				Version: "1.0.0",
				Files:   map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "slug is required",
		},
		{
			name: "reject: invalid semver",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "broken-semver",
				Version: "not.a.semver",
				Files:   map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "invalid version",
		},
		{
			name: "reject: builtin kind by non-admin",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "userland-builtin",
				Version: "1.0.0",
				Kind:    "builtin",
				Files:   map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "admin-only",
		},
		{
			name: "reject: invalid kind value",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "bad-kind",
				Version: "1.0.0",
				Kind:    "totally-made-up",
				Files:   map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "invalid kind",
		},
		{
			name: "reject: team token publishing without namespace",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:           "no-ns",
				Version:        "1.0.0",
				TokenNamespace: ptrUUID(uuid.New()),
				Files:          map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "team token can only publish",
		},
		{
			name: "reject: duplicate version",
			setup: func(t *testing.T, fx *skillFixtures) {
				fx.publishBaseSkill(t, "dup", fx.owner)
			},
			as: func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "dup",
				Version: "1.0.0",
				Files:   map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "already exists",
		},
		{
			name: "reject: version not greater than current latest",
			setup: func(t *testing.T, fx *skillFixtures) {
				fx.publishBaseSkill(t, "older", fx.owner)
			},
			as: func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:    "older",
				Version: "0.5.0",
				Files:   map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "must be greater",
		},
		{
			name: "reject: invalid category",
			as:   func(fx *skillFixtures) *model.User { return fx.owner },
			req: PublishRequest{
				Slug:     "bad-cat",
				Version:  "1.0.0",
				Category: "made-up-category",
				Files:    map[string][]byte{"SKILL.md": []byte("# x\n")},
			},
			wantErr: "invalid category",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			if tc.setup != nil {
				tc.setup(t, fx)
			}

			skill, ver, err := fx.svc.PublishVersion(context.Background(), tc.as(fx), tc.req)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("PublishVersion: unexpected error %v", err)
				}
				if skill == nil || ver == nil {
					t.Fatalf("PublishVersion: expected non-nil skill and version on success")
				}
				if skill.Slug != tc.req.Slug {
					t.Errorf("skill.Slug = %q, want %q", skill.Slug, tc.req.Slug)
				}
				if ver.Version != tc.req.Version {
					t.Errorf("version.Version = %q, want %q", ver.Version, tc.req.Version)
				}
				return
			}
			if err == nil {
				t.Fatalf("PublishVersion: want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("PublishVersion: error %v does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestSkillService_SoftDelete covers the authorization branches of
// SoftDelete: owner allowed, non-owner forbidden via ErrForbidden,
// system admin allowed, missing skill returns a not-found message.
func TestSkillService_SoftDelete(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		as         func(fx *skillFixtures) *model.User
		publish    bool
		wantErr    error  // sentinel match via errors.Is
		wantErrStr string // substring match; mutually exclusive with wantErr
	}{
		{name: "owner can delete", as: func(fx *skillFixtures) *model.User { return fx.owner }, publish: true},
		{name: "system admin can delete", as: func(fx *skillFixtures) *model.User { return fx.admin }, publish: true},
		{name: "non-owner forbidden", as: func(fx *skillFixtures) *model.User { return fx.other }, publish: true, wantErr: ErrForbidden},
		{name: "skill not found", as: func(fx *skillFixtures) *model.User { return fx.owner }, publish: false, wantErrStr: "skill not found"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			slug := "demo"
			if tc.publish {
				fx.publishBaseSkill(t, slug, fx.owner)
			}

			err := fx.svc.SoftDelete(context.Background(), tc.as(fx), model.SkillRef{Slug: slug}, nil)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SoftDelete: want errors.Is %v, got %v", tc.wantErr, err)
				}
			case tc.wantErrStr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErrStr) {
					t.Fatalf("SoftDelete: want error containing %q, got %v", tc.wantErrStr, err)
				}
			default:
				if err != nil {
					t.Fatalf("SoftDelete: unexpected error %v", err)
				}
				// GetBySlug filters out soft-deleted rows, so query the raw
				// table directly — the row should still exist but with
				// SoftDeletedAt set.
				var got model.Skill
				if err := fx.db.Unscoped().Where("slug = ?", slug).First(&got).Error; err != nil {
					t.Fatalf("raw skill lookup after SoftDelete: %v", err)
				}
				if got.SoftDeletedAt == nil {
					t.Errorf("expected SoftDeletedAt to be set")
				}
			}
		})
	}
}

func TestSkillService_SoftDelete_NamespaceRoles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		memberRole string
		wantErr    error
	}{
		{name: "namespace admin can delete", memberRole: "admin"},
		{name: "namespace member can delete", memberRole: "member"},
		{name: "namespace reader forbidden", memberRole: "reader", wantErr: ErrForbidden},
		{name: "non-member forbidden", memberRole: "", wantErr: ErrForbidden},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			nsSlug := "team-" + strings.ReplaceAll(tc.name, " ", "-")
			slug := "demo"
			fx.publishNamespaceSkill(t, nsSlug, slug, tc.memberRole)

			err := fx.svc.SoftDelete(context.Background(), fx.other, model.SkillRef{Namespace: nsSlug, Slug: slug}, nil)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SoftDelete: want errors.Is %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SoftDelete: unexpected error %v", err)
			}
		})
	}
}

func TestSkillService_Undelete_ResolvesSoftDeletedSkill(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ref  model.SkillRef
	}{
		{name: "bare slug", ref: model.SkillRef{Slug: "demo"}},
		{name: "namespace slug", ref: model.SkillRef{Namespace: "team-restore", Slug: "demo"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			if tc.ref.Namespace == "" {
				fx.publishBaseSkill(t, tc.ref.Slug, fx.owner)
			} else {
				fx.publishNamespaceSkill(t, tc.ref.Namespace, tc.ref.Slug, "")
			}

			if err := fx.svc.SoftDelete(context.Background(), fx.owner, tc.ref, nil); err != nil {
				t.Fatalf("SoftDelete: %v", err)
			}
			if err := fx.svc.Undelete(context.Background(), fx.owner, tc.ref, nil); err != nil {
				t.Fatalf("Undelete: %v", err)
			}

			got, err := fx.svc.resolveSkillRef(context.Background(), tc.ref)
			if err != nil {
				t.Fatalf("resolve after Undelete: %v", err)
			}
			if got == nil {
				t.Fatalf("resolve after Undelete returned nil")
			}
			if got.SoftDeletedAt != nil {
				t.Fatalf("SoftDeletedAt after Undelete = %v, want nil", got.SoftDeletedAt)
			}
		})
	}
}

// TestSkillService_YankVersion covers the happy path, the double-yank
// guard, and the missing-version branch. Each case uses a freshly
// published 1.0.0 so the version_repo lookup succeeds.
func TestSkillService_YankVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		setup      func(t *testing.T, fx *skillFixtures, slug string)
		as         func(fx *skillFixtures) *model.User
		version    string
		wantErrStr string
	}{
		{
			name:    "owner yanks 1.0.0",
			as:      func(fx *skillFixtures) *model.User { return fx.owner },
			version: "1.0.0",
		},
		{
			name: "double yank rejected",
			setup: func(t *testing.T, fx *skillFixtures, slug string) {
				if err := fx.svc.YankVersion(context.Background(), fx.owner, model.SkillRef{Slug: slug}, "1.0.0", "first", nil); err != nil {
					t.Fatalf("priming yank: %v", err)
				}
			},
			as:         func(fx *skillFixtures) *model.User { return fx.owner },
			version:    "1.0.0",
			wantErrStr: "already yanked",
		},
		{
			name:       "non-existent version",
			as:         func(fx *skillFixtures) *model.User { return fx.owner },
			version:    "9.9.9",
			wantErrStr: "version not found",
		},
		{
			name:       "non-owner forbidden",
			as:         func(fx *skillFixtures) *model.User { return fx.other },
			version:    "1.0.0",
			wantErrStr: "forbidden",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			slug := "demo"
			fx.publishBaseSkill(t, slug, fx.owner)
			if tc.setup != nil {
				tc.setup(t, fx, slug)
			}

			err := fx.svc.YankVersion(context.Background(), tc.as(fx), model.SkillRef{Slug: slug}, tc.version, "test reason", nil)
			if tc.wantErrStr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrStr) {
					t.Fatalf("YankVersion: want error containing %q, got %v", tc.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("YankVersion: unexpected error %v", err)
			}
			// Confirm the yank actually landed in the DB.
			sk, _ := fx.skillRepo.GetBySlug(context.Background(), slug)
			if sk == nil {
				t.Fatalf("skill missing after yank")
			}
			ver, _ := fx.versionRepo.GetBySkillAndVersion(context.Background(), sk.ID, tc.version)
			if ver == nil {
				t.Fatalf("version missing after yank")
			}
			if ver.YankedAt == nil {
				t.Errorf("expected YankedAt to be set after successful yank")
			}
		})
	}
}

// TestSkillService_DeprecateVersion covers the deprecate / undeprecate
// state machine: deprecate works on a fresh version, undeprecate fails
// when nothing is deprecated, and undeprecate succeeds after a prior
// deprecate.
func TestSkillService_DeprecateVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		op         string // "deprecate" | "undeprecate"
		prePrime   bool   // run a deprecate before the op (only meaningful for undeprecate)
		wantErrStr string
	}{
		{name: "deprecate: happy path", op: "deprecate"},
		{name: "undeprecate: without prior deprecate fails", op: "undeprecate", wantErrStr: "is not deprecated"},
		{name: "undeprecate: after deprecate succeeds", op: "undeprecate", prePrime: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			slug := "demo"
			fx.publishBaseSkill(t, slug, fx.owner)

			ctx := context.Background()
			if tc.prePrime {
				if err := fx.svc.DeprecateVersion(ctx, fx.owner, model.SkillRef{Slug: slug}, "1.0.0", "buggy", nil); err != nil {
					t.Fatalf("priming deprecate: %v", err)
				}
			}

			var err error
			switch tc.op {
			case "deprecate":
				err = fx.svc.DeprecateVersion(ctx, fx.owner, model.SkillRef{Slug: slug}, "1.0.0", "buggy", nil)
			case "undeprecate":
				err = fx.svc.UndeprecateVersion(ctx, fx.owner, model.SkillRef{Slug: slug}, "1.0.0", nil)
			default:
				t.Fatalf("unknown op %q", tc.op)
			}

			if tc.wantErrStr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrStr) {
					t.Fatalf("%s: want error containing %q, got %v", tc.op, tc.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.op, err)
			}

			// Confirm the new state landed.
			sk, _ := fx.skillRepo.GetBySlug(ctx, slug)
			ver, _ := fx.versionRepo.GetBySkillAndVersion(ctx, sk.ID, "1.0.0")
			switch tc.op {
			case "deprecate":
				if ver.DeprecatedAt == nil {
					t.Errorf("expected DeprecatedAt to be set after deprecate")
				}
			case "undeprecate":
				if ver.DeprecatedAt != nil {
					t.Errorf("expected DeprecatedAt to be cleared after undeprecate")
				}
			}
		})
	}
}

// TestSkillService_RequestPublic exercises the visibility transition: any
// non-owner / non-admin caller is forbidden, an already-public+approved
// skill is rejected, and the happy path leaves the skill in
// (private, pending_review) waiting for a moderator.
func TestSkillService_RequestPublic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		as          func(fx *skillFixtures) *model.User
		priorPublic bool // mark the skill public+approved before calling
		wantErrStr  string
	}{
		{name: "owner requests public", as: func(fx *skillFixtures) *model.User { return fx.owner }},
		{name: "system admin requests public", as: func(fx *skillFixtures) *model.User { return fx.admin }},
		{name: "non-owner forbidden", as: func(fx *skillFixtures) *model.User { return fx.other }, wantErrStr: "forbidden"},
		{name: "already public+approved rejected", as: func(fx *skillFixtures) *model.User { return fx.owner }, priorPublic: true, wantErrStr: "already public"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := setupSkillFixtures(t)
			slug := "demo"
			sk := fx.publishBaseSkill(t, slug, fx.owner)

			ctx := context.Background()
			if tc.priorPublic {
				if err := fx.skillRepo.SetVisibility(ctx, sk.ID, "public", "approved"); err != nil {
					t.Fatalf("seed public: %v", err)
				}
			}

			err := fx.svc.RequestPublic(ctx, tc.as(fx), model.SkillRef{Slug: slug})
			if tc.wantErrStr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrStr) {
					t.Fatalf("RequestPublic: want error containing %q, got %v", tc.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequestPublic: unexpected error %v", err)
			}
			got, _ := fx.skillRepo.GetBySlug(ctx, slug)
			if got == nil {
				t.Fatalf("skill missing after RequestPublic")
			}
			if got.Visibility != "private" || got.ModerationStatus != "pending_review" {
				t.Errorf("after RequestPublic: visibility=%q moderation=%q, want private / pending_review",
					got.Visibility, got.ModerationStatus)
			}
		})
	}
}

func TestSkillService_SetSkillVisibilityReindexesSearch(t *testing.T) {
	t.Parallel()

	fx := setupSkillFixtures(t)
	sc, err := search.New(config.SearchConfig{IndexPath: filepath.Join(t.TempDir(), "skills.bleve")})
	if err != nil {
		t.Fatalf("search.New: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	fx.svc.searchClient = sc

	ctx := context.Background()
	if _, _, err := fx.svc.PublishVersion(ctx, fx.admin, PublishRequest{
		Slug:    "visibility-index",
		Version: "1.0.0",
		Summary: "unique-search-needle",
		Files: map[string][]byte{
			"SKILL.md": []byte("---\nname: visibility-index\n---\n# visibility-index\n"),
		},
	}); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}

	filters := []search.Filter{
		{Field: "visibility", Value: "public"},
		{Field: "moderationStatus", Value: "approved"},
		{Field: "isDeleted", Value: false},
	}
	before, err := sc.Search(ctx, "unique-search-needle", 10, 0, nil, filters)
	if err != nil {
		t.Fatalf("search before visibility change: %v", err)
	}
	if before.EstimatedTotal != 0 {
		t.Fatalf("private skill search hits before visibility change = %d, want 0", before.EstimatedTotal)
	}

	if err := fx.svc.SetSkillVisibility(ctx, ptrUUID(fx.admin.ID), model.SkillRef{Slug: "visibility-index"}, "public"); err != nil {
		t.Fatalf("SetSkillVisibility: %v", err)
	}
	after, err := sc.Search(ctx, "unique-search-needle", 10, 0, nil, filters)
	if err != nil {
		t.Fatalf("search after visibility change: %v", err)
	}
	if after.EstimatedTotal != 1 {
		t.Fatalf("public skill search hits after visibility change = %d, want 1", after.EstimatedTotal)
	}
}

func TestSkillService_UpdateFile(t *testing.T) {
	t.Parallel()

	t.Run("happy: update SKILL.md bumps patch", func(t *testing.T) {
		t.Parallel()
		fx := setupSkillFixtures(t)
		slug := "demo"
		fx.publishBaseSkill(t, slug, fx.owner)

		ctx := context.Background()
		newContent := []byte("---\nname: demo\n---\n# Updated\n")

		skill, ver, err := fx.svc.UpdateFile(ctx, fx.owner, UpdateFileRequest{
			Ref:     model.SkillRef{Slug: slug},
			Path:    "SKILL.md",
			Content: newContent,
		})
		if err != nil {
			t.Fatalf("UpdateFile: %v", err)
		}
		if ver.Version != "1.0.1" {
			t.Errorf("version = %q, want 1.0.1", ver.Version)
		}
		if skill == nil {
			t.Fatal("expected non-nil skill")
		}

		content, err := fx.svc.GetFile(ctx, model.SkillRef{Slug: slug}, "1.0.1", "SKILL.md", fx.owner)
		if err != nil {
			t.Fatalf("GetFile after update: %v", err)
		}
		if string(content) != string(newContent) {
			t.Errorf("content mismatch: got %q", string(content))
		}
	})

	t.Run("reject: non-owner forbidden", func(t *testing.T) {
		t.Parallel()
		fx := setupSkillFixtures(t)
		slug := "demo"
		fx.publishBaseSkill(t, slug, fx.owner)

		_, _, err := fx.svc.UpdateFile(context.Background(), fx.other, UpdateFileRequest{
			Ref:     model.SkillRef{Slug: slug},
			Path:    "SKILL.md",
			Content: []byte("hacked"),
		})
		if err == nil || !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("expected forbidden error, got %v", err)
		}
	})

	t.Run("reject: no versions published", func(t *testing.T) {
		t.Parallel()
		fx := setupSkillFixtures(t)

		_, _, err := fx.svc.UpdateFile(context.Background(), fx.owner, UpdateFileRequest{
			Ref:     model.SkillRef{Slug: "nonexistent"},
			Path:    "SKILL.md",
			Content: []byte("data"),
		})
		if err == nil {
			t.Fatal("expected error for nonexistent skill")
		}
	})
}
