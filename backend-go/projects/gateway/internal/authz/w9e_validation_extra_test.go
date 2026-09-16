package authz

// w9e 覆盖率战役：补 validation.go 的媒体类型/严格解码/查询解析全分支、
// limits 归一化纯函数与 return_group/expiry 的可达分支。本地构造无外部依赖。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// w9eMarkerRecorder 模拟 kernel 的 upstream 标记能力，验证英文 zod 消息原样透传。
type w9eMarkerRecorder struct {
	*httptest.ResponseRecorder
	upstream bool
}

func (w *w9eMarkerRecorder) MarkUpstream()        { w.upstream = true }
func (w *w9eMarkerRecorder) MarkedUpstream() bool { return w.upstream }

func TestW9EJSONBodyGateAndStrictDecode(t *testing.T) {
	header := http.Header{}
	if jsonBodyOnly(header) {
		t.Fatal("空 Content-Type 不应命中")
	}
	header.Set("Content-Type", "application/json; charset=utf-8")
	if !jsonBodyOnly(header) {
		t.Fatal("带参数的 JSON 应命中")
	}
	header.Set("Content-Type", "APPLICATION/JSON")
	if !jsonBodyOnly(header) {
		t.Fatal("大小写不敏感应命中")
	}
	header.Set("Content-Type", "text/plain")
	if jsonBodyOnly(header) {
		t.Fatal("text/plain 不应命中")
	}

	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	if requestHasBody(r) {
		t.Fatal("无 body 请求不应有 body")
	}
	r.Header.Set("Transfer-Encoding", "chunked")
	if !requestHasBody(r) {
		t.Fatal("chunked 应有 body")
	}
	r2 := httptest.NewRequest(http.MethodPost, "/x", nil)
	r2.Header.Set("Content-Length", "abc")
	if requestHasBody(r2) {
		t.Fatal("非法 content-length 不应有 body")
	}

	// decodeStrictJSON：非 JSON 媒体类型跳过（返回 true 且 target 不变）。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "text/plain")
	var target struct {
		A int `json:"a"`
	}
	if !decodeStrictJSON(rec, req, &target, map[string]bool{"a": true}) {
		t.Fatal("非 JSON 媒体类型应放行")
	}
	// 空白 body 放行。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("   "))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 3
	if !decodeStrictJSON(rec, req, &target, map[string]bool{"a": true}) {
		t.Fatal("空白 body 应放行")
	}
	// 未知键 → 英文 zod 消息 + upstream 标记。
	marker := &w9eMarkerRecorder{ResponseRecorder: httptest.NewRecorder()}
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1,"b":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Length", "13")
	if decodeStrictJSON(marker, req, &target, map[string]bool{"a": true}) {
		t.Fatal("未知键必须拒绝")
	}
	if !marker.upstream || !strings.Contains(marker.Body.String(), "Unrecognized key(s) in object: 'b'") {
		t.Fatalf("unknown key = %d %s upstream=%v", marker.Code, marker.Body.String(), marker.upstream)
	}
	// 坏 JSON。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Length", "5")
	if decodeStrictJSON(rec, req, &target, map[string]bool{"a": true}) {
		t.Fatal("坏 JSON 必须拒绝")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", rec.Code)
	}
	// 合法路径。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":7}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Length", "7")
	if !decodeStrictJSON(rec, req, &target, map[string]bool{"a": true}) || target.A != 7 {
		t.Fatalf("合法解码失败: %d %+v", rec.Code, target)
	}
}

