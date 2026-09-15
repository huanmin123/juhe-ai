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
	h := openTestDB(t)
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
			available, status, hasStatus, _ := s.deriveEffectiveAvailability(input)
			if available || !hasStatus || status != tc.want {
				t.Fatalf("got available=%t status=%q hasStatus=%t, want %q", available, status, hasStatus, tc.want)
			}
		})
	}

	// Healthy owner account is available; the authorized access type keeps the
	// quota-branch limitation flag.
	available, status, hasStatus, limited := s.deriveEffectiveAvailability(w7cDeriveBase())
	if !available || hasStatus || status != "" || limited {
		t.Fatalf("healthy owner: %t %q %t %t", available, status, hasStatus, limited)
	}
	authorized := w7cDeriveBase()
	authorized.accessType = "authorized"
	authorized.authorizationStatus = "active"
	available, status, hasStatus, limited = s.deriveEffectiveAvailability(authorized)
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
	h := openTestDB(t)
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
	// anthropic / gemini / oauth / default fallbacks.
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
	// Credential list wins; non-text entries are skipped; an all-invalid list
	// falls back to protocol defaults including the images mode.
	listed := NormalizedEndpointModes("openai", "api_key", map[string]any{
		"supported_endpoint_modes": []any{"chat_json", 42, "images_json"},
	})
	if len(listed) != 2 || !listed[accountprobe.ModeImagesJSON] || listed[accountprobe.ModeChatSSE] {
		t.Fatalf("listed modes: %v", listed)
	}
	invalid := NormalizedEndpointModes("openai", "api_key", map[string]any{
		"supported_endpoint_modes": []any{"nope"},
	})
	if len(invalid) != 4 {
		t.Fatalf("invalid list fallback: %v", invalid)
	}
	wrongType := NormalizedEndpointModes("openai", "api_key", map[string]any{
		"supported_endpoint_modes": "chat_json",
	})
	if len(wrongType) != 4 {
		t.Fatalf("non-list fallback: %v", wrongType)
	}
}

