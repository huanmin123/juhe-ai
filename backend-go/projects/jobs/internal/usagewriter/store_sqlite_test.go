package usagewriter

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openStoreDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSqliteShardStoreWriteBatch(t *testing.T) {
	root := t.TempDir()
	catalog := openStoreDB(t, filepath.Join(root, "catalog.sqlite3"))
	business := openStoreDB(t, filepath.Join(root, "business.sqlite3"))
	if _, err := business.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, last_used_at TEXT, updated_at TEXT, deleted_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`INSERT INTO accounts (id) VALUES ('acc1')`); err != nil {
		t.Fatal(err)
	}

	store := NewSqliteShardStore(SqliteShardStoreConfig{
		CatalogDB:  catalog,
		ShardRoot:  root,
		ShardCount: 16,
		BusinessDB: business,
		Now: func() time.Time {
			parsed, _ := time.Parse(timeRFC3339Millis, "2026-01-02T03:04:05.000Z")
			return parsed
		},
	})
	if err := store.EnsureCatalogSchema(); err != nil {
		t.Fatal(err)
	}

	clock := fixedClock("2026-01-02T03:04:05.000Z")
	input := UsageRecordInput{
		SystemAccountID: "sys1", TraceID: "t1", TrafficSource: TrafficSourceGateway, Success: true,
		AccountID: "acc1", AccountOwnerSystemAccountID: "sys1", AccountAccessType: AccountAccessTypeOwner,
		Model: "gpt-5", CostUsd: floatPtr(0.25), InputTokens: intPtr(5),
	}
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, WritePlanOptions{
		CatalogSnapshotEnabled: true,
		ShardCount:             16,
		ShardRoot:              root,
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := store.WriteBatch(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d", inserted)
	}

	location := plan.RowsByShard[0].Location
	db, err := sql.Open("sqlite", location.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE trace_id = 't1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("usage rows = %d", count)
	}
	var snapshot any
	if err := db.QueryRow(`SELECT cost_breakdown_snapshot_json FROM usage_records WHERE trace_id = 't1'`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !strings.Contains(snapshot.(string), `"serviceTierPricingSource":"unknown"`) {
		t.Fatalf("snapshot json = %v", snapshot)
	}

	// Idempotent rewrite: ON CONFLICT(id) DO NOTHING keeps one row.
	inserted, err = store.WriteBatch(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 0 {
		t.Fatalf("rewrite inserted = %d, want 0", inserted)
	}

	// Catalog entries + registered shard location.
	var entries int
	if err := catalog.QueryRow(`SELECT COUNT(*) FROM usage_record_shard_entries WHERE trace_id = 't1'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("catalog entries = %d", entries)
	}
	var locations int
	if err := catalog.QueryRow(`SELECT COUNT(*) FROM usage_record_shards WHERE shard_key = ?`, location.ShardKey).Scan(&locations); err != nil {
		t.Fatal(err)
	}
	if locations != 1 {
		t.Fatalf("registered locations = %d", locations)
	}

	// Account side effect flushed to the business database.
	var lastUsed sql.NullString
	if err := business.QueryRow(`SELECT last_used_at FROM accounts WHERE id = 'acc1'`).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if !lastUsed.Valid || lastUsed.String != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("last_used_at = %v", lastUsed)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSqliteShardStoreSchemaUpgradeAddsColumn(t *testing.T) {
	root := t.TempDir()
	store := NewSqliteShardStore(SqliteShardStoreConfig{
		CatalogDB:  openStoreDB(t, filepath.Join(root, "catalog.sqlite3")),
		ShardRoot:  root,
		ShardCount: 16,
	})
	location := UsageRecordShardLocationForBucket("20260102", 3, root)
	// Pre-create an old-schema shard file without upstream_response_model.
	if err := os.MkdirAll(filepath.Dir(location.FilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", location.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(UsageShardBaseSchemaSQL); err != nil {
		t.Fatal(err)
	}
	// Roll the schema back to the pre-upstream_response_model generation.
	if _, err := old.Exec(`ALTER TABLE usage_records DROP COLUMN upstream_response_model`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	defer store.Close()

	db, err := store.openShardDB(location)
	if err != nil {
		t.Fatal(err)
	}
	var hasColumn bool
	rows, err := db.Query(`PRAGMA table_info(usage_records)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, dflt, pk any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "upstream_response_model" {
			hasColumn = true
		}
	}
	rows.Close()
	if !hasColumn {
		t.Fatal("upstream_response_model column not added")
	}
}

func TestWriterAgainstSqliteStoreEndToEnd(t *testing.T) {
	// 全链路（进程内队列 → 批量计划 → SQLite 分片落库 → 目录表），
	// 全部限制在 t.TempDir() 内。
	root := t.TempDir()
	catalog := openStoreDB(t, filepath.Join(root, "catalog.sqlite3"))
	store := NewSqliteShardStore(SqliteShardStoreConfig{
		CatalogDB:  catalog,
		ShardRoot:  root,
		ShardCount: 4,
	})
	if err := store.EnsureCatalogSchema(); err != nil {
		t.Fatal(err)
	}
	config := Config{BatchSize: 8, FlushIntervalMs: 60_000, ShardCount: 4, ShardRoot: root}
	writer, _ := newTestWriter(t, config, store)
	for i := 0; i < 8; i++ {
		input := gatewayInput(string(rune('a' + i)))
		input.ID = "usage_20260102_s" + FormatShardID(i%4) + "_1767225600000_e" + itoa(i)
		input.CreatedAt = "2026-01-02T03:04:05.000Z"
		if err := writer.Enqueue(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	writer.Close(ctx)

	if got := writer.Runtime().WrittenRecords; got != 8 {
		t.Fatalf("written = %d", got)
	}
	var entries int
	if err := catalog.QueryRow(`SELECT COUNT(*) FROM usage_record_shard_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 8 {
		t.Fatalf("catalog entries = %d", entries)
	}
	var shards int
	if err := catalog.QueryRow(`SELECT COUNT(*) FROM usage_record_shards WHERE status = 'active'`).Scan(&shards); err != nil {
		t.Fatal(err)
	}
	if shards != 4 {
		t.Fatalf("registered shards = %d, want 4", shards)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
