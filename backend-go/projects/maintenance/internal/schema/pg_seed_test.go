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
	query, args := buildPostgresModelSeedUpsert(modelCatalogSeedRows, "2026-09-04T08:00:00.123Z")
	if got := len(modelCatalogSeedRows) * 39; len(args) != got {
		t.Fatalf("bulk upsert arg count = %d, want %d", len(args), got)
	}
	if !strings.Contains(query, `INSERT INTO "juhe_business"."provider_model_catalog"`) {
		t.Fatal("bulk upsert misses the qualified insert target")
	}
	if !strings.Contains(query, "ON CONFLICT(provider_code, model) DO UPDATE SET") {
		t.Fatal("bulk upsert misses the Node conflict clause")
	}
	for _, guard := range []string{"manual-override", "manual-visibility-override", "excluded.catalog_visible"} {
		if !strings.Contains(query, guard) {
			t.Fatalf("bulk upsert misses guard %q", guard)
		}
	}
	// status is the 'active' literal once per row, never a parameter.
	if got := strings.Count(query, "'active'"); got != len(modelCatalogSeedRows) {
		t.Fatalf("'active' literal count = %d, want %d", got, len(modelCatalogSeedRows))
	}
	// Row boundaries: second row starts at $40, last row ends at $<args len>.
	if !strings.Contains(query, "),\n          ($40, $41, $42, 'active'") {
		t.Fatal("bulk upsert second row does not start at $40")
	}
	if !strings.Contains(query, "$"+strconv.Itoa(len(args))) {
		t.Fatalf("bulk upsert misses the final placeholder $%d", len(args))
	}
	if strings.Contains(query, "$"+strconv.Itoa(len(args)+1)) {
		t.Fatal("bulk upsert has a placeholder beyond the arg count")
	}
	// First row args mirror the first static row plus now/now.
	first := modelCatalogSeedRows[0]
	if args[0] != first.ID || args[1] != first.ProviderCode || args[2] != first.Model {
		t.Fatalf("first row identity args = %v/%v/%v", args[0], args[1], args[2])
	}
	if args[3] != seedNullableString(first.Mode) {
		t.Fatalf("first row mode arg = %v, want %v", args[3], seedNullableString(first.Mode))
	}
	if args[len(args)-1] != "2026-09-04T08:00:00.123Z" || args[len(args)-2] != "2026-09-04T08:00:00.123Z" {
		t.Fatal("bulk upsert does not end with the seeded timestamps")
	}
}

func TestPostgresSeedStatementContracts(t *testing.T) {
	// The stale disable keeps every Node reference guard.
	for _, fragment := range []string{
		"jsonb_to_recordset($2::jsonb) AS built_in(provider_code text, model text)",
		"source NOT IN ('manual-override', 'manual-visibility-override')",
		`"juhe_business"."account_supported_models"`,
		`"juhe_business"."account_model_mappings"`,
		`"juhe_business"."provider_default_health_check_models"`,
		`"juhe_business"."provider_system_default_health_check_models"`,
		`"juhe_business"."accounts"`,
		"account.health_check_model",
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
		"adminChatAPIKey":      pgSeedAdminChatAPIKeyInsert,
		"externalSourceInsert": pgSeedExternalIntegrationSourceInsert,
	} {
		if !strings.Contains(statement, "ON CONFLICT DO NOTHING") {
			t.Fatalf("%s insert misses ON CONFLICT DO NOTHING", name)
		}
	}
	// The built-in model key list serializes like the Node
	// JSON.stringify(currentBuiltInModels) payload.
	encoded, err := json.Marshal([]pgSeedBuiltInModelKey{{ProviderCode: "gpt", Model: "gpt-5.6-sol"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `[{"provider_code":"gpt","model":"gpt-5.6-sol"}]` {
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
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."system_accounts" WHERE id = 'sys_admin' AND username = 'admin'`,
	).Scan(&adminRows); err != nil {
		t.Fatalf("count admin rows: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("admin rows = %d, want 1", adminRows)
	}
	var catalogRows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."provider_model_catalog" WHERE status = 'active'`,
	).Scan(&catalogRows); err != nil {
		t.Fatalf("count catalog rows: %v", err)
	}
	if catalogRows != 106 {
		t.Fatalf("active catalog rows = %d, want 106", catalogRows)
	}
	var defaultKeys int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM "juhe_business"."api_keys" WHERE system_account_id = 'sys_admin' AND (is_default = 1 OR purpose = 'chat')`,
	).Scan(&defaultKeys); err != nil {
		t.Fatalf("count seeded api keys: %v", err)
	}
	if defaultKeys != 8 {
		t.Fatalf("seeded api keys = %d, want 8", defaultKeys)
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
