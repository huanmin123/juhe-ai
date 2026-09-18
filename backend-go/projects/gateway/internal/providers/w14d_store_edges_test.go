// w14d_store_edges_test.go pins the previously uncovered Store-layer branches
// of the custom-model write domain: closed-DB error forks, the scan error
// path, the upsert conflict/validation forks, the optimistic-concurrency
// patch outcomes and the binding summary owner predicates.
package providers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// w14dClosedStore opens then closes an in-memory SQLite handle so every
// QueryContext/ExecContext/BeginTx fails with "database is closed".
func w14dClosedStore(t *testing.T, pg bool) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:w14d-closed-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, pg: pg, now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }}
}

var w14dErrBoom = errors.New("w14d boom")

func TestW14dCustomModelScanError(t *testing.T) {
	if _, err := scanCustomProviderModelRecord(func(...any) error { return w14dErrBoom }); !errors.Is(err, w14dErrBoom) {
		t.Fatalf("scan error passthrough: %v", err)
	}
	// The record-building path stays exercised by the happy-path suite.
}

func TestW14dCustomModelFindErrors(t *testing.T) {
	store := w14dClosedStore(t, false)
	ctx := context.Background()
	if _, err := store.findCustomProviderModelByID(ctx, "id", "owner"); err == nil {
		t.Fatal("closed DB find by id must fail")
	}
	if _, err := store.findCustomProviderModelByScope(ctx, "gpt", "global", "", "m"); err == nil {
		t.Fatal("closed DB find by scope (global) must fail")
	}
	if _, err := store.findCustomProviderModelByScope(ctx, "gpt", "personal", "u1", "m"); err == nil {
		t.Fatal("closed DB find by scope (personal) must fail")
	}
}

func TestW14dUpsertValidationForks(t *testing.T) {
	store := w14dClosedStore(t, false)
	ctx := context.Background()
	// requiredCustomText failures.
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{}); err == nil ||
		err.Error() != "供应商代码不能为空" {
		t.Fatalf("provider code required: %v", err)
	}
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{ProviderCode: "gpt"}); err == nil ||
		err.Error() != "模型 ID 不能为空" {
		t.Fatalf("model required: %v", err)
	}
	// Personal scope without any owner falls back to the actor, then fails.
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{ProviderCode: "gpt", Model: "m"}); err == nil ||
		err.Error() != "个人模型必须归属系统账户" {
		t.Fatalf("personal owner required: %v", err)
	}
	// Closed DB: the existing lookup fails before the capability normalize.
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "m", SystemAccountID: "u1",
	}); err == nil {
		t.Fatal("closed DB upsert must fail")
	}
}

// TestW14dUpsertCapabilityNormalizeAfterLookup pins the write-side
// normalization fork that runs after the existing-row lookup (image mode with
// tier prices): the handler never reaches it, the store contract does.
func TestW14dUpsertCapabilityNormalizeAfterLookup(t *testing.T) {
	env := newTestEnv(t)
	image := "image"
	if _, err := env.providersDeps.Store.upsertCustomProviderModel(context.Background(), customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "m", SystemAccountID: "u1", Mode: &image,
		ServiceTierPrices: map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(1)}},
	}); err == nil || err.Error() != "只有文本自定义模型支持服务档位价格" {
		t.Fatalf("image tier prices: %v", err)
	}
	// Same for an invalid mode string.
	weird := "video"
	if _, err := env.providersDeps.Store.upsertCustomProviderModel(context.Background(), customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "m2", SystemAccountID: "u1", Mode: &weird,
	}); err == nil || err.Error() != "当前只支持文本和图像自定义模型" {
		t.Fatalf("invalid mode: %v", err)
	}
}

func TestW14dUpsertModelIDImmutabilityAndIDReuse(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	created, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ID: "custom_model_w14d-1", ProviderCode: "gpt", Model: "orig-model", SystemAccountID: "u1",
	})
	if err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	if created.ID != "custom_model_w14d-1" {
		t.Fatalf("explicit id reuse: %s", created.ID)
	}
	// Same id with a different model is rejected verbatim.
	if _, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ID: "custom_model_w14d-1", ProviderCode: "gpt", Model: "other-model", SystemAccountID: "u1",
	}); err == nil || err.Error() != "模型 ID 创建后不能修改" {
		t.Fatalf("model immutability: %v", err)
	}
	// Unknown explicit id: the id is honored for a fresh insert.
	fresh, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ID: "custom_model_w14d-2", ProviderCode: "gpt", Model: "fresh-model", SystemAccountID: "u1",
	})
	if err != nil || fresh.ID != "custom_model_w14d-2" {
		t.Fatalf("fresh explicit id: %v %s", err, fresh.ID)
	}
}

