package statreads

// statreads 第二轮补充（wd_ 前缀，独占新增文件）：读错误→500 的降级契约、
// PG outcome 入口不可达的失败面、分页 hasMore、损坏 shard 的容错与系统账户
// 名回退。读错误契约用 pgtestkit 空 PG 库；分片/shard 容错（组B，SQLite-only
// 组B 分片读面测试已随 sqlite 生产臂退役（2026-10-24 存储形态收口）。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "modernc.org/sqlite"
)

// wdErrTimezone 返回固定失败的时区源（模拟系统设置损坏）。
func wdErrTimezone() TimezoneSource {
	return func(context.Context) (string, error) { return "", errors.New("系统设置缺少 usageStatsTimezone") }
}

// wdUTCOnlyDeps 只提供 UTC 时区、不给任何表：所有读查询都会失败（PG 空库上
// 为 relation does not exist），用于锁定"读错误一律 500 服务器内部错误"的
// 路由契约。
func wdUTCOnlyDeps(t *testing.T) *Deps {
	t.Helper()
	db := pgtestkit.New(t)
	return &Deps{
		Business: db,
		Stats:    db,
		Now:      func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
		Timezone: func(context.Context) (string, error) { return "UTC", nil },
	}
}

func TestWdReadErrorsSurfaceAs500(t *testing.T) {
	t.Run("usage-overview 各分节", func(t *testing.T) {
		deps := wdUTCOnlyDeps(t)
		for _, section := range []string{"summary", "daily-trend", "hourly-trend", "model-distribution", "errors"} {
			recorder := wdGet(t, deps.overviewSectionHandler(false), "/usage-overview/"+section+"?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("分节 %s 读错误应 500: %d %s", section, recorder.Code, recorder.Body.String())
			}
		}
	})
	t.Run("account-usage 家族", func(t *testing.T) {
		deps := wdUTCOnlyDeps(t)
		cases := []struct {
			name    string
			handler http.HandlerFunc
		}{
			{"列表", deps.accountUsageHandler(false)},
			{"摘要", deps.accountUsageSummaryHandler(false)},
			{"趋势", deps.accountUsageTrendHandler(false)},
		}
		for _, tc := range cases {
			// 趋势无 accountIds 时在读查询前就返回空结果，因此统一带一个选中账户。
			recorder := wdGet(t, tc.handler, "/?startDate=2026-09-04&endDate=2026-09-04&accountIds=acct-x", adminAuth(""))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("%s 读错误应 500: %d %s", tc.name, recorder.Code, recorder.Body.String())
			}
		}
	})
	t.Run("ai-performance 家族", func(t *testing.T) {
		deps := wdUTCOnlyDeps(t)
		if recorder := wdGet(t, deps.aiPerformanceBaseHandler(false), "/?startDate=2026-09-04&endDate=2026-09-04", adminAuth("")); recorder.Code != http.StatusInternalServerError {
			t.Fatalf("base 读错误应 500: %d", recorder.Code)
		}
		if recorder := wdGet(t, deps.aiPerformanceSeriesHandler(false), "/?startDate=2026-09-04&endDate=2026-09-04&accountIds=a", adminAuth("")); recorder.Code != http.StatusInternalServerError {
			t.Fatalf("series 读错误应 500: %d", recorder.Code)
		}
	})
	t.Run("ai-health 列表与详情", func(t *testing.T) {
		deps := wdUTCOnlyDeps(t)
		if recorder := wdGet(t, deps.aiHealthListHandler(false), "/?hours=24", adminAuth("")); recorder.Code != http.StatusInternalServerError {
			t.Fatalf("ai-health 列表读错误应 500: %d", recorder.Code)
		}
		recorder := wdGet(t, deps.aiHealthHourDetailHandler(false), "/?accountId=a&statHour=2026-09-04T10", adminAuth(""))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("ai-health 详情读错误应 500: %d", recorder.Code)
		}
	})
	t.Run("usage-records 参数解析", func(t *testing.T) {
		// 日期窗口解析依赖时区，时区失败时解析错误应呈现为 500。
		deps := &Deps{Timezone: wdErrTimezone(), Now: func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }}
		recorder := wdGet(t, deps.usageRecordsListHandler(true), "/", userAuth())
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("日期窗口解析失败应 500: %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("时区源失败的入口", func(t *testing.T) {
		deps := &Deps{Timezone: wdErrTimezone(), Now: func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }}
		for name, handler := range map[string]http.HandlerFunc{
			"overview":     deps.overviewSectionHandler(false),
			"accountList":  deps.accountUsageHandler(false),
			"accountTrend": deps.accountUsageTrendHandler(false),
			"perfBase":     deps.aiPerformanceBaseHandler(false),
			"perfSeries":   deps.aiPerformanceSeriesHandler(false),
		} {
			target := "/?startDate=2026-09-04&endDate=2026-09-04"
			if name == "perfSeries" {
				// accountIds 参数校验先于日期归一，须带合法重复参数。
				target += "&accountIds=acct-x"
			}
			recorder := wdGet(t, handler, target, adminAuth(""))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("%s 时区失败应 500: %d %s", name, recorder.Code, recorder.Body.String())
			}
		}
	})
}

