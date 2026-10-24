// Tests for the full PostgreSQL seed port (pg_seed.go): statement rendering
// without a server, plus the opt-in integration smoke behind the same env
// gate as the schema golden test.

package schema

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var pgSeedTestClock = time.Date(2026, 9, 4, 8, 0, 0, 123456000, time.UTC)

func TestBuildPostgresModelSeedUpsertRendering(t *testing.T) {
	rows, err := modelSeedTargetRows("2026-09-04")
	if err != nil {
		t.Fatalf("modelSeedTargetRows: %v", err)
	}
	query, args := buildPostgresModelSeedUpsert(rows, "2026-09-04T08:00:00.123Z")
	if got := len(rows) * 40; len(args) != got {
		t.Fatalf("bulk upsert arg count = %d, want %d", len(args), got)
	}
	if !strings.Contains(query, `INSERT INTO "juhe_business"."models"`) {
		t.Fatal("bulk upsert misses the qualified insert target")
	}
	if !strings.Contains(query, "ON CONFLICT (coalesce(vendor_code, ''), scope, coalesce(system_account_id, ''), upstream_model) DO UPDATE SET") {
		t.Fatal("bulk upsert misses the identity-index conflict clause")
	}
	for _, guard := range []string{"manual-override", "manual-visibility-override", "excluded.catalog_visible"} {
		if !strings.Contains(query, guard) {
			t.Fatalf("bulk upsert misses guard %q", guard)
		}
	}
	// scope/system_account_id/currency are per-row literals, never parameters.
	if got := strings.Count(query, "'builtin'"); got != len(rows) {
		t.Fatalf("'builtin' literal count = %d, want %d", got, len(rows))
	}
	if got := strings.Count(query, "'USD'"); got != len(rows) {
		t.Fatalf("'USD' literal count = %d, want %d", got, len(rows))
	}
	// 第二行行首元素格式校验：每行 40 参数，第二行从 $41 重启，且字面量列序
	// 与首行一致（'builtin' 后紧跟 system_account_id 的 NULL，再接 status）。
	if !strings.Contains(query, "),\n          ($41, $42, $43, $44, 'builtin', NULL, $45") {
		t.Fatal("bulk upsert second row misses the 40-parameter restart with builtin/NULL literals")
	}
	if !strings.Contains(query, "$"+strconv.Itoa(len(args))) {
		t.Fatalf("bulk upsert misses the final placeholder $%d", len(args))
	}
	if strings.Contains(query, "$"+strconv.Itoa(len(args)+1)) {
		t.Fatal("bulk upsert has a placeholder beyond the arg count")
	}
	// First row args mirror the first projected row.
	first := rows[0]
	if args[0] != first.ID || args[1] != first.VendorCode || args[2] != first.PublicModel || args[3] != first.UpstreamModel {
		t.Fatalf("first row identity args = %v/%v/%v/%v", args[0], args[1], args[2], args[3])
	}
	if args[4] != first.Status {
		t.Fatalf("first row status arg = %v, want %v", args[4], first.Status)
	}
	if args[39] != seedNullableFloat64(first.OutputUsdPerImage) {
		t.Fatalf("first row final arg = %v, want OutputUsdPerImage %v (currency 是语句内字面量)", args[39], first.OutputUsdPerImage)
	}
}

// --- models bulk upsert arity 防线（PG 与 SQLite 双侧）---

