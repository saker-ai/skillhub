package service

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/cinience/skillhub/pkg/metrics"
	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/google/uuid"
)

// InvitationTTL is how long a pending invitation remains valid.
const InvitationTTL = 14 * 24 * time.Hour

var nsSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

type NamespaceService struct {
	nsRepo    *repository.NamespaceRepo
	userRepo  *repository.UserRepo
	invRepo   *repository.NamespaceInvitationRepo
	tokenRepo *repository.TokenRepo
	auditSvc  *AuditService
	metrics   *metrics.Metrics
}

func NewNamespaceService(nsRepo *repository.NamespaceRepo, userRepo *repository.UserRepo) *NamespaceService {
	return &NamespaceService{nsRepo: nsRepo, userRepo: userRepo}
}

// SetInvitationRepo wires the invitation repo. Optional — invitation endpoints
// fail gracefully if not configured.
func (s *NamespaceService) SetInvitationRepo(invRepo *repository.NamespaceInvitationRepo) {
	s.invRepo = invRepo
}

// SetTokenRepo wires the token repo. Optional but strongly recommended:
// when nil, RemoveMember / Leave / Delete cannot cascade-revoke namespace-bound
// team tokens, leaving them as orphans. The publish-time auth check
// (authorizeSkillWrite) is the second line of defense — it rejects tokens whose
// namespace_id no longer matches the actor's membership — but the orphan
// records themselves linger in the DB until manually cleaned up.
//
// 之所以做成 setter 而非构造函数参数，是为了不破坏既有的 NewNamespaceService
// 签名（与 SetInvitationRepo 一致的风格）。
func (s *NamespaceService) SetTokenRepo(tokenRepo *repository.TokenRepo) {
	s.tokenRepo = tokenRepo
}

// SetAuditService wires the audit service so cascade-revoke paths
// (member remove / leave / namespace delete) can record how many team tokens
// were affected and why. nil is a no-op so embedders without an audit pipeline
// keep the cascade semantics without a dependency.
func (s *NamespaceService) SetAuditService(a *AuditService) {
	s.auditSvc = a
}

// SetMetrics wires the Prometheus metrics instance so cascade-revoke paths
// can increment skillhub_team_token_revoked_total{cause=cascade_*}. nil falls
// back to metrics.Default — same convention as SkillService.SetMetrics.
func (s *NamespaceService) SetMetrics(m *metrics.Metrics) {
	s.metrics = m
}

func (s *NamespaceService) metricsOrDefault() *metrics.Metrics {
	if s.metrics != nil {
		return s.metrics
	}
	return metrics.Default
}

// EnsurePersonalNamespace finds or creates the personal namespace for a user.
func (s *NamespaceService) EnsurePersonalNamespace(ctx context.Context, user *model.User) (*model.Namespace, error) {
	return s.nsRepo.EnsurePersonalNamespace(ctx, user)
}

// EnsureOrgNamespace finds or creates a team namespace for an organization slug,
// and ensures the given user is at least a member. Used by OAuth org sync.
func (s *NamespaceService) EnsureOrgNamespace(ctx context.Context, orgSlug string, userID uuid.UUID) (*model.Namespace, error) {
	ns, err := s.nsRepo.GetBySlug(ctx, orgSlug)
	if err != nil {
		return nil, err
	}
	if ns == nil {
		ns = &model.Namespace{
			ID:                uuid.New(),
			Slug:              orgSlug,
			OwnerID:           userID,
			Type:              "team",
			Status:            "active",
			DefaultVisibility: "private",
		}
		if err := s.nsRepo.Create(ctx, ns); err != nil {
			// Race: another request created it — fetch.
			ns, _ = s.nsRepo.GetBySlug(ctx, orgSlug)
			if ns == nil {
				return nil, err
			}
		} else {
			member := &model.NamespaceMember{
				ID:          uuid.New(),
				NamespaceID: ns.ID,
				UserID:      userID,
				Role:        "owner",
			}
			_ = s.nsRepo.AddMember(ctx, member)
			return ns, nil
		}
	}

	// Ensure user is a member.
	role, _ := s.nsRepo.GetMemberRole(ctx, ns.ID, userID)
	if role == "" {
		member := &model.NamespaceMember{
			ID:          uuid.New(),
			NamespaceID: ns.ID,
			UserID:      userID,
			Role:        "member",
		}
		_ = s.nsRepo.AddMember(ctx, member)
	}
	return ns, nil
}

