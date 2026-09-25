package gatewayquota

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// TestWJParseRfc3339Instant 固定共享 RFC3339 解析契约：offset 必填、
// 裸日期不猜本地时区、非法日历值一律拒绝。
func TestWJParseRfc3339Instant(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string // 期望的 UTC 渲染；空串表示解析必须失败
		wantErr bool
	}{
		{name: "Z 后缀", value: "2026-09-04T00:00:00Z", want: "2026-09-04T00:00:00.000Z"},
		{name: "正 offset", value: "2026-09-04T08:30:00+08:00", want: "2026-09-04T00:30:00.000Z"},
		{name: "负 offset", value: "2026-09-04T20:00:00-05:30", want: "2026-09-05T01:30:00.000Z"},
		{name: "毫秒小数", value: "2026-09-04T00:00:00.123Z", want: "2026-09-04T00:00:00.123Z"},
		{name: "一位小数补齐", value: "2026-09-04T00:00:00.5Z", want: "2026-09-04T00:00:00.500Z"},
		{name: "九位小数", value: "2026-09-04T00:00:00.123456789Z", want: "2026-09-04T00:00:00.123Z"},
		{name: "缺 offset 拒绝", value: "2026-09-04T00:00:00", wantErr: true},
		{name: "裸日期拒绝", value: "2026-09-04", wantErr: true},
		{name: "月份 13 拒绝", value: "2026-13-01T00:00:00Z", wantErr: true},
		{name: "2 月 30 拒绝", value: "2026-02-30T00:00:00Z", wantErr: true},
		{name: "小时 24 拒绝", value: "2026-09-04T24:00:00Z", wantErr: true},
		{name: "分钟 60 拒绝", value: "2026-09-04T00:60:00Z", wantErr: true},
		{name: "垃圾输入拒绝", value: "not-a-time", wantErr: true},
		{name: "空串拒绝", value: "   ", wantErr: true},
		{name: "闰日接受", value: "2028-02-29T12:00:00Z", want: "2028-02-29T12:00:00.000Z"},
		{name: "平年 2 月 29 拒绝", value: "2026-02-29T00:00:00Z", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, ok := parseRfc3339Instant(tt.value)
			if tt.wantErr {
				if ok {
					t.Fatalf("%q 应解析失败, 得到 %v", tt.value, parsed)
				}
				return
			}
			if !ok {
				t.Fatalf("%q 应解析成功", tt.value)
			}
			if got := parsed.UTC().Format("2006-01-02T15:04:05.000Z"); got != tt.want {
				t.Fatalf("解析结果 = %s, 期望 %s", got, tt.want)
			}
		})
	}
}

// TestWJCanonicalizeAndMilliseconds 固定规范化输出（Node toISOString 毫秒
// 精度 + Z）与毫秒换算、必填校验错误文案。
func TestWJCanonicalizeAndMilliseconds(t *testing.T) {
	canon, ok := canonicalizeRfc3339Instant("2026-09-04T08:00:00+08:00")
	if !ok || canon != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("canonicalize = (%q, %v)", canon, ok)
	}
	if _, ok := canonicalizeRfc3339Instant("bad"); ok {
		t.Fatal("非法输入必须失败")
	}
	ms, ok := rfc3339InstantMilliseconds("2026-09-04T00:00:00.500Z")
	if !ok || ms != time.Date(2026, 9, 4, 0, 0, 0, 500000000, time.UTC).UnixMilli() {
		t.Fatalf("milliseconds = (%d, %v)", ms, ok)
	}
	if _, ok := rfc3339InstantMilliseconds("nope"); ok {
		t.Fatal("非法输入换算必须失败")
	}
	normalized, err := requiredRfc3339Instant(" 2026-09-04T00:00:00Z ", "测试字段")
	if err != nil || normalized != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("required = (%q, %v)", normalized, err)
	}
	_, err = requiredRfc3339Instant("bad", "测试字段")
	if err == nil || !strings.Contains(err.Error(), "测试字段") || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("错误文案必须带字段标签: %v", err)
	}
}

