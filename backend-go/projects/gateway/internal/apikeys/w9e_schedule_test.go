package apikeys

// w9e 覆盖率战役：补 schedule 归一化/求值全分支、ScheduleJSON/ScheduleStatus、
// zonedParts 边界与 patch/quota 的可达守卫。纯函数直测 + 既有 env。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func w9eScheduleObject(text string) map[string]any {
	t := &testing.T{}
	_ = t
	var decoded any
	_ = json.Unmarshal([]byte(text), &decoded)
	object, _ := decoded.(map[string]any)
	return object
}

func TestW9ENormalizeScheduleBranchMatrix(t *testing.T) {
	// 非对象输入。
	if _, err := NormalizeSchedule("not-object"); err == nil {
		t.Fatal("字符串输入必须失败")
	}
	// 空白对象：缺 enabled。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{}`)); err == nil {
		t.Fatal("缺 enabled 必须失败")
	}
	// enabled=false。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":false}`)); err == nil {
		t.Fatal("enabled=false 必须失败")
	}
	// mode 错误。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"deny_windows"}`)); err == nil {
		t.Fatal("mode 错误必须失败")
	}
	// 时区显式 null / 非字符串 / 空白 / 非法。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":null}`)); err == nil {
		t.Fatal("timezone null 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":3}`)); err == nil {
		t.Fatal("timezone 数字必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":"  "}`)); err == nil {
		t.Fatal("空白 timezone 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":"Mars/Olympus"}`)); err == nil {
		t.Fatal("非法 timezone 必须失败")
	}
	// 合法时区省略 → 回退默认。
	schedule, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}]}`))
	if err != nil || schedule.Timezone == "" {
		t.Fatalf("默认时区 = %+v err=%v", schedule, err)
	}
	// windows 非数组 / 超量 / 非对象元素 / 键不支持 / 时间格式 / start==end / 天数非法。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":"x"}`)); err == nil {
		t.Fatal("windows 非数组必须失败")
	}
	manyWindows := `{"enabled":true,"mode":"allow_windows","windows":[` + strings.Repeat(`{"daysOfWeek":[1],"start":"01:00","end":"02:00"},`, maxScheduleWindows) + `{"daysOfWeek":[1],"start":"03:00","end":"04:00"}]}`
	if _, err := NormalizeSchedule(w9eScheduleObject(manyWindows)); err == nil {
		t.Fatal("超量 windows 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":["x"]}`)); err == nil {
		t.Fatal("窗口非对象必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00","bogus":1}]}`)); err == nil {
		t.Fatal("窗口多余键必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"1:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("开始时间格式必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"01:00","end":"01:00"}]}`)); err == nil {
		t.Fatal("start==end 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[8],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("星期 8 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("空星期必须失败")
	}
	// 重复星期会被去重，不报错。
	dedup, derr := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[2,1,2],"start":"01:00","end":"02:00"}]}`))
	if derr != nil {
		t.Fatalf("重复星期去重失败: %v", derr)
	}
	if len(dedup.Windows) != 1 || len(dedup.Windows[0].DaysOfWeek) != 2 {
		t.Fatalf("去重后 = %+v", dedup.Windows)
	}
	// 空 windows 数组。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[]}`)); err == nil {
		t.Fatal("空 windows 必须失败")
	}
	// dateRange：null/非对象/多余键/格式/倒置/全空。
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","dateRange":null,"windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("dateRange null 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","dateRange":"x","windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("dateRange 非对象必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","dateRange":{"bogus":1},"windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("dateRange 多余键必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","dateRange":{"startDate":"2026-13-01"},"windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("非法开始日期必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","dateRange":{"startDate":"2026-02-01","endDate":"2026-01-01"},"windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`)); err == nil {
		t.Fatal("日期倒置必须失败")
	}
	// 例外：null / 非数组 / 超量 / 非对象 / 多余键 / 空日期 / 动作无效 / deny 带 windows / allow 缺 windows / allow 空 windows。
	base := `{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}],"exceptions":`
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `null}`)); err == nil {
		t.Fatal("exceptions null 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `"x"}`)); err == nil {
		t.Fatal("exceptions 非数组必须失败")
	}
	manyExceptions := base + `[` + strings.Repeat(`{"date":"2026-01-01","action":"deny"},`, maxScheduleExceptions) + `{"date":"2026-02-02","action":"deny"}]}`
	if _, err := NormalizeSchedule(w9eScheduleObject(manyExceptions)); err == nil {
		t.Fatal("超量例外必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `["x"]}`)); err == nil {
		t.Fatal("例外非对象必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `{"bogus":1}` + `]}`)); err == nil {
		t.Fatal("例外多余键必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `[{"date":"","action":"deny"}]}`)); err == nil {
		t.Fatal("空例外日期必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `[{"date":"2026-01-01","action":"bogus"}]}`)); err == nil {
		t.Fatal("非法动作必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `[{"date":"2026-01-01","action":"deny","windows":null}]}`)); err == nil {
		t.Fatal("deny windows null 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `[{"date":"2026-01-01","action":"deny","windows":[]}]}`)); err == nil {
		t.Fatal("deny 带 windows 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `[{"date":"2026-01-01","action":"allow"}]}`)); err == nil {
		t.Fatal("allow 缺 windows 必须失败")
	}
	if _, err := NormalizeSchedule(w9eScheduleObject(base + `[{"date":"2026-01-01","action":"allow","windows":[]}]}`)); err == nil {
		t.Fatal("allow 空 windows 必须失败")
	}
	// 合法 allow 例外窗口不带 daysOfWeek。
	schedule, err = NormalizeSchedule(w9eScheduleObject(base + `[{"date":"2026-01-01","action":"allow","windows":[{"start":"05:00","end":"06:00"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.Exceptions) != 1 || len(schedule.Exceptions[0].Windows) != 1 || len(schedule.Exceptions[0].Windows[0].DaysOfWeek) != 0 {
		t.Fatalf("allow 例外 = %+v", schedule.Exceptions)
	}
}

func TestW9EScheduleJSONRoundTripAndCorruption(t *testing.T) {
	schedule, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":[{"daysOfWeek":[1,2],"start":"09:00","end":"18:00"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, ok := ScheduleJSON(schedule)
	if !ok || encoded == "" {
		t.Fatal("合法 schedule 应可序列化")
	}
	parsed, err := ParseScheduleJSON(encoded)
	if err != nil || parsed == nil || len(parsed.Windows) != 1 {
		t.Fatalf("round trip = %+v err=%v", parsed, err)
	}
	// nil schedule 序列化为空。
	if encoded, ok := ScheduleJSON(nil); ok || encoded != "" {
		t.Fatalf("nil schedule = %q %v", encoded, ok)
	}
	// 空串视为无计划。
	if parsed, err := ParseScheduleJSON(""); err != nil || parsed != nil {
		t.Fatalf("空串 = %+v err=%v", parsed, err)
	}
	// 空白存储是损坏信号。
	if _, err := ParseScheduleJSON("   "); err == nil {
		t.Fatal("空白存储必须失败")
	}
	if _, err := ParseScheduleJSON("{bad"); err == nil {
		t.Fatal("坏 JSON 必须失败")
	}
}

func TestW9EScheduleStatusAndAllows(t *testing.T) {
	schedule, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// 全天窗口内 → active。
	status, ok := ScheduleStatus(schedule, time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	if !ok || status != "active" {
		t.Fatalf("status = %q %v", status, ok)
	}
	// 窗口外 → inactive。
	narrow, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	status, ok = ScheduleStatus(narrow, time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	if !ok || status != "disabled" {
		t.Fatalf("窗口外 status = %q %v", status, ok)
	}
	// 例外 deny 覆盖 → inactive。
	withDeny, err := NormalizeSchedule(w9eScheduleObject(`{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],"exceptions":[{"date":"2026-01-05","action":"deny"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	status, _ = ScheduleStatus(withDeny, time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	if status != "disabled" {
		t.Fatalf("deny 例外应 disabled, got %q", status)
	}
	// nil schedule → 无状态。
	if _, ok := ScheduleStatus(nil, time.Now()); ok {
		t.Fatal("nil schedule 不应有状态")
	}
	// 时区非法回退 UTC。
	if got := scheduleZonedParts(time.Date(2026, 1, 5, 0, 30, 0, 0, time.UTC), "Mars/Olympus"); got.minuteOfDay != 30 {
		t.Fatalf("非法时区应回退 UTC, got %+v", got)
	}
}

func TestW9EStorePureHelpersAndErrors(t *testing.T) {
	// 错误类型 Error() 契约。
	if (&ConflictError{Message: "dup"}).Error() != "dup" {
		t.Fatal("ConflictError.Error 不符")
	}
	if (&ValidationError{Message: "bad"}).Error() != "bad" {
		t.Fatal("ValidationError.Error 不符")
	}
	var secretErr error = &SecretUnavailableError{}
	if secretErr.Error() == "" {
		t.Fatal("SecretUnavailableError.Error 不应为空")
	}
	// AccessScope 语义。
	admin := AccessScope{ViewerID: "u", IsAdmin: true, FilterID: "f"}
	if admin.manageableID() != "f" {
		t.Fatal("admin 应透传 filter")
	}
	user := AccessScope{ViewerID: "u"}
	if user.manageableID() != "u" || user.canAccessAll() {
		t.Fatal("普通用户应锁定自身")
	}
	if _, err := (AccessScope{}).ownerID(); err == nil {
		t.Fatal("无上下文 ownerID 必须失败")
	}
	// 整数比较辅助。
	if minInt(1, 2) != 1 || maxInt(1, 2) != 2 {
		t.Fatal("min/max 不符")
	}
	// 前缀上界。
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("prefix bound = %q", got)
	}
	if got := textPrefixUpperBound(""); got != "\uffff" {
		t.Fatalf("empty prefix bound = %q", got)
	}
	// BusInvalidator：nil Bus 安全短路。
	var nilBus BusInvalidator
	if err := nilBus.InvalidateValidation("k1", "reason", nil); err != nil {
		t.Fatalf("nil bus 应安全: %v", err)
	}
	nilBus.InvalidateQuota("k1", "reason")
}

func TestW9EAPIKeyLifecycleWithFiltersAndDelete(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w9e-root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, admin, "rs-default")

	created := map[string]any{}
	for _, name := range []string{"w9e-alpha", "w9e-bravo", "w9e-charlie"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"`+name+`"}`)
		if code != http.StatusCreated {
			t.Fatalf("create %s = %d %v", name, code, payload)
		}
		if name == "w9e-alpha" {
			created = dataMap(t, payload)
		}
	}
	// 名称过滤。
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?keyword=w9e-bravo", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	items := dataMap(t, list)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("search items = %d", len(items))
	}
	// 分页 + hasMore。
	code, page := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?page=1&pageSize=2", "")
	if code != http.StatusOK {
		t.Fatalf("page = %d", code)
	}
	if len(dataMap(t, page)["items"].([]any)) != 2 {
		t.Fatal("pageSize=2 应返回 2 行")
	}
	// status 过滤 all。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys?status=all", "")
	if code != http.StatusOK {
		t.Fatalf("status=all = %d", code)
	}
	// 详情 + 密文读取。
	keyID := created["id"].(string)
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusOK || dataMap(t, detail)["name"] != "w9e-alpha" {
		t.Fatalf("detail = %d %v", code, detail)
	}
	code, secret := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+keyID+"/secret", "")
	if code != http.StatusOK {
		t.Fatalf("secret = %d %v", code, secret)
	}
	// 刷新密钥。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/api-keys/"+keyID+"/refresh-key", `{}`)
	if code != http.StatusOK {
		t.Fatalf("refresh = %d", code)
	}
	// PATCH 停用（refresh 后 revision 变化，先读详情）。
	code, fresh := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusOK {
		t.Fatalf("fresh detail = %d", code)
	}
	revision := dataMap(t, fresh)["revision"].(string)
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("patch disabled = %d", code)
	}
	// DELETE。
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	// my-api-keys 面（self scope）。
	code, mine := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys", "")
	if code != http.StatusOK {
		t.Fatalf("my list = %d %v", code, mine)
	}
}

func TestW9EJSQueryIntegerEdgeForms(t *testing.T) {
	cases := []struct {
		raw  string
		want int
		ok   bool
	}{
		{"0x10", 16, true},
		{"0o17", 15, true},
		{"0b101", 5, true},
		{"0x", 0, false},
		{"-Infinity", 0, false},
		{"Infinity", 0, false},
		{"1e2", 100, true},
		{"1.0", 1, true},
		{"1.5", 0, false},
		{"1_000", 0, false},
		{"inf", 0, false},
		{"nan", 0, false},
		{"\u00a05", 5, true},
		{"1_0", 0, false},
		{"", 0, false},
		{"999999999999999999999999", 9223372036854775807, true},
	}
	for _, tc := range cases {
		got, ok := jsQueryInteger(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("jsQueryInteger(%q) = %d %v, want %d %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
	// 巨大十六进制字面量按饱和处理。
	if got, ok := jsQueryInteger("0x" + strings.Repeat("f", 20)); !ok || got <= 0 {
		t.Fatalf("huge hex = %d %v", got, ok)
	}
	// 带符号的进制前缀按 JS 语义不识别（radix prefixes without sign）。
	if _, ok := jsQueryInteger("-0x" + strings.Repeat("f", 20)); ok {
		t.Fatal("带符号进制前缀应视为 NaN")
	}
	if got := apiKeyStatusQueryValue("bogus"); got != "" {
		t.Fatalf("bogus status = %q", got)
	}
	if got := apiKeyStatusQueryValue("active"); got != "active" {
		t.Fatalf("active status = %q", got)
	}
}
