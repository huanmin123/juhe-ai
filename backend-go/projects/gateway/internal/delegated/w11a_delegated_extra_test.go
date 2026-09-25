package delegated

// w11a coverage arms: delegated error paths (broken schema, decode failures,
// dialect lock arms, quota-binding failures) plus the remaining pure-parser
// branches. Each sub-test builds its own in-memory SQLite database (the env
// keys off the full test name), so fixtures never collide.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// Deps without DB / limiter (nil-guards + Mount fallback limiter).
// ---------------------------------------------------------------------------

func TestW11ADepsWithoutDBAndLimiterArms(t *testing.T) {
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	(&Deps{}).Mount(k) // limiter fallback path must not panic

	empty := &Deps{}
	if row, err := empty.findProfileByID(context.Background(), "w11a-user"); row != nil || err != nil {
		t.Fatalf("findProfileByID nil db = %v, %v", row, err)
	}
	if row, err := empty.updateProfileDisplayName(context.Background(), "w11a-user", "x"); row != nil || err != nil {
		t.Fatalf("updateProfileDisplayName nil db = %v, %v", row, err)
	}
	if ids, err := empty.inheritedSourceAccountIDs(context.Background(), []string{"w11a-a"}); err != nil || len(ids) != 0 {
		t.Fatalf("inheritedSourceAccountIDs nil db = %v, %v", ids, err)
	}

	request := httptest.NewRequest(http.MethodGet, Prefix+"/groups", nil)
	if hasScope(request, "groups.read") {
		t.Fatal("hasScope without access context must be false")
	}
}

// ---------------------------------------------------------------------------
// Groups: broken schema 500s, decode failures, schedulingPolicy arm.
// ---------------------------------------------------------------------------

