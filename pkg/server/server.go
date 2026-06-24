package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/saker-ai/skillhub/pkg/auth"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/gitstore"
	"github.com/saker-ai/skillhub/pkg/handler"
	"github.com/saker-ai/skillhub/pkg/metrics"
	"github.com/saker-ai/skillhub/pkg/middleware"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
	"github.com/saker-ai/skillhub/pkg/search"
	"github.com/saker-ai/skillhub/pkg/service"
	"github.com/saker-ai/skillhub/pkg/store"
	"gorm.io/gorm"
)

// Deps 是 NewWithDeps 接受的可选依赖集合。
//
// 阶段 2 引入：所有字段都允许 nil，nil 时回退到自动创建 / 全局默认值，
// 用于既保留独立二进制零参数启动行为，又允许嵌入方按需注入隔离实例。
type Deps struct {
	// DB 嵌入方注入的 *gorm.DB。nil 时由 SkillHub 按 cfg.Database 自行创建。
	DB *gorm.DB

	// Logger 嵌入方注入的 *slog.Logger。nil 时回退到 slog.Default()。
	Logger *slog.Logger

	// Metrics 嵌入方注入的 *metrics.Metrics 实例。nil 时使用 metrics.Default 单例。
	Metrics *metrics.Metrics

	// TablePrefix 仅当 DB == nil 时生效，传给 repository.NewDBWithOptions。
	// 嵌入方设置 "sh_" 等前缀可避免与宿主表撞名。
	TablePrefix string

	// IdentityProvider 阶段 5 引入：嵌入方可注入自定义身份解析。
	// nil 时回退到 SkillHub 默认 token-based 实现 (*auth.Service)。
	//
	// 注意：自定义 IdentityProvider 仅替换 RequireAuth/OptionalAuth 的解析逻辑——
	// /login、/api/v1/tokens 等管理 SkillHub 自己 token 表的端点仍然走内置 *auth.Service。
	// 嵌入方如果完全不需要 SkillHub 的 token 体系，可在 RegisterRoutes 之外
	// 手动屏蔽这些路由。
	IdentityProvider auth.IdentityProvider
}

// handlers 把所有 HTTP handler 实例打包，方便在 Server 上以单字段访问。
//
// 之所以用聚合 struct 而不是各自做 Server 的字段，是为了让 routes.go
// 通过 s.h.skill 这种短路径访问，避免在路由注册段反复重复 s.skillHandler 这类长名。
type handlers struct {
	skill     *handler.SkillHandler
	search    *handler.SearchHandler
	download  *handler.DownloadHandler
	auth      *handler.AuthHandler
	star      *handler.StarHandler
	admin     *handler.AdminHandler
	audit     *handler.AuditHandler
	token     *handler.TokenHandler
	namespace *handler.NamespaceHandler
	notif     *handler.NotificationHandler
	rating    *handler.RatingHandler
	comment   *handler.CommentHandler
	oauth     *handler.OAuthHandler
	device    *handler.DeviceAuthHandler
	webhook   *handler.WebhookHandler
	wellKnown *handler.WellKnownHandler
	agent     *handler.AgentHandler
	runtime    *handler.RuntimeSkillHandler
	agentSkill *handler.AgentSkillHandler
	webAuth   *handler.WebAuthHandler
	openapi   *handler.OpenAPIHandler
	plugin    *handler.PluginHandler
}

// Server 持有 SkillHub 完整的运行时依赖与 HTTP handler 集合。
//
// 阶段 4 重构后：构造函数只负责装配依赖与 handler，路由注册被抽到
// routes.go 的 RegisterRoutes / RegisterStatic 方法，由 Run() 或嵌入方按需调用。
type Server struct {
	cfg          *config.Config
	httpServer   *http.Server
	db           *gorm.DB
	searchClient *search.Client
	oauthSvc     *auth.OAuthService
	deviceSvc    *auth.DeviceAuthService
	rateLimiter  *middleware.RateLimiter
	logger       *slog.Logger
	metrics      *metrics.Metrics
	bgCtx        context.Context
	bgCancel     context.CancelFunc

	// 中间件 / NoRoute 等需要直接访问的服务依赖。
	authSvc   *auth.Service
	mirrorSvc *gitstore.MirrorService

	// idp 是 RequireAuth/OptionalAuth 中间件实际使用的身份解析器。
	// 默认指向 authSvc；嵌入方通过 Deps.IdentityProvider 注入时替换为自定义实现。
	idp auth.IdentityProvider

	h handlers
}

