package proxylatency

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/operationlogappend"
)

// 本文件覆盖 J3a 管理面的 PostgreSQL 数据访问语义：临时令牌解析、认证传播、
// 代理快照读取（信封/目标聚合/重复 provider 拒绝）、存在性确认、契约预检、
// F4 审计委托，以及管理 handler 的错误到 HTTP 状态映射。

func TestWFManualAdminSourceConstruction(t *testing.T) {
	if _, err := NewPostgresManualAdminSource(nil, nil); err == nil {
		t.Fatal("缺数据库必须拒绝")
	}
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	source, err := NewPostgresManualAdminSource(db, func() time.Time { return wfProjBase })
	if err != nil {
		t.Fatalf("合法构造失败: %v", err)
	}
	if source == nil || source.auth == nil {
		t.Fatal("认证器必须装配")
	}
}

func TestWFManualAdminTokenResolution(t *testing.T) {
	valid := "juhe_tmp_" + strings.Repeat("a", 43)
	token, err := resolveManualAdminToken("Bearer "+valid, nil)
	if err != nil || token != valid {
		t.Fatalf("合法 bearer token=%q err=%v", token, err)
	}
	token, err = resolveManualAdminToken("  bearer "+valid+"  ", nil)
	if err != nil || token != valid {
		t.Fatalf("小写/带空白 bearer token=%q err=%v", token, err)
	}
	if _, err := resolveManualAdminToken("", &http.Cookie{Name: "juhe_ai_session", Value: "cookie-token"}); err != nil {
		t.Fatalf("cookie 回退 err=%v", err)
	}
	for _, bad := range []string{"Token abc", "Bearer short", "Bearer juhe_bad_" + strings.Repeat("a", 43), "Bearer juhe_tmp_" + strings.Repeat("a", 42)} {
		if _, err := resolveManualAdminToken(bad, nil); !errors.Is(err, ErrManualAdminInvalidToken) {
			t.Fatalf("bearer=%q err=%v 必须是 ErrManualAdminInvalidToken", bad, err)
		}
	}
	if _, err := resolveManualAdminToken("", nil); !errors.Is(err, ErrManualAdminLoginRequired) {
		t.Fatalf("无凭证 err=%v", err)
	}
	if _, err := resolveManualAdminToken("", &http.Cookie{Name: "juhe_ai_session", Value: "  "}); !errors.Is(err, ErrManualAdminLoginRequired) {
		t.Fatalf("空白 cookie err=%v", err)
	}
}

func TestWFManualAdminAuthenticate(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	source, err := NewPostgresManualAdminSource(db, func() time.Time { return wfProjBase })
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	ctx := context.Background()

	// 形状非法的 token 在进入数据库前即被拒绝。
	if _, err := source.Authenticate(ctx, "Bearer short", nil); !errors.Is(err, ErrManualAdminInvalidToken) {
		t.Fatalf("非法 token err=%v", err)
	}

	// 合法形状 token 走会话查询；无会话行 → 会话过期。
	valid := "juhe_tmp_" + strings.Repeat("b", 43)
	if _, err := source.Authenticate(ctx, "Bearer "+valid, nil); !errors.Is(err, ErrManualAdminSessionExpired) {
		t.Fatalf("无会话 err=%v", err)
	}
	joined := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
	}
	if !strings.Contains(joined, "ss.token_hash") {
		t.Fatal("认证必须查询会话表")
	}

	// 命中有效管理员会话：返回 actor 且丢弃凭据。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	source2, _ := NewPostgresManualAdminSource(db2, func() time.Time { return wfProjBase })
	rec2.script("ss.token_hash", []string{"id", "expires_at", "last_seen_at", "sa_id", "username", "display_name", "role", "must_change"},
		[][]driver.Value{{"session-1", wfProjBase.Add(time.Hour), wfProjBase, "sys-1", "admin", "管理员", "admin", false}})
	validToken := "juhe_tmp_" + strings.Repeat("c", 43)
	actor, err := source2.Authenticate(ctx, "Bearer "+validToken, nil)
	if err != nil {
		t.Fatalf("管理员认证失败: %v", err)
	}
	if actor.SystemAccountID != "sys-1" || actor.Username != "admin" || actor.Role != "admin" {
		t.Fatalf("actor=%+v", actor)
	}

	// 非 admin 角色 → ErrForbidden。
	rec3 := newWFRecorder()
	db3 := wfOpenRecorderDB(t, rec3)
	source3, _ := NewPostgresManualAdminSource(db3, func() time.Time { return wfProjBase })
	rec3.script("ss.token_hash", []string{"id", "expires_at", "last_seen_at", "sa_id", "username", "display_name", "role", "must_change"},
		[][]driver.Value{{"session-2", wfProjBase.Add(time.Hour), wfProjBase, "sys-2", "viewer", "访客", "viewer", false}})
	if _, err := source3.Authenticate(ctx, "Bearer juhe_tmp_"+strings.Repeat("d", 43), nil); !errors.Is(err, ErrManualAdminForbidden) {
		t.Fatalf("非管理员 err=%v", err)
	}
}

