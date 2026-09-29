package accounts

// BUG-0238 单测（docs/bug/问题-0238-管理面账户编辑明细接口回显明文凭据.md）：
// ① FindEditBasicDetail 敏感键统一密文占位 + credentialsMasked；② reveal
// 明文投影（含 api_keys 全量池）与授权实例 403/缺失 404 语义；③ PATCH 提交
// 占位值不污染存储凭据（混合 api_keys 按滤占位后数组语义）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// seedAccountCredentials 直插一个携带任意凭据记录的 api_key 账户行（读路径
// fixture，不经历创建归一化）。
func (e *testEnv) seedAccountCredentials(t *testing.T, id, ownerID, name string, credentials Credentials) {
	t.Helper()
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', 'active', ?, 'sk-see***'+?, 'gpt-4o-mini', ?, ?)`,
		id, ownerID, name, sealed, id, now, now)
}

// storedCredentials 解封当前存储凭据（占位防回写断言的对照源）。
func (e *testEnv) storedCredentials(t *testing.T, id string) Credentials {
	t.Helper()
	var credentials Credentials
	if err := DecryptJSON(testSecret, e.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id), &credentials); err != nil {
		t.Fatal(err)
	}
	return credentials
}

func TestBug0238EditDetailCipherPlaceholder(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 敏感键 + 非敏感键混合的存量凭据。
	env.seedAccountCredentials(t, "acc-b238-a", adminID, "占位账户", Credentials{
		"api_key": "sk-detail-secret", "api_keys": []any{"sk-pool-a", "sk-pool-b"},
		"api_key_strategy": "random", "base_url": "https://api.openai.com/v1",
	})
	detail, err := env.store.FindEditBasicDetail(context.Background(), "acc-b238-a", admin)
	if err != nil || detail == nil {
		t.Fatalf("detail: %v %v", detail, err)
	}
	if detail.Credentials["api_key"] != credentialCipherPlaceholder {
		t.Fatalf("api_key 应为占位符: %v", detail.Credentials)
	}
	pool, _ := detail.Credentials["api_keys"].([]any)
	if len(pool) != 2 || pool[0] != credentialCipherPlaceholder || pool[1] != credentialCipherPlaceholder {
		t.Fatalf("api_keys 应逐项占位: %v", detail.Credentials["api_keys"])
	}
	if detail.Credentials["api_key_strategy"] != "random" || detail.Credentials["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("非敏感键应原样返回: %v", detail.Credentials)
	}
	if !detail.CredentialsMasked {
		t.Fatalf("存在被替换敏感键时 credentialsMasked 应为 true: %v", detail.Credentials)
	}
	// 明文不落响应：整个投影序列化后不得出现任何真实材料。
	raw, _ := json.Marshal(detail.Credentials)
	if strings.Contains(string(raw), "sk-detail-secret") || strings.Contains(string(raw), "sk-pool-a") {
		t.Fatalf("明细泄露明文凭据: %s", raw)
	}

	// 无敏感键的行：非敏感键原样、credentialsMasked=false。
	env.seedAccountCredentials(t, "acc-b238-b", adminID, "无敏感键账户", Credentials{
		"base_url": "https://api.openai.com/v1", "api_key_strategy": "random",
	})
	plain, err := env.store.FindEditBasicDetail(context.Background(), "acc-b238-b", admin)
	if err != nil || plain == nil {
		t.Fatalf("plain detail: %v %v", plain, err)
	}
	if plain.CredentialsMasked || plain.Credentials["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("无敏感键行不应置 masked: %v %v", plain.Credentials, plain.CredentialsMasked)
	}
}

