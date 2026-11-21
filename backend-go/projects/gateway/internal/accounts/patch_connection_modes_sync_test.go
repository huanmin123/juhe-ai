package accounts

// connections-only PATCH 的凭据影子副本同步回归（2026-11-21）：
//   - 连接集合整替换后 credentials.supported_endpoint_modes 必须收敛到最终
//     enabled 连接 modes（历史漂移自愈），且集合相同（忽略顺序）时幂等不重封；
//   - 显式 credentials 提交优先，不被同步覆盖；
//   - google_oauth 的 token/client_secret 与 oauth_type 派生列在
//     connections-only PATCH 后保持不变；
//   - 连接变化（含 connections-only base_url 变化）按既有语义进入 pending_test
//     并清健康投影；网关运行时缓存失效集合包含 connections。

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// connectionsPayloadForTest 构造单连接 connections 载荷（可改 baseUrl/modes）。
func connectionsPayloadForTest(baseURL string, modes []string) string {
	return connectionsPayloadForPreset("gpt_openai_v1", baseURL, modes)
}

func connectionsPayloadForPreset(preset, baseURL string, modes []string) string {
	quoted := make([]string, 0, len(modes))
	for _, mode := range modes {
		quoted = append(quoted, `"`+mode+`"`)
	}
	return `[{"connectionPresetCode":"` + preset + `","baseUrl":"` + baseURL +
		`","endpointModes":[` + strings.Join(quoted, ",") + `],"enabled":true}]`
}

// accountConnectionModesForTest 读账户连接行 modes（单连接夹具）。
func accountConnectionModesForTest(t *testing.T, env *testEnv, accountID string) []string {
	t.Helper()
	rows, err := env.db.Query(env.bindFixture(`SELECT account_endpoint_modes.endpoint_mode
		FROM account_endpoint_modes
		INNER JOIN account_connections ON account_connections.id = account_endpoint_modes.connection_id
		WHERE account_connections.account_id = ?
		ORDER BY account_endpoint_modes.endpoint_mode`), accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	modes := []string{}
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			t.Fatal(err)
		}
		modes = append(modes, mode)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return modes
}

