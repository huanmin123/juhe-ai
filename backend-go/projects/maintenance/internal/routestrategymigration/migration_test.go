package routestrategymigration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	_ "modernc.org/sqlite"
)

// hybridFixtureTimestamp is the created_at/updated_at value every fixture row
// starts with; confirm runs must overwrite it only on migrated rows.
const hybridFixtureTimestamp = "2026-01-02T03:04:05.000Z"

const hybridFixtureConfigA = `{"hybridRoutingConfig":{"downgradeConsecutiveLowCount":2,"levelRoutes":[]}}`
const hybridFixtureConfigB = `{"hybridRoutingConfig":{"downgradeConsecutiveLowCount":1}}`

// newBusinessSQLite opens a single-connection in-memory business database with
// the real SQLite business schema (same DDL the storage bootstrap applies).
func newBusinessSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	// :memory: databases are per-connection; pin the pool to one connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure business schema: %v", err)
	}
	return db
}

// seedHybridFixture writes one owner, two hybrid_smart strategies (one with
// two group bindings and one bound API Key, one bare), one normal control
// strategy with its own binding and API Key.
func seedHybridFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO system_accounts (id, username, display_name, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"sa-1", "owner-1", "Owner One", "hash", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO providers (id, code, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			[]any{"prov-1", "openai", "OpenAI", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"grp-1", "sa-1", "Group One", "openai", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"grp-2", "sa-1", "Group Two", "openai", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"grp-3", "sa-1", "Group Three", "openai", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO route_strategies (id, system_account_id, name, mode, status, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"strat-hybrid-a", "sa-1", "Hybrid A", SourceMode, "active", hybridFixtureConfigA, hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO route_strategies (id, system_account_id, name, mode, status, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"strat-hybrid-b", "sa-1", "Hybrid B", SourceMode, "active", hybridFixtureConfigB, hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO route_strategies (id, system_account_id, name, mode, status, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"strat-normal-c", "sa-1", "Normal C", "normal", "active", nil, hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"rsg-1", "strat-hybrid-a", "sa-1", "grp-1", 1, 1, "active", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"rsg-2", "strat-hybrid-a", "sa-1", "grp-2", 2, 1, "active", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"rsg-3", "strat-normal-c", "sa-1", "grp-3", 1, 1, "active", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"key-1", "sa-1", "strat-hybrid-a", "Key One", "hash-1", "ju-", "one", "enc-1", hybridFixtureTimestamp, hybridFixtureTimestamp}},
		{`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"key-2", "sa-1", "strat-normal-c", "Key Two", "hash-2", "ju-", "two", "enc-2", hybridFixtureTimestamp, hybridFixtureTimestamp}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed fixture (%s): %v", statement.query, err)
		}
	}
}

func queryStrategyRow(t *testing.T, db *sql.DB, id string) (mode, status string, configJSON sql.NullString, updatedAt string) {
	t.Helper()
	err := db.QueryRowContext(context.Background(),
		`SELECT mode, status, config_json, updated_at FROM route_strategies WHERE id = ?`, id,
	).Scan(&mode, &status, &configJSON, &updatedAt)
	if err != nil {
		t.Fatalf("query strategy %s: %v", id, err)
	}
	return mode, status, configJSON, updatedAt
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return count
}

func TestRunDryRunLeavesDataUntouched(t *testing.T) {
	db := newBusinessSQLite(t)
	seedHybridFixture(t, db)
	report, err := Run(context.Background(), db, DialectSQLite, false, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !report.DryRun || report.Confirm {
		t.Fatalf("expected dry-run report, got dryRun=%v confirm=%v", report.DryRun, report.Confirm)
	}
	if report.Driver != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", report.Driver)
	}
	if report.HybridSmartCount != 2 || report.RemainingHybridSmart != 2 {
		t.Fatalf("counts = found %d remaining %d, want 2/2", report.HybridSmartCount, report.RemainingHybridSmart)
	}
	if len(report.Strategies) != 2 {
		t.Fatalf("strategies = %d rows, want 2", len(report.Strategies))
	}
	// ORDER BY id: strat-hybrid-a first with its single bound API Key.
	first := report.Strategies[0]
	if first.ID != "strat-hybrid-a" || first.Name != "Hybrid A" || first.Status != "active" || first.BoundAPIKeys != 1 {
		t.Fatalf("first strategy = %+v, want strat-hybrid-a/Hybrid A/active/1 key", first)
	}
	second := report.Strategies[1]
	if second.ID != "strat-hybrid-b" || second.BoundAPIKeys != 0 {
		t.Fatalf("second strategy = %+v, want strat-hybrid-b with 0 keys", second)
	}
	// Dry-run must not write: mode, status, config_json and updated_at stay.
	mode, status, configJSON, updatedAt := queryStrategyRow(t, db, "strat-hybrid-a")
	if mode != SourceMode || status != "active" || !configJSON.Valid || configJSON.String != hybridFixtureConfigA || updatedAt != hybridFixtureTimestamp {
		t.Fatalf("dry-run mutated strat-hybrid-a: mode=%s status=%s config=%v updated_at=%s", mode, status, configJSON, updatedAt)
	}
	if _, _, _, updatedAt := queryStrategyRow(t, db, "strat-normal-c"); updatedAt != hybridFixtureTimestamp {
		t.Fatalf("dry-run mutated control row updated_at: %s", updatedAt)
	}
}

func TestRunConfirmMigratesAndPreservesBindings(t *testing.T) {
	db := newBusinessSQLite(t)
	seedHybridFixture(t, db)
	now := time.Date(2026, 9, 19, 12, 30, 45, 0, time.UTC)
	report, err := Run(context.Background(), db, DialectSQLite, true, now)
	if err != nil {
		t.Fatalf("confirm run: %v", err)
	}
	if !report.Confirm || report.DryRun {
		t.Fatalf("expected confirm report, got confirm=%v dryRun=%v", report.Confirm, report.DryRun)
	}
	if report.MigratedRows != 2 || report.HybridSmartCount != 2 {
		t.Fatalf("migrated %d of found %d, want 2/2", report.MigratedRows, report.HybridSmartCount)
	}
	if report.UpdatedAt != "2026-09-19T12:30:45.000Z" {
		t.Fatalf("updatedAt = %q, want the ISO millisecond UTC form", report.UpdatedAt)
	}
	if report.RemainingHybridSmart != 0 || !report.Ready() {
		t.Fatalf("remaining = %d ready = %v, want 0/true", report.RemainingHybridSmart, report.Ready())
	}
	if len(report.ModeDistribution) != 2 || report.ModeDistribution[TargetMode] != 2 || report.ModeDistribution["normal"] != 1 {
		t.Fatalf("mode distribution = %v, want failover:2 normal:1 and no hybrid_smart", report.ModeDistribution)
	}
	if _, ok := report.ModeDistribution[SourceMode]; ok {
		t.Fatalf("mode distribution still contains hybrid_smart: %v", report.ModeDistribution)
	}
	if len(report.Migrated) != 2 {
		t.Fatalf("migrated listing = %d rows, want 2", len(report.Migrated))
	}
	if report.Migrated[0].ID != "strat-hybrid-a" || report.Migrated[0].Mode != TargetMode || report.Migrated[0].Status != TargetStatus || report.Migrated[0].BoundAPIKeys != 1 {
		t.Fatalf("migrated[0] = %+v, want strat-hybrid-a failover/disabled with 1 key", report.Migrated[0])
	}
	if report.Migrated[1].ID != "strat-hybrid-b" || report.Migrated[1].Mode != TargetMode || report.Migrated[1].Status != TargetStatus || report.Migrated[1].BoundAPIKeys != 0 {
		t.Fatalf("migrated[1] = %+v, want strat-hybrid-b failover/disabled with 0 keys", report.Migrated[1])
	}
	// Row state: failover + disabled + config_json NULL + refreshed updated_at.
	for _, id := range []string{"strat-hybrid-a", "strat-hybrid-b"} {
		mode, status, configJSON, updatedAt := queryStrategyRow(t, db, id)
		if mode != TargetMode || status != TargetStatus || configJSON.Valid || updatedAt != report.UpdatedAt {
			t.Fatalf("%s = mode=%s status=%s config=%v updated_at=%s, want failover/disabled/NULL/%s", id, mode, status, configJSON, updatedAt, report.UpdatedAt)
		}
	}
	// Control row: normal strategy untouched in every column.
	mode, status, configJSON, updatedAt := queryStrategyRow(t, db, "strat-normal-c")
	if mode != "normal" || status != "active" || configJSON.Valid || updatedAt != hybridFixtureTimestamp {
		t.Fatalf("control row mutated: mode=%s status=%s config=%v updated_at=%s", mode, status, configJSON, updatedAt)
	}
	// Bindings preserved: group bindings keep both rows and priorities.
	if got := countRows(t, db, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ?`, "strat-hybrid-a"); got != 2 {
		t.Fatalf("group bindings for strat-hybrid-a = %d, want 2", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ? AND group_id = ? AND priority = ?`, "strat-hybrid-a", "grp-2", 2); got != 1 {
		t.Fatalf("priority ordering binding lost: %d rows, want 1", got)
	}
	// API Key bindings preserved (strategy disabled, Key untouched).
	if got := countRows(t, db, `SELECT COUNT(*) FROM api_keys WHERE route_strategy_id = ?`, "strat-hybrid-a"); got != 1 {
		t.Fatalf("api key bindings for strat-hybrid-a = %d, want 1", got)
	}
}

func TestRunConfirmRerunIsIdempotent(t *testing.T) {
	db := newBusinessSQLite(t)
	seedHybridFixture(t, db)
	ctx := context.Background()
	first, err := Run(ctx, db, DialectSQLite, true, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first confirm run: %v", err)
	}
	second, err := Run(ctx, db, DialectSQLite, true, time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second confirm run: %v", err)
	}
	if second.HybridSmartCount != 0 || second.MigratedRows != 0 || len(second.Strategies) != 0 || len(second.Migrated) != 0 {
		t.Fatalf("rerun report = found %d migrated %d strategies %d migrated-list %d, want all zero",
			second.HybridSmartCount, second.MigratedRows, len(second.Strategies), len(second.Migrated))
	}
	if second.Note != "no hybrid_smart rows" {
		t.Fatalf("rerun note = %q, want %q", second.Note, "no hybrid_smart rows")
	}
	if second.RemainingHybridSmart != 0 || !second.Ready() {
		t.Fatalf("rerun remaining = %d ready = %v, want 0/true", second.RemainingHybridSmart, second.Ready())
	}
	// The rerun must not touch the already-migrated rows (updated_at keeps the
	// first run's timestamp).
	mode, status, configJSON, updatedAt := queryStrategyRow(t, db, "strat-hybrid-a")
	if mode != TargetMode || status != TargetStatus || configJSON.Valid || updatedAt != first.UpdatedAt {
		t.Fatalf("rerun mutated strat-hybrid-a: mode=%s status=%s config=%v updated_at=%s", mode, status, configJSON, updatedAt)
	}
}

func TestRunDryRunAndConfirmOnEmptyTable(t *testing.T) {
	db := newBusinessSQLite(t)
	ctx := context.Background()
	dryRun, err := Run(ctx, db, DialectSQLite, false, time.Now())
	if err != nil {
		t.Fatalf("dry-run on empty table: %v", err)
	}
	if dryRun.HybridSmartCount != 0 || dryRun.Note != "no hybrid_smart rows" || !dryRun.Ready() {
		t.Fatalf("empty dry-run = %+v, want zero rows with note and ready", dryRun)
	}
	confirm, err := Run(ctx, db, DialectSQLite, true, time.Now())
	if err != nil {
		t.Fatalf("confirm on empty table: %v", err)
	}
	if confirm.MigratedRows != 0 || confirm.Note != "no hybrid_smart rows" || !confirm.Ready() {
		t.Fatalf("empty confirm = %+v, want zero migrated rows with note and ready", confirm)
	}
}

func TestRunRejectsNilDatabase(t *testing.T) {
	if _, err := Run(context.Background(), nil, DialectSQLite, false, time.Now()); err == nil {
		t.Fatal("expected an error for a nil database handle")
	}
}

func TestOpenRejectsNonPostgresURLs(t *testing.T) {
	for _, rawURL := range []string{"", "sqlite://business.sqlite3", "file:/tmp/business.sqlite3", "http://localhost:5432/juhe"} {
		db, err := Open(rawURL)
		if err == nil {
			_ = db.Close()
			t.Fatalf("Open(%q) succeeded, want a PostgreSQL-only rejection", rawURL)
		}
		if db != nil {
			t.Fatalf("Open(%q) returned a handle alongside the error", rawURL)
		}
	}
	for _, rawURL := range []string{"postgres://maintenance:pw@localhost:5432/juhe", "postgresql://maintenance:pw@localhost:5432/juhe"} {
		db, err := Open(rawURL)
		if err != nil {
			t.Fatalf("Open(%q) failed: %v", rawURL, err)
		}
		_ = db.Close()
	}
}

func TestReportReadySemantics(t *testing.T) {
	cases := []struct {
		name   string
		report Report
		want   bool
	}{
		{"dry-run always ready", Report{DryRun: true, RemainingHybridSmart: 5}, true},
		{"confirm clean", Report{Confirm: true, RemainingHybridSmart: 0}, true},
		{"confirm with leftovers", Report{Confirm: true, RemainingHybridSmart: 1}, false},
	}
	for _, tc := range cases {
		if got := tc.report.Ready(); got != tc.want {
			t.Fatalf("%s: Ready() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDialectTableAndPlaceholder(t *testing.T) {
	if got := DialectPostgres.table("route_strategies"); got != "juhe_business.route_strategies" {
		t.Fatalf("postgres table = %q", got)
	}
	if got := DialectSQLite.table("route_strategies"); got != "route_strategies" {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := DialectPostgres.placeholder(3); got != "$3" {
		t.Fatalf("postgres placeholder = %q", got)
	}
	if got := DialectSQLite.placeholder(3); got != "?" {
		t.Fatalf("sqlite placeholder = %q", got)
	}
	if DialectPostgres.String() != "postgres" || DialectSQLite.String() != "sqlite" {
		t.Fatalf("dialect labels = %q / %q", DialectPostgres.String(), DialectSQLite.String())
	}
}