func TestW11AGroupBrokenSchemaAndDecodeArms(t *testing.T) {
	t.Run("broken_schema", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.read", "juhe:groups.write")
		f.env.seedGroup("w11a-grp", f.accountID, "g1", "openai", "personal", true)

		f.env.exec(`DROP TABLE groups`)
		if r := f.env.do(http.MethodGet, Prefix+"/groups", "", f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("listGroups broken schema = %d (%s)", r.status, r.raw)
		}
		if r := f.env.do(http.MethodGet, Prefix+"/groups/w11a-grp", "", f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("getGroup broken schema = %d (%s)", r.status, r.raw)
		}
		if r := f.env.do(http.MethodPatch, Prefix+"/groups/w11a-grp",
			`{"name":"x","expectedUpdatedAt":"2026-01-10T08:30:00.000Z"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchGroup broken schema = %d (%s)", r.status, r.raw)
		}
		if r := f.env.do(http.MethodDelete, Prefix+"/groups/w11a-grp", "", f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("deleteGroup broken schema = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("create_arms", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.write")
		if r := f.env.do(http.MethodPost, Prefix+"/groups", "{bad json", f.token); r.status != http.StatusBadRequest {
			t.Fatalf("createGroup bad json = %d (%s)", r.status, r.raw)
		}
		if r := f.env.do(http.MethodPost, Prefix+"/groups",
			`{"name":"x","providerCode":"openai","schedulingPolicy":"not-an-object"}`, f.token); r.status != http.StatusBadRequest {
			t.Fatalf("createGroup schedulingPolicy arm = %d (%s)", r.status, r.raw)
		}
		f.env.exec(`DROP TABLE providers`)
		if r := f.env.do(http.MethodPost, Prefix+"/groups",
			`{"name":"x","providerCode":"openai"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("createGroup provider lookup failure = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("patch_decode", func(t *testing.T) {
		f := newFixture(t, "juhe:groups.write")
		f.env.seedGroup("w11a-grp2", f.accountID, "g2", "openai", "personal", true)
		if r := f.env.do(http.MethodPatch, Prefix+"/groups/w11a-grp2", "{bad json", f.token); r.status != http.StatusBadRequest {
			t.Fatalf("patchGroup bad json = %d (%s)", r.status, r.raw)
		}
	})
}

// hasRouteStrategyBinding pagination: the binding lives past the first
// 500-item page, so the scan must advance pages before deciding.
func TestW11ADeleteGroupStrategyBindingScanPaginates(t *testing.T) {
	f := newFixture(t, "juhe:groups.read", "juhe:groups.write") // no route_strategies.write
	f.env.seedGroup("w11a-grp", f.accountID, "gpag", "openai", "personal", true)
	f.env.exec(`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i < 501)
		INSERT INTO route_strategies (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		SELECT 'w11a-st-'||i, ?, 's'||i, NULL, 'normal', 'active', 0, NULL, '2026-01-10T08:30:00.000Z', '2026-01-10T08:30:00.000Z' FROM seq`, f.accountID)
	f.env.seedStrategyBinding("w11a-bind", "w11a-st-501", f.accountID, "w11a-grp", 1, 1, "active")

	r := f.env.do(http.MethodDelete, Prefix+"/groups/w11a-grp", "", f.token)
	if r.status != http.StatusForbidden {
		t.Fatalf("deleteGroup bound on page two = %d (%s)", r.status, r.raw)
	}
	if r := f.env.do(http.MethodGet, Prefix+"/groups/w11a-grp", "", f.token); r.status != http.StatusOK {
		t.Fatalf("group must survive forbidden delete: %d (%s)", r.status, r.raw)
	}

	// Strategy list scan itself failing (broken schema) surfaces as 500.
	f.env.exec(`DROP TABLE route_strategies`)
	if r := f.env.do(http.MethodDelete, Prefix+"/groups/w11a-grp", "", f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("deleteGroup binding scan failure = %d (%s)", r.status, r.raw)
	}
}

// ---------------------------------------------------------------------------
// Route strategies: DTO description fields, decode failures, broken schema.
// ---------------------------------------------------------------------------

func TestW11AStrategyDTOAndBrokenSchemaArms(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.read", "juhe:route_strategies.write")
	f.env.seedStrategy("w11a-st", f.accountID, "strat", "normal", "active")
	f.env.exec(`UPDATE route_strategies SET description = 'w11a desc' WHERE id = 'w11a-st'`)

	r := f.env.do(http.MethodGet, Prefix+"/route-strategies", "", f.token)
	if r.status != http.StatusOK {
		t.Fatalf("listRouteStrategies = %d (%s)", r.status, r.raw)
	}
	data := r.data(t)
	items, _ := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v (%s)", items, r.raw)
	}
	if items[0].(map[string]any)["description"] != "w11a desc" {
		t.Fatalf("description missing: %s", r.raw)
	}

	f.env.exec(`DROP TABLE route_strategies`)
	if r := f.env.do(http.MethodGet, Prefix+"/route-strategies", "", f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("listRouteStrategies broken schema = %d (%s)", r.status, r.raw)
	}
	if r := f.env.do(http.MethodGet, Prefix+"/route-strategies/w11a-st", "", f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("getRouteStrategy broken schema = %d (%s)", r.status, r.raw)
	}
	if r := f.env.do(http.MethodDelete, Prefix+"/route-strategies/w11a-st", "", f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("deleteRouteStrategy broken schema = %d (%s)", r.status, r.raw)
	}
}

func TestW11AStrategyCreatePatchArms(t *testing.T) {
	f := newFixture(t, "juhe:groups.read", "juhe:route_strategies.read", "juhe:route_strategies.write")
	f.env.seedGroup("w11a-grp", f.accountID, "g1", "openai", "personal", true)

	if r := f.env.do(http.MethodPost, Prefix+"/route-strategies", "{bad json", f.token); r.status != http.StatusBadRequest {
		t.Fatalf("createRouteStrategy bad json = %d (%s)", r.status, r.raw)
	}
	if r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/none", "{bad json", f.token); r.status != http.StatusBadRequest {
		t.Fatalf("patchRouteStrategy bad json = %d (%s)", r.status, r.raw)
	}

	f.env.exec(`CREATE UNIQUE INDEX w11a_strategy_name_unique ON route_strategies(system_account_id, name)`)
	create := `{"name":"w11a-s1","mode":"normal","groupBindings":[{"groupId":"w11a-grp"}]}`
	if r := f.env.do(http.MethodPost, Prefix+"/route-strategies", create, f.token); r.status != http.StatusCreated {
		t.Fatalf("createRouteStrategy = %d (%s)", r.status, r.raw)
	}
	// Duplicate name renders the 已存在 conflict arm.
	if r := f.env.do(http.MethodPost, Prefix+"/route-strategies", create, f.token); r.status != http.StatusConflict {
		t.Fatalf("duplicate strategy name = %d (%s)", r.status, r.raw)
	}

	// Broken strategy table surfaces through create and through the patch
	// pre-read (FindDetail).
	f.env.exec(`DROP TABLE route_strategies`)
	if r := f.env.do(http.MethodPost, Prefix+"/route-strategies", create, f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("createRouteStrategy broken schema = %d (%s)", r.status, r.raw)
	}
	if r := f.env.do(http.MethodPatch, Prefix+"/route-strategies/w11a-st",
		`{"name":"x","expectedUpdatedAt":"2026-01-10T08:30:00.000Z"}`, f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("patchRouteStrategy broken schema = %d (%s)", r.status, r.raw)
	}
}

// parseStrategyMutation arms: binding priority/status, config null vs object.
func TestW11AParseStrategyMutationExtraArms(t *testing.T) {
	body := map[string]any{
		"name":                "s",
		"groupBindings":       []any{map[string]any{"groupId": "g", "priority": float64(2), "weight": float64(3), "status": "disabled"}},
		"normalRoutingConfig": map[string]any{"k": "v"},
	}
	input, ok, message := parseStrategyMutation(body, true)
	if !ok {
		t.Fatalf("parse = %s", message)
	}
	if len(input.Bindings) != 1 || *input.Bindings[0].Priority != 2 || *input.Bindings[0].Weight != 3 || input.Bindings[0].Status != "disabled" {
		t.Fatalf("bindings = %+v", input.Bindings)
	}
	if !input.HasNormal || input.NormalConfig == nil {
		t.Fatal("normalRoutingConfig object arm must set HasNormal with config")
	}
	body = map[string]any{
		"name":                "s",
		"groupBindings":       []any{map[string]any{"groupId": "g"}},
		"normalRoutingConfig": nil,
	}
	input, ok, _ = parseStrategyMutation(body, true)
	if !ok {
		t.Fatal("null normalRoutingConfig arm must parse")
	}
	if !input.HasNormal || input.NormalConfig != nil {
		t.Fatal("normalRoutingConfig null arm must set HasNormal without config")
	}

	// strategyMutation defaults: blank binding status → active, nil priority
	// → 1-based position.
	mutation := strategyMutation(&strategyMutationInput{
		HasBindings: true,
		Bindings:    []strategyBindingInput{{GroupID: "g"}},
	})
	if len(mutation.Bindings) != 1 || mutation.Bindings[0].Status != "active" || *mutation.Bindings[0].Priority != 1 {
		t.Fatalf("mutation defaults = %+v", mutation.Bindings)
	}
}

// ---------------------------------------------------------------------------
// API keys: DTO description, list failure, patch tx failure arms.
// ---------------------------------------------------------------------------

func TestW11AApiKeyDTOAndListArms(t *testing.T) {
	f := newFixture(t, "juhe:api_keys.read", "juhe:api_keys.write")
	f.env.seedStrategy("w11a-st", f.accountID, "strat", "normal", "active")
	f.env.seedApiKey("w11a-ak", f.accountID, "key1", "w11a-st", "active")
	f.env.exec(`UPDATE api_keys SET description = 'w11a key desc' WHERE id = 'w11a-ak'`)

	r := f.env.do(http.MethodGet, Prefix+"/api-keys", "", f.token)
	if r.status != http.StatusOK {
		t.Fatalf("listApiKeys = %d (%s)", r.status, r.raw)
	}
	data := r.data(t)
	items, _ := data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["description"] != "w11a key desc" {
		t.Fatalf("apiKeyDTO description arm: %s", r.raw)
	}

	f.env.exec(`DROP TABLE api_keys`)
	if r := f.env.do(http.MethodGet, Prefix+"/api-keys", "", f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("listApiKeys broken schema = %d (%s)", r.status, r.raw)
	}
}

func TestW11APatchApiKeyFailureArms(t *testing.T) {
	t.Run("guards_and_failures", func(t *testing.T) {
		f := newFixture(t, "juhe:api_keys.write")
		f.env.seedStrategy("w11a-st", f.accountID, "strat", "normal", "active")
		f.env.seedApiKey("w11a-ak", f.accountID, "key1", "w11a-st", "active")
		revision := "2026-01-10T08:30:00.000Z"

		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak", "{bad json", f.token); r.status != http.StatusBadRequest {
			t.Fatalf("patchApiKey bad json = %d (%s)", r.status, r.raw)
		}

		// Unknown target strategy propagates the store guard (400).
		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak",
			fmt.Sprintf(`{"expectedRevision":%q,"routeStrategyId":"w11a-missing"}`, revision), f.token); r.status != http.StatusBadRequest {
			t.Fatalf("patchApiKey unknown strategy = %d (%s)", r.status, r.raw)
		}

		// Unparsable stored revision fails nextApiKeyRevision.
		f.env.exec(`UPDATE api_keys SET updated_at = 'not-a-time' WHERE id = 'w11a-ak'`)
		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak",
			`{"expectedRevision":"not-a-time","status":"disabled"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchApiKey bad stored revision = %d (%s)", r.status, r.raw)
		}
		f.env.exec(`UPDATE api_keys SET updated_at = ? WHERE id = 'w11a-ak'`, revision)

		// UPDATE failure via trigger.
		f.env.exec(`CREATE TRIGGER w11a_block_key_update BEFORE UPDATE ON api_keys
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak",
			fmt.Sprintf(`{"expectedRevision":%q,"status":"disabled"}`, revision), f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchApiKey update failure = %d (%s)", r.status, r.raw)
		}
		f.env.exec(`DROP TRIGGER w11a_block_key_update`)

		// Quota-scope binding DELETE failure (missing table).
		f.env.exec(`DROP TABLE request_quota_hourly_window_scope_bindings`)
		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak",
			fmt.Sprintf(`{"expectedRevision":%q,"status":"disabled"}`, revision), f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchApiKey binding delete failure = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("closed_db", func(t *testing.T) {
		f := newFixture(t, "juhe:api_keys.write")
		f.env.seedApiKey("w11a-ak2", f.accountID, "key2", "strat", "active")
		if err := f.env.db.Close(); err != nil {
			t.Fatal(err)
		}
		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak2",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","status":"disabled"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchApiKey closed db = %d (%s)", r.status, r.raw)
		}
	})
}

