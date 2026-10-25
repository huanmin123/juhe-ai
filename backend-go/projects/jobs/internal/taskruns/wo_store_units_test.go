package taskruns

import (
	"strings"
	"testing"
	"time"
)

// ---- OpenStore 配置校验与双模式 ----

func TestPostgresBoolVariants(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{true, true}, {false, false},
		{"t", true}, {"true", true}, {"1", true}, {"f", false}, {"other", false},
		{int64(1), true}, {int64(0), false},
		{nil, false}, {3.5, false},
	}
	for _, tc := range cases {
		if got := postgresBool(tc.value); got != tc.want {
			t.Fatalf("postgresBool(%#v)=%v，期望 %v", tc.value, got, tc.want)
		}
	}
}

func TestParseJSONObjectVariants(t *testing.T) {
	if got := parseJSONObject(" "); len(got) != 0 {
		t.Fatalf("空文本应返回空对象: %#v", got)
	}
	if got := parseJSONObject("{broken"); len(got) != 0 {
		t.Fatalf("非法 JSON 应返回空对象: %#v", got)
	}
	if got := parseJSONObject(`[1,2]`); len(got) != 0 {
		t.Fatalf("非对象应返回空对象: %#v", got)
	}
	got := parseJSONObject(`{"k":"v"}`)
	if got["k"] != "v" {
		t.Fatalf("合法对象应解析: %#v", got)
	}
}

func TestClockHelpers(t *testing.T) {
	fixed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if got := NowIso(NewFakeClock(fixed)); got != "2026-09-10T12:00:00.000Z" {
		t.Fatalf("NowIso 应输出毫秒 UTC 文本: %s", got)
	}
	// OptionalInstant：nil/空串放行，非法拒绝。
	if got, err := OptionalInstant(nil, "startAt"); err != nil || got != nil {
		t.Fatalf("nil 应放行: %v %v", got, err)
	}
	empty := ""
	if got, err := OptionalInstant(&empty, "startAt"); err != nil || got != nil {
		t.Fatalf("空串应放行: %v %v", got, err)
	}
	valid := "2026-09-10T12:00:00.000Z"
	got, err := OptionalInstant(&valid, "startAt")
	if err != nil || got == nil || !got.Equal(fixed) {
		t.Fatalf("合法时间应解析: %v %v", got, err)
	}
	invalid := "not-a-time"
	if _, err := OptionalInstant(&invalid, "startAt"); err == nil || !strings.Contains(err.Error(), "startAt") {
		t.Fatalf("非法时间应报错并带字段名: %v", err)
	}
}

func TestDerefRunNilSafety(t *testing.T) {
	zero := TaskRun{}
	if derefRun(nil).JobType != zero.JobType || derefRun(nil).RunID != zero.RunID {
		t.Fatalf("nil 应解引用为零值")
	}
	run := &TaskRun{RunID: "run-1"}
	if got := derefRun(run); got.RunID != "run-1" {
		t.Fatalf("非 nil 应返回拷贝: %#v", got)
	}
}