// New 是 NewWithDeps(cfg, Deps{}) 的兼容别名，行为与旧版完全一致。
//
// 独立二进制（cmd/skillhub serve）继续走这条路径——所有依赖按 cfg 自动创建，
// 全局 metrics.Default 与 slog.Default() 提供默认行为。
func New(cfg *config.Config) (*Server, error) {
	return NewWithDeps(cfg, Deps{})
}

// NewWithDeps 在 New 基础上接受外部依赖，用于嵌入到宿主进程的场景。
//
// 行为约定：
//   - deps.DB != nil → 直接使用，跳过 NewDB；TablePrefix 字段被忽略
//     （嵌入方应自行决定 NamingStrategy）。
//   - deps.Logger == nil → slog.Default()，与原行为一致。
//   - deps.Metrics == nil → metrics.Default 单例，与原行为一致。
//
// 阶段 4 起本函数不再创建 gin.Engine、不再注册任何路由，仅装配依赖与 handler。
// 路由由 Run() 或嵌入方调用 RegisterRoutes / RegisterStatic 完成。
func NewWithDeps(cfg *config.Config, deps Deps) (*Server, error) {
	// Resolve defaults — 缺省值与独立二进制行为完全一致。
	lg := deps.Logger
	if lg == nil {
		lg = slog.Default()
	}
	mx := deps.Metrics
	if mx == nil {
		mx = metrics.Default
	}

	// Database — 优先采用外部注入，否则按 cfg 自动创建。
	var db *gorm.DB
	var err error
	if deps.DB != nil {
		db = deps.DB
	} else {
		db, err = repository.NewDBWithOptions(cfg.Database, repository.DBOptions{
			TablePrefix: deps.TablePrefix,
		})
		if err != nil {
			return nil, fmt.Errorf("database: %w", err)
		}
	}

	// Auto-setup: create admin user from env vars if not exists
	autoSetup(db, lg)

	// Repositories
	userRepo := repository.NewUserRepo(db)
	skillRepo := repository.NewSkillRepo(db)
	versionRepo := repository.NewVersionRepo(db)
	tokenRepo := repository.NewTokenRepo(db)
	starRepo := repository.NewStarRepo(db)
	downloadRepo := repository.NewDownloadRepo(db)

	// Skill 查询缓存 — 命中 GetBySlug / GetBySlugOrAlias，省掉 JOIN users 的开销。
	// SkillCacheSize <= 0 → NewSkillCache 返回 nil → repo 自动绕过；不需要 if-guard。
	if cache := repository.NewSkillCache(
		cfg.Cache.SkillCacheSize,
		cfg.Cache.SkillCacheTTL,
		mx.SkillCacheHits,
		mx.SkillCacheMisses,
	); cache != nil {
		skillRepo.SetCache(cache)
	}

	// Git Store (always created — Mirror and Import depend on it)
	gs, err := gitstore.New(cfg.GitStore.BasePath)
	if err != nil {
		return nil, fmt.Errorf("gitstore: %w", err)
	}

	// File Store backend — 阶段 3 起改走 driver registry：
	//   cmd/skillhub blank-imports pkg/store/{git,s3,oss}，三种 backend
	//   通过各自的 init() 自注册到 store 包；cfg.Store.Backend == "" 默认走 git。
	// 嵌入方如果只需要其中部分 backend，可省略对应 blank import 以缩小二进制。
	fileStore, err := store.Open(cfg.Store.Backend, store.OpenContext{
		Cfg: cfg.Store,
		GS:  gs,
	})
	if err != nil {
		return nil, fmt.Errorf("file store: %w", err)
	}

	// Mirror Service
	mirrorSvc := gitstore.NewMirrorService(gs, cfg.GitStore.Mirror)
	mirrorSvc.SetLogger(lg)

	// Import Service
	importSvc := gitstore.NewImportService(gs, cfg.GitStore.Import)

	// Search (Bleve - embedded, always available)
	searchClient, err := search.New(cfg.Search)
	if err != nil {
		lg.Warn("search index unavailable, search will be disabled", "err", err)
		searchClient = nil
	}

	// Auth
	authSvc := auth.NewService(tokenRepo, userRepo)

	// IdentityProvider — 优先嵌入方注入，否则走默认 token-based 实现。
	idp := deps.IdentityProvider
	if idp == nil {
		if cfg.Auth.InternalAuth.Enabled {
			idp, err = auth.NewSynapseJWTIdentityProvider(auth.SynapseJWTConfig{
				Issuer:                     cfg.Auth.InternalAuth.Issuer,
				Audience:                   cfg.Auth.InternalAuth.Audience,
				MasterSecret:               cfg.Auth.InternalAuth.MasterSecret,
				TTL:                        cfg.Auth.InternalAuth.TTL,
				ClockSkew:                  cfg.Auth.InternalAuth.ClockSkew,
				AllowAuthorizationFallback: cfg.Auth.InternalAuth.AllowAuthorizationFallback,
			}, userRepo)
			if err != nil {
				return nil, err
			}
		} else {
			idp = authSvc
		}
	}

	// OAuth
	oauthRepo := repository.NewOAuthRepo(db)
	oauthSvc := auth.NewOAuthService(cfg.Auth.OAuth, oauthRepo, userRepo, authSvc, cfg.Server.BaseURL)

	// Device Auth
	deviceSvc := auth.NewDeviceAuthService(authSvc, cfg.Server.BaseURL)

	// OAuth org sync — enabled per-provider via config. We wire the hook
	// later after nsSvc is created, but set the flag now.
	oauthSyncOrgs := false
	for _, pcfg := range cfg.Auth.OAuth {
		if pcfg.SyncOrgs {
			oauthSyncOrgs = true
			break
		}
	}
	if oauthSyncOrgs {
		oauthSvc.SetSyncOrgs(true)
	}

	// Audit
	auditRepo := repository.NewAuditRepo(db)
	auditSvc := service.NewAuditService(auditRepo)
	auditSvc.SetLogger(lg)

	// Service — 注入 metrics 实例避免直接读包级全局。
	bgCtx, bgCancel := context.WithCancel(context.Background())

	skillSvc := service.NewSkillService(db, skillRepo, versionRepo, userRepo, downloadRepo, starRepo, fileStore, searchClient, mirrorSvc, auditSvc)
	skillSvc.SetBackgroundContext(bgCtx)
	skillSvc.SetMetrics(mx)
	skillSvc.SetLogger(lg)

	// Namespace
	nsRepo := repository.NewNamespaceRepo(db)
	nsInvRepo := repository.NewNamespaceInvitationRepo(db)
	nsSvc := service.NewNamespaceService(nsRepo, userRepo)
	nsSvc.SetInvitationRepo(nsInvRepo)
	// Cascade-revoke namespace-bound team tokens on member exit / namespace delete.
	// Without this wiring the orphan-token defense would rely solely on the
	// publish-time auth check (authorizeSkillWrite).
	nsSvc.SetTokenRepo(tokenRepo)
	// auditSvc is constructed above; wire it so cascade-revoke writes a row
	// reviewers can correlate with the membership/namespace mutation.
	nsSvc.SetAuditService(auditSvc)
	// Same metrics instance everywhere so /metrics aggregates correctly when
	// multiple skillhubs are embedded into one host (each gets its own *Metrics).
	nsSvc.SetMetrics(mx)
	skillSvc.SetNamespaceService(nsSvc)

	// Wire OAuth org sync hook now that nsSvc is available.
	if oauthSyncOrgs {
		oauthSvc.SetPostLoginHook(func(ctx context.Context, user *model.User, orgs []string) {
			for _, org := range orgs {
				if _, err := nsSvc.EnsureOrgNamespace(ctx, org, user.ID); err != nil {
					lg.Warn("oauth org sync failed", "org", org, "user", user.Handle, "err", err)
				}
			}
		})
	}

	// Notifications
	notifRepo := repository.NewNotificationRepo(db)
	notifSvc := service.NewNotificationService(notifRepo)
	notifSvc.SetLogger(lg)
	skillSvc.SetNotificationService(notifSvc)

	// Ratings
	ratingRepo := repository.NewRatingRepo(db)
	ratingSvc := service.NewRatingService(ratingRepo, skillRepo)
	ratingSvc.SetNamespaceService(nsSvc)

	// Comments
	commentRepo := repository.NewCommentRepo(db)
	commentSvc := service.NewCommentService(commentRepo, skillRepo, auditSvc)
	commentSvc.SetNamespaceService(nsSvc)

	// Rate Limiter
	rateLimiter := middleware.NewRateLimiter(cfg.RateLimit)

	// Plugin service
	pluginSvc := service.NewPluginService(db, repository.NewPluginRepo(db), fileStore, searchClient, auditSvc, lg)
	pluginSvc.SetNamespaceService(nsSvc)

	// Handlers — populated into Server.h, consumed by RegisterRoutes.
	searchHandler := handler.NewSearchHandler(searchClient)
	searchHandler.SetMetrics(mx)
	h := handlers{
		skill:     handler.NewSkillHandler(skillSvc),
		search:    searchHandler,
		download:  handler.NewDownloadHandler(skillSvc),
		auth:      handler.NewAuthHandler(authSvc, userRepo),
		star:      handler.NewStarHandler(skillSvc),
		admin:     handler.NewAdminHandler(userRepo, skillSvc, pluginSvc, auditSvc),
		audit:     handler.NewAuditHandler(auditSvc),
		token:     handler.NewTokenHandler(authSvc, tokenRepo, nsSvc),
		namespace: handler.NewNamespaceHandler(nsSvc),
		notif:     handler.NewNotificationHandler(notifSvc),
		rating:    handler.NewRatingHandler(ratingSvc),
		comment:   handler.NewCommentHandler(commentSvc),
		oauth:     handler.NewOAuthHandler(oauthSvc),
		device:    handler.NewDeviceAuthHandler(deviceSvc),
		webhook:   handler.NewWebhookHandler(importSvc),
		wellKnown: handler.NewWellKnownHandler(cfg),
		agent:     handler.NewAgentHandler(authSvc, userRepo),
		runtime:    handler.NewRuntimeSkillHandler(skillSvc),
		agentSkill: handler.NewAgentSkillHandler(skillSvc),
		webAuth:   handler.NewWebAuthHandler(authSvc),
		openapi:   handler.NewOpenAPIHandler(),
		plugin:    handler.NewPluginHandler(pluginSvc),
	}

	// Wire optional dependencies into handlers AFTER the literal — keeps
	// NewTokenHandler's signature stable (matches the SetInvitationRepo /
	// SetTokenRepo pattern). Team-token create/revoke without this hook still
	// work, just no audit row is emitted.
	h.token.SetAuditService(auditSvc)
	h.token.SetMetrics(mx)
	// Logger injection mirrors metrics: handler 层之前各自走 slog.Default(),
	// 现在统一走 Deps.Logger,让宿主进程注入的结构化 handler 也能拿到这些事件。
	h.skill.SetLogger(lg)
	h.download.SetLogger(lg)

	// Mirror push on startup if configured
	if mirrorSvc.Enabled() && cfg.GitStore.Mirror.PushOnStartup {
		go func() {
			if err := mirrorSvc.PushAll(bgCtx); err != nil {
				lg.Error("mirror push on startup failed", "err", err)
			}
		}()
	}

	// Rebuild search index on startup to backfill namespace data for
	// existing documents. Async to avoid blocking server readiness.
	if searchClient != nil {
		go func() {
			n, err := skillSvc.ReindexAll(bgCtx)
			if err != nil {
				lg.Error("search reindex on startup failed", "err", err)
			} else if n > 0 {
				lg.Info("search reindex complete", "indexed", n)
			}
		}()
	}

	return &Server{
		cfg:          cfg,
		db:           db,
		searchClient: searchClient,
		oauthSvc:     oauthSvc,
		deviceSvc:    deviceSvc,
		rateLimiter:  rateLimiter,
		logger:       lg,
		metrics:      mx,
		bgCtx:        bgCtx,
		bgCancel:     bgCancel,
		authSvc:      authSvc,
		mirrorSvc:    mirrorSvc,
		idp:          idp,
		h:            h,
	}, nil
}

