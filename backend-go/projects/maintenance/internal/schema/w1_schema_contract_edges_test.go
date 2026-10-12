package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const w1PGCapabilityDomainCheck = "CHECK ((capability_domain = ANY (ARRAY['chat','video','audio','image'])))"

func w1PostgresShapeResult(t *testing.T, columnRows, checkRows [][]driver.Value) error {
	t.Helper()
	rec := &wmSchemaRecorder{}
	rec.script("FROM information_schema.columns", nil, columnRows)
	rec.script("FROM information_schema.table_constraints", nil, checkRows)
	db := openWMSchemaFakeDB(rec)
	t.Cleanup(func() { _ = db.Close() })
	return VerifyPostgresBusinessCapabilityDomainShape(context.Background(), db)
}

func TestW1PostgresCapabilityDomainShapeEdges(t *testing.T) {
	validColumn := [][]driver.Value{{"text", "NO", "'chat'::text"}}
	validCheck := [][]driver.Value{{w1PGCapabilityDomainCheck}}
	cases := []struct {
		name       string
		columnRows [][]driver.Value
		checkRows  [][]driver.Value
		wantError  string
	}{
		{name: "missing column is accepted", columnRows: [][]driver.Value{}, checkRows: [][]driver.Value{}},
		{name: "complete normalized check", columnRows: validColumn, checkRows: validCheck},
		{name: "wrong type", columnRows: [][]driver.Value{{"character varying", "NO", "'chat'::text"}}, wantError: "列类型"},
		{name: "nullable", columnRows: [][]driver.Value{{"text", "YES", "'chat'::text"}}, wantError: "列可空"},
		{name: "missing default", columnRows: [][]driver.Value{{"text", "NO", nil}}, wantError: "默认值为 NULL（无默认值）"},
		{name: "wrong default", columnRows: [][]driver.Value{{"text", "NO", "'video'::text"}}, wantError: "默认值为 'video'::text"},
		{name: "no check", columnRows: validColumn, checkRows: [][]driver.Value{}, wantError: "未找到该列的 CHECK 约束"},
		{name: "incomplete enum", columnRows: validColumn, checkRows: [][]driver.Value{{"CHECK (capability_domain IN ('chat','video'))"}}, wantError: "CHECK 约束缺少四值枚举"},
		{name: "enum values without column", columnRows: validColumn, checkRows: [][]driver.Value{{"CHECK (code IN ('chat','video','audio','image'))"}}, wantError: "CHECK 约束缺少四值枚举"},
		{name: "column scan error", columnRows: [][]driver.Value{{"text", nil, "'chat'::text"}}, wantError: "converting NULL to string is unsupported"},
		{name: "check scan error", columnRows: validColumn, checkRows: [][]driver.Value{{nil}}, wantError: "converting NULL to string is unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := w1PostgresShapeResult(t, tc.columnRows, tc.checkRows)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("VerifyPostgresBusinessCapabilityDomainShape: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}

	rec := &wmSchemaRecorder{}
	rec.script("FROM information_schema.columns", nil, validColumn)
	rec.scriptNextError("FROM information_schema.table_constraints", [][]driver.Value{{w1PGCapabilityDomainCheck}})
	db := openWMSchemaFakeDB(rec)
	t.Cleanup(func() { _ = db.Close() })
	if err := VerifyPostgresBusinessCapabilityDomainShape(context.Background(), db); err == nil || !strings.Contains(err.Error(), "wm fake rows: 注入迭代错误") {
		t.Fatalf("rows.Err must be preserved: %v", err)
	}
	closed := openWMSchemaFakeDB(&wmSchemaRecorder{})
	if err := closed.Close(); err != nil {
		t.Fatalf("close fake DB: %v", err)
	}
	if err := VerifyPostgresBusinessCapabilityDomainShape(context.Background(), closed); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("query error must be preserved, got %v", err)
	}
}

func TestW1EnsurePostgresCapabilityDomainColumnExecutesAndPreservesErrors(t *testing.T) {
	const wantAlter = `ALTER TABLE "juhe_business"."accounts" ADD COLUMN IF NOT EXISTS capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))`
	t.Run("add and verify", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		rec.script("FROM information_schema.columns", nil, [][]driver.Value{{"text", "NO", "'chat'::text"}})
		rec.script("FROM information_schema.table_constraints", nil, [][]driver.Value{{w1PGCapabilityDomainCheck}})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		if err := EnsurePostgresBusinessCapabilityDomainColumn(context.Background(), db); err != nil {
			t.Fatalf("EnsurePostgresBusinessCapabilityDomainColumn: %v", err)
		}
		queries := rec.execQueries()
		if len(queries) != 1 || queries[0] != wantAlter {
			t.Fatalf("executed ALTER = %#v, want %#v", queries, []string{wantAlter})
		}
	})

	t.Run("add failure keeps original error", func(t *testing.T) {
		original := errors.New("injected PostgreSQL DDL error")
		rec := &wmSchemaRecorder{failNextExecsWith: []error{original}}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		err := EnsurePostgresBusinessCapabilityDomainColumn(context.Background(), db)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "add accounts.capability_domain") {
			t.Fatalf("wrapped DDL error = %v, want original error and operation context", err)
		}
		if rec.queryCount() != 0 {
			t.Fatalf("shape verification must not run after ALTER failure, queries=%d", rec.queryCount())
		}
	})
}

