package skillhub

import (
	"fmt"
	"log/slog"

	"github.com/saker-ai/skillhub/pkg/auth"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// options 汇总了构造 Hub 时可注入的所有可选项。
// 字段保持包内可见即可，对外使用 Functional Options 进行写入。
//
// 阶段 2 扩展：除了 Config 之外，新增 BYO（bring-your-own）字段，
// 允许嵌入方注入自己的 DB/Logger/Metrics 实例，避免与宿主进程争用资源。
type options struct {
	cfg *config.Config

	// db 嵌入方注入的 *gorm.DB。nil 时由 SkillHub 按 cfg.Database 自行创建。
	db *gorm.DB

	// logger 嵌入方注入的 *slog.Logger。nil 时回退到 slog.Default()。
	logger *slog.Logger

	// metrics 嵌入方注入的 *metrics.Metrics 实例。nil 时使用 metrics.Default 单例。
	metrics *metrics.Metrics

	// tablePrefix 仅当 db == nil 时生效，传给 repository.NewDBWithOptions。
	// 嵌入方设置 "sh_" 等前缀可避免与宿主表撞名。
	tablePrefix string

	// identityProvider 阶段 5 引入：嵌入方注入的自定义身份解析器。
	// nil 时回退到 SkillHub 默认 token-based 实现 (*auth.Service)。
	identityProvider auth.IdentityProvider
}

// Option 是配置 Hub 的函数选项。
type Option func(*options)

// defaultOptions 返回带默认值的 options 集合。
// 默认值与 cmd/skillhub serve 在没有任何额外参数时的行为对齐。
func defaultOptions() *options {
	return &options{
		cfg: config.DefaultConfig(),
	}
}

// validate 在 New 中真正构造 Hub 之前做最小必要校验。
// 阶段 2 仍只校验 Config 不为 nil；其余字段允许 nil 并各自走默认值。
func (o *options) validate() error {
	if o.cfg == nil {
		return fmt.Errorf("skillhub: config is nil")
	}
	return nil
}

// WithConfig 注入完整 Config 对象（整体覆盖默认值）。
//
// 传入 nil 时静默忽略，保留默认配置——避免调用方因传入零值导致 New 失败。
func WithConfig(cfg *config.Config) Option {
	return func(o *options) {
		if cfg != nil {
			o.cfg = cfg
		}
	}
}

// WithConfigFile 从 YAML 文件加载配置。
//
// 文件读取失败时静默回退到既有配置——这是阶段 1 的兼容策略，
// 后续阶段会引入显式错误传递（例如 OptionWithError）。
func WithConfigFile(path string) Option {
	return func(o *options) {
		cfg, err := config.Load(path)
		if err == nil && cfg != nil {
			o.cfg = cfg
		}
	}
}

// WithDB 注入嵌入方已有的 *gorm.DB。
//
// 提供后 SkillHub 不会再调用 repository.NewDB，cfg.Database 与 WithTablePrefix
// 都会被忽略——嵌入方自行决定连接、命名策略、迁移行为。
//
// 传入 nil 时静默忽略，回退到自动创建。
func WithDB(db *gorm.DB) Option {
	return func(o *options) {
		if db != nil {
			o.db = db
		}
	}
}

// WithLogger 注入 *slog.Logger，用于 HTTP 中间件与 server 内部日志。
//
// 传入 nil 时静默忽略，最终回退到 slog.Default()。
func WithLogger(lg *slog.Logger) Option {
	return func(o *options) {
		if lg != nil {
			o.logger = lg
		}
	}
}

// WithMetrics 直接注入一个已构造的 *metrics.Metrics。
//
// 适合嵌入方需要在多个 SkillHub 实例之间共享同一组 collector 的场景。
// 与 WithMetricsRegistry 互斥——若两者都设置，最后一次调用胜出。
//
// 传入 nil 时静默忽略，最终回退到 metrics.Default 单例。
func WithMetrics(m *metrics.Metrics) Option {
	return func(o *options) {
		if m != nil {
			o.metrics = m
		}
	}
}

// WithMetricsRegistry 在指定的 prometheus.Registerer 上构造一组 SkillHub collector。
//
// 用于嵌入方使用自定义 prometheus.Registry（避免与宿主 DefaultRegisterer 冲突）。
// 传 nil 时回退到 prometheus.DefaultRegisterer——等价于 metrics.Default。
//
// 注意：metrics.New 在重复注册同名 collector 时会 panic；多次调用本 Option
// 而 reg 一致时会触发 panic，调用方需自行避免。
func WithMetricsRegistry(reg prometheus.Registerer) Option {
	return func(o *options) {
		o.metrics = metrics.New(reg)
	}
}

// WithTablePrefix 设置 GORM NamingStrategy 的表名前缀。
//
// 仅在未通过 WithDB 注入外部 *gorm.DB 时生效——SkillHub 自动创建 DB
// 时会把它传递给 repository.NewDBWithOptions。
//
// 例如设置 "sh_" 会让 users 表实际名 sh_users。空串等价于不设置前缀。
func WithTablePrefix(prefix string) Option {
	return func(o *options) {
		o.tablePrefix = prefix
	}
}

// WithIdentityProvider 注入嵌入方自定义的 auth.IdentityProvider，
// 用于替换 RequireAuth/OptionalAuth 中间件默认的 token-based 身份解析。
//
// 典型用法：宿主已经在自己的 gin 中间件里把用户写到 *http.Request.Context() 或
// gin.Context，WithIdentityProvider 让 SkillHub 路由复用同一份身份；
// 或者宿主用 OIDC/JWT/mTLS，自己解析后构造 *model.User 返回。
//
// 仅替换中间件解析逻辑——SkillHub 自带的 /login、/api/v1/tokens 等管理 SkillHub
// token 表的端点仍走内置 *auth.Service。如果完全不想暴露这些端点，嵌入方可以
// 不调用 RegisterRoutes，自己挑选需要的子路由挂载。
//
// 传入 nil 时静默忽略，回退到默认实现。
func WithIdentityProvider(p auth.IdentityProvider) Option {
	return func(o *options) {
		if p != nil {
			o.identityProvider = p
		}
	}
}
