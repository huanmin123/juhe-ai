package proberepo

// w7c reader-side contract completion: the authorized/instance availability
// derivation matrix, proxy profile resolution, metadata lookups, endpoint
// mode normalization, schema helpers and store construction guards.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
)

func w7cDeriveBase() deriveInput {
	return deriveInput{
		accessType: "owner", boundGroupID: "group-1", status: "active", schedulable: true,
		sourceID: "src-1", sourceStatus: "active", sourceSchedulable: true,
		credentials:   map[string]any{"api_keys": []any{"k1", "k2"}},
		apiKeyRuntime: map[string]string{},
		nowMS:         testNow.UnixMilli(),
	}
}

func TestW7CDeriveAuthorizedBranchMatrix(t *testing.T) {
	h := openTestDBPostgres(t)
	s := h.store
	cases := []struct {
		name   string
		mutate func(*deriveInput)
		want   string
	}{
		{"binding missing", func(i *deriveInput) { i.accessType = "authorized"; i.boundGroupID = "" }, "binding_missing"},
		{"authorization inactive", func(i *deriveInput) { i.accessType = "authorized"; i.authorizationStatus = "revoked" }, "authorization_unavailable"},
		{"authorization expired", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.authorizationExpiresAt = plusMillis(-1000)
		}, "authorization_expired"},
		{"authorization instant invalid", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.authorizationExpiresAt = "garbage"
		}, "authorization_unavailable"},
		{"source deleted", func(i *deriveInput) { i.accessType = "authorized"; i.authorizationStatus = "active"; i.sourceID = "" }, "source_deleted"},
		{"source expired code", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceErrorCode = "account_expired"
		}, "source_expired"},
		{"source expires past", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceExpiresAt = plusMillis(-1000)
		}, "source_expired"},
		{"source disabled", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceStatus = "disabled"
		}, "source_disabled"},
		{"source pending", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceStatus = "pending_test"
		}, "source_pending_test"},
		{"source error", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceStatus = "error"
		}, "source_error"},
		{"source rate limited", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceStatus = "rate_limited"
		}, "source_rate_limited"},
		{"source temporary", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceStatus = "temporary_unavailable"
		}, "source_temporary_unavailable"},
		{"source isolated", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceStatus = "quality_isolated"
		}, "source_quality_isolated"},
		{"source cooldown", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceCooldownUntil = plusMillis(60_000)
		}, "source_cooldown"},
		{"source unschedulable", func(i *deriveInput) {
			i.accessType = "authorized"
			i.authorizationStatus = "active"
			i.sourceSchedulable = false
		}, "source_unschedulable"},
		{"instance expired code", func(i *deriveInput) { i.lastErrorCode = "account_expired" }, "instance_expired"},
		{"instance expired instant", func(i *deriveInput) { i.expiresAt = plusMillis(-1000) }, "instance_expired"},
		{"instance disabled", func(i *deriveInput) { i.status = "disabled" }, "instance_disabled"},
		{"instance pending", func(i *deriveInput) { i.status = "pending_test" }, "instance_pending_test"},
		{"instance error", func(i *deriveInput) { i.status = "error" }, "instance_error"},
		{"instance rate limited", func(i *deriveInput) { i.status = "rate_limited" }, "instance_rate_limited"},
		{"instance temporary", func(i *deriveInput) { i.status = "temporary_unavailable" }, "instance_temporary_unavailable"},
		{"instance isolated", func(i *deriveInput) { i.status = "quality_isolated" }, "instance_quality_isolated"},
		{"instance cooldown", func(i *deriveInput) { i.cooldownUntil = plusMillis(60_000) }, "instance_cooldown"},
		{"instance unschedulable", func(i *deriveInput) { i.schedulable = false }, "instance_unschedulable"},
		{"pool unavailable", func(i *deriveInput) {
			i.apiKeyRuntime = map[string]string{
				s.FingerprintAPIKey("k1"): "rate_limited",
				s.FingerprintAPIKey("k2"): "temporary_unavailable",
			}
		}, "api_key_pool_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := w7cDeriveBase()
			tc.mutate(&input)
			available, status, hasStatus, _, _ := s.deriveEffectiveAvailability(input)
			if available || !hasStatus || status != tc.want {
				t.Fatalf("got available=%t status=%q hasStatus=%t, want %q", available, status, hasStatus, tc.want)
			}
		})
	}

	// Healthy owner account is available; the authorized access type keeps the
	// quota-branch limitation flag.
	available, status, hasStatus, limited, _ := s.deriveEffectiveAvailability(w7cDeriveBase())
	if !available || hasStatus || status != "" || limited {
		t.Fatalf("healthy owner: %t %q %t %t", available, status, hasStatus, limited)
	}
	authorized := w7cDeriveBase()
	authorized.accessType = "authorized"
	authorized.authorizationStatus = "active"
	available, status, hasStatus, limited, _ = s.deriveEffectiveAvailability(authorized)
	if !available || !limited {
		t.Fatalf("healthy authorized: %t %t (%q)", available, limited, status)
	}
}

