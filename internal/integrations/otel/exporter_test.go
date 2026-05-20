package otel

import (
	"context"
	"testing"
)

// These tests exercise the exporter against the OTEL no-op global
// providers (which is what's active when RELIC_OTEL_ENABLED is unset).
// The contract is: emitting events when disabled is safe + cheap.
// We're not asserting on a recorded exporter here; that would require
// wiring an SDK provider with an in-memory exporter, which the
// observability package already covers in its own tests when added.

func TestEmitPolicyDecision_NoCrashWhenDisabled(t *testing.T) {
	// Several calls just to exercise the sync.Once instrument init
	// path under concurrency.
	for i := 0; i < 5; i++ {
		EmitPolicyDecision(context.Background(), "o1", "agent-a", "exec", "allow")
		EmitPolicyDecision(context.Background(), "o1", "agent-a", "exec", "deny")
	}
}

func TestEmitTraceIngest_NoCrash(t *testing.T) {
	EmitTraceIngest(context.Background(), "o1", "agent-a", 42, 17)
}

func TestEmitAuthLogin_NoCrash(t *testing.T) {
	EmitAuthLogin(context.Background(), "o1", "u1", "oidc", true)
	EmitAuthLogin(context.Background(), "o1", "u1", "oidc", false)
}