func TestBug0238RevealCredentialsStore(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccountCredentials(t, "acc-b238-r", adminID, "reveal账户", Credentials{
		"api_key": "sk-detail-secret", "api_keys": []any{"sk-pool-a", "sk-pool-b"},
		"api_key_strategy": "random", "base_url": "https://api.openai.com/v1",
	})
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	revealed, err := env.store.FindRevealableCredentials(context.Background(), "acc-b238-r", admin)
	if err != nil || revealed == nil {
		t.Fatalf("reveal: %v %v", revealed, err)
	}
	if revealed.ID != "acc-b238-r" || revealed.Name != "reveal账户" || revealed.Type != "api_key" ||
		revealed.OwnerSystemAccountID != adminID || revealed.ConfigRevision != 1 {
		t.Fatalf("reveal 身份字段: %+v", revealed)
	}
	if revealed.Credentials["api_key"] != "sk-detail-secret" {
		t.Fatalf("reveal 应返回明文 api_key: %v", revealed.Credentials)
	}
	pool, _ := revealed.Credentials["api_keys"].([]any)
	if len(pool) != 2 || pool[0] != "sk-pool-a" || pool[1] != "sk-pool-b" {
		t.Fatalf("reveal 应返回 api_keys 全量池: %v", revealed.Credentials["api_keys"])
	}
	if revealed.Credentials["base_url"] != "https://api.openai.com/v1" || revealed.Credentials["api_key_strategy"] != "random" {
		t.Fatalf("reveal 非敏感键应原样: %v", revealed.Credentials)
	}

	// 缺失/越权 → (nil, nil)。
	missing, err := env.store.FindRevealableCredentials(context.Background(), "acc-b238-none", admin)
	if err != nil || missing != nil {
		t.Fatalf("缺失行应返回 (nil,nil): %v %v", missing, err)
	}
	bob := AccessScope{ViewerID: "sys-bob"}
	cross, err := env.store.FindRevealableCredentials(context.Background(), "acc-b238-r", bob)
	if err != nil || cross != nil {
		t.Fatalf("越权行应返回 (nil,nil): %v %v", cross, err)
	}

	// 授权实例行：与 detail 相同的保留 403（admin 面同样拒绝）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('acc-b238-inst', ?, 'gpt', 'prof-gpt', 'openai', 'v1', '实例行', 'api_key', 'active',
		'sealed', 'masked', 'gpt-4o-mini', 'acc-b238-r', ?, ?)`, adminID, now, now)
	if _, err := env.store.FindRevealableCredentials(context.Background(), "acc-b238-inst", admin); err == nil {
		t.Fatal("授权实例行 reveal 应 403")
	} else if _, ok := err.(*editBasicForbiddenError); !ok {
		t.Fatalf("授权实例行应为 editBasicForbiddenError: %v", err)
	}
}

func TestBug0238RevealRouteAndAudit(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccountCredentials(t, "acc-b238-route", adminID, "路由账户", Credentials{
		"api_key": "sk-route-secret", "api_keys": []any{"sk-pool-a"},
		"api_key_strategy": "random", "base_url": "https://api.openai.com/v1",
	})

	// admin 面 POST reveal → 200 + 明文投影 + no-store。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-b238-route/reveal-credentials", `{}`)
	if code != http.StatusOK {
		t.Fatalf("admin reveal: %d %v", code, payload)
	}
	reveal := dataMap(t, payload)
	if reveal["id"] != "acc-b238-route" || reveal["configRevision"] != float64(1) {
		t.Fatalf("reveal 身份字段: %v", reveal)
	}
	revealCredentials := reveal["credentials"].(map[string]any)
	if revealCredentials["api_key"] != "sk-route-secret" {
		t.Fatalf("reveal 应返回明文 api_key: %v", revealCredentials)
	}
	if pool, _ := revealCredentials["api_keys"].([]any); len(pool) != 1 || pool[0] != "sk-pool-a" {
		t.Fatalf("reveal 应返回 api_keys 全量池: %v", revealCredentials["api_keys"])
	}

	// 审计：accounts.reveal_credentials，Changes 只记敏感键名清单，不记值。
	var auditSummary, auditAfter string
	audited := false
	env.sink.mu.Lock()
	for _, entry := range env.sink.entries {
		if entry.Module != "accounts" || entry.Action != "reveal_credentials" || len(entry.Changes) != 1 {
			continue
		}
		audited = true
		auditSummary = entry.Summary
		auditAfter = entry.Changes[0].After
	}
	env.sink.mu.Unlock()
	if !audited {
		t.Fatalf("reveal 审计缺失: %v", env.sink.actions())
	}
	if auditSummary != "查看账户凭据明文：路由账户" {
		t.Fatalf("reveal 审计 Summary: %q", auditSummary)
	}
	if auditAfter != "api_key,api_keys" {
		t.Fatalf("reveal 审计 Changes.After 应为敏感键名清单: %q", auditAfter)
	}
	if strings.Contains(auditAfter, "sk-route-secret") || strings.Contains(auditAfter, "sk-pool-a") {
		t.Fatalf("reveal 审计不得记录凭据值: %q", auditAfter)
	}

	// owner 自面 reveal 同样可达；越权与缺失走 404；实例行走 403。
	ownerID := env.login(t, "b238owner", "owner-pass", "user")
	env.seedAccountCredentials(t, "acc-b238-self", ownerID, "自面账户", Credentials{
		"api_key": "sk-self-secret", "base_url": "https://api.openai.com/v1",
	})
	code, mine := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-b238-self/reveal-credentials", `{}`)
	if code != http.StatusOK || dataMap(t, mine)["credentials"].(map[string]any)["api_key"] != "sk-self-secret" {
		t.Fatalf("self reveal: %d %v", code, mine)
	}
	env.login(t, "b238bob", "bob-pass", "user")
	code, cross := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-b238-self/reveal-credentials", `{}`)
	if code != http.StatusNotFound || cross["message"] != "账户不存在" {
		t.Fatalf("越权 reveal 应 404: %d %v", code, cross)
	}
	code, absent := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-b238-missing/reveal-credentials", `{}`)
	if code != http.StatusNotFound || absent["message"] != "账户不存在" {
		t.Fatalf("缺失 reveal 应 404: %d %v", code, absent)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('acc-b238-inst2', ?, 'gpt', 'prof-gpt', 'openai', 'v1', '实例行2', 'api_key', 'active',
		'sealed', 'masked', 'gpt-4o-mini', 'acc-b238-route', ?, ?)`, adminID, now, now)
	env.login(t, "root", "root-pass", "super_admin")
	code, instance := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-b238-inst2/reveal-credentials", `{}`)
	if code != http.StatusForbidden || instance["message"] != "无权查看账户凭据" {
		t.Fatalf("实例 reveal 应 403: %d %v", code, instance)
	}
}

