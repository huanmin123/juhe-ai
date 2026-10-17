// vendor 默认支持模型 × 种子模型目录 不变量守护。
//
// 不变量（运行时可见语义）：每个内置 vendor 种子（pgSeedProviders，SQLite 与
// PostgreSQL 种子共用同一份）的 default_supported_models 里列出的每个模型，
// 都必须能在种子模型目录数据（model_catalog_data_gen.go 的
// modelCatalogSeedRows）中以「运行时可见」形态命中。运行时可见 = models 表
// 行 status 为 active + catalog_visible 为真 + shutdown_date 未到。
//
// 与 gateway 运行时读取规则的对应关系（旧 providers/catalog.go 语义，目标
// 形态读取 models 表）：可见性谓词 status='active' AND catalog_visible AND
// (shutdown_date IS NULL OR '' OR shutdown_date > today)。models 收敛后
// status 由投影在写入时判定（modelSeedRowStatus：shutdown_date 已过写
// 'disabled'），本测试用同一判定复刻可见集合；catalog_visible 按字段判断。
//
// 目标形态下无供应商展开（openai/hybrid 行已退役，vendors 恰为 §1.1 的 9 个
// 真实厂商）：每个 vendor 的默认列表只在其自身目录行中命中。
//
// 背景：真实实例曾出现默认列表中的 gpt-image-2 不在可用目录（2026-09-20
// 专项审计）。本测试锁定「默认列表 ⊆ 可见目录」：目录数据再生成或默认列表
// 调整破坏不变量时，先修数据再合入，不允许带着缺口出库。
//
// 既有 model_catalog_seed_test.go 锁定目录行数/排序/ID 形态与 mode 推导
// 分布，不覆盖本跨数据集不变量；二者互补，无重复。

package schema

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestProviderDefaultsModelsSeededInVisibleCatalog 锁定不变量：每个启用
// vendor 的默认支持模型，都能在种子目录数据中以运行时可见形态命中。
func TestProviderDefaultsModelsSeededInVisibleCatalog(t *testing.T) {
	// 基准日与种子代码一致：seedTimestamp 的 UTC YYYY-MM-DD 前缀。
	today := time.Now().UTC().Format("2006-01-02")
	// 运行时可见目录行集合：status 用与 seed 相同的 modelSeedRowStatus 判定，
	// catalog_visible 按字段。
	visible := map[string]map[string]bool{}
	for _, row := range modelCatalogSeedRows {
		if modelSeedRowStatus(row, today) != modelSeedStatusActive {
			continue
		}
		if !row.CatalogVisible {
			continue
		}
		if visible[row.ProviderCode] == nil {
			visible[row.ProviderCode] = map[string]bool{}
		}
		visible[row.ProviderCode][row.Model] = true
	}
	if len(visible) == 0 {
		t.Fatalf("种子目录数据在 %s 之后没有任何可见行：目录快照疑似被清空，先恢复 model_catalog_data_gen.go 再合入", today)
	}
	for _, vendor := range pgSeedProviders {
		if vendor.Enabled != 1 {
			continue
		}
		var models []string
		if err := json.Unmarshal([]byte(vendor.DefaultSupportedModelsJSON), &models); err != nil {
			t.Errorf("vendor %s 的 default_supported_models_json 解析失败: %v", vendor.Code, err)
			continue
		}
		if len(models) == 0 {
			t.Errorf("vendor %s 的 default_supported_models 为空：新建账户将没有默认可选模型，用户在账户创建页看不到默认模型列表", vendor.Code)
			continue
		}
		seenModels := map[string]bool{}
		for _, model := range models {
			if strings.TrimSpace(model) == "" {
				t.Errorf("vendor %s 的 default_supported_models 含空模型名", vendor.Code)
				continue
			}
			if seenModels[model] {
				t.Errorf("vendor %s 的 default_supported_models 含重复模型 %q", vendor.Code, model)
				continue
			}
			seenModels[model] = true
			if !visible[vendor.Code][model] {
				t.Errorf("vendor %s 默认支持模型 %q 无法在种子模型目录中以运行时可见形态命中（基准日 %s）：该模型会出现在 vendor 默认列表但模型目录校验与目录页均不可见（真实实例历史案例：默认列表中的 gpt-image-2）。请同步修复种子目录数据 model_catalog_data_gen.go 或 vendor 默认列表 pg_schema.go pgSeedProviders，再合入。",
					vendor.Code, model, today)
			}
		}
	}
}