// wfSnapshotColumns 是快照查询的占位列名（18 列）。
func wfSnapshotColumns() []string {
	return []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11", "c12", "c13", "c14", "c15", "c16", "c17", "c18"}
}

// wfSnapshotRow 构造快照查询一行（列序与 manualAdminSnapshotSQL 一致）。
func wfSnapshotRow(proxyID string, provider, profileID, targetURL string) []driver.Value {
	return []driver.Value{
		proxyID, "代理一", "http", "10.0.0.1", int64(8080), "user", "", wfProjRevision,
		"passed", int64(12), nil, nil, nil, nil, provider, provider, profileID, targetURL,
	}
}

func TestWFManualAdminLoadSnapshot(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	source, _ := NewPostgresManualAdminSource(db, func() time.Time { return wfProjBase })
	ctx := context.Background()

	// 空 proxyID 直接缺失。
	if _, err := source.LoadSnapshot(ctx, "  ", time.Minute); !errors.Is(err, ErrManualAdminProxyMissing) {
		t.Fatalf("空 proxyID err=%v", err)
	}

	// 无行 → 缺失。
	if _, err := source.LoadSnapshot(ctx, "p-1", time.Minute); !errors.Is(err, ErrManualAdminProxyMissing) {
		t.Fatalf("无行 err=%v", err)
	}

	// 一行代理 + 一个目标。
	rec.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{wfSnapshotRow("p-1", "gpt", "profile-gpt", "https://api.openai.com/v1")})
	snapshot, err := source.LoadSnapshot(ctx, "p-1", 25*time.Second)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if snapshot.Request.ProxyID != "p-1" || snapshot.Request.ProxyPort != 8080 || snapshot.Request.DeadlineMS != 25000 {
		t.Fatalf("快照请求=%+v", snapshot.Request)
	}
	if snapshot.Request.ConfigRevision != wfProjRevision || snapshot.before.status != "passed" {
		t.Fatalf("revision/before=%q/%q", snapshot.Request.ConfigRevision, snapshot.before.status)
	}
	if snapshot.before.latencyMS == nil || *snapshot.before.latencyMS != 12 {
		t.Fatalf("before latency=%v", snapshot.before.latencyMS)
	}
	if len(snapshot.Request.Targets) != 1 || snapshot.Request.Targets[0].Provider != "gpt" || snapshot.Request.Targets[0].Name != "gpt" {
		t.Fatalf("targets=%+v", snapshot.Request.Targets)
	}

	// LEFT JOIN 空目标（四列全空）→ 0 目标仍可用。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	source2, _ := NewPostgresManualAdminSource(db2, func() time.Time { return wfProjBase })
	rec2.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{wfSnapshotRow("p-2", "", "", "")})
	snapshot2, err := source2.LoadSnapshot(ctx, "p-2", time.Minute)
	if err != nil || len(snapshot2.Request.Targets) != 0 {
		t.Fatalf("空目标快照=%+v err=%v", snapshot2.Request.Targets, err)
	}

	// 密码信封合法 → 装载凭据。
	rec3 := newWFRecorder()
	db3 := wfOpenRecorderDB(t, rec3)
	source3, _ := NewPostgresManualAdminSource(db3, func() time.Time { return wfProjBase })
	rowWithPassword := wfSnapshotRow("p-3", "gpt", "profile-gpt", "https://api.openai.com/v1")
	rowWithPassword[6] = wfTestEnvelope()
	rec3.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{rowWithPassword})
	snapshot3, err := source3.LoadSnapshot(ctx, "p-3", time.Minute)
	if err != nil || snapshot3.Request.ProxyPassword == nil || snapshot3.Request.ProxyPassword.Kind != "proxy_password" {
		t.Fatalf("凭据快照=%+v err=%v", snapshot3.Request.ProxyPassword, err)
	}

	// 密码信封损坏 → 报错。
	rec4 := newWFRecorder()
	db4 := wfOpenRecorderDB(t, rec4)
	source4, _ := NewPostgresManualAdminSource(db4, func() time.Time { return wfProjBase })
	badPassword := wfSnapshotRow("p-4", "gpt", "profile-gpt", "https://api.openai.com/v1")
	badPassword[6] = "v1:broken"
	rec4.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{badPassword})
	if _, err := source4.LoadSnapshot(ctx, "p-4", time.Minute); err == nil || !strings.Contains(err.Error(), "envelope 无效") {
		t.Fatalf("坏信封 err=%v", err)
	}

	// 重复 provider → 报错。
	rec5 := newWFRecorder()
	db5 := wfOpenRecorderDB(t, rec5)
	source5, _ := NewPostgresManualAdminSource(db5, func() time.Time { return wfProjBase })
	rec5.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{
		wfSnapshotRow("p-5", "gpt", "profile-gpt", "https://api.openai.com/v1"),
		wfSnapshotRow("p-5", "gpt", "profile-gpt-2", "https://other.invalid/v1"),
	})
	if _, err := source5.LoadSnapshot(ctx, "p-5", time.Minute); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复 provider err=%v", err)
	}

	// 目标标识残缺（provider 有值但 profileID 空）→ 报错。
	rec6 := newWFRecorder()
	db6 := wfOpenRecorderDB(t, rec6)
	source6, _ := NewPostgresManualAdminSource(db6, func() time.Time { return wfProjBase })
	brokenTarget := wfSnapshotRow("p-6", "gpt", "", "https://api.openai.com/v1")
	rec6.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{brokenTarget})
	if _, err := source6.LoadSnapshot(ctx, "p-6", time.Minute); err == nil || !strings.Contains(err.Error(), "标识无效") {
		t.Fatalf("目标标识 err=%v", err)
	}

	// 查询失败。
	rec7 := newWFRecorder()
	db7 := wfOpenRecorderDB(t, rec7)
	source7, _ := NewPostgresManualAdminSource(db7, func() time.Time { return wfProjBase })
	rec7.failQuery("FROM juhe_business.proxy_profiles p", errors.New("boom"))
	if _, err := source7.LoadSnapshot(ctx, "p-7", time.Minute); err == nil || !strings.Contains(err.Error(), "快照失败") {
		t.Fatalf("查询失败 err=%v", err)
	}
}

