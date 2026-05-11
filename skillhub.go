// Package skillhub 提供 SkillHub 注册中心的可嵌入 SDK API。
//
// 本包是 SkillHub 对外的稳定门面层，封装了内部 service / repository 等装配细节，
// 使其他 Go 项目可以直接以库的形式嵌入 SkillHub，而无需复制内部连线代码。
//
// 嵌入方使用示例：
//
//	hub, err := skillhub.New(ctx,
//	    skillhub.WithConfigFile("configs/skillhub.yaml"),
//	    skillhub.WithDB(myDB),
//	    skillhub.WithLogger(myLogger),
//	    skillhub.WithMetricsRegistry(myReg),
//	    skillhub.WithTablePrefix("sh_"),
//	)
//	if err != nil {
//	    return err
//	}
//	defer hub.Close()
//
//	// 嵌入方仅使用 SDK 时不需要调用 Run；
//	// 需要 HTTP 服务时调用 Run，由 ctx 控制生命周期。
//	if err := hub.Run(ctx); err != nil {
//	    return err
//	}
package skillhub

import (
	"context"
	"fmt"
	"sync"

	"github.com/cinience/skillhub/pkg/config"
	"github.com/cinience/skillhub/pkg/server"
	"github.com/gin-gonic/gin"
)

// Hub 是 SkillHub 的对外门面。
//
// 阶段 2 之后持有 Config 与底层 Server 引用，复用 server.NewWithDeps 装配链路；
// 后续阶段会逐步向本结构添加更细粒度的字段（svc、db、logger、metrics 等），
// 同时保持本文件中已有方法签名稳定。
type Hub struct {
	cfg    *config.Config
	server *server.Server // 阶段 4 之后会进一步拆分；阶段 2 直接复用

	// closeOnce 是 Hub 层的"只关一次"保证。Hub.Run 在 ctx.Done() 时会通过
	// Hub.Close 触发 shutdown，宿主进程 defer hub.Close() 也会调用——
	// 让 Close 自身幂等比让下游 Server.Shutdown 处处加防御更干净。
	closeOnce sync.Once
	closeErr  error
}

// New 创建一个 Hub 实例。Options 应用顺序决定优先级，后者覆盖前者。
//
// ctx 用于在装配阶段传播取消信号；阶段 2 实际不会向内部传播，
// 保留参数是为后续阶段（异步装配、上游探测等）预留接口稳定性。
func New(ctx context.Context, opts ...Option) (*Hub, error) {
	_ = ctx // 阶段 2 暂不使用，保留入参用于后续阶段

	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	if err := o.validate(); err != nil {
		return nil, err
	}

	srv, err := server.NewWithDeps(o.cfg, server.Deps{
		DB:               o.db,
		Logger:           o.logger,
		Metrics:          o.metrics,
		TablePrefix:      o.tablePrefix,
		IdentityProvider: o.identityProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("skillhub: create server: %w", err)
	}

	return &Hub{
		cfg:    o.cfg,
		server: srv,
	}, nil
}

// Run 启动 HTTP 服务并阻塞直到 ctx 取消或服务出错。
//
// 嵌入方如果只用 SDK 模式不需要 HTTP，可以不调用 Run；
// 调用 Run 时由 ctx 控制 graceful shutdown：ctx.Done() 触发后
// Run 走 Hub.Close() 路径完成清理——这样宿主进程的 defer hub.Close()
// 第二次触发就走 sync.Once 兜底成 no-op，不会让下游 deviceSvc/oauthSvc
// 等 close(channel) 二次执行。
func (h *Hub) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.server.Run()
	}()
	select {
	case <-ctx.Done():
		return h.Close()
	case err := <-errCh:
		return err
	}
}

// Close 释放所有资源：HTTP server、数据库连接、搜索索引、后台 goroutine。
//
// 幂等：sync.Once 保证 Hub.Run 内部触发 + 宿主 defer 二次触发只会真正执行一次，
// 第二次及之后直接返回首次缓存的错误（通常为 nil）。
func (h *Hub) Close() error {
	h.closeOnce.Do(func() {
		h.closeErr = h.server.Shutdown(context.Background())
	})
	return h.closeErr
}

// Config 返回 Hub 的配置（只读用途）。
//
// 注意：返回的是内部引用，调用方不应修改其字段。
func (h *Hub) Config() *config.Config {
	return h.cfg
}

// RegisterRoutes 把 SkillHub 的全部 HTTP API 挂到嵌入方提供的 router 上。
//
// 嵌入场景的典型用法：
//
//	engine := gin.New()
//	engine.Use(myAuthMiddleware, myLoggingMiddleware)
//	hub.RegisterRoutes(engine.Group("/skillhub"))   // 挂在子路径下
//	engine.Run(":8080")                              // 由宿主自己监听
//
// 嵌入方调用 RegisterRoutes 后通常不再调用 Hub.Run——两者会创建重复的 handler。
//
// 路由清单见 server.Server.RegisterRoutes 的方法注释。
func (h *Hub) RegisterRoutes(r gin.IRouter) {
	h.server.RegisterRoutes(r)
}

// RegisterStatic 挂载 SkillHub 自带的前端 SPA（含 /assets 与 NoRoute 兜底）。
//
// 嵌入方一般不调用本方法——他们已经有自己的前端 UI。
// 仅当你确实想复用 SkillHub 的内置 React 前端时才需要它。
//
// 因为 NoRoute 只能在 *gin.Engine 上注册（不在 IRouter 接口里），所以本方法
// 的入参类型与 RegisterRoutes 不同。
func (h *Hub) RegisterStatic(engine *gin.Engine) {
	h.server.RegisterStatic(engine)
}

// NewDefaultEngine 返回一个预装基础中间件（Recovery / RequestID / Logging /
// Metrics / SecurityHeaders）的 gin.Engine，与 Hub.Run() 内部使用的 engine 等价。
//
// 嵌入方如果想复用同一套中间件配置但又想自管 HTTP 监听，可以：
//
//	engine := hub.NewDefaultEngine()
//	hub.RegisterRoutes(engine)
//	hub.RegisterStatic(engine)         // 可选
//	srv := &http.Server{Addr: ":8080", Handler: engine}
//	srv.ListenAndServe()
func (h *Hub) NewDefaultEngine() *gin.Engine {
	return h.server.NewDefaultEngine()
}
