package proxyprofiles

// 路由层与探测辅助函数的补充契约测试：HTTP handler 全流程（options/list/
// patch/delete）、查询参数的 ECMAScript 数字文法、输入校验分支、操作日志
// 差异与写失败映射，外加探测消息与代理 URL 构造的边缘。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func wjJSONRequest(t *testing.T, method, target, body string, auth *authsys.AuthContext) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if body != "" {
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	}
	return request
}

// TestWJProxyOptionsAndListHandlers 固定 options 与列表 handler 的参数契约。
func TestWJProxyOptionsAndListHandlers(t *testing.T) {
	fixture := newProxyFixture(t)
	fixture.seedProfile(t, "p-1", "2026-01-01", "unknown")

	// options：非法 selectedIds → 400。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?selectedIds=a,b", nil)
	optionsHandler(recorder, request, fixture.store)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("csv selectedIds 必须 400: %d", recorder.Code)
	}

	// options：合法查询 → 200。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?limit=10&keyword=Beta", nil)
	optionsHandler(recorder, request, fixture.store)
	if recorder.Code != http.StatusOK {
		t.Fatalf("options 必须 200: %d %s", recorder.Code, recorder.Body.String())
	}

	// options：存储错误 → 500。
	broken := newProxyFixture(t)
	if err := broken.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	optionsHandler(recorder, request, broken.store)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误必须 500: %d", recorder.Code)
	}

	// list：分页参数 → 200。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies?page=1&pageSize=10", nil)
	listHandler(fixture.store)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list 必须 200: %d %s", recorder.Code, recorder.Body.String())
	}
	// list：非法数字（1.8）→ 回退默认 → 200。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies?page=1.8&pageSize=abc", nil)
	listHandler(fixture.store)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list 非法数字必须回退 200: %d", recorder.Code)
	}
	// list：存储错误 → 500。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies", nil)
	listHandler(broken.store)(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list 存储错误必须 500: %d", recorder.Code)
	}
}

// TestWJIntegerQueryValueGrammar 固定查询数字的 ECMAScript 文法。
func TestWJIntegerQueryValueGrammar(t *testing.T) {
	tests := []struct {
		raw     string
		want    float64
		present bool
	}{
		{"", 0, false},
		{"  ", 0, false},
		{"42", 42, true},
		{" 7 ", 7, true},
		{"+8", 8, true},
		{"-9", -9, true},
		{"0x10", 16, true},
		{"0o17", 15, true},
		{"0b101", 5, true},
		{"1e2", 100, true},
		{"1e-400", 0, true},
		{"Infinity", 0, false},
		{"-Infinity", 0, false},
		{"1.8", 0, false},
		{"abc", 0, false},
		{"-0x10", 0, false},
		{"0xZZ", 0, false},
		{"NaN", 0, false},
		{"9e999", 0, false},
	}
	for _, tt := range tests {
		got, present := integerQueryValue(tt.raw)
		if present != tt.present || (present && got != tt.want) {
			t.Fatalf("integerQueryValue(%q) = (%v, %v), 期望 (%v, %v)", tt.raw, got, present, tt.want, tt.present)
		}
	}
}

// TestWJOptionLimitAndIntFromQuery 固定 limit 钳制与 int 饱和。
func TestWJOptionLimitAndIntFromQuery(t *testing.T) {
	if optionLimitValue(0, false) != 50 {
		t.Fatal("无 limit 必须 50")
	}
	if optionLimitValue(51, true) != 50 || optionLimitValue(0, true) != 1 || optionLimitValue(10, true) != 10 {
		t.Fatal("limit 钳制不符")
	}
	if intFromQuery(0, false, 20) != 20 {
		t.Fatal("absent 必须回退")
	}
	if intFromQuery(1e300, true, 20) != int(^uint(0)>>1) {
		t.Fatal("正溢出必须饱和 MaxInt")
	}
	if intFromQuery(-1e300, true, 20) != -int(^uint(0)>>1)-1 {
		t.Fatal("负溢出必须饱和 MinInt")
	}
	if intFromQuery(7, true, 20) != 7 {
		t.Fatal("正常值必须透传")
	}
}

