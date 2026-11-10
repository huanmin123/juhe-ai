package cleanuprepo

import (
	"testing"
	"time"
)

// 纯函数助手的小分支补测：numberOf / parseNumber / textOf。这些分支为
// switch/条件全类型覆盖，走真实断言即可稳定复现。

func TestW10ENumberOfTypeBranches(t *testing.T) {
	cases := map[string]struct {
		in   any
		want float64
	}{
		"nil":     {nil, 0},
		"float64": {float64(2.5), 2.5},
		"int64":   {int64(7), 7},
		"int":     {7, 7},
		"[]byte":  {[]byte("3.25"), 3.25},
		"string":  {"9.5", 9.5},
		"invalid": {"not-a-number", 0},
		"other":   {true, 0},
		"trim":    {"  4.0  ", 4},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := numberOf(tc.in)
			if got != tc.want {
				t.Fatalf("numberOf(%v) = %v, 期望 %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestW10EParseNumber(t *testing.T) {
	if got := parseNumber(""); got != 0 {
		t.Fatalf("parseNumber 空串 = %v", got)
	}
	if got := parseNumber("abc"); got != 0 {
		t.Fatalf("parseNumber 非法 = %v", got)
	}
	if got := parseNumber("1e3"); got != 1000 {
		t.Fatalf("parseNumber 指数 = %v", got)
	}
}

func TestW10ETextOfBranches(t *testing.T) {
	if got := textOf(nil); got != "" {
		t.Fatalf("textOf(nil) = %q", got)
	}
	if got := textOf("abc"); got != "abc" {
		t.Fatalf("textOf(string) = %q", got)
	}
	if got := textOf([]byte("bytes")); got != "bytes" {
		t.Fatalf("textOf([]byte) = %q", got)
	}
	now := time.Date(2026, 9, 4, 8, 30, 0, 0, time.FixedZone("T", 8*3600))
	if got := textOf(now); got != "2026-09-04T00:30:00.000Z" {
		t.Fatalf("textOf(time) = %q", got)
	}
	if got := textOf(int64(42)); got != "42" {
		t.Fatalf("textOf(other) = %q", got)
	}
	if got := optionalText("  "); got != "" {
		t.Fatalf("optionalText(空白) = %q", got)
	}
}

func TestW10ENewRandomHex32(t *testing.T) {
	first := newRandomHex32()
	if first == "" {
		t.Fatalf("newRandomHex32 不应为空")
	}
	if second := newRandomHex32(); second == first {
		t.Fatalf("两次随机值不应相同")
	}
	// 返回 32 个十六进制字符（16 字节）。
	if len(first) != 32 {
		t.Fatalf("newRandomHex32 长度 = %d, 期望 32", len(first))
	}
}