func TestW9EQueryParserAllBranches(t *testing.T) {
	mk := func(query string) *queryParser {
		return newQueryParser(httptest.NewRequest(http.MethodGet, "/x?"+query, nil))
	}
	// text：缺失 / 空白。
	if _, ok := mk("").text("q", "必填"); !ok {
		t.Fatal("缺失键应放行")
	}
	p := mk("q=")
	if _, ok := p.text("q", "必填"); ok || p.issue != "必填" {
		t.Fatalf("空白 text = %v %q", ok, p.issue)
	}
	// enum：缺失 / 命中 / 非法。
	p = mk("s=use")
	if v, ok := p.enum("s", "use", "manage"); !ok || v != "use" {
		t.Fatalf("enum = %q %v", v, ok)
	}
	p = mk("s=bogus")
	if _, ok := p.enum("s", "use", "manage"); ok {
		t.Fatal("非法 enum 必须失败")
	}
	if !strings.Contains(p.issue, "Invalid enum value. Expected 'use' | 'manage'") {
		t.Fatalf("enum issue = %q", p.issue)
	}
	// date：缺失 / 合法 / 非法。
	p = mk("d=2026-09-16")
	if v, ok := p.date("d", "日期格式无效"); !ok || v != "2026-09-16" {
		t.Fatalf("date = %q %v", v, ok)
	}
	p = mk("d=2026/09/16")
	if _, ok := p.date("d", "日期格式无效"); ok {
		t.Fatal("非法 date 必须失败")
	}
	// int：缺失 / 空白 / 非数字 / 小数 / 0 / 超上限 / 合法。
	p = mk("n=")
	if _, ok := p.int("n", "至少为 1", 100, "至多为 100"); ok || p.issue != "至少为 1" {
		t.Fatalf("空 int = %v %q", ok, p.issue)
	}
	p = mk("n=abc")
	if _, ok := p.int("n", "至少为 1", 100, "至多为 100"); ok || !strings.Contains(p.issue, "received nan") {
		t.Fatalf("非数字 int = %v %q", ok, p.issue)
	}
	p = mk("n=1.5")
	if _, ok := p.int("n", "至少为 1", 100, "至多为 100"); ok || !strings.Contains(p.issue, "received 1.5") {
		t.Fatalf("小数 int = %v %q", ok, p.issue)
	}
	p = mk("n=0")
	if _, ok := p.int("n", "至少为 1", 100, "至多为 100"); ok || p.issue != "至少为 1" {
		t.Fatalf("0 int = %v %q", ok, p.issue)
	}
	p = mk("n=101")
	if _, ok := p.int("n", "至少为 1", 100, "至多为 100"); ok || p.issue != "至多为 100" {
		t.Fatalf("超上限 int = %v %q", ok, p.issue)
	}
	p = mk("n=50")
	if v, ok := p.int("n", "至少为 1", 0, ""); !ok || v != 50 {
		t.Fatalf("无上限 int = %d %v", v, ok)
	}
	// 首个 issue 优先。
	p = mk("q=&n=abc")
	_, _ = p.text("q", "文本必填")
	_, _ = p.int("n", "数值至少为 1", 100, "至多为 100")
	if p.issue != "文本必填" {
		t.Fatalf("首个 issue = %q", p.issue)
	}
	// writeBadRequest：英文消息打 upstream 标记，中文原样。
	p = mk("s=bogus")
	_, _ = p.enum("s", "use")
	rec := httptest.NewRecorder()
	if p.writeBadRequest(rec, "") {
		t.Fatal("有 issue 必须拒绝")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad request = %d", rec.Code)
	}
	p = mk("q=")
	_, _ = p.text("q", "中文必填消息")
	rec = httptest.NewRecorder()
	if p.writeBadRequest(rec, "") {
		t.Fatal("有中文 issue 必须拒绝且不打 upstream 标记")
	}
	// containsCJK。
	if !containsCJK("中文") || containsCJK("english") {
		t.Fatal("containsCJK 契约不符")
	}
	// sortStrings。
	values := []string{"b", "c", "a"}
	sortStrings(values)
	if strings.Join(values, ",") != "a,b,c" {
		t.Fatalf("sortStrings = %v", values)
	}
}

func TestW9ELimitsNormalizationHelpers(t *testing.T) {
	// 合法 limits 规范化。
	normalized, err := normalizeAuthorizationLimitsJSON(`{"daily":{"enabled":true,"limit":10.123456}}`)
	if err != nil || normalized == nil {
		t.Fatalf("normalize = %v err=%v", normalized, err)
	}
	// 空/空白 → nil（SQL NULL）。
	if got, err := normalizeAuthorizationLimitsJSON(""); err != nil || got != nil {
		t.Fatalf("empty = %v err=%v", got, err)
	}
	if got, err := normalizeAuthorizationLimitsJSON("   "); err != nil || got != nil {
		t.Fatalf("blank = %v err=%v", got, err)
	}
	// 非法字段 → 错误。
	if _, err := normalizeAuthorizationLimitsJSON(`{"foo":{"enabled":true,"limit":1}}`); err == nil {
		t.Fatal("未知 limits 字段必须失败")
	}
	// canonical：nil/空白 → 空。
	if got := canonicalAuthorizationLimits(nil); got != "" {
		t.Fatalf("nil canonical = %q", got)
	}
	if got := canonicalAuthorizationLimits(strPtrW9E("  ")); got != "" {
		t.Fatalf("blank canonical = %q", got)
	}
	// canonical：非法存储原样返回（读路径兜底）。
	raw := "{not-json"
	if got := canonicalAuthorizationLimits(strPtrW9E(raw)); got != raw {
		t.Fatalf("invalid canonical = %q", got)
	}
	// decodeAuthorizationLimits：空/坏/空对象 → nil。
	if decodeAuthorizationLimits("") != nil {
		t.Fatal("空 limits 应为 nil")
	}
	if decodeAuthorizationLimits("{bad") != nil {
		t.Fatal("坏 limits 应为 nil")
	}
	if decodeAuthorizationLimits("{}") != nil {
		t.Fatal("空对象 limits 应为 nil")
	}
	if decodeAuthorizationLimits(`{"daily":{"enabled":true,"limit":1}}`) == nil {
		t.Fatal("非空 limits 不应为 nil")
	}
}

