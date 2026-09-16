package tablemonitor

// w9b 纯 helper 与错误臂：行值矩阵、方言 bind、重复列错误、路由窗口钳制。

import (
	"errors"
	"testing"
	"time"
)

func TestW9BParseFloatAndNumber(t *testing.T) {
	if got := parseFloat("  12.5 "); got == nil || *got != 12.5 {
		t.Fatalf("parseFloat=%v", got)
	}
	if got := parseFloat("-0.25"); got == nil || *got != -0.25 {
		t.Fatalf("负数=%v", got)
	}
	if got := parseFloat("1e2"); got == nil || *got != 12 {
		t.Fatalf("手写解析器把 e 当占位（1e2 → 12）=%v", got)
	}
	if got := parseFloat(""); got != nil {
		t.Fatalf("空=%v", got)
	}
	if got := parseFloat("abc"); got != nil {
		t.Fatalf("非法=%v", got)
	}
	if got := parseNumber("3.14159"); got < 3.14159 || got > 3.14160 {
		t.Fatalf("parseNumber=%v", got)
	}
	if got := parseNumber("-42"); got != -42 {
		t.Fatalf("负整数=%v", got)
	}
}

func TestW9BRowBooleanMatrix(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{true, true},
		{false, false},
		{int64(1), true},
		{int64(0), false},
		{float64(2), true},
		{float64(0), false},
		{"1", true},
		{"TRUE", true},
		{"no", false},
		{nil, false},
		{struct{}{}, false},
	}
	for _, tc := range cases {
		row := row{"flag": tc.value}
		if got := row.boolean("flag"); got != tc.want {
			t.Fatalf("boolean(%#v)=%v want %v", tc.value, got, tc.want)
		}
	}
}

func TestW9BIsDuplicateColumnError(t *testing.T) {
	if isDuplicateColumnError(nil) {
		t.Fatal("nil 必须返回 false")
	}
	if !isDuplicateColumnError(errors.New("SQLite: duplicate column name: x")) {
		t.Fatal("重复列必须识别")
	}
	if isDuplicateColumnError(errors.New("other failure")) {
		t.Fatal("其他错误不应识别")
	}
}

func TestW9BClampOverviewWindowMs(t *testing.T) {
	if got := clampOverviewWindowMs(90 * time.Minute); got != tableMonitorOverviewMaxStaleMs {
		t.Fatalf("上限=%v", got)
	}
	if got := clampOverviewWindowMs(0); got != time.Millisecond {
		t.Fatalf("下限=%v", got)
	}
	if got := clampOverviewWindowMs(-time.Hour); got != time.Millisecond {
		t.Fatalf("负值=%v", got)
	}
	if got := clampOverviewWindowMs(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("中值=%v", got)
	}
}

func TestW9BZodReceivedJSONMatrix(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{true, "boolean"},
		{"text", "string"},
		{float64(1), "number"},
		{int(2), "number"},
		{int64(3), "number"},
		{[]any{1}, "array"},
		{map[string]any{"k": "v"}, "object"},
		{struct{}{}, "unknown"},
	}
	for _, tc := range cases {
		if got := zodReceivedJSON(tc.value); got != tc.want {
			t.Fatalf("zodReceivedJSON(%#v)=%q want %q", tc.value, got, tc.want)
		}
	}
}

func TestW9BEnqueueBindDialect(t *testing.T) {
	dispatch := &DurableDispatch{}
	if got := dispatch.bind("VALUES (?, ?, ?)"); got != "VALUES (?, ?, ?)" {
		t.Fatalf("SQLite bind=%q", got)
	}
	pgDispatch := &DurableDispatch{pg: true}
	if got := pgDispatch.bind("VALUES (?, ?)"); got != "VALUES ($1, $2)" {
		t.Fatalf("PG bind=%q", got)
	}
}
