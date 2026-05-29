package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestHandlersEnforceTenantScoping is a static audit. It parses every
// internal/api/*.go file, finds every method on *Server with a name
// like handleX, and verifies the body invokes one of the tenant-
// scoping helpers:
//
//   - s.requireOrg(...)            — the standard 403-on-missing path
//   - s.requireMatchingOrg(...)    — used in routes with {orgID} param
//   - middleware.OrgIDFromContext  — when the handler reads org
//                                    directly (e.g. simple read paths)
//
// Handlers that are LEGITIMATELY public or pre-auth go in the
// allowlist below with a one-line reason. The allowlist is the
// review gate: adding a new entry to it should require explicit
// reviewer attention.
//
// This guards against the most common cross-tenant leak pattern:
// new endpoint, forgets to filter by org, returns the union of
// every tenant's rows.
func TestHandlersEnforceTenantScoping(t *testing.T) {
	// reason text is informational; the key is what matters.
	allowlist := map[string]string{
		// pre-auth surface
		"handleVersion":      "/v1/version is mounted outside the auth middleware (public deployment metadata)",
		"handleReadyz":       "/readyz is the liveness probe; no auth, no body",
		"handleAuthLogin":    "POST /v1/auth/login establishes the session; pre-auth by definition",
		"handleOIDCLogin":    "OIDC PKCE flow start; pre-auth",
		"handleOIDCCallback": "OIDC PKCE flow finish; pre-auth, sets cookie",
		"handleOIDCLogout":   "OIDC logout: clears cookie; org_id irrelevant",

		// onboarding: creates the caller's org, so by definition runs
		// before an org exists for them. Still authed.
		"handleOnboard": "Onboarding creates the caller's org; nothing to scope to yet",

		// stubbed unimplemented endpoints. They return 501 or empty
		// payloads without touching any tenant data.
		"handleSearchRegistry":     "stub — returns empty; no tenant data accessed",
		"handlePublishListing":     "stub — returns 501",
		"handleUpdateListing":      "stub — returns 501",
		"handleDeleteListing":      "stub — returns 501",
		"handleGetTrustScore":      "stub — returns 501",
		"handleListTransactions":   "stub — returns empty",
		"handleTransactionSummary": "stub — returns zeros",
		"handleGetTransaction":     "stub — returns 501",

		// Three-line delegators to s.decideProposal, which calls
		// requireOrg. The audit can't follow the call so allowlist
		// the wrappers explicitly.
		"handleApproveProposal": "delegates to s.decideProposal which calls requireOrg",
		"handleRejectProposal":  "delegates to s.decideProposal which calls requireOrg",
		"handleDismissProposal": "delegates to s.decideProposal which calls requireOrg",
	}

	scopingCalls := []string{
		"requireOrg",
		"requireMatchingOrg",
		"OrgIDFromContext",
	}

	// Walk internal/api/*.go (handler files live at the package root).
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()

	type violation struct{ name, file string }
	var violations []violation
	seen := map[string]bool{}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		// We only audit handler methods on *Server.
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// Must be a method on *Server.
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "Server" {
				continue
			}
			// Must be named handle*.
			if !strings.HasPrefix(fn.Name.Name, "handle") {
				continue
			}
			// Must have an http.ResponseWriter parameter (rules out
			// helpers that happen to be named handle*).
			if !looksLikeHTTPHandler(fn) {
				continue
			}

			name := fn.Name.Name
			seen[name] = true
			if _, allowed := allowlist[name]; allowed {
				continue
			}

			body := exprText(src, fset, fn.Body.Pos(), fn.Body.End())
			if !containsAny(body, scopingCalls) {
				violations = append(violations, violation{name: name, file: f})
			}
		}
	}

	if len(violations) > 0 {
		var lines []string
		for _, v := range violations {
			lines = append(lines, "  - "+v.file+":"+v.name)
		}
		sort.Strings(lines)
		t.Fatalf("handlers without tenant-scoping call (requireOrg / requireMatchingOrg / OrgIDFromContext):\n%s\n\n"+
			"Every handler in internal/api must enforce org scoping. If the handler is "+
			"legitimately pre-auth or unscoped, add it to the allowlist in handlers_audit_test.go "+
			"with a one-line reason.",
			strings.Join(lines, "\n"))
	}

	// Catch allowlist rot: if a name in the allowlist no longer
	// matches a real handler, we want to know — otherwise the
	// allowlist silently grows stale and may mask later regressions.
	for name := range allowlist {
		if !seen[name] {
			t.Errorf("allowlist entry %q does not match any handler in this package — remove the stale exception", name)
		}
	}
}

// looksLikeHTTPHandler returns true when the function signature is
// roughly `func(...) (w http.ResponseWriter, r *http.Request)`. We
// don't require exact type matching — substring on the params source
// text is plenty for an audit.
func looksLikeHTTPHandler(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	hasRW := false
	hasReq := false
	for _, p := range fn.Type.Params.List {
		typeStr := exprName(p.Type)
		if strings.Contains(typeStr, "ResponseWriter") {
			hasRW = true
		}
		if strings.Contains(typeStr, "Request") {
			hasReq = true
		}
	}
	return hasRW && hasReq
}

func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprName(v.X)
	}
	return ""
}

func exprText(src []byte, fset *token.FileSet, start, end token.Pos) string {
	a := fset.Position(start).Offset
	b := fset.Position(end).Offset
	if a < 0 {
		a = 0
	}
	if b > len(src) {
		b = len(src)
	}
	if a >= b {
		return ""
	}
	return string(src[a:b])
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
