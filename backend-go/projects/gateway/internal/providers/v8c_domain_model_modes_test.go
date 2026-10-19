package providers

import (
	"net/http"
	"testing"
)

// v8-C 域模型选择器定向测试 + BUG-0324 回归（models 单表形态）：GET
// /providers/models/options 的 `modes` 查询参数按目录 mode 词表（六值）白名单
// 校验，非法值 400；命中时展开成 25 值 endpoint mode 集与
// supported_endpoint_modes_json 做 json 交集（单表后 builtin 与 custom 行共用
// 同一词表与同一过滤，旧 customVocabularyModes 的 text/image 翻译层随双表退
// 役）；响应行携带逆映射派生的六值 mode；缺省不过滤（既有调用方零破坏）。
// 旧「无 mode 行」（custom_provider_models.mode NULL）在单表下不复存在——协议
// 列表必产出 modes 集，显式标注行语义与 chat 面一致。

func TestV8CModelOptionsDomainModes(t *testing.T) {
	env := newTestEnv(t)
	const now = "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO vendors (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt', 'gpt', 'OpenAI', 1, '[]', ?, ?)`, now, now)
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-chat", ProviderCode: "gpt", Model: "v8c-chat-model", Status: "active",
		Protocols: `["chat_completions"]`})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-video", ProviderCode: "gpt", Model: "v8c-video-model", Status: "active",
		Protocols: `["video"]`})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-image", ProviderCode: "gpt", Model: "v8c-image-model", Status: "active",
		Protocols: `["images"]`})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "v8c-cat-nomode", ProviderCode: "gpt", Model: "v8c-nomode-model", Status: "active",
		Protocols: `["chat_completions"]`})
	// custom 行：chat 域（protocols chat_completions）、image 域（protocols
	// images）、个人归属 chat 域、以及一份与 v8c-cat-nomode 同 modes 的全局行。
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_by, created_at, updated_at)
		VALUES ('v8c-cu-chat', 'gpt', 'v8c-custom-chat', 'v8c-custom-chat', 'global', 'active',
		'["chat_json","chat_sse"]', 'seed', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_by, created_at, updated_at)
		VALUES ('v8c-cu-image', 'gpt', 'v8c-custom-image', 'v8c-custom-image', 'global', 'active',
		'["images_json"]', 'seed', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, created_by, created_at, updated_at)
		VALUES ('v8c-cu-personal', 'gpt', 'v8c-custom-personal', 'v8c-custom-personal', 'personal', 'acct-1', 'active',
		'["chat_json","chat_sse"]', 'seed', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_by, created_at, updated_at)
		VALUES ('v8c-cu-nomode', 'gpt', 'v8c-custom-nomode', 'v8c-custom-nomode', 'global', 'active',
		'["chat_json","chat_sse"]', 'seed', ?, ?)`, now, now)
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
	sameIDs := func(left []string, right []string) bool {
		if len(left) != len(right) {
			return false
		}
		for _, item := range left {
			if !contains(right, item) {
				return false
			}
		}
		return true
	}

	// 缺省不过滤（modes 语义）：既有调用方（同步候选等）零破坏。personal 行
	// 因归属过滤（root 身份 ≠ acct-1）不在列，与 modes 无关。
	allIDs := optionIDs(t, "")
	if len(allIDs) != 7 {
		t.Fatalf("缺省必须只按归属过滤（4 内置 + 3 全局自定义）：%v", allIDs)
	}

	// modes=chat：内置 chat 两行 + custom chat 两行（同名 modes 集合行同样命中）；
	// personal 行未显式归属不过滤。
	chatIDs := optionIDs(t, "&modes=chat")
	if len(chatIDs) != 4 || !contains(chatIDs, "v8c-chat-model") || !contains(chatIDs, "v8c-nomode-model") ||
		!contains(chatIDs, "v8c-custom-chat") || !contains(chatIDs, "v8c-custom-nomode") {
		t.Fatalf("modes=chat 应返回内置 chat 行 + custom chat 行：%v", chatIDs)
	}

	// BUG-0324 核心：personal custom chat 行在管理员显式归属过滤下进入 chat 域候选。
	scopedIDs := optionIDs(t, "&modes=chat&systemAccountId=acct-1")
	if len(scopedIDs) != 5 || !contains(scopedIDs, "v8c-custom-personal") {
		t.Fatalf("modes=chat+归属过滤应含 personal custom chat 行：%v", scopedIDs)
	}

	// 逗号分隔多值与重复参数等价。
	multiIDs := optionIDs(t, "&modes=chat,video")
	if len(multiIDs) != 5 || !contains(multiIDs, "v8c-video-model") || !contains(multiIDs, "v8c-custom-chat") {
		t.Fatalf("modes=chat,video 应含 video 行与 custom chat 行：%v", multiIDs)
	}
	repeatIDs := optionIDs(t, "&modes=chat&modes=video")
	if len(repeatIDs) != len(multiIDs) {
		t.Fatalf("modes 重复参数应与逗号分隔等价：%v vs %v", repeatIDs, multiIDs)
	}

	// image_generation 白名单展开为 images_json：内置 + custom image 行。
	imageIDs := optionIDs(t, "&modes=image_generation")
	if len(imageIDs) != 2 || !contains(imageIDs, "v8c-image-model") || !contains(imageIDs, "v8c-custom-image") {
		t.Fatalf("modes=image_generation 应返回内置与 custom image 行：%v", imageIDs)
	}

	// embedding 展开集（message_token_counting/count_tokens/embed_content）无行命中 → 空。
	embeddingIDs := optionIDs(t, "&modes=embedding")
	if len(embeddingIDs) != 0 {
		t.Fatalf("modes=embedding 不应返回任何行：%v", embeddingIDs)
	}

	// 大小写不敏感（词表全小写，入参归一化后校验）。
	upperIDs := optionIDs(t, "&modes=CHAT")
	if !sameIDs(upperIDs, chatIDs) {
		t.Fatalf("modes=CHAT 应等价 modes=chat：%v", upperIDs)
	}

	// 空值（含纯逗号）不过滤。
	emptyIDs := optionIDs(t, "&modes=")
	if len(emptyIDs) != 7 {
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

	// 响应行携带逆映射派生的六值 mode（单表后 custom 行不再有四值存储原值）。
	code, body := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&modes=chat", "")
	if code != http.StatusOK {
		t.Fatalf("mode field fetch: %d %v", code, body)
	}
	rows := dataArray(t, body)
	if len(rows) != 4 {
		t.Fatalf("modes=chat 应恰返回 4 行：%v", rows)
	}
	for _, row := range rows {
		item := row.(map[string]any)
		switch item["id"] {
		case "v8c-chat-model", "v8c-nomode-model", "v8c-custom-chat", "v8c-custom-nomode":
			if item["mode"] != "chat" {
				t.Fatalf("响应行应携带派生 mode=chat：%v", item)
			}
		default:
			t.Fatalf("modes=chat 不应返回其他行：%v", item)
		}
	}
}