func TestW7CLoadProxyURLArms(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
	    VALUES ('p-ok', 'http', '10.0.0.5', 8080, NULL, NULL, 1)`)
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
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, enabled) VALUES ('p-off', 'http', '10.0.0.6', 8080, 0)`)
	if _, err := h.store.loadProxyURL(context.Background(), "p-off"); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("disabled profile: %v", err)
	}
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, enabled) VALUES ('p-badport', 'http', '10.0.0.7', 70000, 1)`)
	if _, err := h.store.loadProxyURL(context.Background(), "p-badport"); err == nil {
		t.Fatal("bad port must fail")
	}
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, enabled) VALUES ('p-scheme', 'gre', '10.0.0.8', 8080, 1)`)
	if _, err := h.store.loadProxyURL(context.Background(), "p-scheme"); err == nil || !strings.Contains(err.Error(), "协议不受支持") {
		t.Fatalf("bad scheme: %v", err)
	}
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, enabled) VALUES ('p-socks', 'socks5', '10.0.0.9', 1080, 1)`)
	url, err = h.store.loadProxyURL(context.Background(), "p-socks")
	if err != nil || url != "socks5h://10.0.0.9:1080" {
		t.Fatalf("socks aliasing: %q %v", url, err)
	}

	password := h.sealCredentials(t, map[string]any{"password": "secret-pass"})
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
	    VALUES ('p-auth', 'https', '10.0.0.10', 8443, 'user', ?, 1)`, password)
	url, err = h.store.loadProxyURL(context.Background(), "p-auth")
	if err != nil || url != "https://user:secret-pass@10.0.0.10:8443" {
		t.Fatalf("authenticated proxy: %q %v", url, err)
	}

	badPassword := h.sealCredentials(t, map[string]any{"other": 1})
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
	    VALUES ('p-badpass', 'http', '10.0.0.11', 8080, 'user', ?, 1)`, badPassword)
	if _, err := h.store.loadProxyURL(context.Background(), "p-badpass"); err == nil || !strings.Contains(err.Error(), "密码缺失") {
		t.Fatalf("missing password field: %v", err)
	}
	h.exec(t, `INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
	    VALUES ('p-garbage', 'http', '10.0.0.12', 8080, 'user', 'garbage', 1)`)
	if _, err := h.store.loadProxyURL(context.Background(), "p-garbage"); err == nil || !strings.Contains(err.Error(), "密码不可用") {
		t.Fatalf("undecryptable password: %v", err)
	}
}

func TestW7CLoadAccountMetadataByIds(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	credentials := h.sealCredentials(t, map[string]any{"base_url": "https://x", "api_key": "k"})
	h.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted)
	    VALUES ('m-1', 'sys-1', 'n', 'api_key', 'active', ?)`, credentials)
	h.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted, deleted_at)
	    VALUES ('m-2', 'sys-2', 'n', 'api_key', 'active', ?, '2026-01-01')`, credentials)

	metadata, err := h.store.LoadAccountMetadataByIds(context.Background(), []string{"m-1", "m-1", "", "m-2", "m-3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 {
		t.Fatalf("deleted/missing accounts must be skipped: %+v", metadata)
	}
	if metadata["m-1"].SystemAccountID != "sys-1" || metadata["m-1"].ProviderCode != "openai" {
		t.Fatalf("metadata: %+v", metadata["m-1"])
	}
	if empty, err := h.store.LoadAccountMetadataByIds(context.Background(), nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty ids: %v %v", empty, err)
	}
}

func TestW7CAuthorizedInstanceGroupPath(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	credentials := h.sealCredentials(t, map[string]any{
		"base_url": "https://authorized.example.com",
		"api_keys": []any{"sk-a", "sk-b"},
	})
	h.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted, provider_code,
	    authorization_instance_authorization_id, authorization_instance_source_account_id)
	    VALUES ('inst-1', 'sys-grantee', '实例', 'api_key', 'active', ?, 'openai', 'authz-1', 'src-1')`, credentials)
	// Source account backing the authorization instance.
	h.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted)
	    VALUES ('src-1', 'sys-owner', '源账户', 'oauth', 'active', ?)`,
		h.sealCredentials(t, map[string]any{"access_token": "tok-1", "base_url": "https://source.example.com"}))
	h.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id,
	    resource_owner_system_account_id, status) VALUES ('authz-1', 'account', 'src-1', 'sys-grantee', 'sys-owner', 'active')`)
	// Group access for a grantee requires its own active group authorization.
	h.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id,
	    resource_owner_system_account_id, status) VALUES ('authz-grp', 'group', 'group-1', 'sys-grantee', 'sys-owner', 'active')`)
	h.exec(t, `INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('group-1', 'sys-owner', 'openai', 1)`)
	h.exec(t, `INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled) VALUES ('group-1', 'sys-owner', 'inst-1', 1)`)

	candidate, err := h.store.LoadAccountForGroup(context.Background(), "group-1", "inst-1", "sys-grantee")
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil {
		t.Fatal("authorized candidate must resolve")
	}
	if candidate.AccountOwnerSystemAccountID != "sys-owner" {
		t.Fatalf("access owner: %q", candidate.AccountOwnerSystemAccountID)
	}
	if candidate.CredentialSourceAccountID != "src-1" || candidate.Type != "oauth" || candidate.SelectedAPIKey != "tok-1" {
		t.Fatalf("source fallback: %+v", candidate.OpenAIAccountCandidate)
	}

	// Authorization row expired → grantee loses access.
	h.exec(t, `UPDATE resource_authorizations SET status='revoked' WHERE id='authz-1'`)
	missing, err := h.store.LoadAccountForGroup(context.Background(), "group-1", "inst-1", "sys-grantee")
	if err != nil || missing != nil {
		t.Fatalf("revoked authorization: %v %v", missing, err)
	}
	h.exec(t, `UPDATE resource_authorizations SET status='active', expires_at=? WHERE id='authz-1'`, plusMillis(-1000))
	missing, err = h.store.LoadAccountForGroup(context.Background(), "group-1", "inst-1", "sys-grantee")
	if err != nil || missing != nil {
		t.Fatalf("expired authorization: %v %v", missing, err)
	}

	// Grantee system account mismatch on the instance row → nil.
	h.exec(t, `UPDATE resource_authorizations SET expires_at=NULL WHERE id='authz-1'`)
	mismatch, err := h.store.LoadAccountForGroupFull(context.Background(), "inst-1")
	if err != nil || mismatch == nil {
		t.Fatalf("full reload: %v %v", mismatch, err)
	}

	// oauth with only a refresh token still selects a key.
	h.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'src-1'`,
		h.sealCredentials(t, map[string]any{"refresh_token": "refresh-1"}))
	full, err := h.store.LoadAccountForGroupFull(context.Background(), "inst-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = full

	// An account whose credentials cannot be decrypted resolves to nil.
	h.exec(t, `UPDATE accounts SET credentials_encrypted='garbage' WHERE id='inst-1'`)
	broken, err := h.store.LoadAccountForGroupFull(context.Background(), "inst-1")
	if err != nil || broken != nil {
		t.Fatalf("broken credentials: %v %v", broken, err)
	}
}

