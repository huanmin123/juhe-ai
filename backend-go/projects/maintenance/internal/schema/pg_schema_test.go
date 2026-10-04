// Tests alongside pg_schema.go. The golden counts are extracted from the Node
// PostgreSQL schema sources (backend/src/storage/postgres-schema.ts); regenerate
// them when the Node sources change. The live-database smoke test is opt-in via
// the JUHE_AI_PG_SCHEMA_SMOKE_URL environment variable.

package schema

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// goldenPostgresSchemaStatementCount is the number of statements produced by
// collectPostgresSchemaStatements() in backend/src/storage/postgres-schema.ts,
// plus the Go-appended statements (model-check question bank table, its
// indexes, the custom_question_ids ALTER columns, the stats
// success_cost_usd ALTER/backfill statements, the chat_conversations
// account-binding ALTER columns, the model_quality_schedules
// interval_minutes CHECK migration DO block, the chat_user_tool_preferences
// user-level tool binding table, the accounts health_check_endpoint_mode
// audio/video CHECK migration DO block, the M2 media_jobs table with its
// three indexes, and the usage_records output_video_seconds ALTER column;
// the retired bind-mode ALTERs
// were removed with the account-only binding migration). Regenerate them when
// either source changes.
const goldenPostgresSchemaStatementCount = 694

// goldenPostgresSchemaStatementCountsPerSchema pins the per-schema statement
// counts of collectPostgresSchemaStatements().
var goldenPostgresSchemaStatementCountsPerSchema = map[string]int{
	"juhe_business":      315,
	"juhe_chat":          43,
	"juhe_dataset":       7,
	"juhe_usage":         52,
	"juhe_stats":         261,
	"juhe_codex_context": 16,
}

// goldenPostgresSchemaNameOrder is the Node first-seen schema order, which is
// also the CREATE SCHEMA execution order.
var goldenPostgresSchemaNameOrder = []string{
	"juhe_business",
	"juhe_chat",
	"juhe_dataset",
	"juhe_usage",
	"juhe_stats",
	"juhe_codex_context",
}

func TestPostgresSchemaStatementsGoldenCount(t *testing.T) {
	statements := Statements()
	if len(statements) != goldenPostgresSchemaStatementCount {
		t.Fatalf("len(Statements()) = %d, want %d (Node collectPostgresSchemaStatements)", len(statements), goldenPostgresSchemaStatementCount)
	}
	if len(postgresSchemaStatements) != goldenPostgresSchemaStatementCount {
		t.Fatalf("len(postgresSchemaStatements) = %d, want %d", len(postgresSchemaStatements), goldenPostgresSchemaStatementCount)
	}
	for i, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			t.Fatalf("statement %d is empty", i)
		}
	}
}

func TestPostgresSchemaStatementCountsPerSchema(t *testing.T) {
	counts := make(map[string]int, len(goldenPostgresSchemaStatementCountsPerSchema))
	firstSeenOrder := make([]string, 0, len(goldenPostgresSchemaNameOrder))
	for _, statement := range postgresSchemaStatements {
		if _, known := goldenPostgresSchemaStatementCountsPerSchema[statement.SchemaName]; !known {
			t.Fatalf("statement %d has unknown schema %q", statementIndex(postgresSchemaStatements, statement), statement.SchemaName)
		}
		if _, seen := counts[statement.SchemaName]; !seen {
			firstSeenOrder = append(firstSeenOrder, statement.SchemaName)
		}
		counts[statement.SchemaName]++
	}
	for schemaName, want := range goldenPostgresSchemaStatementCountsPerSchema {
		if got := counts[schemaName]; got != want {
			t.Fatalf("schema %s statement count = %d, want %d", schemaName, got, want)
		}
	}
	if len(counts) != len(goldenPostgresSchemaStatementCountsPerSchema) {
		t.Fatalf("schema count = %d, want %d", len(counts), len(goldenPostgresSchemaStatementCountsPerSchema))
	}
	if strings.Join(firstSeenOrder, ",") != strings.Join(goldenPostgresSchemaNameOrder, ",") {
		t.Fatalf("schema order = %v, want %v", firstSeenOrder, goldenPostgresSchemaNameOrder)
	}
}

