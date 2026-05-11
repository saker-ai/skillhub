package service

import (
	"context"
	"log/slog"

	"github.com/cinience/skillhub/pkg/model"
	"github.com/cinience/skillhub/pkg/repository"
	"github.com/google/uuid"
)

type AuditService struct {
	repo   *repository.AuditRepo
	logger *slog.Logger
}

func NewAuditService(repo *repository.AuditRepo) *AuditService {
	return &AuditService{repo: repo}
}

// SetLogger 注入 *slog.Logger。nil 等价于走 slog.Default()。
// 必须在 Server 装配阶段调用；运行期切换不被支持。
func (s *AuditService) SetLogger(lg *slog.Logger) {
	s.logger = lg
}

func (s *AuditService) loggerOrDefault() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Log records an audit event. Errors are logged but not returned to avoid
// breaking the caller's flow for a non-critical side effect.
func (s *AuditService) Log(ctx context.Context, actorID *uuid.UUID, action, resourceType string, resourceID *uuid.UUID, details, ipAddress string) {
	entry := &model.AuditLog{
		ID:           uuid.New(),
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
	}
	if details != "" {
		entry.Details = &details
	}
	if ipAddress != "" {
		entry.IPAddress = &ipAddress
	}
	if err := s.repo.Create(ctx, entry); err != nil {
		s.loggerOrDefault().Warn("audit: failed to log", "action", action, "resource_type", resourceType, "err", err)
	}
}

// List returns paginated audit logs with optional filters.
func (s *AuditService) List(ctx context.Context, limit int, cursor string, filter repository.AuditFilter) ([]model.AuditLog, string, error) {
	return s.repo.List(ctx, limit, cursor, filter)
}
