package service

import (
	"context"
	"fmt"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/google/uuid"
)

func (s *PluginService) SoftDelete(ctx context.Context, user *model.User, slug string) error {
	p, err := s.lookupOwnedPlugin(ctx, user, slug)
	if err != nil {
		return err
	}
	if err := s.pluginRepo.SoftDelete(ctx, p.ID); err != nil {
		return fmt.Errorf("delete plugin: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "delete", "plugin", &p.ID, "", "")
	}
	return nil
}

func (s *PluginService) Undelete(ctx context.Context, user *model.User, slug string) error {
	p, err := s.pluginRepo.GetBySlugIncludeDeleted(ctx, slug)
	if err != nil {
		return fmt.Errorf("plugin not found")
	}
	if p.OwnerID != user.ID && !user.IsAdmin() {
		return fmt.Errorf("%w: not the plugin owner", ErrForbidden)
	}
	if p.SoftDeletedAt == nil {
		return fmt.Errorf("%w: plugin is not deleted", ErrValidation)
	}
	if err := s.pluginRepo.Undelete(ctx, p.ID); err != nil {
		return fmt.Errorf("undelete plugin: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "undelete", "plugin", &p.ID, "", "")
	}
	return nil
}

func (s *PluginService) YankVersion(ctx context.Context, user *model.User, slug, version, reason string) error {
	p, ver, err := s.lookupOwnedPluginVersion(ctx, user, slug, version)
	if err != nil {
		return err
	}
	if ver.YankedAt != nil {
		return fmt.Errorf("%w: version %s is already yanked", ErrConflict, version)
	}
	if err := s.pluginRepo.SetYanked(ctx, ver.ID, reason); err != nil {
		return fmt.Errorf("yank version: %w", err)
	}
	if err := s.pluginRepo.RepointLatest(ctx, p.ID); err != nil {
		s.logger.Warn("failed to repoint latest after yank", "plugin", slug, "err", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "yank_version", "plugin_version", &ver.ID, reason, "")
	}
	return nil
}

func (s *PluginService) UnyankVersion(ctx context.Context, user *model.User, slug, version string) error {
	p, ver, err := s.lookupOwnedPluginVersion(ctx, user, slug, version)
	if err != nil {
		return err
	}
	if ver.YankedAt == nil {
		return fmt.Errorf("%w: version %s is not yanked", ErrValidation, version)
	}
	if err := s.pluginRepo.ClearYanked(ctx, ver.ID); err != nil {
		return fmt.Errorf("unyank version: %w", err)
	}
	if err := s.pluginRepo.RepointLatest(ctx, p.ID); err != nil {
		s.logger.Warn("failed to repoint latest after unyank", "plugin", slug, "err", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "unyank_version", "plugin_version", &ver.ID, "", "")
	}
	return nil
}

func (s *PluginService) ListAllForAdmin(ctx context.Context, limit int, cursor, visibility string) ([]model.PluginWithOwner, string, error) {
	return s.pluginRepo.ListAllForAdmin(ctx, limit, cursor, visibility)
}

func (s *PluginService) ReviewPlugin(ctx context.Context, reviewerID *uuid.UUID, slug string, approve bool) error {
	p, err := s.pluginRepo.GetBySlug(ctx, slug)
	if err != nil {
		return fmt.Errorf("plugin not found")
	}
	status := "rejected"
	visibility := p.Visibility
	if approve {
		status = "approved"
		visibility = "public"
	}
	if err := s.pluginRepo.SetVisibility(ctx, p.ID, visibility, status); err != nil {
		return fmt.Errorf("update plugin: %w", err)
	}
	if s.auditSvc != nil {
		action := "reject"
		if approve {
			action = "approve"
		}
		s.auditSvc.Log(ctx, reviewerID, action, "plugin", &p.ID, "", "")
	}
	return nil
}

func (s *PluginService) SetPluginVisibility(ctx context.Context, adminID *uuid.UUID, slug, visibility string) error {
	p, err := s.pluginRepo.GetBySlug(ctx, slug)
	if err != nil {
		return fmt.Errorf("plugin not found")
	}
	if visibility != "public" && visibility != "private" {
		return fmt.Errorf("%w: visibility must be 'public' or 'private'", ErrValidation)
	}
	if err := s.pluginRepo.SetVisibility(ctx, p.ID, visibility, "approved"); err != nil {
		return fmt.Errorf("update plugin: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, adminID, "set_visibility", "plugin", &p.ID, visibility, "")
	}
	return nil
}

func (s *PluginService) lookupOwnedPlugin(ctx context.Context, user *model.User, slug string) (*model.Plugin, error) {
	p, err := s.pluginRepo.GetBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("plugin not found")
	}
	if p.OwnerID != user.ID && !user.IsAdmin() {
		return nil, fmt.Errorf("%w: not the plugin owner", ErrForbidden)
	}
	return p, nil
}

func (s *PluginService) lookupOwnedPluginVersion(ctx context.Context, user *model.User, slug, version string) (*model.Plugin, *model.PluginVersion, error) {
	p, err := s.lookupOwnedPlugin(ctx, user, slug)
	if err != nil {
		return nil, nil, err
	}
	ver, err := s.pluginRepo.GetVersion(ctx, p.ID, version)
	if err != nil {
		return nil, nil, fmt.Errorf("version %s not found", version)
	}
	return p, ver, nil
}
