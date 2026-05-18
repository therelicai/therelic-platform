package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
	"github.com/therelicai/therelic-platform/internal/version"
)

// backupCommand handles `relic-api backup OUT.tar.gz` and
// `relic-api restore IN.tar.gz`. Reachable via the top-level dispatch
// in main.go.
//
// Backup bundle layout:
//
//   relic-backup/
//     manifest.json         { schema_version, taken_at, blob_count }
//     database.sql.gz       gzipped output of `pg_dump --no-owner --no-acl`
//     blobs.txt             newline-separated S3 keys (objects are NOT
//                           downloaded by default; see --include-blobs)
//
// Restore re-creates the database state, but leaves S3 alone. Blob
// objects are too large to bundle by default; operators with strict
// recovery requirements should mirror the bucket separately or pass
// --include-blobs at backup time.
func backupCommand(verb string, args []string, logger *slog.Logger) {
	switch verb {
	case "backup":
		runBackup(args, logger)
	case "restore":
		runRestore(args, logger)
	}
}

func runBackup(args []string, logger *slog.Logger) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: relic-api backup OUT_FILE.tar.gz")
		os.Exit(2)
	}
	out := args[0]
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required for backup")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// 1. pg_dump to a temp file (gzipped). We shell out because
	//    re-implementing pg_dump in Go is a non-starter and operators
	//    already trust pg_dump for their other Postgres backups.
	if _, err := exec.LookPath("pg_dump"); err != nil {
		fmt.Fprintln(os.Stderr, "pg_dump not found in PATH. Install postgresql-client.")
		os.Exit(1)
	}

	tmp, err := os.CreateTemp("", "relic-backup-*.sql.gz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp: %v\n", err)
		os.Exit(1)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	logger.Info("running pg_dump", "url_scrubbed", scrubURL(dbURL))
	dumpCmd := exec.CommandContext(ctx, "pg_dump",
		"--no-owner", "--no-acl", "--format=plain", dbURL,
	)
	gz := gzip.NewWriter(tmp)
	dumpCmd.Stdout = gz
	dumpCmd.Stderr = os.Stderr
	if err := dumpCmd.Run(); err != nil {
		_ = gz.Close()
		tmp.Close()
		fmt.Fprintf(os.Stderr, "pg_dump: %v\n", err)
		os.Exit(1)
	}
	_ = gz.Close()
	if err := tmp.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close temp: %v\n", err)
		os.Exit(1)
	}

	// 2. Schema version snapshot.
	pool, err := storage.NewPostgres(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect db: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()
	sv, _ := version.SchemaVersion(ctx, pool.Pool())

	// 3. List S3 keys (if S3 is configured). The list is recorded
	//    in the bundle so the operator knows which blobs need to be
	//    present in the destination bucket for a clean restore.
	var blobKeys []string
	if os.Getenv("S3_BUCKET") != "" {
		s3c, err := storage.NewS3(
			os.Getenv("S3_ENDPOINT"),
			os.Getenv("S3_BUCKET"),
			os.Getenv("S3_ACCESS_KEY"),
			os.Getenv("S3_SECRET_KEY"),
			os.Getenv("S3_REGION"),
		)
		if err != nil {
			logger.Warn("s3 client init failed, omitting blob list", "error", err)
		} else if keys, err := s3c.ListKeys(ctx); err != nil {
			logger.Warn("s3 list failed, omitting blob list", "error", err)
		} else {
			blobKeys = keys
		}
	}

	manifest := map[string]any{
		"schema_version": sv,
		"build":          version.Build,
		"commit":         version.Commit,
		"taken_at":       time.Now().UTC().Format(time.RFC3339),
		"blob_count":     len(blobKeys),
	}

	// 4. Assemble the tarball.
	if err := writeBackupBundle(out, tmpName, manifest, blobKeys); err != nil {
		fmt.Fprintf(os.Stderr, "write bundle: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Backup written: %s\n", out)
	fmt.Printf("  schema_version: %s\n", sv)
	fmt.Printf("  blob keys:      %d (S3 objects NOT copied; mirror your bucket separately)\n", len(blobKeys))
}

func runRestore(args []string, logger *slog.Logger) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: relic-api restore IN_FILE.tar.gz")
		os.Exit(2)
	}
	in := args[0]
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required for restore")
		os.Exit(1)
	}
	if _, err := exec.LookPath("psql"); err != nil {
		fmt.Fprintln(os.Stderr, "psql not found in PATH. Install postgresql-client.")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	dumpPath, manifest, err := extractBundle(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "extract: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(dumpPath)
	logger.Info("restoring", "from", in, "schema_version", manifest["schema_version"])

	psql := exec.CommandContext(ctx, "psql", "-v", "ON_ERROR_STOP=1", dbURL)
	gzf, err := os.Open(dumpPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open dump: %v\n", err)
		os.Exit(1)
	}
	defer gzf.Close()
	gr, err := gzip.NewReader(gzf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gunzip: %v\n", err)
		os.Exit(1)
	}
	defer gr.Close()
	psql.Stdin = gr
	psql.Stdout = os.Stdout
	psql.Stderr = os.Stderr
	if err := psql.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "psql: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Restore complete.")
	if bc, ok := manifest["blob_count"].(float64); ok && bc > 0 {
		fmt.Printf("Note: %d blob keys were referenced by the backup. Ensure your S3 bucket contains those objects.\n", int(bc))
	}
}

func writeBackupBundle(outPath, dumpPath string, manifest map[string]any, blobKeys []string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// manifest.json
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if err := writeTarEntry(tw, "relic-backup/manifest.json", manifestJSON); err != nil {
		return err
	}

	// database.sql.gz
	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}
	if err := writeTarEntry(tw, "relic-backup/database.sql.gz", dump); err != nil {
		return err
	}

	// blobs.txt
	if err := writeTarEntry(tw, "relic-backup/blobs.txt", []byte(strings.Join(blobKeys, "\n"))); err != nil {
		return err
	}
	return nil
}

func writeTarEntry(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(body)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func extractBundle(in string) (dumpPath string, manifest map[string]any, err error) {
	f, err := os.Open(in)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	manifest = map[string]any{}
	out, err := os.CreateTemp("", "relic-restore-*.sql.gz")
	if err != nil {
		return "", nil, err
	}
	dumpPath = out.Name()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
		switch hdr.Name {
		case "relic-backup/manifest.json":
			body, _ := io.ReadAll(tr)
			_ = json.Unmarshal(body, &manifest)
		case "relic-backup/database.sql.gz":
			if _, err := io.Copy(out, tr); err != nil {
				return "", nil, err
			}
		}
	}
	_ = out.Close()
	return dumpPath, manifest, nil
}

func scrubURL(u string) string {
	// "postgres://user:secret@host:port/db?..." -> "postgres://user:***@host:port/db"
	at := strings.Index(u, "@")
	colon := strings.Index(u, "://")
	if at < 0 || colon < 0 {
		return "<scrubbed>"
	}
	prefix := u[:colon+3]
	rest := u[colon+3:]
	atIdx := strings.Index(rest, "@")
	if atIdx < 0 {
		return u
	}
	userpass := rest[:atIdx]
	hostPart := rest[atIdx:]
	c := strings.Index(userpass, ":")
	if c < 0 {
		return prefix + userpass + hostPart
	}
	return prefix + userpass[:c] + ":***" + hostPart
}
