package accounts

// BUG-0337 回归：常规账户面入口映射的 targetConnectionPresetCode 缺省回退。
//
// B1 断点批前全部 account_connections 行的 connection_preset_code 均为空串，
// 前端不携带连接维度（编辑对话框单连接隐式 UI，normalizeAccountModelMappings
// 从不发送 targetConnectionPresetCode）的映射载荷经空 preset 隐式命中连接；
// B1 后连接行都携带真实 preset，空 preset 在 resolveEntryConnectionID 恒 0 行
// → 英文 ValidationError（"connection preset \"\" resolves to no enabled
// account connection"）被 kernel localize 改写为「请求参数无效」，用户侧不可
// 归因。2026-10-24 生产编辑账户（扩支持模型 + 新增映射，acc_c339f2160fa88c75）
// 首次撞上。修复 = 空 preset 回退为该账户唯一的启用连接（恢复 B1 前隐式默认
// UX，全库 2026-10-24 实测 0 个多连接账户）；多启用连接无法无歧义默认，按中
// 文 400 列出候选 preset（localize 不改写，用户可归因）；零启用连接同样中文
// 400。显式 preset 行为逐字节不变。

import (
	"net/http"
	"strings"
	"testing"
)

// TestBug0337EmptyPresetMappingDefaultsToSingleEnabledConnection 是生产请求的
// 准确形态：扩支持模型 + 新增映射行、载荷不带 targetConnectionPresetCode、
// 账户恰有一条启用连接（createPayload 自带 gpt_openai_v1 连接，不再另种）→
// 回退到该连接，映射落库 target_connection_id 命中。
func TestBug0337EmptyPresetMappingDefaultsToSingleEnabledConnection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "BUG-0337 单连接回退")

	code, payload := w2PatchBody(t, env, id, `"supportedModels":["gpt-4o-mini","gpt-4.1","gpt-4o"],
		"modelMappings":[{"sourceModel":"gpt-4o","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o","upstreamEndpointFamily":"chat_completions"}]`)
	if code != http.StatusOK {
		t.Fatalf("单连接空 preset 映射应回退放行：%d %v", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ? AND source_model = 'gpt-4o'
		AND target_connection_id = (SELECT id FROM account_connections WHERE account_id = ? AND connection_preset_code = 'gpt_openai_v1')`, id, id); got != 1 {
		t.Fatalf("映射应落库且 target_connection_id 命中唯一启用连接：%d", got)
	}
}

// TestBug0337EmptyPresetMappingWithMultipleConnectionsRejectedInChinese：多个
// 启用连接时空 preset 无法无歧义默认，必须中文 400 并列出全部候选 preset——
// 中文文案过 kernel localize 不被改写为「请求参数无效」，用户可归因。
func TestBug0337EmptyPresetMappingWithMultipleConnectionsRejectedInChinese(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "BUG-0337 多连接歧义")
	env.seedAccountConnection(t, id, "generic_openai_v1", "gpt-4o-mini", "gpt-4.1")

	code, payload := w2PatchBody(t, env, id, `"supportedModels":["gpt-4o-mini","gpt-4.1","gpt-4o"],
		"modelMappings":[{"sourceModel":"gpt-4o","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o","upstreamEndpointFamily":"chat_completions"}]`)
	if code != http.StatusBadRequest {
		t.Fatalf("多启用连接空 preset 应 400：%d %v", code, payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "多个启用的上游连接") ||
		!strings.Contains(message, "targetConnectionPresetCode") ||
		!strings.Contains(message, "gpt_openai_v1") ||
		!strings.Contains(message, "generic_openai_v1") {
		t.Fatalf("应中文列出候选 preset（localize 不改写）：%v", payload["message"])
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?`, id); got != 0 {
		t.Fatalf("被拒 PATCH 不应落映射行：%d", got)
	}
}

// TestBug0337ExplicitPresetBehaviorUnchanged 锁定显式 preset 两臂逐字节不变：
// 命中启用连接照常放行；未知 preset 保留既有英文 ValidationError（本缺陷不在
// 该臂，不随本批改文案）。
func TestBug0337ExplicitPresetBehaviorUnchanged(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "BUG-0337 显式 preset")
	env.seedAccountConnection(t, id, "generic_openai_v1", "gpt-4o-mini", "gpt-4.1", "gpt-4o")

	code, payload := w2PatchBody(t, env, id, `"modelMappings":[{"sourceModel":"gpt-4o","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o","upstreamEndpointFamily":"chat_completions","targetConnectionPresetCode":"generic_openai_v1"}]`)
	if code != http.StatusOK {
		t.Fatalf("显式命中 preset 应放行：%d %v", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?
		AND target_connection_id = ?`, id, "conn-"+id+"-generic_openai_v1"); got != 1 {
		t.Fatalf("映射应落库到显式连接：%d", got)
	}
}

// TestBug0337ZeroEnabledConnectionsRejectedInChinese：零启用连接时空 preset
// 无可回退，中文 400 可归因。
func TestBug0337ZeroEnabledConnectionsRejectedInChinese(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "BUG-0337 零连接")
	// createPayload 自带一条启用连接；停用后构成零启用连接形态。
	env.exec(t, `UPDATE account_connections SET enabled = 0 WHERE account_id = ?`, id)

	// target 取支持集合内的 gpt-4.1，避免 T52 门（支持集合校验）先于连接解析拒绝。
	code, payload := w2PatchBody(t, env, id, `"modelMappings":[{"sourceModel":"gpt-4.1","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4.1","upstreamEndpointFamily":"chat_completions"}]`)
	if code != http.StatusBadRequest {
		t.Fatalf("零启用连接空 preset 应 400：%d %v", code, payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "没有启用的上游连接") {
		t.Fatalf("应中文提示无启用连接（localize 不改写）：%v", payload["message"])
	}
}
