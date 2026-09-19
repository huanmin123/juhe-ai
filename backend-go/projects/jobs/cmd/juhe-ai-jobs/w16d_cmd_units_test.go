// 波次 w16d：纯单元臂批次一（无外部依赖）。覆盖：
//   - worker_balance_detect.go 的 balanceConfigJSON/解析器助手错误与归一化臂；
//   - worker_business_db.go 的 scanNullTime 方言分支；
//   - main.go healthHandler 槽位类型断言失败时的默认值调用臂（875/883/885）；
//   - worker_schedule_settings.go 的 OnlineFreshnessCap 收敛臂；
//   - worker_config.go 的 env 整数解析错误臂与 ProbeEnabled 业务库门禁；
//   - worker_health_probe_outbox.go 的保留天数解析臂；
//   - worker_health_projection.go 的 poll/batch 边界臂。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	_ "modernc.org/sqlite"
)

func TestW16DBalanceConfigParsersArms(t *testing.T) {
	// normalizeBalanceConfigJSON：intervalMinutes=0 回落 5；preferred 非空保留。
	defaulted, err := normalizeBalanceConfigJSON("builtin", 0, "")
	if err != nil {
		t.Fatalf("normalize 默认间隔: %v", err)
	}
	if defaulted != `{"adapter":"builtin","intervalMinutes":5}` {
		t.Fatalf("默认间隔归一化错误: %s", defaulted)
	}
	withPreferred, err := normalizeBalanceConfigJSON("builtin", 7, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("normalize preferred: %v", err)
	}
	if withPreferred != `{"adapter":"builtin","intervalMinutes":7,"preferredBuiltinAdapter":"gpt-4o-mini"}` {
		t.Fatalf("preferred 归一化错误: %s", withPreferred)
	}

	// balanceConfigJSONEqual：非法 JSON / 非对象 / 不一致 / 一致。
	if balanceConfigJSONEqual("not-json", defaulted) {
		t.Fatal("非法 JSON 必须不相等")
	}
	if balanceConfigJSONEqual(`[1,2]`, defaulted) {
		t.Fatal("非对象 JSON 必须不相等")
	}
	if balanceConfigJSONEqual(`{"adapter":"other","intervalMinutes":5}`, defaulted) {
		t.Fatal("adapter 不同必须不相等")
	}
	// stored 缺 intervalMinutes → intOrZero=0 → 归一化 5，与默认等价。
	if !balanceConfigJSONEqual(`{"adapter":"builtin"}`, defaulted) {
		t.Fatal("缺 intervalMinutes 应归一化后相等")
	}
	if !balanceConfigJSONEqual(`{"adapter":"builtin","intervalMinutes":5}`, defaulted) {
		t.Fatal("等价配置必须相等")
	}

	// parseRFC3339Millis：合法/非法。
	if _, err := parseRFC3339Millis(" 2026-09-18T00:00:00Z "); err != nil {
		t.Fatalf("合法 RFC3339 解析失败: %v", err)
	}
	if _, err := parseRFC3339Millis("not-a-time"); err == nil {
		t.Fatal("非法 RFC3339 必须报错")
	}
	// hasAtLeastOneAPIKey：空列表/空白键/键形式。
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{"  "}}) {
		t.Fatal("空白 api_keys 不得通过")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_key": "   "}) {
		t.Fatal("空白 api_key 不得通过")
	}
	if !hasAtLeastOneAPIKey(map[string]any{"api_key": "sk-x"}) {
		t.Fatal("非空 api_key 必须通过")
	}
	if hasAtLeastOneAPIKey(map[string]any{}) {
		t.Fatal("无凭据不得通过")
	}
	// intOrZero 的 json.Number 分支。
	if got := intOrZero(json.Number("42")); got != 42 {
		t.Fatalf("json.Number 分支: %d", got)
	}
	if got := intOrZero("not-number"); got != 0 {
		t.Fatalf("未知类型必须回 0: %d", got)
	}
}

func TestW16DScanNullTimeArms(t *testing.T) {
	stamp := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	// nil / time.Time / string / []byte。
	if value, err := scanNullTime(false, nil); err != nil || value != nil {
		t.Fatalf("nil 必须 → nil,nil: %v %v", value, err)
	}
	if value, err := scanNullTime(false, stamp); err != nil || value == nil || !value.Equal(stamp) {
		t.Fatalf("time.Time 直通失败: %v %v", value, err)
	}
	if value, err := scanNullTime(false, stamp.UTC().Format(time.RFC3339Nano)); err != nil || value == nil {
		t.Fatalf("string 解析失败: %v %v", value, err)
	}
	if value, err := scanNullTime(false, []byte(stamp.UTC().Format(time.RFC3339Nano))); err != nil || value == nil {
		t.Fatalf("[]byte 解析失败: %v %v", value, err)
	}
	if value, err := scanNullTime(true, []byte(stamp.UTC().Format(time.RFC3339Nano))); err != nil || value == nil {
		t.Fatalf("PG []byte 解析失败: %v %v", value, err)
	}
	if _, err := scanNullTime(false, "bad-time"); err == nil {
		t.Fatal("非法 string 必须报错")
	}
	if _, err := scanNullTime(false, []byte("bad-time")); err == nil {
		t.Fatal("非法 []byte 必须报错")
	}
	if _, err := scanNullTime(false, 12345); err == nil {
		t.Fatal("不支持类型必须报错")
	}
}

