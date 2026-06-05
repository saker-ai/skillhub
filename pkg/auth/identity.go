package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/google/uuid"
)

// IdentityProvider 解析当前 HTTP 请求背后的调用者身份。
//
// SkillHub 默认实现 (*Service) 从 Authorization: Bearer 或 session_token
// cookie 取出 token，再到 tokens 表里查 user。嵌入方可以注入自己的
// IdentityProvider 来对接宿主进程已有的鉴权体系，例如：
//
//   - 宿主已经把用户写到 gin.Context 里 → 直接读出来；
//   - 宿主用 OIDC / JWT / mTLS → 自己解析后构造 *model.User；
//   - 多租户：根据 X-Tenant 头切换 user 来源。
//
// 实现必须并发安全。
//
// 返回值约定：
//   - (user != nil, scope, namespaceID, nil)：身份解析成功；
//     scope 为 "" 或 "full" 时视为不限范围，其他值由 middleware.RequireScope 校验。
//     namespaceID != nil 表示该身份只允许在该 namespace 下做写操作（团队 token）。
//   - (nil, "", nil, nil)：未携带身份凭据。RequireAuth 返回 401；OptionalAuth 放行。
//   - (nil, "", nil, err)：解析过程出错（DB 异常等）。两种中间件都按"未认证"处理，
//     不向调用方暴露错误细节——日志由调用侧自行记录。
//
// 嵌入方实现：宿主鉴权体系通常没有 namespace 概念，直接返回 nil 即可——这相当于
// 宿主放弃 SkillHub 的团队 token 约束，调用方权限只受 user.Role / namespace 成员
// 关系控制。
type IdentityProvider interface {
	Identify(ctx context.Context, r *http.Request) (user *model.User, scope string, namespaceID *uuid.UUID, err error)
}

// Identify 在 *Service 上实现 IdentityProvider 接口。
//
// 解析顺序与历史 middleware.extractUser 完全一致：
//  1. Authorization: Bearer <token>
//  2. session_token cookie
//
// 任一处取到合法 token 即返回，scope 为该 token 的 scope（"full" 兜底），
// namespaceID 透传 token 上的绑定字段（personal token 为 nil）。
// 两处都没取到时返回 (nil, "", nil, nil)。
func (s *Service) Identify(ctx context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	if header := r.Header.Get("Authorization"); header != "" {
		if token := strings.TrimPrefix(header, "Bearer "); token != header && token != "" {
			user, scope, nsID, err := s.ValidateToken(ctx, token)
			if err != nil {
				return nil, "", nil, err
			}
			if user != nil {
				return user, scope, nsID, nil
			}
		}
	}

	if cookie, err := r.Cookie("session_token"); err == nil && cookie.Value != "" {
		user, scope, nsID, err := s.ValidateToken(ctx, cookie.Value)
		if err != nil {
			return nil, "", nil, err
		}
		if user != nil {
			return user, scope, nsID, nil
		}
	}

	return nil, "", nil, nil
}