func TestPostgresSchemaStatementsAreIdempotencyGuarded(t *testing.T) {
	var doBlocks, alterColumns, extensions, triggerBlocks, triggers, droppedTriggers int
	for i, statement := range postgresSchemaStatements {
		trimmed := strings.TrimSpace(statement.SQL)
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "CREATE TABLE") || strings.HasPrefix(upper, "CREATE INDEX") ||
			strings.HasPrefix(upper, "CREATE UNIQUE INDEX") || strings.HasPrefix(upper, "CREATE EXTENSION") {
			if !strings.Contains(upper, "IF NOT EXISTS") {
				t.Fatalf("statement %d (%s) lacks IF NOT EXISTS: %.80s", i, statement.Source, trimmed)
			}
		}
		if strings.HasPrefix(upper, "DROP INDEX") && !strings.Contains(upper, "IF EXISTS") {
			t.Fatalf("statement %d (%s) lacks IF EXISTS: %.80s", i, statement.Source, trimmed)
		}
		if strings.HasPrefix(upper, "ALTER TABLE") {
			if !strings.Contains(upper, "ADD COLUMN IF NOT EXISTS") {
				t.Fatalf("statement %d (%s) is an unguarded ALTER TABLE: %.120s", i, statement.Source, trimmed)
			}
			alterColumns++
		}
		if strings.HasPrefix(upper, "DO ") {
			doBlocks++
			if !strings.Contains(upper, "IS NOT NULL") {
				t.Fatalf("statement %d (%s) is an unguarded DO block", i, statement.Source)
			}
		}
		if strings.HasPrefix(upper, "CREATE EXTENSION") {
			extensions++
		}
		if strings.Contains(upper, "CREATE TRIGGER ") {
			triggerBlocks++
			triggers += strings.Count(statement.SQL, "CREATE TRIGGER ")
			droppedTriggers += strings.Count(statement.SQL, "DROP TRIGGER IF EXISTS ")
		}
	}
	if alterColumns != 81 {
		t.Fatalf("ALTER TABLE ADD COLUMN count = %d, want 81", alterColumns)
	}
	if doBlocks != 3 {
		t.Fatalf("DO block count = %d, want 3", doBlocks)
	}
	if extensions != 1 {
		t.Fatalf("CREATE EXTENSION count = %d, want 1", extensions)
	}
	if triggerBlocks != 2 {
		t.Fatalf("trigger block count = %d, want 2", triggerBlocks)
	}
	if triggers != droppedTriggers {
		t.Fatalf("CREATE TRIGGER count %d does not match DROP TRIGGER IF EXISTS count %d; trigger creation would not be idempotent", triggers, droppedTriggers)
	}
}

func statementIndex(statements []PGStatement, target PGStatement) int {
	for i, statement := range statements {
		if statement.SchemaName == target.SchemaName && statement.Source == target.Source && statement.SQL == target.SQL {
			return i
		}
	}
	return -1
}