func TestWdAiHealthPostgresOutcomeUnavailableSurfaces500(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		// 至少一个可见账户，j1OutcomesForAccounts 才会真正走到 PG 入口。
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status) VALUES ('acct-pg', 'PGAccount', 'sys-user-1', 'openai', 'api_key', 'active')`,
	)
	// PG outcome 源在测试环境没有可达 PG：入口应在预算内失败并映射 500，
	// 不产生挂起（连接拒绝是确定性的快速失败）。
	fixture.deps.HealthOutcomes = &HealthOutcomeSource{PostgresURL: "postgres://127.0.0.1:1/none?connect_timeout=1"}
	recorder := wdGet(t, fixture.deps.aiHealthListHandler(false), "/?hours=24", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("PG outcome 不可达应 500: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWdAiHealthOversizedKeywordYieldsEmptyPage(t *testing.T) {
	fixture := newWdFixture(t)
	// 关键字超过 128 rune：n-gram 词表为空 → "0 = 1" 恒假过滤，空集而非错误。
	// 用 ASCII 保证字节长度不超过 boundedKeyword 的 200 上限。
	keyword := strings.Repeat("a", 129)
	recorder := wdGet(t, fixture.deps.aiHealthListHandler(false), "/?hours=24&keyword="+keyword, adminAuth(""))
	items := wdJSONArray(t, wdData(t, recorder)["items"], "items")
	if len(items) != 0 {
		t.Fatalf("超长关键字应得空集: %#v", items)
	}
}

func TestWdAiPerformanceKeywordNoMatchYieldsEmptyOptions(t *testing.T) {
	fixture := newWdFixture(t)
	recorder := wdGet(t, fixture.deps.aiPerformanceAccountsHandler(false), "/?keyword=NoSuchKeyword", adminAuth(""))
	options := wdDataArray(t, recorder)
	if len(options) != 0 {
		t.Fatalf("无命中关键字应得空选项: %#v", options)
	}
}

func TestWdSmallPureHelpersRoundTwo(t *testing.T) {
	// nullText.Scan 非文本值报错。
	var raw nullText
	if err := raw.Scan(struct{}{}); err == nil {
		t.Fatalf("nullText 应拒绝非文本值")
	}
	if err := raw.Scan(nil); err != nil || raw.Valid {
		t.Fatalf("nullText nil 应 Valid=false")
	}
	// 空访问范围的汇总/统计映射（viewer 为空的防御路径）。
	sysID, scopeID := accountUsageOverviewSummaryScope(AccessScope{})
	if sysID != "" || scopeID != "" {
		t.Fatalf("空 scope 汇总应为空: %q %q", sysID, scopeID)
	}
	sysID, scopeID = usageOverviewStatsScope(AccessScope{})
	if sysID != "" || scopeID != "" {
		t.Fatalf("空 scope 统计范围应为空: %q %q", sysID, scopeID)
	}
	// nullStringPtr。
	if nullStringPtr(sql.NullString{}) != nil {
		t.Fatalf("Invalid 应为 nil")
	}
	value := "x"
	if got := nullStringPtr(sql.NullString{String: value, Valid: true}); got == nil || *got != value {
		t.Fatalf("Valid 应返回指针: %#v", got)
	}
	// Row.value 直取。
	if (Row{"k": 1}).value("missing") != nil {
		t.Fatalf("缺失键 value 应为 nil")
	}
	if (Row{"k": nil}).boolLike("k") {
		t.Fatalf("nil 列 boolLike 应为 false")
	}
	// deref 非 *any 原样返回。
	if got := deref("raw"); got != "raw" {
		t.Fatalf("deref 非指针应原样返回: %#v", got)
	}
}