// Quota-scope binding INSERT failure: the binding table exists but lacks the
// expected columns.
func TestW11APatchApiKeyBindingInsertFailure(t *testing.T) {
	f := newFixture(t, "juhe:api_keys.write")
	f.env.seedStrategy("w11a-st", f.accountID, "strat", "normal", "active")
	f.env.seedApiKey("w11a-ak", f.accountID, "key1", "w11a-st", "disabled")
	f.env.exec(`UPDATE api_keys SET quota_limits_json = ? WHERE id = 'w11a-ak'`, `{"hourly":{"enabled":true,"hours":5}}`)
	f.env.exec(`DROP TABLE request_quota_hourly_window_scope_bindings`)
	f.env.exec(`CREATE TABLE request_quota_hourly_window_scope_bindings (id TEXT PRIMARY KEY)`)

	r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak",
		`{"expectedRevision":"2026-01-10T08:30:00.000Z","status":"active"}`, f.token)
	if r.status != http.StatusInternalServerError {
		t.Fatalf("binding insert failure = %d (%s)", r.status, r.raw)
	}
}

// PGDialect arms: the FOR UPDATE lock clause is emitted (then rejected by
// SQLite, which is fine for the coverage of the assignment and the error
// propagation).
func TestW11APGDialectLockArms(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		f := newFixture(t, "juhe:profile.write")
		f.env.deps.PGDialect = true
		if r := f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"w11a name"}`, f.token); r.status != http.StatusConflict {
			t.Fatalf("patchProfile PG lock arm = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("api_key", func(t *testing.T) {
		f := newFixture(t, "juhe:api_keys.write")
		f.env.seedStrategy("w11a-st", f.accountID, "strat", "normal", "active")
		f.env.seedApiKey("w11a-ak", f.accountID, "key1", "w11a-st", "active")
		f.env.deps.PGDialect = true
		if r := f.env.do(http.MethodPatch, Prefix+"/api-keys/w11a-ak",
			`{"expectedRevision":"2026-01-10T08:30:00.000Z","status":"disabled"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchApiKey PG lock arm = %d (%s)", r.status, r.raw)
		}
	})
}

