package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/google/uuid"
)

const (
	maxCommentLen = 2000 // characters; matches GitHub issue comment cap roughly
)

type CommentService struct {
	commentRepo *repository.CommentRepo
	skillRepo   *repository.SkillRepo
	auditSvc    *AuditService
}

func NewCommentService(commentRepo *repository.CommentRepo, skillRepo *repository.SkillRepo, auditSvc *AuditService) *CommentService {
	return &CommentService{
		commentRepo: commentRepo,
		skillRepo:   skillRepo,
		auditSvc:    auditSvc,
	}
}

// Create posts a new comment on a skill. The skill must be visible to the
// commenter (private skills only allow owner/admin/moderator comments).
func (s *CommentService) Create(ctx context.Context, user *model.User, slug, body string) (*model.Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("comment body is required")
	}
	if len(body) > maxCommentLen {
		return nil, fmt.Errorf("comment exceeds %d characters", maxCommentLen)
	}
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return nil, fmt.Errorf("skill not found")
	}
	if !canViewSkill(skill, user) {
		return nil, fmt.Errorf("skill not found")
	}
	c := &model.Comment{
		ID:      uuid.New(),
		SkillID: skill.ID,
		UserID:  user.ID,
		Body:    body,
	}
	if err := s.commentRepo.Create(ctx, c); err != nil {
		return nil, fmt.Errorf("create comment: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "comment_create", "skill", &skill.ID, "", "")
	}
	return c, nil
}

// List returns paginated comments for a skill (newest first).
func (s *CommentService) List(ctx context.Context, viewer *model.User, slug string, limit int, cursor string) ([]model.CommentWithUser, string, error) {
	skill, err := s.skillRepo.GetBySlugOrAlias(ctx, slug)
	if err != nil || skill == nil {
		return nil, "", fmt.Errorf("skill not found")
	}
	if !canViewSkill(skill, viewer) {
		return nil, "", fmt.Errorf("skill not found")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.commentRepo.ListBySkill(ctx, skill.ID, limit, cursor)
}

// Delete soft-deletes a comment. Only the comment author, the skill owner,
// or an admin/moderator may delete.
func (s *CommentService) Delete(ctx context.Context, user *model.User, id uuid.UUID) error {
	c, err := s.commentRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("comment not found")
	}
	skill, err := s.skillRepo.GetByID(ctx, c.SkillID)
	if err != nil || skill == nil {
		return fmt.Errorf("skill not found")
	}
	if c.UserID != user.ID && skill.OwnerID != user.ID && !user.IsModerator() {
		return fmt.Errorf("forbidden")
	}
	if err := s.commentRepo.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}
	if s.auditSvc != nil {
		s.auditSvc.Log(ctx, &user.ID, "comment_delete", "comment", &id, "", "")
	}
	return nil
}