// Create creates a new namespace and adds the creator as owner.
// NamespaceOptions holds optional fields for Create/Update.
type NamespaceOptions struct {
	DefaultVisibility string
	MaxSkills         int
}

func (s *NamespaceService) Create(ctx context.Context, user *model.User, slug, displayName, description, nsType string, opts ...NamespaceOptions) (*model.Namespace, error) {
	if !nsSlugRe.MatchString(slug) {
		return nil, fmt.Errorf("invalid namespace slug: must be 3-64 lowercase alphanumeric characters or hyphens")
	}

	existing, err := s.nsRepo.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("namespace slug '%s' already taken", slug)
	}

	if nsType == "" {
		nsType = "team"
	}
	if nsType != "personal" && nsType != "team" {
		return nil, fmt.Errorf("type must be 'personal' or 'team'")
	}

	ns := &model.Namespace{
		ID:                uuid.New(),
		Slug:              slug,
		OwnerID:           user.ID,
		Type:              nsType,
		Status:            "active",
		DefaultVisibility: "private",
	}
	if displayName != "" {
		ns.DisplayName = &displayName
	}
	if description != "" {
		ns.Description = &description
	}
	if len(opts) > 0 {
		if opts[0].DefaultVisibility == "public" || opts[0].DefaultVisibility == "private" {
			ns.DefaultVisibility = opts[0].DefaultVisibility
		}
		if opts[0].MaxSkills > 0 {
			ns.MaxSkills = opts[0].MaxSkills
		}
	}

	if err := s.nsRepo.Create(ctx, ns); err != nil {
		return nil, err
	}

	// Add creator as owner member
	member := &model.NamespaceMember{
		ID:          uuid.New(),
		NamespaceID: ns.ID,
		UserID:      user.ID,
		Role:        "owner",
	}
	if err := s.nsRepo.AddMember(ctx, member); err != nil {
		return nil, err
	}

	return ns, nil
}

// GetBySlug returns a namespace by slug.
func (s *NamespaceService) GetBySlug(ctx context.Context, slug string) (*model.Namespace, error) {
	return s.nsRepo.GetBySlug(ctx, slug)
}

// ListByUser returns all namespaces the user belongs to.
func (s *NamespaceService) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Namespace, error) {
	return s.nsRepo.ListByUser(ctx, userID)
}

// Update updates a namespace's display name and description.
func (s *NamespaceService) Update(ctx context.Context, user *model.User, slug, displayName, description string, opts ...NamespaceOptions) (*model.Namespace, error) {
	ns, err := s.nsRepo.GetBySlug(ctx, slug)
	if err != nil || ns == nil {
		return nil, fmt.Errorf("namespace not found")
	}

	role, err := s.nsRepo.GetMemberRole(ctx, ns.ID, user.ID)
	if err != nil {
		return nil, err
	}
	if role != "owner" && role != "admin" && !user.IsAdmin() {
		return nil, fmt.Errorf("forbidden")
	}

	ns.DisplayName = &displayName
	ns.Description = &description
	if len(opts) > 0 {
		if opts[0].DefaultVisibility == "public" || opts[0].DefaultVisibility == "private" {
			ns.DefaultVisibility = opts[0].DefaultVisibility
		}
		if opts[0].MaxSkills >= 0 {
			ns.MaxSkills = opts[0].MaxSkills
		}
	}

	if err := s.nsRepo.Update(ctx, ns); err != nil {
		return nil, err
	}
	return ns, nil
}

