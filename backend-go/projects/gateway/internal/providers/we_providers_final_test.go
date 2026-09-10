package providers

import (
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 删除守卫：绑定账户的模型不可删除（409 + 明细文案）
// ---------------------------------------------------------------------------

func TestWeProvidersDeleteModelBoundToAccount(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// cu-2（global gpt-4o）绑定到一个 AI 账户的支持列表。
	accountID := "acc-bound"
	env.exec(t, `INSERT INTO accounts (id, system_account_id, name, deleted_at) VALUES (?, 'sys-1', '绑定账户', NULL)`, accountID)
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model) VALUES (?, 'gpt', 'gpt-4o')`, accountID)

	code, conflict := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-2", "")
	if code != http.StatusConflict {
		t.Fatalf("删除绑定模型: %d %v", code, conflict)
	}
	message, _ := conflict["message"].(string)
	if !strings.Contains(message, "模型已绑定 AI 账户，不能删除") || !strings.Contains(message, "个账户支持模型") {
		t.Fatalf("绑定消息 = %s", message)
	}

	// 解绑后可删除。
	env.exec(t, `DELETE FROM account_supported_models WHERE account_id = ?`, accountID)
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-2", "")
	if code != http.StatusOK || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("解绑后删除: %d %v", code, deleted)
	}
}

func TestWeProvidersPatchModelWrongProvider(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// cu-1 属于 gpt：换到 gemini 维度补丁 → 404（provider 不匹配分支）。
	updatedAt := env.updatedAtOf(t, "cu-1")
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gemini/models/cu-1",
		`{"expectedUpdatedAt":"`+updatedAt+`","inputUsdPer1M":9}`)
	if code != http.StatusNotFound || payload["message"] != "自定义模型不存在" {
		t.Fatalf("跨供应商补丁: %d %v", code, payload)
	}
}

func TestWeProvidersDefaultHealthCheckInvalidSelection(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 非法模型名：不在目录中 → 具体诊断消息。
	code, missing := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?systemAccountId=all",
		`{"model":"gpt-not-exist"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法默认模型: %d %v", code, missing)
	}
	message, _ := missing["message"].(string)
	if !strings.Contains(message, "gpt-not-exist") {
		t.Fatalf("应包含模型名: %s", message)
	}
}

func TestWeTextProtocolPredicateDialects(t *testing.T) {
	sqlitePredicate := textProtocolPredicate(&Store{}, "supported_api_protocols_json")
	if !strings.Contains(sqlitePredicate, "json_array_length") || !strings.Contains(sqlitePredicate, "json_each") {
		t.Fatal("sqlite 方言应使用 json1")
	}
	pgPredicate := textProtocolPredicate(&Store{pg: true}, "supported_api_protocols_json")
	if !strings.Contains(pgPredicate, "jsonb_array_length") || !strings.Contains(pgPredicate, "?|") {
		t.Fatal("pg 方言应使用 jsonb")
	}
}