// TestWJRequestQuotaLimitsJSON 固定序列化契约：空限制输出 SQL NULL 标记，
// 非空限制按 Node 属性顺序输出。
func TestWJRequestQuotaLimitsJSON(t *testing.T) {
	if value, ok := RequestQuotaLimitsJSON(RequestQuotaLimits{}); ok || value != "" {
		t.Fatalf("空限制必须输出 NULL 标记: (%q, %v)", value, ok)
	}
	hours := 6
	limits := RequestQuotaLimits{
		Hourly: &HourlyQuotaLimit{Enabled: true, Hours: hours, Limit: 1.5},
		Daily:  &QuotaLimit{Enabled: true, Limit: 10},
		Total:  &QuotaLimit{Enabled: true, Limit: 100},
	}
	value, ok := RequestQuotaLimitsJSON(limits)
	if !ok {
		t.Fatal("非空限制必须可序列化")
	}
	want := `{"hourly":{"enabled":true,"hours":6,"limit":1.5},"daily":{"enabled":true,"limit":10},"total":{"enabled":true,"limit":100}}`
	if value != want {
		t.Fatalf("JSON = %s, 期望 %s", value, want)
	}
}

// TestWJNormalizeLimitsEdgeCases 固定归一化的 JS 对齐边界：顶层 null 视为
// 空限制、非对象拒绝、window 字段逐一校验。
func TestWJNormalizeLimitsEdgeCases(t *testing.T) {
	// 顶层 null → 空限制。
	limits, err := ParseRequestQuotaLimitsJSON("null")
	if err != nil || HasEnabledRequestQuotaLimit(limits) {
		t.Fatalf("顶层 null 必须解析为空限制: (%+v, %v)", limits, err)
	}
	// 数组 → 参数无效。
	if _, err := ParseRequestQuotaLimitsJSON(`[]`); err == nil || !strings.Contains(err.Error(), "请求额度限制参数无效") {
		t.Fatalf("数组必须拒绝: %v", err)
	}
	// null 字段（JS null 与 absent 的区分）→ 对应维度参数无效。
	if _, err := ParseRequestQuotaLimitsJSON(`{"daily":null}`); err == nil || !strings.Contains(err.Error(), "日额度参数无效") {
		t.Fatalf("daily null 必须拒绝: %v", err)
	}
	// weekly 非法结构。
	if _, err := ParseRequestQuotaLimitsJSON(`{"weekly":{"enabled":true}}`); err == nil || !strings.Contains(err.Error(), "周额度金额必须是大于 0 的数字") {
		t.Fatalf("weekly 缺 limit 必须拒绝: %v", err)
	}
	// monthly 禁用态显式 false。
	if _, err := ParseRequestQuotaLimitsJSON(`{"monthly":{"enabled":false,"limit":1}}`); err == nil || !strings.Contains(err.Error(), "月额度启用状态必须为 true") {
		t.Fatalf("monthly enabled=false 必须拒绝: %v", err)
	}
	// total 不支持字段。
	if _, err := ParseRequestQuotaLimitsJSON(`{"total":{"enabled":true,"limit":1,"foo":1}}`); err == nil || !strings.Contains(err.Error(), "总额度包含不支持字段：foo") {
		t.Fatalf("total 多余字段必须拒绝: %v", err)
	}
	// hourly hours 非整数 / 越界。
	if _, err := ParseRequestQuotaLimitsJSON(`{"hourly":{"enabled":true,"hours":1.5,"limit":1}}`); err == nil || !strings.Contains(err.Error(), "小时额度窗口必须是数字") {
		t.Fatalf("hourly 非整数 hours 必须拒绝: %v", err)
	}
	if _, err := ParseRequestQuotaLimitsJSON(`{"hourly":{"enabled":true,"hours":0,"limit":1}}`); err == nil || !strings.Contains(err.Error(), "小时额度窗口必须在 1-720 之间") {
		t.Fatalf("hourly hours=0 必须拒绝: %v", err)
	}
	if _, err := ParseRequestQuotaLimitsJSON(`{"hourly":{"enabled":true,"hours":721,"limit":1}}`); err == nil || !strings.Contains(err.Error(), "小时额度窗口必须在 1-720 之间") {
		t.Fatalf("hourly hours=721 必须拒绝: %v", err)
	}
	// 金额精度：超过 6 位小数拒绝，恰好 6 位通过。
	if _, err := ParseRequestQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":0.0000001}}`); err == nil || !strings.Contains(err.Error(), "日额度金额最多支持 6 位小数") {
		t.Fatalf("7 位小数必须拒绝: %v", err)
	}
	limits, err = ParseRequestQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":0.000001}}`)
	if err != nil || limits.Daily == nil || limits.Daily.Limit != 0.000001 {
		t.Fatalf("6 位小数必须通过: (%+v, %v)", limits, err)
	}
	// 金额上限。
	if _, err := ParseRequestQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":1e30}}`); err == nil || !strings.Contains(err.Error(), "日额度金额必须是大于 0 的数字") {
		t.Fatalf("超上限金额必须拒绝: %v", err)
	}
}

