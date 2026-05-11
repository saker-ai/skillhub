package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/cinience/skillhub/pkg/config"
	"github.com/cinience/skillhub/pkg/metrics"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
)

// setupCascadeFixtures 起一个临时 sqlite + 全量 AutoMigrate，并创建一个
// owner + 一个非 owner 成员、namespace 与每人各一个 namespace-bound token。
//
// 所有副作用都落在 t.TempDir() 下，测试结束自动清理。
type cascadeFixtures struct {
	svc       *NamespaceService
	tokenRepo *repository.TokenRepo
	auditRepo *repository.AuditRepo
	mx        *metrics.Metrics
	owner     *model.User
	member    *model.User
	ns        *model.Namespace
	tokOwner  *model.APIToken
	tokMember *model.APIToken
}

func setupCascadeFixtures(t *testing.T) *cascadeFixtures {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.DatabaseConfig{
		Driver:      "sqlite",
		URL:         filepath.Join(tmp, "cascade.db"),
		AutoMigrate: true,
	}
	db, err := repository.NewDB(cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	ctx := context.Background()
	userRepo := repository.NewUserRepo(db)
	nsRepo := repository.NewNamespaceRepo(db)
	tokenRepo := repository.NewTokenRepo(db)

	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	member := &model.User{ID: uuid.New(), Handle: "bob", Role: "user"}
	if err := userRepo.Create(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := userRepo.Create(ctx, member); err != nil {
		t.Fatalf("create member: %v", err)
	}

	ns := &model.Namespace{
		ID:      uuid.New(),
		Slug:    "acme",
		OwnerID: owner.ID,
		Type:    "team",
		Status:  "active",
	}
	if err := nsRepo.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if err := nsRepo.AddMember(ctx, &model.NamespaceMember{
		ID: uuid.New(), NamespaceID: ns.ID, UserID: owner.ID, Role: "owner",
	}); err != nil {
		t.Fatalf("add owner member: %v", err)
	}
	if err := nsRepo.AddMember(ctx, &model.NamespaceMember{
		ID: uuid.New(), NamespaceID: ns.ID, UserID: member.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("add admin member: %v", err)
	}

	nsID := ns.ID
	tokOwner := &model.APIToken{
		ID: uuid.New(), UserID: owner.ID, NamespaceID: &nsID,
		Prefix: "alice___", TokenHash: "hash-alice-" + uuid.NewString(), Scope: "publish",
	}
	tokMember := &model.APIToken{
		ID: uuid.New(), UserID: member.ID, NamespaceID: &nsID,
		Prefix: "bob_____", TokenHash: "hash-bob-" + uuid.NewString(), Scope: "publish",
	}
	if err := tokenRepo.Create(ctx, tokOwner); err != nil {
		t.Fatalf("create owner token: %v", err)
	}
	if err := tokenRepo.Create(ctx, tokMember); err != nil {
		t.Fatalf("create member token: %v", err)
	}

	auditRepo := repository.NewAuditRepo(db)
	auditSvc := NewAuditService(auditRepo)

	// Private registry per test so counter values start at zero and parallel
	// runs don't see each other (the global metrics.Default is shared).
	mx := metrics.New(prometheus.NewRegistry())

	svc := NewNamespaceService(nsRepo, userRepo)
	svc.SetTokenRepo(tokenRepo)
	svc.SetAuditService(auditSvc)
	svc.SetMetrics(mx)

	return &cascadeFixtures{
		svc:       svc,
		tokenRepo: tokenRepo,
		auditRepo: auditRepo,
		mx:        mx,
		owner:     owner,
		member:    member,
		ns:        ns,
		tokOwner:  tokOwner,
		tokMember: tokMember,
	}
}

// findCascadeAuditEntry returns the most recent team_token_cascade_revoke entry
// whose Details substring matches `containsCause`, or nil if none exists.
//
// 用 Details substring 匹配是因为 cascade 写的 details 形如
// "cause=member_remove,target=bob,count=1"，断言里只关心 cause + count 部分。
func findCascadeAuditEntry(t *testing.T, repo *repository.AuditRepo, containsCause string) *model.AuditLog {
	t.Helper()
	logs, _, err := repo.List(context.Background(), 50, "", repository.AuditFilter{
		Action:       "team_token_cascade_revoke",
		ResourceType: "namespace",
	})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	for i := range logs {
		if logs[i].Details != nil && contains(*logs[i].Details, containsCause) {
			return &logs[i]
		}
	}
	return nil
}

// contains is a tiny stdlib-free substring helper to keep the test file's
// import surface narrow (avoids a strings import just for this).
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func tokenIsRevoked(t *testing.T, repo *repository.TokenRepo, id uuid.UUID) bool {
	t.Helper()
	tok, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if tok == nil {
		t.Fatalf("token %s not found", id)
	}
	return tok.RevokedAt != nil
}

// TestNamespaceCascade_RemoveMember 验证 owner/admin 把成员踢出时，
// 该成员名下绑定到此 namespace 的所有 token 必须立即失效；
// owner 自己的 token 不能被波及——他还在团队里。
func TestNamespaceCascade_RemoveMember(t *testing.T) {
	t.Parallel()
	fx := setupCascadeFixtures(t)
	ctx := context.Background()

	if err := fx.svc.RemoveMember(ctx, fx.owner, fx.ns.Slug, fx.member.Handle); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	if !tokenIsRevoked(t, fx.tokenRepo, fx.tokMember.ID) {
		t.Errorf("expected member token to be revoked after RemoveMember")
	}
	if tokenIsRevoked(t, fx.tokenRepo, fx.tokOwner.ID) {
		t.Errorf("owner token should NOT be revoked — owner is still a member")
	}

	entry := findCascadeAuditEntry(t, fx.auditRepo, "cause=member_remove,target=bob,count=1")
	if entry == nil {
		t.Fatalf("expected audit log entry for member_remove cascade")
	}
	if entry.ActorID == nil || *entry.ActorID != fx.owner.ID {
		t.Errorf("expected actor=owner, got %v", entry.ActorID)
	}
	if entry.ResourceID == nil || *entry.ResourceID != fx.ns.ID {
		t.Errorf("expected resource=namespace, got %v", entry.ResourceID)
	}

	// Counter incremented by batch size — bob had exactly 1 token under acme.
	if got := testutil.ToFloat64(fx.mx.TeamTokenRevoked.WithLabelValues("cascade_member_remove")); got != 1 {
		t.Errorf("metric cascade_member_remove = %v, want 1", got)
	}
}

// TestNamespaceCascade_Leave 验证非 owner 成员主动离开时，自己的 token 失效，
// 仍在团队里的 owner token 不受影响。
func TestNamespaceCascade_Leave(t *testing.T) {
	t.Parallel()
	fx := setupCascadeFixtures(t)
	ctx := context.Background()

	if err := fx.svc.Leave(ctx, fx.member, fx.ns.Slug); err != nil {
		t.Fatalf("Leave: %v", err)
	}

	if !tokenIsRevoked(t, fx.tokenRepo, fx.tokMember.ID) {
		t.Errorf("expected leaving member's token to be revoked")
	}
	if tokenIsRevoked(t, fx.tokenRepo, fx.tokOwner.ID) {
		t.Errorf("owner token should NOT be revoked — owner stayed")
	}

	entry := findCascadeAuditEntry(t, fx.auditRepo, "cause=member_leave,count=1")
	if entry == nil {
		t.Fatalf("expected audit log entry for member_leave cascade")
	}
	if entry.ActorID == nil || *entry.ActorID != fx.member.ID {
		t.Errorf("expected actor=leaving member, got %v", entry.ActorID)
	}

	if got := testutil.ToFloat64(fx.mx.TeamTokenRevoked.WithLabelValues("cascade_member_leave")); got != 1 {
		t.Errorf("metric cascade_member_leave = %v, want 1", got)
	}
}

// TestNamespaceCascade_Delete 验证 owner 删除整个 namespace 时，
// 该 namespace 下所有成员（含 owner 自己）的 token 全部失效。
func TestNamespaceCascade_Delete(t *testing.T) {
	t.Parallel()
	fx := setupCascadeFixtures(t)
	ctx := context.Background()

	if err := fx.svc.Delete(ctx, fx.owner, fx.ns.Slug); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !tokenIsRevoked(t, fx.tokenRepo, fx.tokOwner.ID) {
		t.Errorf("expected owner token to be revoked after namespace delete")
	}
	if !tokenIsRevoked(t, fx.tokenRepo, fx.tokMember.ID) {
		t.Errorf("expected member token to be revoked after namespace delete")
	}

	// count=2 because Delete revokes ALL tokens under the namespace, including
	// the owner's own — Leave/RemoveMember only touch one user's slice.
	entry := findCascadeAuditEntry(t, fx.auditRepo, "cause=namespace_delete,count=2")
	if entry == nil {
		t.Fatalf("expected audit log entry for namespace_delete cascade with count=2")
	}
	if entry.ActorID == nil || *entry.ActorID != fx.owner.ID {
		t.Errorf("expected actor=owner, got %v", entry.ActorID)
	}

	if got := testutil.ToFloat64(fx.mx.TeamTokenRevoked.WithLabelValues("cascade_namespace_delete")); got != 2 {
		t.Errorf("metric cascade_namespace_delete = %v, want 2", got)
	}
}

// TestNamespaceCascade_NoOpWithoutTokenRepo 是回归保险：
// 当嵌入方忘记 SetTokenRepo 时，cascade 不应在 nil pointer 上 panic，
// 也不应报错——保持向后兼容（生产路径会被 server.go 强制注入）。
func TestNamespaceCascade_NoOpWithoutTokenRepo(t *testing.T) {
	t.Parallel()
	fx := setupCascadeFixtures(t)
	ctx := context.Background()

	// Detach the cascade wire to simulate "embedder never called SetTokenRepo".
	fx.svc.tokenRepo = nil

	if err := fx.svc.RemoveMember(ctx, fx.owner, fx.ns.Slug, fx.member.Handle); err != nil {
		t.Errorf("RemoveMember without tokenRepo should not error, got: %v", err)
	}
	// Member should be removed but tokens left intact (cascade is opt-in via SetTokenRepo).
	if tokenIsRevoked(t, fx.tokenRepo, fx.tokMember.ID) {
		t.Errorf("without tokenRepo wired, member token must NOT be auto-revoked")
	}
}
