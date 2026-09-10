package statreads

// statreads 纯函数契约补充测试（wd_ 前缀，独占新增文件）：
//   - 时间键/范围归一（timekeys.go）：日历钳位、时区感知键、二分找本地零点。
//   - Row 取值器（db.go）：驱动自然类型到 Node Number()/String() 语义的归一。
//   - 访问范围（scope.go）与查询参数镜像、用量记录过滤/排序/失败原因映射。
//   - AI 性能/系统指标解析与进程角色词表。
// 全部为确定性断言，不依赖真实 PG/Redis。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---------------------------------------------------------------------------
// timekeys.go
// ---------------------------------------------------------------------------

func TestWdParseDateKeyPartsRejectsInvalidCalendar(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"格式错误", "2026/09/04", false},
		{"位数不足", "2026-9-4", false},
		{"二月三十", "2026-02-30", false},
		{"十三月", "2026-13-01", false},
		{"合法日期", "2026-09-04", true},
		{"闰日", "2028-02-29", true},
		{"非闰年闰日", "2026-02-29", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok := parseDateKeyParts(tc.value)
			if ok != tc.ok {
				t.Fatalf("parseDateKeyParts(%q) ok = %v, want %v", tc.value, ok, tc.ok)
			}
		})
	}
}

func TestWdDateKeyAndHourKeyInZone(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	instant := time.Date(2026, 9, 4, 20, 30, 0, 0, time.UTC)
	if got := dateKeyIn(instant, zone); got != "2026-09-05" {
		t.Fatalf("dateKeyIn 跨日未按本地时区取键: %q", got)
	}
	if got := hourKeyIn(instant, zone); got != "2026-09-05T04" {
		t.Fatalf("hourKeyIn 结果错误: %q", got)
	}
	if got := dateKeyIn(instant, time.UTC); got != "2026-09-04" {
		t.Fatalf("dateKeyIn UTC 结果错误: %q", got)
	}
}

func TestWdNormalizeRangeClampsTo31DayWindow(t *testing.T) {
	today := "2026-09-04"
	cases := []struct {
		name       string
		start, end string
		wantStart  string
		wantEnd    string
		wantDays   int
	}{
		{"默认今天", "", "", "2026-09-04", "2026-09-04", 1},
		{"未来结束钳到今天", "2026-09-01", "2026-12-31", "2026-09-01", "2026-09-04", 4},
		// 过旧范围：起止都塌缩到最早支持日（today-30），而不是保留原日期。
		{"过旧起点钳到窗口最早", "2020-01-01", "2020-01-05", "2026-08-05", "2026-08-05", 1},
		// 倒置范围不交换，start 直接塌缩到 end（Node normalizeRange 同契约）。
		{"起止倒置塌缩", "2026-09-03", "2026-09-01", "2026-09-01", "2026-09-01", 1},
		{"跨度超过31天钳位", "2026-07-01", "2026-09-04", "2026-08-05", "2026-09-04", 31},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rng := normalizeRange(tc.start, tc.end, today)
			if rng.StartDate != tc.wantStart || rng.EndDate != tc.wantEnd {
				t.Fatalf("范围错误: got %s..%s want %s..%s", rng.StartDate, rng.EndDate, tc.wantStart, tc.wantEnd)
			}
			if rng.Days != tc.wantDays || rng.MaxDays != 31 {
				t.Fatalf("天数错误: got days=%d maxDays=%d want %d/%d", rng.Days, rng.MaxDays, tc.wantDays, 31)
			}
		})
	}
}

func TestWdDateKeysInRangeAndHourBuckets(t *testing.T) {
	if keys := dateKeysInRange(Range{StartDate: "2026-09-04", EndDate: "2026-09-01"}); keys != nil {
		t.Fatalf("起止倒置应返回 nil: %#v", keys)
	}
	if keys := dateKeysInRange(Range{StartDate: "bad", EndDate: "2026-09-04"}); keys != nil {
		t.Fatalf("非法键应返回 nil: %#v", keys)
	}
	keys := dateKeysInRange(Range{StartDate: "2026-08-30", EndDate: "2026-09-02"})
	if len(keys) != 4 || keys[0] != "2026-08-30" || keys[3] != "2026-09-02" {
		t.Fatalf("日期键序列错误: %#v", keys)
	}
	// 跨度超窗时截断到 31 个键。
	oversize := dateKeysInRange(Range{StartDate: "2026-01-01", EndDate: "2026-12-31"})
	if len(oversize) != 31 {
		t.Fatalf("超窗应截断为 31 键: %d", len(oversize))
	}
	if buckets := hourBucketsForRange(Range{StartDate: "bad", EndDate: "2026-09-04"}); buckets != nil {
		t.Fatalf("非法范围的小时桶应为 nil: %#v", buckets)
	}
	buckets := hourBucketsForRange(Range{StartDate: "2026-09-04", EndDate: "2026-09-04"})
	if len(buckets) != 24 || buckets[0] != "2026-09-04T00" || buckets[23] != "2026-09-04T23" {
		t.Fatalf("小时桶错误: %#v", buckets)
	}
}

func TestWdNextCalendarDateKeyAndFixedRangeFallback(t *testing.T) {
	if got := nextCalendarDateKey("2026-09-04"); got != "2026-09-05" {
		t.Fatalf("次日键错误: %q", got)
	}
	// 非法键原样返回（调用方自行处理）。
	if got := nextCalendarDateKey("not-a-date"); got != "not-a-date" {
		t.Fatalf("非法键应原样返回: %q", got)
	}
	bad := fixedUsageStatsDefaultRange("2026-02-30")
	if bad.Days != 1 || bad.StartDate != "2026-02-30" || bad.EndDate != "2026-02-30" || bad.MaxDays != 31 {
		t.Fatalf("非法今日键的一日降级错误: %#v", bad)
	}
	good := fixedUsageStatsDefaultRange("2026-09-04")
	if good.StartDate != "2026-08-05" || good.Days != 31 {
		t.Fatalf("默认 31 天窗口错误: %#v", good)
	}
}

func TestWdStartOfZonedDateKeyIso(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	if got := startOfZonedDateKeyIso("bad-key", time.UTC); got != "" {
		t.Fatalf("非法键应返回空串: %q", got)
	}
	got := startOfZonedDateKeyIso("2026-09-04", zone)
	// 上海 UTC+8：本地零点等于前一日 16:00 UTC。
	if got != "2026-09-03T16:00:00.000Z" {
		t.Fatalf("上海本地零点错误: %q", got)
	}
	gotUTC := startOfZonedDateKeyIso("2026-09-04", time.UTC)
	if gotUTC != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("UTC 本地零点错误: %q", gotUTC)
	}
}

func TestWdHourBucketsUntilNow(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	buckets := hourBucketsUntilNow(3, now, time.UTC)
	if len(buckets) != 3 || buckets[0] != "2026-09-04T10" || buckets[2] != "2026-09-04T12" {
		t.Fatalf("小时桶序列错误: %#v", buckets)
	}
	// hours < 1 钳位为 1。
	if single := hourBucketsUntilNow(0, now, time.UTC); len(single) != 1 || single[0] != "2026-09-04T12" {
		t.Fatalf("hours<1 应钳位为当前小时: %#v", single)
	}
}