// TestWJStatWindowKeys 固定统计键的日历口径：周一开周、时区无关。
func TestWJStatWindowKeys(t *testing.T) {
	utc := time.UTC
	moment := time.Date(2026, 9, 4, 8, 0, 0, 0, utc) // 周五
	if got := dateKey(moment, utc); got != "2026-09-04" {
		t.Fatalf("dateKey = %s", got)
	}
	if got := monthKey(moment, utc); got != "2026-09" {
		t.Fatalf("monthKey = %s", got)
	}
	if got := weekKey(moment, utc); got != "2026-08-31" {
		t.Fatalf("weekKey（周五属周一开周）= %s", got)
	}
	// 周日属于前一个周一开头的周。
	sunday := time.Date(2026, 9, 6, 23, 0, 0, 0, utc)
	if got := weekKey(sunday, utc); got != "2026-08-31" {
		t.Fatalf("weekKey（周日）= %s", got)
	}
	// 跨年周：2027-01-01（周五）属于 2026-12-28 开头的周。
	newYear := time.Date(2027, 1, 1, 0, 0, 0, 0, utc)
	if got := weekKey(newYear, utc); got != "2026-12-28" {
		t.Fatalf("weekKey（跨年）= %s", got)
	}
	// 非 UTC 时区的日历键按墙钟计算。
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	if got := dateKey(time.Date(2026, 9, 4, 20, 0, 0, 0, utc), shanghai); got != "2026-09-05" {
		t.Fatalf("上海时区 dateKey = %s", got)
	}
}

// TestWJLRUCacheSemantics 固定 LRU 语义：容量淘汰、访问提升、TTL 过期与
// dispose 回调。
func TestWJLRUCacheSemantics(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	var evicted []string
	cache := newLRUCache[string](2, 5*time.Second, clock.Now, func(key string) { evicted = append(evicted, key) })

	cache.set("a", "1")
	cache.set("b", "2")
	if cache.len() != 2 {
		t.Fatalf("len = %d", cache.len())
	}
	// 访问 a 提升 a，使 b 成为最旧。
	if _, ok := cache.get("a"); !ok {
		t.Fatal("a 必须命中")
	}
	clock.Advance(1 * time.Second)
	cache.set("c", "3")
	if len(evicted) != 1 || evicted[0] != "b" {
		t.Fatalf("淘汰列表 = %v, 期望 [b]", evicted)
	}
	if _, ok := cache.get("b"); ok {
		t.Fatal("b 必须被淘汰")
	}
	// TTL 过期读作 miss（恰好到期时刻仍算有效，After 边界）。
	clock.Advance(5 * time.Second)
	if _, ok := cache.get("a"); ok {
		t.Fatal("a 过期后必须 miss")
	}
	clock.Advance(1 * time.Second)
	if _, ok := cache.get("c"); ok {
		t.Fatal("c 过期后必须 miss")
	}
	// 覆盖写刷新过期时间。
	cache.set("d", "4")
	clock.Advance(4 * time.Second)
	cache.set("d", "5")
	clock.Advance(2 * time.Second)
	if v, ok := cache.get("d"); !ok || v != "5" {
		t.Fatalf("覆盖写必须刷新 TTL: (%q, %v)", v, ok)
	}
	cache.delete("d")
	cache.delete("d") // 重复删除必须幂等
	// set d 时 LRU 淘汰了最久未访问的 a（过期读 miss 不提升新度）；c 只是
	// 过期读作 miss，条目仍惰性残留，显式删除后归零。
	if cache.len() != 1 {
		t.Fatalf("删除 d 后 len = %d（c 过期残留）", cache.len())
	}
	cache.delete("c")
	if cache.len() != 0 {
		t.Fatalf("删除 c 后 len = %d", cache.len())
	}
	// 非法容量归一化为 1；nil 时钟回退 time.Now。
	small := newLRUCache[string](0, time.Minute, nil, nil)
	small.set("x", "y")
	small.set("z", "w")
	if small.len() != 1 {
		t.Fatalf("容量 1 缓存 len = %d", small.len())
	}
}