// TestWJParseProxyInputValidation 固定输入校验的全部拒绝分支。
func TestWJParseProxyInputValidation(t *testing.T) {
	bad := func(body map[string]any, update bool) {
		t.Helper()
		if _, message := parseProxyInput(body, update); message != "代理参数无效" {
			t.Fatalf("body %v (update=%v) 必须拒绝, got %q", body, update, message)
		}
	}
	bad(map[string]any{"unknown": 1}, false)
	bad(map[string]any{"name": 1}, false)
	bad(map[string]any{"description": 3}, false)
	bad(map[string]any{"type": 1}, false)
	bad(map[string]any{"host": true}, false)
	bad(map[string]any{"port": "80"}, false)
	bad(map[string]any{"port": 80.5}, false)
	bad(map[string]any{"username": 1}, false)
	bad(map[string]any{"password": nil}, false)
	bad(map[string]any{"enabled": "yes"}, false)
	// create 缺必填。
	bad(map[string]any{"type": "http"}, false)
	bad(map[string]any{"name": "n"}, false)
	bad(map[string]any{"name": "  "}, false)
	// update 缺 expectedUpdatedAt / 非法时间 / 只有 expectedUpdatedAt。
	bad(map[string]any{"name": "n"}, true)
	bad(map[string]any{"name": "n", "expectedUpdatedAt": nil}, true)
	bad(map[string]any{"name": "n", "expectedUpdatedAt": "not-a-time"}, true)
	bad(map[string]any{"expectedUpdatedAt": "2026-09-04T00:00:00Z"}, true)
	// 归一化失败：port 越界。
	bad(map[string]any{"name": "n", "type": "http", "host": "h", "port": 70000}, false)

	// 合法 create。
	input, message := parseProxyInput(map[string]any{
		"name": "n", "type": "http", "host": " h ", "port": float64(8080),
		"description": nil, "username": "u", "password": "p", "enabled": true,
	}, false)
	if message != "" || input.Name == nil || input.Host == nil || *input.Host != "h" || !input.HasPassword {
		t.Fatalf("合法 create 解析不符: (%+v, %q)", input, message)
	}
	// 合法 update。
	input, message = parseProxyInput(map[string]any{
		"name": "n2", "expectedUpdatedAt": "2026-09-04T12:00:00Z",
	}, true)
	if message != "" || input.ExpectedUpdatedAt != "2026-09-04T12:00:00Z" {
		t.Fatalf("合法 update 解析不符: (%+v, %q)", input, message)
	}
	if validRFC3339("2026-09-04T12:00:00Z") != true || validRFC3339("") != false || validRFC3339("nope") != false {
		t.Fatal("validRFC3339 不符")
	}
}

