// Package metrics owns the Prometheus collectors for relic-api.
//
// Why centralise in one file rather than colocating with the
// subsystem that emits them? Two reasons:
//
//  1. The Prometheus client library panics on duplicate registration
//     within a process. Putting every collector through a single
//     package makes it impossible to register the same metric twice
//     from two different init() funcs.
//  2. The metric names are the operator-facing API. Centralising
//     them keeps the contract reviewable as a single file.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace is the Prometheus metric namespace; final metric names
// look like relic_api_http_requests_total. We deliberately don't use
// "therelic_" because the platform's logical name in dashboards is
// "relic-api" and matching that is less surprising than matching the
// org name.
const Namespace = "relic_api"

// Registry holds all collectors the platform exposes. We register
// against a private *prometheus.Registry rather than the default one
// so tests can spin up a server cleanly and so we don't accidentally
// inherit Go runtime metrics that the default registry pre-loads
// when a transitive dependency calls promauto.
var registry = prometheus.NewRegistry()

// HTTPRequests counts requests by method, status, and route pattern.
// Route pattern (not full URL) keeps cardinality bounded.
var HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: Namespace,
	Name:      "http_requests_total",
	Help:      "Total HTTP requests processed by relic-api.",
}, []string{"method", "route", "status"})

// HTTPRequestDuration tracks request latency. The buckets are tuned
// for an API that does parsing + storage I/O: most requests should
// land in the 25-500ms range; we keep enough granularity for SLO
// alerting on p95 / p99.
var HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: Namespace,
	Name:      "http_request_duration_seconds",
	Help:      "Duration of HTTP requests handled by relic-api.",
	Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
}, []string{"method", "route"})

// TraceUploads counts trace uploads by outcome. "rejected" outcomes
// include parse failures, chain-broken (Slice 6), and oversize
// payloads. Useful for spotting integration regressions.
var TraceUploads = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: Namespace,
	Name:      "trace_uploads_total",
	Help:      "Trace uploads by outcome.",
}, []string{"outcome"})

// RetentionSweeps and friends mirror the in-memory counters the
// retention worker maintains. The /metrics handler reads them from
// the worker each scrape rather than the worker pushing into the
// collector, so we don't double-source the state.
var (
	RetentionSweeps = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "retention_sweeps_completed",
		Help:      "Number of retention sweeps completed since process start.",
	}, retentionSweepsValue)

	RetentionRowsDeleted = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "retention_rows_deleted_total",
		Help:      "Number of rows reaped from the runs table by retention.",
	}, retentionDeletedValue)

	RetentionS3Failures = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "retention_s3_failures_total",
		Help:      "Number of S3 delete failures during retention sweeps.",
	}, retentionS3FailuresValue)

	RetentionLastRunSeconds = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "retention_last_run_timestamp_seconds",
		Help:      "Unix timestamp of the most recent retention sweep (0 if never).",
	}, retentionLastRunValue)
)

// DBPool* gauges are populated by SetDBPoolProvider. We use closures
// rather than direct values because pgxpool stats are read live.
var (
	DBPoolAcquired = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "db_pool_connections_acquired",
		Help:      "Currently acquired connections in the pgxpool.",
	}, func() float64 { return float64(dbPoolStats().Acquired) })

	DBPoolIdle = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "db_pool_connections_idle",
		Help:      "Currently idle connections in the pgxpool.",
	}, func() float64 { return float64(dbPoolStats().Idle) })

	DBPoolMax = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "db_pool_connections_max",
		Help:      "Configured maximum connections for the pgxpool.",
	}, func() float64 { return float64(dbPoolStats().Max) })
)

// Provider closures registered by the API server. We use package
// vars rather than struct injection because the Prometheus
// collectors register their callbacks at package init time and
// can't be re-bound to a struct.
var (
	retentionStatsFn func() RetentionStats
	dbPoolStatsFn    func() DBPoolStats
)

// RetentionStats is the subset of retention.Stats this package needs.
// Mirror in retention/worker.go so the import dependency points one
// direction (metrics doesn't import retention).
type RetentionStats struct {
	SweepsCompleted int64
	RowsDeleted     int64
	RowsDBFailures  int64
	RowsS3Failures  int64
	LastRunAt       time.Time
}

// DBPoolStats mirrors storage.PoolStat with the same one-way import.
type DBPoolStats struct {
	Acquired int32
	Idle     int32
	Max      int32
}

// SetRetentionProvider hooks the worker's Stats() into the gauges.
// Idempotent; calling multiple times overwrites.
func SetRetentionProvider(f func() RetentionStats) {
	retentionStatsFn = f
}

// SetDBPoolProvider hooks pgxpool.Stat() into the gauges.
func SetDBPoolProvider(f func() DBPoolStats) {
	dbPoolStatsFn = f
}

func retentionSweepsValue() float64 {
	if retentionStatsFn == nil {
		return 0
	}
	return float64(retentionStatsFn().SweepsCompleted)
}
func retentionDeletedValue() float64 {
	if retentionStatsFn == nil {
		return 0
	}
	return float64(retentionStatsFn().RowsDeleted)
}
func retentionS3FailuresValue() float64 {
	if retentionStatsFn == nil {
		return 0
	}
	return float64(retentionStatsFn().RowsS3Failures)
}
func retentionLastRunValue() float64 {
	if retentionStatsFn == nil {
		return 0
	}
	t := retentionStatsFn().LastRunAt
	if t.IsZero() {
		return 0
	}
	return float64(t.Unix())
}
func dbPoolStats() DBPoolStats {
	if dbPoolStatsFn == nil {
		return DBPoolStats{}
	}
	return dbPoolStatsFn()
}

func init() {
	// Process and Go runtime collectors give the cheap wins:
	// goroutine count, memory, GC, file descriptors.
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	registry.MustRegister(collectors.NewGoCollector())

	registry.MustRegister(
		HTTPRequests,
		HTTPRequestDuration,
		TraceUploads,
		RetentionSweeps,
		RetentionRowsDeleted,
		RetentionS3Failures,
		RetentionLastRunSeconds,
		DBPoolAcquired,
		DBPoolIdle,
		DBPoolMax,
	)
}

// Handler returns the /metrics HTTP handler. Should be mounted on a
// route that does NOT go through the API auth middleware — Prometheus
// scrapers don't carry bearer tokens.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Registry: registry,
	})
}

// ObserveRequest is the helper used by the request-logging middleware
// to record both counter and histogram in one call.
func ObserveRequest(method, route string, status int, dur time.Duration) {
	statusStr := strconv.Itoa(status)
	HTTPRequests.WithLabelValues(method, route, statusStr).Inc()
	HTTPRequestDuration.WithLabelValues(method, route).Observe(dur.Seconds())
}