func TestW1EnsurePostgresReturnsFinalCapabilityShapeError(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script("FROM information_schema.columns", nil, [][]driver.Value{{"character varying", "NO", "'chat'::text"}})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	result, err := EnsurePostgres(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "verify accounts.capability_domain shape") || !strings.Contains(err.Error(), "列类型") {
		t.Fatalf("EnsurePostgres shape error = %v, want wrapped final verifier error", err)
	}
	if result != (PGResult{}) {
		t.Fatalf("failed final shape verification must return zero result, got %+v", result)
	}
}

func TestW1SQLiteCapabilityDomainShapeEdges(t *testing.T) {
	const check = "CHECK (capability_domain IN ('chat','video','audio','image'))"
	cases := []struct {
		name      string
		decl      string
		wantError string
	}{
		{name: "valid", decl: "TEXT NOT NULL DEFAULT 'chat' " + check},
		{name: "wrong type", decl: "BLOB NOT NULL DEFAULT 'chat' " + check, wantError: "列类型"},
		{name: "nullable", decl: "TEXT DEFAULT 'chat' " + check, wantError: "列可空"},
		{name: "missing default", decl: "TEXT NOT NULL " + check, wantError: "默认值为 NULL（无默认值）"},
		{name: "wrong default", decl: "TEXT NOT NULL DEFAULT 'video' " + check, wantError: "默认值为 'video'"},
		{name: "missing check", decl: "TEXT NOT NULL DEFAULT 'chat'", wantError: "建表文本缺少列级 CHECK 判据"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openSharedMemorySQLite(t, "w1-capability-shape-"+strings.ReplaceAll(tc.name, " ", "-"))
			if _, err := db.Exec("CREATE TABLE accounts (capability_domain " + tc.decl + ")"); err != nil {
				t.Fatalf("create shape fixture: %v", err)
			}
			err := VerifySQLiteCapabilityDomainShape(context.Background(), db)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("VerifySQLiteCapabilityDomainShape: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}

	if got := sqliteShapeDefaultText(sql.NullString{}); got != "NULL（无默认值）" {
		t.Fatalf("sqlite null default text = %q", got)
	}
	if got := sqliteShapeDefaultText(sql.NullString{String: "'chat'", Valid: true}); got != "'chat'" {
		t.Fatalf("sqlite valid default text = %q", got)
	}
}

func TestW1SQLiteCapabilityDomainGuardAddsLegacyColumn(t *testing.T) {
	ctx := context.Background()
	db := openSharedMemorySQLite(t, "w1-capability-guard-legacy")
	if err := EnsureSQLiteBusinessCapabilityDomainColumn(ctx, db); err != nil {
		t.Fatalf("missing accounts table is a no-op: %v", err)
	}
	if err := VerifySQLiteCapabilityDomainShape(ctx, db); err == nil || !strings.Contains(err.Error(), "列缺失") {
		t.Fatalf("missing column shape error = %v", err)
	}
	if _, err := db.Exec("CREATE TABLE accounts (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("create legacy accounts: %v", err)
	}
	if _, err := db.Exec("INSERT INTO accounts (id) VALUES ('legacy')"); err != nil {
		t.Fatalf("insert legacy account: %v", err)
	}
	if err := EnsureSQLiteBusinessCapabilityDomainColumn(ctx, db); err != nil {
		t.Fatalf("add legacy capability_domain: %v", err)
	}
	var got string
	if err := db.QueryRow("SELECT capability_domain FROM accounts WHERE id = 'legacy'").Scan(&got); err != nil {
		t.Fatalf("read legacy default: %v", err)
	}
	if got != "chat" {
		t.Fatalf("legacy capability_domain = %q, want chat", got)
	}
	if err := EnsureSQLiteBusinessCapabilityDomainColumn(ctx, db); err != nil {
		t.Fatalf("repeated ensure must be idempotent: %v", err)
	}
	if _, err := db.Exec("UPDATE accounts SET capability_domain = 'other' WHERE id = 'legacy'"); err == nil {
		t.Fatal("legacy-added CHECK must reject values outside the four-value enum")
	}
	closed, err := sql.Open("sqlite", "file:w1-capability-guard-closed?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	if err := EnsureSQLiteBusinessCapabilityDomainColumn(ctx, closed); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("capability guard query error must be preserved, got %v", err)
	}
}

func TestW1SQLiteColumnGuardSemanticsAndErrors(t *testing.T) {
	ctx := context.Background()
	db := openSharedMemorySQLite(t, "w1-column-guard")
	if err := ensureSQLiteTableColumn(ctx, db, "missing_table", "extra", "TEXT"); err != nil {
		t.Fatalf("missing table is a no-op: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE guarded (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO guarded (id) VALUES ('row-1')"); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	decl := "TEXT NOT NULL DEFAULT 'fresh' CHECK (extra IN ('fresh','stale'))"
	if err := ensureSQLiteTableColumn(ctx, db, "guarded", "extra", decl); err != nil {
		t.Fatalf("add guarded column: %v", err)
	}
	var value string
	if err := db.QueryRow("SELECT extra FROM guarded WHERE id='row-1'").Scan(&value); err != nil || value != "fresh" {
		t.Fatalf("existing row default = %q, err=%v", value, err)
	}
	if err := ensureSQLiteTableColumn(ctx, db, "guarded", "extra", decl); err != nil {
		t.Fatalf("existing column must be an idempotent no-op: %v", err)
	}
	if _, err := db.Exec("UPDATE guarded SET extra='other' WHERE id='row-1'"); err == nil {
		t.Fatal("column CHECK must reject values outside its enum")
	}

	if _, err := db.Exec("CREATE TABLE strict_legacy (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("create strict legacy table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO strict_legacy (id) VALUES ('row-1')"); err != nil {
		t.Fatalf("insert strict legacy row: %v", err)
	}
	err := ensureSQLiteTableColumn(ctx, db, "strict_legacy", "required_value", "TEXT NOT NULL")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cannot add a not null column with default value null") {
		t.Fatalf("ALTER error = %v, want original SQLite constraint error", err)
	}

	closed, err := sql.Open("sqlite", "file:w1-column-guard-closed?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	if err := ensureSQLiteTableColumn(ctx, closed, "guarded", "new_value", "TEXT"); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("guard query error must be preserved, got %v", err)
	}
	if _, err := sqliteTableColumnSet(ctx, closed, "guarded"); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("column-set query error must be preserved, got %v", err)
	}
}

func TestW1SQLiteGuardWrappersPreserveDDLFailure(t *testing.T) {
	cases := []struct {
		name   string
		view   string
		ensure func(context.Context, *sql.DB) error
		prefix string
	}{
		{
			name:   "business custom question columns",
			view:   "CREATE VIEW model_quality_policies AS SELECT 1 AS revision",
			ensure: ensureSQLiteBusinessCustomQuestionColumns,
			prefix: "ensure model_quality_policies.custom_question_ids",
		},
		{
			name:   "stats success cost columns",
			view:   "CREATE VIEW usage_stats_totals AS SELECT 1 AS system_account_id",
			ensure: ensureSQLiteStatsSuccessCostColumns,
			prefix: "ensure usage_stats_totals.success_cost_usd",
		},
		{
			name:   "stats media columns",
			view:   "CREATE VIEW usage_stats_totals AS SELECT 1 AS system_account_id",
			ensure: ensureSQLiteStatsMediaColumns,
			prefix: "ensure usage_stats_totals.input_audio_tokens",
		},
		{
			name:   "chat account binding columns",
			view:   "CREATE VIEW chat_conversations AS SELECT 1 AS id",
			ensure: ensureSQLiteChatAccountBindingColumns,
			prefix: "ensure chat_conversations.bind_account_id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openSharedMemorySQLite(t, "w1-guard-view-"+strings.ReplaceAll(tc.name, " ", "-"))
			if _, err := db.Exec(tc.view); err != nil {
				t.Fatalf("create incompatible view fixture: %v", err)
			}
			err := tc.ensure(context.Background(), db)
			if err == nil || !strings.Contains(err.Error(), tc.prefix) || !strings.Contains(strings.ToLower(err.Error()), "cannot add a column to a view") {
				t.Fatalf("guard error = %v, want operation prefix %q and original SQLite error", err, tc.prefix)
			}
		})
	}
}

func TestW1SQLiteEnsureEntryErrorsRetainOriginalCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ensure func(context.Context, *sql.DB) (SchemaCounts, error)
	}{
		{name: "business", ensure: EnsureSQLiteBusiness},
		{name: "stats", ensure: EnsureSQLiteStats},
		{name: "chat", ensure: EnsureSQLiteChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:w1-closed-"+tc.name+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close sqlite: %v", err)
			}
			counts, err := tc.ensure(context.Background(), db)
			if err == nil || counts != (SchemaCounts{}) || !strings.Contains(err.Error(), "sqlite schema statement 0") || !strings.Contains(err.Error(), "database is closed") {
				t.Fatalf("ensure result=(%+v, %v), want statement context and original closed DB error", counts, err)
			}
		})
	}
}