// TestWJQuotaMemoryCacheIndex 固定 API Key 内存缓存的按 ID 失效索引。
func TestWJQuotaMemoryCacheIndex(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	cache := newQuotaMemoryCache(clock.Now, time.Minute, 10)
	cache.set("ak", "sys\x00ak\x00win\x00limits", CachedDecision{Allowed: true})
	cache.set("ak2", "sys\x00ak2\x00win\x00limits", CachedDecision{Allowed: false})
	if _, ok := cache.get("sys\x00ak\x00win\x00limits"); !ok {
		t.Fatal("ak 条目必须命中")
	}
	cache.removeByID("ak")
	if _, ok := cache.get("sys\x00ak\x00win\x00limits"); ok {
		t.Fatal("removeByID 必须删除 ak 的全部条目")
	}
	if _, ok := cache.get("sys\x00ak2\x00win\x00limits"); !ok {
		t.Fatal("ak2 条目必须保留")
	}
	cache.clear()
	if _, ok := cache.get("sys\x00ak2\x00win\x00limits"); ok {
		t.Fatal("clear 必须清空全部条目")
	}
	// 对同一 cacheKey 覆盖写必须先移除旧索引再重建。
	cache.set("ak", "k1", CachedDecision{Allowed: true})
	cache.set("other", "k1", CachedDecision{Allowed: false})
	cache.removeByID("other")
	if _, ok := cache.get("k1"); ok {
		t.Fatal("覆盖写后旧索引必须失效")
	}
}

// wjMockEstimator 是 CostEstimator 的可控 mock。
type wjMockEstimator struct {
	cost float64
	ok   bool
}

func (m *wjMockEstimator) EstimateCatalogCostUSD(context.Context, CatalogCostInput) (float64, bool) {
	return m.cost, m.ok
}

// TestWJInflightDefaultsAndHelpers 固定在途额度的默认调度器、测试清理与
// 保护性拒绝路径。
func TestWJInflightDefaultsAndHelpers(t *testing.T) {
	logs := &logRecorder{}
	estimator := &wjMockEstimator{cost: 0.5, ok: true}
	service, err := NewInflightQuotaService(InflightQuotaConfig{Log: logs.hook, Estimator: estimator})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	limits, err := ParseRequestQuotaLimitsJSON(testQuotaLimits)
	if err != nil {
		t.Fatalf("parse limits: %v", err)
	}
	// 默认调度器（time.AfterFunc）下预约成功；Complete 停掉泄漏计时器。
	decision := service.Reserve(ReserveInput{
		APIKeyID:         "ak",
		Limits:           limits,
		CurrentCosts:     RequestQuotaCosts{Daily: 5},
		EstimatedCostUsd: 1,
		ReleaseDelayMs:   intPtr(0),
	})
	if !decision.Allowed || decision.Reservation == nil {
		t.Fatalf("预约必须成功: %+v", decision)
	}
	decision.Reservation.Complete()
	if len(service.Snapshot()) != 1 {
		t.Fatalf("释放延迟内仍应保留预约: %+v", service.Snapshot())
	}
	// ReleaseDelayMs=0 立即调度释放，用真实调度器无法确定性等待，改用
	// ClearForTest 清理（同时覆盖清理入口）。
	service.ClearForTest()
	if states := service.Snapshot(); len(states) != 0 {
		t.Fatalf("ClearForTest 后必须为空: %+v", states)
	}

	// 缺少成本快照且未配置 DB service → 保护性拒绝并记录事件。
	gwDecision, err := service.ReserveGatewayCost(context.Background(), GatewayReserveInput{
		APIKey:   APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits},
		Estimate: EstimateRequestInput{RawBodyBytes: 400},
	})
	if err != nil {
		t.Fatalf("ReserveGatewayCost: %v", err)
	}
	if gwDecision.Allowed || !gwDecision.HasEstimatedCostUsd {
		t.Fatalf("无 DB service 必须保护性拒绝: %+v", gwDecision)
	}
	if !logs.has("gateway_api_key_inflight_quota_exact_cost_failed|") {
		t.Fatalf("必须记录精确成本失败事件: %v", logs.items)
	}

	// DB service 读取失败同样保护性拒绝。
	failing := &mockDBService{readCostsErr: errors.New("ipc down")}
	service2, err := NewInflightQuotaService(InflightQuotaConfig{DBService: failing, Log: logs.hook, Estimator: estimator})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	gwDecision, err = service2.ReserveGatewayCost(context.Background(), GatewayReserveInput{
		APIKey:   APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits},
		Estimate: EstimateRequestInput{RawBodyBytes: 400},
	})
	if err != nil {
		t.Fatalf("ReserveGatewayCost: %v", err)
	}
	if gwDecision.Allowed {
		t.Fatalf("DB service 读取失败必须保护性拒绝: %+v", gwDecision)
	}
}