// TestWJWriteMutationErrorMapping 固定写错误到 HTTP 状态的映射。
func TestWJWriteMutationErrorMapping(t *testing.T) {
	// 冲突 → 409。
	recorder := httptest.NewRecorder()
	writeMutationError(recorder, ErrConflict, "fallback")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("ErrConflict 必须 409: %d", recorder.Code)
	}
	// 重名 → 409。
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, &DuplicateNameError{Name: "重名"}, "fallback")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("DuplicateName 必须 409: %d", recorder.Code)
	}
	// 在用 → 409。
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, &InUseError{AccountCount: 2, AccountNames: []string{"a", "b"}}, "fallback")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("InUse 必须 409: %d", recorder.Code)
	}
	// 普通错误 → 400（kernel 渲染通用文案，不透传内部错误文本）。
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, errors.New("bad input"), "fallback")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("普通错误必须 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// nil 错误 → 400（fallback 文案同样被 kernel 包装为通用消息）。
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, nil, "fallback")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("nil 错误必须 400: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestWJOperationLogChangeHelpers 固定操作日志字段归一化与差异。
func TestWJOperationLogChangeHelpers(t *testing.T) {
	if sensitiveFingerprint(" secret ") != "[set]" || sensitiveFingerprint("") != "" || sensitiveFingerprint(nil) != "" {
		t.Fatal("sensitiveFingerprint 不符")
	}
	if textOrNil(nil) != nil || textOrNil(&[]string{"v"}[0]) != "v" {
		t.Fatal("textOrNil 不符")
	}
	latency := int64(42)
	if int64OrNil(nil) != nil || int64OrNil(&latency) != int64(42) {
		t.Fatal("int64OrNil 不符")
	}
	if stringOrNil("") != nil || stringOrNil("ip") != "ip" {
		t.Fatal("stringOrNil 不符")
	}
	if comparableValue(nil) != "null" || comparableValue(1) != "1" {
		t.Fatal("comparableValue 不符")
	}
	if comparableValue(make(chan int)) != "null" {
		t.Fatal("不可序列化值必须回退 null")
	}
	// 名称差异 → change；port 数值与字符串在 JSON 文本层面不同（80 vs
	// \"80\"）→ 也是 change；enabled 相同 → 无 change。
	changes := diffSafeChanges(
		map[string]any{"name": "a", "port": float64(80), "enabled": true},
		map[string]any{"name": "b", "port": "80", "enabled": true},
	)
	if len(changes) != 2 {
		t.Fatalf("diffSafeChanges = %#v", changes)
	}
	// 敏感字段 safeChange：前后均有值。
	change := safeChange("password", "密码", "old", "new")
	if !change.Sensitive || change.Before != "已设置" || change.After != "已变更" {
		t.Fatalf("敏感 change = %#v", change)
	}
	// 非敏感：长字符串截断 200。
	long := strings.Repeat("z", 300)
	change = safeChange("name", "名称", long, nil)
	if len(change.Before) != 200 || change.After != "" {
		t.Fatalf("normalizeSafeValue 截断不符: %d", len(change.Before))
	}
	// 结构体值序列化截断。
	change = safeChange("name", "名称", map[string]any{"k": strings.Repeat("y", 300)}, nil)
	if len(change.Before) != 200 {
		t.Fatalf("结构体序列化截断不符: %d", len(change.Before))
	}
	// actorResolver：有/无认证上下文。
	withAuth := httptest.NewRequest(http.MethodGet, "/", nil)
	withAuth = withAuth.WithContext(authsys.WithAuthContext(withAuth.Context(), &authsys.AuthContext{SystemAccountID: "sys-1"}))
	if actorResolver(withAuth) != "sys-1" {
		t.Fatal("有认证必须取 SystemAccountID")
	}
	if actorResolver(httptest.NewRequest(http.MethodGet, "/", nil)) != "anonymous" {
		t.Fatal("无认证必须 anonymous")
	}
}

// TestWJPatchAndDeleteHandlers 固定 PATCH/DELETE handler 全流程。
func TestWJPatchAndDeleteHandlers(t *testing.T) {
	fixture := newProxyFixture(t)
	created := wjCreateProxy(t, fixture, "代理A")

	// PATCH：改名 → 200 + operation log + 名称差异。
	recorder := httptest.NewRecorder()
	request := wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/"+created.ID,
		`{"name":"代理B","expectedUpdatedAt":"`+created.UpdatedAt+`"}`, fixture.auth(""))
	request.SetPathValue("id", created.ID)
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch 必须 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.sink.entries) == 0 {
		t.Fatal("patch 必须记录操作日志")
	}

	// PATCH：同名（无实际变化）→ no-op OK（无新日志）。
	entriesBefore := len(fixture.sink.entries)
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/"+created.ID,
		`{"name":"代理B","expectedUpdatedAt":"`+currentUpdatedAt(t, fixture, created.ID)+`"}`, fixture.auth(""))
	request.SetPathValue("id", created.ID)
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch no-op 必须 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.sink.entries) != entriesBefore {
		t.Fatalf("无变化不得记录日志: %d -> %d", entriesBefore, len(fixture.sink.entries))
	}

	// PATCH：不存在 → 404。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/ghost",
		`{"name":"x","expectedUpdatedAt":"2026-09-04T12:00:00Z"}`, fixture.auth(""))
	request.SetPathValue("id", "ghost")
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("patch ghost 必须 404: %d", recorder.Code)
	}

	// PATCH：非法 body → 400。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/"+created.ID, `{"unknown":1}`, fixture.auth(""))
	request.SetPathValue("id", created.ID)
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("patch 非法 body 必须 400: %d", recorder.Code)
	}

	// DELETE：成功 → 204 + 日志；再删 → 404。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodDelete, "/__aisys__/api/proxies/"+created.ID, "", fixture.auth(""))
	request.SetPathValue("id", created.ID)
	deleteHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete 必须 204: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.sink.entries) == 0 || fixture.sink.entries[len(fixture.sink.entries)-1].Action != "delete" {
		t.Fatal("delete 必须记录操作日志")
	}
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodDelete, "/__aisys__/api/proxies/"+created.ID, "", fixture.auth(""))
	request.SetPathValue("id", created.ID)
	deleteHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("重复 delete 必须 404: %d", recorder.Code)
	}

	// 未认证 → 401。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodDelete, "/__aisys__/api/proxies/x", "", nil)
	request.SetPathValue("id", "x")
	deleteHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未认证必须 401: %d", recorder.Code)
	}
}