// AddMember adds a user to a namespace.
func (s *NamespaceService) AddMember(ctx context.Context, actor *model.User, nsSlug, handle, role string) error {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return fmt.Errorf("namespace not found")
	}

	actorRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, actor.ID)
	if err != nil {
		return err
	}
	if actorRole != "owner" && actorRole != "admin" && !actor.IsAdmin() {
		return fmt.Errorf("%w: only owner or admin can manage members", ErrForbidden)
	}

	if role == "" {
		role = "member"
	}
	if role != "owner" && role != "admin" && role != "member" && role != "reader" {
		return fmt.Errorf("role must be 'owner', 'admin', 'member', or 'reader'")
	}

	targetUser, err := s.userRepo.GetByHandle(ctx, handle)
	if err != nil || targetUser == nil {
		return fmt.Errorf("user not found: %s", handle)
	}

	// Check if already a member
	existingRole, _ := s.nsRepo.GetMemberRole(ctx, ns.ID, targetUser.ID)
	if existingRole != "" {
		return fmt.Errorf("user is already a member")
	}

	member := &model.NamespaceMember{
		ID:          uuid.New(),
		NamespaceID: ns.ID,
		UserID:      targetUser.ID,
		Role:        role,
	}
	return s.nsRepo.AddMember(ctx, member)
}

// RemoveMember removes a user from a namespace.
func (s *NamespaceService) RemoveMember(ctx context.Context, actor *model.User, nsSlug, handle string) error {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return fmt.Errorf("namespace not found")
	}

	actorRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, actor.ID)
	if err != nil {
		return err
	}
	if actorRole != "owner" && actorRole != "admin" && !actor.IsAdmin() {
		return fmt.Errorf("%w: only owner or admin can manage members", ErrForbidden)
	}

	targetUser, err := s.userRepo.GetByHandle(ctx, handle)
	if err != nil || targetUser == nil {
		return fmt.Errorf("user not found: %s", handle)
	}

	// Cannot remove the owner
	targetRole, _ := s.nsRepo.GetMemberRole(ctx, ns.ID, targetUser.ID)
	if targetRole == "owner" {
		return fmt.Errorf("cannot remove the owner from the namespace")
	}

	// Cascade-revoke target's namespace-bound team tokens BEFORE removing
	// membership. Order matters: if revoke errors out, membership is unchanged
	// and the caller can retry — the second revoke is a no-op (UPDATE filters
	// revoked_at IS NULL). If we removed first then revoked, a transient revoke
	// failure would leave orphan tokens whose owner has already lost access.
	var revokedCount int64
	if s.tokenRepo != nil {
		n, err := s.tokenRepo.RevokeByNamespaceAndUser(ctx, ns.ID, targetUser.ID)
		if err != nil {
			return fmt.Errorf("cascade-revoke team tokens: %w", err)
		}
		revokedCount = n
	}

	if err := s.nsRepo.RemoveMember(ctx, ns.ID, targetUser.ID); err != nil {
		return err
	}

	// Audit AFTER both side effects succeed — a Log row that says "we revoked N
	// tokens" should never be present if the membership row didn't actually go
	// away. Skipping when count==0 keeps the audit trail focused on real impact.
	if revokedCount > 0 {
		if s.auditSvc != nil {
			nsID := ns.ID
			s.auditSvc.Log(ctx, &actor.ID, "team_token_cascade_revoke", "namespace", &nsID,
				fmt.Sprintf("cause=member_remove,target=%s,count=%d", targetUser.Handle, revokedCount), "")
		}
		s.metricsOrDefault().TeamTokenRevoked.
			WithLabelValues("cascade_member_remove").Add(float64(revokedCount))
	}
	return nil
}

// ListMembers returns all members of a namespace.
func (s *NamespaceService) ListMembers(ctx context.Context, nsSlug string) ([]model.NamespaceMemberWithUser, error) {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return nil, fmt.Errorf("namespace not found")
	}
	return s.nsRepo.ListMembers(ctx, ns.ID)
}

// ListMemberIDs returns all member user IDs for a namespace.
func (s *NamespaceService) ListMemberIDs(ctx context.Context, nsID uuid.UUID) ([]uuid.UUID, error) {
	return s.nsRepo.ListMemberIDs(ctx, nsID)
}

