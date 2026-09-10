package gatewayanthropic

import (
	"encoding/json"
	"math"
	"testing"
)

// waParseJSONObject 把 JSON 文本解析为 map（helper 集中错误处理）。
func waParseJSONObject(t *testing.T, text string) map[string]any {
	t.Helper()
	value, err := decodeJSONIntoMap(text)
	if err != nil {
		t.Fatalf("解析测试 JSON 失败: %v（原文 %q）", err, text)
	}
	return value
}

// waAssertToken 校验可选 token 数值。
func waAssertToken(t *testing.T, value *int, want int, label string) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s：得到 nil，期望 %d", label, want)
	}
	if *value != want {
		t.Fatalf("%s = %d，期望 %d", label, *value, want)
	}
}

// Anthropic usage 提取契约：input/output/cache_read/cache_creation（含
// cache_creation 细分 5m+1h 求和兜底）与 output_tokens_details.thinking_tokens；
// speed 字段作为 service tier（小写 token 才有效）。
func TestWAExtractUsage(t *testing.T) {
	t.Run("完整字段含 cache_creation 细分", func(t *testing.T) {
		usage := ExtractUsage(waParseJSONObject(t, `{
			"input_tokens": 10,
			"output_tokens": 6,
			"cache_read_input_tokens": 3,
			"cache_creation_input_tokens": 9,
			"speed": "priority",
			"output_tokens_details": {"thinking_tokens": 2}
		}`))
		waAssertToken(t, usage.InputTokens, 10, "input")
		waAssertToken(t, usage.OutputTokens, 6, "output")
		waAssertToken(t, usage.CacheReadTokens, 3, "cacheRead")
		waAssertToken(t, usage.CacheWriteTokens, 9, "cacheWrite 显式值优先于细分求和")
		if usage.CacheWrite1hTokens != nil {
			t.Fatalf("cacheWrite1h 未上报应为 nil，得到 %d", *usage.CacheWrite1hTokens)
		}
		waAssertToken(t, usage.ThinkingTokens, 2, "thinking")
		if usage.ServiceTier != "priority" {
			t.Fatalf("ServiceTier = %q，期望 priority", usage.ServiceTier)
		}
	})
	t.Run("cache_creation_input_tokens 缺省时按 5m+1h 求和兜底", func(t *testing.T) {
		usage := ExtractUsage(waParseJSONObject(t, `{
			"cache_creation": {"ephemeral_5m_input_tokens": 4, "ephemeral_1h_input_tokens": 5}
		}`))
		waAssertToken(t, usage.CacheWriteTokens, 9, "cacheWrite = 5m+1h")
		waAssertToken(t, usage.CacheWrite1hTokens, 5, "cacheWrite1h")
	})
	t.Run("非对象输入返回空 usage", func(t *testing.T) {
		if usage := ExtractUsage("not-a-map"); HasAnyUsageValue(usage) {
			t.Fatalf("字符串输入不应产生 usage: %+v", usage)
		}
	})
	t.Run("数字字符串与非法数字", func(t *testing.T) {
		usage := ExtractUsage(waParseJSONObject(t, `{"input_tokens": "12", "output_tokens": "abc"}`))
		waAssertToken(t, usage.InputTokens, 12, "数字字符串应解析")
		if usage.OutputTokens != nil {
			t.Fatalf("非数字字符串应为 nil，得到 %d", *usage.OutputTokens)
		}
	})
	t.Run("负数与小数截断", func(t *testing.T) {
		usage := ExtractUsage(waParseJSONObject(t, `{"input_tokens": -1, "output_tokens": 7.9}`))
		if usage.InputTokens != nil {
			t.Fatalf("负数应为 nil，得到 %d", *usage.InputTokens)
		}
		waAssertToken(t, usage.OutputTokens, 7, "小数按截断处理")
	})
	t.Run("numberValue 直接边界", func(t *testing.T) {
		if numberValue(float64(math.NaN())) != nil {
			t.Fatal("NaN 应为 nil")
		}
		if numberValue(float64(math.Inf(1))) != nil {
			t.Fatal("Inf 应为 nil")
		}
		if numberValue(true) != nil {
			t.Fatal("布尔应为 nil")
		}
		if numberValue("NaN") != nil {
			t.Fatal("NaN 字符串经 JSON 解析失败应为 nil")
		}
		if numberValue("012") != nil {
			t.Fatal("非 JSON 数字字面量应为 nil")
		}
	})
}

