package gatewaymedia

// provider_options 测试（契约 §2.1 L3、§2.4 规则 4）：Extract 形态校验与键
// 归一；Merge 的覆盖同名/忽略未命中/嵌套合并/不修改入参。
import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtractProviderOptions(t *testing.T) {
	// 合法：多 provider 子对象 + 键归一（大小写/空白）。
	extracted, err := ExtractProviderOptions(json.RawMessage(
		`{"openai":{"stream":false}," Gemini ":{"aspectRatio":"16:9"}}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(extracted) != 2 {
		t.Fatalf("提取数 = %d, want 2", len(extracted))
	}
	if got := extracted["openai"]["stream"]; got != false {
		t.Fatalf("openai.stream = %v", got)
	}
	if got := extracted["gemini"]["aspectRatio"]; got != "16:9" {
		t.Fatalf("gemini.aspectRatio = %v", got)
	}

	// 空/null：未使用扩展通道是正常形态。
	for _, raw := range []json.RawMessage{nil, []byte(``), []byte(`  `), []byte(`null`)} {
		got, err := ExtractProviderOptions(raw)
		if err != nil || len(got) != 0 {
			t.Fatalf("raw=%q 应为空结果: (%v, %v)", raw, got, err)
		}
	}

	// 顶层非对象。
	if _, err := ExtractProviderOptions(json.RawMessage(`["openai"]`)); err == nil {
		t.Fatal("顶层数组应报错")
	}
	// 子值非对象（契约 §2.1：值 map）。
	if _, err := ExtractProviderOptions(json.RawMessage(`{"openai":true}`)); err == nil {
		t.Fatal("子值非对象应报错")
	}
	if _, err := ExtractProviderOptions(json.RawMessage(`{"openai":[1]}`)); err == nil {
		t.Fatal("子值为数组应报错")
	}
}

func TestMergeProviderOptions(t *testing.T) {
	base := map[string]any{
		"model":  "sora-2",
		"prompt": "a cat",
		"options": map[string]any{
			"timeout": float64(30),
			"nested":  map[string]any{"keep": true, "over": "base"},
		},
	}
	opts := map[string]any{
		"openai": map[string]any{ // 命中子对象
			"prompt": "an overridden cat",   // 同名覆盖（L2 值）
			"options": map[string]any{       // 嵌套合并
				"nested": map[string]any{"over": "vendor"},
			},
			"vendor_extra": "raw", // 未知键原样透传
		},
		"gemini": map[string]any{"aspectRatio": "16:9"}, // 未命中：整体忽略
	}

	merged := MergeProviderOptions(base, "openai", opts)

	if got := merged["model"]; got != "sora-2" {
		t.Fatalf("未涉及键应保留: %v", got)
	}
	if got := merged["prompt"]; got != "an overridden cat" {
		t.Fatalf("同名覆盖 = %v", got)
	}
	options, ok := merged["options"].(map[string]any)
	if !ok {
		t.Fatalf("options 形态 = %T", merged["options"])
	}
	if got := options["timeout"]; got != float64(30) {
		t.Fatalf("嵌套兄弟键应保留: %v", got)
	}
	nested, _ := options["nested"].(map[string]any)
	if nested == nil || nested["keep"] != true || nested["over"] != "vendor" {
		t.Fatalf("嵌套合并结果 = %v", nested)
	}
	if got := merged["vendor_extra"]; got != "raw" {
		t.Fatalf("未知键应透传: %v", got)
	}
	if _, exists := merged["aspectRatio"]; exists {
		t.Fatal("未命中 provider 的键不应进入合并结果")
	}

	// 入参不被修改（纯函数语义）。
	wantBase := map[string]any{
		"model":  "sora-2",
		"prompt": "a cat",
		"options": map[string]any{
			"timeout": float64(30),
			"nested":  map[string]any{"keep": true, "over": "base"},
		},
	}
	if !reflect.DeepEqual(base, wantBase) {
		t.Fatalf("base 被修改: %v", base)
	}

	// 未命中任何 provider：返回 base 副本。
	same := MergeProviderOptions(base, "minimax", opts)
	if got := same["prompt"]; got != "a cat" {
		t.Fatalf("未命中时不应覆盖: %v", got)
	}
	if _, exists := same["aspectRatio"]; exists {
		t.Fatal("未命中时不应引入其它 provider 键")
	}

	// 空 opts / nil base 的边界。
	if got := MergeProviderOptions(nil, "openai", nil); len(got) != 0 {
		t.Fatalf("nil 输入结果 = %v", got)
	}
	// providerCode 归一匹配（大小写不敏感）。
	normalized := MergeProviderOptions(base, " OpenAI ", opts)
	if got := normalized["prompt"]; got != "an overridden cat" {
		t.Fatalf("归一匹配失败: %v", got)
	}
	// 命中子对象非对象值：忽略不报错。
	weird := MergeProviderOptions(base, "openai", map[string]any{"openai": "not-a-map"})
	if got := weird["prompt"]; got != "a cat" {
		t.Fatalf("非法子对象不应生效: %v", got)
	}
}

// TestProviderOptionAliasMatching M2 复审 Minor-1 观察项 2：匹配键同时认
// 账户 provider_code 原键（gpt——openai 的 OAuth 子供应商）与 adapter 注册
// 键（openai）：gpt 账户的 {"gpt":{...}} 与 {"openai":{...}} 同样生效
//（providerFamilyAlias 在归一映射处折叠子供应商代码）。
func TestProviderOptionAliasMatching(t *testing.T) {
	base := map[string]any{"model": "sora-2", "prompt": "a cat"}
	merged := MergeProviderOptions(base, "openai", map[string]any{
		"gpt":    map[string]any{"prompt": "via gpt key"},
		"gemini": map[string]any{"aspectRatio": "16:9"},
	})
	if got := merged["prompt"]; got != "via gpt key" {
		t.Fatalf("gpt 原键应命中 openai adapter: %v", got)
	}
	if _, exists := merged["aspectRatio"]; exists {
		t.Fatal("未命中 provider 的键不应进入合并结果")
	}
	// 对称性：providerCode 侧与键侧同一归一（merge 生产面恒以 adapter 注册
	// 键调用，此处钉住双侧折叠的一致性）。
	mergedReverse := MergeProviderOptions(base, "gpt", map[string]any{
		"openai": map[string]any{"prompt": "via openai key"},
	})
	if got := mergedReverse["prompt"]; got != "via openai key" {
		t.Fatalf("openai 键应命中 gpt 归一: %v", got)
	}
}

// TestAppliedProviderOptionKeys M2 复审 Minor-1 回显面（契约 §2.4 规则 4）：
// 命中子对象键名摘要（字典序、不含值）、与 merge 同源匹配（gpt 别名）、
// 未命中/非对象/空输入返回 nil。
func TestAppliedProviderOptionKeys(t *testing.T) {
	opts := map[string]any{
		"openai": map[string]any{"zeta": 1, "alpha": map[string]any{"k": true}},
		"gemini": map[string]any{"aspectRatio": "16:9"},
	}
	got := AppliedProviderOptionKeys("openai", opts)
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied keys = %v, want %v（字典序、不含值）", got, want)
	}
	// gpt 原键同源命中（与 MergeProviderOptions 共用匹配谓词）。
	if got := AppliedProviderOptionKeys("openai", map[string]any{"gpt": map[string]any{"vendor_flag": true}}); len(got) != 1 || got[0] != "vendor_flag" {
		t.Fatalf("gpt 原键 applied = %v, want [vendor_flag]", got)
	}
	if got := AppliedProviderOptionKeys("minimax", opts); got != nil {
		t.Fatalf("未命中应返回 nil: %v", got)
	}
	if got := AppliedProviderOptionKeys("openai", map[string]any{"openai": "not-a-map"}); got != nil {
		t.Fatalf("非对象子对象应返回 nil: %v", got)
	}
	if got := AppliedProviderOptionKeys("openai", nil); got != nil {
		t.Fatalf("空输入应返回 nil: %v", got)
	}
}
