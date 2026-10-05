package pricing

// catalogdata 导出与等价校验（计划-20261005T230500000Z 迁移步骤 1-2）：
//   - TestExportCatalogData：受环境变量 JUHE_AI_EXPORT_CATALOGDATA=1 触发，
//     把九个快照变量反射序列化写入 catalogdata/*.json（一次性迁移工具，
//     未来数据修复后可重跑再生成校验基线）。
//   - TestCatalogDataEquivalence：加载 catalogdata/*.json 与原 Go 快照
//     逐模型 reflect.DeepEqual——不等价不切换（迁移步骤 2 的兜底门禁）。
import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func catalogSnapshotVars() map[string][]rawModel {
	return map[string][]rawModel{
		"gpt":        openAIModelPricingData,
		"deepseek":   deepSeekModelPricingData,
		"glm":        glmModelPricingData,
		"anthropic":  anthropicModelPricingData,
		"gemini":     geminiModelPricingData,
		"xai":        xAIModelPricingData,
		"minimax":    minimaxModelPricingData,
		"volcengine": volcengineModelPricingData,
		"qwen":       qwenModelPricingData,
	}
}

func TestExportCatalogData(t *testing.T) {
	if os.Getenv("JUHE_AI_EXPORT_CATALOGDATA") != "1" {
		t.Skip("set JUHE_AI_EXPORT_CATALOGDATA=1 to regenerate catalogdata/*.json")
	}
	for provider, models := range catalogSnapshotVars() {
		entries := make([]map[string]any, 0, len(models))
		for _, model := range models {
			entries = append(entries, rawModelToJSON(model))
		}
		doc := map[string]any{"provider": provider, "models": entries}
		encoded, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		target := filepath.Join("catalogdata", provider+".json")
		if err := os.WriteFile(target, append(encoded, '\n'), 0o644); err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		t.Logf("wrote %s (%d models)", target, len(entries))
	}
}

func TestCatalogDataEquivalence(t *testing.T) {
	for provider, expected := range catalogSnapshotVars() {
		loaded, err := loadProviderCatalogModels(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		byModel := map[string]rawModel{}
		for _, model := range expected {
			byModel[model.Model] = model
		}
		if len(loaded) != len(expected) {
			t.Fatalf("%s: loaded %d models, want %d", provider, len(loaded), len(expected))
		}
		for _, got := range loaded {
			want, ok := byModel[got.Model]
			if !ok {
				t.Fatalf("%s: loaded model %q 不在 Go 快照中", provider, got.Model)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s %s: JSON 加载值与 Go 快照不等价\n got %+v\nwant %+v", provider, got.Model, got, want)
			}
		}
	}
}