// ---------------------------------------------------------------------------
// AI accounts: facts loading failures, page options, patch arms.
// ---------------------------------------------------------------------------

func TestW11AAiAccountFactsAndPageArms(t *testing.T) {
	f := newFixture(t, "juhe:ai_accounts.read", "juhe:ai_accounts.write")
	f.env.seedAiAccount("w11a-a1", f.accountID, "acct1", "active", "")
	now := "2026-01-10T08:30:00.000Z"
	f.env.exec(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES ('w11a-a1', 'openai', 'gpt-4o', ?)`, now)
	f.env.exec(`INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('w11a-a1', 'openai', 'gpt-4o', 'responses', 'gpt-4o', 'responses', 1, ?, ?)`, now, now)

	r := f.env.do(http.MethodGet, Prefix+"/ai-accounts?page=2&pageSize=1", "", f.token)
	if r.status != http.StatusOK {
		t.Fatalf("listAiAccounts paged = %d (%s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["page"].(float64) != 2 || data["pageSize"].(float64) != 1 {
		t.Fatalf("page options ignored: %s", r.raw)
	}

	f.env.exec(`DROP TABLE account_supported_models`)
	if r := f.env.do(http.MethodGet, Prefix+"/ai-accounts", "", f.token); r.status != http.StatusInternalServerError {
		t.Fatalf("listAiAccounts model facts failure = %d (%s)", r.status, r.raw)
	}
}

func TestW11APatchAiAccountArms(t *testing.T) {
	t.Run("conflict_and_facts", func(t *testing.T) {
		f := newFixture(t, "juhe:ai_accounts.write")
		f.env.seedAiAccount("w11a-a1", f.accountID, "acct1", "active", "")
		f.env.seedAiAccount("w11a-a2", f.accountID, "acct2", "active", "")

		if r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/w11a-a1", "{bad json", f.token); r.status != http.StatusBadRequest {
			t.Fatalf("patchAiAccount bad json = %d (%s)", r.status, r.raw)
		}
		// Duplicate name → accounts.ConflictError → 409.
		if r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/w11a-a2",
			`{"expectedConfigRevision":1,"name":"acct1"}`, f.token); r.status != http.StatusConflict {
			t.Fatalf("patchAiAccount duplicate name = %d (%s)", r.status, r.raw)
		}

		// Facts failure after a successful patch (model table dropped first).
		f.env.exec(`DROP TABLE account_supported_models`)
		if r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/w11a-a1",
			`{"expectedConfigRevision":1,"status":"disabled"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchAiAccount facts failure = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("list_failure", func(t *testing.T) {
		f := newFixture(t, "juhe:ai_accounts.write")
		f.env.exec(`DROP TABLE accounts`)
		if r := f.env.do(http.MethodPatch, Prefix+"/ai-accounts/none",
			`{"expectedConfigRevision":1,"status":"disabled"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchAiAccount list failure = %d (%s)", r.status, r.raw)
		}
	})
}

// ---------------------------------------------------------------------------
// Profile / request limits: broken schema, settings failures, dateParts.
// ---------------------------------------------------------------------------

type w11aErrSettings struct{ err error }

func (s *w11aErrSettings) SettingValue(string) (string, error) { return "", s.err }

func TestW11AProfileAndRequestLimitArms(t *testing.T) {
	t.Run("broken_schema", func(t *testing.T) {
		f := newFixture(t, "juhe:profile.read", "juhe:profile.write", "juhe:request_limits.read")
		f.env.exec(`DROP TABLE system_accounts`)
		if r := f.env.do(http.MethodGet, Prefix+"/profile", "", f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("getProfile broken schema = %d (%s)", r.status, r.raw)
		}
		if r := f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"w11a name"}`, f.token); r.status != http.StatusInternalServerError {
			t.Fatalf("patchProfile broken schema = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("update_trigger_failure", func(t *testing.T) {
		f := newFixture(t, "juhe:profile.write")
		f.env.exec(`CREATE TRIGGER w11a_block_profile_update BEFORE UPDATE ON system_accounts
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		if r := f.env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"w11a name"}`, f.token); r.status != http.StatusConflict {
			t.Fatalf("patchProfile update failure = %d (%s)", r.status, r.raw)
		}
	})

	t.Run("request_limit_settings", func(t *testing.T) {
		f := newFixture(t, "juhe:request_limits.read")
		f.env.deps.Settings = &w11aErrSettings{err: errors.New("w11a settings down")}
		rec := httptest.NewRecorder()
		f.env.deps.getRequestLimitsSnapshot(rec, w9CRequestAs(t, http.MethodGet, Prefix+"/request-limits", "", f.accountID))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("request limits settings failure = %d (%s)", rec.Code, rec.Body.String())
		}

		ctx := context.Background()
		if _, err := f.env.deps.loadGlobalRequestLimitSettings(ctx); err == nil {
			t.Fatal("settings error must propagate")
		}
		f.env.deps.Settings = &fakeSettings{values: map[string]string{
			"gatewayUserRequestLimitPerMinute": "not-a-number",
		}}
		if _, err := f.env.deps.loadGlobalRequestLimitSettings(ctx); err == nil {
			t.Fatal("invalid numeric setting must fail")
		}
		f.env.deps.Settings = &fakeSettings{values: map[string]string{
			"gatewayUserRequestLimitPerMinute": "-5",
		}}
		if _, err := f.env.deps.loadGlobalRequestLimitSettings(ctx); err == nil {
			t.Fatal("negative setting must fail")
		}
		f.env.deps.Settings = &fakeSettings{values: map[string]string{
			"usageStatsTimezone": "not-json",
		}}
		if _, err := f.env.deps.loadGlobalRequestLimitSettings(ctx); err == nil {
			t.Fatal("invalid timezone json must fail")
		}
		f.env.deps.Settings = &fakeSettings{values: map[string]string{
			"usageStatsTimezone": `"Asia/Shanghai"`,
		}}
		settings, err := f.env.deps.loadGlobalRequestLimitSettings(ctx)
		if err != nil || settings.Timezone != "Asia/Shanghai" {
			t.Fatalf("timezone override = %v, %v", settings, err)
		}
		// Unknown location falls back to UTC inside dateParts.
		if parts := dateParts("W11a/Nowhere", 0); parts.year != 1970 {
			t.Fatalf("dateParts fallback = %+v", parts)
		}
	})
}

// inheritedSourceAccountIDs query failure (accounts table gone) and the
// blank-ids early return.
func TestW11AInheritedSourceAccountIDsArms(t *testing.T) {
	f := newFixture(t, "juhe:ai_accounts.read")
	f.env.seedAiAccount("w11a-a1", f.accountID, "acct1", "active", "w11a-src")
	if ids, err := f.env.deps.inheritedSourceAccountIDs(context.Background(), []string{"w11a-a1"}); err != nil || !ids["w11a-a1"] {
		t.Fatalf("inherited = %v, %v", ids, err)
	}
	if ids, err := f.env.deps.inheritedSourceAccountIDs(context.Background(), nil); len(ids) != 0 || err != nil {
		t.Fatalf("blank ids = %v, %v", ids, err)
	}
	f.env.exec(`DROP TABLE accounts`)
	if _, err := f.env.deps.inheritedSourceAccountIDs(context.Background(), []string{"w11a-a1"}); err == nil {
		t.Fatal("broken schema must fail inherited lookup")
	}
}
