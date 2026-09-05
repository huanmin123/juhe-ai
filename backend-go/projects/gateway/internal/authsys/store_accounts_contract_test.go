package authsys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"

	_ "modernc.org/sqlite"
)

// readyOwnerGate is the gate shape a proven handoff produces.
var readyOwnerGate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}

// captureSink records operation-log entries for assertions.
type captureSink struct {
	mu      sync.Mutex
	entries []OperationLogEntry
}

func (s *captureSink) Record(entry OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *captureSink) snapshot() []OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]OperationLogEntry(nil), s.entries...)
}

// recordingInvalidator records the post-commit invalidation channels.
type recordingInvalidator struct {
	mu         sync.Mutex
	runtime    []string
	validation []string
	// failValidation makes InvalidateAPIKeyValidation return an error.
	failValidation bool
}

func (r *recordingInvalidator) InvalidateRuntime(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtime = append(r.runtime, reason)
}

func (r *recordingInvalidator) InvalidateAPIKeyValidation(reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.validation = append(r.validation, reason)
	if r.failValidation {
		return errors.New("validation cache unavailable")
	}
	return nil
}

func (r *recordingInvalidator) calls() (runtime []string, validation []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.runtime...), append([]string(nil), r.validation...)
}

// sealedSecrets records SealSecret plaintexts.
type sealedSecrets struct {
	mu       sync.Mutex
	plainIVs []string
}

func (s *sealedSecrets) SealSecret(_ context.Context, plaintext string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plainIVs = append(s.plainIVs, plaintext)
	return "v1:test-sealed:" + plaintext[len(plaintext)-4:], nil
}

// newContractTestDB mirrors newTestEnv's SQLite recipe plus the default
// resource relations (groups / route_strategies / route_strategy_groups /
// api_keys) with the Node production column defaults.
func newContractTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:authsys-contract-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER CHECK (ai_account_limit BETWEEN 0 AND 1000000), request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, key_hash TEXT NOT NULL UNIQUE, key_prefix TEXT NOT NULL, key_suffix TEXT NOT NULL, key_secret_encrypted TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, purpose TEXT NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat')), expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT, availability_schedule_next_check_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// TestAccountStoreOwnerGateFailsClosed proves BUG-0169.3: a store assembled
// without a proven handoff gate refuses every operation (writes and reads),
// while the same store with the proven gate serves the identical calls.
func TestAccountStoreOwnerGateFailsClosed(t *testing.T) {
	db := newContractTestDB(t)
	closed, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := closed.Create(ctx, CreateInput{Username: "gate-user", DisplayName: "gate_user", Password: "gate-password"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Create err=%v want ErrOwnerGate", err)
	}
	if _, err := closed.Patch(ctx, "sysacc_x", PatchInput{ExpectedUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Patch err=%v want ErrOwnerGate", err)
	}
	if _, err := closed.UpdatePassword(ctx, "sysacc_x", "gate-password"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("UpdatePassword err=%v want ErrOwnerGate", err)
	}
	if _, err := closed.FindByID(ctx, "sysacc_x"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("FindByID err=%v want ErrOwnerGate", err)
	}
	if _, err := closed.FindByUsername(ctx, "gate-user"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("FindByUsername err=%v want ErrOwnerGate", err)
	}
	if _, _, _, err := closed.ListPage(ctx, "", 1, 20); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ListPage err=%v want ErrOwnerGate", err)
	}
	if _, err := closed.ListOptions(ctx, nil, "", 50); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ListOptions err=%v want ErrOwnerGate", err)
	}

	ready, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	item, err := ready.Create(ctx, CreateInput{Username: "gate-user", DisplayName: "gate_user", Password: "gate-password"})
	if err != nil {
		t.Fatalf("Create with ready gate: %v", err)
	}
	if item.ID == "" {
		t.Fatal("Create returned empty id")
	}
	// CheckContract passes once every relation exists.
	if err := ready.CheckContract(ctx); err != nil {
		t.Fatalf("CheckContract: %v", err)
	}
	// CheckContract fails closed on a database missing the relations this
	// store (and its default-resource bootstrap) writes.
	bare, err := sql.Open("sqlite", "file:authsys-contract-bare-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	bareStore, err := NewAccountStore(bare, modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	if err := bareStore.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("CheckContract on bare db err=%v want verification failure", err)
	}
}

