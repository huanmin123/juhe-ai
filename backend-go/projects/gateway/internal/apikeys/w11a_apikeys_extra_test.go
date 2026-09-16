package apikeys

// w11a coverage arms: pure helper matrices (patch typing/quota/schedule),
// store constructor and cleanup-target branches, schedule timezone fallbacks,
// and the store error paths (broken schema, closed db, triggers).

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// ---------------------------------------------------------------------------
// Pure helpers: patch typing, sorting, scope filters, integers.
// ---------------------------------------------------------------------------

func TestW11APatchTypingHelpers(t *testing.T) {
	typeName := map[string]string{
		"null": patchTypeName(nil), "boolean": patchTypeName(true),
		"number": patchTypeName(float64(1)), "string": patchTypeName("s"),
		"array": patchTypeName([]any{}), "object": patchTypeName(map[string]any{}),
		"unknown": patchTypeName(complex(1, 2)),
	}
	for want, got := range typeName {
		if got != want {
			t.Fatalf("patchTypeName(%q arm) = %q", want, got)
		}
	}
	received := map[string]string{
		"null": patchReceived(nil), "true": patchReceived(true), "false": patchReceived(false),
		"42":      patchReceived(float64(42)),
		"1.5":     patchReceived(1.5),
		"text":    patchReceived("text"),
		"a,b":     patchReceived([]any{"a", "b"}),
		"[object Object]": patchReceived(map[string]any{"k": 1}),
		"unknown": patchReceived(complex(1, 2)),
	}
	for want, got := range received {
		if got != want {
			t.Fatalf("patchReceived(%q arm) = %q", want, got)
		}
	}
	values := []string{"pear", "apple", "banana"}
	sortStrings(values)
	if strings.Join(values, ",") != "apple,banana,pear" {
		t.Fatalf("sortStrings = %v", values)
	}
	if got := normalizeScopeFilter(" all "); got != "" {
		t.Fatalf("all filter = %q", got)
	}
	if got := normalizeScopeFilter("active"); got != "active" {
		t.Fatalf("explicit filter = %q", got)
	}
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q", got)
	}
	if got := maxInt(2, 1); got != 2 {
		t.Fatalf("maxInt = %d", got)
	}
	if value, ok := jsQueryInteger("-1e100"); !ok || value != math.MinInt64 {
		t.Fatalf("min-int saturation = %d, %v", value, ok)
	}
	request := httptest.NewRequest(http.MethodGet, "/keys?systemAccountId=%20", nil)
	if scopeQueryOK(request) {
		t.Fatal("blank systemAccountId must fail the scope query")
	}
	if got := actorResolver(httptest.NewRequest(http.MethodGet, "/", nil)); got != "anonymous" {
		t.Fatalf("anonymous actor = %q", got)
	}
	if _, err := NewStatsUsageSource(nil, false); err == nil {
		t.Fatal("nil stats db must fail")
	}
	if ensureCtx(nil) == nil {
		t.Fatal("ensureCtx(nil) must return a context")
	}
}

// ---------------------------------------------------------------------------
// parsePatchBody / createBody field arms.
// ---------------------------------------------------------------------------