func TestWdNumberHelpers(t *testing.T) {
	if got := numberOrZero(nil); got != 0 {
		t.Fatalf("numberOrZero(nil) = %v", got)
	}
	if got := numberOrZero("abc"); got != 0 {
		t.Fatalf("numberOrZero(非数值) = %v", got)
	}
	if got := numberOrUndefined(nil); got != nil {
		t.Fatalf("numberOrUndefined(nil) 应为 nil")
	}
	if got := numberOrUndefined("oops"); got != nil {
		t.Fatalf("numberOrUndefined(不可解析) 应为 nil")
	}
	if got := numberOrUndefined(json.Number("3.5")); got == nil || *got != 3.5 {
		t.Fatalf("numberOrUndefined(json.Number) 错误: %#v", got)
	}
	if avg := averageFromSum("400", int64(4)); avg == nil || *avg != 100 {
		t.Fatalf("averageFromSum 字符串和错误: %#v", avg)
	}
	if avg := averageFromSum(nil, int64(0)); avg != nil {
		t.Fatalf("count=0 应返回 nil: %#v", avg)
	}
	if mx := maxFromCountedMetric(-2.4, 2); mx == nil || *mx != 0 {
		t.Fatalf("负值最大值应钳位为 0: %#v", mx)
	}
	if mx := maxFromCountedMetric(1.5, 0); mx != nil {
		t.Fatalf("count=0 应返回 nil: %#v", mx)
	}
	if got := mathRound(-1.5); got != -2 {
		t.Fatalf("mathRound(-1.5) = %v", got)
	}
	if got := pad2(7); got != "07" {
		t.Fatalf("pad2(7) = %q", got)
	}
	if got := pad2(23); got != "23" {
		t.Fatalf("pad2(23) = %q", got)
	}
	if value, ok := toFloat([]byte("2.5")); !ok || value != 2.5 {
		t.Fatalf("toFloat([]byte) 错误: %v %v", value, ok)
	}
	if _, ok := toFloat(struct{}{}); ok {
		t.Fatalf("toFloat(未知类型) 应失败")
	}
}

func TestWdQueryValueHelpers(t *testing.T) {
	values := url.Values{"page": {" 3 "}, "pageSize": {"x"}, "limit": {"12.5"}, "kw": {" 关键词 "}}
	if got := optionalQueryText(values, "kw"); got != "关键词" {
		t.Fatalf("optionalQueryText 应去空白: %q", got)
	}
	if page, ok := integerQueryValue(values, "page"); !ok || page != 3 {
		t.Fatalf("integerQueryValue 整数解析错误: %d %v", page, ok)
	}
	if _, ok := integerQueryValue(values, "pageSize"); ok {
		t.Fatalf("非整数应视为未提供")
	}
	if _, ok := integerQueryValue(values, "absent"); ok {
		t.Fatalf("缺失键应视为未提供")
	}
	if _, ok := finiteNumberQueryValue(values, "limit"); !ok {
		t.Fatalf("12.5 不是整数但仍是有穷数，应 ok=true")
	}
	if _, ok := finiteNumberQueryValue(values, "pageSize"); ok {
		t.Fatalf("非数值应视为未提供")
	}
	ids := parseAccountIDs([]string{"a, b,,c", "b", "d"})
	if len(ids) != 4 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" || ids[3] != "d" {
		t.Fatalf("parseAccountIDs 应 CSV 拆分去重保序: %#v", ids)
	}
}

// ---------------------------------------------------------------------------
// db.go Row 取值器
// ---------------------------------------------------------------------------

func TestWdRowAccessorsNormalizeDriverTypes(t *testing.T) {
	ts := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	row := Row{
		"text":    "abc",
		"bytes":   []byte("byt"),
		"time":    ts,
		"number":  json.Number("12"),
		"int":     int64(7),
		"float":   2.5,
		"nil":     nil,
		"bool_t":  true,
		"bool_1":  int64(1),
		"bool_0":  float64(0),
		"bool_s1": "1",
		"bool_st": "TRUE",
		"struct":  struct{}{},
	}
	if got := row.text("time"); got != "2026-09-04T10:00:00.000Z" {
		t.Fatalf("time.Time 文本应归一为毫秒 UTC: %q", got)
	}
	if got := row.text("number"); got != "12" {
		t.Fatalf("json.Number 文本错误: %q", got)
	}
	if got := row.text("struct"); got != "{}" {
		t.Fatalf("未知类型文本应走 %v 兜底: %q", "{}", got)
	}
	if row.nullText("nil") != nil {
		t.Fatalf("nil 列 nullText 应为 nil")
	}
	if row.nullNumber("float") == nil || *row.nullNumber("float") != 3 {
		t.Fatalf("nullNumber 应四舍五入: %#v", row.nullNumber("float"))
	}
	if row.nullNumber("nil") != nil {
		t.Fatalf("nil 列 nullNumber 应为 nil")
	}
	if row.nullNumber("text") != nil {
		t.Fatalf("不可解析列 nullNumber 应为 nil")
	}
	if row.nullFloat("int") == nil || *row.nullFloat("int") != 7 {
		t.Fatalf("nullFloat 错误: %#v", row.nullFloat("int"))
	}
	if !row.boolLike("bool_t") || !row.boolLike("bool_1") || row.boolLike("bool_0") ||
		!row.boolLike("bool_s1") || !row.boolLike("bool_st") || row.boolLike("struct") {
		t.Fatalf("boolLike 类型分支错误")
	}
}

func TestWdParseJSONAndAtoiDefault(t *testing.T) {
	var value int
	if err := parseJSON(`"abc"`, &value); err == nil || !strings.Contains(err.Error(), "value_json 解析失败") {
		t.Fatalf("parseJSON 应包装解析错误: %v", err)
	}
	if err := parseJSON(`5`, &value); err != nil || value != 5 {
		t.Fatalf("parseJSON 正常路径错误: %v %d", err, value)
	}
	if got := atoiDefault("nope", 9); got != 9 {
		t.Fatalf("atoiDefault 回退错误: %d", got)
	}
	if got := atoiDefault("42", 9); got != 42 {
		t.Fatalf("atoiDefault 正常错误: %d", got)
	}
	if _, _, err := firstRow(nil, errors.New("boom")); err == nil {
		t.Fatalf("firstRow 应透传查询错误")
	}
	if _, found, err := firstRow(nil, nil); found || err != nil {
		t.Fatalf("firstRow 空集应为 found=false: %v %v", found, err)
	}
}

func TestWdQueryCtxVariants(t *testing.T) {
	fixture := newFixture(t)
	rows, err := fixture.deps.queryStatsCtx(context.Background(), `SELECT 1 AS one`)
	if err != nil || len(rows) != 1 || rows[0].number("one") != 1 {
		t.Fatalf("queryStatsCtx 错误: %v %#v", err, rows)
	}
	rows, err = fixture.deps.queryBusinessCtx(context.Background(), `SELECT 2 AS two`)
	if err != nil || len(rows) != 1 || rows[0].number("two") != 2 {
		t.Fatalf("queryBusinessCtx 错误: %v %#v", err, rows)
	}
}

