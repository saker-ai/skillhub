package service

import (
	"context"
	"log/slog"

	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
	"github.com/google/uuid"
)

type NotificationService struct {
	repo   *repository.NotificationRepo
	logger *slog.Logger
}

func NewNotificationService(repo *repository.NotificationRepo) *NotificationService {
	return &NotificationService{repo: repo}
}

// SetLogger 注入 *slog.Logger。nil 等价于走 slog.Default()。
func (s *NotificationService) SetLogger(lg *slog.Logger) {
	s.logger = lg
}

func (s *NotificationService) loggerOrDefault() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Notify creates a notification for a user. Errors are logged but not returned.
func (s *NotificationService) Notify(ctx context.Context, userID uuid.UUID, category, title, body, link string) {
	n := &model.Notification{
		ID:       uuid.New(),
		UserID:   userID,
		Category: category,
		Title:    title,
	}
	if body != "" {
		n.Body = &body
	}
	if link != "" {
		n.Link = &link
	}
	if err := s.repo.Create(ctx, n); err != nil {
		s.loggerOrDefault().Warn("notification: failed to create", "user_id", userID, "err", err)
	}
}

// List returns paginated notifications for a user.
func (s *NotificationService) List(ctx context.Context, userID uuid.UUID, limit int, cursor string) ([]model.Notification, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.repo.ListByUser(ctx, userID, limit, cursor)
}

// CountUnread returns the unread notification count.
func (s *NotificationService) CountUnread(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.repo.CountUnread(ctx, userID)
}

// MarkRead marks a single notification as read.
func (s *NotificationService) MarkRead(ctx context.Context, id, userID uuid.UUID) error {
	return s.repo.MarkRead(ctx, id, userID)
}

// MarkAllRead marks all notifications as read for a user.
func (s *NotificationService) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	return s.repo.MarkAllRead(ctx, userID)
}