func TestW7CTimestampAndTruthinessHelpers(t *testing.T) {
	if ok, err := isPastInstant(plusMillis(-60_000), testNow.UnixMilli()); err != nil || !ok {
		t.Fatalf("isPast: %t %v", ok, err)
	}
	if _, err := isPastInstant("garbage", 0); err == nil {
		t.Fatal("invalid instant must fail")
	}
	if ok, err := isFutureInstant(plusMillis(60_000), testNow.UnixMilli()); err != nil || !ok {
		t.Fatalf("isFuture: %t %v", ok, err)
	}
	if ms, err := instantMS("  " + nowMillisText() + " "); err != nil || ms != testNow.UnixMilli() {
		t.Fatalf("instantMS: %d %v", ms, err)
	}
	if _, err := instantMS("not-a-time"); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("instantMS error: %v", err)
	}
	if truthy(true) != true || truthy(int64(1)) != true || truthy(int(1)) != true ||
		truthy(int64(0)) != false || truthy("1") != false || truthy(nil) != false {
		t.Fatal("truthy contract")
	}
	if ternary(true, "a", "b") != "a" || ternary(false, "a", "b") != "b" {
		t.Fatal("ternary contract")
	}
	if got := mapField(map[string]any{"p": map[string]any{"a": 1}}, "p"); got == nil {
		t.Fatal("mapField must return the nested map")
	}
	if got := mapField(map[string]any{"p": "x"}, "p"); got != nil {
		t.Fatalf("mapField scalar: %v", got)
	}
	if got := mapField(nil, "p"); got != nil {
		t.Fatalf("mapField nil: %v", got)
	}
	if got := textCredential(map[string]any{"u": "x"}, "u"); got != "x" {
		t.Fatalf("textCredential: %q", got)
	}
	if got := textCredential(map[string]any{"u": 1}, "u"); got != "" {
		t.Fatalf("textCredential non-string: %q", got)
	}
	if got := textCredential(nil, "u"); got != "" {
		t.Fatalf("textCredential nil: %q", got)
	}
}

func TestW7CAllKeysUnavailableMatrix(t *testing.T) {
	h := openTestDBPostgres(t)
	s := h.store
	credentials := map[string]any{"api_keys": []any{"k1", "k2"}}
	if s.allKeysUnavailable(nil, nil) {
		t.Fatal("nil credentials cannot be unavailable")
	}
	if s.allKeysUnavailable(map[string]any{"api_key": "solo"}, nil) {
		t.Fatal("single-key accounts cannot be pool-unavailable")
	}
	if s.allKeysUnavailable(credentials, map[string]string{}) {
		t.Fatal("missing runtime rows default to active")
	}
	if s.allKeysUnavailable(credentials, map[string]string{
		s.FingerprintAPIKey("k1"): "active",
		s.FingerprintAPIKey("k2"): "rate_limited",
	}) {
		t.Fatal("one active key keeps the pool available")
	}
	if !s.allKeysUnavailable(credentials, map[string]string{
		s.FingerprintAPIKey("k1"): "rate_limited",
		s.FingerprintAPIKey("k2"): "temporary_unavailable",
	}) {
		t.Fatal("all inactive keys must block the pool")
	}
}