// ---------------------------------------------------------------------------
// scope.go
// ---------------------------------------------------------------------------

func TestWdAccessScopeHelpers(t *testing.T) {
	admin := AccessScope{ViewerID: "sys-admin", IsAdmin: true}
	if admin.scopedID() != "" || !admin.canAccessAll() {
		t.Fatalf("无过滤 admin 应可访问全局且 scopedID 为空")
	}
	filtered := AccessScope{ViewerID: "sys-admin", IsAdmin: true, FilterID: " sys-user "}
	if filtered.scopedID() != "sys-user" {
		t.Fatalf("admin 过滤 ID 应去空白: %q", filtered.scopedID())
	}
	user := AccessScope{ViewerID: "sys-user"}
	if user.scopedID() != "sys-user" || user.canAccessAll() {
		t.Fatalf("非 admin 应被钉到自己")
	}
	empty := AccessScope{}
	if empty.currentID() != "" {
		t.Fatalf("空 scope currentID 应为空: %q", empty.currentID())
	}
	if (AccessScope{IsAdmin: true}).currentID() != "" {
		t.Fatalf("admin 无 viewer 时 currentID 回退 scopedID，应为空")
	}
}

func TestWdRequestScopeAndSelfScopeWithoutAuth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if scope := requestScope(request); scope.ViewerID != "" || scope.IsAdmin {
		t.Fatalf("无认证上下文应得空 scope: %#v", scope)
	}
	if scope := selfScope(request); scope.ViewerID != "" || scope.IsAdmin {
		t.Fatalf("无认证上下文 selfScope 应为空: %#v", scope)
	}
}

func TestWdRequestScopeFilterSemantics(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/?systemAccountId=sys-other", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), adminAuth("super_admin")))
	scope := requestScope(request)
	if !scope.IsAdmin || scope.FilterID != "sys-other" || scope.ViewerID != "sys-admin-1" {
		t.Fatalf("super_admin 过滤语义错误: %#v", scope)
	}
	allRequest := httptest.NewRequest(http.MethodGet, "/?systemAccountId=all", nil)
	allRequest = allRequest.WithContext(authsys.WithAuthContext(allRequest.Context(), adminAuth("super_admin")))
	if scope := requestScope(allRequest); scope.FilterID != "" {
		t.Fatalf("systemAccountId=all 应清空过滤: %#v", scope)
	}
	userRequest := httptest.NewRequest(http.MethodGet, "/?systemAccountId=sys-other", nil)
	userRequest = userRequest.WithContext(authsys.WithAuthContext(userRequest.Context(), userAuth()))
	scope = requestScope(userRequest)
	if scope.IsAdmin || scope.FilterID != "" || scope.ViewerID != "sys-user-1" {
		t.Fatalf("普通用户应忽略过滤并钉到自己: %#v", scope)
	}
}

// ---------------------------------------------------------------------------
// accountusage.go 纯函数
// ---------------------------------------------------------------------------

func TestWdSchedulableQueryValue(t *testing.T) {
	for _, allowed := range []string{"all", "enabled", "disabled", "cooling"} {
		if got := schedulableQueryValue(allowed); got != allowed {
			t.Fatalf("schedulableQueryValue(%q) = %q", allowed, got)
		}
	}
	if got := schedulableQueryValue(""); got != "" {
		t.Fatalf("空值应返回空串: %q", got)
	}
	if got := schedulableQueryValue("weird"); got != "" {
		t.Fatalf("未知值应返回空串: %q", got)
	}
}

func TestWdScopeIDFilterChunking(t *testing.T) {
	if sql, params := scopeIDFilter(nil); sql != "0 = 1" || params != nil {
		t.Fatalf("空 ID 集应得到恒假过滤: %q %#v", sql, params)
	}
	sql, params := scopeIDFilter([]string{"a", "b", "a", ""})
	if !strings.Contains(sql, "IN (?,?)") || len(params) != 2 {
		t.Fatalf("单 chunk 过滤错误: %q %#v", sql, params)
	}
	// 401 个 ID 触发双 chunk OR 形态。
	ids := make([]string, 401)
	for index := range ids {
		ids[index] = "id-" + strconv.Itoa(index)
	}
	sql, params = scopeIDFilter(ids)
	if !strings.HasPrefix(sql, "(") || !strings.Contains(sql, " OR ") {
		t.Fatalf("多 chunk 应为 OR 复合: %q", sql)
	}
	if len(params) != 401 {
		t.Fatalf("多 chunk 参数数错误: %d", len(params))
	}
}

func integerTextWd(value int) string { return strconv.Itoa(value) }

func TestWdPageAndChunkHelpers(t *testing.T) {
	rows := []Row{{"scope_id": "a"}, {"scope_id": "b"}, {"scope_id": "c"}}
	pageRows, hasMore := takePageRows(rows, 2)
	if !hasMore || len(pageRows) != 2 {
		t.Fatalf("takePageRows 分页错误: %v %d", hasMore, len(pageRows))
	}
	pageRows, hasMore = takePageRows(rows[:2], 2)
	if hasMore || len(pageRows) != 2 {
		t.Fatalf("takePageRows 恰好一页错误: %v %d", hasMore, len(pageRows))
	}
	if got := pagedTotalUpperBound(2, 10, 10, true); got != 21 {
		t.Fatalf("pagedTotalUpperBound 错误: %d", got)
	}
	if clampInt(0, 1, 10) != 1 || clampInt(99, 1, 10) != 10 || clampInt(5, 1, 10) != 5 {
		t.Fatalf("clampInt 错误")
	}
	if chunks := chunkStrings([]string{"a", "b", "c"}, 2); len(chunks) != 2 || len(chunks[0]) != 2 || len(chunks[1]) != 1 {
		t.Fatalf("chunkStrings 错误: %#v", chunks)
	}
	if chunks := chunkStrings([]string{"a"}, 0); len(chunks) != 1 || len(chunks[0]) != 1 {
		t.Fatalf("chunkStrings size<1 应钳位为 1: %#v", chunks)
	}
	params := flatParams("head", []string{"a", "b"}, []any{1, 2}, 3)
	if len(params) != 6 || params[0] != "head" || params[1] != "a" || params[3] != 1 || params[5] != 3 {
		t.Fatalf("flatParams 展平错误: %#v", params)
	}
	if joinAND([]string{"a", "b"}) != "a AND b" {
		t.Fatalf("joinAND 错误")
	}
}

func TestWdMergeAccountUsageSourceRowsDedupes(t *testing.T) {
	pageRows := []Row{{"scope_id": "a"}, {"scope_id": "b"}}
	selected := []Row{{"scope_id": "b"}, {"scope_id": "c"}}
	merged := mergeAccountUsageSourceRows(pageRows, selected)
	if len(merged) != 3 || merged[0].text("scope_id") != "a" || merged[2].text("scope_id") != "c" {
		t.Fatalf("合并去重错误: %#v", merged)
	}
}