func TestWAEmptyMergeHasAnyUsage(t *testing.T) {
	if HasAnyUsageValue(EmptyUsage()) {
		t.Fatal("空 usage 不应有任何值")
	}
	base := ParsedUsage{InputTokens: intPtr(1)}
	next := ParsedUsage{InputTokens: intPtr(5), ServiceTier: "priority"}
	merged := MergeUsage(base, next)
	waAssertToken(t, merged.InputTokens, 5, "next 非 nil 覆盖 current")
	if merged.ServiceTier != "priority" {
		t.Fatalf("ServiceTier = %q，期望 priority", merged.ServiceTier)
	}
	// next 为 nil/空时保留 current。
	kept := MergeUsage(base, ParsedUsage{})
	waAssertToken(t, kept.InputTokens, 1, "next 空时保留 current")
}

func intPtr(value int) *int { return &value }

func TestWANormalizeOptionalUsageServiceTier(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"priority", "priority"},
		{"a.b_c-d1", "a.b_c-d1"},
		{"PRIORITY", ""}, // Anthropic 服务端正则无 i 标志，大写无效
		{" auto ", ""},
		{"", ""},
		{123, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := NormalizeOptionalUsageServiceTier(tc.in); got != tc.want {
			t.Fatalf("NormalizeOptionalUsageServiceTier(%v) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
	// 长度边界：64 字符有效，65 字符无效。
	long64 := ""
	long65 := ""
	for i := 0; i < 64; i++ {
		long64 += "a"
	}
	long65 = long64 + "a"
	if got := NormalizeOptionalUsageServiceTier(long64); got != long64 {
		t.Fatalf("长度 64 的 service tier 应有效，得到 %q", got)
	}
	if got := NormalizeOptionalUsageServiceTier(long65); got != "" {
		t.Fatalf("长度 65 的 service tier 应无效，得到 %q", got)
	}
}

func TestWAParseUsageFromJSONBufferAndValue(t *testing.T) {
	t.Run("空 buffer", func(t *testing.T) {
		if usage := ParseUsageFromJSONBuffer(nil); HasAnyUsageValue(usage) {
			t.Fatalf("空 buffer 应为空 usage: %+v", usage)
		}
	})
	t.Run("完整 JSON 文档", func(t *testing.T) {
		usage := ParseUsageFromJSONBuffer([]byte(`{"id":"m","usage":{"input_tokens":4,"output_tokens":2}}`))
		waAssertToken(t, usage.InputTokens, 4, "input")
		waAssertToken(t, usage.OutputTokens, 2, "output")
	})
	t.Run("JSONValue 只读根 usage", func(t *testing.T) {
		value := waParseJSONObject(t, `{"usage":{"input_tokens":8},"message":{"usage":{"input_tokens":99}}}`)
		usage := ParseUsageFromJSONValue(value)
		waAssertToken(t, usage.InputTokens, 8, "只读根 usage")
	})
	t.Run("JSONValue 非对象", func(t *testing.T) {
		if usage := ParseUsageFromJSONValue([]any{1}); HasAnyUsageValue(usage) {
			t.Fatalf("数组输入应为空 usage: %+v", usage)
		}
	})
}

// 片段扫描契约：在可能截断的大响应文本中反查最后一个完整的 "usage":{...}。
func TestWAParseUsageFromJSONTextFragment(t *testing.T) {
	t.Run("最后一个 usage 对象生效", func(t *testing.T) {
		text := `{"usage":{"input_tokens":1}} tail {"usage":{"input_tokens":7,"output_tokens":3}}`
		usage := ParseUsageFromJSONTextFragment(text)
		waAssertToken(t, usage.InputTokens, 7, "input（最后一个对象）")
		waAssertToken(t, usage.OutputTokens, 3, "output（最后一个对象）")
	})
	t.Run("字符串内的花括号不干扰配平", func(t *testing.T) {
		text := `{"usage":{"input_tokens":5,"note":"brace } inside"}}`
		usage := ParseUsageFromJSONTextFragment(text)
		waAssertToken(t, usage.InputTokens, 5, "input")
	})
	t.Run("无 usage 返回空", func(t *testing.T) {
		if usage := ParseUsageFromJSONTextFragment(`{"id":"x"}`); HasAnyUsageValue(usage) {
			t.Fatalf("无 usage 应为空: %+v", usage)
		}
	})
	t.Run("usage 值不是对象返回空", func(t *testing.T) {
		// "usage": 后跟字符串而不是 '{'，反查会继续往前找并最终失败。
		if usage := ParseUsageFromJSONTextFragment(`{"usage":"none"}`); HasAnyUsageValue(usage) {
			t.Fatalf("usage 非对象应为空: %+v", usage)
		}
	})
	t.Run("空文本", func(t *testing.T) {
		if usage := ParseUsageFromJSONTextFragment(""); HasAnyUsageValue(usage) {
			t.Fatalf("空文本应为空 usage: %+v", usage)
		}
	})
	t.Run("usage 对象未配平", func(t *testing.T) {
		if usage := ParseUsageFromJSONTextFragment(`{"usage":{"input_tokens":5`); HasAnyUsageValue(usage) {
			t.Fatalf("未配平对象应为空 usage: %+v", usage)
		}
	})
}

// RFC3339 解析契约：必须带 Z 或数值 offset；日期分量按真实日历校验。
func TestWAParseRFC3339Instant(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"UTC Z", "2025-01-15T10:30:00Z", true},
		{"毫秒小数", "2025-01-15T10:30:00.123Z", true},
		{"九位小数", "2025-01-15T10:30:00.123456789Z", true},
		{"数值 offset", "2025-01-15T18:30:00+08:00", true},
		{"负 offset", "2025-01-15T02:30:00-08:00", true},
		{"缺 offset", "2025-01-15T10:30:00", false},
		{"月份 13", "2025-13-01T10:30:00Z", false},
		{"月份 0", "2025-00-01T10:30:00Z", false},
		{"日 32", "2025-01-32T10:30:00Z", false},
		{"平年 2 月 29", "2023-02-29T10:30:00Z", false},
		{"闰年 2 月 29", "2024-02-29T10:30:00Z", true},
		{"小月 31 日", "2023-04-31T10:30:00Z", false},
		{"小时 24", "2025-01-15T24:30:00Z", false},
		{"分钟 60", "2025-01-15T10:60:00Z", false},
		{"秒 60", "2025-01-15T10:30:60Z", false},
		{"offset 小时 25", "2025-01-15T10:30:00+25:00", false},
		{"offset 分钟 60", "2025-01-15T10:30:00+00:60", false},
		{"完全不是时间", "not-a-time", false},
		{"首尾空白可接受", "  2025-01-15T10:30:00Z  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := ParseRFC3339Instant(tc.value)
			if ok != tc.want {
				t.Fatalf("ParseRFC3339Instant(%q) ok = %v，期望 %v", tc.value, ok, tc.want)
			}
		})
	}
}

func TestWACanonicalizeRFC3339Instant(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"UTC 原样毫秒化", "2025-01-15T10:30:00Z", "2025-01-15T10:30:00.000Z"},
		{"offset 归一到 UTC", "2025-01-15T18:30:00+08:00", "2025-01-15T10:30:00.000Z"},
		{"小数位保留毫秒", "2025-01-15T10:30:00.5Z", "2025-01-15T10:30:00.500Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalizeRFC3339Instant(tc.in)
			if !ok {
				t.Fatalf("CanonicalizeRFC3339Instant(%q) 应成功", tc.in)
			}
			if got != tc.want {
				t.Fatalf("CanonicalizeRFC3339Instant(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
	if _, ok := CanonicalizeRFC3339Instant("2025-02-30T00:00:00Z"); ok {
		t.Fatal("不存在日期应失败")
	}
}

// decodeJSONIntoMap 的错误路径（供 helper 与调用方契约使用）。
func TestWADecodeJSONIntoMapError(t *testing.T) {
	if _, err := decodeJSONIntoMap("{broken"); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(`{"a":1}`), &value); err != nil {
		t.Fatalf("合法 JSON 不应报错: %v", err)
	}
}