func TestW1SQLiteChatAssetsRebuildRollsBackOnCopyFailure(t *testing.T) {
	db := openSharedMemorySQLite(t, "w1-chat-assets-rollback")
	if _, err := db.Exec(legacyChatAssetsMediaDDL); err != nil {
		t.Fatalf("create legacy chat_assets: %v", err)
	}
	const legacyRow = `INSERT INTO chat_assets (
		id, system_account_id, conversation_id, original_filename, original_mime_type,
		original_bytes, original_sha256, quota_bytes, cleanup_attempt_count, created_at, updated_at, expires_at
	) VALUES (
		'asset-invalid-for-new-check', 'sys-1', 'conv-1', 'a.bin', 'application/octet-stream',
		10, '0000000000000000000000000000000000000000000000000000000000000000', 10, -1,
		'2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z'
	)`
	if _, err := db.Exec(legacyRow); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	var beforeDDL string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='chat_assets'").Scan(&beforeDDL); err != nil {
		t.Fatalf("read old chat_assets DDL: %v", err)
	}
	err := ensureSQLiteChatAssetsMediaCheck(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "回填 chat_assets 重建表数据失败") || !strings.Contains(strings.ToLower(err.Error()), "constraint") || !strings.Contains(err.Error(), "cleanup_attempt_count") {
		t.Fatalf("rebuild error = %v, want copy-check failure and operation context", err)
	}
	var afterDDL string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='chat_assets'").Scan(&afterDDL); err != nil {
		t.Fatalf("read chat_assets after rollback: %v", err)
	}
	if afterDDL != beforeDDL {
		t.Fatal("failed rebuild must preserve the original chat_assets definition")
	}
	var attempts, stagingTables int
	if err := db.QueryRow("SELECT cleanup_attempt_count FROM chat_assets WHERE id='asset-invalid-for-new-check'").Scan(&attempts); err != nil || attempts != -1 {
		t.Fatalf("failed rebuild must preserve the original row: attempts=%d err=%v", attempts, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chat_assets_migrating'").Scan(&stagingTables); err != nil || stagingTables != 0 {
		t.Fatalf("failed rebuild must roll back staging table: count=%d err=%v", stagingTables, err)
	}
}