func TestWdBoundedKeyword(t *testing.T) {
	if text, tooLong := boundedKeyword(url.Values{}, "kw"); text != "" || tooLong {
		t.Fatalf("缺失键应返回空且未超长: %q %v", text, tooLong)
	}
	if text, tooLong := boundedKeyword(url.Values{"kw": {"  ok "}}, "kw"); text != "ok" || tooLong {
		t.Fatalf("keyword 应去空白: %q %v", text, tooLong)
	}
	long := strings.Repeat("长", 201)
	if _, tooLong := boundedKeyword(url.Values{"kw": {long}}, "kw"); !tooLong {
		t.Fatalf("超过 200 字符应报超长")
	}
}

func TestWdAccountUsageScopes(t *testing.T) {
	if state := accountUsageListScope(AccessScope{ViewerID: "u", IsAdmin: true}); state.systemAccountID != "global" || state.scopeType != "account" {
		t.Fatalf("全局列表范围错误: %#v", state)
	}
	if state := accountUsageListScope(AccessScope{ViewerID: "u", IsAdmin: true, FilterID: "f"}); state.systemAccountID != "f" || state.scopeType != "caller_account" {
		t.Fatalf("过滤列表范围错误: %#v", state)
	}
	if got := accountUsageOptionScope(AccessScope{ViewerID: "u"}); got != "u" {
		t.Fatalf("非 admin 选项范围应钉到自己: %q", got)
	}
	if got := accountUsageOptionScope(AccessScope{ViewerID: "a", IsAdmin: true}); got != "" {
		t.Fatalf("admin 选项范围应为空（全库）: %q", got)
	}
	sysID, scopeID := accountUsageOverviewSummaryScope(AccessScope{ViewerID: "a", IsAdmin: true})
	if sysID != "global" || scopeID != "global" {
		t.Fatalf("admin 汇总范围错误: %q %q", sysID, scopeID)
	}
	sysID, scopeID = accountUsageOverviewSummaryScope(AccessScope{ViewerID: "u", IsAdmin: true, FilterID: "f"})
	if sysID != "f" || scopeID != "f" {
		t.Fatalf("过滤汇总范围错误: %q %q", sysID, scopeID)
	}
}

func TestWdTrendScopesAndEmptyDailyUsage(t *testing.T) {
	metadata := []Row{{"id": "acct-1"}, {"id": ""}}
	scopes := trendScopes(accountUsageScopeState{systemAccountID: "sys-1", scopeType: "account"}, metadata)
	if len(scopes) != 2 || scopes[0].RowKey != "acct-1" || scopes[0].SystemAccountID != "sys-1" || scopes[0].ScopeID != "acct-1" {
		t.Fatalf("trendScopes 错误: %#v", scopes)
	}
	empty := emptyDailyUsage([]string{"2026-09-03", "2026-09-04"})
	if len(empty) != 2 || empty[0].StatDate != "2026-09-03" || empty[0].RequestCount != 0 {
		t.Fatalf("emptyDailyUsage 错误: %#v", empty)
	}
}

func TestWdAccountUsageDailyPointFromRow(t *testing.T) {
	row := Row{
		"stat_date":             "2026-09-04",
		"request_count":         4.0,
		"input_tokens":          10.0,
		"output_tokens":         5.0,
		"cache_read_tokens":     1.0,
		"cache_read_cost_usd":   0.1,
		"cache_write_tokens":    2.0,
		"cache_write_1h_tokens": 3.0,
		"cache_write_cost_usd":  0.2,
		"thinking_tokens":       4.0,
		"input_image_tokens":    5.0,
		"output_image_tokens":   6.0,
		"total_cost":            1.5,
		"last_used_at":          "2026-09-04T10:00:00.000Z",
	}
	point := accountUsageDailyPointFromRow(row, "2026-09-04")
	if point.StatDate != "2026-09-04" || point.RequestCount != 4 || point.TotalTokens != 15 || point.TotalCost != 1.5 {
		t.Fatalf("日点聚合错误: %#v", point)
	}
	if point.LastUsedAt == nil || *point.LastUsedAt != "2026-09-04T10:00:00.000Z" {
		t.Fatalf("日点 lastUsedAt 错误: %#v", point.LastUsedAt)
	}
}

// ---------------------------------------------------------------------------
// usagerecords.go 纯函数
// ---------------------------------------------------------------------------

func TestWdTextPrefixUpperBound(t *testing.T) {
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("文本前缀上界错误: %q", got)
	}
	if got := textPrefixUpperBound("abz"); got != "ab{" {
		t.Fatalf("进位上界错误: %q", got)
	}
	if got := textPrefixUpperBound(""); got != "\U0010ffff" {
		t.Fatalf("空串上界错误: %q", got)
	}
	if got := textPrefixUpperBound("\U0010ffff"); got != "\U0010ffff\U0010ffff" {
		t.Fatalf("最大码点应追加而非进位: %q", got)
	}
}

func TestWdHasAllSystemAccountUnsupportedFilters(t *testing.T) {
	base := url.Values{}
	if hasAllSystemAccountUnsupportedFilters(base) {
		t.Fatalf("无过滤参数应返回 false")
	}
	for _, key := range allSystemAccountUnsupportedFilterKeys {
		values := url.Values{}
		values.Set(key, "x")
		if !hasAllSystemAccountUnsupportedFilters(values) {
			t.Fatalf("键 %s 应触发全局过滤保护", key)
		}
	}
	if !hasAllSystemAccountUnsupportedFilters(url.Values{"sortBy": {"model"}}) {
		t.Fatalf("sortBy=model 应触发保护")
	}
	if !hasAllSystemAccountUnsupportedFilters(url.Values{"sortOrder": {"asc"}}) {
		t.Fatalf("sortOrder=asc 应触发保护")
	}
	if hasAllSystemAccountUnsupportedFilters(url.Values{"sortBy": {"createdAt"}, "sortOrder": {"desc"}}) {
		t.Fatalf("合法排序不应触发保护")
	}
}

func TestWdDateQueryValue(t *testing.T) {
	if got := dateQueryValue(" 2026-09-04 "); got != "2026-09-04" {
		t.Fatalf("合法日期去空白错误: %q", got)
	}
	if got := dateQueryValue("2026-02-30"); got != "" {
		t.Fatalf("非法日历日期应为空: %q", got)
	}
	if got := dateQueryValue("2026/09/04"); got != "" {
		t.Fatalf("非法格式应为空: %q", got)
	}
	if got := dateQueryValue(""); got != "" {
		t.Fatalf("空串应为空: %q", got)
	}
}

func TestWdNormalizeUsageRecordListOptions(t *testing.T) {
	page, pageSize := normalizeUsageRecordListOptions(0, 0)
	if page != 1 || pageSize != 50 {
		t.Fatalf("默认分页错误: %d %d", page, pageSize)
	}
	page, pageSize = normalizeUsageRecordListOptions(-5, 99999)
	if page != 1 || pageSize != 200 {
		t.Fatalf("分页钳位错误: %d %d", page, pageSize)
	}
	// page 超过 1001/1 页上界时钳位到最大页。
	page, _ = normalizeUsageRecordListOptions(99999, 1)
	if page != 1000 {
		t.Fatalf("页码上界应为 (1001-1)/1=1000: %d", page)
	}
}