func strPtrW9E(v string) *string { return &v }

func TestW9EExpiryPureHelpers(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	// parseAuthorizationRFC3339Instant。
	if _, ok := parseAuthorizationRFC3339Instant("not-a-time"); ok {
		t.Fatal("非法时间不应解析")
	}
	if _, ok := parseAuthorizationRFC3339Instant("2026-09-16T12:00:00+99:00"); ok {
		t.Fatal("非法 offset 不应解析")
	}
	if parsed, ok := parseAuthorizationRFC3339Instant("2026-09-16T20:00:00+08:00"); !ok || parsed.UTC().Hour() != 12 {
		t.Fatalf("带 offset 解析 = %v %v", parsed, ok)
	}

	// validateAuthorizationCreateExpiresAt。
	if _, err := validateAuthorizationCreateExpiresAt(nil, nil, now); err != nil {
		t.Fatalf("nil = %v", err)
	}
	bad := "oops"
	if _, err := validateAuthorizationCreateExpiresAt(&bad, nil, now); err == nil || !strings.Contains(err.Error(), "格式不正确") {
		t.Fatalf("非法格式 = %v", err)
	}
	past := "2020-01-01T00:00:00Z"
	if _, err := validateAuthorizationCreateExpiresAt(&past, nil, now); err == nil || !strings.Contains(err.Error(), "不能早于当前时间") {
		t.Fatalf("过去时间 = %v", err)
	}
	future := "2027-01-01T00:00:00Z"
	accountBad := "oops"
	if _, err := validateAuthorizationCreateExpiresAt(&future, &accountBad, now); err == nil || !strings.Contains(err.Error(), "账户到期时间必须是带 Z 或数值 offset 的 RFC3339 时间") {
		t.Fatalf("账户到期非法 = %v", err)
	}
	if _, err := validateAuthorizationCreateExpiresAt(&future, &past, now); err == nil || !strings.Contains(err.Error(), "不能晚于账户到期时间") {
		t.Fatalf("早于账户到期（账户已过期）= %v", err)
	}
	beyond := "2028-01-01T00:00:00Z"
	if _, err := validateAuthorizationCreateExpiresAt(&beyond, &future, now); err == nil || !strings.Contains(err.Error(), "不能晚于账户到期时间") {
		t.Fatalf("超出账户到期 = %v", err)
	}
	if got, err := validateAuthorizationCreateExpiresAt(&future, nil, now); err != nil || got == nil {
		t.Fatalf("合法 = %v err=%v", got, err)
	}

	// canonicalizeAuthorizationInstant 与版本比较。
	if got := canonicalizeAuthorizationInstant("2026-09-16T20:00:00+08:00"); got == "" {
		t.Fatal("canonicalize 不应为空")
	}
	if !authorizationVersionEqual("2026-09-16T12:00:00Z", "2026-09-16T12:00:00.000Z") {
		t.Fatal("毫秒等价应判定相等")
	}
	if authorizationVersionEqual("2026-09-16T12:00:00Z", "2026-09-17T12:00:00Z") {
		t.Fatal("不同时间不应相等")
	}
	if authorizationVersionEqual("bogus", "2026-09-16T12:00:00Z") {
		t.Fatal("非法版本不应相等")
	}

	// authorizationExpiresPassed。
	if authorizationExpiresPassed("", now) {
		t.Fatal("NULL 永不过期")
	}
	if authorizationExpiresPassed("bogus", now) {
		t.Fatal("非法时间不过期")
	}
	if !authorizationExpiresPassed("2020-01-01T00:00:00Z", now) {
		t.Fatal("过去时间应判定过期")
	}

	// parseTimeOrNow：合法解析与非法回退（只验证非 nil 与时区）。
	if parsed := parseTimeOrNow("2026-09-16T12:00:00Z"); parsed.IsZero() {
		t.Fatal("合法解析不应为零值")
	}
	if _, err := time.Parse(time.RFC3339Nano, "bogus"); err == nil {
		t.Fatal("预置条件不符")
	}

	// instantMilliseconds。
	if _, ok := instantMilliseconds("2026-09-16T12:00:00Z"); !ok {
		t.Fatal("合法 instant 应解析")
	}
	if _, ok := instantMilliseconds("bogus"); ok {
		t.Fatal("非法 instant 不应解析")
	}
}