// GetMemberRole returns the user's role in the namespace, or "" if not a member.
func (s *NamespaceService) GetMemberRole(ctx context.Context, nsID uuid.UUID, userID uuid.UUID) (string, error) {
	return s.nsRepo.GetMemberRole(ctx, nsID, userID)
}

// IsMemberOrAdmin checks if a user is a member of the namespace or a system admin.
func (s *NamespaceService) IsMemberOrAdmin(ctx context.Context, nsID uuid.UUID, user *model.User) bool {
	if user.IsAdmin() {
		return true
	}
	role, err := s.nsRepo.GetMemberRole(ctx, nsID, user.ID)
	if err != nil {
		return false
	}
	return role != ""
}

// CanPublish checks if a user can publish to a namespace.
// Readers cannot publish — only owner, admin, and member roles can.
func (s *NamespaceService) CanPublish(ctx context.Context, nsSlug string, userID uuid.UUID) (bool, error) {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return false, nil
	}
	role, err := s.nsRepo.GetMemberRole(ctx, ns.ID, userID)
	if err != nil {
		return false, err
	}
	return role != "" && role != "reader", nil
}

// CanManageTokens 判定 user 是否可在该 namespace 下创建/吊销/列出团队 token。
// 规则：
//   - 系统 admin 永远可以；
//   - namespace 内角色为 owner / admin 的成员可以；
//   - 普通 member 不可以——避免 invite-once-then-self-issue-token 升权。
func (s *NamespaceService) CanManageTokens(ctx context.Context, nsID uuid.UUID, user *model.User) bool {
	if user.IsAdmin() {
		return true
	}
	role, err := s.nsRepo.GetMemberRole(ctx, nsID, user.ID)
	if err != nil {
		return false
	}
	return role == "owner" || role == "admin"
}

// TransferOwnership transfers ownership of a namespace to another existing
// member. The current owner is demoted to "admin". Only the current owner
// (or a system admin) may initiate the transfer.
func (s *NamespaceService) TransferOwnership(ctx context.Context, actor *model.User, nsSlug, newOwnerHandle string) error {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return fmt.Errorf("namespace not found")
	}

	actorRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, actor.ID)
	if err != nil {
		return err
	}
	if actorRole != "owner" && !actor.IsAdmin() {
		return fmt.Errorf("%w: only the owner can transfer ownership", ErrForbidden)
	}

	target, err := s.userRepo.GetByHandle(ctx, newOwnerHandle)
	if err != nil || target == nil {
		return fmt.Errorf("user not found: %s", newOwnerHandle)
	}
	if target.ID == ns.OwnerID {
		return fmt.Errorf("user is already the owner")
	}

	targetRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, target.ID)
	if err != nil {
		return err
	}
	if targetRole == "" {
		return fmt.Errorf("user is not a member of this namespace; add them first")
	}

	return s.nsRepo.TransferOwnership(ctx, ns.ID, ns.OwnerID, target.ID)
}

// Leave removes the current user from a namespace.
// The owner cannot leave — they must transfer ownership or delete the namespace first.
func (s *NamespaceService) Leave(ctx context.Context, user *model.User, nsSlug string) error {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return fmt.Errorf("namespace not found")
	}

	role, err := s.nsRepo.GetMemberRole(ctx, ns.ID, user.ID)
	if err != nil {
		return err
	}
	if role == "" {
		return fmt.Errorf("you are not a member of this namespace")
	}
	if role == "owner" {
		return fmt.Errorf("the owner cannot leave; transfer ownership or delete the namespace first")
	}

	// Cascade-revoke caller's tokens — see RemoveMember for the ordering rationale.
	var revokedCount int64
	if s.tokenRepo != nil {
		n, err := s.tokenRepo.RevokeByNamespaceAndUser(ctx, ns.ID, user.ID)
		if err != nil {
			return fmt.Errorf("cascade-revoke team tokens: %w", err)
		}
		revokedCount = n
	}

	if err := s.nsRepo.RemoveMember(ctx, ns.ID, user.ID); err != nil {
		return err
	}

	if revokedCount > 0 {
		if s.auditSvc != nil {
			nsID := ns.ID
			s.auditSvc.Log(ctx, &user.ID, "team_token_cascade_revoke", "namespace", &nsID,
				fmt.Sprintf("cause=member_leave,count=%d", revokedCount), "")
		}
		s.metricsOrDefault().TeamTokenRevoked.
			WithLabelValues("cascade_member_leave").Add(float64(revokedCount))
	}
	return nil
}