// TestPostgresQualityScheduleIntervalCheckContract pins the widened
// model_quality_schedules.interval_minutes CHECK (lower bound 10 -> 1 minute)
// in both the CREATE TABLE statement and the guarded legacy migration, while
// the three recovery_interval_minutes CHECKs keep the 10-minute floor. The
// DO block cannot be exercised without a live PostgreSQL, so this asserts the
// SQL text (the migration targets pg_get_constraintdef's normalized
// '((interval_minutes >= 10) AND ...)' rendering, verified against a real
// catalog).
func TestPostgresQualityScheduleIntervalCheckContract(t *testing.T) {
	var createTable string
	var migration string
	for _, statement := range postgresSchemaStatements {
		switch {
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS model_quality_schedules"):
			createTable = statement.SQL
		case strings.Contains(statement.SQL, "model_quality_schedules_interval_minutes_range_check"):
			migration = statement.SQL
		}
	}
	if createTable == "" || migration == "" {
		t.Fatalf("missing schedule CREATE TABLE or migration statement")
	}
	if !strings.Contains(createTable, "CHECK (interval_minutes BETWEEN 1 AND 10080)") {
		t.Fatalf("schedule CREATE TABLE must widen interval_minutes to BETWEEN 1 AND 10080")
	}
	recoveryChecks := strings.Count(createTable, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)")
	if recoveryChecks != 1 {
		t.Fatalf("schedule CREATE TABLE must keep exactly one recovery_interval_minutes BETWEEN 10 AND 10080 check, got %d", recoveryChecks)
	}
	for _, legacyFragment := range []string{
		"to_regclass('juhe_business.model_quality_schedules') IS NOT NULL",
		"LIKE '%(interval_minutes >= 10)%'",
		"EXECUTE format('ALTER TABLE juhe_business.model_quality_schedules DROP CONSTRAINT %I', legacy_constraint)",
		"ADD CONSTRAINT model_quality_schedules_interval_minutes_range_check CHECK (interval_minutes BETWEEN 1 AND 10080)",
	} {
		if !strings.Contains(migration, legacyFragment) {
			t.Fatalf("interval migration missing fragment %q", legacyFragment)
		}
	}
	if strings.Contains(migration, "recovery_interval_minutes") {
		t.Fatalf("interval migration must not touch recovery_interval_minutes")
	}
	// policies 与 enforcements 的 recovery 下限不受本次放开影响。
	for _, statement := range postgresSchemaStatements {
		if strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS model_quality_policies") &&
			!strings.Contains(statement.SQL, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("model_quality_policies must keep the 10-minute recovery floor")
		}
		if strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS account_quality_enforcements") &&
			!strings.Contains(statement.SQL, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("account_quality_enforcements must keep the 10-minute recovery floor")
		}
	}
}

