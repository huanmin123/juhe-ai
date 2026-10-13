package providers

import (
	"net/http"
	"testing"
)

// v8-C 域模型选择器根治定向测试：GET /providers/models/options 的 `modes`
// 查询参数按目录 mode 词表（六值）白名单校验，非法值 400；命中时 built-in 与
// custom 两条 SQL 都加 mode IN (...) 过滤（custom 空 mode 行自然排除）；响应行
// 携带目录 mode 字段；缺省不过滤（既有调用方零破坏）。
func TestV8CModelOptionsDomainModes(t *testing.T) {
	env := newTestEnv(t)
	const now = "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt', 'gpt', 'OpenAI', 1, '[]', ?, ?)`, now, now)
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-chat", ProviderCode: "gpt", Model: "v8c-chat-model", Status: "active",
		Mode: "chat", Protocols: `["chat_completions"]`})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-video", ProviderCode: "gpt", Model: "v8c-video-model", Status: "active",
		Mode: "video", Protocols: `["video"]`})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-image", ProviderCode: "gpt", Model: "v8c-image-model", Status: "active",
		Mode: "image_generation", Protocols: `["images"]`})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-nomode", ProviderCode: "gpt", Model: "v8c-nomode-model", Status: "active",
		Protocols: `["chat_completions"]`})
	// custom 行：一条声明 mode=chat，一条 mode 为空（域过滤下自然排除）。
	env.exec(t, `INSERT INTO custom_provider_models (id, provider_code, model, scope, status, mode,
		supported_api_protocols_json, created_by, created_at, updated_at)
		VALUES ('v8c-cu-chat', 'gpt', 'v8c-custom-chat', 'global', 'active', 'chat',
		'["chat_completions"]', 'seed', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO custom_provider_models (id, provider_code, model, scope, status,
		supported_api_protocols_json, created_by, created_at, updated_at)
		VALUES ('v8c-cu-nomode', 'gpt', 'v8c-custom-nomode', 'global', 'active',
		'["chat_completions"]', 'seed', ?, ?)`, now, now)
	env.login(t, "root", "root-pass", "super_admin")

	optionIDs := func(t *testing.T, query string) []string {
		t.Helper()
		code, body := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt"+query, "")
		if code != http.StatusOK {
			t.Fatalf("options %q: %d %v", query, code, body)
		}
		rows := dataArray(t, body)
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.(map[string]any)["id"].(string))
		}
		return ids
	}
	contains := func(list []string, want string) bool {
		for _, item := range list {
			if item == want {
				return true
			}
		}
		return false
	}

	// 缺省不过滤：既有调用方（同步候选等）零破坏。
	allIDs := optionIDs(t, "")
	if len(allIDs) != 6 {
		t.Fatalf("缺省必须不过滤（4 内置 + 2 自定义）：%v", allIDs)
	}

	// modes=chat：SQL mode IN 过滤，custom 空 mode 行自然排除。
	chatIDs := optionIDs(t, "&modes=chat")
	if len(chatIDs) != 2 || !contains(chatIDs, "v8c-chat-model") || !contains(chatIDs, "v8c-custom-chat") {
		t.Fatalf("modes=chat 应只返回 chat 目录行：%v", chatIDs)
	}

	// 逗号分隔多值与重复参数等价。
	multiIDs := optionIDs(t, "&modes=chat,video")
	if len(multiIDs) != 3 || !contains(multiIDs, "v8c-video-model") {
		t.Fatalf("modes=chat,video 应含 video 行：%v", multiIDs)
	}
	repeatIDs := optionIDs(t, "&modes=chat&modes=video")
	if len(repeatIDs) != len(multiIDs) {
		t.Fatalf("modes 重复参数应与逗号分隔等价：%v vs %v", repeatIDs, multiIDs)
	}

	// 大小写不敏感（词表全小写，入参归一化后校验）。
	upperIDs := optionIDs(t, "&modes=CHAT")
	if len(upperIDs) != len(chatIDs) {
		t.Fatalf("modes=CHAT 应等价 modes=chat：%v", upperIDs)
	}

	// 空值（含纯逗号）不过滤。
	emptyIDs := optionIDs(t, "&modes=")
	if len(emptyIDs) != 6 {
		t.Fatalf("modes= 空值不过滤：%v", emptyIDs)
	}

	// 非法值 400，携带词表白名单报错文案。
	code, bad := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&modes=bogus", "")
	if code != http.StatusBadRequest || bad["message"] != "modes 必须是 chat、responses、embedding、audio、video 或 image_generation" {
		t.Fatalf("modes=bogus: %d %v", code, bad)
	}
	code, bad = env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&modes=chat%2Cbad", "")
	if code != http.StatusBadRequest {
		t.Fatalf("modes=chat,bad: %d %v", code, bad)
	}

	// 响应行携带目录 mode 字段（custom 空 mode 行省略该字段）。
	code, body := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&modes=chat", "")
	if code != http.StatusOK {
		t.Fatalf("mode field fetch: %d %v", code, body)
	}
	rows := dataArray(t, body)
	for _, row := range rows {
		item := row.(map[string]any)
		switch item["id"] {
		case "v8c-chat-model", "v8c-custom-chat":
			if item["mode"] != "chat" {
				t.Fatalf("响应行应携带 mode=chat：%v", item)
			}
		default:
			t.Fatalf("modes=chat 不应返回其他行：%v", item)
		}
	}
}
