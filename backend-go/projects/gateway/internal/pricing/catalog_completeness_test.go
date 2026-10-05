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
	"strings"
	"testing"
)

var datedModelName = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2})$`)

func allSnapshotRows() map[string][]rawModel {
	// 2026-10-05 全厂商补全批（A1）：MiniMax 新增 chat 行（MiniMax-M3/
	// M2.7/M2.7-highspeed），纳入 chat 行 function_calling 门禁。
	return map[string][]rawModel{
		"openai":     openAIModelPricingData,
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

// catalogModeVocabulary 是 mode 的统一词表：核对批把 xai 图像三行从旧词
// 表 "image" 归一到 "image_generation"（展示投影对存量双轨兼容，新数据不
// 得再落旧词）；"responses" 是 gpt-realtime 等 Responses 专属行的现役值
// （归一裁决登记计划待办，词表先行收敛防新行乱写）。
var catalogModeVocabulary = map[string]bool{
	"chat": true, "audio": true, "video": true,
	"image_generation": true, "embedding": true, "responses": true,
}

func TestCatalogSnapshotModeInVocabulary(t *testing.T) {
	for provider, models := range allSnapshotRows() {
		for _, model := range models {
			if model.Mode != "" && !catalogModeVocabulary[model.Mode] {
				t.Errorf("%s %s Mode = %q, 不在统一词表 %v（防 image 旧词双轨复活，归一裁决见计划待办）", provider, model.Model, model.Mode, catalogModeVocabulary)
			}
		}
	}
}

// TestCatalogSnapshotCNYSourceTraceable 钉住人民币换算裁决的「可溯」要求：
// 声明 CNY 官方价的行必须四件套齐全（币种 + 约定汇率 7.0 + 汇率日期 +
// 来源 note），且 Rate 只允许约定值 7.0；反向地，未声明币种的行（USD
// 明文覆盖行）不得残留换算 Rate/Date 字段——三件套要么删净要么落全，
// 半删即 Source 元信息断链。
func TestCatalogSnapshotCNYSourceTraceable(t *testing.T) {
	for provider, models := range allSnapshotRows() {
		for _, model := range models {
			cny := model.SourcePricingCurrency == "CNY"
			hasRate := model.SourceExchangeRateToUsd != nil
			hasDate := model.SourceExchangeRateDate != ""
			if cny {
				if !hasRate || !hasDate || model.SourcePricingNote == "" {
					t.Errorf("%s %s 声明 CNY 但四件套不齐（rate=%v date=%q note 空=%v）", provider, model.Model, hasRate, model.SourceExchangeRateDate, model.SourcePricingNote == "")
					continue
				}
				if *model.SourceExchangeRateToUsd != 7.0 {
					t.Errorf("%s %s SourceExchangeRateToUsd = %v, 约定汇率唯一值 7.0", provider, model.Model, *model.SourceExchangeRateToUsd)
				}
				if len(model.SourceExchangeRateDate) != 10 || strings.Count(model.SourceExchangeRateDate, "-") != 2 {
					t.Errorf("%s %s SourceExchangeRateDate = %q, 需 YYYY-MM-DD", provider, model.Model, model.SourceExchangeRateDate)
				}
				continue
			}
			if hasRate || hasDate {
				t.Errorf("%s %s 未声明币种但残留换算字段（rate=%v date=%q）——USD 覆盖行三件套须删净", provider, model.Model, hasRate, model.SourceExchangeRateDate)
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