func TestPostgresSeedPasswordHashFormat(t *testing.T) {
	hash, err := hashSeedPassword("admin")
	if err != nil {
		t.Fatalf("hashSeedPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 {
		t.Fatalf("hash parts = %d, want 5: %q", len(parts), hash)
	}
	if parts[0] != "pbkdf2" || parts[1] != "sha512" || parts[2] != strconv.Itoa(pgSeedPasswordIterations) {
		t.Fatalf("unexpected hash header %q", hash)
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	derived, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	if len(salt) != 16 {
		t.Fatalf("salt length = %d, want 16", len(salt))
	}
	if len(derived) != 32 {
		t.Fatalf("digest length = %d, want 32", len(derived))
	}
	// Node hashPassword / Go gateway verifyNodePBKDF2Password both derive from
	// the base64url salt TEXT bytes — the historical defect derived from the
	// raw decoded salt bytes, producing hashes neither runtime could verify.
	recomputed, err := pbkdf2.Key(sha512.New, "admin", []byte(parts[3]), pgSeedPasswordIterations, 32)
	if err != nil {
		t.Fatalf("recompute pbkdf2: %v", err)
	}
	if !bytes.Equal(recomputed, derived) {
		t.Fatal("recomputed digest does not match the stored digest")
	}
}

// TestSeedPasswordHashMatchesNodeVerifySemantics is the seed-interop
// regression: a Go seed hash must verify under the exact byte semantics of
// Node verifyPassword, mirrored byte-for-byte here from backend
// src/storage/crypto.ts (pbkdf2Sync(password, saltText, ...)) and the Go
// gateway modelcheckauth.verifyNodePBKDF2Password (pbkdf2 over []byte(salt
// text)). The seed self-test previously replayed the same raw-salt mistake as
// hashSeedPassword, so the incompatible hash went unnoticed.
func TestSeedPasswordHashMatchesNodeVerifySemantics(t *testing.T) {
	const password = "admin"
	hash, err := hashSeedPassword(password)
	if err != nil {
		t.Fatalf("hashSeedPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 {
		t.Fatalf("hash parts = %d, want 5: %q", len(parts), hash)
	}
	iterations, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("parse iterations: %v", err)
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	// Node verifyPassword: pbkdf2Sync(password, salt, iterations, len, sha512)
	// where salt is the base64url TEXT (parts[3]).
	derived, err := pbkdf2.Key(sha512.New, password, []byte(parts[3]), iterations, len(expected))
	if err != nil {
		t.Fatalf("derive with node semantics: %v", err)
	}
	if !bytes.Equal(derived, expected) {
		t.Fatal("seed hash must verify under the Node base64url-salt-text semantics")
	}
	// The raw-salt-bytes interpretation must NOT verify: if it does, the seed
	// has regressed to the incompatible derivation.
	rawSalt, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	if legacy, err := pbkdf2.Key(sha512.New, password, rawSalt, iterations, len(expected)); err == nil && bytes.Equal(legacy, expected) {
		t.Fatal("seed hash unexpectedly verifies under raw-salt-byte derivation; base64url text semantics regressed")
	}
}

func TestPostgresSeedDataParity(t *testing.T) {
	// The default seed data must stay aligned with the schema objects it feeds.
	// 2026-10-04 M3 minimax 增补（媒体设计 §9/契约 §8）：providers 8→9、
	// endpoint families 10→12（openai_v1_video_generation/tts）、profiles
	// 13→14（profile_minimax_openai_v1）、groups 8→9、profile family 绑定
	// 30→32（minimax 档案挂 2 个媒体族）。同日 M3 第四批 volcengine 增补
	//（媒体设计 §9/契约 §9）：providers 9→10、profiles 14→15
	//（profile_volcengine_openai_v1）、groups 9→10、profile family 绑定
	// 32→33（volcengine 档案只挂 video_generation 1 个媒体族——复用既有
	// openai_v1_video_generation 族，endpoint families 维持 12）。同日 M3
	// 第五批 qwen 增补（契约 §10）：providers 10→11、profiles 15→16
	//（profile_qwen_openai_v1）、groups 10→11、profile family 绑定 33→34
	//（qwen 档案只挂 video_generation 1 个媒体族，families 维持 12）。
	// 同日 M3f 长音频批 + M4b hybrid 媒体映射（契约 §10.2/媒体设计 §9
	// hybrid 行）：families 12→13（新增 openai_v1_audio_transcription 族）、
	// profile family 绑定 34→37（qwen 档案 +audio_transcription、hybrid
	// 档案 +video_generation+tts）。
	if len(pgSeedProviders) != 11 {
		t.Fatalf("provider seeds = %d, want 11", len(pgSeedProviders))
	}
	if len(pgSeedProtocols) != 3 {
		t.Fatalf("protocol seeds = %d, want 3", len(pgSeedProtocols))
	}
	if len(pgSeedEndpointFamilies) != 13 {
		t.Fatalf("endpoint family seeds = %d, want 13", len(pgSeedEndpointFamilies))
	}
	if len(pgSeedProfiles) != 16 {
		t.Fatalf("profile seeds = %d, want 16", len(pgSeedProfiles))
	}
	if len(pgSeedGroups) != 11 {
		t.Fatalf("group seeds = %d, want 11", len(pgSeedGroups))
	}
	if len(pgSeedGlobalSettings) != 2 {
		t.Fatalf("global settings = %d, want 2", len(pgSeedGlobalSettings))
	}
	// system settings 68→71：+3 来自 M5b realtime 会话生命周期键与
	// 2026-10-02 日志与审计 retention 批（与 sqlite 侧钉值同源）。
	if len(pgSeedSystemSettings) != 71 {
		t.Fatalf("system settings = %d, want 71", len(pgSeedSystemSettings))
	}
	profileFamilyCount := 0
	for _, profile := range pgSeedProfiles {
		profileFamilyCount += len(profile.EndpointFamilies)
	}
	if profileFamilyCount != 37 {
		t.Fatalf("profile endpoint family bindings = %d, want 37", profileFamilyCount)
	}
	for _, key := range []string{"appName", "appIcon"} {
		found := false
		for _, setting := range pgSeedGlobalSettings {
			if setting.Key == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("global settings missing key %q", key)
		}
	}
}

// TestEnsurePostgresIdempotentSmoke is the opt-in PostgreSQL integration test.
// It only runs when JUHE_AI_PG_SCHEMA_SMOKE_URL points at a disposable
// PostgreSQL database; otherwise it is skipped so plain `go test` stays green.
func TestEnsurePostgresIdempotentSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_PG_SCHEMA_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_PG_SCHEMA_SMOKE_URL not set; PostgreSQL schema smoke test skipped")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctxWithTimeout(t)); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	ctx := ctxWithTimeout(t)

	result1, err := EnsurePostgres(ctx, db)
	if err != nil {
		t.Fatalf("first EnsurePostgres: %v", err)
	}
	if result1.StatementCount != goldenPostgresSchemaStatementCount {
		t.Fatalf("first EnsurePostgres statement count = %d, want %d", result1.StatementCount, goldenPostgresSchemaStatementCount)
	}
	if result1.SchemaCount != len(goldenPostgresSchemaNameOrder) {
		t.Fatalf("first EnsurePostgres schema count = %d, want %d", result1.SchemaCount, len(goldenPostgresSchemaNameOrder))
	}
	tableCount1, err := countPGSmokeTables(ctx, db)
	if err != nil {
		t.Fatalf("count tables after first EnsurePostgres: %v", err)
	}
	if tableCount1 == 0 {
		t.Fatal("no tables were created in the juhe_* schemas")
	}

	result2, err := EnsurePostgres(ctx, db)
	if err != nil {
		t.Fatalf("second EnsurePostgres (idempotency): %v", err)
	}
	if result2 != result1 {
		t.Fatalf("second EnsurePostgres result = %+v, want %+v", result2, result1)
	}
	tableCount2, err := countPGSmokeTables(ctx, db)
	if err != nil {
		t.Fatalf("count tables after second EnsurePostgres: %v", err)
	}
	if tableCount2 != tableCount1 {
		t.Fatalf("table count changed between runs: %d -> %d", tableCount1, tableCount2)
	}

	seed1, err := EnsurePostgresSeeds(ctx, db)
	if err != nil {
		t.Fatalf("first EnsurePostgresSeeds: %v", err)
	}
	if seed1.StatementCount == 0 {
		t.Fatal("first EnsurePostgresSeeds executed no statements")
	}
	seed2, err := EnsurePostgresSeeds(ctx, db)
	if err != nil {
		t.Fatalf("second EnsurePostgresSeeds (idempotency): %v", err)
	}
	if seed2.StatementCount != seed1.StatementCount {
		t.Fatalf("seed statement count changed between runs: %d -> %d", seed1.StatementCount, seed2.StatementCount)
	}

	var adminRows int
	var mustChangePassword int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(max(must_change_password), 0) FROM "juhe_business"."system_accounts" WHERE id = 'sys_admin' AND username = 'admin'`,
	).Scan(&adminRows, &mustChangePassword); err != nil {
		t.Fatalf("query seeded admin account: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("seeded admin account rows = %d, want 1", adminRows)
	}
	// BUG-0224: the seeded super admin must carry must_change_password = 1 so
	// the first login hits the must_change_password 403 gate.
	if mustChangePassword != 1 {
		t.Fatalf("seeded admin must_change_password = %d, want 1", mustChangePassword)
	}
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func countPGSmokeTables(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema IN ('juhe_business', 'juhe_chat', 'juhe_dataset', 'juhe_usage', 'juhe_stats', 'juhe_codex_context')`).Scan(&count)
	return count, err
}