func TestWdUsageRecordFailureReasonMapping(t *testing.T) {
	if reason := usageRecordListFailureReason(Row{"success": int64(1)}); reason != nil {
		t.Fatalf("成功记录不应有失败原因: %#v", reason)
	}
	downstream := usageRecordListFailureReason(Row{"success": 0, "error_code": "downstream_connection_closed"})
	if downstream == nil || *downstream != "下游连接关闭" {
		t.Fatalf("下游关闭归因错误: %#v", downstream)
	}
	attribution := usageRecordListFailureReason(Row{"success": 0, "failure_attribution": "downstream_closed"})
	if attribution == nil || *attribution != "下游连接关闭" {
		t.Fatalf("downstream_closed 归因错误: %#v", attribution)
	}
	facts := usageRecordListFailureReason(Row{"success": 0, "error_code": "rate_limit_exceeded", "error_message": "慢一点"})
	if facts == nil || *facts != "rate_limit_exceeded | 慢一点" {
		t.Fatalf("事实串应优先: %#v", facts)
	}
	// 行为存疑：usageFailureReasonByErrorCode 映射表在任何非空 error_code 下都
	// 会被事实串分支先返回，仅 error_code 为空时查表得到空串，映射实际不生效；
	// 这里按当前实际行为断言归因回退。
	dependency := usageRecordListFailureReason(Row{"success": 0, "failure_attribution": "account_dependency"})
	if dependency == nil || *dependency != "账户依赖不可用" {
		t.Fatalf("account_dependency 归因错误: %#v", dependency)
	}
	defaultReason := usageRecordListFailureReason(Row{"success": 0})
	if defaultReason == nil || *defaultReason != "请求未正常完成" {
		t.Fatalf("默认归因错误: %#v", defaultReason)
	}
	long := strings.Repeat("长", 501)
	bounded := boundedUsageFailureMessage(long)
	// 截断保留 500 rune + 6 rune 的 " [已截断]" 后缀。
	if bounded == nil || len([]rune(*bounded)) != 506 || !strings.HasSuffix(*bounded, " [已截断]") {
		t.Fatalf("超长错误消息应截断为 506 rune: %d", len([]rune(coalesceStringWd(bounded))))
	}
	if boundedUsageFailureMessage("") != nil {
		t.Fatalf("空消息应为 nil")
	}
	if failureAttributionOrUndefined(nil) != nil {
		t.Fatalf("nil 归因应为 nil")
	}
	if got := failureAttributionOrUndefined(toPointerWd("unknown_value")); got != nil {
		t.Fatalf("未知归因应映射为 undefined: %#v", got)
	}
	if got := failureAttributionOrUndefined(toPointerWd("gateway_policy")); got == nil || *got != "gateway_policy" {
		t.Fatalf("已知归因应保留: %#v", got)
	}
	if !upstreamModelMismatch(toPointerWd("gpt-4"), toPointerWd("gpt-4o")) {
		t.Fatalf("上下游模型不一致应报 mismatch")
	}
	if upstreamModelMismatch(toPointerWd("gpt-4"), toPointerWd("gpt-4")) ||
		upstreamModelMismatch(nil, toPointerWd("gpt-4o")) ||
		upstreamModelMismatch(toPointerWd(" "), toPointerWd("gpt-4o")) {
		t.Fatalf("一致/缺失/空白模型不应报 mismatch")
	}
}

func coalesceStringWd(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func toPointerWd(value string) *string { return &value }

func TestWdUsageRecordCompareRows(t *testing.T) {
	left := Row{"created_at": "2026-09-04T10:00:00.000Z", "id": "u-1"}
	right := Row{"created_at": "2026-09-04T11:00:00.000Z", "id": "u-2"}
	// less(left,right) 语义：DESC 时更晚的记录排前（right 更新 → less(left,right)=false）。
	if compareUsageRecordRows(left, right, sortDesc) {
		t.Fatalf("DESC 下旧记录不应排在更新记录之前")
	}
	if !compareUsageRecordRows(right, left, sortDesc) {
		t.Fatalf("DESC 应新记录在前")
	}
	if !compareUsageRecordRows(left, right, sortAsc) {
		t.Fatalf("ASC 应旧记录在前")
	}
	if compareUsageRecordRows(left, left, sortAsc) {
		t.Fatalf("同键同 id 不应有严格序")
	}
	// created_at 相同时按 id 决胜：DESC 下 id 大者在前。
	older := Row{"created_at": "2026-09-04T10:00:00.000Z", "id": "u-1"}
	newer := Row{"created_at": "2026-09-04T10:00:00.000Z", "id": "u-2"}
	if !compareUsageRecordRows(newer, older, sortDesc) || !compareUsageRecordRows(older, newer, sortAsc) {
		t.Fatalf("id 决胜方向错误")
	}
	// 不可解析时间戳比较为相等，落到 id 决胜（Node 抛错走 500，Go 端排序层
	// 不 panic 即为当前契约）。
	if compareUsageTimestamp("bad", "2026-09-04T10:00:00.000Z") != 0 {
		t.Fatalf("不可解析时间戳应比较为相等")
	}
	if _, ok := parseRFC3339Millis("nope"); ok {
		t.Fatalf("非法时间戳不应解析成功")
	}
}

func TestWdBucketDateKeyHelpers(t *testing.T) {
	if got := bucketDateKeyFromIso("2026-09-04T10:00:00.000Z"); got != "20260904" {
		t.Fatalf("bucket 键错误: %q", got)
	}
	if got := bucketDateKeyFromIso("short"); got != "" {
		t.Fatalf("过短时间应得空 bucket 键: %q", got)
	}
	if ms, ok := bucketDateKeyToUTCms("20260904"); !ok || ms != time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("bucket 键转毫秒错误: %d %v", ms, ok)
	}
	if _, ok := bucketDateKeyToUTCms("2026-09"); ok {
		t.Fatalf("非法 bucket 键应失败")
	}
}

func TestWdErrToString(t *testing.T) {
	if got := errToString(nil); got != "<nil>" {
		t.Fatalf("errToString(nil) = %q", got)
	}
	if got := errToString(errors.New("boom")); got != "boom" {
		t.Fatalf("errToString 错误: %q", got)
	}
}

func TestWdCollectColumnAndOptionalText(t *testing.T) {
	rows := []Row{
		{"account_id": "a", "other": "x"},
		{"account_id": " b "},
		{"account_id": nil},
		{"account_id": "a"},
	}
	if ids := collectColumn(rows, "account_id"); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("collectColumn 去重去空白错误: %#v", ids)
	}
	if optionalText(nil) != "" || optionalText(" v ") != "v" {
		t.Fatalf("optionalText 错误")
	}
}

// ---------------------------------------------------------------------------
// aiperformance.go 纯函数
// ---------------------------------------------------------------------------