// TestWJDBTimezoneSourceEdgeCases 固定数据库时区源的错误与缓存契约。
func TestWJDBTimezoneSourceEdgeCases(t *testing.T) {
	if _, err := NewDBTimezoneSource(nil, false, nil); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("nil db 必须拒绝: %v", err)
	}
	if _, err := NewStaticTimezoneSource(nil); err == nil || !strings.Contains(err.Error(), "requires a location") {
		t.Fatalf("nil location 必须拒绝: %v", err)
	}
	if _, err := normalizeUsageStatsTimezone("  "); err == nil || !strings.Contains(err.Error(), "统计时区必须是非空字符串") {
		t.Fatalf("空时区必须拒绝: %v", err)
	}
	if _, err := normalizeUsageStatsTimezone(42); err == nil || !strings.Contains(err.Error(), "统计时区必须是非空字符串") {
		t.Fatalf("非字符串必须拒绝: %v", err)
	}
	if _, err := normalizeUsageStatsTimezone("No/SuchZone"); err == nil || !strings.Contains(err.Error(), "统计时区不存在") {
		t.Fatalf("未知时区必须拒绝: %v", err)
	}

	db := newTestDB(t, "wj-tz")
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT, key TEXT, value_json TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	source, err := NewDBTimezoneSource(db, false, clock.Now)
	if err != nil {
		t.Fatalf("NewDBTimezoneSource: %v", err)
	}
	ctx := context.Background()
	// 缺行。
	if _, err := source.StatsTimezone(ctx); err == nil || !strings.Contains(err.Error(), "系统设置缺少 usageStatsTimezone") {
		t.Fatalf("缺行必须报错: %v", err)
	}
	// 空值。
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsTimezone', '')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := source.StatsTimezone(ctx); err == nil || !strings.Contains(err.Error(), "系统设置缺少 usageStatsTimezone") {
		t.Fatalf("空值必须报错: %v", err)
	}
	// 非法 JSON。
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '{bad' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := source.StatsTimezone(ctx); err == nil || !strings.Contains(err.Error(), "系统设置 usageStatsTimezone 无效") {
		t.Fatalf("非法 JSON 必须报错: %v", err)
	}
	// 非法时区名。
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"No/SuchZone"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := source.StatsTimezone(ctx); err == nil || !strings.Contains(err.Error(), "系统设置 usageStatsTimezone 无效") {
		t.Fatalf("非法时区必须报错: %v", err)
	}
	// 成功后 60s 内命中缓存（改库不生效）。
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"UTC"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	loc, err := source.StatsTimezone(ctx)
	if err != nil || loc.String() != "UTC" {
		t.Fatalf("成功读取: (%v, %v)", loc, err)
	}
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"Asia/Shanghai"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if loc, err = source.StatsTimezone(ctx); err != nil || loc.String() != "UTC" {
		t.Fatalf("TTL 内必须命中缓存: (%v, %v)", loc, err)
	}
	// TTL 过后重新读取。
	clock.Advance(61 * time.Second)
	if loc, err = source.StatsTimezone(ctx); err != nil || loc.String() != "Asia/Shanghai" {
		t.Fatalf("TTL 过后必须重读: (%v, %v)", loc, err)
	}
}