// splitSeedUpsertTopLevelCommas 按顶层逗号切分 VALUES 元组体：单引号字符串
// 字面量内的逗号与 '' 转义不参与切分；$n / ? 占位符按普通 token 返回。
func splitSeedUpsertTopLevelCommas(body string) []string {
	var elements []string
	var current strings.Builder
	inString := false
	for index := 0; index < len(body); index++ {
		switch ch := body[index]; {
		case inString:
			current.WriteByte(ch)
			if ch == '\'' {
				if index+1 < len(body) && body[index+1] == '\'' {
					current.WriteByte('\'')
					index++
					continue
				}
				inString = false
			}
		case ch == '\'':
			inString = true
			current.WriteByte(ch)
		case ch == ',':
			elements = append(elements, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	return append(elements, strings.TrimSpace(current.String()))
}

// scanSeedUpsertTuples 提取 VALUES 段内全部顶层 (...) 元组（单引号字面量内的
// 括号与逗号不参与结构计数）。
func scanSeedUpsertTuples(section string) []string {
	var tuples []string
	depth := 0
	inString := false
	start := -1
	for index := 0; index < len(section); index++ {
		switch ch := section[index]; {
		case inString:
			if ch == '\'' {
				if index+1 < len(section) && section[index+1] == '\'' {
					index++
					continue
				}
				inString = false
			}
		case ch == '\'':
			inString = true
		case ch == '(':
			if depth == 0 {
				start = index
			}
			depth++
		case ch == ')':
			depth--
			if depth == 0 && start >= 0 {
				tuples = append(tuples, section[start:index+1])
				start = -1
			}
		}
	}
	return tuples
}

// seedUpsertInsertColumnCount 解析 INSERT 语句的目标列清单（首个括号组，列名
// 是普通标识符），返回列数 N。
func seedUpsertInsertColumnCount(t *testing.T, insertStatement string) int {
	t.Helper()
	open := strings.Index(insertStatement, "(")
	if open < 0 {
		t.Fatalf("insert statement misses the column list: %.80s", insertStatement)
	}
	depth := 0
	inString := false
	for index := open; index < len(insertStatement); index++ {
		switch ch := insertStatement[index]; {
		case inString:
			if ch == '\'' {
				inString = false
			}
		case ch == '\'':
			inString = true
		case ch == '(':
			depth++
		case ch == ')':
			depth--
			if depth == 0 {
				columns := splitSeedUpsertTopLevelCommas(insertStatement[open+1 : index])
				if len(columns) == 0 {
					t.Fatalf("insert statement has an empty column list: %.120s", insertStatement)
				}
				return len(columns)
			}
		}
	}
	t.Fatalf("insert statement has an unterminated column list: %.120s", insertStatement)
	return 0
}

// TestPostgresModelSeedUpsertRowArityMatchesTargetColumns 是 PG models 批量
// upsert 的 arity 防线：目标列数 N 取自 pgSeedModelsColumns 本身，对渲染出的
// 每一行 VALUES 元组做顶层逗号元素计数，逐行断言 == N，且每行恰有 40 个 $n
// 占位符（40 参数 + 8 字面量）。历史 blocker：目标列 48 列而每行只渲染 43 值，
// PG 执行报 42601 且第 25 列起整体错位。
func TestPostgresModelSeedUpsertRowArityMatchesTargetColumns(t *testing.T) {
	columnCount := seedUpsertInsertColumnCount(t, pgSeedModelsColumns)
	rows, err := modelSeedTargetRows("2026-09-04")
	if err != nil {
		t.Fatalf("modelSeedTargetRows: %v", err)
	}
	query, args := buildPostgresModelSeedUpsert(rows, "2026-09-04T08:00:00.123Z")
	if len(args) != len(rows)*40 {
		t.Fatalf("bulk upsert arg count = %d, want %d", len(args), len(rows)*40)
	}
	valuesIndex := strings.Index(query, ") VALUES")
	conflictIndex := strings.Index(query, "ON CONFLICT")
	if valuesIndex < 0 || conflictIndex < 0 || valuesIndex > conflictIndex {
		t.Fatalf("bulk upsert misses the VALUES...ON CONFLICT frame: values=%d conflict=%d", valuesIndex, conflictIndex)
	}
	tuples := scanSeedUpsertTuples(query[valuesIndex+len(") VALUES") : conflictIndex])
	if len(tuples) != len(rows) {
		t.Fatalf("rendered VALUES tuples = %d, want %d", len(tuples), len(rows))
	}
	for index, tuple := range tuples {
		elements := splitSeedUpsertTopLevelCommas(strings.TrimSuffix(strings.TrimPrefix(tuple, "("), ")"))
		if len(elements) != columnCount {
			t.Fatalf("VALUES row %d element count = %d, want %d (pgSeedModelsColumns)", index+1, len(elements), columnCount)
		}
		placeholders := 0
		for _, element := range elements {
			if strings.HasPrefix(element, "$") {
				placeholders++
			}
		}
		if placeholders != 40 {
			t.Fatalf("VALUES row %d placeholder count = %d, want 40", index+1, placeholders)
		}
	}
}

// TestSQLiteModelSeedUpsertTupleArityMatchesTargetColumns 对 SQLite 同构实现
// （sqSeedModelsUpsert，PG 行模板的正确参照）做同样的 arity 断言：静态语句的
// 目标列数与单个 VALUES 元组元素数必须相等，且恰有 40 个 ? 占位符，防止两侧
// 行模板再次漂移。
func TestSQLiteModelSeedUpsertTupleArityMatchesTargetColumns(t *testing.T) {
	columnCount := seedUpsertInsertColumnCount(t, sqSeedModelsUpsert)
	const valuesMarker = ") VALUES ("
	valuesIndex := strings.Index(sqSeedModelsUpsert, valuesMarker)
	conflictIndex := strings.Index(sqSeedModelsUpsert, "ON CONFLICT")
	if valuesIndex < 0 || conflictIndex < 0 || valuesIndex > conflictIndex {
		t.Fatalf("sqSeedModelsUpsert misses the VALUES...ON CONFLICT frame: values=%d conflict=%d", valuesIndex, conflictIndex)
	}
	tuples := scanSeedUpsertTuples(sqSeedModelsUpsert[valuesIndex+len(valuesMarker)-1 : conflictIndex])
	if len(tuples) != 1 {
		t.Fatalf("sqSeedModelsUpsert VALUES tuples = %d, want 1", len(tuples))
	}
	elements := splitSeedUpsertTopLevelCommas(strings.TrimSuffix(strings.TrimPrefix(tuples[0], "("), ")"))
	if len(elements) != columnCount {
		t.Fatalf("sqSeedModelsUpsert VALUES element count = %d, want %d (target columns)", len(elements), columnCount)
	}
	placeholders := 0
	for _, element := range elements {
		if element == "?" {
			placeholders++
		}
	}
	if placeholders != 40 {
		t.Fatalf("sqSeedModelsUpsert placeholder count = %d, want 40", placeholders)
	}
}

func TestPostgresSeedStatementContracts(t *testing.T) {
	// The stale disable keeps the reference guards translated to the models
	// target form（支持关系/入口映射按 model_id、健康检查按 upstream_model 名，
	// manual 覆盖行保留）.
	for _, fragment := range []string{
		`UPDATE "juhe_business"."models"`,
		"jsonb_to_recordset($2::jsonb) AS built_in(vendor_code text, upstream_model text)",
		"source NOT IN ('manual-override', 'manual-visibility-override')",
		`"juhe_business"."account_supported_models"`,
		`"juhe_business"."account_entry_mappings"`,
		`"juhe_business"."accounts"`,
		"account.health_check_model = stale_model.upstream_model",
	} {
		if !strings.Contains(pgSeedStaleBuiltInModelsDisable, fragment) {
			t.Fatalf("stale disable misses %q", fragment)
		}
	}
	// The PostgreSQL default-group lookup orders by updated_at DESC (the
	// SQLite variant orders by created_at ASC — both match their Node dialect).
	if !strings.Contains(pgSeedAdminDefaultGroupSelect, "ORDER BY updated_at DESC, id ASC") {
		t.Fatal("pg default group select misses the updated_at DESC order")
	}
	if !strings.Contains(sqSeedDefaultGroupsSelect, "ORDER BY created_at ASC, id ASC") {
		t.Fatal("sqlite default group select misses the created_at ASC order")
	}
	// New PG inserts are all ON CONFLICT DO NOTHING (Node idempotency).
	for name, statement := range map[string]string{
		"adminRouteStrategy":   pgSeedAdminRouteStrategyInsert,
		"adminRouteBinding":    pgSeedAdminRouteStrategyGroupBindingInsert,
		"adminDefaultAPIKey":   pgSeedAdminDefaultAPIKeyInsert,
		"externalSourceInsert": pgSeedExternalIntegrationSourceInsert,
	} {
		if !strings.Contains(statement, "ON CONFLICT DO NOTHING") {
			t.Fatalf("%s insert misses ON CONFLICT DO NOTHING", name)
		}
	}
	// The built-in model key list serializes like the identity-key payload.
	encoded, err := json.Marshal([]pgSeedBuiltInModelKey{{VendorCode: "gpt", UpstreamModel: "gpt-5.6-sol"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `[{"vendor_code":"gpt","upstream_model":"gpt-5.6-sol"}]` {
		t.Fatalf("built-in key payload = %s", encoded)
	}
}

// TestSeedPostgresDefaultsSmoke is the opt-in full-seed integration test. It
// only runs when JUHE_AI_PG_SCHEMA_SMOKE_URL points at a disposable
// PostgreSQL database; otherwise it is skipped so plain `go test` stays green.
func TestSeedPostgresDefaultsSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_PG_SCHEMA_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_PG_SCHEMA_SMOKE_URL not set; PostgreSQL seed smoke test skipped")
	}
	db := openPGSeedSmokeDB(t, databaseURL)
	defer db.Close()
	ctx := context.Background()
	if _, err := EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	options := SeedOptions{Now: func() time.Time { return pgSeedTestClock }, Secret: "juhe-ai-seed-test-secret"}
	first, err := SeedPostgresDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("first SeedPostgresDefaults: %v", err)
	}
	if first.StatementCount == 0 {
		t.Fatal("first seed executed no statements")
	}
	second, err := SeedPostgresDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("second SeedPostgresDefaults (idempotency): %v", err)
	}
	if second.StatementCount != first.StatementCount {
		t.Fatalf("seed statement count changed between runs: %d -> %d", first.StatementCount, second.StatementCount)
	}
	var adminRows int
	var mustChangePassword int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(max(must_change_password), 0) FROM "juhe_business"."system_accounts" WHERE id = 'sys_admin' AND username = 'admin'`,
	).Scan(&adminRows, &mustChangePassword); err != nil {
		t.Fatalf("count admin rows: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("admin rows = %d, want 1", adminRows)
	}
	// BUG-0224: the seeded super admin must carry must_change_password = 1 so
	// the first login hits the must_change_password 403 gate.
	if mustChangePassword != 1 {
		t.Fatalf("seeded admin must_change_password = %d, want 1", mustChangePassword)
	}
	var modelRows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."models"`,
	).Scan(&modelRows); err != nil {
		t.Fatalf("count model rows: %v", err)
	}
	// pgSeedTestClock 钉在 2026-09-04：models 表全量落库（disabled 行计入）
	// = 目录快照 212 行；其中 shutdown 已过的 6 行（xai 2026-05-15 四行 +
	// gemini veo-3.0 两行 2025-11-12）写 'disabled'。
	if modelRows != 212 {
		t.Fatalf("model rows = %d, want 212 (full snapshot, disabled included)", modelRows)
	}
	var activeModelRows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."models" WHERE status = 'active'`,
	).Scan(&activeModelRows); err != nil {
		t.Fatalf("count active model rows: %v", err)
	}
	if activeModelRows != 206 {
		t.Fatalf("active model rows = %d, want 206 (6 shutdown-expired rows disabled at the pinned clock)", activeModelRows)
	}
	var defaultKeys int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."api_keys" WHERE system_account_id = 'sys_admin' AND (is_default = 1 OR purpose = 'chat')`,
	).Scan(&defaultKeys); err != nil {
		t.Fatalf("count seeded api keys: %v", err)
	}
	// 2026-10-07 默认资源收口：seed 只保留 GPT 一套 —— 默认 GPT 分组派生
	// 1 把默认 Key（is_default=1、purpose='general'）；admin chat Key 不预置。
	// 2026-10-24 免 Key 直调改造后 AI 问答也不再创建任何 Key（EnsureChatAPIKey
	// 链已删除），上面的 purpose='chat' 谓词仅用于钉住「历史 seed 不再写该行」。
	if defaultKeys != 1 {
		t.Fatalf("seeded api keys = %d, want 1", defaultKeys)
	}
	var tokens int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."external_integration_source_tokens" WHERE id = 'exttok_builtin_test'`,
	).Scan(&tokens); err != nil {
		t.Fatalf("count external tokens: %v", err)
	}
	if tokens != 1 {
		t.Fatalf("external tokens = %d, want 1", tokens)
	}
}

// openPGSeedSmokeDB opens the opt-in smoke database connection.
func openPGSeedSmokeDB(t *testing.T, databaseURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