// TestWJProxyTestChangesDiff 固定手动检测日志的差异字段。
func TestWJProxyTestChangesDiff(t *testing.T) {
	report := proxyTestReport{
		Status: "reachable", OutboundIP: "1.2.3.4",
		OutboundRegion: " somewhere", Message: "ok", TestedAt: "2026-09-04T12:00:00Z",
	}
	before := proxyTestBeforeState{Status: "unknown"}
	changes := proxyTestChanges(before, report)
	if len(changes) == 0 {
		t.Fatal("从 unknown 到 reachable 必须有差异")
	}
	// 合法状态相同值不得产生差异。
	same := proxyTestChanges(proxyTestBeforeState{Status: "passed"}, proxyTestReport{Status: "passed", Message: ""})
	for _, change := range same {
		if change.Field == "testStatus" {
			t.Fatalf("相同状态不得产生差异: %#v", same)
		}
	}
}

// TestWJProxyTestItemMessages 固定探测消息文案契约。
func TestWJProxyTestItemMessages(t *testing.T) {
	if got := proxyTestItemMessage(probeItemResult{HTTPStatus: 502}, "http://t"); !strings.Contains(got, "HTTP 502") {
		t.Fatalf("HTTP 状态消息 = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemFailed, ErrorCode: proxyTestTargetErrorInvalidURL}, "ftp://h"); !strings.Contains(got, "不支持的目标协议") {
		t.Fatalf("不支持协议消息 = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemFailed, ErrorCode: proxyTestTargetErrorInvalidURL}, "http://h"); !strings.Contains(got, "Invalid URL") {
		t.Fatalf("Invalid URL 消息 = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemFailed, ErrorCode: "timeout"}, ""); got != "timeout" {
		t.Fatalf("错误码透传 = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemFailed}, ""); got != "代理传输失败" {
		t.Fatalf("失败消息 = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemUnknown}, ""); got != "未形成真实代理检测请求" {
		t.Fatalf("unknown 消息 = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: "reachable"}, ""); got != "代理目标检测完成" {
		t.Fatalf("成功消息 = %q", got)
	}

	// 响应读取失败分类。
	if got := proxyTestResponseReadFailureResult(context.DeadlineExceeded); got.ErrorCode != "timeout" {
		t.Fatalf("DeadlineExceeded = %+v", got)
	}
	if got := proxyTestConfigurationCode(errors.New("proxy required")); got != "proxy_required" {
		t.Fatalf("required code = %q", got)
	}
	if got := proxyTestConfigurationCode(errors.New("other")); got != "proxy_invalid" {
		t.Fatalf("invalid code = %q", got)
	}
}

// TestWJProxyTestURLErrors 固定代理 URL 构造的错误分支。
func TestWJProxyTestURLErrors(t *testing.T) {
	fixture := newProxyFixture(t)
	build := func(snapshot *proxyTestSnapshot) error {
		_, err := fixture.store.proxyTestURL(snapshot)
		return err
	}
	if err := build(&proxyTestSnapshot{ProxyType: "ftp", ProxyHost: "h", ProxyPort: 8080}); err == nil {
		t.Fatal("无效类型必须报错")
	}
	if err := build(&proxyTestSnapshot{ProxyType: "http", ProxyHost: " ", ProxyPort: 8080}); err == nil {
		t.Fatal("空主机必须报错")
	}
	if err := build(&proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 0}); err == nil {
		t.Fatal("无效端口必须报错")
	}
	if err := build(&proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 8080, PasswordEncrypted: "sealed"}); err == nil {
		t.Fatal("有密码无用户名必须报错")
	}
	if err := build(&proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 8080, ProxyUsername: "u", PasswordEncrypted: "not-sealed"}); err == nil {
		t.Fatal("解密失败必须报错")
	}
	// 仅用户名 → 合法 URL。
	url, err := fixture.store.proxyTestURL(&proxyTestSnapshot{ProxyType: "socks5", ProxyHost: "h", ProxyPort: 1080, ProxyUsername: "u"})
	if err != nil || !strings.HasPrefix(url, "socks5://u@h:1080") {
		t.Fatalf("仅用户名 URL = (%q, %v)", url, err)
	}
}

func wjCreateProxy(t *testing.T, fixture *proxyFixture, name string) struct {
	ID        string
	UpdatedAt string
} {
	t.Helper()
	profile, err := fixture.store.Create(context.Background(), func() proxyInput {
		input, message := parseProxyInput(map[string]any{
			"name": name, "type": "http", "host": "h", "port": float64(8080), "password": "pw",
		}, false)
		if message != "" {
			t.Fatalf("parse: %q", message)
		}
		return input
	}(), "sys-admin-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return struct {
		ID        string
		UpdatedAt string
	}{ID: profile.ID, UpdatedAt: profile.UpdatedAt}
}

func currentUpdatedAt(t *testing.T, fixture *proxyFixture, id string) string {
	t.Helper()
	var updatedAt string
	if err := fixture.db.QueryRow(`SELECT updated_at FROM proxy_profiles WHERE id = ?`, id).Scan(&updatedAt); err != nil {
		t.Fatalf("query updated_at: %v", err)
	}
	return updatedAt
}

// TestWJStoreNowAndDeadline 固定检测槽与超时辅助。
func TestWJStoreNowAndDeadline(t *testing.T) {
	if proxyTestDeadline() <= 0 {
		t.Fatal("检测超时必须为正")
	}
	release, acquired := tryAcquireProxyTestSlot()
	if !acquired {
		t.Skip("检测槽被并行测试占用；跳过")
	}
	release()
}

var _ = time.Second

// TestWJCreateDuplicateAndDescriptionRoute 固定 create 的重名 409 与描述字
// 段解析。
func TestWJCreateDuplicateAndDescriptionRoute(t *testing.T) {
	fixture := newProxyFixture(t)
	body := `{"name":"描述代理","type":"http","host":"h","port":8080,"description":"说明文本"}`
	recorder := httptest.NewRecorder()
	request := wjJSONRequest(t, http.MethodPost, "/__aisys__/api/proxies", body, fixture.auth(""))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create 必须 201: %d %s", recorder.Code, recorder.Body.String())
	}
	// 重名 → 409（create 的 writeMutationError 路径）。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPost, "/__aisys__/api/proxies", body, fixture.auth(""))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("重名创建必须 409: %d %s", recorder.Code, recorder.Body.String())
	}
	// 空 body（无 JSON）→ 400。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPost, "/__aisys__/api/proxies", "", fixture.auth(""))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("空 body 必须 400: %d", recorder.Code)
	}
}