// Delete deletes a namespace (owner only).
func (s *NamespaceService) Delete(ctx context.Context, user *model.User, nsSlug string) error {
	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return fmt.Errorf("namespace not found")
	}

	role, err := s.nsRepo.GetMemberRole(ctx, ns.ID, user.ID)
	if err != nil {
		return err
	}
	if role != "owner" && !user.IsAdmin() {
		return fmt.Errorf("%w: only the owner can delete a namespace", ErrForbidden)
	}

	// Cascade-revoke ALL tokens under this namespace before deleting it.
	// Same ordering as RemoveMember: revoke first so a transient revoke failure
	// does not leave dangling tokens whose namespace_id points to a row that
	// no longer exists (would only matter if there is no FK ON DELETE clause —
	// belt-and-suspenders).
	var revokedCount int64
	if s.tokenRepo != nil {
		n, err := s.tokenRepo.RevokeByNamespace(ctx, ns.ID)
		if err != nil {
			return fmt.Errorf("cascade-revoke team tokens: %w", err)
		}
		revokedCount = n
	}

	if err := s.nsRepo.Delete(ctx, ns.ID); err != nil {
		return err
	}

	// nsID captured before the namespace row went away — the AuditLog row
	// itself outlives the namespace and reviewers can correlate by id.
	if revokedCount > 0 {
		if s.auditSvc != nil {
			nsID := ns.ID
			s.auditSvc.Log(ctx, &user.ID, "team_token_cascade_revoke", "namespace", &nsID,
				fmt.Sprintf("cause=namespace_delete,count=%d", revokedCount), "")
		}
		s.metricsOrDefault().TeamTokenRevoked.
			WithLabelValues("cascade_namespace_delete").Add(float64(revokedCount))
	}
	return nil
}

// Invite creates a pending invitation for an existing user to join a namespace.
// Owner/admin only. Replaces direct AddMember for the consensual flow:
// the invitee must accept before they get any access.
func (s *NamespaceService) Invite(ctx context.Context, actor *model.User, nsSlug, inviteeHandle, role, message string) (*model.NamespaceInvitation, error) {
	if s.invRepo == nil {
		return nil, fmt.Errorf("invitation flow not configured on this server")
	}

	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return nil, fmt.Errorf("namespace not found")
	}

	actorRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, actor.ID)
	if err != nil {
		return nil, err
	}
	if actorRole != "owner" && actorRole != "admin" && !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: only owner or admin can invite members", ErrForbidden)
	}

	if role == "" {
		role = "member"
	}
	if role != "admin" && role != "member" && role != "reader" {
		return nil, fmt.Errorf("invited role must be 'admin', 'member', or 'reader'")
	}

	invitee, err := s.userRepo.GetByHandle(ctx, inviteeHandle)
	if err != nil || invitee == nil {
		return nil, fmt.Errorf("user not found: %s", inviteeHandle)
	}

	existingRole, _ := s.nsRepo.GetMemberRole(ctx, ns.ID, invitee.ID)
	if existingRole != "" {
		return nil, fmt.Errorf("user is already a member")
	}

	if existing, _ := s.invRepo.GetPending(ctx, ns.ID, invitee.ID); existing != nil {
		return nil, fmt.Errorf("an invitation is already pending for this user")
	}

	inv := &model.NamespaceInvitation{
		ID:            uuid.New(),
		NamespaceID:   ns.ID,
		InviterID:     actor.ID,
		InviteeID:     invitee.ID,
		InviteeHandle: invitee.Handle,
		Role:          role,
		Status:        "pending",
		ExpiresAt:     time.Now().Add(InvitationTTL),
	}
	if message != "" {
		inv.Message = &message
	}
	if err := s.invRepo.Create(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// ListInvitations returns invitations for a namespace, filtered by status (or all if status="").
// Owner/admin only.
func (s *NamespaceService) ListInvitations(ctx context.Context, actor *model.User, nsSlug, status string) ([]model.NamespaceInvitation, error) {
	if s.invRepo == nil {
		return nil, fmt.Errorf("invitation flow not configured on this server")
	}

	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return nil, fmt.Errorf("namespace not found")
	}

	actorRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, actor.ID)
	if err != nil {
		return nil, err
	}
	if actorRole != "owner" && actorRole != "admin" && !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: only owner or admin can view invitations", ErrForbidden)
	}

	return s.invRepo.ListByNamespace(ctx, ns.ID, status)
}

