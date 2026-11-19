package usagewriter

import (
	"encoding/json"
	"testing"
)

// 统一媒体计费维度的冻结面契约（非对话媒体统一计费设计 §6.1/§6.2）：
// HasCostDimension 必须识别新增媒体维度，行项扩展字段必须能无损序列化
// （0 价与 1.0 倍率不得被 omitempty 丢掉）。

func TestHasCostDimensionIncludesMediaDimensions(t *testing.T) {
	one := 1.0
	seconds := 1.0
	chars := int64(1)
	cases := map[string]UsageRecordInput{
		"ttsInputChars":      {TtsInputChars: &chars},
		"audioInputSeconds":  {AudioInputSeconds: &seconds},
		"outputVideoSeconds": {OutputVideoSeconds: &seconds},
		"videoCalls":         {VideoCalls: &one},
	}
	for name, input := range cases {
		if !HasCostDimension(input) {
			t.Fatalf("%s 必须计入计费维度", name)
		}
		if !HasPricingSnapshotFact(input) {
			t.Fatalf("%s 必须触发定价快照事实", name)
		}
	}
	if HasCostDimension(UsageRecordInput{}) {
		t.Fatal("空输入不得计为有计费维度")
	}
}

func TestCostLineItemMediaFieldsJSONRoundTrip(t *testing.T) {
	zero := 0.0
	one := 1.0
	line := CostLineItem{
		Key: "video_output_seconds", Kind: "video_output_seconds",
		Quantity: 2, Unit: "second", UnitSize: 1, UnitPriceUsd: 0, CostUsd: 0,
		Tier: "720p", BaseUnitPriceUsd: &zero, Multiplier: &one, QuantitySource: "upstream",
	}
	encoded, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded CostLineItem
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Tier != "720p" || decoded.QuantitySource != "upstream" {
		t.Fatalf("tier/source 丢失: %s", encoded)
	}
	if decoded.BaseUnitPriceUsd == nil || *decoded.BaseUnitPriceUsd != 0 {
		t.Fatalf("免费 0 价被省略: %s", encoded)
	}
	if decoded.Multiplier == nil || *decoded.Multiplier != 1.0 {
		t.Fatalf("1.0 倍率被省略: %s", encoded)
	}

	// 历史行项（无扩展字段）反序列化保持零值，不受新字段影响。
	var legacy CostLineItem
	if err := json.Unmarshal([]byte(`{"key":"input","kind":"input","label":"输入 Token","quantity":1,"unit":"token","unitSize":1000000,"unitPriceUsd":1,"costUsd":0.000001}`), &legacy); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}
	if legacy.Tier != "" || legacy.BaseUnitPriceUsd != nil || legacy.Multiplier != nil || legacy.QuantitySource != "" {
		t.Fatalf("历史快照扩展字段应为零值: %#v", legacy)
	}
}
