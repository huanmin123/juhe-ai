// provider 默认支持模型 × 种子模型目录 不变量守护。
//
// 不变量（运行时可见语义）：每个内置 provider 种子（pgSeedProviders，
// SQLite 与 PostgreSQL 种子共用同一份）的 default_supported_models 里列出
// 的每个模型，都必须能在种子模型目录数据（model_catalog_data.go 的
// modelCatalogSeedRows）中以「运行时可见」形态命中。运行时可见 = 目录行
// status 为 active + catalog_visible 为真 + shutdown_date 未到。
//
// 与 gateway 运行时读取规则的对应关系
// （backend-go/projects/gateway/internal/providers/catalog.go）：
//   - 可见性过滤对应 listBuiltInCatalogModels 的谓词
//     status='active' AND catalog_visible AND (shutdown_date IS NULL OR '' OR
//     shutdown_date > today)。种子 upsert 把 status 固定为 'active' 字面量
//     （buildPostgresModelSeedUpsert 及 SQLite 对应实现），modelCatalogSeedRow
//     因此没有 status 字段；shutdown 过滤复用包内 activeModelCatalogSeedRows
//     （与 Node hasModelShutdown 同一方向），catalog_visible 按字段判断。
//   - provider 展开集合对应 ModelCatalogSourceProviderCodes：
//     openai 兼容 provider 展开为其 openai 协议子 provider + 自身，hybrid
//     展开为全部内置协议 provider，其余 provider 展开为自身；内置种子形态
//     下 openai 目标还会经 modelCatalogBuiltInSourceProviderCodes 丢弃
//     openai 自身（内置目录没有 openai 行）。
//     providerDefaultsCatalogSourceCodes 按内置种子数据复刻该展开。
//
// 背景：真实实例曾出现 openai 默认列表中的 gpt-image-2 不在可用目录
// （2026-09-20 专项审计）。本测试锁定「默认列表 ⊆ 可见目录」：目录数据
// 人工同步（源自 gateway pricing catalog）或默认列表调整破坏不变量时，
// 先修数据再合入，不允许带着缺口出库。
//
// 既有 model_catalog_seed_test.go 锁定目录行数/排序/ID 形态，不覆盖本
// 跨数据集不变量；二者互补，无重复。

package schema

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// providerDefaultsCatalogBuiltinCodes 返回内置目录数据中出现过的 provider
// code（按 DEFAULT_PROVIDER_SEEDS 的目录行顺序），对应 gateway hybrid 展开
// 在内置种子形态下的全集（gpt/xai/deepseek/anthropic/gemini/glm）。
func providerDefaultsCatalogBuiltinCodes() []string {
	seen := map[string]bool{}
	codes := []string{}
	for _, row := range modelCatalogSeedRows {
		if !seen[row.ProviderCode] {
			seen[row.ProviderCode] = true
			codes = append(codes, row.ProviderCode)
		}
	}
	return codes
}

// providerDefaultsCatalogSourceCodes 复刻 gateway
// ModelCatalogSourceProviderCodes 在「零自定义数据 + 内置种子」形态下的
// provider 展开集合（见文件头注释）：openai 兼容 provider 展开为
// ParentCode=openai 且启用的子 provider + 自身（内置目录无 openai 行，与
// modelCatalogBuiltInSourceProviderCodes 丢弃 openai 自身等效）；hybrid 展开
// 为全部内置目录 provider；其余 provider 展开为自身。
func providerDefaultsCatalogSourceCodes(code string) []string {
	normalized := strings.ToLower(strings.TrimSpace(code))
	if normalized == "" {
		return nil
	}
	if normalized == "hybrid" {
		return providerDefaultsCatalogBuiltinCodes()
	}
	if normalized == "openai" {
		codes := []string{}
		for _, provider := range pgSeedProviders {
			if provider.ParentCode == "openai" && provider.Enabled == 1 {
				codes = append(codes, provider.Code)
			}
		}
		return append(codes, normalized)
	}
	return []string{normalized}
}

// TestProviderDefaultsModelsSeededInVisibleCatalog 锁定不变量：每个启用
// provider 的默认支持模型，都能在种子目录数据中以运行时可见形态命中。
func TestProviderDefaultsModelsSeededInVisibleCatalog(t *testing.T) {
	// 基准日与种子代码一致：seedTimestamp 的 UTC YYYY-MM-DD 前缀。
	today := time.Now().UTC().Format("2006-01-02")
	// 运行时可见目录行集合：status 由 upsert 固定 'active'（无字段可判），
	// shutdown 过滤复用 activeModelCatalogSeedRows，catalog_visible 按字段。
	visible := map[string]map[string]bool{}
	for _, row := range activeModelCatalogSeedRows(today) {
		if !row.CatalogVisible {
			continue
		}
		if visible[row.ProviderCode] == nil {
			visible[row.ProviderCode] = map[string]bool{}
		}
		visible[row.ProviderCode][row.Model] = true
	}
	if len(visible) == 0 {
		t.Fatalf("种子目录数据在 %s 之后没有任何可见行：目录快照疑似被清空，先恢复 model_catalog_data.go 再合入", today)
	}
	for _, provider := range pgSeedProviders {
		if provider.Enabled != 1 {
			continue
		}
		var models []string
		if err := json.Unmarshal([]byte(provider.DefaultSupportedModelsJSON), &models); err != nil {
			t.Errorf("provider %s 的 default_supported_models_json 解析失败: %v", provider.Code, err)
			continue
		}
		if len(models) == 0 {
			t.Errorf("provider %s 的 default_supported_models 为空：新建账户将没有默认可选模型，用户在账户创建页看不到默认模型列表", provider.Code)
			continue
		}
		seenModels := map[string]bool{}
		sources := providerDefaultsCatalogSourceCodes(provider.Code)
		for _, model := range models {
			if strings.TrimSpace(model) == "" {
				t.Errorf("provider %s 的 default_supported_models 含空模型名", provider.Code)
				continue
			}
			if seenModels[model] {
				t.Errorf("provider %s 的 default_supported_models 含重复模型 %q", provider.Code, model)
				continue
			}
			seenModels[model] = true
			hit := false
			for _, source := range sources {
				if visible[source][model] {
					hit = true
					break
				}
			}
			if !hit {
				t.Errorf("provider %s 默认支持模型 %q 无法在种子模型目录中以运行时可见形态命中（展开源 %v，基准日 %s）：该模型会出现在 provider 默认列表但模型目录校验与目录页均不可见（真实实例历史案例：openai 默认列表中的 gpt-image-2）。请同步修复种子目录数据 model_catalog_data.go 或 provider 默认列表 pg_schema.go DEFAULT_PROVIDER_SEEDS，再合入。",
					provider.Code, model, sources, today)
			}
		}
	}
}
