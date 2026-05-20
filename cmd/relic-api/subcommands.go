package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/therelicai/therelic-platform/internal/auth"
	"github.com/therelicai/therelic-platform/internal/migrate"
	"github.com/therelicai/therelic-platform/internal/storage"
	"github.com/therelicai/therelic-platform/internal/version"
)

// printUsage is the catch-all -h / help dispatcher.
func printUsage() {
	fmt.Fprintf(os.Stderr, `relic-api %s (%s)

Usage:
  relic-api                         Start the HTTP server (default).
  relic-api migrate up              Apply pending migrations.
  relic-api migrate status          List applied migrations + schema version.
  relic-api version                 Print build / commit / schema_version.
  relic-api backup OUT_FILE         Dump database + S3 manifest to OUT_FILE.tar.gz.
  relic-api restore IN_FILE         Restore from a backup produced by 'backup'.
  relic-api reset-password EMAIL    Reset a local-auth user's password.

Environment:
  DATABASE_URL         Postgres connection string (required).
  RELIC_AUTH_MODE      local | supabase | oidc (required).
  RELIC_RLS_ENABLED    true to apply migrations.rls/.
  S3_* / MINIO_ROOT_*  Blob storage credentials (required for serve + backup).

See RUNNING.md for the full quickstart.
`, version.Build, version.Commit)
}

// resetPasswordCommand handles `reset-password EMAIL [NEW_PASSWORD]`.
// Used when the admin forgets the bootstrap password. New password
// can be passed as an argument (handy for scripts) or piped on stdin
// (more secure: no shell history). Requires DATABASE_URL.
//
// Only resets users whose auth_provider is `local` — Supabase / OIDC
// users own their credentials in their IdP, not here.
func resetPasswordCommand(args []string, logger *slog.Logger) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: relic-api reset-password EMAIL [NEW_PASSWORD]")
		fmt.Fprintln(os.Stderr, "  If NEW_PASSWORD is omitted, reads it from stdin.")
		os.Exit(2)
	}
	email := strings.ToLower(strings.TrimSpace(args[0]))
	var pw string
	if len(args) >= 2 {
		pw = args[1]
	} else {
		fmt.Fprint(os.Stderr, "New password (>=8 chars): ")
		var line string
		if _, err := fmt.Scanln(&line); err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			os.Exit(1)
		}
		pw = strings.TrimSpace(line)
	}
	if len(pw) < 8 {
		fmt.Fprintln(os.Stderr, "password must be at least 8 characters")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := storage.NewPostgres(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect db: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash password: %v\n", err)
		os.Exit(1)
	}

	tag, err := pool.Pool().Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE email = $2 AND auth_provider = 'local'`,
		hash, email)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		os.Exit(1)
	}
	if tag.RowsAffected() == 0 {
		fmt.Fprintf(os.Stderr, "no local-auth user with email %q (auth_provider must be 'local')\n", email)
		os.Exit(1)
	}
	logger.Info("password reset", "email", email)
	fmt.Printf("Password reset for %s. The user can now sign in with the new password.\n", email)
}

// versionCommand prints the platform's self-reported version metadata.
func versionCommand() {
	fmt.Printf("build:   %s\n", version.Build)
	fmt.Printf("commit:  %s\n", version.Commit)
	// Schema version requires a DB connection; only print it when
	// DATABASE_URL is set. Otherwise just the build info is useful.
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := storage.NewPostgres(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not connect to DATABASE_URL: %v\n", err)
		return
	}
	defer pool.Close()
	sv, _ := version.SchemaVersion(ctx, pool.Pool())
	fmt.Printf("schema:  %s\n", sv)
}

// migrateCommand handles `migrate up` and `migrate status`.
func migrateCommand(args []string, logger *slog.Logger) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: relic-api migrate {up|status}")
		os.Exit(2)
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := storage.NewPostgres(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect db: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	opts := migrate.Options{
		CoreDir:     envOr("RELIC_MIGRATIONS_DIR", "/migrations"),
		SupabaseDir: envOr("RELIC_MIGRATIONS_SUPABASE_DIR", "/migrations.supabase"),
		RLSDir:      envOr("RELIC_MIGRATIONS_RLS_DIR", "/migrations.rls"),
		AuthMode:    strings.TrimSpace(os.Getenv("RELIC_AUTH_MODE")),
		RLSEnabled:  strings.EqualFold(os.Getenv("RELIC_RLS_ENABLED"), "true"),
	}
	runner := migrate.New(pool.Pool(), opts, logger)

	switch args[0] {
	case "up":
		applied, err := runner.Up(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migrate up: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Applied %d migration(s).\n", applied)
	case "status":
		files, err := runner.Status(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migrate status: %v\n", err)
			os.Exit(1)
		}
		if len(files) == 0 {
			fmt.Println("(no migrations applied)")
			return
		}
		for _, f := range files {
			fmt.Println(f)
		}
		sv, _ := version.SchemaVersion(ctx, pool.Pool())
		fmt.Printf("\nschema_version: %s\n", sv)
	default:
		fmt.Fprintf(os.Stderr, "unknown migrate subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
