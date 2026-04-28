// Package metrics holds Prometheus collectors for the skillhub server.
//
// Why a tiny dedicated package: keeps the prometheus dependency out of
// hot paths and lets feature code import only the collectors it touches
// without dragging the gin middleware along.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HTTPRequests counts requests by method+route+status.
	// Route uses gin's matched template (e.g. /api/v1/skills/:slug),
	// not the raw URL — keeps cardinality bounded.
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "skillhub_http_requests_total",
		Help: "Total HTTP requests, labeled by method, route template, and status.",
	}, []string{"method", "route", "status"})

	// HTTPDuration measures request latency seconds.
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "skillhub_http_request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	// SkillPublished is incremented per successful publish.
	// Labels: visibility (public|private) so admins can spot bursts of
	// either kind without joining tables.
	SkillPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "skillhub_skill_published_total",
		Help: "Total skill versions published.",
	}, []string{"visibility"})

	// SkillDownloads is incremented per resolved download (deduped or not).
	SkillDownloads = promauto.NewCounter(prometheus.CounterOpts{
		Name: "skillhub_skill_downloads_total",
		Help: "Total skill downloads served.",
	})

	// SearchQueries counts search calls.
	SearchQueries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "skillhub_search_queries_total",
		Help: "Total search queries executed.",
	})
)