func TestW11AParsePatchBodyArms(t *testing.T) {
	const revision = "2026-01-01T00:00:00.000Z"
	patch := func(body map[string]any) (*PatchInput, string) {
		body["expectedRevision"] = revision
		return parsePatchBody(body)
	}
	if _, issue := patch(map[string]any{"name": 5}); issue == "" {
		t.Fatal("non-string name must fail")
	}
	if _, issue := patch(map[string]any{"name": "  "}); issue == "" {
		t.Fatal("blank name must fail")
	}
	if input, issue := patch(map[string]any{"description": nil}); issue != "" || !input.HasDescription || input.Description != nil {
		t.Fatalf("null description = %+v, %q", input, issue)
	}
	if _, issue := patch(map[string]any{"description": 5}); issue == "" {
		t.Fatal("non-string description must fail")
	}
	if input, issue := patch(map[string]any{"description": "   "}); issue != "" || input.Description != nil {
		t.Fatalf("blank description = %+v, %q", input, issue)
	}
	if _, issue := patch(map[string]any{"routeStrategyId": 5}); issue == "" {
		t.Fatal("non-string routeStrategyId must fail")
	}
	if _, issue := patch(map[string]any{"routeStrategyId": "  "}); issue == "" {
		t.Fatal("blank routeStrategyId must fail")
	}
	if _, issue := patch(map[string]any{"status": 5}); !strings.Contains(issue, "Invalid enum value") {
		t.Fatalf("numeric status issue = %q", issue)
	}
	if _, issue := patch(map[string]any{"status": true}); !strings.Contains(issue, "'true'") {
		t.Fatalf("boolean status issue = %q", issue)
	}
	if _, issue := patch(map[string]any{"status": float64(1)}); !strings.Contains(issue, "received '1'") {
		t.Fatalf("number status issue = %q", issue)
	}
	if input, issue := patch(map[string]any{"expiresAt": nil}); issue != "" || !input.HasExpiresAt || input.ExpiresAt != nil {
		t.Fatalf("null expiresAt = %+v, %q", input, issue)
	}
	long := strings.Repeat("x", 201)
	if _, issue := patch(map[string]any{"description": long, "name": "k"}); !strings.Contains(issue, "200") {
		t.Fatalf("long description issue = %q", issue)
	}
}

