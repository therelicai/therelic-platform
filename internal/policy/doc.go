// Package policy is a vendored mirror of the runtime's policy
// evaluator (github.com/therelicai/therelic/internal/policy). It
// exists so the platform can run policy.Simulate against historical
// trace events without taking a Go-module dependency on the runtime,
// for the same isolation reasons documented in
// internal/trace/integrity.go.
//
// The mirrored files (engine.go, parser.go, sequence.go) are
// byte-identical to upstream at the SHA pinned in UPSTREAM.txt. A CI
// job fails on any drift. simulate.go is a platform-only addition
// that wraps the canonical Evaluate.
package policy
