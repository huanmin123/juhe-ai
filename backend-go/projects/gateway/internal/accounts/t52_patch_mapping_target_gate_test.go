package accounts

// T52（BUG-0335 独立复审非阻塞发现）回归：patch 链映射整替换前的映射 target
// 预校验。account_entry_mappings 复合外键 (account_id, target_model_id) →
// account_supported_models(account_id, model_id)（设计 §5.1，无级联动作）把映
// 射 target 锁进账户支持模型集合；修复前 patch 链缺 batch/import 链同位预校验
// （AssertMappingUpstreamsAllowed，batch.go:1119 / import.go:1471），越界
// target 在生产 PG 由复合外键 23503 兜底成 500。修复后写入前按 400 族语义化
// 拒绝，文案与 batch/import 链同源。夹具沿用同包 PATCH 测试基建
// （createPayload / w2PatchAccount / seedAccountConnection / w2PatchBody）。

import (
	"context"
	"net/http"
	"testing"
)

// TestT52PatchMappingTargetOutsideSupportedModelsRejected 是生产 500 的准确形
// 态：target gpt-4o 可解析到 owner 可见 active models 行（排除
// mapping_target_unsupported 的目录拒绝路径），但不在账户支持模型集合
// {gpt-4o-mini, gpt-4.1} 内 → 400 语义化拒绝、文案逐字对照
// AssertMappingUpstreamsAllowed，且被拒 PATCH 不落任何映射行。
func TestT52PatchMappingTargetOutsideSupportedModelsRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "T52 映射门禁")
	env.seedAccountConnection(t, id, "generic_openai_v1", "gpt-4o-mini", "gpt-4.1")

	code, payload := w2PatchBody(t, env, id, `"modelMappings":[{"sourceModel":"gpt-4o","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o","upstreamEndpointFamily":"chat_completions","targetConnectionPresetCode":"generic_openai_v1"}]`)
	if code != http.StatusBadRequest {
		t.Fatalf("越界 target 应 400：%d %v", code, payload)
	}
	if payload["message"] != "映射上游模型只能选择账户支持模型：gpt-4o" {
		t.Fatalf("文案应与 batch/import 链 AssertMappingUpstreamsAllowed 一致：%v", payload["message"])
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?`, id); got != 0 {
		t.Fatalf("被拒 PATCH 不应落映射行：%d", got)
	}

	// store 层同判定：错误类型必须是 *ValidationError（accountscore 别名，
	// routes 据此映射 400 族），文案一致。
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	_, err := env.store.Patch(context.Background(), id, PatchInput{
		ExpectedConfigRevision: 1,
		ModelMappingsPresent:   true,
		ModelMappings: []ModelMapping{{
			SourceModel: "gpt-4o", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-4o", UpstreamEndpointFamily: "chat_completions",
			TargetConnectionPresetCode: "generic_openai_v1",
		}},
	}, scope)
	assertValidationError(t, err, "映射上游模型只能选择账户支持模型：gpt-4o")
}

// TestT52PatchMappingTargetValidatedAgainstPostPatchSupportedModels 证明校验
// 基准是本次 PATCH 生效后的支持模型集合，而非库中当前集合：同一 target
// gpt-4o（不在创建集合内）随本次支持模型扩集一并提交时放行并落库。修复前该
// 组合在生产 PG 同样 23503（支持模型先落新集合、映射 target 是新成员，本就
// 满足外键；此处锁定的是预校验不得用旧集合误拒合法组合）。
func TestT52PatchMappingTargetValidatedAgainstPostPatchSupportedModels(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "T52 生效集合")
	env.seedAccountConnection(t, id, "generic_openai_v1", "gpt-4o-mini", "gpt-4.1")

	code, payload := w2PatchBody(t, env, id, `"supportedModels":["gpt-4o-mini","gpt-4.1","gpt-4o"],
		"modelMappings":[{"sourceModel":"gpt-4o","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o","upstreamEndpointFamily":"chat_completions","targetConnectionPresetCode":"generic_openai_v1"}]`)
	if code != http.StatusOK {
		t.Fatalf("生效后集合内的 target 应放行：%d %v", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?
		AND source_model = 'gpt-4o' AND target_model_id = (SELECT id FROM models WHERE upstream_model = 'gpt-4o')`, id); got != 1 {
		t.Fatalf("映射未落库：%d", got)
	}
}