func TestBug0238PatchStripsCipherPlaceholder(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("占位防回写"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	const originalKey = "sk-live-secret-1234567890"

	// legacy credentials 通道提交占位 api_key：存储凭据不变、revision 不推进。
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"credentials":{"api_key":"`+credentialCipherPlaceholder+`","base_url":"https://api.openai.com/v1"}}`)
	if code != http.StatusOK {
		t.Fatalf("占位 PATCH: %d %v", code, patched)
	}
	if stored := env.storedCredentials(t, id); stored["api_key"] != originalKey {
		t.Fatalf("占位提交不得污染存储 api_key: %v", stored)
	}
	if revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, id); revision != "1" {
		t.Fatalf("无实际变更不得推进 config_revision: %s", revision)
	}

	// credentialsPatch 通道同样剔除占位（null=删键语义不受影响）。
	code, patchChannel := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"credentialsPatch":{"api_key":"`+credentialCipherPlaceholder+`"}}`)
	if code != http.StatusOK {
		t.Fatalf("credentialsPatch 占位提交: %d %v", code, patchChannel)
	}
	if stored := env.storedCredentials(t, id); stored["api_key"] != originalKey {
		t.Fatalf("credentialsPatch 占位提交不得污染存储 api_key: %v", stored)
	}

	// api_keys 混合占位：滤除占位项后以余下数组为新值（保留 2 项真实 Key，
	// 归一化管线才会保留池形态；旧池成员被整体替换）。
	env.seedAccountCredentials(t, "acc-b238-pool", adminID, "混合池账户", Credentials{
		"api_keys": []any{"sk-pool-a", "sk-pool-b"}, "api_key_strategy": "random",
		"base_url": "https://api.openai.com/v1",
	})
	code, mixed := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-b238-pool",
		`{"expectedConfigRevision":1,"credentials":{"api_keys":["`+credentialCipherPlaceholder+`","sk-pool-new-1","sk-pool-new-2"],"api_key_strategy":"random"}}`)
	if code != http.StatusOK {
		t.Fatalf("混合池 PATCH: %d %v", code, mixed)
	}
	stored := env.storedCredentials(t, "acc-b238-pool")
	pool, _ := stored["api_keys"].([]any)
	if len(pool) != 2 || pool[0] != "sk-pool-new-1" || pool[1] != "sk-pool-new-2" {
		t.Fatalf("混合 api_keys 应按滤占位后数组落库: %v", stored["api_keys"])
	}
	if stored["api_key"] != "sk-pool-new-1" {
		t.Fatalf("池首键应同步为归一化 api_key: %v", stored["api_key"])
	}

	// api_keys 全占位：滤后为空 → 视为省略该键，存储凭据原样保留（单 Key 池
	// 经归一化坍缩为单键形态，真实 Key 不变）。
	env.seedAccountCredentials(t, "acc-b238-pool2", adminID, "全占位池账户", Credentials{
		"api_keys": []any{"sk-pool-c"}, "api_key_strategy": "random",
		"base_url": "https://api.openai.com/v1",
	})
	code, allPlaceholder := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-b238-pool2",
		`{"expectedConfigRevision":1,"credentials":{"api_keys":["`+credentialCipherPlaceholder+`"],"api_key_strategy":"random"}}`)
	if code != http.StatusOK {
		t.Fatalf("全占位池 PATCH: %d %v", code, allPlaceholder)
	}
	if stored := env.storedCredentials(t, "acc-b238-pool2"); stored["api_key"] != "sk-pool-c" {
		t.Fatalf("全占位 api_keys 应保留现值: %v", stored)
	}
}

