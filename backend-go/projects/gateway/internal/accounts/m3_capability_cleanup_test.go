package accounts

// AI 账户能力域拆分 M3「域外能力」显式清理（计划 §M3 配套适配 + D6 只缩不扩）
// 行为级测试（T4 清理并发场景在 M3 补齐）：
//   - 显式清理缩小成功 + 操作日志单独审计（「清理域外能力」含被移除值列表）；
//   - 扩大/换元（减一加一）拒绝；
//   - 删键（null）走默认集重置维持逐项相等拦截（I7 不借归一化静默删除）；
//   - legacy credentials 显式数组同样放行缩小；
//   - 批量编辑路径逐账户缩小 + 审计；
//   - 清理并发 CAS 409 不变；
//   - 列表 legacyCapabilityModes 提示列（对话域遗留 + 媒体域容错）。

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// m3SeedDomainAccount 直插一个指定域/供应商/类型的存量账户（modes 可含域外
// 遗留值），供列表徽标与媒体域容错用例使用。
func m3SeedDomainAccount(t *testing.T, env *testEnv, id, ownerID, name, domain, providerCode, profileID, accountType string, modes []string) {
	t.Helper()
	credentials := Credentials{"api_key": "sk-m3-" + id, "base_url": "https://api.openai.com/v1"}
	if modes != nil {
		credentials["supported_endpoint_modes"] = anyStrings(modes)
	}
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, capability_domain, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'openai', 'v1', ?, ?, 'active', ?, ?, 'sk-***', 'm-default', ?, ?)`,
		id, ownerID, providerCode, profileID, name, accountType, domain, sealed, now, now)
}

// m3SinkEntries 按动作过滤录制 sink 的条目。
func m3SinkEntries(env *testEnv, action string) []authsys.OperationLogEntry {
	entries := []authsys.OperationLogEntry{}
	for _, entry := range env.sink.entries {
		if entry.Action == action {
			entries = append(entries, entry)
		}
	}
	return entries
}

// TestM3CleanupShrinkSuccessWithAudit：显式数组缩小 → 200 + 库内遗留值移除 +
// 操作日志单独「清理域外能力」条目（含账户与被移除值列表）。
func TestM3CleanupShrinkSuccessWithAudit(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	m2SeedAccountWithModes(t, env, "acc-m3-mixed", adminID, "m3-mixed", []string{"chat_json", "video_create", "video_get"})

	// 列表提示列：清理前 mixed 行携带 legacyCapabilityModes。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?keyword=m3-mixed", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", code, payload)
	}
	items := dataMap(t, payload)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("列表行数应 1：%v", items)
	}
	row := items[0].(map[string]any)
	legacy, ok := row["legacyCapabilityModes"].([]any)
	if !ok || len(legacy) != 2 {
		t.Fatalf("遗留行应回显 2 个域外能力：%v", row["legacyCapabilityModes"])
	}

	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m3-mixed",
		`{"expectedConfigRevision":1,"credentialsPatch":{"supported_endpoint_modes":["chat_json"]}}`)
	if code != http.StatusOK {
		t.Fatalf("显式清理缩小应 200：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m3-mixed"); !unorderedStringListEqual(modes, []string{"chat_json"}) {
		t.Fatalf("域外遗留值应被清理：%v", modes)
	}

	entries := m3SinkEntries(env, "cleanup_capability_legacy")
	if len(entries) != 1 {
		t.Fatalf("清理应落一条单独审计：%v", env.sink.actions())
	}
	entry := entries[0]
	if entry.ResourceType != "account" || entry.ResourceID != "acc-m3-mixed" || entry.ResourceName != "m3-mixed" {
		t.Fatalf("审计条目应指向被清理账户：%v %v", entry.ResourceID, entry.ResourceName)
	}
	if !strings.Contains(entry.Summary, "video_create") || !strings.Contains(entry.Summary, "video_get") {
		t.Fatalf("审计 Summary 应含被移除值列表：%s", entry.Summary)
	}
	if len(entry.Changes) != 1 || entry.Changes[0].Field != "capabilityLegacyModes" {
		t.Fatalf("审计 changes 应含域外能力变更：%v", entry.Changes)
	}

	// 清理后列表字段缺席（omitempty）。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts?keyword=m3-mixed", "")
	if code != http.StatusOK {
		t.Fatalf("清理后列表应 200：%d %v", code, payload)
	}
	row = dataMap(t, payload)["items"].([]any)[0].(map[string]any)
	if _, present := row["legacyCapabilityModes"]; present {
		t.Fatalf("清理后不应再回显域外能力：%v", row["legacyCapabilityModes"])
	}
}

// TestM3CleanupRejectsExpansionAndSwap：扩大与换元（减一加一）一律拒绝。
func TestM3CleanupRejectsExpansionAndSwap(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	m2SeedAccountWithModes(t, env, "acc-m3-guard", adminID, "m3-guard", []string{"chat_json", "video_create"})

	// 换元：移除 video_create 但加入 video_get → newLegacy ⊄ oldLegacy → 400。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m3-guard",
		`{"expectedConfigRevision":1,"credentialsPatch":{"supported_endpoint_modes":["chat_json","video_get"]}}`)
	if code != http.StatusBadRequest || m2Message(payload) != "域外能力不可变更（仅可通过显式清理缩小）" {
		t.Fatalf("换元应 400：%d %v", code, payload)
	}
	// 扩大：原值保留再加新值 → 400。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m3-guard",
		`{"expectedConfigRevision":1,"credentialsPatch":{"supported_endpoint_modes":["chat_json","video_create","video_get"]}}`)
	if code != http.StatusBadRequest || m2Message(payload) != "域外能力不可变更（仅可通过显式清理缩小）" {
		t.Fatalf("扩大应 400：%d %v", code, payload)
	}
	// 删键（null）走归一化默认集重置：默认集不含遗留值，放开即借归一化静默
	// 删除遗留能力（I7）→ 维持逐项相等拦截 400。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m3-guard",
		`{"expectedConfigRevision":1,"credentialsPatch":{"supported_endpoint_modes":null}}`)
	if code != http.StatusBadRequest || m2Message(payload) != "域外能力不可变更（仅可通过显式清理缩小）" {
		t.Fatalf("删键应维持拦截：%d %v", code, payload)
	}
	// legacy credentials（非 patch 形态）显式数组同样放行缩小。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m3-guard",
		`{"expectedConfigRevision":1,"credentials":{"api_key":"sk-m3-rotated","supported_endpoint_modes":["chat_json"]}}`)
	if code != http.StatusOK {
		t.Fatalf("legacy 显式数组缩小应 200：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m3-guard"); !unorderedStringListEqual(modes, []string{"chat_json"}) {
		t.Fatalf("域外遗留值应被清理：%v", modes)
	}
	if entries := m3SinkEntries(env, "cleanup_capability_legacy"); len(entries) != 1 {
		t.Fatalf("legacy 形态清理同样落单独审计：%v", env.sink.actions())
	}
}

// TestM3BatchCleanupLegacyCapabilities：批量编辑路径逐账户缩小 + 审计。
func TestM3BatchCleanupLegacyCapabilities(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	m2SeedAccountWithModes(t, env, "acc-m3-b1", adminID, "m3-b1", []string{"chat_json", "chat_sse", "video_create"})
	m2SeedAccountWithModes(t, env, "acc-m3-b2", adminID, "m3-b2", []string{"chat_json", "video_get"})
	// 批量 modes 覆盖触发 assertSupportedModelsRequired，账户须已有支持模型。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES ('acc-m3-b1', 'gpt', 'm-default', ?), ('acc-m3-b2', 'gpt', 'm-default', ?)`, now, now)

	// 同域整批（M2 规则不变）+ 显式数组逐账户缩小：b1 清掉 video_create，
	// b2 清掉 video_get。
	body := `{"targets":[{"accountId":"acc-m3-b1","configRevision":1},{"accountId":"acc-m3-b2","configRevision":1}],
		"updates":{"supportedEndpointModes":{"enabled":true,"value":["chat_json","chat_sse"]}}}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", body)
	if code != http.StatusOK {
		t.Fatalf("批量缩小应 200：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m3-b1"); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("b1 遗留值应被清理：%v", modes)
	}
	if modes := m2AccountModes(t, env, "acc-m3-b2"); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("b2 遗留值应被清理（目标集为域内并集，chat_sse 属域内新增）：%v", modes)
	}
	items := dataMap(t, payload)["items"].([]any)
	cleanupByID := map[string][]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		if list, ok := item["capabilityLegacyCleanup"].([]any); ok && len(list) > 0 {
			cleanupByID[item["id"].(string)] = list
		}
	}
	if len(cleanupByID["acc-m3-b1"]) != 1 || cleanupByID["acc-m3-b1"][0] != "video_create" {
		t.Fatalf("b1 应回显清理值：%v", cleanupByID)
	}
	if len(cleanupByID["acc-m3-b2"]) != 1 || cleanupByID["acc-m3-b2"][0] != "video_get" {
		t.Fatalf("b2 应回显清理值：%v", cleanupByID)
	}
	if entries := m3SinkEntries(env, "cleanup_capability_legacy"); len(entries) != 2 {
		t.Fatalf("批量清理应逐账户落审计：%v", env.sink.actions())
	}

	// 批量扩大 → 整批 400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
		{"accountId":"acc-m3-b1","configRevision":2},{"accountId":"acc-m3-b2","configRevision":2}],
		"updates":{"supportedEndpointModes":{"enabled":true,"value":["chat_json","chat_sse","video_cancel"]}}}`)
	if code != http.StatusBadRequest || m2Message(payload) != "域外能力不可变更（仅可通过显式清理缩小）" {
		t.Fatalf("批量扩大应整批 400：%d %v", code, payload)
	}
}