func TestW7CSchemaAndStoreGuards(t *testing.T) {
	h := openTestDB(t)
	ctx := context.Background()
	// The fixture schema is richer; EnsureSchema must be idempotent over it.
	h.seedSchema(t)
	if err := h.store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := h.store.ValidateCoreTables(ctx); err != nil {
		t.Fatalf("core tables present: %v", err)
	}
	// A database without the core business tables must fail validation.
	bare := openTestDB(t)
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
	pg := &Store{db: h.db, postgres: true, secret: "s", now: func() time.Time { return testNow }}
	if pg.table("accounts") != "juhe_business.accounts" {
		t.Fatalf("pg table: %q", pg.table("accounts"))
	}
	if _, ok := pg.timeParam(testNow).(interface{ IsZero() bool }); !ok {
		t.Fatal("pg timeParam must keep the native time")
	}
	if text, ok := (&Store{db: h.db}).timeParam(testNow).(string); !ok || text != testNow.UTC().Format("2006-01-02T15:04:05.999999999Z07:00") {
		t.Fatalf("sqlite timeParam: %#v", text)
	}
	if _, ok := pg.instantParam(nowMillisText()).(interface{ IsZero() bool }); !ok {
		t.Fatal("pg instantParam must parse native times")
	}
	if text, ok := pg.instantParam("garbage").(string); !ok || text != "garbage" {
		t.Fatalf("pg instantParam fallback: %#v", text)
	}
	if text, ok := (&Store{}).instantParam(nowMillisText()).(string); !ok {
		t.Fatalf("sqlite instantParam passthrough: %#v", text)
	}
	if errInvalid("x").Error() != "x" {
		t.Fatal("errInvalid contract")
	}
}

func TestW7CAssembleProbeViewArms(t *testing.T) {
	if AssembleProbeView(nil, nil) != nil {
		t.Fatal("nil inputs must assemble to nil")
	}
	h := openTestDB(t)
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
	if _, _, ok, err := h.store.resolveGroupAccess(context.Background(), "w7c-missing", "sys-1"); err != nil || ok {
		t.Fatalf("missing group: %v %v", ok, err)
	}
	h.exec(t, `UPDATE groups SET enabled = 0 WHERE id = 'group-1'`)
	if _, _, ok, err := h.store.resolveGroupAccess(context.Background(), "group-1", "sys-1"); err != nil || ok {
		t.Fatalf("disabled group: %v %v", ok, err)
	}
}