func TestWFManualAdminExistsAndContract(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	source, _ := NewPostgresManualAdminSource(db, func() time.Time { return wfProjBase })
	ctx := context.Background()

	rec.script("SELECT EXISTS", []string{"exists"}, [][]driver.Value{{true}})
	exists, err := source.Exists(ctx, "p-1")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	rec.script("SELECT EXISTS", []string{"exists"}, [][]driver.Value{{false}})
	exists, err = source.Exists(ctx, "p-1")
	if err != nil || exists {
		t.Fatalf("不存在 exists=%v err=%v", exists, err)
	}
	rec.failQuery("SELECT EXISTS", errors.New("boom"))
	if _, err := source.Exists(ctx, "p-1"); err == nil || !strings.Contains(err.Error(), "存在失败") {
		t.Fatalf("exists 查询失败 err=%v", err)
	}

	// CheckContract：auth 关系失败直接传播。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	source2, _ := NewPostgresManualAdminSource(db2, func() time.Time { return wfProjBase })
	rec2.failExec("juhe_business.system_sessions", errors.New("denied"))
	if err := source2.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "会话认证契约失败") {
		t.Fatalf("auth 契约失败 err=%v", err)
	}

	// CheckContract：全链路通过（auth 权限、5 个关系、快照、4 条审计 EXPLAIN、写权限）。
	rec3 := newWFRecorder()
	db3 := wfOpenRecorderDB(t, rec3)
	source3, _ := NewPostgresManualAdminSource(db3, func() time.Time { return wfProjBase })
	rec3.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec3.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	if err := source3.CheckContract(ctx); err != nil {
		t.Fatalf("契约检查必须通过: %v", err)
	}
	joined := ""
	for _, statement := range rec3.all() {
		joined += statement.query + "\n"
	}
	for _, required := range []string{
		"juhe_business.system_sessions",
		"juhe_business.provider_protocol_profiles",
		"EXPLAIN INSERT INTO juhe_dataset.operation_logs",
		"juhe_dataset.operation_log_summary_search_terms",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("契约缺少 %q", required)
		}
	}

	// 写权限缺失（has_table_privilege=false）→ fail closed。
	rec4 := newWFRecorder()
	db4 := wfOpenRecorderDB(t, rec4)
	source4, _ := NewPostgresManualAdminSource(db4, func() time.Time { return wfProjBase })
	rec4.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec4.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{false}})
	if err := source4.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "审计追加权限") {
		t.Fatalf("缺写权限 err=%v", err)
	}

	// nil 接收者。
	var nilSource *PostgresManualAdminSource
	if err := nilSource.CheckContract(ctx); err == nil {
		t.Fatal("nil source 必须报未初始化")
	}
}