// TestWJSmallHelpers 固定零散工具函数的契约。
func TestWJSmallHelpers(t *testing.T) {
	if ensureCtx(nil) == nil {
		t.Fatal("nil ctx 必须回退 Background")
	}
	if ensureCtx(context.Background()) == nil {
		t.Fatal("非 nil ctx 必须原样返回")
	}
	noopLog("event", map[string]any{}, "message")
	if NormalizeHourlyWindowHours(0) != 1 || NormalizeHourlyWindowHours(-3) != 1 || NormalizeHourlyWindowHours(7) != 7 {
		t.Fatal("小时窗口归一化必须 clamp 到 >=1")
	}
	// bindPlaceholders 的 PG 绑定在其它测试覆盖；这里覆盖表名前缀。
	if statsTable(false, "t") != "t" || statsBusinessTable(false, "t") != "t" {
		t.Fatal("SQLite 表名必须不带前缀")
	}
	// itoa64 的多位与非负分支由 runtimeCacheKey 触发；这里直接验证。
	if itoa64(0) != "0" || itoa64(42) != "42" || itoa64(-7) != "-7" {
		t.Fatal("itoa64 数值转文本不符")
	}
	// chunkStrings / sqlPlaceholders / uniqueNonEmpty 的边界。
	if got := chunkStrings([]string{"a", "b", "c"}, 2); len(got) != 2 || len(got[1]) != 1 {
		t.Fatalf("chunkStrings 分块不符: %v", got)
	}
	if got := chunkStrings([]string{"a"}, 0); len(got) != 1 {
		t.Fatalf("非法块大小必须归一化为 1: %v", got)
	}
	if got := sqlPlaceholders(3); got != "?,?,?" {
		t.Fatalf("sqlPlaceholders = %s", got)
	}
	if got := sqlPlaceholders(0); got != "?" {
		t.Fatalf("sqlPlaceholders 下限 = %s", got)
	}
	if got := uniqueNonEmpty([]string{"a", "", "a", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("uniqueNonEmpty = %v", got)
	}
	if accountSummaryAuthorizationID(nil) != "" || accountSummaryQuotaLimited(nil) {
		t.Fatal("nil account 摘要必须返回零值")
	}
	if accountSummaryAuthorizationID(&AccountAuthorizationSummary{AccountAuthorizationID: "aa"}) != "aa" {
		t.Fatal("非 nil account 摘要必须返回授权 ID")
	}
}

// TestWJSharedCacheTransportErrors 固定 Redis shared cache 在传输故障下的
// 错误传播（server 关闭后所有操作必须返回错误而非静默 miss）。
// wjNewMiniredis 启动一个随测试生命周期的 miniredis 并返回已连接客户端。
func wjNewMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

// TestWJSharedCacheTransportErrors 固定 Redis shared cache 在传输故障下的
// 错误传播（server 关闭后所有操作必须返回错误而非静默 miss）。
func TestWJSharedCacheTransportErrors(t *testing.T) {
	server, client := wjNewMiniredis(t)
	cache, err := NewRedisSharedCache(client, "dev", "wj-transport")
	if err != nil {
		t.Fatalf("NewRedisSharedCache: %v", err)
	}
	ctx := context.Background()
	if _, err := cache.Get(ctx, "k", &CachedDecision{}); err != nil {
		t.Fatalf("初始 Get 必须 miss 无错: %v", err)
	}
	server.Close()
	if _, err := cache.Get(ctx, "k", &CachedDecision{}); err == nil {
		t.Fatal("传输故障后 Get 必须报错")
	}
	if err := cache.Set(ctx, "k", CachedDecision{Allowed: true}, time.Minute); err == nil {
		t.Fatal("传输故障后 Set 必须报错")
	}
	if err := cache.Clear(ctx); err == nil {
		t.Fatal("传输故障后 Clear 必须报错")
	}
}

// TestWJRedisStoreConstructionErrors 固定 Redis 存储构造期校验。
func TestWJRedisStoreConstructionErrors(t *testing.T) {
	if _, err := NewRedisRuntimeStateStore(nil, "dev", "s"); err == nil || !strings.Contains(err.Error(), "requires a redis client") {
		t.Fatalf("nil client 必须拒绝: %v", err)
	}
	if _, err := NewRedisSharedCache(nil, "dev", "s"); err == nil || !strings.Contains(err.Error(), "requires a redis client") {
		t.Fatalf("nil client 必须拒绝: %v", err)
	}
	_, client := wjNewMiniredis(t)
	store, err := NewRedisRuntimeStateStore(client, "dev", "s")
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	if _, err := store.key("  "); err == nil || !strings.Contains(err.Error(), "Redis key 不能为空") {
		t.Fatalf("空 key 必须拒绝: %v", err)
	}
	if _, err := store.GetJSON(context.Background(), "s", "  ", &CachedDecision{}); err == nil {
		t.Fatal("空 key 读取必须报错")
	}
	if err := store.SetJSON(context.Background(), "s", "  ", CachedDecision{}, time.Minute); err == nil {
		t.Fatal("空 key 写入必须报错")
	}
	if err := store.Delete(context.Background(), "s", "  "); err == nil {
		t.Fatal("空 key 删除必须报错")
	}
	if normalizeTtlMs(0) != time.Millisecond || normalizeTtlMs(-time.Second) != time.Millisecond {
		t.Fatal("TTL 下限必须为 1ms")
	}
	if normalizeTtlMs(time.Second) != time.Second {
		t.Fatal("合法 TTL 必须原样返回")
	}
	if sanitizeRedisKeyPart("") != "default" || sanitizeRedisKeyPart("  ") != "default" {
		t.Fatal("空 key 部分必须回退 default")
	}
	if got := sanitizeRedisKeyPart("a b/c"); got != "a_b_c" {
		t.Fatalf("非法字符必须替换: %q", got)
	}
}

// TestWJStatsStoreQueryErrors 固定统计读取的查询错误传播：缺表时批量
// 装载必须报错而不是当作零成本。
func TestWJStatsStoreQueryErrors(t *testing.T) {
	db := newTestDB(t, "wj-missing-tables")
	// 只建 totals 表，其余投影缺失。
	if _, err := db.Exec(`CREATE TABLE usage_stats_totals (system_account_id TEXT, scope_type TEXT, scope_id TEXT, total_cost_usd REAL, success_cost_usd REAL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	// LoadCosts：totals 命中后 daily 查询缺表 → 错误传播。
	if _, err := stats.LoadCosts(ctx, CostInput{SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now}, time.UTC); err == nil {
		t.Fatal("daily 缺表必须报错")
	}
	// LoadCostsBatch：totals 查询本身缺列以外的表 → 错误传播。
	if _, err := stats.LoadCostsBatch(ctx, []CostInput{{SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now}}, time.UTC); err == nil {
		t.Fatal("批量装载缺表必须报错")
	}
}

// TestWJConcurrentLoadCostsBatch 固定批量装载在并发下的结果稳定性。
func TestWJConcurrentLoadCostsBatch(t *testing.T) {
	db := newTestDB(t, "wj-batch-conc")
	statsSchema(t, db)
	seedCost(t, db, "usage_stats_totals", []string{"system_account_id", "scope_type", "scope_id", "success_cost_usd"},
		[]any{"s", "api_key", "k", 3})
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	inputs := []CostInput{
		{SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now},
		{SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now}, // 去重
		{SystemAccountID: "s2", ScopeType: "api_key", ScopeID: "k2", Now: now},
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			byKey, err := stats.LoadCostsBatch(ctx, inputs, time.UTC)
			if err != nil {
				errs[slot] = err
				return
			}
			if len(byKey) != 2 {
				errs[slot] = errors.New("去重后必须只有 2 个请求键")
				return
			}
			if got := byKey[CostKey(inputs[0], time.UTC)]; got.Total != 3 {
				errs[slot] = errors.New("totals 必须命中 3")
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}