func TestW11ACreateBodyArms(t *testing.T) {
	if _, issue := createBody(map[string]any{}); issue == "" {
		t.Fatal("missing name must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "bogus": 1}); issue == "" {
		t.Fatal("unknown key must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "description": 5}); issue == "" {
		t.Fatal("non-string description must fail")
	}
	long := strings.Repeat("x", 201)
	if _, issue := createBody(map[string]any{"name": "k", "description": long}); issue == "" {
		t.Fatal("long description must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "routeStrategyId": 5}); issue == "" {
		t.Fatal("non-string routeStrategyId must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "routeStrategyId": " "}); issue == "" {
		t.Fatal("blank routeStrategyId must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "status": "frozen"}); issue == "" {
		t.Fatal("bad status must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "expiresAt": 5}); issue == "" {
		t.Fatal("non-string expiresAt must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "quotaLimits": "x"}); issue == "" {
		t.Fatal("non-object quotaLimits must fail")
	}
	if _, issue := createBody(map[string]any{"name": "k", "availabilitySchedule": "x"}); issue == "" {
		t.Fatal("non-object schedule must fail")
	}
	input, issue := createBody(map[string]any{"name": " k ", "description": "d", "expiresAt": nil, "quotaLimits": nil, "availabilitySchedule": nil})
	if issue != "" || input.Name != " k " || input.Description == nil {
		t.Fatalf("create body = %+v, %q", input, issue)
	}
}

func TestW11APatchComparableAndSafeValueArms(t *testing.T) {
	if got := patchComparableValue("a", false); got != "null" {
		t.Fatalf("absent value = %q", got)
	}
	if got := patchComparableValue(nil, true); got != "null" {
		t.Fatalf("null value = %q", got)
	}
	if got := patchComparableValue("text", true); got != `"text"` {
		t.Fatalf("string value = %q", got)
	}
	if got := patchSafeValue("plain", true); got != "plain" {
		t.Fatalf("plain value = %q", got)
	}
	if got := patchSafeValue(nil, true); got != "" {
		t.Fatalf("null safe value = %q", got)
	}
	long := strings.Repeat("x", 250)
	if got := patchSafeValue(long, true); got != string([]rune(long)[:200])+"..." {
		t.Fatalf("long string truncation = %d runes", len([]rune(got)))
	}
	longObject := map[string]any{"k": strings.Repeat("x", 600)}
	if got := patchSafeValue(longObject, true); len([]rune(got)) != 500 {
		t.Fatalf("long object truncation = %d runes", len([]rune(got)))
	}
}

// ---------------------------------------------------------------------------
// Quota limits normalization matrix.
// ---------------------------------------------------------------------------

func TestW11AQuotaNormalizationArms(t *testing.T) {
	if limits, err := ParseQuotaLimitsJSON("   "); err != nil || limits.hasEnabled() {
		t.Fatalf("blank quota = %+v, %v", limits, err)
	}
	if _, err := ParseQuotaLimitsJSON("{bad"); err == nil {
		t.Fatal("malformed json must fail")
	}
	if _, err := normalizeQuotaLimits("x", emptyQuotaLimits()); err == nil {
		t.Fatal("non-object must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"bogus":1}`); err == nil {
		t.Fatal("unknown key must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":null}`); err == nil {
		t.Fatal("null hourly must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":"x"}`); err == nil {
		t.Fatal("non-object hourly must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":{"bogus":1}}`); err == nil {
		t.Fatal("unknown hourly key must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":{"enabled":false,"limit":1,"hours":1}}`); err == nil {
		t.Fatal("disabled hourly must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":{"enabled":true,"limit":"x","hours":1}}`); err == nil {
		t.Fatal("string limit must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":{"enabled":true,"limit":1,"hours":1.5}}`); err == nil {
		t.Fatal("fractional hours must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"hourly":{"enabled":true,"limit":1,"hours":0}}`); err == nil {
		t.Fatal("zero hours must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"daily":null}`); err == nil {
		t.Fatal("null daily must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"daily":"x"}`); err == nil {
		t.Fatal("non-object daily must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"daily":{"bogus":1}}`); err == nil {
		t.Fatal("unknown daily key must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"daily":{"enabled":false,"limit":1}}`); err == nil {
		t.Fatal("disabled daily must fail")
	}
	if _, err := ParseQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":0.0000001}}); `); err == nil {
		t.Fatal("sub-micro limit must fail")
	}
	for _, key := range []string{"weekly", "monthly", "total"} {
		payload := `{"` + key + `":{"enabled":true,"limit":2.5}}`
		limits, err := ParseQuotaLimitsJSON(payload)
		if err != nil {
			t.Fatalf("%s = %v", key, err)
		}
		var limit *QuotaLimit
		switch key {
		case "weekly":
			limit = limits.Weekly
		case "monthly":
			limit = limits.Monthly
		case "total":
			limit = limits.Total
		}
		if limit == nil || limit.Limit != 2.5 {
			t.Fatalf("%s limit = %+v", key, limit)
		}
	}
	if encoded, ok := QuotaLimitsJSON(emptyQuotaLimits()); ok || encoded != "" {
		t.Fatalf("disabled quota encode = %q, %v", encoded, ok)
	}
	limits, err := ParseQuotaLimitsJSON(`{"hourly":{"enabled":true,"limit":1,"hours":2}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := QuotaLimitsJSON(limits); !ok {
		t.Fatal("enabled quota must encode")
	}
}

// ---------------------------------------------------------------------------
// Store constructor, invalidator bus, cleanup target, timezone fallbacks.
// ---------------------------------------------------------------------------

func TestW11AStoreConstructorAndCleanupArms(t *testing.T) {
	if _, err := NewStore(nil, false, "secret", time.Now, nil, nil); err == nil {
		t.Fatal("nil db must fail")
	}
	env := newTestEnv(t)
	if _, err := NewStore(env.db, false, "  ", time.Now, nil, nil); err == nil {
		t.Fatal("blank secret must fail")
	}

	// BusInvalidator with a live bus reaches all three topics.
	bus := inval.New(time.Now)
	invalidator := BusInvalidator{Bus: bus}
	if err := invalidator.InvalidateValidation("w11a-key", "w11a-reason", nil); err != nil {
		t.Fatal(err)
	}
	invalidator.InvalidateQuota("w11a-key", "w11a-reason")
	invalidator.InvalidateRuntime("w11a-key", "w11a-reason")

	// SQLite store without the dataset handle refuses the cleanup target.
	if err := env.store.RegisterCleanupTarget(context.Background(), "w11a-key", "w11a-owner"); err == nil {
		t.Fatal("missing dataset db must fail")
	}

	// A wired dataset handle records the target row (schema fixture exists).
	dataset, err := sql.Open("sqlite", "file:w11a-dataset-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	dataset.SetMaxOpenConns(1)
	t.Cleanup(func() { dataset.Close() })
	if _, err := dataset.Exec(`CREATE TABLE api_key_record_cleanup_targets (
		api_key_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at TEXT,
		last_blocked_reason TEXT, last_error_message TEXT)`); err != nil {
		t.Fatal(err)
	}
	wired, err := NewStore(env.db, false, "w11a-secret", time.Now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wired.datasetDB = dataset
	if err := wired.RegisterCleanupTarget(context.Background(), "w11a-key", "w11a-owner"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dataset.QueryRow(`SELECT COUNT(*) FROM api_key_record_cleanup_targets WHERE api_key_id = 'w11a-key'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("cleanup targets = %d", count)
	}
}

func TestW11AScheduleTimezoneFallbacks(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, `CREATE TABLE IF NOT EXISTS system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`)
	// Cached value survives a second read within the TTL.
	first := env.store.defaultScheduleTimezone()
	second := env.store.defaultScheduleTimezone()
	if first == "" || first != second {
		t.Fatalf("timezone cache = %q vs %q", first, second)
	}
	// Malformed stored values degrade to the fallback.
	if _, err := env.db.Exec(`INSERT OR REPLACE INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', 'not-json', ?)`, isoMillis(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT OR REPLACE INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', '""', ?)`, isoMillis(time.Now())); err != nil {
		t.Fatal(err)
	}
	if got := env.store.readUsageStatsTimezoneSetting(); got != "" {
		t.Fatalf("blank timezone = %q", got)
	}
	if _, err := env.db.Exec(`INSERT OR REPLACE INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', '"Mars/Olympus"', ?)`, isoMillis(time.Now())); err != nil {
		t.Fatal(err)
	}
	if got := env.store.readUsageStatsTimezoneSetting(); got != "" {
		t.Fatalf("invalid timezone = %q", got)
	}
	if _, err := env.db.Exec(`DROP TABLE system_settings`); err != nil {
		t.Fatal(err)
	}
	if got := env.store.readUsageStatsTimezoneSetting(); got != "" {
		t.Fatalf("missing settings table = %q", got)
	}
	// fallbackScheduleTimezone with a named local zone.
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	original := time.Local
	time.Local = shanghai
	if got := fallbackScheduleTimezone(); got != "Asia/Shanghai" {
		t.Fatalf("named local fallback = %q", got)
	}
	time.Local = original
}

// ---------------------------------------------------------------------------
// Store error paths through the HTTP surface.
// ---------------------------------------------------------------------------

func TestW11AStoreBrokenSchemaArms(t *testing.T) {
	create := func(t *testing.T, env *testEnv) string {
		t.Helper()
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		status, body := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"w11a-key"}`)
		if status != http.StatusCreated {
			t.Fatalf("create = %d (%v)", status, body)
		}
		return dataMap(t, body)["id"].(string)
	}

	t.Run("list_fails", func(t *testing.T) {
		env := newTestEnv(t)
		env.login(t, "root", "root-pass", "super_admin")
		env.exec(t, `DROP TABLE api_keys`)
		status, body := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
		if status != http.StatusInternalServerError {
			t.Fatalf("list broken schema = %d (%v)", status, body)
		}
	})

	t.Run("create_fails", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		env.exec(t, `DROP TABLE api_keys`)
		status, body := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"w11a-key"}`)
		if status != http.StatusInternalServerError {
			t.Fatalf("create broken schema = %d (%v)", status, body)
		}
	})

	t.Run("closed_db_patch", func(t *testing.T) {
		env := newTestEnv(t)
		created := create(t, env)
		if err := env.db.Close(); err != nil {
			t.Fatal(err)
		}
		status, _ := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+created,
			`{"expectedRevision":"2026-01-01T00:00:00.000Z","status":"disabled"}`)
		if status != http.StatusInternalServerError {
			t.Fatalf("closed db patch = %d", status)
		}
	})

	t.Run("update_trigger_failure", func(t *testing.T) {
		env := newTestEnv(t)
		created := create(t, env)
		env.exec(t, `CREATE TRIGGER w11a_block_key_update BEFORE UPDATE ON api_keys
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		revision := env.queryCell(t, `SELECT updated_at FROM api_keys WHERE id = ?`, created)
		status, body := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+created,
			`{"expectedRevision":"`+revision+`","status":"disabled"}`)
		if status != http.StatusInternalServerError {
			t.Fatalf("update trigger = %d (%v)", status, body)
		}
	})

	t.Run("delete_trigger_failure", func(t *testing.T) {
		env := newTestEnv(t)
		created := create(t, env)
		env.exec(t, `CREATE TRIGGER w11a_block_key_delete BEFORE DELETE ON api_keys
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		status, body := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+created, "")
		if status != http.StatusInternalServerError {
			t.Fatalf("delete trigger = %d (%v)", status, body)
		}
	})

	t.Run("binding_table_missing", func(t *testing.T) {
		env := newTestEnv(t)
		created := create(t, env)
		env.exec(t, `UPDATE api_keys SET quota_limits_json = ? WHERE id = ?`,
			`{"hourly":{"enabled":true,"limit":1,"hours":3}}`, created)
		env.exec(t, `DROP TABLE request_quota_hourly_window_scope_bindings`)
		revision := env.queryCell(t, `SELECT updated_at FROM api_keys WHERE id = ?`, created)
		status, _ := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+created,
			`{"expectedRevision":"`+revision+`","status":"disabled"}`)
		if status != http.StatusInternalServerError {
			t.Fatalf("binding table missing = %d", status)
		}
	})

	t.Run("secret_corruption", func(t *testing.T) {
		env := newTestEnv(t)
		created := create(t, env)
		env.exec(t, `UPDATE api_keys SET key_secret_encrypted = 'not-cipher-text' WHERE id = ?`, created)
		status, body := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+created+"/secret", "")
		if status == http.StatusOK {
			t.Fatalf("corrupted secret must not reveal: %v", body)
		}
	})
}

// ---------------------------------------------------------------------------
// Description normalization and duplicate-name error mapping.
// ---------------------------------------------------------------------------

func strPtr(value string) *string { return &value }

func TestW11ADescriptionAndDuplicateNameArms(t *testing.T) {
	if got, err := normalizeOptionalDescription(strPtr("   ")); err != nil || got.Valid {
		t.Fatalf("blank description = %+v, %v", got, err)
	}
	long := strings.Repeat("x", 201)
	if _, err := normalizeOptionalDescription(strPtr(long)); err == nil {
		t.Fatal("long description must fail")
	}
	if got, err := normalizeOptionalDescription(nil); err != nil || got.Valid {
		t.Fatalf("nil description = %+v, %v", got, err)
	}
	if err := duplicateAPIKeyNameError(errors.New("plain failure"), "k"); err != nil {
		t.Fatalf("unrelated error = %v", err)
	}
	if err := duplicateAPIKeyNameError(errors.New("UNIQUE constraint failed: api_keys.system_account_id, api_keys.name"), "k"); err == nil {
		t.Fatal("unique violation must map to a conflict")
	}
	if err := duplicateAPIKeyNameError(errors.New("idx_api_keys_owner_name_unique violated"), "k"); err == nil {
		t.Fatal("pg unique violation must map to a conflict")
	}
}

