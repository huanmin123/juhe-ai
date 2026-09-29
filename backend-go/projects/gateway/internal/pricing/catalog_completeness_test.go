package pricing

// 目录快照完整性门禁（2026-09-29 全列空值盘点后固化）：
//  1. 全部供应商 chat 模式行必须声明 function_calling（BUG-0231 口径；
//     image/embedding/audio 等非对话模式豁免——它们不进主对话、不注入工具）。
//  2. anthropic 构造器行必须带 Mode（空 Mode 会让 DB 目录 mode 列空，
//     与其他供应商「chat」口径不一致）。
//  3. 模型名自带 YYYY-MM-DD 后缀的行必须带同值 ReleaseDate（dated 快照行的
//     日期可由名字完全佐证，缺即漏写）。
import (
	"regexp"
	"testing"
)

var datedModelName = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2})$`)

func allSnapshotRows() map[string][]rawModel {
	return map[string][]rawModel{
		"openai":    openAIModelPricingData,
		"deepseek":  deepSeekModelPricingData,
		"glm":       glmModelPricingData,
		"anthropic": anthropicModelPricingData,
		"gemini":    geminiModelPricingData,
		"xai":       xAIModelPricingData,
	}
}

func TestCatalogSnapshotChatRowsDeclareFunctionCalling(t *testing.T) {
	for provider, models := range allSnapshotRows() {
		for _, model := range models {
			if model.Mode != "" && model.Mode != "chat" {
				continue
			}
			declared := false
			for _, tools := range model.SupportedToolsByProtocol {
				for _, tool := range tools {
					if tool == "function_calling" {
						declared = true
					}
				}
			}
			if !declared {
				t.Errorf("%s %s tools = %v, chat 行缺 function_calling (BUG-0231)", provider, model.Model, model.SupportedToolsByProtocol)
			}
		}
	}
}

func TestCatalogSnapshotModeFilled(t *testing.T) {
	for provider, models := range allSnapshotRows() {
		for _, model := range models {
			if model.Mode == "" {
				t.Errorf("%s %s Mode 为空（DB 目录 mode 列将落空值）", provider, model.Model)
			}
		}
	}
}

func TestCatalogSnapshotDatedNamesCarryReleaseDate(t *testing.T) {
	for provider, models := range allSnapshotRows() {
		for _, model := range models {
			m := datedModelName.FindStringSubmatch(model.Model)
			if m == nil {
				continue
			}
			if model.ReleaseDate != m[1] {
				t.Errorf("%s %s 名字含日期 %s 但 ReleaseDate = %q", provider, model.Model, m[1], model.ReleaseDate)
			}
		}
	}
}