// TestM3CleanupConcurrentCASConflict：清理并发 CAS 409 不变，遗留集合不被覆盖。
func TestM3CleanupConcurrentCASConflict(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	m2SeedAccountWithModes(t, env, "acc-m3-cas", adminID, "m3-cas", []string{"chat_json", "video_create"})

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m3-cas",
		`{"expectedConfigRevision":99,"credentialsPatch":{"supported_endpoint_modes":["chat_json"]}}`)
	if code != http.StatusConflict {
		t.Fatalf("清理并发应 409：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m3-cas"); !unorderedStringListEqual(modes, []string{"chat_json", "video_create"}) {
		t.Fatalf("409 后遗留集合不得变化：%v", modes)
	}
	if entries := m3SinkEntries(env, "cleanup_capability_legacy"); len(entries) != 0 {
		t.Fatalf("失败的清理不得落审计：%v", env.sink.actions())
	}
}

// TestM3ListLegacyCapabilityModesMediaTolerance：媒体域账户的域外遗留值同样
// 回显（媒体域理论无遗留，容错展示），干净行缺席字段。
func TestM3ListLegacyCapabilityModesMediaTolerance(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	m2SeedProvider(t, env, adminID, "volcengine", volcengineOpenAIV1ProfileID, "openai", "v1", []string{"api_key"})
	// video 域 + 对话遗留值（存量混合形态容错）。
	m3SeedDomainAccount(t, env, "acc-m3-video", adminID, "m3-video", "video", "volcengine", volcengineOpenAIV1ProfileID, "api_key",
		[]string{"video_create", "chat_json"})
	// 同域干净行（域内集合）。
	m3SeedDomainAccount(t, env, "acc-m3-video-clean", adminID, "m3-video-clean", "video", "volcengine", volcengineOpenAIV1ProfileID, "api_key",
		[]string{"video_create"})

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?domain=video", "")
	if code != http.StatusOK {
		t.Fatalf("video 域列表应 200：%d %v", code, payload)
	}
	items := dataMap(t, payload)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("video 域列表应 2 行：%v", items)
	}
	seen := map[string][]any{}
	for _, raw := range items {
		row := raw.(map[string]any)
		legacy, _ := row["legacyCapabilityModes"].([]any)
		seen[row["name"].(string)] = legacy
	}
	if legacy := seen["m3-video"]; len(legacy) != 1 || legacy[0] != "chat_json" {
		t.Fatalf("媒体域遗留值应容错回显：%v", seen)
	}
	if legacy, present := seen["m3-video-clean"]; present && len(legacy) != 0 {
		t.Fatalf("干净行不应回显域外能力：%v", legacy)
	}
}
