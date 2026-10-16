package providers

import (
	"net/http"
	"testing"
)

// v8-D 媒体域新建默认供应商收敛定向测试：GET /providers/models/options/provider-codes
// 返回「有至少一条在售目录模型」的供应商去重编码，口径与 /models/options 完全一致
// （active + 目录可见 + 未过下线日，内置 ∪ 自定义，custom personal 行按 systemAccountId
// 收敛），聚合形态不受 options 端点 LIMIT 50 截断影响；modes 词表校验同源。
func TestV8DModelOptionProviderCodes(t *testing.T) {
	env := newTestEnv(t)
	const now = "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt', 'gpt', 'OpenAI', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-glm', 'glm', 'GLM', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-ds', 'ds', 'DeepSeek', 1, '[]', ?, ?)`, now, now)
	// gpt 视频目录：sora 模拟行已下线（ShutdownDate 早于今天）→ 不计入 video 聚合。
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8d-cat-sora", ProviderCode: "gpt", Model: "v8d-sora", Status: "active",
		Mode: "video", Protocols: `["video"]`, ShutdownDate: "2020-01-01"})
	// gpt chat 行：正常在售 → 缺省不过滤时 gpt 在列。
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8d-cat-chat", ProviderCode: "gpt", Model: "v8d-chat", Status: "active",
		Mode: "chat", Protocols: `["chat_completions"]`})
	// 非 active 与目录不可见行不改变聚合。
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8d-cat-disabled", ProviderCode: "gpt", Model: "v8d-disabled", Status: "disabled",
		Mode: "video", Protocols: `["video"]`})
	hidden := 0
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8d-cat-hidden", ProviderCode: "gpt", Model: "v8d-hidden", Status: "active",
		Mode: "video", Protocols: `["video"]`, CatalogVisible: &hidden})
	// glm：custom 全局 video 行 → video 聚合含 glm。
	env.exec(t, `INSERT INTO custom_provider_models (id, provider_code, model, scope, status, mode,
		supported_api_protocols_json, created_by, created_at, updated_at)
		VALUES ('v8d-cu-glm-video', 'glm', 'v8d-glm-video', 'global', 'active', 'video',
		'["video"]', 'seed', ?, ?)`, now, now)
	// ds：仅一条 custom 全局 text 行（写入枚举真实词表）→ BUG-0324 翻译后并入 chat 聚合。
	env.exec(t, `INSERT INTO custom_provider_models (id, provider_code, model, scope, status, mode,
		supported_api_protocols_json, created_by, created_at, updated_at)
		VALUES ('v8d-cu-ds-text', 'ds', 'v8d-ds-text', 'global', 'active', 'text',
		'["chat_completions"]', 'seed', ?, ?)`, now, now)
	// gpt personal video 行：仅管理员显式 systemAccountId 过滤时可见。
	env.exec(t, `INSERT INTO custom_provider_models (id, provider_code, model, scope, status, mode,
		supported_api_protocols_json, system_account_id, created_by, created_at, updated_at)
		VALUES ('v8d-cu-personal', 'gpt', 'v8d-gpt-personal', 'personal', 'active', 'video',
		'["video"]', 'acct-1', 'seed', ?, ?)`, now, now)
	env.login(t, "root", "root-pass", "super_admin")

	codes := func(t *testing.T, query string) []string {
		t.Helper()
		code, body := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options/provider-codes"+query, "")
		if code != http.StatusOK {
			t.Fatalf("provider-codes %q: %d %v", query, code, body)
		}
		payload := body["data"].(map[string]any)
		raw := payload["providerCodes"].([]any)
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			out = append(out, item.(string))
		}
		return out
	}
	equal := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("providerCodes 应为 %v，实得 %v", want, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("providerCodes 应为 %v，实得 %v", want, got)
			}
		}
	}

	// modes=video：gpt 视频行全部下线/禁用/隐藏 → 只剩 glm 的 custom 全局行。
	equal(codes(t, "?modes=video"), "glm")

	// modes=chat：gpt 的在售 chat 行 + ds 的 custom text 行（chat→text 翻译，BUG-0324）。
	equal(codes(t, "?modes=chat"), "ds", "gpt")

	// modes=embedding：custom 词表无映射、内置无 embedding 行 → 空聚合。
	equal(codes(t, "?modes=embedding"))

	// 缺省不过滤：三供应商都有满足谓词的行，去重升序。
	equal(codes(t, ""), "ds", "glm", "gpt")

	// personal 行：不带 systemAccountId（落到调用者身份）不可见。
	equal(codes(t, "?modes=video"), "glm")
	// 管理员显式 systemAccountId 过滤后 gpt 的 personal video 行并入聚合。
	equal(codes(t, "?modes=video&systemAccountId=acct-1"), "glm", "gpt")

	// modes 非法 400，文案与 options 端点同源。
	code, bad := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options/provider-codes?modes=bogus", "")
	if code != http.StatusBadRequest || bad["message"] != "modes 必须是 chat、responses、embedding、audio、video 或 image_generation" {
		t.Fatalf("modes=bogus: %d %v", code, bad)
	}
}
