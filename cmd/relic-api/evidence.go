package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/therelicai/therelic-platform/internal/compliance"
	"github.com/therelicai/therelic-platform/internal/storage"
	"github.com/therelicai/therelic-platform/internal/version"
)

// evidencePackCommand handles `relic-api evidence-pack`.
//
// Usage:
//   relic-api evidence-pack \
//     --org=<org-id> --framework=soc2-cc \
//     --period=Q3-2026 \
//     --start=2026-07-01 --end=2026-10-01 \
//     --sign-with=hmac \
//     --out=q3.tar.gz
func evidencePackCommand(args []string, logger *slog.Logger) {
	var (
		org, framework, period, startStr, endStr, signWith, out, mappingsDir string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--org="):
			org = strings.TrimPrefix(a, "--org=")
		case strings.HasPrefix(a, "--framework="):
			framework = strings.TrimPrefix(a, "--framework=")
		case strings.HasPrefix(a, "--period="):
			period = strings.TrimPrefix(a, "--period=")
		case strings.HasPrefix(a, "--start="):
			startStr = strings.TrimPrefix(a, "--start=")
		case strings.HasPrefix(a, "--end="):
			endStr = strings.TrimPrefix(a, "--end=")
		case strings.HasPrefix(a, "--sign-with="):
			signWith = strings.TrimPrefix(a, "--sign-with=")
		case strings.HasPrefix(a, "--out="):
			out = strings.TrimPrefix(a, "--out=")
		case strings.HasPrefix(a, "--mappings-dir="):
			mappingsDir = strings.TrimPrefix(a, "--mappings-dir=")
		case a == "-h" || a == "--help":
			fmt.Print(evidencePackUsage)
			return
		}
	}
	if org == "" || framework == "" || period == "" || startStr == "" || endStr == "" || out == "" {
		fmt.Fprint(os.Stderr, evidencePackUsage)
		os.Exit(2)
	}
	if signWith == "" {
		signWith = "hmac"
	}
	if mappingsDir == "" {
		mappingsDir = "internal/compliance/mappings"
	}

	start, err := parseDay(startStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --start: %v\n", err)
		os.Exit(2)
	}
	end, err := parseDay(endStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --end: %v\n", err)
		os.Exit(2)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db, err := storage.NewPostgres(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	signer, err := buildSigner(signWith)
	if err != nil {
		fmt.Fprintf(os.Stderr, "signer: %v\n", err)
		os.Exit(1)
	}

	outFile, err := os.Create(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", out, err)
		os.Exit(1)
	}
	defer outFile.Close()

	manifest, err := compliance.BuildPack(ctx, compliance.PackRequest{
		OrgID:       org,
		Framework:   framework,
		Period:      period,
		Start:       start,
		End:         end,
		Generator:   "relic-api " + version.Build,
		MappingsDir: mappingsDir,
		Signer:      signer,
	}, db, outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build pack: %v\n", err)
		os.Exit(1)
	}
	logger.Info("evidence pack written",
		"path", out,
		"framework", framework,
		"period", period,
		"audit_events", manifest.Counts.AuditEvents,
		"runs", manifest.Counts.Runs,
		"sig_kind", manifest.Signature.Kind,
		"sig_key_id", manifest.Signature.KeyID,
	)
	abs, _ := filepath.Abs(out)
	fmt.Printf("Evidence pack written: %s\n", abs)
	fmt.Printf("  framework:    %s (period: %s)\n", framework, period)
	fmt.Printf("  audit events: %d\n", manifest.Counts.AuditEvents)
	fmt.Printf("  runs:         %d\n", manifest.Counts.Runs)
	fmt.Printf("  signature:    %s (key id: %s)\n", manifest.Signature.Kind, manifest.Signature.KeyID)
}

// evidenceVerifyCommand handles `relic-api evidence-verify <path>`.
func evidenceVerifyCommand(args []string, logger *slog.Logger) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: relic-api evidence-verify <pack.tar.gz>")
		os.Exit(2)
	}
	path := args[0]
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
		os.Exit(1)
	}
	defer f.Close()

	// We only support HMAC at the moment; the signer in env must
	// match the one used at pack time. Operators rotate by re-
	// generating older packs after a key change.
	signer, err := buildSigner("hmac")
	if err != nil {
		fmt.Fprintf(os.Stderr, "signer: %v\n", err)
		os.Exit(1)
	}

	manifest, err := compliance.VerifyPack(f, signer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED · %v\n", err)
		os.Exit(1)
	}
	logger.Info("evidence pack verified", "framework", manifest.Framework, "period", manifest.Period)
	fmt.Printf("VERIFIED · framework=%s · period=%s · %d audit events · %d runs · sig=%s key=%s\n",
		manifest.Framework, manifest.Period,
		manifest.Counts.AuditEvents, manifest.Counts.Runs,
		manifest.Signature.Kind, manifest.Signature.KeyID,
	)
}

func buildSigner(kind string) (compliance.Signer, error) {
	switch kind {
	case "hmac", "":
		key := strings.TrimSpace(os.Getenv("RELIC_EVIDENCE_KEY"))
		if key == "" {
			key = strings.TrimSpace(os.Getenv("RELIC_JWT_SECRET"))
		}
		if key == "" {
			return nil, fmt.Errorf("RELIC_EVIDENCE_KEY (or RELIC_JWT_SECRET) required for hmac signer")
		}
		return compliance.NewHMACSigner(key)
	case "gpg":
		return nil, fmt.Errorf("--sign-with=gpg not yet supported (v1 enhancement; tracked in build plan WS-3B)")
	default:
		return nil, fmt.Errorf("unknown --sign-with=%q (expected: hmac | gpg)", kind)
	}
}

func parseDay(s string) (time.Time, error) {
	// Accept YYYY-MM-DD (UTC) or full RFC3339.
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339, got %q", s)
}

const evidencePackUsage = `usage: relic-api evidence-pack \
  --org=<org-id> --framework=soc2-cc \
  --period=Q3-2026 \
  --start=YYYY-MM-DD --end=YYYY-MM-DD \
  [--sign-with=hmac]            (gpg coming; hmac is the v0 default)
  [--mappings-dir=internal/compliance/mappings]
  --out=<file.tar.gz>

Reads RELIC_EVIDENCE_KEY (or RELIC_JWT_SECRET as fallback) for the HMAC
signer. Writes a signed tarball you can hand to an auditor.

Verify a pack with:
  relic-api evidence-verify <file.tar.gz>
`