func TestWdHasAnyAccountIdsKey(t *testing.T) {
	if hasAnyAccountIdsKey(url.Values{"other": {"x"}}) {
		t.Fatalf("无 accountIds 键应返回 false")
	}
	if !hasAnyAccountIdsKey(url.Values{"accountIds[]": {"a"}}) {
		t.Fatalf("accountIds[] 应命中")
	}
	if !hasAnyAccountIdsKey(url.Values{"accountIds[0]": {"a"}}) {
		t.Fatalf("accountIds[0] 应命中")
	}
}

func TestWdParseSeriesAccountIds(t *testing.T) {
	if _, errText := parseSeriesAccountIds(url.Values{}); errText == "" {
		t.Fatalf("缺少 accountIds 应报错")
	}
	if _, errText := parseSeriesAccountIds(url.Values{"accountIds[0]": {"a"}}); errText == "" {
		t.Fatalf("下标形式应报错")
	}
	if _, errText := parseSeriesAccountIds(url.Values{"accountIds": {"a,b"}}); errText == "" {
		t.Fatalf("CSV 应报错")
	}
	if _, errText := parseSeriesAccountIds(url.Values{"accountIds": {"", ""}}); errText == "" {
		t.Fatalf("空 ID 应报错")
	}
	many := url.Values{}
	for index := 0; index < 21; index++ {
		many.Add("accountIds", "acct-"+strconv.Itoa(index))
	}
	if _, errText := parseSeriesAccountIds(many); errText == "" {
		t.Fatalf("超过 20 个应报错")
	}
	ids, errText := parseSeriesAccountIds(url.Values{"accountIds": {" a ", "a", "b"}, "accountIds[]": {"c"}})
	if errText != "" || len(ids) != 3 || ids[0] != "a" || ids[2] != "c" {
		t.Fatalf("合法重复参数解析错误: %#v %q", ids, errText)
	}
}