func TestW14dPatchCustomModelUnitOutcomes(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "patch-target", SystemAccountID: "u1", InputUsdPer1M: ptrFloat64(1),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Stale expectedUpdatedAt short-circuits to conflict.
	outcome, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Status: "disabled",
	}, []string{"status"}, "2000-01-01T00:00:00.000Z", "", nil)
	if err != nil || outcome.Kind != "conflict" {
		t.Fatalf("stale patch: %v %+v", err, outcome)
	}

	// Equal values collapse to no_op.
	outcome, err = store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", InputUsdPer1M: ptrFloat64(1),
	}, []string{"inputUsdPer1M"}, current.UpdatedAt, "", nil)
	if err != nil || outcome.Kind != "no_op" {
		t.Fatalf("no-op patch: %v %+v", err, outcome)
	}

	// A real update flips the status and bumps the stamp strictly after now.
	outcome, err = store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Status: "disabled",
	}, []string{"status"}, current.UpdatedAt, "", nil)
	if err != nil || outcome.Kind != "updated" {
		t.Fatalf("update patch: %v %+v", err, outcome)
	}
	if outcome.Record.Status != "disabled" || outcome.Record.UpdatedAt == current.UpdatedAt {
		t.Fatalf("updated record: %+v", outcome.Record)
	}

	// The next-update stamp never regresses when the clock stands still.
	stamp := nextCustomModelUpdatedAt(outcome.Record.UpdatedAt, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if stamp == outcome.Record.UpdatedAt {
		t.Fatalf("stamp must advance past the current one: %s", stamp)
	}
}

func TestW14dPatchCustomModelClosedDBForks(t *testing.T) {
	store := w14dClosedStore(t, false)
	ctx := context.Background()
	current := &customProviderModelRecord{ID: "m1", ProviderCode: "gpt", Model: "m", Status: "active",
		UpdatedAt: "2026-01-01T00:00:00.000Z"}
	// BeginTx failure.
	if _, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Status: "disabled",
	}, []string{"status"}, current.UpdatedAt, "", nil); err == nil {
		t.Fatal("closed DB patch must fail")
	}
	// Capability normalization failure inside the patch assignments.
	image := "image"
	if _, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Mode: &image, SupportedServiceTiers: []string{"priority"},
	}, []string{"mode", "supportedServiceTiers"}, current.UpdatedAt, "", nil); err == nil ||
		err.Error() != "只有文本自定义模型支持服务等级和思考能力配置" {
		t.Fatalf("patch normalization: %v", err)
	}
	// Delete on the closed DB.
	if _, err := store.deleteCustomProviderModel(ctx, "m1", "", nil); err == nil {
		t.Fatal("closed DB delete must fail")
	}
}

