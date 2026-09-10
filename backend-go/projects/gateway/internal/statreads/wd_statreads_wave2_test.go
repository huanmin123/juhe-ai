package statreads

// statreads 第二轮补充（wd_ 前缀，独占新增文件）：读错误→500 的降级契约、
// PG 入口在无 PG 环境下的失败面、requireAdminInternal 门禁、分页 hasMore、
// 损坏 shard 的容错与系统账户名回退。全部使用确定性 SQLite/httptest。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "modernc.org/sqlite"
)

// wdErrTimezone 返回固定失败的时区源（模拟系统设置损坏）。
func wdErrTimezone() TimezoneSource {
	return func(context.Context) (string, error) { return "", errors.New("系统设置缺少 usageStatsTimezone") }
}

// wdUTCOnlyDeps 只提供 UTC 时区、不给任何业务/统计表：所有读查询都会失败，
// 用于锁定"读错误一律 500 服务器内部错误"的路由契约。
func wdUTCOnlyDeps(t *testing.T) *Deps {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	t.Run("system-metrics 趋势", func(t *testing.T) {
		deps := wdUTCOnlyDeps(t)
		recorder := wdGet(t, deps.systemMetricsTrendHandler, "/?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("系统指标读错误应 500: %d %s", recorder.Code, recorder.Body.String())
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
			"goRuntime":    deps.goRuntimeTrendHandler,
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
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-pg', 'PGAccount', 'sys-user-1', 'openai', 'api_key', 'active')`,
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

func TestWdRequireAdminInternalGate(t *testing.T) {
	deps := &Deps{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	gate := deps.requireAdminInternal(next)
	// 无认证上下文 → 401。
	recorder := httptest.NewRecorder()
	gate.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusUnauthorized || decodeBody(t, recorder)["message"] != "请先登录" {
		t.Fatalf("无认证应 401: %d %s", recorder.Code, recorder.Body.String())
	}
	// 普通用户（my-stats 降级后）→ 403。
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), userAuth()))
	gate.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || decodeBody(t, recorder)["message"] != "需要管理员权限" {
		t.Fatalf("普通用户应 403: %d %s", recorder.Code, recorder.Body.String())
	}
	// admin → 放行。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), adminAuth("")))
	gate.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin 应放行: %d", recorder.Code)
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

func TestWdUsageRecordsHasMoreAndBadShard(t *testing.T) {
	fixture, shardPath, catalog := newWdShardFixture(t)
	wdShardExec(t, shardPath,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, status_code, created_at) VALUES
			('u-h2', 'sys-user-1', 'tr-2', 'gateway', 1, 200, '2026-09-04T11:00:00.000Z')`,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, status_code, created_at) VALUES
			('u-h1', 'sys-user-1', 'tr-1', 'gateway', 1, 200, '2026-09-04T10:00:00.000Z')`,
	)
	// 额外登记一个损坏 shard（非 SQLite 文件）：打开成功、查询失败应被跳过。
	badPath := filepath.Join(t.TempDir(), "bad.sqlite3")
	if err := os.WriteFile(badPath, []byte("definitely not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write bad shard: %v", err)
	}
	if _, err := catalog.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
		VALUES ('20260904:s001', '2026-09-04', 1, ?, 'active')`, badPath); err != nil {
		t.Fatalf("catalog bad seed: %v", err)
	}
	handler := fixture.deps.usageRecordsListHandler(true)
	recorder := wdGet(t, handler, "/?pageSize=1", userAuth())
	payload := wdData(t, recorder)
	items := wdJSONArray(t, payload["items"], "items")
	if len(items) != 1 || items[0].(map[string]any)["id"] != "u-h2" {
		t.Fatalf("pageSize=1 应取最新一条: %#v", payload)
	}
	if payload["hasMore"] != true || payload["total"] != float64(2) {
		t.Fatalf("hasMore/total 上界错误: %#v", payload)
	}
}

func TestWdUsageRecordsSystemAccountNameUsernameFallback(t *testing.T) {
	fixture, shardPath, _ := newWdShardFixture(t)
	wdShardExec(t, shardPath,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, status_code, created_at) VALUES
			('u-n1', 'sys-nodisplay', 'tr-1', 'gateway', 1, 200, '2026-09-04T10:00:00.000Z')`,
	)
	fixture.exec(t,
		// display_name 为 NULL：admin 面回退 username。
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-nodisplay', 'fallbackname', NULL)`,
	)
	recorder := wdGet(t, fixture.deps.usageRecordsListHandler(false), "/?systemAccountId=sys-nodisplay", adminAuth(""))
	items := wdJSONArray(t, wdData(t, recorder)["items"], "items")
	if len(items) != 1 {
		t.Fatalf("行数错误: %#v", items)
	}
	row := items[0].(map[string]any)
	if row["systemAccountId"] != "sys-nodisplay" || row["systemAccountName"] != "fallbackname" {
		t.Fatalf("display_name 缺失应回退 username: %#v", row)
	}
}

func TestWdGoRuntimeTrendDateContractAndQueryFailure(t *testing.T) {
	fixture := newWdFixture(t)
	// 日期契约 400。
	recorder := wdGet(t, fixture.deps.goRuntimeTrendHandler, "/?startDate=nope", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法日期应 400: %d", recorder.Code)
	}
	if got := decodeBody(t, recorder)["message"]; got != "Go 运行时指标日期范围不合法" {
		t.Fatalf("400 消息错误: %#v", got)
	}
	// 查询失败（store 底层库已关闭）→ 读错误 500。
	broken, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "broken.sqlite3"))
	if err != nil {
		t.Fatalf("open broken: %v", err)
	}
	store, err := gometrics.NewStore(broken, gometrics.DialectSQLite)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := broken.Close(); err != nil {
		t.Fatalf("close broken: %v", err)
	}
	fixture.deps.GoRuntimeMetrics = store
	recorder = wdGet(t, fixture.deps.goRuntimeTrendHandler, "/?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("store 查询失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}
}