func TestWdPostgresSubstringLikePatternEscapes(t *testing.T) {
	if got := postgresSubstringLikePattern(`100%_x\`); got != `100\%\_x\\` {
		t.Fatalf("LIKE 转义错误: %q", got)
	}
}

func TestWdPerfScopeVariants(t *testing.T) {
	deps := &Deps{}
	super := httptest.NewRequest(http.MethodGet, "/?systemAccountId=sys-f", nil)
	super = super.WithContext(authsys.WithAuthContext(super.Context(), adminAuth("super_admin")))
	scope := deps.perfScope(super, false)
	if scope.SystemAccountID != "sys-f" || scope.ScopeType != "caller_account" || !scope.IncludeSystemAccountName {
		t.Fatalf("admin 过滤 perfScope 错误: %#v", scope)
	}
	global := httptest.NewRequest(http.MethodGet, "/", nil)
	global = global.WithContext(authsys.WithAuthContext(global.Context(), adminAuth("super_admin")))
	scope = deps.perfScope(global, false)
	if scope.SystemAccountID != "global" || scope.ScopeType != "account" || !scope.IncludeSystemAccountName {
		t.Fatalf("admin 全局 perfScope 错误: %#v", scope)
	}
	self := httptest.NewRequest(http.MethodGet, "/", nil)
	self = self.WithContext(authsys.WithAuthContext(self.Context(), adminAuth("super_admin")))
	scope = deps.perfScope(self, true)
	if scope.SystemAccountID != "sys-admin-1" || scope.IncludeSystemAccountName {
		t.Fatalf("self perfScope 应钉到调用者且不含系统账户名: %#v", scope)
	}
}

func TestWdAiPerformanceMappersAndHelpers(t *testing.T) {
	if got := mapAiPerformanceSummary(Row{}); got.RequestCount != 0 || got.AverageDurationMs != nil {
		t.Fatalf("空行摘要应全零: %#v", got)
	}
	accounts := mapAiPerformanceAccounts([]aiPerfAccountRow{
		{ID: "a", Name: "A", ProviderCode: "openai", SystemAccountName: toPointerWd("Owner"), AccessType: "authorized", OwnerSystemAccountName: toPointerWd("Boss")},
		{ID: "b", Name: "B", ProviderCode: "openai", SystemAccountName: toPointerWd("Hidden"), AccessType: "owner"},
	}, perfScopeState{IncludeSystemAccountName: true})
	if accounts[0].OwnerSystemAccountName == nil || *accounts[0].OwnerSystemAccountName != "Boss" {
		t.Fatalf("authorized 行应带 owner 名: %#v", accounts[0])
	}
	if accounts[1].AccessType != nil {
		t.Fatalf("owner 行不应带 accessType: %#v", accounts[1])
	}
	hidden := mapAiPerformanceAccounts([]aiPerfAccountRow{{ID: "b", Name: "B", ProviderCode: "openai", SystemAccountName: toPointerWd("Hidden")}}, perfScopeState{IncludeSystemAccountName: false})
	if hidden[0].SystemAccountName != nil {
		t.Fatalf("未授权系统账户名不应导出: %#v", hidden[0])
	}
	if got := containsString([]string{"a", "b"}, "b"); !got || containsString([]string{"a"}, "b") {
		t.Fatalf("containsString 错误")
	}
	if got := uniqueNonEmpty([]string{" x ", "", "x", "y"}); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("uniqueNonEmpty 错误: %#v", got)
	}
	if got := placeholders(0); got != "?" {
		t.Fatalf("placeholders(0) 应钳位为 1: %q", got)
	}
	if got := placeholders(3); got != "?,?,?" {
		t.Fatalf("placeholders(3) 错误: %q", got)
	}
	if got := candidateIds([]aiPerfCandidate{{ID: "a"}, {ID: "b"}}); len(got) != 2 {
		t.Fatalf("candidateIds 错误: %#v", got)
	}
	if got := accountIdsOfCandidates([]aiPerfCandidate{{ID: "z"}}); len(got) != 1 || got[0] != "z" {
		t.Fatalf("accountIdsOfCandidates 错误: %#v", got)
	}
	emptyRange := Range{StartDate: "2026-09-04", EndDate: "2026-09-04"}
	if got := firstHour(emptyRange, nil); got != "2026-09-04T00" {
		t.Fatalf("firstHour 空桶回退错误: %q", got)
	}
	if got := lastHour(emptyRange, nil); got != "2026-09-04T23" {
		t.Fatalf("lastHour 空桶回退错误: %q", got)
	}
	if got := firstHour(emptyRange, []string{"h1"}); got != "h1" {
		t.Fatalf("firstHour 错误: %q", got)
	}
}

// ---------------------------------------------------------------------------
// systemmetrics.go 纯函数
// ---------------------------------------------------------------------------

func TestWdRuntimePaginationAndParse(t *testing.T) {
	if got := roundNonNegative(-1.2); got != 0 {
		t.Fatalf("负数应得 0: %d", got)
	}
	if got := roundNonNegative(nanWd()); got != 0 {
		t.Fatalf("NaN 应得 0: %d", got)
	}
	if got := roundNonNegative(2.5); got != 3 {
		t.Fatalf("四舍五入错误: %d", got)
	}
	page := paginateSystemMetricsRows([]any{1, 2, 3}, 2, 2)
	if page["page"] != 2 || page["total"] != 3 || page["hasMore"] != false {
		t.Fatalf("分页元数据错误: %#v", page)
	}
	if items, ok := page["items"].([]any); !ok || len(items) != 1 || items[0] != 3 {
		t.Fatalf("分页切片错误: %#v", page["items"])
	}
	empty := paginateSystemMetricsRows([]any{}, 3, 10)
	if empty["hasMore"] != false || len(empty["items"].([]any)) != 0 {
		t.Fatalf("越界页应为空: %#v", empty)
	}
}

func nanWd() float64 { zero := 0.0; return zero / zero }

func TestWdParseRuntimePageQuery(t *testing.T) {
	recorder := httptest.NewRecorder()
	page, pageSize, ok := parseRuntimePageQuery(url.Values{"page": {"2"}, "pageSize": {"20"}}, recorder)
	if !ok || page != 2 || pageSize != 20 {
		t.Fatalf("合法分页解析错误: %d %d %v", page, pageSize, ok)
	}
	recorder = httptest.NewRecorder()
	if _, _, ok := parseRuntimePageQuery(url.Values{"page": {"0"}}, recorder); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("page=0 应 400: %d %v", recorder.Code, ok)
	}
	recorder = httptest.NewRecorder()
	if _, _, ok := parseRuntimePageQuery(url.Values{"pageSize": {"9"}}, recorder); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("pageSize=9 应 400: %d %v", recorder.Code, ok)
	}
	recorder = httptest.NewRecorder()
	if _, _, ok := parseRuntimePageQuery(url.Values{"pageSize": {"51"}}, recorder); ok {
		t.Fatalf("pageSize=51 应 400")
	}
	recorder = httptest.NewRecorder()
	if _, _, ok := parseRuntimePageQuery(url.Values{"page": {"x"}}, recorder); ok {
		t.Fatalf("非数字页应 400")
	}
}

func TestWdTrendStatusRolesPerformanceMode(t *testing.T) {
	deps := &Deps{RuntimeMode: "performance"}
	rows := []Row{
		{"process_role": "stats-worker"},
		{"process_role": "gateway:1"},
		{"process_role": "bogus-role"},
		{"process_role": "gateway:1"},
	}
	roles := deps.trendStatusRoles(rows)
	if len(roles) != 2 || roles[0] != "gateway:1" || roles[1] != "stats-worker" {
		t.Fatalf("performance 模式应导出行内有效角色并按文本排序: %#v", roles)
	}
	standalone := (&Deps{}).trendStatusRoles(rows)
	if len(standalone) != len(standaloneProcessRoles) || standalone[0] != "server" {
		t.Fatalf("standalone 模式应保持固定角色表: %#v", standalone)
	}
}

func TestWdFilterValidRoles(t *testing.T) {
	rows := filterValidRoles([]Row{{"process_role": "server"}, {"process_role": "nope"}})
	if len(rows) != 1 || rows[0].text("process_role") != "server" {
		t.Fatalf("filterValidRoles 过滤错误: %#v", rows)
	}
}

// ---------------------------------------------------------------------------
// aihealth.go 纯函数
// ---------------------------------------------------------------------------

func TestWdParseAlphaIntAndBoundedQueryInt(t *testing.T) {
	if _, err := parseAlphaInt(""); err == nil {
		t.Fatalf("空串应报错")
	}
	if _, err := parseAlphaInt("12x"); err == nil {
		t.Fatalf("非数字应报错")
	}
	if _, err := parseAlphaInt("99999999999"); err == nil {
		t.Fatalf("溢出应报错")
	}
	if got, err := parseAlphaInt("042"); err != nil || got != 42 {
		t.Fatalf("parseAlphaInt 错误: %d %v", got, err)
	}
	if got, errText := boundedQueryInt(url.Values{}, "hours", 168, 1, 10); errText != "" || got != 168 {
		t.Fatalf("缺省应回退: %d %q", got, errText)
	}
	if _, errText := boundedQueryInt(url.Values{"hours": {"x"}}, "hours", 168, 1, 10); errText == "" {
		t.Fatalf("非法值应报 invalid")
	}
	if _, errText := boundedQueryInt(url.Values{"hours": {"99"}}, "hours", 168, 1, 10); errText == "" {
		t.Fatalf("超界应报 range")
	}
	if got, errText := boundedQueryInt(url.Values{"hours": {" 5 "}}, "hours", 168, 1, 10); errText != "" || got != 5 {
		t.Fatalf("带空白合法值错误: %d %q", got, errText)
	}
}

func TestWdIsValidCalendarStatHourAndAccountIDTooLong(t *testing.T) {
	if !isValidCalendarStatHour("2026-09-04T25") {
		t.Fatalf("合法日历日期 + 非法小时仍应通过日历校验（小时由正则拒绝）")
	}
	if isValidCalendarStatHour("2026-02-30T10") {
		t.Fatalf("非法日历日期应拒绝")
	}
	if !accountIDTooLong(strings.Repeat("a", 201)) || accountIDTooLong("a") {
		t.Fatalf("accountIDTooLong 阈值错误")
	}
}

func TestWdZonedHourRange(t *testing.T) {
	start, end := zonedHourRange("2026-09-04T10", time.UTC)
	if start != "2026-09-04T10:00:00.000Z" || end != "2026-09-04T11:00:00.000Z" {
		t.Fatalf("UTC 小时窗错误: %q %q", start, end)
	}
	start, end = zonedHourRange("bad", time.UTC)
	if start != "" || end != "" {
		t.Fatalf("非法小时应返回空窗: %q %q", start, end)
	}
}

func TestWdTimestampValue(t *testing.T) {
	if timestampValue("") != -1<<62 {
		t.Fatalf("空时间戳应得极小值")
	}
	if timestampValue("not-a-time") != -1<<62 {
		t.Fatalf("非法时间戳应得极小值")
	}
	if got := timestampValue("2026-09-04T10:00:00.000Z"); got != time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("合法时间戳毫秒错误: %d", got)
	}
}

func TestWdJ1OutcomeMergeHelpers(t *testing.T) {
	location := time.UTC
	hourBuckets := []string{"2026-09-04T10"}
	success := j1Outcome{AccountID: "acct-a", Outcome: "complete_success", ObservedAt: "2026-09-04T10:30:00.000Z"}
	failure := j1Outcome{AccountID: "acct-a", Outcome: "upstream_failure", ObservedAt: "2026-09-04T10:45:00.000Z"}
	stale := j1Outcome{AccountID: "acct-a", Outcome: "stale", ObservedAt: "2026-09-04T10:50:00.000Z"}
	outOfBucket := j1Outcome{AccountID: "acct-b", Outcome: "complete_success", ObservedAt: "2026-09-04T09:10:00.000Z"}
	rows := j1OutcomeHealthRows([]j1Outcome{success, failure, stale, outOfBucket}, hourBuckets, location)
	if len(rows) != 1 {
		t.Fatalf("同账户同小时应只保留最新非 stale: %#v", rows)
	}
	if rows[0].text("status") != "failure" || rows[0].text("account_id") != "acct-a" {
		t.Fatalf("合并行状态错误: %#v", rows[0])
	}
	latest := latestJ1OutcomeForAccount([]j1Outcome{success, failure, stale}, "acct-a")
	if latest == nil || latest.Outcome != "upstream_failure" {
		t.Fatalf("最新 outcome 选择错误: %#v", latest)
	}
	if latest := latestJ1OutcomeForAccount([]j1Outcome{stale}, "acct-a"); latest != nil {
		t.Fatalf("仅 stale 时应无最新 outcome: %#v", latest)
	}
	newest := newestJ1OutcomeHourRow([]j1Outcome{success, failure, outOfBucket}, "2026-09-04T10", location)
	if newest == nil || newest.Outcome != "upstream_failure" {
		t.Fatalf("小时详情最新 outcome 错误: %#v", newest)
	}
	if got := newestJ1OutcomeHourRow([]j1Outcome{outOfBucket}, "2026-09-04T10", location); got != nil {
		t.Fatalf("跨小时 outcome 不应命中: %#v", got)
	}
	if (j1Outcome{Outcome: "stale"}).Status() != "" || (j1Outcome{Outcome: "complete_success"}).Status() != "success" ||
		(j1Outcome{Outcome: "other"}).Status() != "failure" {
		t.Fatalf("Status 映射错误")
	}
	nullable := nullableAny(toPointerWd("x"))
	if nullable != "x" || nullableAny[string](nil) != nil {
		t.Fatalf("nullableAny 错误")
	}
	if got := dedupeStrings([]string{"a", "b", "a"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("dedupeStrings 错误: %#v", got)
	}
}

func TestWdAccountNameSearchQueryTerms(t *testing.T) {
	if terms := accountNameSearchQueryTerms("   "); terms != nil {
		t.Fatalf("空白关键字应无词项: %#v", terms)
	}
	long := strings.Repeat("字", 129)
	if terms := accountNameSearchQueryTerms(long); terms != nil {
		t.Fatalf("超过 128 rune 应无词项")
	}
	terms := accountNameSearchQueryTerms(" GPT-4 ")
	// "GPT-4" 5 个 rune，3-gram 依次为 GPT / PT- / T-4。
	if len(terms) != 3 || terms[0] != "GPT" || terms[2] != "T-4" {
		t.Fatalf("n-gram 词项错误: %#v", terms)
	}
	// "aaaa" 收缩为 3-gram 后仅剩一个去重词项。
	if dup := accountNameSearchQueryTerms("aaaa"); len(dup) != 1 || dup[0] != "aaa" {
		t.Fatalf("n-gram 去重错误: %#v", dup)
	}
	if normalizeAccountNameSearchText(" x ") != "x" {
		t.Fatalf("规范化应仅去首尾空白")
	}
}

func TestWdSectionFromPathAndQueryIsPresent(t *testing.T) {
	if got := sectionFromPath("/x/usage-overview/summary/"); got != "summary" {
		t.Fatalf("section 解析错误: %q", got)
	}
	if got := sectionFromPath("/no-marker"); got != "" {
		t.Fatalf("无标记应得空 section: %q", got)
	}
	if !queryIsPresent(url.Values{"includeSummary": {""}}, "includeSummary") {
		t.Fatalf("存在即视为提供")
	}
	if queryIsPresent(url.Values{}, "includeSummary") {
		t.Fatalf("缺失键不应视为提供")
	}
}

func TestWdParseUsageOverviewQuery(t *testing.T) {
	start, end, bad := parseUsageOverviewQuery(url.Values{"startDate": {" 2026-09-04 "}, "endDate": {"2026-09-05"}})
	if bad != "" || start != "2026-09-04" || end != "2026-09-05" {
		t.Fatalf("合法查询解析错误: %q %q %q", start, end, bad)
	}
	_, _, bad = parseUsageOverviewQuery(url.Values{"endDate": {"20260905"}})
	if bad != "结束日期格式应为 YYYY-MM-DD" {
		t.Fatalf("非法结束日期应报格式错误: %q", bad)
	}
	_, _, bad = parseUsageOverviewQuery(url.Values{"startDate": {"x"}})
	if bad != "开始日期格式应为 YYYY-MM-DD" {
		t.Fatalf("非法开始日期应报格式错误: %q", bad)
	}
}

func TestWdRangeWindowKeyAndStatsScope(t *testing.T) {
	rng := Range{StartDate: "2026-09-01", EndDate: "2026-09-04"}
	if got := rangeWindowKey(rng); got != "2026-09-01:2026-09-04" {
		t.Fatalf("窗口键错误: %q", got)
	}
	sysID, scopeID := usageOverviewStatsScope(AccessScope{ViewerID: "u"})
	if sysID != "u" || scopeID != "u" {
		t.Fatalf("非 admin 汇总范围错误: %q %q", sysID, scopeID)
	}
	sysID, scopeID = usageOverviewStatsScope(AccessScope{ViewerID: "a", IsAdmin: true})
	if sysID != "global" || scopeID != "global" {
		t.Fatalf("admin 汇总范围错误: %q %q", sysID, scopeID)
	}
}

func TestWdTimezoneLocationSourceError(t *testing.T) {
	if _, err := timezoneLocation(context.Background(), func(context.Context) (string, error) {
		return "", errors.New("缺少设置")
	}); err == nil {
		t.Fatalf("source 失败应透传错误")
	}
	location, err := timezoneLocation(context.Background(), func(context.Context) (string, error) {
		return "UTC", nil
	})
	if err != nil || location.String() != "UTC" {
		t.Fatalf("UTC 解析错误: %v %v", location, err)
	}
}

func TestWdSystemSettingsTimezoneSourceBranches(t *testing.T) {
	// PG 方言只改表名前缀；SQLite 上直接调用会因限定表不存在而报错，
	// 这正是"PG 走 juhe_business 前缀"的可观察差异。
	pgSource := NewSystemSettingsTimezoneSource(newFixture(t).db, true)
	if _, err := pgSource(nil); err == nil {
		t.Fatalf("PG 限定表在 SQLite 测试库上应报错（验证表名前缀路径）")
	}
	fixture := newFixture(t)
	source := NewSystemSettingsTimezoneSource(fixture.db, false)
	if _, err := source(nil); err != nil {
		t.Fatalf("已 seed UTC 的库应能解析时区: %v", err)
	}
	// NULL value_json → 明确的配置错误。
	if _, err := fixture.db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'x', 'null', 't')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := source(nil); err != nil {
		t.Fatalf("无关键不应影响 usageStatsTimezone 读取: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE system_settings SET value_json = 'not-json' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := source(nil); err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("非法 JSON 应报无效: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE system_settings SET value_json = '"   "' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := source(nil); err == nil || !strings.Contains(err.Error(), "非空字符串") {
		t.Fatalf("空白时区应报非空: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE system_settings SET value_json = '"Mars/Phobos"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := source(nil); err == nil || !strings.Contains(err.Error(), "时区不存在") {
		t.Fatalf("未知时区应报不存在: %v", err)
	}
}

func TestWdWriteReadErrorContract(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Deps{}).writeReadError(recorder, errors.New("任意存储错误"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误应映射 500: %d", recorder.Code)
	}
}
