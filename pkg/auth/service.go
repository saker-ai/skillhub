package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	tokenRepo *repository.TokenRepo
	userRepo  *repository.UserRepo
}

func NewService(tokenRepo *repository.TokenRepo, userRepo *repository.UserRepo) *Service {
	return &Service{tokenRepo: tokenRepo, userRepo: userRepo}
}

// CreateToken generates a new personal API token for a user.
//
// scope: "full" (default), "read", or "publish".
// expiresIn: optional duration; if zero, the token never expires.
//
// 用 personal token 调用写操作时，作者身份 = userID。需要把 token 绑定到团队
// (namespace) 时改用 CreateNamespaceToken。
func (s *Service) CreateToken(ctx context.Context, userID uuid.UUID, label, scope string, expiresIn time.Duration) (string, *model.APIToken, error) {
	return s.createToken(ctx, userID, nil, label, scope, expiresIn)
}

// CreateNamespaceToken 在 CreateToken 基础上把 token 绑定到指定 namespace。
//
// 调用方负责先校验 userID 在该 namespace 内具备 owner/admin 角色——本服务层只
// 落库，不做权限判断。
//
// 行为约束（在 SkillService.PublishVersion / 删除 / yank 等写路径强制执行）：
//   - 写操作的目标 skill 必须挂在 namespaceID 下，否则一律拒绝；
//   - 读操作不受 namespace 限制，遵循 skill.visibility 即可；
//   - token 的 author 仍是创建该 token 的 userID，便于审计追责。
func (s *Service) CreateNamespaceToken(ctx context.Context, userID, namespaceID uuid.UUID, label, scope string, expiresIn time.Duration) (string, *model.APIToken, error) {
	return s.createToken(ctx, userID, &namespaceID, label, scope, expiresIn)
}

func (s *Service) createToken(ctx context.Context, userID uuid.UUID, namespaceID *uuid.UUID, label, scope string, expiresIn time.Duration) (string, *model.APIToken, error) {
	rawToken, prefix, tokenHash, err := GenerateToken("")
	if err != nil {
		return "", nil, err
	}

	if scope == "" {
		scope = "full"
	}
	if scope != "full" && scope != "read" && scope != "publish" {
		return "", nil, fmt.Errorf("invalid scope: %s", scope)
	}

	token := &model.APIToken{
		ID:          uuid.New(),
		UserID:      userID,
		NamespaceID: namespaceID,
		Label:       &label,
		Prefix:      prefix,
		TokenHash:   tokenHash,
		Scope:       scope,
	}

	if expiresIn > 0 {
		exp := time.Now().Add(expiresIn)
		token.ExpiresAt = &exp
	}

	if err := s.tokenRepo.Create(ctx, token); err != nil {
		return "", nil, err
	}

	return rawToken, token, nil
}

// ValidateToken validates a raw token and returns the associated user, scope and
// optional namespace binding.
//
// 第三个返回值是 token 绑定的 namespaceID：
//   - nil：personal token，写操作不受 namespace 约束。
//   - non-nil：team token，调用方应在写路径上把它当成"必须命中此 namespace 的硬约束"。
func (s *Service) ValidateToken(ctx context.Context, rawToken string) (*model.User, string, *uuid.UUID, error) {
	prefix := ExtractPrefix(rawToken)
	tokens, err := s.tokenRepo.GetByPrefix(ctx, prefix)
	if err != nil {
		return nil, "", nil, err
	}

	tokenHash := HashToken(rawToken)

	for _, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(t.TokenHash), []byte(tokenHash)) == 1 {
			// Check expiration
			if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
				continue
			}

			// Update last used (fire and forget) —— 写失败只丢一次时间戳，下次仍可恢复，
			// 不希望它阻塞 hot-path 的 token 校验。
			go func(id uuid.UUID) { _ = s.tokenRepo.UpdateLastUsed(context.Background(), id) }(t.ID)

			user, err := s.userRepo.GetByID(ctx, t.UserID)
			if err != nil {
				return nil, "", nil, err
			}
			if user == nil || user.IsBanned {
				return nil, "", nil, nil
			}
			scope := t.Scope
			if scope == "" {
				scope = "full"
			}
			return user, scope, t.NamespaceID, nil
		}
	}

	return nil, "", nil, nil
}

// HashPassword hashes a plaintext password using bcrypt.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword verifies a plaintext password against a bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Login validates a handle+password, and returns a new API token for the session.
func (s *Service) Login(ctx context.Context, handle, password string) (string, *model.User, error) {
	user, err := s.userRepo.GetByHandle(ctx, handle)
	if err != nil {
		return "", nil, err
	}
	if user == nil {
		return "", nil, errors.New("invalid username or password")
	}
	if user.IsBanned {
		return "", nil, errors.New("account is banned")
	}
	if user.PasswordHash == nil || *user.PasswordHash == "" {
		return "", nil, errors.New("password not set, contact an admin")
	}
	if !CheckPassword(*user.PasswordHash, password) {
		return "", nil, errors.New("invalid username or password")
	}

	// Create a session token (30-day expiry)
	label := "web-session"
	rawToken, _, err := s.CreateToken(ctx, user.ID, label, "full", 30*24*time.Hour)
	if err != nil {
		return "", nil, err
	}

	return rawToken, user, nil
}

// SetPassword sets a user's password hash after validating complexity.
func (s *Service) SetPassword(ctx context.Context, userID uuid.UUID, password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.userRepo.SetPassword(ctx, userID, hash)
}
