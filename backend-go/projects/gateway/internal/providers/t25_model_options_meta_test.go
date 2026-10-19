package providers

import (
	"fmt"
	"net/http"
	"testing"
)

// T25 截断提示元数据定向测试：GET /providers/models/options 的 envelope data
// 保持数组（既有调用方零破坏），顶层附加 total（当前筛选条件下的可见候选总
// 数，limit 截断前）与 hasMore（total > 返回条数）。前端以该元数据替代"浏览
// 取数达 50 条"的条数推断；selectedIds 行全部可见（返回条数可超 limit），
// hasMore 因此必须按 total 与实际返回条数比较。

func TestT25ModelOptionsMetaEnvelope(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, `INSERT INTO vendors (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt', 'gpt', 'OpenAI', 1, '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	// 75 个候选：默认 limit 50 下必截断。
	for index := 1; index <= 75; index++ {
		env.seedBuiltinModel(t, builtinModelSeed{ID: fmt.Sprintf("t25-cat-%03d", index), ProviderCode: "gpt",
			Model: fmt.Sprintf("t25-model-%03d", index), Protocols: `["chat_completions"]`})
	}
	env.login(t, "root", "root-pass", "super_admin")

	code, body := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt", "")
	if code != http.StatusOK {
		t.Fatalf("options: %d %v", code, body)
	}
	rows := dataArray(t, body)
	if len(rows) != 50 {
		t.Fatalf("默认窗口应返回 50 条，got %d", len(rows))
	}
	if body["total"] != float64(75) {
		t.Fatalf("total 应为截断前候选总数 75，got %v", body["total"])
	}
	if body["hasMore"] != true {
		t.Fatalf("hasMore 应为 true，got %v", body["hasMore"])
	}

	// 未截断：total == 返回条数，hasMore=false。
	code, body = env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&keyword=t25-model-07", "")
	if code != http.StatusOK {
		t.Fatalf("keyword options: %d %v", code, body)
	}
	rows = dataArray(t, body)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.(map[string]any)["id"].(string))
	}
	// 候选 001..075，keyword=t25-model-07 命中 070..075 共 6 行（未截断）。
	if len(rows) != 6 {
		t.Fatalf("keyword 窗口应返回 6 条，got %d: %v", len(rows), ids)
	}
	if body["total"] != float64(6) || body["hasMore"] != false {
		t.Fatalf("keyword total/hasMore 应为 6/false，got %v/%v", body["total"], body["hasMore"])
	}

	// keyword 搜索下命中行超过 limit：total 保持筛选后全集，hasMore=true。
	// keyword=t25-model-0 命中全部 75 行。
	code, body = env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&keyword=t25-model-0&limit=5", "")
	if code != http.StatusOK {
		t.Fatalf("limited keyword options: %d %v", code, body)
	}
	if rows = dataArray(t, body); len(rows) != 5 {
		t.Fatalf("limit=5 应只返回 5 条，got %d", len(rows))
	}
	if body["total"] != float64(75) || body["hasMore"] != true {
		t.Fatalf("t25-model-0 前缀命中 75 行，total/hasMore 应为 75/true，got %v/%v", body["total"], body["hasMore"])
	}

	// selectedIds 行（keyword 之外）全部可见；hasMore 按 total 与实际返回条数
	// 比较：8 候选（6 keyword 命中 + 2 selected）返回 5 条（3 窗口 + 2 selected）。
	selected := "t25-model-001,t25-model-002"
	code, body = env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&keyword=t25-model-07&limit=3&selectedIds="+selected, "")
	if code != http.StatusOK {
		t.Fatalf("selected options: %d %v", code, body)
	}
	if rows = dataArray(t, body); len(rows) != 5 {
		t.Fatalf("keyword 6 行 + 2 selected 在 limit=3 下应返回 5 条，got %d", len(rows))
	}
	if body["total"] != float64(8) || body["hasMore"] != true {
		t.Fatalf("selected 场景 total/hasMore 应为 8/true（8 候选仅返回 5），got %v/%v", body["total"], body["hasMore"])
	}
}