// TestWJPatchConflictRouteAndDeleteError 固定 patch 冲突与 delete 错误的
// 路由层映射。
func TestWJPatchConflictRouteAndDeleteError(t *testing.T) {
	fixture := newProxyFixture(t)
	created := wjCreateProxy(t, fixture, "代理-路由冲突")
	// 过期版本经 handler → 409。
	recorder := httptest.NewRecorder()
	request := wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/"+created.ID,
		`{"name":"x","expectedUpdatedAt":"2020-01-01T00:00:00Z"}`, fixture.auth(""))
	request.SetPathValue("id", created.ID)
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("patch 冲突必须 409: %d %s", recorder.Code, recorder.Body.String())
	}
	// patch 空 body → 400。
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/"+created.ID, "", fixture.auth(""))
	request.SetPathValue("id", created.ID)
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("patch 空 body 必须 400: %d", recorder.Code)
	}
	// delete 存储错误 → writeMutationError 400。
	broken := newProxyFixture(t)
	broken.seedProfile(t, "p-x", "2026-01-01", "unknown")
	if err := broken.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodDelete, "/__aisys__/api/proxies/p-x", "", broken.auth(""))
	request.SetPathValue("id", "p-x")
	deleteHandler(recorder, request, broken.store, broken.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("delete 存储错误必须 400: %d", recorder.Code)
	}
}

