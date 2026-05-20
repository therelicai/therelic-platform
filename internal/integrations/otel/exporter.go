// Package otel provides the high-level Emit functions handlers call
// to record policy decisions, trace ingest, and login events as OTEL
// spans + counters.
//
// The underlying tracer + meter providers are set up by
// internal/observability/otel.go on boot. If observability is disabled
// (RELIC_OTEL_ENABLED unset), the global providers are no-ops and
// these functions are effectively free — no allocations beyond
// attribute slices.
package otel

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "relic-api"
const meterName = "relic-api"

var (
	once sync.Once

	policyCounter metric.Int64Counter
	ingestCounter metric.Int64Counter
	loginCounter  metric.Int64Counter

	tracer trace.Tracer
)

func ensureInstruments() {
	once.Do(func() {
		tracer = otel.Tracer(tracerName)
		meter := otel.Meter(meterName)
		// Counter creation never fails in practice; ignore the error
		// and let later Add calls record into the noop counter.
		policyCounter, _ = meter.Int64Counter("relic_policy_decisions_total")
		ingestCounter, _ = meter.Int64Counter("relic_trace_events_ingested_total")
		loginCounter, _ = meter.Int64Counter("relic_auth_login_total")
	})
}

// EmitPolicyDecision records one policy.evaluate span + a counter
// tick. Decision should be "allow" / "deny" / "flag".
func EmitPolicyDecision(ctx context.Context, orgID, agent, tool, decision string) {
	ensureInstruments()
	_, span := tracer.Start(ctx, "policy.evaluate",
		trace.WithAttributes(
			attribute.String("org_id", orgID),
			attribute.String("agent_name", agent),
			attribute.String("tool", tool),
			attribute.String("decision", decision),
		),
	)
	span.End()
	policyCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("decision", decision),
		attribute.String("org", orgID),
	))
}

// EmitTraceIngest records one trace.ingest span + bumps the trace
// event counter by eventCount.
func EmitTraceIngest(ctx context.Context, orgID, agent string, eventCount int, durationMs int64) {
	ensureInstruments()
	_, span := tracer.Start(ctx, "trace.ingest",
		trace.WithAttributes(
			attribute.String("org_id", orgID),
			attribute.String("agent_name", agent),
			attribute.Int("event_count", eventCount),
			attribute.Int64("duration_ms", durationMs),
		),
	)
	span.End()
	ingestCounter.Add(ctx, int64(eventCount), metric.WithAttributes(
		attribute.String("org", orgID),
	))
}

// EmitAuthLogin records one auth.login span + counter tick.
// success is true for successful logins; provider is "local",
// "oidc", or "supabase".
func EmitAuthLogin(ctx context.Context, orgID, userID, provider string, success bool) {
	ensureInstruments()
	result := "success"
	if !success {
		result = "failure"
	}
	_, span := tracer.Start(ctx, "auth.login",
		trace.WithAttributes(
			attribute.String("org_id", orgID),
			attribute.String("user_id", userID),
			attribute.String("provider", provider),
			attribute.String("result", result),
		),
	)
	span.End()
	loginCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", provider),
		attribute.String("result", result),
	))
}