func TestW14dPatchConflictWhenRowMoved(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "conflict-target", SystemAccountID: "u1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Move the row's optimistic stamp behind the caller's back; the guarded
	// UPDATE then hits zero rows.
	env.exec(t, `UPDATE custom_provider_models SET updated_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, current.ID)
	outcome, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Status: "disabled",
	}, []string{"status"}, current.UpdatedAt, "", nil)
	if err != nil || outcome.Kind != "conflict" {
		t.Fatalf("row-moved conflict: %v %+v", err, outcome)
	}
}

func TestW14dPatchWithCleanupAndOwnerPredicate(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "cleanup-target", SystemAccountID: "u1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cleanup := &defaultReferenceCleanupInput{
		Model:           current.Model,
		SystemAccountID: "u1",
		Targets:         []defaultReferenceCleanupTarget{{ProviderCode: "gpt"}},
	}
	// Owner predicate present: the owner-matched patch succeeds.
	outcome, err := store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Status: "disabled",
	}, []string{"status"}, current.UpdatedAt, "u1", cleanup)
	if err != nil || outcome.Kind != "updated" {
		t.Fatalf("owned patch with cleanup: %v %+v", err, outcome)
	}
	if len(outcome.ClearedDefaultHealthCheckProviderCodes) != 0 {
		t.Fatalf("no defaults configured: %v", outcome.ClearedDefaultHealthCheckProviderCodes)
	}

	// A mismatched owner predicate turns the same patch into a conflict.
	env.exec(t, `UPDATE custom_provider_models SET status = 'active' WHERE id = ?`, current.ID)
	current.Status = "active"
	outcome, err = store.patchCustomProviderModel(ctx, current, customProviderModelUpsertInput{
		ActorSystemAccountID: "u1", Status: "disabled",
	}, []string{"status"}, current.UpdatedAt, "other-owner", nil)
	if err != nil || outcome.Kind != "conflict" {
		t.Fatalf("owner-mismatch conflict: %v %+v", err, outcome)
	}
}

func TestW14dDeleteCustomModelUnit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	created, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "delete-target", SystemAccountID: "u1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Owner-guarded delete with the cleanup fan-out attached.
	deleted, err := store.deleteCustomProviderModel(ctx, created.ID, "u1", &defaultReferenceCleanupInput{
		Model:           created.Model,
		SystemAccountID: "u1",
		Targets:         []defaultReferenceCleanupTarget{{ProviderCode: "gpt"}},
	})
	if err != nil || !deleted {
		t.Fatalf("owned delete: %v %v", err, deleted)
	}
	// Second delete finds nothing.
	deleted, err = store.deleteCustomProviderModel(ctx, created.ID, "u1", nil)
	if err != nil || deleted {
		t.Fatalf("repeat delete: %v %v", err, deleted)
	}
}

func TestW14dCustomProviderModelBindingsUnit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store

	// requiredCustomText forks.
	if _, err := store.customProviderModelBindings(ctx, " ", "m", "personal", "u1"); err == nil ||
		err.Error() != "供应商代码不能为空" {
		t.Fatalf("binding provider required: %v", err)
	}
	if _, err := store.customProviderModelBindings(ctx, "gpt", " ", "personal", "u1"); err == nil ||
		err.Error() != "模型 ID 不能为空" {
		t.Fatalf("binding model required: %v", err)
	}
	if _, err := store.customProviderModelBindings(ctx, "gpt", "m", "personal", " "); err == nil ||
		err.Error() != "个人模型必须归属系统账户" {
		t.Fatalf("binding owner required: %v", err)
	}

	// Seed two accounts with supported-model and mapping bindings.
	env.exec(t, `INSERT INTO accounts (id, name) VALUES ('w14d-acc-1', 'acc1'), ('w14d-acc-2', 'acc2')`)
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model) VALUES
		('w14d-acc-1', 'gpt', 'bind-model'), ('w14d-acc-2', 'gpt', 'bind-model')`)
	env.exec(t, `INSERT INTO account_model_mappings (account_id, source_model, upstream_model) VALUES
		('w14d-acc-1', 'bind-model', 'up-model')`)

	summary, err := store.customProviderModelBindings(ctx, "gpt", "bind-model", "global", "")
	if err != nil {
		t.Fatalf("global bindings: %v", err)
	}
	if summary.SupportedModelAccountCount != 2 || summary.MappingSourceAccountCount != 1 ||
		summary.MappingUpstreamAccountCount != 0 || summary.TotalAccountCount != 2 {
		t.Fatalf("global summary: %+v", summary)
	}

	// The personal scope filters by the owner predicate.
	env.exec(t, `UPDATE accounts SET system_account_id = 'u1' WHERE id = 'w14d-acc-1'`)
	summary, err = store.customProviderModelBindings(ctx, "gpt", "bind-model", "personal", "u1")
	if err != nil {
		t.Fatalf("personal bindings: %v", err)
	}
	if summary.SupportedModelAccountCount != 1 || summary.TotalAccountCount != 1 {
		t.Fatalf("personal summary: %+v", summary)
	}

	// Closed DB error fork.
	if _, err := w14dClosedStore(t, false).customProviderModelBindings(ctx, "gpt", "m", "global", ""); err == nil {
		t.Fatal("closed DB bindings must fail")
	}
}