// TestWJDerefAndBoolHelpers 固定取值助手的全部分支。
func TestWJDerefAndBoolHelpers(t *testing.T) {
	text := "t"
	if derefOrEmpty(nil) != "" || derefOrEmpty(&text) != "t" {
		t.Fatal("derefOrEmpty 不符")
	}
	zero, one := 0, 7
	if derefInt(nil) != 0 || derefInt(&one) != 7 || derefInt(&zero) != 0 {
		t.Fatal("derefInt 不符")
	}
	if !boolFromValue(true) || boolFromValue(false) {
		t.Fatal("boolFromValue bool 分支不符")
	}
	if !boolFromValue(int64(1)) || boolFromValue(int64(0)) {
		t.Fatal("boolFromValue int64 分支不符")
	}
	if !boolFromValue(float64(2)) || boolFromValue(float64(0)) {
		t.Fatal("boolFromValue float64 分支不符")
	}
	if !boolFromValue([]byte("1")) || boolFromValue([]byte("0")) {
		t.Fatal("boolFromValue []byte 分支不符")
	}
	if boolFromValue(nil) {
		t.Fatal("boolFromValue 默认分支不符")
	}
}

// TestWJCreatePatchUnauthenticatedRoute 固定 create/patch 的认证门。
func TestWJCreatePatchUnauthenticatedRoute(t *testing.T) {
	fixture := newProxyFixture(t)
	recorder := httptest.NewRecorder()
	request := wjJSONRequest(t, http.MethodPost, "/__aisys__/api/proxies", `{"name":"n"}`, nil)
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("create 未认证必须 401: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request = wjJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxies/x", `{"name":"n"}`, nil)
	request.SetPathValue("id", "x")
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("patch 未认证必须 401: %d", recorder.Code)
	}
}

// TestWJTestHandlerRunErrorIs502 固定检测执行失败的 502 映射：无效代理类
// 型使 runProxyTest 在构造代理 URL 时失败。
func TestWJTestHandlerRunErrorIs502(t *testing.T) {
	fixture := newProxyTestFixture(t)
	// 无效 type 的代理行 + 一个启用的 provider 目标 → proxyTestURL 失败。
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles
		(id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('p-bad', 'sa-1', '坏类型', 'ftp', 'h', 8080, 1, 'unknown', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-1', 'openai', 'OpenAI', 1, '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, enabled, base_url, updated_at)
		VALUES ('pp-1', 'openai', 1, 'https://api.example.com/v1', '2026-01-01')`); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	recorder := postProxyTest(t, fixture, "p-bad", true)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("执行失败必须 502: %d %s", recorder.Code, recorder.Body.String())
	}
}