func TestW7CEndpointModeNormalization(t *testing.T) {
	// anthropic / gemini / oauth / default fallbacks（空连接 modes 回落协议默认）。
	anthropic := NormalizedEndpointModes("anthropic", "api_key", nil)
	if !anthropic[accountprobe.ModeMessagesJSON] || !anthropic[accountprobe.ModeMessagesSSE] || len(anthropic) != 2 {
		t.Fatalf("anthropic modes: %v", anthropic)
	}
	gemini := NormalizedEndpointModes(" Gemini ", "api_key", nil)
	if len(gemini) != 4 || !gemini[accountprobe.ModeInteractionsSSE] {
		t.Fatalf("gemini modes: %v", gemini)
	}
	oauth := NormalizedEndpointModes("openai", "oauth", nil)
	if len(oauth) != 2 || !oauth[accountprobe.ModeResponsesJSON] {
		t.Fatalf("oauth modes: %v", oauth)
	}
	def := NormalizedEndpointModes("openai", "api_key", nil)
	if len(def) != 4 || !def[accountprobe.ModeChatSSE] {
		t.Fatalf("default modes: %v", def)
	}
	// 连接声明优先（account_endpoint_modes 投影）；未知值跳过；全部无效回落
	// 协议默认（含 images 形态）。
	listed := NormalizedEndpointModes("openai", "api_key", []string{"chat_json", "images_json"})
	if len(listed) != 2 || !listed[accountprobe.ModeImagesJSON] || listed[accountprobe.ModeChatSSE] {
		t.Fatalf("listed modes: %v", listed)
	}
	invalid := NormalizedEndpointModes("openai", "api_key", []string{"nope"})
	if len(invalid) != 4 {
		t.Fatalf("invalid list fallback: %v", invalid)
	}
}

func TestW7CLoadProxyURLArms(t *testing.T) {
	h := openTestDBPostgres(t)
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
	    VALUES ('p-ok', 'sys-1', 'p', 'http', '10.0.0.5', 8080, NULL, NULL, TRUE, $1, $1)`, nowMillisText())
	url, err := h.store.loadProxyURL(context.Background(), "p-ok")
	if err != nil || url != "http://10.0.0.5:8080" {
		t.Fatalf("plain proxy: %q %v", url, err)
	}

	missing, err := h.store.loadProxyURL(context.Background(), "p-missing")
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing profile: %q %v", missing, err)
	}
	if got, err := h.store.loadProxyURL(context.Background(), "   "); err != nil || got != "" {
		t.Fatalf("blank profile: %q %v", got, err)
	}
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at) VALUES ('p-off', 'sys-1', 'p', 'http', '10.0.0.6', 8080, FALSE, $1, $1)`, nowMillisText())
	if _, err := h.store.loadProxyURL(context.Background(), "p-off"); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("disabled profile: %v", err)
	}
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at) VALUES ('p-badport', 'sys-1', 'p', 'http', '10.0.0.7', 70000, TRUE, $1, $1)`, nowMillisText())
	if _, err := h.store.loadProxyURL(context.Background(), "p-badport"); err == nil {
		t.Fatal("bad port must fail")
	}
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at) VALUES ('p-scheme', 'sys-1', 'p', 'gre', '10.0.0.8', 8080, TRUE, $1, $1)`, nowMillisText())
	if _, err := h.store.loadProxyURL(context.Background(), "p-scheme"); err == nil || !strings.Contains(err.Error(), "协议不受支持") {
		t.Fatalf("bad scheme: %v", err)
	}
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at) VALUES ('p-socks', 'sys-1', 'p', 'socks5', '10.0.0.9', 1080, TRUE, $1, $1)`, nowMillisText())
	url, err = h.store.loadProxyURL(context.Background(), "p-socks")
	if err != nil || url != "socks5h://10.0.0.9:1080" {
		t.Fatalf("socks aliasing: %q %v", url, err)
	}

	password := h.sealCredentials(t, map[string]any{"password": "secret-pass"})
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
	    VALUES ('p-auth', 'sys-1', 'p', 'https', '10.0.0.10', 8443, 'user', $1, TRUE, $2, $2)`, password, nowMillisText())
	url, err = h.store.loadProxyURL(context.Background(), "p-auth")
	if err != nil || url != "https://user:secret-pass@10.0.0.10:8443" {
		t.Fatalf("authenticated proxy: %q %v", url, err)
	}

	badPassword := h.sealCredentials(t, map[string]any{"other": 1})
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
	    VALUES ('p-badpass', 'sys-1', 'p', 'http', '10.0.0.11', 8080, 'user', $1, TRUE, $2, $2)`, badPassword, nowMillisText())
	if _, err := h.store.loadProxyURL(context.Background(), "p-badpass"); err == nil || !strings.Contains(err.Error(), "密码缺失") {
		t.Fatalf("missing password field: %v", err)
	}
	h.exec(t, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
	    VALUES ('p-garbage', 'sys-1', 'p', 'http', '10.0.0.12', 8080, 'user', 'garbage', TRUE, $1, $1)`, nowMillisText())
	if _, err := h.store.loadProxyURL(context.Background(), "p-garbage"); err == nil || !strings.Contains(err.Error(), "密码不可用") {
		t.Fatalf("undecryptable password: %v", err)
	}
}