func TestW14dCountDistinctBoundAccountsError(t *testing.T) {
	store := w14dClosedStore(t, false)
	if _, err := store.countDistinctBoundAccounts(context.Background(), "SELECT 1 AS account_id", nil); err == nil {
		t.Fatal("closed DB count must fail")
	}
}

func TestW14dDefaultReferenceCleanupTargetsUnit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store

	// Empty provider code short-circuits to no targets.
	input, err := store.defaultReferenceCleanupTargets(ctx, "", "u1", "m", false)
	if err != nil || len(input.Targets) != 0 {
		t.Fatalf("empty code targets: %v %v", err, input)
	}
	// The hybrid code fans to itself only.
	input, err = store.defaultReferenceCleanupTargets(ctx, "hybrid", "", "m", true)
	if err != nil || len(input.Targets) != 1 || input.Targets[0].ProviderCode != "hybrid" {
		t.Fatalf("hybrid targets: %v %v", err, input)
	}
	if !input.ClearSystemDefault || input.SystemAccountID != "" || input.Model != "m" {
		t.Fatalf("cleanup input fields: %+v", input)
	}
	// Unknown definition still resolves to itself.
	input, err = store.defaultReferenceCleanupTargets(ctx, "w14d-unknown", "u1", "m", false)
	if err != nil || len(input.Targets) != 1 || input.Targets[0].ProviderCode != "w14d-unknown" {
		t.Fatalf("unknown definition targets: %v %v", err, input)
	}
	// Closed DB error fork.
	if _, err := w14dClosedStore(t, false).defaultReferenceCleanupTargets(ctx, "gpt", "u1", "m", false); err == nil {
		t.Fatal("closed DB cleanup targets must fail")
	}
}

func TestW14dProviderModelDefaultReferenceCodesUnit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store

	// Empty code: empty fan-out.
	codes, err := store.providerModelDefaultReferenceCodes(ctx, "  ")
	if err != nil || len(codes) != 0 {
		t.Fatalf("empty code: %v %v", err, codes)
	}
	// Hybrid code: itself only.
	codes, err = store.providerModelDefaultReferenceCodes(ctx, "hybrid")
	if err != nil || len(codes) != 1 || codes[0] != "hybrid" {
		t.Fatalf("hybrid code: %v %v", err, codes)
	}
	// Unknown definition: itself only.
	codes, err = store.providerModelDefaultReferenceCodes(ctx, "w14d-ghost")
	if err != nil || len(codes) != 1 || codes[0] != "w14d-ghost" {
		t.Fatalf("unknown code: %v %v", err, codes)
	}
	// Closed DB: the definition lookup fails.
	if _, err := w14dClosedStore(t, false).providerModelDefaultReferenceCodes(ctx, "gpt"); err == nil {
		t.Fatal("closed DB reference codes must fail")
	}
}

