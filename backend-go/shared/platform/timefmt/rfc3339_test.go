package timefmt

import (
	"testing"
	"time"
)

func TestParseRFC3339InstantValid(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"2026-09-18T12:00:00Z", "2026-09-18T12:00:00.000Z"},
		{"2026-09-18T12:00:00.123Z", "2026-09-18T12:00:00.123Z"},
		{"2026-09-18T12:00:00.123456Z", "2026-09-18T12:00:00.123Z"},
		{"2026-09-18T12:00:00.1Z", "2026-09-18T12:00:00.100Z"},
		{" 2026-09-18T12:00:00Z ", "2026-09-18T12:00:00.000Z"},
		{"2026-09-18T20:00:00+08:00", "2026-09-18T12:00:00.000Z"},
		{"2026-09-18T04:00:00-08:00", "2026-09-18T12:00:00.000Z"},
		{"2024-02-29T00:00:00Z", "2024-02-29T00:00:00.000Z"},
	}
	for _, tc := range cases {
		got, ok := ParseRFC3339Instant(tc.in)
		if !ok {
			t.Fatalf("%q 应可解析", tc.in)
		}
		if formatted := FormatRFC3339Millis(got); formatted != tc.want {
			t.Fatalf("%q 规范化=%s want %s", tc.in, formatted, tc.want)
		}
	}
}

func TestParseRFC3339InstantInvalid(t *testing.T) {
	cases := []string{
		"",
		"not-a-time",
		"2026-09-18 12:00:00Z",
		"2026-13-01T00:00:00Z",
		"2026-02-30T00:00:00Z",
		"2026-09-18T24:00:00Z",
		"2026-09-18T12:60:00Z",
		"2026-09-18T12:00:60Z",
		"2026-09-18T12:00:00",
		"2026-09-18T12:00:00+24:00",
		"2026-09-18T12:00:00+08:99",
	}
	for _, in := range cases {
		if _, ok := ParseRFC3339Instant(in); ok {
			t.Fatalf("%q 应拒绝", in)
		}
	}
}

func TestCanonicalizeAndRequired(t *testing.T) {
	got, ok := CanonicalizeRFC3339Instant("2026-09-18T12:00:00.999999Z")
	if !ok || got != "2026-09-18T12:00:00.999Z" {
		t.Fatalf("canonicalize=%s ok=%v", got, ok)
	}
	if _, err := RequiredRFC3339Instant("bad", "统计时间"); err == nil {
		t.Fatal("Required 对非法输入必须报错")
	} else if err.Error() != "统计时间必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("错误文案不符: %v", err)
	}
	if got, err := RequiredRFC3339Instant("2026-09-18T12:00:00Z", "统计时间"); err != nil || got != "2026-09-18T12:00:00.000Z" {
		t.Fatalf("Required 合法输入: %s %v", got, err)
	}
}

func TestRFC3339Milliseconds(t *testing.T) {
	ms, ok := RFC3339Milliseconds("2026-09-18T12:00:00.123Z")
	if !ok {
		t.Fatal("应可解析")
	}
	want := time.Date(2026, 9, 18, 12, 0, 0, 123_000_000, time.UTC).UnixMilli()
	if ms != want {
		t.Fatalf("ms=%d want %d", ms, want)
	}
	if _, ok := RFC3339Milliseconds("bad"); ok {
		t.Fatal("非法输入必须 false")
	}
}

func TestFormatRFC3339Millis(t *testing.T) {
	got := FormatRFC3339Millis(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	if got != "2026-09-18T12:00:00.000Z" {
		t.Fatalf("format=%s", got)
	}
}