// Run 创建默认 gin.Engine（含基础中间件）、注册全部路由（API + 静态）、
// 在 cfg.Server.Host:Port 监听并阻塞直到出错。
//
// 嵌入方一般不调用 Run，而是：
//
//	hub.RegisterRoutes(myEngine)        // API + auth + health
//	hub.RegisterStatic(myEngine)        // 仅当需要 SkillHub 自带的 SPA 时
//	myEngine.Run(...)                   // 由宿主自己监听
func (s *Server) Run() error {
	registration := startServiceDiscovery(context.Background(), s.logger, s.cfg)
	defer func() {
		if registration != nil {
			_ = registration.Stop(context.Background())
		}
	}()
	engine := s.NewDefaultEngine()

	var routeRoot gin.IRouter = engine
	if bp := s.cfg.Server.BasePath; bp != "" {
		routeRoot = engine.Group(bp)
	}
	s.RegisterRoutes(routeRoot)
	s.RegisterStatic(engine)

	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return s.httpServer.ListenAndServe()
}

// NewDefaultEngine 创建一个预装基础中间件（Recovery / RequestID / Logging /
// Metrics / SecurityHeaders）的 gin.Engine。
//
// 嵌入方如果想用自己的 engine 但又想复用相同的中间件栈，可以调用本方法，
// 然后再 .Use(自己的中间件)；也可以完全自建 engine 跳过本方法。
func (s *Server) NewDefaultEngine() *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(middleware.RequestID())
	router.Use(middleware.LoggingWith(s.logger))
	router.Use(middleware.MetricsWith(s.metrics))
	router.Use(middleware.SecurityHeaders())
	return router
}