// TestSuperAdminAdvisoryLockContract proves the BUG-0169.4 PostgreSQL fix at
// the SQL level (a real pg_advisory_xact_lock cannot run in unit tests): the
// issued statement and lock key match Node byte for byte, the placeholder
// renders as $1 in the PostgreSQL dialect, and the lock fires exactly when
// the patch carries role or status.
func TestSuperAdminAdvisoryLockContract(t *testing.T) {
	if superAdminAdvisoryLockQuery != "SELECT pg_advisory_xact_lock(hashtextextended(?, 0))" {
		t.Fatalf("advisory lock query drifted from Node: %q", superAdminAdvisoryLockQuery)
	}
	if superAdminAdvisoryLockKey != "juhe-ai:system-accounts:active-super-admin" {
		t.Fatalf("advisory lock key drifted from Node: %q", superAdminAdvisoryLockKey)
	}
	pg, err := NewAccountStore(sql.OpenDB(nil), modelcheckauth.Postgres, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.db.Close()
	if rendered := pg.bind(superAdminAdvisoryLockQuery); rendered != "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))" {
		t.Fatalf("postgres render=%q", rendered)
	}
	sqlite, err := NewAccountStore(sql.OpenDB(nil), modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.db.Close()
	if rendered := sqlite.bind(superAdminAdvisoryLockQuery); rendered != superAdminAdvisoryLockQuery {
		t.Fatalf("sqlite render=%q", rendered)
	}
	cases := []struct {
		input PatchInput
		want  bool
	}{
		{input: PatchInput{}, want: false},
		{input: PatchInput{DisplayName: strPtr("n")}, want: false},
		{input: PatchInput{Password: strPtr("new-password")}, want: false},
		{input: PatchInput{Role: strPtr("user")}, want: true},
		{input: PatchInput{Status: strPtr("disabled")}, want: true},
		{input: PatchInput{Role: strPtr("user"), Status: strPtr("disabled")}, want: true},
	}
	for _, tc := range cases {
		if got := superAdminInvariantLockNeeded(tc.input); got != tc.want {
			t.Fatalf("superAdminInvariantLockNeeded(%+v)=%v want %v", tc.input, got, tc.want)
		}
	}
}

func strPtr(value string) *string { return &value }

// TestPatchLastSuperAdminSQLiteSingleWriter proves the last-super-admin
// invariant holds under concurrency on SQLite: the single-writer transaction
// serializes the role/status patches (the equivalent of the PostgreSQL
// advisory lock), so concurrent demotions can never both commit and leave
// zero active super admins (BUG-0169.4).
func TestPatchLastSuperAdminSQLiteSingleWriter(t *testing.T) {
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := store.Create(ctx, CreateInput{Username: "super-one", DisplayName: "super_one", Password: "super-password", Role: "super_admin"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, CreateInput{Username: "super-two", DisplayName: "super_two", Password: "super-password", Role: "super_admin"})
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		name string
		err  error
	}
	results := make(chan outcome, 2)
	wg := sync.WaitGroup{}
	for _, target := range []AccountListItem{first, second} {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Patch(ctx, target.ID, PatchInput{
				ExpectedUpdatedAt: target.EditVersion,
				Status:            strPtr("disabled"),
			})
			results <- outcome{name: target.Username, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var failures, successes int
	var lastSuperAdminRejections int
	for result := range results {
		if result.err == nil {
			successes++
			continue
		}
		failures++
		var validation *ValidationError
		if errors.As(result.err, &validation) && validation.Message == "至少保留一个启用的超级管理员" {
			lastSuperAdminRejections++
		} else {
			t.Fatalf("%s unexpected error: %v", result.name, result.err)
		}
	}
	if successes != 1 || lastSuperAdminRejections != 1 {
		t.Fatalf("successes=%d rejections=%d want exactly 1/1", successes, lastSuperAdminRejections)
	}
	var activeSuperAdmins int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_accounts WHERE role='super_admin' AND status='active'`).Scan(&activeSuperAdmins); err != nil {
		t.Fatal(err)
	}
	if activeSuperAdmins != 1 {
		t.Fatalf("active super admins=%d want 1", activeSuperAdmins)
	}
}

// TestCreateSeedsDefaultResources proves BUG-0170.1: with the production
// ensurer wired, Create seeds the eight built-in groups, seven default route
// strategies (hybrid excluded) with bindings, seven default API keys and the
// chat API key inside the same transaction, with the Node field values.
func TestCreateSeedsDefaultResources(t *testing.T) {
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &sealedSecrets{}
	store.SetSecretSealer(secrets)
	store.SetDefaultResourceEnsurer(NewSQLDefaultResources(store, secrets))

	item, err := store.Create(context.Background(), CreateInput{
		Username: "seeded-user", DisplayName: "seeded_user", Password: "seed-password",
		Description: strPtr("  受限说明  "),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Groups: 8 built-ins, defaults, personal type via schema default.
	type groupRow struct {
		name, provider, groupType string
		enabled, isDefault        int
	}
	groups := map[string]groupRow{}
	groupCount := 0
	rows, err := db.Query(`SELECT name, provider_code, group_type, enabled, is_default FROM groups WHERE system_account_id = ?`, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row groupRow
		if err := rows.Scan(&row.name, &row.provider, &row.groupType, &row.enabled, &row.isDefault); err != nil {
			t.Fatal(err)
		}
		groups[row.provider] = row
		groupCount++
	}
	rows.Close()
	wantSeeds := []defaultResourceGroupSeed{
		{name: "默认 OpenAI 兼容分组", provider: "openai"},
		{name: "默认 GPT 分组", provider: "gpt"},
		{name: "默认 xAI 分组", provider: "xai"},
		{name: "默认 DeepSeek 分组", provider: "deepseek"},
		{name: "默认 Anthropic 分组", provider: "anthropic"},
		{name: "默认 Gemini 分组", provider: "gemini"},
		{name: "默认 GLM 分组", provider: "glm"},
		{name: "默认混合供应商分组", provider: "hybrid", description: "混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射"},
	}
	if groupCount != len(wantSeeds) {
		t.Fatalf("groups=%d want %d", groupCount, len(wantSeeds))
	}
	for _, seed := range wantSeeds {
		row, ok := groups[seed.provider]
		if !ok {
			t.Fatalf("missing default group for provider %s", seed.provider)
		}
		if row.name != seed.name || row.enabled != 1 || row.isDefault != 1 || row.groupType != "personal" {
			t.Fatalf("group %+v != seed %+v", row, seed)
		}
	}

	// Route strategies: 7 (hybrid excluded), default normal/active with the
	// Node-generated names.
	routeNames := map[string]string{}
	routeCount := 0
	rows, err = db.Query(`SELECT name, mode, status, is_default FROM route_strategies WHERE system_account_id = ?`, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, mode, status string
		var isDefault int
		if err := rows.Scan(&name, &mode, &status, &isDefault); err != nil {
			t.Fatal(err)
		}
		if mode != "normal" || status != "active" || isDefault != 1 {
			t.Fatalf("route %s mode=%s status=%s isDefault=%d", name, mode, status, isDefault)
		}
		routeNames[name] = name
		routeCount++
	}
	rows.Close()
	if routeCount != 7 {
		t.Fatalf("route strategies=%d want 7", routeCount)
	}
	for _, want := range []string{"默认 OpenAI 兼容路由", "默认 GPT 路由", "默认 xAI 路由", "默认 DeepSeek 路由", "默认 Anthropic 路由", "默认 Gemini 路由", "默认 GLM 路由"} {
		if _, ok := routeNames[want]; !ok {
			t.Fatalf("missing route strategy %q (got %v)", want, routeNames)
		}
	}

	// Bindings: one active priority-1/weight-1 binding per route.
	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategy_groups WHERE system_account_id = ? AND priority = 1 AND weight = 1 AND status = 'active'`, item.ID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 7 {
		t.Fatalf("route strategy bindings=%d want 7", bindings)
	}

	// API keys: 7 defaults (is_default=1, purpose 'general' schema default)
	// + 1 chat key (purpose 'chat', is_default=0) bound to the GPT route.
	type keyRow struct {
		name, purpose, status, keyPrefix, keySuffix, sealed string
		isDefault                                           int
	}
	keys := []keyRow{}
	rows, err = db.Query(`SELECT name, purpose, status, key_prefix, key_suffix, key_secret_encrypted, is_default FROM api_keys WHERE system_account_id = ?`, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row keyRow
		if err := rows.Scan(&row.name, &row.purpose, &row.status, &row.keyPrefix, &row.keySuffix, &row.sealed, &row.isDefault); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, row)
	}
	rows.Close()
	if len(keys) != 8 {
		t.Fatalf("api keys=%d want 8", len(keys))
	}
	var chatKeys, defaultKeys int
	chatBoundToGPTRoute := false
	for _, key := range keys {
		if key.status != "active" {
			t.Fatalf("key %s status=%s", key.name, key.status)
		}
		if key.purpose == "chat" {
			chatKeys++
			if key.isDefault != 0 || key.name != "AI 对话 API Key" {
				t.Fatalf("chat key %+v", key)
			}
			var provider string
			if err := db.QueryRow(`SELECT g.provider_code FROM api_keys k JOIN route_strategy_groups rsg ON rsg.route_strategy_id = k.route_strategy_id JOIN groups g ON g.id = rsg.group_id WHERE k.system_account_id = ? AND k.purpose = 'chat'`, item.ID).Scan(&provider); err != nil {
				t.Fatal(err)
			}
			chatBoundToGPTRoute = provider == "gpt"
			continue
		}
		defaultKeys++
		if key.isDefault != 1 {
			t.Fatalf("default key %+v", key)
		}
		if !strings.HasPrefix(key.name, "默认 ") || !strings.HasSuffix(key.name, "API Key") {
			t.Fatalf("default key name %q", key.name)
		}
		if key.sealed == "" || !strings.HasPrefix(key.sealed, "v1:test-sealed:") {
			t.Fatalf("key %s not sealed through the injected sealer: %q", key.name, key.sealed)
		}
	}
	if chatKeys != 1 || defaultKeys != 7 {
		t.Fatalf("chatKeys=%d defaultKeys=%d want 1/7", chatKeys, defaultKeys)
	}
	if !chatBoundToGPTRoute {
		t.Fatal("chat key is not bound to the default GPT route")
	}
	if len(secrets.plainIVs) != 8 {
		t.Fatalf("sealed secrets=%d want 8", len(secrets.plainIVs))
	}
	for _, secret := range secrets.plainIVs {
		if len(secret) != 67 || !strings.HasPrefix(secret, "sk-") {
			t.Fatalf("secret shape %q", secret[:10]+"...")
		}
	}

	// Description normalization: trimmed value persisted.
	var description string
	if err := db.QueryRow(`SELECT COALESCE(description,'') FROM system_accounts WHERE id = ?`, item.ID).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if description != "受限说明" {
		t.Fatalf("description=%q", description)
	}
}

// TestNextDefaultResourceName proves the Node dedupe helper semantics.
func TestNextDefaultResourceName(t *testing.T) {
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	ensurer := NewSQLDefaultResources(store, &sealedSecrets{})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	name, err := ensurer.nextDefaultResourceName(context.Background(), tx, "api_keys", "sysacc_x", "AI 对话 API Key")
	if err != nil || name != "AI 对话 API Key" {
		t.Fatalf("fresh name=%q err=%v", name, err)
	}
	for index, taken := range []string{"AI 对话 API Key", "AI 对话 API Key 2"} {
		if _, err := tx.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at, updated_at) VALUES (?, 'sysacc_x', 'route_x', ?, ?, 'p', 's', 'e', '2026-01-01', '2026-01-01')`, "key_"+taken, taken, "hash_"+fmt.Sprint(index)); err != nil {
			t.Fatal(err)
		}
	}
	name, err = ensurer.nextDefaultResourceName(context.Background(), tx, "api_keys", "sysacc_x", "AI 对话 API Key")
	if err != nil || name != "AI 对话 API Key 3" {
		t.Fatalf("deduped name=%q err=%v", name, err)
	}
	// The suffix rules mirror the Node regex replace.
	if got := defaultRouteStrategyNameForGroup("默认 GPT 分组"); got != "默认 GPT 路由" {
		t.Fatalf("route name=%q", got)
	}
	if got := defaultAPIKeyNameForRouteStrategy("默认 GPT 路由"); got != "默认 GPT API Key" {
		t.Fatalf("api key name=%q", got)
	}
}

// TestPatchCacheInvalidationChannels proves BUG-0170.5: status, image
// generation and request-limits changes invalidate both channels post-commit
// with the Node reason strings; a validation-channel failure surfaces through
// the mutation receipt; unrelated or no-op patches invalidate nothing.
func TestPatchCacheInvalidationChannels(t *testing.T) {
	buildStore := func(t *testing.T) (*AccountStore, *AccountListItem) {
		t.Helper()
		db := newContractTestDB(t)
		store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, readyOwnerGate)
		if err != nil {
			t.Fatal(err)
		}
		item, err := store.Create(context.Background(), CreateInput{Username: "invalid-user", DisplayName: "invalid_user", Password: "invalid-password"})
		if err != nil {
			t.Fatal(err)
		}
		return store, &item
	}

	t.Run("status change invalidates both channels", func(t *testing.T) {
		store, item := buildStore(t)
		invalidator := &recordingInvalidator{}
		store.SetCacheInvalidator(invalidator)
		result, err := store.Patch(context.Background(), item.ID, PatchInput{ExpectedUpdatedAt: item.EditVersion, Status: strPtr("disabled")})
		if err != nil {
			t.Fatal(err)
		}
		runtime, validation := invalidator.calls()
		if len(runtime) != 1 || runtime[0] != "system_account_status_changed" {
			t.Fatalf("runtime=%v", runtime)
		}
		if len(validation) != 1 || validation[0] != "system_account_status_changed" {
			t.Fatalf("validation=%v", validation)
		}
		if result.APIKeyValidationCacheInvalidationFailed {
			t.Fatal("unexpected failure flag")
		}
	})

	t.Run("image generation and request limits reasons", func(t *testing.T) {
		image := true
		perMinute := 5
		for _, tc := range []struct {
			name   string
			patch  func(item AccountListItem) PatchInput
			reason string
		}{
			{
				name:   "imageGenerationEnabled",
				reason: "system_account_image_generation_changed",
				patch: func(item AccountListItem) PatchInput {
					return PatchInput{ExpectedUpdatedAt: item.EditVersion, ImageGenerationEnabled: &image}
				},
			},
			{
				name:   "requestLimits",
				reason: "system_account_request_limits_changed",
				patch: func(item AccountListItem) PatchInput {
					return PatchInput{ExpectedUpdatedAt: item.EditVersion, RequestLimits: &UserRequestLimits{PerMinute: &perMinute}, RequestLimitsPresent: true}
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store, item := buildStore(t)
				invalidator := &recordingInvalidator{}
				store.SetCacheInvalidator(invalidator)
				if _, err := store.Patch(context.Background(), item.ID, tc.patch(*item)); err != nil {
					t.Fatal(err)
				}
				runtime, validation := invalidator.calls()
				if len(runtime) != 1 || runtime[0] != tc.reason || len(validation) != 1 || validation[0] != tc.reason {
					t.Fatalf("runtime=%v validation=%v want %s", runtime, validation, tc.reason)
				}
			})
		}
	})

	t.Run("validation failure sets receipt flag", func(t *testing.T) {
		store, item := buildStore(t)
		invalidator := &recordingInvalidator{failValidation: true}
		store.SetCacheInvalidator(invalidator)
		result, err := store.Patch(context.Background(), item.ID, PatchInput{ExpectedUpdatedAt: item.EditVersion, Status: strPtr("disabled")})
		if err != nil {
			t.Fatal(err)
		}
		if !result.APIKeyValidationCacheInvalidationFailed {
			t.Fatal("apiKeyValidationCacheInvalidationFailed not set")
		}
	})

	t.Run("display name only and no-op patches invalidate nothing", func(t *testing.T) {
		store, item := buildStore(t)
		invalidator := &recordingInvalidator{}
		store.SetCacheInvalidator(invalidator)
		first, err := store.Patch(context.Background(), item.ID, PatchInput{ExpectedUpdatedAt: item.EditVersion, DisplayName: strPtr("renamed_user")})
		if err != nil {
			t.Fatal(err)
		}
		// The second patch re-observes the new revision and changes nothing.
		if _, err := store.Patch(context.Background(), item.ID, PatchInput{ExpectedUpdatedAt: first.UpdatedAt, DisplayName: strPtr("renamed_user")}); err != nil {
			t.Fatal(err)
		}
		runtime, validation := invalidator.calls()
		if len(runtime) != 0 || len(validation) != 0 {
			t.Fatalf("runtime=%v validation=%v want empty", runtime, validation)
		}
	})
}

// TestSystemAccountValidationAlignment proves BUG-0170.6 at the HTTP
// contract level: description beyond 200 UTF-16 code units and patch
// passwords shorter than 4 characters fail with the Node 400 payload.
func TestSystemAccountValidationAlignment(t *testing.T) {
	deps, _, server := newTestEnv(t)
	sink := &captureSink{}
	deps.Sink = sink
	super := seedAccount(t, deps, "validator", "validator-password", "super_admin")
	cookie := login(t, server, "validator", "validator-password")

	description := strings.Repeat("汉", 200)
	code, payload := sendAccountJSON(t, server, "/__aisys__/api/system-accounts", map[string]any{
		"username": "bounded-user", "displayName": "bounded_user", "password": "bounded-password", "description": description,
	}, cookie)
	if code != http.StatusCreated {
		t.Fatalf("create with 200-codepoint description status=%d payload=%v", code, payload)
	}

	code, payload = sendAccountJSON(t, server, "/__aisys__/api/system-accounts", map[string]any{
		"username": "overflow-user", "displayName": "overflow_user", "password": "overflow-password", "description": strings.Repeat("汉", 201),
	}, cookie)
	if code != http.StatusBadRequest || payload["message"] != "系统账户参数无效" {
		t.Fatalf("create overflow status=%d payload=%v", code, payload)
	}

	base := func() map[string]any {
		return map[string]any{"expectedUpdatedAt": super.EditVersion}
	}
	// Parse-level rejections never reach the CAS comparison, so they are
	// asserted before the one mutation that succeeds (and revokes sessions).
	body := base()
	body["password"] = "abc"
	if code, payload = sendAccountJSON(t, server, "/__aisys__/api/system-accounts/"+super.ID, body, cookie); code != http.StatusBadRequest || payload["message"] != "系统账户参数无效" {
		t.Fatalf("patch short password status=%d payload=%v", code, payload)
	}
	body = base()
	body["password"] = strings.Repeat("码", 3)
	if code, payload = sendAccountJSON(t, server, "/__aisys__/api/system-accounts/"+super.ID, body, cookie); code != http.StatusBadRequest {
		t.Fatalf("patch 3-codepoint password status=%d payload=%v", code, payload)
	}
	body = base()
	body["description"] = strings.Repeat("汉", 201)
	if code, payload = sendAccountJSON(t, server, "/__aisys__/api/system-accounts/"+super.ID, body, cookie); code != http.StatusBadRequest || payload["message"] != "系统账户参数无效" {
		t.Fatalf("patch overflow description status=%d payload=%v", code, payload)
	}
	// The min(4) boundary passes: this mutation succeeds (and revokes the
	// session, which is why it is asserted last).
	body = base()
	body["password"] = "码码码码"
	if code, _ = sendAccountJSON(t, server, "/__aisys__/api/system-accounts/"+super.ID, body, cookie); code != http.StatusOK {
		t.Fatalf("patch 4-codepoint password status=%d", code)
	}
}

// TestSystemAccountOperationLogScopeAndViewer proves BUG-0170.4 (second
// half): create and patch logs are scoped to the TARGET account and carry the
// admin_managed_my_resource viewer.
func TestSystemAccountOperationLogScopeAndViewer(t *testing.T) {
	deps, _, server := newTestEnv(t)
	sink := &captureSink{}
	deps.Sink = sink
	super := seedAccount(t, deps, "auditor", "auditor-password", "super_admin")
	cookie := login(t, server, "auditor", "auditor-password")

	code, payload := sendAccountJSON(t, server, "/__aisys__/api/system-accounts", map[string]any{
		"username": "audited-user", "displayName": "audited_user", "password": "audited-password",
	}, cookie)
	if code != http.StatusCreated {
		t.Fatalf("create status=%d payload=%v", code, payload)
	}
	created, _ := payload["data"].(map[string]any)
	if created == nil || created["id"] == nil {
		t.Fatalf("create payload=%v", payload)
	}
	createdID := fmt.Sprint(created["id"])

	updated, err := deps.Accounts.FindByID(context.Background(), createdID)
	if err != nil || updated.ID == "" {
		t.Fatalf("find created account: %v", err)
	}
	patchBody, _ := json.Marshal(map[string]any{
		"expectedUpdatedAt": updated.UpdatedAt,
		"displayName":       "audited_user_renamed",
	})
	req, _ := http.NewRequest(http.MethodPatch, server.URL+"/__aisys__/api/system-accounts/"+createdID, strings.NewReader(string(patchBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d", resp.StatusCode)
	}

	entries := sink.snapshot()
	if len(entries) != 2 {
		t.Fatalf("operation log entries=%d want 2", len(entries))
	}
	for i, entry := range entries {
		wantScope := createdID
		if entry.OperationScopeSystemAccountID != wantScope {
			t.Fatalf("entry %d scope=%q want target %q (actor %q)", i, entry.OperationScopeSystemAccountID, wantScope, super.ID)
		}
		if len(entry.Viewers) != 1 || entry.Viewers[0].SystemAccountID != createdID || entry.Viewers[0].Reason != "admin_managed_my_resource" {
			t.Fatalf("entry %d viewers=%+v", i, entry.Viewers)
		}
	}
	if entries[0].Action != "create" || entries[1].Action != "update" {
		t.Fatalf("actions=%q/%q", entries[0].Action, entries[1].Action)
	}
}

// patchJSON posts a JSON body to the system-accounts family: POST for the
// collection path, PATCH for the member path.
func sendAccountJSON(t *testing.T, server *httptest.Server, path string, body map[string]any, cookie string) (int, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	method := http.MethodPost
	if !strings.HasSuffix(path, "/system-accounts") {
		method = http.MethodPatch
	}
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return response.StatusCode, payload
}