func TestW7CLoadAccountMetadataByIds(t *testing.T) {
	h := openTestDBPostgres(t)
	credentials := h.sealCredentials(t, map[string]any{"base_url": "https://x", "api_key": "k"})
	h.exec(t, `INSERT INTO juhe_business.accounts (id, system_account_id, name, type, status, credentials_encrypted, connection_driver_code, protocol_code, protocol_version, created_at, updated_at)
	    VALUES ('m-1', 'sys-1', 'n', 'api_key', 'active', $1, 'gpt', 'openai', 'v1', $2, $2)`, credentials, nowMillisText())
	h.exec(t, `INSERT INTO juhe_business.accounts (id, system_account_id, name, type, status, credentials_encrypted, connection_driver_code, protocol_code, protocol_version, created_at, updated_at, deleted_at)
	    VALUES ('m-2', 'sys-2', 'n', 'api_key', 'active', $1, 'gpt', 'openai', 'v1', $2, $2, '2026-01-01')`, credentials, nowMillisText())

	metadata, err := h.store.LoadAccountMetadataByIds(context.Background(), []string{"m-1", "m-1", "", "m-2", "m-3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 {
		t.Fatalf("deleted/missing accounts must be skipped: %+v", metadata)
	}
	if metadata["m-1"].SystemAccountID != "sys-1" || metadata["m-1"].ProviderCode != "" {
		t.Fatalf("metadata（vendor_code 缺省投影空串）: %+v", metadata["m-1"])
	}
	if empty, err := h.store.LoadAccountMetadataByIds(context.Background(), nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty ids: %v %v", empty, err)
	}
}

// TestW7CAuthorizedInstanceGroupPath 验证授权实例的分组访问链（W27③ 修复后
// 的 PG 生产行为）：账户授权与分组授权的 expires_at 均为 text 列，过期必须
// 拒绝、未过期与 NULL 放行（text 列 time.Time 参数字典序比较恒真导致过期
// 仍放行的越权面已修复）。
func TestW7CAuthorizedInstanceGroupPath(t *testing.T) {
	h := openTestDBPostgres(t)
	credentials := h.sealCredentials(t, map[string]any{
		"api_keys": []any{"sk-a", "sk-b"},
	})
	// 授权实例行不再冗余身份/连接事实（vendor_code/健康检查列物理为 NULL），
	// 资源事实全部归来源账户。
	h.exec(t, `INSERT INTO juhe_business.accounts (id, system_account_id, name, type, status, credentials_encrypted, connection_driver_code, protocol_code, protocol_version, authorization_instance_authorization_id, authorization_instance_source_account_id, created_at, updated_at)
	    VALUES ('inst-1', 'sys-grantee', '实例', 'api_key', 'active', $1, 'gpt', 'openai', 'v1', 'authz-1', 'src-1', $2, $2)`,
		credentials, nowMillisText())
	// Source account backing the authorization instance.
	h.exec(t, `INSERT INTO juhe_business.accounts (id, system_account_id, name, type, status, credentials_encrypted, vendor_code, oauth_type, connection_driver_code, protocol_code, protocol_version, created_at, updated_at)
	    VALUES ('src-1', 'sys-owner', '源账户', 'oauth', 'active', $1, 'gpt', NULL, 'gpt', 'openai', 'v1', $2, $2)`,
		h.sealCredentials(t, map[string]any{"access_token": "tok-1"}), nowMillisText())
	h.seedModel(t, "src-1", "gpt-test")
	h.seedProbeConnection(t, "src-1", "gpt_openai_v1", "https://authorized.example.com", "chat_json", "chat_sse")
	h.exec(t, `INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, resource_owner_system_account_id, status, created_by, created_at, updated_at)
	    VALUES ('authz-1', 'account', 'src-1', 'sys-grantee', 'sys-owner', 'active', 'sys-owner', $1, $1)`, nowMillisText())
	// Group access for a grantee requires its own active group authorization.
	h.exec(t, `INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, resource_owner_system_account_id, status, created_by, created_at, updated_at)
	    VALUES ('authz-grp', 'group', 'group-1', 'sys-grantee', 'sys-owner', 'active', 'sys-owner', $1, $1)`, nowMillisText())
	h.exec(t, `INSERT INTO juhe_business.groups (id, system_account_id, name, enabled, created_at, updated_at) VALUES ('group-1', 'sys-owner', 'g', 1, $1, $1)`, nowMillisText())
	h.exec(t, `INSERT INTO juhe_business.group_accounts (group_id, system_account_id, account_id, enabled, created_at, updated_at) VALUES ('group-1', 'sys-owner', 'inst-1', 1, $1, $1)`, nowMillisText())

	load := func(t *testing.T) *CandidateAccount {
		t.Helper()
		candidate, err := h.store.LoadAccountForGroup(context.Background(), "group-1", "inst-1", "sys-grantee")
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}

	// expires_at NULL → 放行，且资源事实投影来自来源账户。
	candidate := load(t)
	if candidate == nil {
		t.Fatal("authorized candidate must resolve")
	}
	if candidate.AccountOwnerSystemAccountID != "sys-owner" {
		t.Fatalf("access owner: %q", candidate.AccountOwnerSystemAccountID)
	}
	if candidate.CredentialSourceAccountID != "src-1" || candidate.Type != "oauth" || candidate.SelectedAPIKey != "tok-1" {
		t.Fatalf("source fallback: %+v", candidate.OpenAIAccountCandidate)
	}
	if candidate.ProviderCode != "gpt" || candidate.BaseURL != "https://authorized.example.com" {
		t.Fatalf("resource facts: vendor=%q base=%q", candidate.ProviderCode, candidate.BaseURL)
	}

	// 账户授权未过期 → 放行。
	h.exec(t, `UPDATE juhe_business.resource_authorizations SET expires_at = $1 WHERE id = 'authz-1'`, plusMillis(60_000))
	if candidate = load(t); candidate == nil {
		t.Fatal("未过期授权必须放行")
	}

	// 账户授权已过期 → 拒绝（activeAuthorizationOwnerByID，W27③ 核心断言）。
	h.exec(t, `UPDATE juhe_business.resource_authorizations SET expires_at = $1 WHERE id = 'authz-1'`, plusMillis(-1000))
	if candidate = load(t); candidate != nil {
		t.Fatalf("过期授权必须拒绝: %+v", candidate)
	}
	h.exec(t, `UPDATE juhe_business.resource_authorizations SET expires_at = NULL WHERE id = 'authz-1'`)

	// 分组授权已过期 → 拒绝（activeGroupAuthorizationOwner，同一缺陷面）。
	h.exec(t, `UPDATE juhe_business.resource_authorizations SET expires_at = $1 WHERE id = 'authz-grp'`, plusMillis(-1000))
	if candidate = load(t); candidate != nil {
		t.Fatalf("过期分组授权必须拒绝: %+v", candidate)
	}
	h.exec(t, `UPDATE juhe_business.resource_authorizations SET expires_at = NULL WHERE id = 'authz-grp'`)

	// oauth with only a refresh token still selects a key.
	h.exec(t, `UPDATE juhe_business.accounts SET credentials_encrypted = $1 WHERE id = 'src-1'`,
		h.sealCredentials(t, map[string]any{"refresh_token": "refresh-1"}))
	if _, err := h.store.LoadAccountForGroupFull(context.Background(), "inst-1"); err != nil {
		t.Fatalf("full reload: %v", err)
	}
}
func TestW7CSchemaAndStoreGuards(t *testing.T) {
	h := openTestDBPostgres(t)
	ctx := context.Background()
	// EnsureSchema must stay idempotent over the fixture schema prepared by
	// the pgtestkit prepare hook.
	if err := h.store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := h.store.ValidateCoreTables(ctx); err != nil {
		t.Fatalf("core tables present: %v", err)
	}
	// A database without the core business tables must fail validation.
	bare := openBareTestDBPostgres(t)
	if err := bare.store.ValidateCoreTables(ctx); err == nil || !strings.Contains(err.Error(), "accounts") {
		t.Fatalf("missing core tables: %v", err)
	}
	if _, err := NewStore(Config{}); err == nil || !strings.Contains(err.Error(), "缺少业务库句柄") {
		t.Fatalf("nil db: %v", err)
	}
	if _, err := NewStore(Config{DB: h.db}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("missing secret: %v", err)
	}
	if _, err := NewStore(Config{DB: h.db, Secret: "s"}); err != nil {
		t.Fatalf("default now: %v", err)
	}
	pg := &Store{db: h.db, secret: "s", now: func() time.Time { return testNow }}
	if pg.table("accounts") != "juhe_business.accounts" {
		t.Fatalf("pg table: %q", pg.table("accounts"))
	}
	// timeParam/instantParam 契约（W27 修复）：text 时间列只绑 RFC3339 文本，
	// 不绑原生 time.Time（pgx 对 text 列回退 Go time.String() 形态致比较失真）。
	if text, ok := pg.timeParam(testNow).(string); !ok || text != nowMillisText() {
		t.Fatalf("pg timeParam must bind RFC3339 milli text: %#v", text)
	}
	if text, ok := pg.instantParam(nowMillisText()).(string); !ok || text != nowMillisText() {
		t.Fatalf("pg instantParam must pass RFC3339 text through: %#v", text)
	}
	if text, ok := pg.instantParam("garbage").(string); !ok || text != "garbage" {
		t.Fatalf("pg instantParam fallback: %#v", text)
	}
	if errInvalid("x").Error() != "x" {
		t.Fatal("errInvalid contract")
	}
}

func TestW7CAssembleProbeViewArms(t *testing.T) {
	if AssembleProbeView(nil, nil) != nil {
		t.Fatal("nil inputs must assemble to nil")
	}
	h := openTestDBPostgres(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	account, err := h.store.LoadAccountForTest(context.Background(), "acc-1")
	if err != nil || account == nil {
		t.Fatalf("account view: %v %v", account, err)
	}
	candidate, err := h.store.LoadAccountForGroup(context.Background(), "group-1", "acc-1", "sys-1")
	if err != nil || candidate == nil {
		t.Fatalf("candidate: %v %v", candidate, err)
	}
	view := AssembleProbeView(account, candidate)
	if view == nil || view.AccountID != "acc-1" || len(view.APIKeyEntries) != 2 {
		t.Fatalf("assembled view: %+v", view)
	}
	if view.SelectedAPIKey != key1 {
		t.Fatalf("selected key: %q", view.SelectedAPIKey)
	}
	// FindAccountForTest wraps the summary projection.
	summary, err := h.store.FindAccountForTest(context.Background(), "acc-1")
	if err != nil || summary == nil || summary.ID != "acc-1" {
		t.Fatalf("find account: %v %v", summary, err)
	}
	if missing, err := h.store.FindAccountForTest(context.Background(), "w7c-missing"); err != nil || missing != nil {
		t.Fatalf("missing account: %v %v", missing, err)
	}
	if empty, err := h.store.LoadAccountForTest(context.Background(), "  "); err != nil || empty != nil {
		t.Fatalf("blank id: %v %v", empty, err)
	}
	// HasAPIKeyEntry with a nil candidate short-circuits.
	if has, err := h.store.HasAPIKeyEntry(context.Background(), nil, "f", "k"); err != nil || has {
		t.Fatalf("nil candidate: %v %v", has, err)
	}
	if _, err := h.store.LoadAccountForGroupFull(context.Background(), "w7c-missing"); err != nil {
		t.Fatalf("missing full reload: %v", err)
	}
	// resolveGroupAccess rejects disabled or broken groups.
	if _, ok, err := h.store.resolveGroupAccess(context.Background(), "w7c-missing", "sys-1"); err != nil || ok {
		t.Fatalf("missing group: %v %v", ok, err)
	}
	h.exec(t, `UPDATE juhe_business.groups SET enabled = 0 WHERE id = 'group-1'`)
	if _, ok, err := h.store.resolveGroupAccess(context.Background(), "group-1", "sys-1"); err != nil || ok {
		t.Fatalf("disabled group: %v %v", ok, err)
	}
}