// setCredentialEndpointModesForTest 直改凭据封套里的 supported_endpoint_modes，
// 模拟历史漂移行（绕过写校验）。
func setCredentialEndpointModesForTest(t *testing.T, env *testEnv, accountID string, modes []string) {
	t.Helper()
	credentials := Credentials{}
	if err := DecryptJSON(testSecret, env.queryCell(t,
		`SELECT credentials_encrypted FROM accounts WHERE id = ?`, accountID), &credentials); err != nil {
		t.Fatal(err)
	}
	credentials["supported_endpoint_modes"] = anyStrings(modes)
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`, sealed, accountID)
}

func assertCredentialAndConnectionModes(t *testing.T, env *testEnv, accountID string, want []string) {
	t.Helper()
	if got := m2AccountModes(t, env, accountID); !unorderedStringListEqual(got, want) {
		t.Fatalf("凭据 supported_endpoint_modes = %v, want %v", got, want)
	}
	if got := accountConnectionModesForTest(t, env, accountID); !unorderedStringListEqual(got, want) {
		t.Fatalf("连接 endpoint modes = %v, want %v", got, want)
	}
}

// TestPatchConnectionsOnlySyncsMediaCredentialModes 覆盖视频/音频/图片媒体账户：
// 仅提交 connections 后连接 modes 与凭据 modes 一致、test-options 判连接测试、
// changedFields 同时披露 connections/credentials、revision +1。
func TestPatchConnectionsOnlySyncsMediaCredentialModes(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	cases := []struct {
		name       string
		domain     string
		initial    []string
		patched    []string
		driftSet   []string
		patchedURL string
	}{
		{
			name: "video", domain: "video",
			initial: []string{"video_create", "video_get", "video_content", "video_cancel"},
			patched: []string{"video_create", "video_get"},
		},
		{
			name: "audio", domain: "audio",
			initial: []string{"audio_speech", "audio_transcription_json"},
			patched: []string{"audio_speech"},
		},
		{
			// image 域 lane 只有 images_json：连接侧以 base_url 变化制造连接变更，
			// 凭据侧先制造历史漂移，再验证同集合同步自愈。
			name: "image", domain: "image",
			initial:    []string{"images_json"},
			patched:    []string{"images_json"},
			driftSet:   []string{"chat_json"},
			patchedURL: "https://api.openai.com/v2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
				m2CreateBody("media-sync-"+tc.name, "gpt", "prof-gpt", "api_key", tc.domain,
					map[string]any{"api_key": "sk-media-sync-" + tc.name, "base_url": "https://api.openai.com/v1"},
					tc.initial))
			if code != http.StatusCreated {
				t.Fatalf("创建媒体账户应 201：%d %v", code, payload)
			}
			id := dataMap(t, payload)["id"].(string)
			if tc.driftSet != nil {
				setCredentialEndpointModesForTest(t, env, id, tc.driftSet)
			}
			revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id)
			revisionNumber, err := strconv.Atoi(revision)
			if err != nil {
				t.Fatal(err)
			}

			patchedURL := tc.patchedURL
			if patchedURL == "" {
				patchedURL = "https://api.openai.com/v1"
			}
			code, payload = w2PatchBody(t, env, id,
				`"connections":`+connectionsPayloadForTest(patchedURL, tc.patched))
			if code != http.StatusOK {
				t.Fatalf("connections-only PATCH 应 200：%d %v", code, payload)
			}
			fields := dataMap(t, payload)["changedFields"]
			if !containsChange(fields, "connections") || !containsChange(fields, "credentials") {
				t.Fatalf("changedFields 应同时含 connections/credentials：%v", fields)
			}
			if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id); got != itoa64(int64(revisionNumber+1)) {
				t.Fatalf("revision 应 +1：%s -> %s", revision, got)
			}
			assertCredentialAndConnectionModes(t, env, id, tc.patched)

			code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"/test-options", "")
			if code != http.StatusOK {
				opts, optErr := env.store.FindManualTestOptionsContext(context.Background(), id,
					&AccessScope{ViewerID: adminID, IsAdmin: true})
				t.Fatalf("test-options 应 200：%d %v（直接调用 err=%v opts=%+v）", code, payload, optErr, opts)
			}
			if kind, _ := payload["testKind"].(string); kind != "connection" {
				t.Fatalf("媒体账户 test-options 应判连接测试：%v", payload)
			}
		})
	}
}

// TestPatchConnectionsOnlyHealsCredentialModeDriftIdempotently 覆盖历史漂移自愈
// 与幂等：手工改旧 modes 后提交相同 connections 仍同步并 +1；第二次相同请求
// 无变更、revision 不变、密文不重封。
func TestPatchConnectionsOnlyHealsCredentialModeDriftIdempotently(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	modes := []string{"video_create", "video_get", "video_content", "video_cancel"}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("drift-sync-video", "gpt", "prof-gpt", "api_key", "video",
			map[string]any{"api_key": "sk-drift-sync", "base_url": "https://api.openai.com/v1"}, modes))
	if code != http.StatusCreated {
		t.Fatalf("创建视频账户应 201：%d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	setCredentialEndpointModesForTest(t, env, id, []string{"video_get"})
	sealedBefore := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id)

	code, payload = w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v1", modes))
	if code != http.StatusOK {
		t.Fatalf("漂移自愈 PATCH 应 200：%d %v", code, payload)
	}
	if !containsChange(dataMap(t, payload)["changedFields"], "credentials") {
		t.Fatalf("漂移自愈应披露 credentials 变更：%v", dataMap(t, payload)["changedFields"])
	}
	if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id); got != "2" {
		t.Fatalf("漂移自愈后 revision 应为 2：%s", got)
	}
	assertCredentialAndConnectionModes(t, env, id, modes)
	sealedAfter := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id)
	if sealedAfter == sealedBefore {
		t.Fatal("漂移自愈应重封凭据密文")
	}

	// 幂等：第二次相同请求无变更、revision 不变、密文不重封。
	code, payload = w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v1", modes))
	if code != http.StatusOK {
		t.Fatalf("幂等 PATCH 应 200：%d %v", code, payload)
	}
	if fields := dataMap(t, payload)["changedFields"].([]any); len(fields) != 0 {
		t.Fatalf("幂等 PATCH 不应产生变更：%v", fields)
	}
	if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id); got != "2" {
		t.Fatalf("幂等 PATCH 不应递增 revision：%s", got)
	}
	if got := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id); got != sealedAfter {
		t.Fatal("幂等 PATCH 不应重封凭据密文")
	}
}

// TestPatchConnectionsOnlyDoesNotOverrideExplicitCredentials 锁定显式凭据优先：
// 同一请求同时提交 connections 与 credentials 时不同步覆盖凭据 modes。
func TestPatchConnectionsOnlyDoesNotOverrideExplicitCredentials(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	initial := []string{"video_create", "video_get", "video_content", "video_cancel"}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("explicit-priority-video", "gpt", "prof-gpt", "api_key", "video",
			map[string]any{"api_key": "sk-explicit-priority", "base_url": "https://api.openai.com/v1"}, initial))
	if code != http.StatusCreated {
		t.Fatalf("创建视频账户应 201：%d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, payload = w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v1", []string{"video_create", "video_get"})+
			`,"credentials":{"supported_endpoint_modes":["video_get"]}`)
	if code != http.StatusOK {
		t.Fatalf("显式凭据 + connections PATCH 应 200：%d %v", code, payload)
	}
	fields := dataMap(t, payload)["changedFields"]
	if !containsChange(fields, "connections") || !containsChange(fields, "credentials") {
		t.Fatalf("changedFields 应含 connections/credentials：%v", fields)
	}
	if got := m2AccountModes(t, env, id); !unorderedStringListEqual(got, []string{"video_get"}) {
		t.Fatalf("显式凭据 modes 不得被连接集合同步覆盖：%v", got)
	}
	if got := accountConnectionModesForTest(t, env, id); !unorderedStringListEqual(got, []string{"video_create", "video_get"}) {
		t.Fatalf("连接 modes 应按请求落库：%v", got)
	}
}

// TestPatchConnectionsOnlyPreservesGoogleOAuthDerivedFields 锁定 google_oauth：
// connections-only PATCH 重封凭据影子副本时 token/client_secret 与
// oauth_type/到期时间/refresh 标记三个派生列保持不变。
func TestPatchConnectionsOnlyPreservesGoogleOAuthDerivedFields(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	m2SeedProvider(t, env, adminID, "gemini", "profile_gemini_native_v1beta", "gemini", "v1beta",
		[]string{"api_key", "google_oauth"})

	credentials := map[string]any{
		"access_token":  "at-oauth-sync",
		"refresh_token": "rt-oauth-sync",
		"client_id":     geminiCLIOAuthClientID,
		"client_secret": "cs-oauth-sync",
		"oauth_type":    "code_assist",
		"expires_at":    "2030-01-01T00:00:00Z",
		"base_url":      "https://generativelanguage.googleapis.com",
	}
	modes := []string{"video_create", "video_get", "video_content", "video_cancel"}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("oauth-connections-sync", "gemini", "profile_gemini_native_v1beta", "google_oauth",
			"video", credentials, modes))
	if code != http.StatusCreated {
		t.Fatalf("创建 google_oauth 视频账户应 201：%d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	// 创建链只在 gpt oauth 派生到期/refresh 列；google_oauth 由轮换/迁移写入。
	// 显式落三个派生列，锁定 connections-only 重封不改写它们。
	env.exec(t, `UPDATE accounts SET oauth_type = 'code_assist',
		oauth_access_token_expires_at = '2030-01-01T00:00:00Z',
		oauth_refresh_token_present = 1 WHERE id = ?`, id)

	before := struct {
		oauthType      string
		expiresAt      string
		refreshPresent string
		accessToken    string
		clientSecret   string
	}{
		oauthType:      env.queryCell(t, `SELECT COALESCE(oauth_type,'') FROM accounts WHERE id = ?`, id),
		expiresAt:      env.queryCell(t, `SELECT COALESCE(oauth_access_token_expires_at,'') FROM accounts WHERE id = ?`, id),
		refreshPresent: env.queryCell(t, `SELECT oauth_refresh_token_present FROM accounts WHERE id = ?`, id),
	}
	stored := Credentials{}
	if err := DecryptJSON(testSecret, env.queryCell(t,
		`SELECT credentials_encrypted FROM accounts WHERE id = ?`, id), &stored); err != nil {
		t.Fatal(err)
	}
	before.accessToken, _ = stored["access_token"].(string)
	before.clientSecret, _ = stored["client_secret"].(string)

	nextModes := []string{"video_create", "video_get"}
	code, payload = w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForPreset("gemini_native_v1beta",
			"https://generativelanguage.googleapis.com", nextModes))
	if code != http.StatusOK {
		t.Fatalf("google_oauth connections-only PATCH 应 200：%d %v", code, payload)
	}
	assertCredentialAndConnectionModes(t, env, id, nextModes)

	after := struct {
		oauthType      string
		expiresAt      string
		refreshPresent string
		accessToken    string
		clientSecret   string
	}{
		oauthType:      env.queryCell(t, `SELECT COALESCE(oauth_type,'') FROM accounts WHERE id = ?`, id),
		expiresAt:      env.queryCell(t, `SELECT COALESCE(oauth_access_token_expires_at,'') FROM accounts WHERE id = ?`, id),
		refreshPresent: env.queryCell(t, `SELECT oauth_refresh_token_present FROM accounts WHERE id = ?`, id),
	}
	patched := Credentials{}
	if err := DecryptJSON(testSecret, env.queryCell(t,
		`SELECT credentials_encrypted FROM accounts WHERE id = ?`, id), &patched); err != nil {
		t.Fatal(err)
	}
	after.accessToken, _ = patched["access_token"].(string)
	after.clientSecret, _ = patched["client_secret"].(string)
	if before.oauthType != "code_assist" || after.oauthType != before.oauthType {
		t.Fatalf("oauth_type 派生列应保持 code_assist：%v -> %v", before.oauthType, after.oauthType)
	}
	if before.expiresAt == "" || after.expiresAt != before.expiresAt {
		t.Fatalf("oauth_access_token_expires_at 应保持不变：%q -> %q", before.expiresAt, after.expiresAt)
	}
	if before.refreshPresent != "1" || after.refreshPresent != before.refreshPresent {
		t.Fatalf("oauth_refresh_token_present 应保持 1：%v -> %v", before.refreshPresent, after.refreshPresent)
	}
	if after.accessToken != "at-oauth-sync" || after.clientSecret != "cs-oauth-sync" {
		t.Fatalf("token/client_secret 应保持不变：access=%q client_secret=%q", after.accessToken, after.clientSecret)
	}
	if _, exists := patched["oauth_type"]; !exists || patched["oauth_type"] != "code_assist" {
		t.Fatalf("凭据封套 oauth_type 应保持：%v", patched["oauth_type"])
	}
}

// TestPatchConnectionsOnAuthorizedInstanceStillRejected 锁定授权实例只读：
// 提交 connections 仍 400 且账户/连接行不变。
func TestPatchConnectionsOnAuthorizedInstanceStillRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAuthorizationInstance(t, "acc-authz-sync-inst", adminID, "authz-sync", "acc-authz-sync-src")

	code, payload := w2PatchBody(t, env, "acc-authz-sync-inst",
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v1", []string{"chat_json", "chat_sse"}))
	if code != http.StatusBadRequest {
		t.Fatalf("授权实例 connections PATCH 应 400：%d %v", code, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "授权实例的连接配置只读跟随来源账户") {
		t.Fatalf("拒绝文案应点名连接只读：%v", payload)
	}
	if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, "acc-authz-sync-inst"); got != "1" {
		t.Fatalf("拒绝后 revision 不应变化：%s", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_connections WHERE account_id = ?`, "acc-authz-sync-inst"); got != 0 {
		t.Fatalf("拒绝后不应写入连接行：%d", got)
	}
}

