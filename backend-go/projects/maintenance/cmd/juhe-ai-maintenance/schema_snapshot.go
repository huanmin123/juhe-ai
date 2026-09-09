package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schemasnapshot"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// runPostgresSchemaSnapshot ports the archived Node
// scripts/operations/postgres-schema-snapshot.ts CLI entry: guarded target,
// explicit read-only confirmation, a REPEATABLE READ READ ONLY transaction
// and the schema JSON (with digest) written to stdout. PostgreSQL is the only
// supported dialect; SQLite targets are rejected by openSnapshotDB.
func runPostgresSchemaSnapshot() {
	target, err := schemasnapshot.AssertSnapshotTarget(os.Getenv(schemasnapshot.EnvTarget))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	rawURL := strings.TrimSpace(os.Getenv(schemasnapshot.EnvPostgresURL))
	if rawURL == "" {
		fmt.Fprintf(os.Stderr, "%s 未配置\n", schemasnapshot.EnvPostgresURL)
		os.Exit(2)
	}
	if os.Getenv(schemasnapshot.EnvReadOnlyConfirm) != schemasnapshot.ReadOnlyConfirmValue {
		fmt.Fprintln(os.Stderr, "必须设置 JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=READ_ONLY；该工具只允许只读快照")
		os.Exit(2)
	}

	db, err := openSnapshotDB(rawURL, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "PostgreSQL schema snapshot failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := runSnapshotSession(ctx, db, target); err != nil {
		fmt.Fprintf(os.Stderr, "PostgreSQL schema snapshot failed: %v\n", err)
		os.Exit(1)
	}
}

// runSnapshotSession mirrors the Node main(): REPEATABLE READ READ ONLY
// transaction, per-session statement/lock timeouts, collect, commit, then
// print. Rollback after a successful Commit is a harmless no-op.
func runSnapshotSession(ctx context.Context, db *sql.DB, target string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true)`); err != nil {
		return err
	}
	snapshot, err := schemasnapshot.CollectSnapshot(ctx, tx, target)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return writeSnapshotJSON(snapshot)
}

// openSnapshotDB validates the explicit PostgreSQL URL (rejecting SQLite and
// every other dialect) and pins the same connection identity as the Node
// client: application_name juhe-ai-schema-snapshot-<target> and a 10s
// connect timeout.
func openSnapshotDB(rawURL, target string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL 必须是 postgres/postgresql URL；schema snapshot 仅支持 PostgreSQL 方言，不支持 SQLite")
	}
	query := parsed.Query()
	query.Set("application_name", "juhe-ai-schema-snapshot-"+target)
	query.Set("connect_timeout", "10")
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 PostgreSQL schema snapshot 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// writeSnapshotJSON mirrors the Node `JSON.stringify(snapshot, null, 2)` +
// "\n" stdout contract. HTML escaping is disabled so <, > and & stay literal
// like JavaScript output.
func writeSnapshotJSON(snapshot schemasnapshot.SchemaSnapshot) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		return fmt.Errorf("encode schema snapshot: %w", err)
	}
	return nil
}
