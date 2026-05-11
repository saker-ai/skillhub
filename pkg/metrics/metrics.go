// Package metrics holds Prometheus collectors for the skillhub server.
//
// 为什么放进一个独立的小包：把 prometheus 依赖隔离在单独 import 之内，
// 让上层 feature code 只引入它真正用到的 collector，不必拖入 gin 中间件。
//
// 阶段 2 重构：把原先的全局 var 改成显式的 *Metrics 实例 + 工厂函数，
// 嵌入方可以注入自己的 prometheus.Registerer，避免与宿主进程的指标命名冲突。
// 同时保留顶层 var（HTTPRequests / HTTPDuration 等）代理到 Default 单例，
// 保证旧调用点（独立二进制）一字不变即可继续工作。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics 持有 SkillHub 全部 Prometheus collector，按依赖注入方式使用。
//
// 嵌入方应通过 New(reg) 构造一个独立实例，把它传入 Server.Deps；
// 独立二进制通过 Default 单例（注册到 prometheus.DefaultRegisterer）继续工作。
type Metrics struct {
	HTTPRequests   *prometheus.CounterVec
	HTTPDuration   *prometheus.HistogramVec
	SkillPublished *prometheus.CounterVec
	SkillDownloads prometheus.Counter
	SearchQueries  prometheus.Counter

	// TeamTokenCreated counts successful team-token mints, labeled by scope
	// (full|read|publish). Useful to alert on sudden bursts that often signal
	// either a CI rollout or a credential-stuffing automation.
	TeamTokenCreated *prometheus.CounterVec

	// TeamTokenRevoked counts revoke events. cause label is a fixed enum to
	// keep cardinality bounded:
	//   - self                          : creator revoked their own token
	//   - by_admin                      : owner/admin revoked someone else's
	//   - cascade_member_remove         : automatic revoke when a member was kicked
	//   - cascade_member_leave          : automatic revoke when a member left
	//   - cascade_namespace_delete      : automatic revoke when the namespace went away
	// For cascade values the counter is incremented by the number of tokens in
	// the batch (Add(float64(n))) so the metric reflects token-level impact,
	// not the number of cascade *operations*.
	TeamTokenRevoked *prometheus.CounterVec

	// TeamTokenQuotaRejected counts CreateForNamespace requests rejected because
	// the namespace already holds maxTeamTokensPerNamespace active tokens.
	// A spike here is a leading indicator of either a misbehaving CI loop minting
	// per-build tokens, or genuine demand to raise the quota.
	TeamTokenQuotaRejected prometheus.Counter

	// SkillCacheHits / SkillCacheMisses 用来观测 SkillRepo 内嵌的 LRU 缓存效果。
	// 命中率 = hits / (hits + misses)。低命中率（<50%）通常意味着 TTL 过短，
	// 或者 service 层在 metadata 没变的路径上调用了 InvalidateCache。
	SkillCacheHits   prometheus.Counter
	SkillCacheMisses prometheus.Counter
}

// New 创建一个 Metrics 实例并注册到指定 Registerer。
// 传 nil 等价于 prometheus.DefaultRegisterer。
//
// 注意：对同一个 Registerer 重复调用 New 会触发 panic
// （prometheus 库在重复注册同名 collector 时主动 panic）。
// 嵌入方如需在测试中多次构造，请传入新的 prometheus.NewRegistry()。
func New(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	factory := promauto.With(reg)
	return &Metrics{
		// HTTPRequests counts requests by method+route+status.
		// Route 用 gin 匹配后的模板（例如 /api/v1/skills/:slug），不是裸 URL —— 控制基数。
		HTTPRequests: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "skillhub_http_requests_total",
			Help: "Total HTTP requests, labeled by method, route template, and status.",
		}, []string{"method", "route", "status"}),

		// HTTPDuration measures request latency seconds.
		HTTPDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "skillhub_http_request_duration_seconds",
			Help:    "HTTP request latency by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),

		// SkillPublished：每次成功发布累加；按 visibility(public|private) 分桶，
		// 方便管理员一眼看到哪种发布有突发。
		SkillPublished: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "skillhub_skill_published_total",
			Help: "Total skill versions published.",
		}, []string{"visibility"}),

		// SkillDownloads：每次解析后的下载累加（无论是否被 dedup）。
		SkillDownloads: factory.NewCounter(prometheus.CounterOpts{
			Name: "skillhub_skill_downloads_total",
			Help: "Total skill downloads served.",
		}),

		// SearchQueries：搜索调用次数。
		SearchQueries: factory.NewCounter(prometheus.CounterOpts{
			Name: "skillhub_search_queries_total",
			Help: "Total search queries executed.",
		}),

		TeamTokenCreated: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "skillhub_team_token_created_total",
			Help: "Total namespace-bound team tokens minted, by scope.",
		}, []string{"scope"}),

		TeamTokenRevoked: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "skillhub_team_token_revoked_total",
			Help: "Total namespace-bound team tokens revoked, by cause (self|by_admin|cascade_*). Cascade values are incremented by the batch size, not by 1.",
		}, []string{"cause"}),

		TeamTokenQuotaRejected: factory.NewCounter(prometheus.CounterOpts{
			Name: "skillhub_team_token_quota_rejected_total",
			Help: "Count of team-token create requests rejected because the namespace already holds the maximum allowed active team tokens.",
		}),

		SkillCacheHits: factory.NewCounter(prometheus.CounterOpts{
			Name: "skillhub_skill_cache_hits_total",
			Help: "Count of SkillRepo cache hits served without DB roundtrip.",
		}),
		SkillCacheMisses: factory.NewCounter(prometheus.CounterOpts{
			Name: "skillhub_skill_cache_misses_total",
			Help: "Count of SkillRepo cache misses that fell through to DB.",
		}),
	}
}

// Default 是兜底单例，注册到 prometheus.DefaultRegisterer。
// 阶段 2 之后所有新代码应通过依赖注入获取 *Metrics，不要直接用 Default。
var Default = New(nil)

// 顶层变量代理到 Default，仅为了不破坏旧调用点（独立二进制行为完全等价）。
// 嵌入方应通过 server.Deps.Metrics 注入自己的实例，而不是写这些顶层变量。
var (
	HTTPRequests   = Default.HTTPRequests
	HTTPDuration   = Default.HTTPDuration
	SkillPublished = Default.SkillPublished
	SkillDownloads = Default.SkillDownloads
	SearchQueries  = Default.SearchQueries
)