func TestW1SQLiteStatsEnsureCreatesMarkerWithoutBackfill(t *testing.T) {
	db := openSharedMemorySQLite(t, "w1-stats-marker-no-backfill")
	ctx := context.Background()
	if _, err := EnsureSQLiteStats(ctx, db); err != nil {
		t.Fatalf("initial stats ensure: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO usage_stats_totals (
		system_account_id, scope_type, scope_id, request_count, success_count, error_count,
		input_tokens, output_tokens, total_cost_usd, success_cost_usd, updated_at
	) VALUES ('sys-1', 'api_key', 'key-1', 1, 1, 0, 0, 0, 12.5, 0, '2026-10-12T00:00:00Z')`); err != nil {
		t.Fatalf("seed pending legacy projection: %v", err)
	}
	if _, err := EnsureSQLiteStats(ctx, db); err != nil {
		t.Fatalf("repeat stats ensure: %v", err)
	}
	var totalCost, successCost float64
	if err := db.QueryRow(`SELECT total_cost_usd, success_cost_usd FROM usage_stats_totals WHERE scope_id='key-1'`).Scan(&totalCost, &successCost); err != nil {
		t.Fatalf("read usage projection: %v", err)
	}
	if totalCost != 12.5 || successCost != 0 {
		t.Fatalf("ensure must not backfill historical costs: total=%v success=%v", totalCost, successCost)
	}
	var markerCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM backfill_progress").Scan(&markerCount); err != nil {
		t.Fatalf("backfill_progress must be created by stats ensure: %v", err)
	}
	if markerCount != 0 {
		t.Fatalf("stats ensure must not create migration markers, got %d", markerCount)
	}
}

func w1SQLiteTableInfoRow(name string, declaredType string, notNull int, defaultValue driver.Value) []driver.Value {
	return []driver.Value{int64(0), name, declaredType, int64(notNull), defaultValue, int64(0)}
}

func TestW1SQLiteMetadataGuardsPreserveScanAndIterationErrors(t *testing.T) {
	ctx := context.Background()
	validCapability := w1SQLiteTableInfoRow("capability_domain", "TEXT", 1, "'chat'")
	scanFailure := []driver.Value{nil, "id", "TEXT", int64(0), nil, int64(0)}

	for _, tc := range []struct {
		name  string
		match string
		rows  [][]driver.Value
		call  func(*sql.DB) error
	}{
		{
			name:  "column set scan failure",
			match: "PRAGMA table_info(guarded)",
			rows:  [][]driver.Value{scanFailure},
			call: func(db *sql.DB) error {
				_, err := sqliteTableColumnSet(ctx, db, "guarded")
				return err
			},
		},
		{
			name:  "column set iteration failure",
			match: "PRAGMA table_info(guarded)",
			rows:  [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)},
			call: func(db *sql.DB) error {
				_, err := sqliteTableColumnSet(ctx, db, "guarded")
				return err
			},
		},
		{
			name:  "column guard scan failure",
			match: "PRAGMA table_info(guarded)",
			rows:  [][]driver.Value{scanFailure},
			call: func(db *sql.DB) error {
				return ensureSQLiteTableColumn(ctx, db, "guarded", "extra", "TEXT")
			},
		},
		{
			name:  "column guard iteration failure",
			match: "PRAGMA table_info(guarded)",
			rows:  [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)},
			call: func(db *sql.DB) error {
				return ensureSQLiteTableColumn(ctx, db, "guarded", "extra", "TEXT")
			},
		},
		{
			name:  "capability guard scan failure",
			match: "PRAGMA table_info(accounts)",
			rows:  [][]driver.Value{scanFailure},
			call: func(db *sql.DB) error {
				return EnsureSQLiteBusinessCapabilityDomainColumn(ctx, db)
			},
		},
		{
			name:  "capability guard iteration failure",
			match: "PRAGMA table_info(accounts)",
			rows:  [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)},
			call: func(db *sql.DB) error {
				return EnsureSQLiteBusinessCapabilityDomainColumn(ctx, db)
			},
		},
		{
			name:  "shape verifier scan failure",
			match: "PRAGMA table_info(accounts)",
			rows:  [][]driver.Value{scanFailure},
			call: func(db *sql.DB) error {
				return VerifySQLiteCapabilityDomainShape(ctx, db)
			},
		},
		{
			name:  "shape verifier iteration failure",
			match: "PRAGMA table_info(accounts)",
			rows:  [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)},
			call: func(db *sql.DB) error {
				return VerifySQLiteCapabilityDomainShape(ctx, db)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &wmSchemaRecorder{}
			rec.scriptNextError(tc.match, tc.rows)
			db := openWMSchemaFakeDB(rec)
			t.Cleanup(func() { _ = db.Close() })
			err := tc.call(db)
			if err == nil {
				t.Fatal("expected metadata iteration error")
			}
			if strings.Contains(tc.name, "scan") {
				if !strings.Contains(err.Error(), "converting NULL") {
					t.Fatalf("scan error = %v, want original conversion error", err)
				}
				return
			}
			if !strings.Contains(err.Error(), "wm fake rows: 注入迭代错误") {
				t.Fatalf("iteration error = %v, want original fake driver error", err)
			}
		})
	}

	t.Run("capability alter failure keeps original cause", func(t *testing.T) {
		original := errors.New("injected capability ALTER failure")
		rec := &wmSchemaRecorder{failNextExecsWith: []error{original}}
		rec.script("PRAGMA table_info(accounts)", nil, [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		err := EnsureSQLiteBusinessCapabilityDomainColumn(ctx, db)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "add accounts.capability_domain") {
			t.Fatalf("ALTER error = %v, want wrapped original cause", err)
		}
	})

	t.Run("shape DDL lookup failure keeps operation context", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		rec.script("PRAGMA table_info(accounts)", nil, [][]driver.Value{validCapability})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		err := VerifySQLiteCapabilityDomainShape(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "读取 accounts 建表文本失败") || !strings.Contains(err.Error(), "sql: no rows in result set") {
			t.Fatalf("shape DDL error = %v, want lookup context and original no-row cause", err)
		}
	})

	t.Run("shape default type drift is reported", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		rec.script("PRAGMA table_info(accounts)", nil, [][]driver.Value{w1SQLiteTableInfoRow("capability_domain", "TEXT", 1, int64(1))})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		err := VerifySQLiteCapabilityDomainShape(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "dflt_value 类型异常 int64") {
			t.Fatalf("shape default type error = %v, want unsupported metadata type", err)
		}
	})
}

func TestW1SQLiteEnsureWrappersPreserveGuardFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("business schedule rebuild metadata iteration", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		rec.script("SELECT sql FROM sqlite_master WHERE type='table' AND name='model_quality_schedules'", nil,
			[][]driver.Value{{sqliteScheduleIntervalLegacyCheckText}})
		rec.script("PRAGMA table_info(model_quality_schedules)", nil,
			[][]driver.Value{w1SQLiteTableInfoRow("custom_question_ids", "TEXT", 0, nil)})
		rec.scriptNextError("PRAGMA table_info(model_quality_schedules)", [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := EnsureSQLiteBusiness(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "ensure sqlite business schedule interval check") || !strings.Contains(err.Error(), "wm fake rows: 注入迭代错误") {
			t.Fatalf("business ensure error = %v, want migration context and original iteration error", err)
		}
	})

	t.Run("stats success-cost guard ALTER", func(t *testing.T) {
		rec := &wmSchemaRecorder{failExecAfter: len(sqliteStatsScript)}
		rec.script("PRAGMA table_info(usage_stats_totals)", nil, [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := EnsureSQLiteStats(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "ensure sqlite stats success_cost_usd columns") || !strings.Contains(err.Error(), "wm schema fake: 注入执行失败") {
			t.Fatalf("stats ensure error = %v, want success-cost guard context and original DDL error", err)
		}
	})

	t.Run("stats media guard ALTER", func(t *testing.T) {
		rec := &wmSchemaRecorder{failExecAfter: len(sqliteStatsScript)}
		for _, table := range sqliteStatsSuccessCostGuardTables {
			rec.script("PRAGMA table_info("+table+")", nil, [][]driver.Value{
				w1SQLiteTableInfoRow("id", "TEXT", 0, nil),
				w1SQLiteTableInfoRow("success_cost_usd", "REAL", 1, int64(0)),
			})
		}
		for _, table := range sqliteStatsMediaColumnTables {
			rec.script("PRAGMA table_info("+table+")", nil, [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := EnsureSQLiteStats(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "ensure sqlite stats media metering columns") || !strings.Contains(err.Error(), "wm schema fake: 注入执行失败") {
			t.Fatalf("stats ensure error = %v, want media guard context and original DDL error", err)
		}
	})

	t.Run("chat binding guard ALTER", func(t *testing.T) {
		rec := &wmSchemaRecorder{failExecAfter: len(sqliteChatScript)}
		rec.script("PRAGMA table_info(chat_conversations)", nil, [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := EnsureSQLiteChat(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "ensure sqlite chat account-binding columns") || !strings.Contains(err.Error(), "wm schema fake: 注入执行失败") {
			t.Fatalf("chat ensure error = %v, want account-binding guard context and original DDL error", err)
		}
	})

	t.Run("chat asset migration metadata error", func(t *testing.T) {
		rec := &wmSchemaRecorder{
			failExecAfter: len(sqliteChatScript) + len(sqliteChatAccountBindingColumns),
		}
		for range sqliteChatAccountBindingColumns {
			rec.script("PRAGMA table_info(chat_conversations)", nil, [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		}
		rec.script("SELECT sql FROM sqlite_master WHERE type='table' AND name='chat_assets'", nil,
			[][]driver.Value{{sqliteChatAssetsLegacyMimeCheckText}})
		rec.scriptNextError("PRAGMA table_info(chat_assets)", [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		_, err := EnsureSQLiteChat(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "ensure sqlite chat_assets media check") || !strings.Contains(err.Error(), "wm fake rows: 注入迭代错误") {
			t.Fatalf("chat ensure error = %v, want asset migration context and original iteration error", err)
		}
	})
}

func TestW1SQLiteBusinessGuardErrorsRetainTargetContext(t *testing.T) {
	ctx := context.Background()
	scanFailure := []driver.Value{nil, "id", "TEXT", int64(0), nil, int64(0)}
	questionBuiltin := w1SQLiteTableInfoRow("is_builtin", "INTEGER", 1, int64(0))
	questionLastTest := w1SQLiteTableInfoRow("last_test_id", "TEXT", 0, nil)
	capabilityDrift := w1SQLiteTableInfoRow("capability_domain", "BLOB", 1, "'chat'")

	for _, tc := range []struct {
		name        string
		wantContext string
		setup       func(*wmSchemaRecorder)
	}{
		{
			name:        "custom question columns",
			wantContext: "ensure sqlite business custom_question_ids columns",
			setup: func(rec *wmSchemaRecorder) {
				rec.scriptNextError("PRAGMA table_info(model_quality_policies)", [][]driver.Value{scanFailure})
			},
		},
		{
			name:        "builtin flag column",
			wantContext: "ensure model_check_question_bank.is_builtin",
			setup: func(rec *wmSchemaRecorder) {
				rec.scriptNextError("PRAGMA table_info(model_check_question_bank)", [][]driver.Value{scanFailure})
			},
		},
		{
			name:        "last test id column",
			wantContext: "ensure model_check_question_bank.last_test_id",
			setup: func(rec *wmSchemaRecorder) {
				rec.script("PRAGMA table_info(model_check_question_bank)", nil, [][]driver.Value{questionBuiltin})
				rec.scriptNextError("PRAGMA table_info(model_check_question_bank)", [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 1, nil)})
			},
		},
		{
			name:        "last test json column",
			wantContext: "ensure model_check_question_bank.last_test_json",
			setup: func(rec *wmSchemaRecorder) {
				rec.script("PRAGMA table_info(model_check_question_bank)", nil, [][]driver.Value{questionBuiltin})
				rec.script("PRAGMA table_info(model_check_question_bank)", nil, [][]driver.Value{questionLastTest})
				rec.scriptNextError("PRAGMA table_info(model_check_question_bank)", [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 1, nil)})
			},
		},
		{
			name:        "system account expedited limit",
			wantContext: "ensure system_accounts.expedited_account_limit",
			setup: func(rec *wmSchemaRecorder) {
				rec.scriptNextError("PRAGMA table_info(system_accounts)", [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
			},
		},
		{
			name:        "account expedited recovery",
			wantContext: "ensure accounts.expedited_recovery_enabled",
			setup: func(rec *wmSchemaRecorder) {
				rec.scriptNextError("PRAGMA table_info(accounts)", [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
			},
		},
		{
			name:        "capability shape drift",
			wantContext: "ensure accounts.capability_domain",
			setup: func(rec *wmSchemaRecorder) {
				rec.script("PRAGMA table_info(accounts)", nil, nil)
				rec.script("PRAGMA table_info(accounts)", nil, [][]driver.Value{capabilityDrift})
				rec.script("PRAGMA table_info(accounts)", nil, [][]driver.Value{capabilityDrift})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &wmSchemaRecorder{}
			tc.setup(rec)
			db := openWMSchemaFakeDB(rec)
			defer db.Close()
			_, err := EnsureSQLiteBusiness(ctx, db)
			if err == nil || !strings.Contains(err.Error(), tc.wantContext) {
				t.Fatalf("business ensure error = %v, want context %q", err, tc.wantContext)
			}
		})
	}
}

func TestW1SQLiteMigrationForeignKeyDisableErrorsPreserveCause(t *testing.T) {
	for _, tc := range []struct {
		name       string
		marker     string
		columns    string
		call       func(context.Context, *sql.DB) error
		wantPrefix string
	}{
		{
			name:       "schedule",
			marker:     sqliteScheduleIntervalLegacyCheckText,
			columns:    "PRAGMA table_info(model_quality_schedules)",
			call:       ensureSQLiteBusinessScheduleIntervalCheck,
			wantPrefix: "关闭 PRAGMA foreign_keys 失败",
		},
		{
			name:       "chat assets",
			marker:     sqliteChatAssetsLegacyMimeCheckText,
			columns:    "PRAGMA table_info(chat_assets)",
			call:       ensureSQLiteChatAssetsMediaCheck,
			wantPrefix: "关闭 PRAGMA foreign_keys 失败",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := errors.New("injected PRAGMA foreign_keys error")
			rec := &wmSchemaRecorder{failNextExecsWith: []error{original}}
			name := strings.TrimPrefix(tc.columns, "PRAGMA table_info(")
			name = strings.TrimSuffix(name, ")")
			rec.script("SELECT sql FROM sqlite_master WHERE type='table' AND name='"+name+"'", nil, [][]driver.Value{{tc.marker}})
			rec.script(tc.columns, nil, [][]driver.Value{w1SQLiteTableInfoRow("id", "TEXT", 0, nil)})
			rec.script("PRAGMA foreign_keys", nil, [][]driver.Value{{int64(1)}})
			db := openWMSchemaFakeDB(rec)
			defer db.Close()
			err := tc.call(context.Background(), db)
			if !errors.Is(err, original) || !strings.Contains(err.Error(), tc.wantPrefix) {
				t.Fatalf("migration error = %v, want wrapped original PRAGMA error", err)
			}
		})
	}
}

func TestW1SQLiteMigrationRejectsUnexpectedStagingView(t *testing.T) {
	for _, tc := range []struct {
		name    string
		create  string
		staging string
		call    func(context.Context, *sql.DB) error
	}{
		{
			name: "schedule",
			create: `CREATE TABLE model_quality_schedules (
				id TEXT PRIMARY KEY,
				interval_minutes INTEGER NOT NULL CHECK (interval_minutes BETWEEN 10 AND 10080)
			)`,
			staging: "model_quality_schedules_migrating",
			call:    ensureSQLiteBusinessScheduleIntervalCheck,
		},
		{
			name:    "chat assets",
			create:  legacyChatAssetsMediaDDL,
			staging: "chat_assets_migrating",
			call:    ensureSQLiteChatAssetsMediaCheck,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openSharedMemorySQLite(t, "w1-staging-view-"+tc.name)
			if _, err := db.Exec(tc.create); err != nil {
				t.Fatalf("create legacy table: %v", err)
			}
			if _, err := db.Exec("CREATE VIEW " + tc.staging + " AS SELECT 1 AS id"); err != nil {
				t.Fatalf("create colliding staging view: %v", err)
			}
			err := tc.call(context.Background(), db)
			if err == nil || !strings.Contains(err.Error(), "清理上次重建残留失败") || !strings.Contains(strings.ToLower(err.Error()), "view") {
				t.Fatalf("migration error = %v, want staging-object conflict and operation context", err)
			}
		})
	}
}

func TestW1SQLiteBusinessScheduleMigrationCopyFailureRollsBack(t *testing.T) {
	db := openSharedMemorySQLite(t, "w1-schedule-copy-rollback")
	ctx := context.Background()
	const legacyDDL = `CREATE TABLE model_quality_schedules (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 10 AND 10080),
		enabled INTEGER NOT NULL DEFAULT 1,
		next_run_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatalf("create incomplete legacy schedule: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id, system_account_id, interval_minutes, enabled, next_run_at, created_at)
		VALUES ('legacy-1', 'sys-1', 60, 1, '2026-10-12T00:00:00Z', '2026-10-12T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy schedule: %v", err)
	}
	var beforeDDL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='model_quality_schedules'`).Scan(&beforeDDL); err != nil {
		t.Fatalf("read legacy schedule DDL: %v", err)
	}
	err := ensureSQLiteBusinessScheduleIntervalCheck(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "回填 model_quality_schedules 重建表数据失败") || !strings.Contains(err.Error(), "model_quality_schedules_migrating.account_id") {
		t.Fatalf("migration error = %v, want required-column copy failure and operation context", err)
	}
	var afterDDL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='model_quality_schedules'`).Scan(&afterDDL); err != nil || afterDDL != beforeDDL {
		t.Fatalf("failed migration must preserve legacy table DDL: equal=%v err=%v", afterDDL == beforeDDL, err)
	}
	var count, stagingTables, foreignKeys int
	if err := db.QueryRow("SELECT COUNT(*) FROM model_quality_schedules WHERE id='legacy-1'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed migration must preserve legacy row: count=%d err=%v", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='model_quality_schedules_migrating'").Scan(&stagingTables); err != nil || stagingTables != 0 {
		t.Fatalf("failed migration must roll back staging table: count=%d err=%v", stagingTables, err)
	}
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("failed migration must restore foreign_keys: value=%d err=%v", foreignKeys, err)
	}
}

