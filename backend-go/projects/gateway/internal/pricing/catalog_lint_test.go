package pricing

// catalog-lint（计划-20261005T230500000Z 步骤 5）：人工编辑 catalogdata/
// *.json 后的秒级校验入口——`go test ./projects/gateway/internal/pricing/
// -run TestCatalogLint`。三层校验：
//  1. 结构层：provider 键匹配、models 非空、必填 model/mode、未知键检测
//     （手工编辑最常见的拼写错，反射 rawModel 字段集判定）；
//  2. 语义层：mode 统一词表、CNY 行四件套/USD 行无换算残留（与快照门禁
//     同规则，直接作用于 JSON 层，坏数据在加载前暴露）；
//  3. 清单层：输出每供应商行数/总数、shutdown 行、CNY 行数、无价行清单
//     （核对目录完整性时直接读输出，替代逐行翻页）。
import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCatalogLint(t *testing.T) {
	allowedKeys := map[string]bool{}
	value := reflect.ValueOf(rawModel{})
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		allowedKeys[strings.ToLower(name[:1])+name[1:]] = true
	}
	allowedKeys["notes"] = true

	total, lintFailures := 0, 0
	for _, provider := range seedGenProviders {
		raw, err := catalogDataFS.ReadFile("catalogdata/" + provider + ".json")
		if err != nil {
			t.Errorf("%s: %v", provider, err)
			lintFailures++
			continue
		}
		var doc struct {
			Provider string           `json:"provider"`
			Models   []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s: JSON 解析失败: %v", provider, err)
			lintFailures++
			continue
		}
		if doc.Provider != provider {
			t.Errorf("%s: provider 键 = %q", provider, doc.Provider)
			lintFailures++
		}
		if len(doc.Models) == 0 {
			t.Errorf("%s: models 为空", provider)
			lintFailures++
		}
		shutdownRows, cnyRows, unpriced := []string{}, 0, []string{}
		for _, entry := range doc.Models {
			model, _ := entry["model"].(string)
			if model == "" {
				t.Errorf("%s: 存在缺 model 的行", provider)
				lintFailures++
				continue
			}
			for key := range entry {
				if !allowedKeys[key] {
					t.Errorf("%s %s: 未知键 %q（拼写错？合法键见 rawModel 字段）", provider, model, key)
					lintFailures++
				}
			}
			mode, _ := entry["mode"].(string)
			if !catalogModeVocabulary[mode] {
				t.Errorf("%s %s: mode = %q 不在统一词表", provider, model, mode)
				lintFailures++
			}
			currency, _ := entry["sourcePricingCurrency"].(string)
			rate := entry["sourceExchangeRateToUsd"]
			date, _ := entry["sourceExchangeRateDate"].(string)
			note, _ := entry["sourcePricingNote"].(string)
			if currency == "CNY" {
				cnyRows++
				if rate == nil || date == "" || note == "" {
					t.Errorf("%s %s: CNY 行四件套不齐", provider, model)
					lintFailures++
				}
			} else if rate != nil || date != "" {
				t.Errorf("%s %s: 未声明 CNY 却残留换算字段", provider, model)
				lintFailures++
			}
			if shutdown, ok := entry["shutdownDate"].(string); ok && shutdown != "" {
				shutdownRows = append(shutdownRows, model+" ("+shutdown+")")
			}
			if hasDirectPriceFields(entry) {
				// 有价行
			} else {
				unpriced = append(unpriced, model)
			}
			total++
		}
		sort.Strings(shutdownRows)
		sort.Strings(unpriced)
		t.Logf("%s: %d 行 | CNY %d | shutdown: %v | 无价行: %v", provider, len(doc.Models), cnyRows, shutdownRows, unpriced)
	}
	t.Logf("总计 %d 行，%d 处 lint 失败", total, lintFailures)
	if lintFailures > 0 {
		t.Fatalf("catalog-lint 发现 %d 处问题", lintFailures)
	}
}

// hasDirectPriceFields 判定 JSON 行是否含任一直接价字段（与 hasDirectPrice
// 同口径的 JSON 层镜像，含媒体四维）。
func hasDirectPriceFields(entry map[string]any) bool {
	priceKeys := []string{
		"inputCostPerToken", "inputCostPerTokenPriority", "inputCostPerTokenFlex", "inputCostPerTokenBatch",
		"outputCostPerToken", "outputCostPerTokenPriority", "outputCostPerTokenFlex", "outputCostPerTokenBatch",
		"cacheCreationInputTokenCost", "cacheReadInputTokenCost", "cacheStorageInputTokenCostPerHour",
		"inputCostPerImageToken", "outputCostPerImage", "outputCostPerImageToken",
		"inputCostPerAudioToken", "outputCostPerAudioToken", "ttsInputCostPerChar",
		"audioInputCostPerSecond", "videoOutputCostPerSecond", "videoOutputCostPerCall",
	}
	for _, key := range priceKeys {
		if _, ok := entry[key]; ok {
			return true
		}
	}
	return false
}