// TestPatchConnectionsOnlyConnectionChangeResetsHealth 锁定 connections-only
// base_url 变化按既有语义进入 pending_test 并清健康投影。
func TestPatchConnectionsOnlyConnectionChangeResetsHealth(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "连接变化健康投影")
	env.exec(t, `UPDATE accounts SET
		next_health_check_at = '2030-01-01T00:00:00Z',
		last_health_check_at = '2029-12-31T00:00:00Z',
		last_health_success_at = '2029-12-30T00:00:00Z',
		health_check_failure_count = 3,
		health_check_failure_started_at = '2029-12-29T00:00:00Z',
		last_health_check_status_code = 500,
		last_health_check_error_code = 'server_error',
		last_health_check_error_message = 'boom',
		last_health_check_trace_id = 'trace-sync'
		WHERE id = ?`, id)

	code, payload := w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v2", []string{"chat_json", "chat_sse"}))
	if code != http.StatusOK {
		t.Fatalf("connections-only base_url 变化应 200：%d %v", code, payload)
	}
	if !containsChange(dataMap(t, payload)["changedFields"], "connections") {
		t.Fatalf("changedFields 应含 connections：%v", dataMap(t, payload)["changedFields"])
	}
	if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = ?`, id); got != "pending_test" {
		t.Fatalf("连接变化应进入 pending_test：%s", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND next_health_check_at IS NULL
		AND last_health_check_at IS NULL
		AND last_health_success_at IS NULL
		AND health_check_failure_count = 0
		AND health_check_failure_started_at IS NULL
		AND last_health_check_status_code IS NULL
		AND last_health_check_error_code IS NULL
		AND last_health_check_error_message IS NULL
		AND last_health_check_trace_id IS NULL`, id); got != 1 {
		t.Fatalf("连接变化应清健康投影（count=%d）", got)
	}
}

