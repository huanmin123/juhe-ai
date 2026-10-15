package statreads

// w11g 覆盖补充：日期范围默认分支、usage-records 过滤/分片/水合细节、
// 行访问器与失败原因映射。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func TestW11GDateRangeDefaultArms(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	// usage-overview：单边/双边默认填充。
	rng, err := fixture.deps.normalizeUsageOverviewDateRange(ctx, "", "2026-09-02")
	if err != nil || rng.StartDate != "2026-09-02" || rng.EndDate != "2026-09-02" {
		t.Fatalf("usage overview end-only = %+v err=%v", rng, err)
	}
	rng, err = fixture.deps.normalizeUsageOverviewDateRange(ctx, "2026-09-01", "")
	if err != nil || rng.StartDate != "2026-09-01" {
		t.Fatalf("usage overview start-only = %+v err=%v", rng, err)
	}
	// ai-performance：start 取 endDate，end 回填 startDate。
	rng, err = fixture.deps.normalizeStatsDateRange(ctx, "", "2026-09-03")
	if err != nil || rng.StartDate != "2026-09-03" || rng.EndDate != "2026-09-03" {
		t.Fatalf("stats end-only = %+v err=%v", rng, err)
	}
	rng, err = fixture.deps.normalizeStatsDateRange(ctx, "2026-09-01", "")
	if err != nil || rng.StartDate != "2026-09-01" || rng.EndDate != "2026-09-01" {
		t.Fatalf("stats start-only = %+v err=%v", rng, err)
	}
	// ai-performance：全空回退默认 3 天窗口 [today-2, today]（BUG-0207，
	// fixture now=2026-09-04 UTC：endDate 不得被 defaultStart 传染）。
	rng, err = fixture.deps.normalizeStatsDateRange(ctx, "", "")
	if err != nil || rng.StartDate != "2026-09-02" || rng.EndDate != "2026-09-04" {
		t.Fatalf("stats both-empty = %+v err=%v", rng, err)
	}
	// normalizeRange：start 晚于 today 回拉、早于窗口下限回拉、无输入全默认。
	today := "2026-09-04"
	if got := normalizeRange("2026-10-01", "2026-09-02", today); got.StartDate != "2026-09-02" || got.EndDate != "2026-09-02" {
		t.Fatalf("future start clamp = %+v", got)
	}
	if got := normalizeRange("2020-01-01", "2026-09-04", today); got.StartDate != "2026-08-05" {
		t.Fatalf("earliest clamp = %+v", got)
	}
}

func TestW11GRowAccessorAndFailureReasonArms(t *testing.T) {
	// nullFloat：nil 与不可解析值。
	var nilRow Row = Row{"v": nil}
	if nilRow.nullFloat("v") != nil {
		t.Fatal("nil value must stay nil")
	}
	badRow := Row{"v": "not-a-number"}
	if badRow.nullFloat("v") != nil {
		t.Fatal("unparseable value must stay nil")
	}
	// toText：time / json.Number / []byte / 其他类型。
	stamp := time.Date(2026, 9, 4, 10, 30, 0, 0, time.FixedZone("W11G", 3600))
	if got := toText(stamp); got != "2026-09-04T09:30:00.000Z" {
		t.Fatalf("time text=%s", got)
	}
	if got := toText([]byte("bytes")); got != "bytes" {
		t.Fatalf("bytes text=%s", got)
	}
	if got := toText(42); got != "42" {
		t.Fatalf("int text=%s", got)
	}
	// 失败原因映射各分支。
	successRow := Row{"success": int64(1)}
	if usageRecordListFailureReason(successRow) != nil {
		t.Fatal("success row has no failure reason")
	}
	cases := []struct {
		row      Row
		contains string
	}{
		{Row{"success": int64(0), "error_code": "downstream_connection_closed"}, "下游连接关闭"},
		{Row{"success": int64(0), "failure_attribution": "downstream_closed"}, "下游连接关闭"},
		{Row{"success": int64(0), "error_code": "code_x", "error_message": "msg"}, "code_x | msg"},
		{Row{"success": int64(0), "error_code": "rate_limit_exceeded"}, "rate_limit_exceeded"},
		{Row{"success": int64(0), "failure_attribution": "account_dependency"}, "账户依赖不可用"},
		{Row{"success": int64(0), "failure_attribution": "opaque_upstream"}, "未返回可解析"},
		{Row{"success": int64(0), "failure_attribution": "account_upstream"}, "上游请求失败"},
		{Row{"success": int64(0), "failure_attribution": "gateway_capacity"}, "容量不足"},
		{Row{"success": int64(0), "failure_attribution": "gateway_policy"}, "策略拒绝"},
		{Row{"success": int64(0)}, "请求未正常完成"},
	}
	for _, testCase := range cases {
		reason := usageRecordListFailureReason(testCase.row)
		if reason == nil || !strings.Contains(*reason, testCase.contains) {
			t.Fatalf("row %v reason=%v want contains %s", testCase.row, reason, testCase.contains)
		}
	}
}

func TestW11GDowngradeRoleWithAndWithoutAuth(t *testing.T) {
	called := false
	handler := downgradeRole(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if auth := authsys.AuthContextFrom(r); auth != nil {
			if auth.Role != "user" {
				t.Fatalf("role=%s", auth.Role)
			}
		}
	}))
	// 带 auth：降级为 user。
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), adminAuth("")))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("handler not called")
	}
	// 无 auth：原样放行。
	called = false
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !called {
		t.Fatal("handler not called without auth")
	}
}