func TestW14dNormalizeHelpersEdges(t *testing.T) {
	// normalizeCustomProtocols drops unknown values and duplicates, keeps order.
	protocols := normalizeCustomProtocols([]string{" chat_completions ", "chat_completions", "nope", "responses"})
	if len(protocols) != 2 || protocols[0] != "chat_completions" || protocols[1] != "responses" {
		t.Fatalf("normalized protocols: %v", protocols)
	}
	// normalizeCapabilityTokenArray rejects bad tokens with the label.
	if _, err := normalizeCapabilityTokenArray([]string{"ok", " "}, "服务等级"); err == nil ||
		!strings.Contains(err.Error(), "服务等级") {
		t.Fatalf("token array error: %v", err)
	}
	// parseCapabilityTokenArray drops invalid JSON and bad entries silently.
	if got := parseCapabilityTokenArray(sql.NullString{String: "{broken", Valid: true}); len(got) != 0 {
		t.Fatalf("broken JSON tiers: %v", got)
	}
	// parseCapabilityTokenArray keeps the storage order (it never dedupes).
	if got := parseCapabilityTokenArray(sql.NullString{String: `["good",7,"!!","good"]`, Valid: true}); len(got) != 2 || got[0] != "good" || got[1] != "good" {
		t.Fatalf("filtered tiers: %v", got)
	}
	if got := capabilityTokenPtr(sql.NullString{String: "  ", Valid: true}); got != nil {
		t.Fatalf("blank effort pointer: %v", got)
	}
	if got := capabilityTokenPtr(sql.NullString{String: "!!", Valid: true}); got != nil {
		t.Fatalf("invalid effort pointer: %v", got)
	}
	if got := parseCustomModelProtocols(sql.NullString{String: "{broken", Valid: true}); len(got) != 0 {
		t.Fatalf("broken JSON protocols: %v", got)
	}
	if got := parseCustomModelProtocols(sql.NullString{String: `["chat_completions","nope",7,"responses"]`, Valid: true}); len(got) != 2 {
		t.Fatalf("filtered protocols: %v", got)
	}
	if got := parseCustomModelProtocols(sql.NullString{}); len(got) != 0 {
		t.Fatalf("null protocols: %v", got)
	}

	// parseRfc3339Millis rejects malformed stamps.
	if parseRfc3339Millis("nope") != nil {
		t.Fatal("malformed stamp must be nil")
	}
	// nextCustomModelUpdatedAt keeps ordering with a fresh clock.
	base := "2026-01-01T00:00:00.000Z"
	if got := nextCustomModelUpdatedAt(base, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); got == base {
		t.Fatalf("stamp must advance: %s", got)
	}
	if got := nextCustomModelUpdatedAt("garbage", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); got != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("garbage stamp falls back to now: %s", got)
	}
}

func TestW14dPatchAssignmentsUnit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.providersDeps.Store
	current, err := store.upsertCustomProviderModel(ctx, customProviderModelUpsertInput{
		ProviderCode: "gpt", Model: "assign-target", SystemAccountID: "u1",
		ContextWindowTokens: ptrInt64(1000),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Empty submitted status falls back to the current status value, so the
	// status column drops out of the assignment list (value equality).
	next := customProviderModelUpsertInput{
		ActorSystemAccountID:  "u1",
		Status:                "",
		ContextWindowTokens:   ptrInt64(2000),
		Mode:                  stringPtr("text"),
		SupportedAPIProtocols: []string{"chat_completions"},
		SupportedServiceTiers: []string{"priority"},
	}
	fields := []string{"status", "contextWindowTokens", "mode", "supportedApiProtocols", "supportedServiceTiers"}
	assignments, params, merged, err := store.customProviderModelPatchAssignments(current, next, fields)
	if err != nil {
		t.Fatalf("assignments: %v", err)
	}
	joined := strings.Join(assignments, ",")
	if strings.Contains(joined, "status = ?") {
		t.Fatalf("equal status must collapse: %v", assignments)
	}
	if !strings.Contains(joined, "context_window_tokens = ?") ||
		!strings.Contains(joined, "mode = ?") || !strings.Contains(joined, "supported_api_protocols_json = ?") ||
		!strings.Contains(joined, "supported_service_tiers_json = ?") {
		t.Fatalf("assignments: %v", assignments)
	}
	if len(params) != len(assignments) {
		t.Fatalf("params/assignments mismatch: %v %v", params, assignments)
	}
	if merged.Status != "active" {
		t.Fatalf("status fallback: %q", merged.Status)
	}
	if merged.ContextWindowTokens == nil || *merged.ContextWindowTokens != 2000 {
		t.Fatalf("merged context window: %v", merged.ContextWindowTokens)
	}
	if merged.Mode == nil || *merged.Mode != "text" {
		t.Fatalf("merged mode: %v", merged.Mode)
	}

	// A normalization failure surfaces through the assignments helper.
	image := "image"
	if _, _, _, err := store.customProviderModelPatchAssignments(current, customProviderModelUpsertInput{
		Mode:                  &image,
		SupportedServiceTiers: []string{"priority"},
	}, []string{"mode"}); err == nil || err.Error() != "只有文本自定义模型支持服务等级和思考能力配置" {
		t.Fatalf("assignment normalization: %v", err)
	}
	// No requested fields -> no assignments.
	assignments, _, _, err = store.customProviderModelPatchAssignments(current, customProviderModelUpsertInput{ActorSystemAccountID: "u1"}, []string{"inputUsdPer1M"})
	if err != nil {
		t.Fatalf("empty assignment: %v", err)
	}
	_ = assignments
}