// TestPatchConnectionsOnlyModeSyncMappingFailureRollsBack 锁定事务回滚语义：
// connections-only 同步触发 endpointModesChanged 后，既有映射目录断言失败必须
// 整体回滚（连接、凭据影子副本、映射与 revision 均保持原值），不得放宽断言。
func TestPatchConnectionsOnlyModeSyncMappingFailureRollsBack(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "连接同步映射回滚")

	// 先建立 responses-only 连接与一条 responses→responses 映射。
	code, payload := w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v1",
			[]string{"responses_json", "responses_sse"})+
			`,"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"responses_json","healthCheckModel":"gpt-4o-mini"`)
	if code != http.StatusOK {
		t.Fatalf("建立 responses-only 连接应 200：%d %v", code, payload)
	}
	code, payload = w2PatchBody(t, env, id,
		`"supportedModels":["gpt-4o-mini","gpt-4o"],
		"modelMappings":[{"sourceModel":"gpt-4o-mini","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o","upstreamEndpointFamily":"responses"}]`)
	if code != http.StatusOK {
		t.Fatalf("建立 responses 映射应 200：%d %v", code, payload)
	}
	revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id)
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id)

	// 连接切到 chat-only，并显式提交同一条 responses 映射（连接段因此跳过既有
	// 映射预校验），由凭据同步后的 endpointModesChanged 触发目录断言失败。
	code, payload = w2PatchBody(t, env, id,
		`"connections":`+connectionsPayloadForTest("https://api.openai.com/v1", []string{"chat_json", "chat_sse"})+
			`,"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini",
			"modelMappings":[{"sourceModel":"gpt-4o-mini","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o","upstreamEndpointFamily":"responses"}]`)
	if code != http.StatusBadRequest {
		t.Fatalf("映射目录断言失败应 400：%d %v", code, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "Responses") {
		t.Fatalf("拒绝文案应点名 Responses：%v", payload)
	}
	if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id); got != revision {
		t.Fatalf("失败事务不应递增 revision：%s -> %s", revision, got)
	}
	if got := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id); got != sealed {
		t.Fatal("失败事务不应重封凭据")
	}
	assertCredentialAndConnectionModes(t, env, id, []string{"responses_json", "responses_sse"})
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?
		AND source_endpoint_family = 'responses'`, id); got != 1 {
		t.Fatalf("失败事务不应改写映射行：%d", got)
	}
}

// TestAccountPatchGatewayRuntimeFieldsIncludesConnections 锁定 connections 进入
// 网关运行时缓存失效字段集，并直接走 finishPatchSideEffects 判定链。
func TestAccountPatchGatewayRuntimeFieldsIncludesConnections(t *testing.T) {
	if !accountPatchGatewayRuntimeFields["connections"] {
		t.Fatal("accountPatchGatewayRuntimeFields 必须包含 connections")
	}
	invalidator := &recordingInvalidator{}
	store := &Store{invalidator: invalidator}
	store.finishPatchSideEffects(context.Background(), &PatchResult{
		ID:            "acc-runtime-fields",
		ChangedFields: []string{"connections"},
	})
	lookups, reasons := invalidator.snapshot()
	if len(lookups) != 0 {
		t.Fatalf("connections 变化不应触发 lookup 失效：%v", lookups)
	}
	if len(reasons) != 1 || reasons[0] != "account_management_patch" {
		t.Fatalf("connections 变化应触发网关运行时失效：%v", reasons)
	}
}
