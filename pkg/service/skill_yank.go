package service

import (
	"context"
	"fmt"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/google/uuid"
)

// NOTE: All public methods in this file take model.SkillRef instead of slug string.

// YankVersion marks a version as yanked. Yanked versions remain installable
// by exact pin (so existing lockfiles keep working) but are excluded from
// "latest" resolution. After yank, the skill's latest pointer is repointed
// to the most recent non-yanked version.
//
// 写权限：tokenNS 非 nil ⇒ 必须 skill 隶属同 namespace；nil ⇒ owner 或系统 admin。
func (s *SkillService) YankVersion(ctx context.Context, user *model.User, ref model.SkillRef, version, reason string, tokenNS *uuid.UUID) error {
	skill, ver, err := s.lookupOwnedVersion(ctx, user, ref, version, tokenNS)
	if err != nil {
		return err
	}
	if ver.YankedAt != nil {
		return fmt.Errorf("version is already yanked")
	}
	if err := s.versionRepo.SetYanked(ctx, ver.ID, true, reason); err != nil {
		return fmt.Errorf("yank version: %w", err)
	}
	if err := s.repointLatest(ctx, skill.ID, ver.ID); err != nil {
		return fmt.Errorf("repoint latest: %w", err)
	}
	// repointLatest 改了 latest_version_id,失效缓存让 GetBySlug 拿到新值。
	s.invalidateSkillCache(skill)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "yank", "skill_version", &ver.ID, "", reason)
	}
	return nil
}

// UnyankVersion clears the yank flag, making the version eligible for latest
// resolution again. tokenNS 语义同 YankVersion。
func (s *SkillService) UnyankVersion(ctx context.Context, user *model.User, ref model.SkillRef, version string, tokenNS *uuid.UUID) error {
	skill, ver, err := s.lookupOwnedVersion(ctx, user, ref, version, tokenNS)
	if err != nil {
		return err
	}
	if ver.YankedAt == nil {
		return fmt.Errorf("version is not yanked")
	}
	if err := s.versionRepo.SetYanked(ctx, ver.ID, false, ""); err != nil {
		return fmt.Errorf("unyank version: %w", err)
	}
	// Re-evaluate latest in case this version is now the newest non-yanked one.
	if err := s.repointLatest(ctx, skill.ID, uuid.Nil); err != nil {
		return fmt.Errorf("repoint latest: %w", err)
	}
	s.invalidateSkillCache(skill)
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "unyank", "skill_version", &ver.ID, "", "")
	}
	return nil
}

// DeprecateVersion attaches a deprecation notice. The version still resolves
// as latest if it would otherwise — deprecation is advisory, not exclusion.
func (s *SkillService) DeprecateVersion(ctx context.Context, user *model.User, ref model.SkillRef, version, message string, tokenNS *uuid.UUID) error {
	_, ver, err := s.lookupOwnedVersion(ctx, user, ref, version, tokenNS)
	if err != nil {
		return err
	}
	if err := s.versionRepo.SetDeprecated(ctx, ver.ID, true, message); err != nil {
		return fmt.Errorf("deprecate version: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "deprecate", "skill_version", &ver.ID, "", message)
	}
	return nil
}

// UndeprecateVersion clears the deprecation notice.
func (s *SkillService) UndeprecateVersion(ctx context.Context, user *model.User, ref model.SkillRef, version string, tokenNS *uuid.UUID) error {
	_, ver, err := s.lookupOwnedVersion(ctx, user, ref, version, tokenNS)
	if err != nil {
		return err
	}
	if ver.DeprecatedAt == nil {
		return fmt.Errorf("version is not deprecated")
	}
	if err := s.versionRepo.SetDeprecated(ctx, ver.ID, false, ""); err != nil {
		return fmt.Errorf("undeprecate version: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "undeprecate", "skill_version", &ver.ID, "", "")
	}
	return nil
}

// lookupOwnedVersion resolves slug+version and verifies the actor may write,
// taking into account both legacy ownership and team-token namespace scoping.
func (s *SkillService) lookupOwnedVersion(ctx context.Context, user *model.User, ref model.SkillRef, version string, tokenNS *uuid.UUID) (*model.SkillWithOwner, *model.SkillVersion, error) {
	skill, err := s.resolveSkillRef(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if skill == nil {
		return nil, nil, fmt.Errorf("skill not found")
	}
	if err := s.authorizeSkillWrite(ctx, skill.NamespaceID, skill.OwnerID, user, tokenNS); err != nil {
		return nil, nil, err
	}
	ver, err := s.versionRepo.GetBySkillAndVersion(ctx, skill.ID, version)
	if err != nil || ver == nil {
		return nil, nil, fmt.Errorf("version not found")
	}
	return skill, ver, nil
}

// repointLatest sets skill.latest_version_id to the most recent non-yanked
// version. If invalidatedID matches the current latest, the recompute always
// happens. Pass uuid.Nil to recompute unconditionally.
func (s *SkillService) repointLatest(ctx context.Context, skillID, invalidatedID uuid.UUID) error {
	newLatest, err := s.versionRepo.GetLatestNonYanked(ctx, skillID)
	if err != nil {
		return err
	}
	var newID uuid.UUID
	if newLatest != nil {
		newID = newLatest.ID
	}
	return s.skillRepo.SetLatestVersion(ctx, skillID, newID)
}