// RevokeInvitation cancels a pending invitation. Owner/admin only.
func (s *NamespaceService) RevokeInvitation(ctx context.Context, actor *model.User, nsSlug string, invID uuid.UUID) error {
	if s.invRepo == nil {
		return fmt.Errorf("invitation flow not configured on this server")
	}

	ns, err := s.nsRepo.GetBySlug(ctx, nsSlug)
	if err != nil || ns == nil {
		return fmt.Errorf("namespace not found")
	}

	actorRole, err := s.nsRepo.GetMemberRole(ctx, ns.ID, actor.ID)
	if err != nil {
		return err
	}
	if actorRole != "owner" && actorRole != "admin" && !actor.IsAdmin() {
		return fmt.Errorf("%w: only owner or admin can revoke invitations", ErrForbidden)
	}

	inv, err := s.invRepo.GetByID(ctx, invID)
	if err != nil || inv == nil {
		return fmt.Errorf("invitation not found")
	}
	if inv.NamespaceID != ns.ID {
		return fmt.Errorf("invitation does not belong to this namespace")
	}
	if inv.Status != "pending" {
		return fmt.Errorf("invitation is not pending (status: %s)", inv.Status)
	}

	return s.invRepo.UpdateStatus(ctx, inv.ID, "revoked")
}

// ListMyInvitations returns the current user's pending invitations across all namespaces.
func (s *NamespaceService) ListMyInvitations(ctx context.Context, user *model.User) ([]model.NamespaceInvitation, error) {
	if s.invRepo == nil {
		return nil, fmt.Errorf("invitation flow not configured on this server")
	}
	return s.invRepo.ListByInvitee(ctx, user.ID)
}

// RespondToInvitation handles accept/decline by the invitee.
func (s *NamespaceService) RespondToInvitation(ctx context.Context, user *model.User, invID uuid.UUID, accept bool) error {
	if s.invRepo == nil {
		return fmt.Errorf("invitation flow not configured on this server")
	}

	inv, err := s.invRepo.GetByID(ctx, invID)
	if err != nil || inv == nil {
		return fmt.Errorf("invitation not found")
	}
	if inv.InviteeID != user.ID {
		return fmt.Errorf("%w: this invitation is not yours", ErrForbidden)
	}
	if inv.Status != "pending" {
		return fmt.Errorf("invitation is not pending (status: %s)", inv.Status)
	}
	if time.Now().After(inv.ExpiresAt) {
		_ = s.invRepo.UpdateStatus(ctx, inv.ID, "expired")
		return fmt.Errorf("invitation has expired")
	}

	if !accept {
		return s.invRepo.UpdateStatus(ctx, inv.ID, "declined")
	}

	// Defensive: avoid duplicate membership if a parallel AddMember slipped in.
	if existing, _ := s.nsRepo.GetMemberRole(ctx, inv.NamespaceID, inv.InviteeID); existing != "" {
		return s.invRepo.UpdateStatus(ctx, inv.ID, "accepted")
	}

	return s.invRepo.AcceptAndAddMember(ctx, inv)
}