// Shutdown 不是幂等的——deviceSvc/oauthSvc/rateLimiter 内部 close(channel)
// 如果二次执行会 panic。"只关一次"由上层 skillhub.Hub.Close() 的 sync.Once 兜底；
// 直接复用 Server 的嵌入方需自行保证不重复调用本方法。
func (s *Server) Shutdown(ctx context.Context) error {
	var firstErr error
	if s.bgCancel != nil {
		s.bgCancel()
	}
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if s.deviceSvc != nil {
		s.deviceSvc.Close()
	}
	if s.oauthSvc != nil {
		s.oauthSvc.Close()
	}
	if s.rateLimiter != nil {
		s.rateLimiter.Close()
	}
	if s.searchClient != nil {
		if err := s.searchClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.db != nil {
		if sqlDB, err := s.db.DB(); err == nil {
			if err := sqlDB.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// autoSetup creates an admin user on first startup if SKILLHUB_ADMIN_USER
// and SKILLHUB_ADMIN_PASSWORD environment variables are set.
//
// lg 不能为 nil — 调用方 NewWithDeps 已在前面 resolve 出 slog.Default() 兜底。
// 走 slog 而不是包级 log 是为了让宿主进程注入的 handler（JSON / leveled /
// 带 trace_id 的 wrapper）能拿到这些事件，否则 admin 创建/失败信息会绕过
// 嵌入方的日志管线落到 stderr。
func autoSetup(db *gorm.DB, lg *slog.Logger) {
	handle := os.Getenv("SKILLHUB_ADMIN_USER")
	password := os.Getenv("SKILLHUB_ADMIN_PASSWORD")
	if handle == "" || password == "" {
		return
	}

	ctx := context.Background()
	userRepo := repository.NewUserRepo(db)

	// Check if user already exists
	existing, _ := userRepo.GetByHandle(ctx, handle)
	if existing != nil {
		return
	}

	// Create admin user
	user := &model.User{
		ID:     uuid.New(),
		Handle: handle,
		Role:   "admin",
	}
	if err := userRepo.Create(ctx, user); err != nil {
		lg.Error("auto-setup: failed to create admin user", "handle", handle, "err", err)
		return
	}

	// Set password
	tokenRepo := repository.NewTokenRepo(db)
	authSvc := auth.NewService(tokenRepo, userRepo)
	if err := authSvc.SetPassword(ctx, user.ID, password); err != nil {
		lg.Error("auto-setup: failed to set admin password", "handle", handle, "err", err)
		return
	}

	lg.Info("auto-setup: created admin user", "handle", handle)
}