func TestWFManualAdminAuditAppenderDelegates(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	appender := NewPostgresManualAdminAuditAppender(db)
	statusCode := http.StatusOK
	input := operationlogappend.Input{
		ID: "oplog_wf_1", ActorSystemAccountID: "sys-1", ActorRole: "admin",
		Mode: "admin", Module: "proxies", Action: "test", OperationKey: "proxies.test",
		ResourceType: "proxy", ResourceID: "p-1", ResourceName: "代理一",
		Summary: "检测代理：代理一", DetailLevel: "full", VisibilityScope: "admin_only",
		Metadata: []byte("{}"), StatusCode: &statusCode, CreatedAt: wfProjBase,
	}
	if err := appender.Append(context.Background(), input); err != nil {
		t.Fatalf("审计追加失败: %v", err)
	}
	found := false
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "INSERT INTO juhe_dataset.operation_logs") {
			found = true
		}
	}
	if !found {
		t.Fatal("必须委托 F4 operation_logs 追加")
	}
	rec.failExec("juhe_dataset.operation_logs", errors.New("insert denied"))
	if err := appender.Append(context.Background(), input); err == nil {
		t.Fatal("审计追加失败必须传播")
	}
}

func TestWFManualAdminHandlerErrorMapping(t *testing.T) {
	validPath := "/__aisys__/api/proxies/p-1/test"
	newRequest := func(method, path string, body io.Reader) *http.Request {
		request := httptest.NewRequest(method, path, body)
		if body == nil {
			request.ContentLength = 0
		}
		return request
	}
	issue := func(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	// 路径/方法不匹配 → 404（在依赖解析之前）。
	notFoundHandler := NewManualAdminHandler(nil, nil, nil, time.Second, nil)
	if code := issue(notFoundHandler, newRequest(http.MethodPost, "/nope", nil)).Code; code != http.StatusNotFound {
		t.Fatalf("未知路径=%d", code)
	}
	if code := issue(notFoundHandler, newRequest(http.MethodGet, validPath, nil)).Code; code != http.StatusNotFound {
		t.Fatalf("GET 不允许=%d", code)
	}
	if code := issue(notFoundHandler, newRequest(http.MethodPost, "/__aisys__/api/proxies/a/b/test", nil)).Code; code != http.StatusNotFound {
		t.Fatalf("含斜杠代理 id=%d", code)
	}

	// 依赖缺失 → 503。
	unavailable := NewManualAdminHandler(nil, &fakeManualAdminSource{}, &fakeManualAdminAuditAppender{}, time.Second, nil)
	if code := issue(unavailable, newRequest(http.MethodPost, validPath, nil)).Code; code != http.StatusServiceUnavailable {
		t.Fatalf("依赖缺失=%d", code)
	}

	authCases := []struct {
		name       string
		authErr    error
		wantStatus int
		wantCode   string
	}{
		{name: "非法令牌", authErr: ErrManualAdminInvalidToken, wantStatus: http.StatusUnauthorized},
		{name: "需要登录", authErr: ErrManualAdminLoginRequired, wantStatus: http.StatusUnauthorized},
		{name: "会话过期", authErr: ErrManualAdminSessionExpired, wantStatus: http.StatusUnauthorized},
		{name: "需要改密", authErr: ErrManualAdminMustChange, wantStatus: http.StatusForbidden, wantCode: "must_change_password"},
		{name: "禁止访问", authErr: ErrManualAdminForbidden, wantStatus: http.StatusForbidden},
		{name: "未知错误", authErr: errors.New("db down"), wantStatus: http.StatusBadGateway},
	}
	for _, tt := range authCases {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeManualAdminSource{authErr: tt.authErr}
			handler := NewManualAdminHandler(&fakeManualAdminRunner{}, source, &fakeManualAdminAuditAppender{}, time.Second, nil)
			response := issue(handler, newRequest(http.MethodPost, validPath, nil))
			if response.Code != tt.wantStatus {
				t.Fatalf("status=%d want %d", response.Code, tt.wantStatus)
			}
			body := response.Body.String()
			if tt.wantCode != "" && !strings.Contains(body, tt.wantCode) {
				t.Fatalf("body=%s 缺少 code=%s", body, tt.wantCode)
			}
		})
	}

	execCases := []struct {
		name       string
		loadErr    error
		runErr     error
		exists     bool
		wantStatus int
	}{
		{name: "代理缺失", loadErr: ErrManualAdminProxyMissing, exists: true, wantStatus: http.StatusNotFound},
		{name: "owner 忙", loadErr: ErrOwnerLeaseHeld, exists: true, wantStatus: http.StatusServiceUnavailable},
		{name: "proxy 忙", runErr: ErrProxyLeaseHeld, exists: true, wantStatus: http.StatusServiceUnavailable},
		{name: "执行失败", runErr: errors.New("upstream boom"), exists: true, wantStatus: http.StatusBadGateway},
		{name: "执行后消失", exists: false, wantStatus: http.StatusNotFound},
	}
	for _, tt := range execCases {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeManualAdminSource{exists: tt.exists, loadErr: tt.loadErr}
			runner := &fakeManualAdminRunner{err: tt.runErr}
			handler := NewManualAdminHandler(runner, source, &fakeManualAdminAuditAppender{}, time.Second, nil)
			response := issue(handler, newRequest(http.MethodPost, validPath, nil))
			if response.Code != tt.wantStatus {
				t.Fatalf("status=%d want %d body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}

	// 非法请求体：JSON 损坏 / 尾随内容。
	source := &fakeManualAdminSource{exists: true}
	runner := &fakeManualAdminRunner{}
	handler := NewManualAdminHandler(runner, source, &fakeManualAdminAuditAppender{}, time.Second, nil)
	badJSON := httptest.NewRequest(http.MethodPost, validPath, strings.NewReader("{not-json"))
	if code := issue(handler, badJSON).Code; code != http.StatusBadRequest {
		t.Fatalf("损坏 JSON=%d", code)
	}
	trailing := httptest.NewRequest(http.MethodPost, validPath, strings.NewReader(`{"a":1} {"b":2}`))
	if code := issue(handler, trailing).Code; code != http.StatusBadRequest {
		t.Fatalf("尾随 JSON=%d", code)
	}
	// 合法 JSON body 接受。
	goodBody := httptest.NewRequest(http.MethodPost, validPath, strings.NewReader(`{"note":"noop"}`))
	if code := issue(handler, goodBody).Code; code == http.StatusBadRequest {
		t.Fatal("合法 JSON 不得拒绝")
	}
}

func TestWFManualAdminPureHelpers(t *testing.T) {
	// manualAdminProxyID 路径解析契约。
	if _, ok := manualAdminProxyID("/__aisys__/api/proxies/p-1/test"); !ok {
		t.Fatal("合法路径必须命中")
	}
	if value, ok := manualAdminProxyID("/__aisys__/api/proxies/p%2E1/test"); !ok || value != "p.1" {
		t.Fatalf("转义解析=%q ok=%v", value, ok)
	}
	for _, bad := range []string{
		"/__aisys__/api/proxies/p-1",      // 缺后缀
		"/other/proxies/p-1/test",         // 缺前缀
		"/__aisys__/api/proxies//test",    // 空 id
		"/__aisys__/api/proxies/a/b/test", // 含斜杠
		"/__aisys__/api/proxies/%zz/test", // 非法转义
	} {
		if _, ok := manualAdminProxyID(bad); ok {
			t.Fatalf("%q 不得命中", bad)
		}
	}

	// 审计字段取值与截断。
	if manualAdminIntValue(nil) != nil || manualAdminStringValue(nil) != nil || emptyToNil("") != nil {
		t.Fatal("nil/空值必须投影为 nil")
	}
	latency := int64(12)
	if value := manualAdminIntValue(&latency); value != int64(12) {
		t.Fatalf("int 值=%v", value)
	}
	message := "msg"
	if value := manualAdminStringValue(&message); value != "msg" {
		t.Fatalf("string 值=%v", value)
	}
	if value := emptyToNil("1.2.3.4"); value != "1.2.3.4" {
		t.Fatalf("非空值=%v", value)
	}
	long := strings.Repeat("字", 201)
	truncated := manualAdminAuditValue(long)
	text, ok := truncated.(string)
	if !ok || len([]rune(text)) != 201 || !strings.HasSuffix(text, "…") {
		t.Fatalf("截断值=%q", text)
	}
	if value := manualAdminAuditValue(int64(3)); value != int64(3) {
		t.Fatalf("非字符串必须原样：%v", value)
	}
	if !manualAdminComparable("a", "a") || manualAdminComparable("a", "b") || manualAdminComparable(nil, "a") {
		t.Fatal("comparable 判定错误")
	}
}