func TestW16DHealthHandlerSlotDefaultsInvoked(t *testing.T) {
	// enabled=true 而对应 ready 槽位缺省/类型错误时，默认 ready 字面量必须被
	// 调用（875/883/885 分支）且健康计算按禁用语义聚合。
	runtimeRunning := &atomic.Bool{}
	runtimeRunning.Store(true)
	handler := healthHandler(ownermode.Active, runtimeRunning, func() bool { return true }, false, nil,
		true, "not-a-func",
		true, "not-a-func",
		func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		true, "not-a-func",
		true, "not-a-func",
		"not-a-map",
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("/health 必须 200: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, fragment := range []string{`"accountBalanceEnabled":true`, `"proxyLatencyEnabled":true`, `"modelCheckEnabled":true`, `"workerEnabled":true`, `"ready":true`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("载荷缺少 %s: %s", fragment, body)
		}
	}
}

func TestW16DResolveScheduleSettingsIntervalCap(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", root+"/settings.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
		PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	key, ok := jobregistry.SettingsIntervalJobNames()["usage-stats-aggregation"]
	if !ok {
		t.Fatal("注册表缺少 usage-stats-aggregation 的设置键")
	}
	if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', ?, '3600')`, key); err != nil {
		t.Fatal(err)
	}
	source := jobssettings.NewSource(jobssettings.Options{DB: db, Mode: jobssettings.SQLite})
	// 3600 超过 OnlineFreshnessCap=60 → 收敛到 60（覆盖 98-100 分支）。
	interval, ok := resolveScheduleSettingsInterval(context.Background(), source, nil, "usage-stats-aggregation")
	if !ok || interval != 60*time.Second {
		t.Fatalf("OnlineFreshnessCap 收敛错误: %v ok=%v", interval, ok)
	}
	// 非设置驱动 job → false（81-84 分支）。
	if _, ok := resolveScheduleSettingsInterval(context.Background(), source, nil, "not-a-spec-job"); ok {
		t.Fatal("非设置驱动 job 必须返回 false")
	}
}

func TestW16DLoadWorkerConfigErrorArms(t *testing.T) {
	base := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for key, value := range extra {
			env[key] = value
		}
		return env
	}
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"record maintenance batch size 非整数", base(map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE": "abc"})},
		{"probe concurrency 非整数", base(map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "abc"})},
		{"投影 interval 非整数", base(map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "abc"})},
		{"投影 batch 非整数", base(map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "abc"})},
		{"投影 max batches 非整数", base(map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN": "abc"})},
		{"投影 worker concurrency 非整数", base(map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY": "abc"})},
		// ProbeEnabled 门禁：禁用 balance-detect 后缺业务库路径在探针族检查处失败。
		{"probe 缺业务库路径", base(map[string]string{
			"JUHE_AI_JOBS_BALANCE_DETECT_ENABLED": "false",
			"JUHE_AI_SECRET":                      "0123456789abcdef0123456789abcdef",
		})},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadWorkerConfig(getenvFrom(test.env)); err == nil {
				t.Fatalf("场景 %s 必须报错", test.name)
			}
		})
	}
}

func TestW16DProbeOutboxRetentionParseArms(t *testing.T) {
	warns := 0
	warn := func(string) { warns++ }
	if got := parseProbeOutboxRetentionDays(func(string) string { return "" }, warn); got != defaultProbeOutboxRetentionDays {
		t.Fatalf("缺省必须回落默认: %d", got)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "30" }, warn); got != 30 {
		t.Fatalf("合法值必须直通: %d", got)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return " 14 " }, warn); got != 14 {
		t.Fatalf("空白环绕值必须直通: %d", got)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "abc" }, warn); got != defaultProbeOutboxRetentionDays || warns != 1 {
		t.Fatalf("非法值必须回落并告警: %d warns=%d", got, warns)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "0" }, warn); got != defaultProbeOutboxRetentionDays || warns != 2 {
		t.Fatalf("下界外必须回落并告警: %d warns=%d", got, warns)
	}
	if got := parseProbeOutboxRetentionDays(func(string) string { return "366" }, warn); got != defaultProbeOutboxRetentionDays || warns != 3 {
		t.Fatalf("上界外必须回落并告警: %d warns=%d", got, warns)
	}
	if got := parseProbeOutboxRetentionDays(nil, nil); got != defaultProbeOutboxRetentionDays {
		t.Fatalf("nil getenv 必须回落默认: %d", got)
	}
}

func TestW16DHealthProjectionBoundArms(t *testing.T) {
	if _, err := healthProjectionPollInterval(func(string) string { return "" }); err != nil {
		t.Fatalf("缺省 poll 必须默认: %v", err)
	}
	if _, err := healthProjectionPollInterval(func(string) string { return "1000" }); err != nil {
		t.Fatalf("合法 poll 必须通过: %v", err)
	}
	if _, err := healthProjectionPollInterval(func(string) string { return "99" }); err == nil {
		t.Fatal("poll 下界外必须报错")
	}
	if _, err := healthProjectionPollInterval(func(string) string { return "abc" }); err == nil {
		t.Fatal("非整数 poll 必须报错")
	}
	if _, err := healthProjectionBatchSize(func(string) string { return "" }); err != nil {
		t.Fatalf("缺省 batch 必须默认: %v", err)
	}
	if _, err := healthProjectionBatchSize(func(string) string { return "0" }); err == nil {
		t.Fatal("batch 下界外必须报错")
	}
	if _, err := healthProjectionBatchSize(func(string) string { return "1001" }); err == nil {
		t.Fatal("batch 上界外必须报错")
	}
	// loadWorkerConfig 的投影边界校验臂（315/322/329/336 行）。
	projectionCases := []struct {
		name string
		env  map[string]string
	}{
		{"interval 下界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "999"}},
		{"batch 上界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "101"}},
		{"max batches 上界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN": "401"}},
		{"worker concurrency 上界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY": "9"}},
	}
	for _, test := range projectionCases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadWorkerConfig(getenvFrom(test.env)); err == nil {
				t.Fatalf("场景 %s 必须报错", test.name)
			}
		})
	}
}