func TestW1SQLiteSchemaMigrationsPreserveClosedDatabaseErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(context.Context, *sql.DB) error
	}{
		{name: "schedule", call: ensureSQLiteBusinessScheduleIntervalCheck},
		{name: "chat assets", call: ensureSQLiteChatAssetsMediaCheck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:w1-migration-closed-"+strings.ReplaceAll(tc.name, " ", "-")+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close sqlite: %v", err)
			}
			err = tc.call(context.Background(), db)
			if err == nil || !strings.Contains(err.Error(), "database is closed") {
				t.Fatalf("migration error = %v, want original closed database error", err)
			}
		})
	}
}

func TestW1BuiltinQuestionSeedUpsertsPreserveFirstWriteErrors(t *testing.T) {
	t.Run("postgres", func(t *testing.T) {
		original := errors.New("injected PostgreSQL builtin question error")
		result := PGSeedResult{}
		attempts := 0
		err := seedPostgresBuiltinModelCheckQuestions(context.Background(), func(string, ...any) error {
			attempts++
			return original
		}, "2026-10-12T00:00:00.000Z", &result)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "builtin question") {
			t.Fatalf("PostgreSQL upsert error = %v, want original cause and question context", err)
		}
		if attempts != 1 || result.StatementCount != 0 || result.BuiltinQuestionRows != 0 {
			t.Fatalf("PostgreSQL failure must stop before later writes: attempts=%d result=%+v", attempts, result)
		}
	})

	t.Run("sqlite", func(t *testing.T) {
		original := errors.New("injected SQLite builtin question error")
		rec := &wmSchemaRecorder{failNextExecsWith: []error{original}}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		result := SQLiteSeedResult{}
		err := seedSQLiteBuiltinModelCheckQuestions(context.Background(), db, "2026-10-12T00:00:00.000Z", &result)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "builtin question") {
			t.Fatalf("SQLite upsert error = %v, want original cause and question context", err)
		}
		if rec.execAttemptCount() != 1 || rec.execCount() != 0 || result.StatementCount != 0 || result.BuiltinQuestionRows != 0 {
			t.Fatalf("SQLite failure must stop before later writes: attempts=%d successful=%d result=%+v", rec.execAttemptCount(), rec.execCount(), result)
		}
	})
}
