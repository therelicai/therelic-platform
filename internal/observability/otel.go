// Package observability wires OpenTelemetry traces + metrics for the
// API. The trace/metric SDK live behind a small Setup() function so
// the API entrypoint (cmd/relic-api) can boot OTEL conditionally
// based on RELIC_OTEL_ENDPOINT.
//
// When the endpoint is unset, Setup returns no-op shutdowns and the
// API behaves exactly as before — zero egress, zero overhead.
//
// We deliberately keep the OTEL surface area small here. The full
// "emit a span per policy decision" wiring lands in WS-4A and
// imports from this package; this file is only the setup plumbing.
package observability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config is the boot-time OTEL configuration, parsed from
// RELIC_OTEL_* env vars by ConfigFromEnv.
type Config struct {
	Enabled            bool
	Endpoint           string
	AuthHeader         string // single "key:value" pair, parsed at use
	ServiceName        string
	ResourceAttributes string // "k=v,k2=v2" pairs
}

// ConfigFromEnv reads RELIC_OTEL_* env vars. Returns Enabled=false
// when RELIC_OTEL_ENABLED is unset or RELIC_OTEL_ENDPOINT is empty,
// so the API entrypoint can branch with a single if.
func ConfigFromEnv() Config {
	cfg := Config{
		Enabled:            strings.EqualFold(strings.TrimSpace(os.Getenv("RELIC_OTEL_ENABLED")), "true"),
		Endpoint:           strings.TrimSpace(os.Getenv("RELIC_OTEL_ENDPOINT")),
		AuthHeader:         strings.TrimSpace(os.Getenv("RELIC_OTEL_AUTH_HEADER")),
		ServiceName:        strings.TrimSpace(os.Getenv("RELIC_OTEL_SERVICE_NAME")),
		ResourceAttributes: strings.TrimSpace(os.Getenv("RELIC_OTEL_RESOURCE_ATTRIBUTES")),
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "relic-api"
	}
	if cfg.Endpoint == "" {
		cfg.Enabled = false
	}
	return cfg
}

// Shutdown is returned by Setup; call before process exit. No-op safe.
type Shutdown func(ctx context.Context) error

// Setup configures the global tracer + meter providers when cfg is
// enabled. Returns a single Shutdown that flushes both. When disabled,
// returns a no-op Shutdown so the caller doesn't need to nil-check.
func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	hdrMap := map[string]string{}
	if k, v, ok := splitHeader(cfg.AuthHeader); ok {
		hdrMap[k] = v
	}

	// --- traces ---
	traceExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(stripScheme(cfg.Endpoint)),
		otlptracehttp.WithHeaders(hdrMap),
	)
	if err != nil {
		return nil, fmt.Errorf("otlptrace: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp,
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// --- metrics ---
	metricExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(stripScheme(cfg.Endpoint)),
		otlpmetrichttp.WithHeaders(hdrMap),
	)
	if err != nil {
		return nil, fmt.Errorf("otlpmetric: %w", err)
	}
	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExp,
			metric.WithInterval(30*time.Second),
		)),
	)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) error {
		// Flush both. Return the first error we see but try both so
		// a hung exporter doesn't strand the other.
		err1 := tp.Shutdown(ctx)
		err2 := mp.Shutdown(ctx)
		if err1 != nil {
			return err1
		}
		return err2
	}, nil
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
	}
	for _, pair := range strings.Split(cfg.ResourceAttributes, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && k != "" {
			attrs = append(attrs, attribute.String(strings.TrimSpace(k), strings.TrimSpace(v)))
		}
	}
	return resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithProcess(),
		resource.WithHost(),
	)
}

func splitHeader(s string) (string, string, bool) {
	k, v, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	v = strings.TrimSpace(v)
	if k == "" || v == "" {
		return "", "", false
	}
	return k, v, true
}

// stripScheme removes the http(s):// prefix because the OTLP HTTP
// exporter takes the host:path separately. WithInsecure / WithTLS
// pick the scheme; we default to TLS (no flag), so any http:// URL
// from the operator is a bug to flag.
func stripScheme(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	return u
}

// HTTPMiddleware is a thin chi-compatible middleware that records an
// OTEL span per request. We don't use otelhttp directly because it
// wraps the whole handler chain and double-counts under our existing
// requestLogger. This emits one span per inbound request and stamps
// the route pattern as an attribute.
//
// When OTEL is disabled, the tracer is a no-op and the middleware is
// effectively free.
func HTTPMiddleware(next http.Handler) http.Handler {
	tracer := otel.Tracer("relic-api/http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
			// We omit route attribute here; the WS-4A wiring sets it
			// from chi's RoutePattern after dispatch.
		)
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