// 纯函数臂：占位替换与剔除的字面量契约。
func TestBug0238PlaceholderPureArms(t *testing.T) {
	masked, maskedAny := applyCipherPlaceholder(Credentials{
		"api_key": "secret", "api_keys": []any{"a", 1.0}, "access_token": "t",
		"refresh_token": "", "client_secret": nil, "base_url": "https://x", "client_id": "cid",
	})
	if masked["api_key"] != credentialCipherPlaceholder || maskedAny != true {
		t.Fatalf("string 敏感键应直接占位: %v %v", masked, maskedAny)
	}
	if masked["base_url"] != "https://x" || masked["client_id"] != "cid" {
		t.Fatalf("非敏感键应原样: %v", masked)
	}
	if masked["client_secret"] != nil {
		t.Fatalf("非 string 敏感值保持原样: %v", masked["client_secret"])
	}
	if pool, _ := masked["api_keys"].([]any); len(pool) != 2 || pool[0] != credentialCipherPlaceholder || pool[1] != 1.0 {
		t.Fatalf("api_keys 逐项占位、非 string 项保留: %v", masked["api_keys"])
	}

	stripped := stripCipherPlaceholderCredentials(Credentials{
		"api_key":          credentialCipherPlaceholder,
		"api_keys":         []any{credentialCipherPlaceholder, "sk-real", 2.0},
		"access_token":     credentialCipherPlaceholder,
		"base_url":         "https://x",
		"api_key_strategy": "random",
	})
	if _, ok := stripped["api_key"]; ok {
		t.Fatalf("占位 string 敏感键应剔除: %v", stripped)
	}
	if _, ok := stripped["access_token"]; ok {
		t.Fatalf("占位 access_token 应剔除: %v", stripped)
	}
	if pool, _ := stripped["api_keys"].([]any); len(pool) != 2 || pool[0] != "sk-real" || pool[1] != 2.0 {
		t.Fatalf("混合 api_keys 应滤占位保留余项: %v", stripped["api_keys"])
	}
	if stripped["base_url"] != "https://x" || stripped["api_key_strategy"] != "random" {
		t.Fatalf("非敏感键不动: %v", stripped)
	}

	// 全占位数组 → 整键删除；空数组 → 同样删除。
	empty := stripCipherPlaceholderCredentials(Credentials{"api_keys": []any{credentialCipherPlaceholder}})
	if _, ok := empty["api_keys"]; ok {
		t.Fatalf("滤后为空的 api_keys 应删除: %v", empty)
	}
	blank := stripCipherPlaceholderCredentials(Credentials{"api_keys": []any{}})
	if _, ok := blank["api_keys"]; ok {
		t.Fatalf("原空数组同样视为省略: %v", blank)
	}
}
