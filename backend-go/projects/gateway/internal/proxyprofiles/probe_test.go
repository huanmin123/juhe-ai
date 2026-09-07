package proxyprofiles

// POST /proxies/{id}/test 契约测试：经 mock 正向代理的真实探测闭环、404、
// 权限门、诊断槽 503、无启用 provider 的 unknown 分支和传输失败分类，外加
// Mount 路由族注册防冲突。基线为 J3a 归档 Node 手动路由
// proxies-manual-test.route.ts 与冻结的外部报告 schema（contract 11.2）。

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

const proxyTestCatalogSchema = `
	CREATE TABLE providers (
		id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE TABLE provider_protocol_profiles (
		id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
		base_url TEXT NOT NULL, updated_at TEXT NOT NULL);
`

func newProxyTestFixture(t *testing.T) *proxyFixture {
	t.Helper()
	fixture := newProxyFixture(t)
	if _, err := fixture.db.Exec(proxyTestCatalogSchema); err != nil {
		t.Fatalf("catalog schema: %v", err)
	}
	return fixture
}

func postProxyTest(t *testing.T, fixture *proxyFixture, id string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies/"+id+"/test", nil)
	// 直调 handler 不经过 ServeMux，{id} 通配符需显式注入。
	request.SetPathValue("id", id)
	if auth {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("admin")))
	}
	recorder := httptest.NewRecorder()
	testHandler(recorder, request, fixture.store, fixture.sink)
	return recorder
}

// forwardProxyStub 是 absolute-form 正向代理：记录经代理转发的目标并回传
// 上游响应，用来证明探测真的走了代理传输。
func forwardProxyStub(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var forwarded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		forwarded = append(forwarded, r.URL.String())
		mu.Unlock()
		if r.URL.Host == "" {
			http.Error(w, "expected absolute-form proxy request", http.StatusBadRequest)
			return
		}
		outbound, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		outbound.Host = r.Host
		for key, values := range r.Header {
			for _, value := range values {
				outbound.Header.Add(key, value)
			}
		}
		response, err := http.DefaultClient.Do(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, forwarded...)
	}
}

func proxyHostPort(rawURL string) (string, int) {
	host := strings.TrimPrefix(rawURL, "http://")
	pieces := strings.Split(host, ":")
	port := 0
	for _, character := range pieces[1] {
		port = port*10 + int(character-'0')
	}
	return pieces[0], port
}

func TestProxyTestRunsThroughForwardProxy(t *testing.T) {
	fixture := newProxyTestFixture(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()
	proxyServer, forwarded := forwardProxyStub(t)
	outboundServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","query":"203.0.113.9","country":"澳大利亚"}`))
	}))
	defer outboundServer.Close()
	originalOutbound := proxyTestOutboundTargets
	proxyTestOutboundTargets = []proxyTestOutboundTarget{{URL: outboundServer.URL + "/json", Parser: "ip-api"}}
	t.Cleanup(func() { proxyTestOutboundTargets = originalOutbound })

	proxyHost, proxyPort := proxyHostPort(proxyServer.URL)
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-1', 'sa-1', '测试代理', 'http', ?, ?, 1, 'unknown', '2026-01-01', '2026-01-01')`, proxyHost, proxyPort); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('provider-1', 'openai', 'OpenAI', 1, '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, enabled, base_url, updated_at)
		VALUES ('profile-1', 'openai', 1, ?, '2026-01-01')`, target.URL); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	recorder := postProxyTest(t, fixture, "proxy-1", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data    proxyTestReport `json:"data"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body %s", err, recorder.Body.String())
	}
	report := envelope.Data
	if report.ProxyID != "proxy-1" || report.ProxyName != "测试代理" {
		t.Fatalf("identity: %#v", report)
	}
	// 基础连通性 + 1 个 provider item 全部 passed：score 100 / grade A。
	if report.Status != "passed" || report.PassedCount != 2 || report.WarningCount != 0 || report.FailedCount != 0 {
		t.Fatalf("status/counts: %s %d/%d/%d", report.Status, report.PassedCount, report.WarningCount, report.FailedCount)
	}
	if report.Score != 100 || report.Grade != "A" || report.Message != "代理质量检测通过" {
		t.Fatalf("score/grade/message: %d %s %q", report.Score, report.Grade, report.Message)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(report.TestedAt) {
		t.Fatalf("testedAt shape: %q", report.TestedAt)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items: %#v", report.Items)
	}
	base := report.Items[0]
	if base.Name != "基础连通性" || base.Status != "passed" || base.TargetURL != nil || base.LatencyMS == nil || base.Message != "全部供应商默认地址可达" {
		t.Fatalf("base item: %#v", base)
	}
	provider := report.Items[1]
	if provider.Name != "OpenAI" || provider.Status != "passed" || provider.TargetURL == nil || provider.LatencyMS == nil {
		t.Fatalf("provider item: %#v", provider)
	}
	if provider.HTTPStatus == nil || *provider.HTTPStatus != 200 {
		t.Fatalf("provider httpStatus: %#v", provider.HTTPStatus)
	}
	if provider.Message != "HTTP 200（传输完整，状态码仅供诊断）" {
		t.Fatalf("provider message: %q", provider.Message)
	}
	if report.BaseLatencyMS == nil {
		t.Fatal("baseLatencyMs missing")
	}
	// 手动触发独有：出口 IP/地区解析自第一个可用的 200 JSON。
	if report.OutboundIP != "203.0.113.9" || report.OutboundRegion != "澳大利亚" {
		t.Fatalf("outbound: %q %q", report.OutboundIP, report.OutboundRegion)
	}
	// 探测与出口探测都经代理转发（absolute-form）。
	forwardedURLs := forwarded()
	if len(forwardedURLs) < 2 {
		t.Fatalf("forwarded: %v", forwardedURLs)
	}
	sawTarget, sawOutbound := false, false
	for _, raw := range forwardedURLs {
		trimmed := strings.TrimSuffix(raw, "/")
		if trimmed == strings.TrimSuffix(target.URL, "/") {
			sawTarget = true
		}
		if trimmed == strings.TrimSuffix(outboundServer.URL+"/json", "/") {
			sawOutbound = true
		}
	}
	if !sawTarget || !sawOutbound {
		t.Fatalf("forwarded missing hops: %v", forwardedURLs)
	}
	// 审计：proxies.test + 检测状态等六列 diff。
	if len(fixture.sink.entries) != 1 {
		t.Fatalf("operation log entries: %d", len(fixture.sink.entries))
	}
	entry := fixture.sink.entries[0]
	if entry.OperationKey != "proxies.test" || entry.Action != "test" || entry.Module != "proxies" ||
		entry.ResourceType != "proxy" || entry.ResourceID != "proxy-1" || entry.ResourceName != "测试代理" ||
		entry.Summary != "检测代理：测试代理" || entry.VisibilityScope != "admin_only" || entry.Mode != "admin" {
		t.Fatalf("operation log entry: %#v", entry)
	}
	if len(entry.Changes) != 6 || entry.Changes[0].Field != "testStatus" || entry.Changes[0].After != "passed" {
		t.Fatalf("changes: %#v", entry.Changes)
	}
}

func TestProxyTestMissingProxyIs404(t *testing.T) {
	fixture := newProxyTestFixture(t)
	recorder := postProxyTest(t, fixture, "gone", true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status: %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "代理不存在") {
		t.Fatalf("body: %s", recorder.Body.String())
	}
	if len(fixture.sink.entries) != 0 {
		t.Fatalf("404 must not log: %#v", fixture.sink.entries)
	}
}

func TestProxyTestRequiresAuthContext(t *testing.T) {
	fixture := newProxyTestFixture(t)
	recorder := postProxyTest(t, fixture, "proxy-1", false)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", recorder.Code)
	}
}

func TestProxyTestWithoutEnabledProviderTargets(t *testing.T) {
	fixture := newProxyTestFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-1', 'sa-1', '空目标', 'http', '127.0.0.1', 1, 0, 'unknown', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	recorder := postProxyTest(t, fixture, "proxy-1", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data proxyTestReport `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	report := envelope.Data
	if report.Status != "unknown" || report.Score != 0 || report.Grade != "D" || report.Message != "代理检测未形成有效传输尝试" {
		t.Fatalf("report: %#v", report)
	}
	if len(report.Items) != 1 || report.Items[0].Name != "基础连通性" || report.Items[0].Status != "unknown" ||
		report.Items[0].Message != "没有启用的供应商默认地址，未形成代理传输检测" || report.Items[0].TargetURL != nil {
		t.Fatalf("base item: %#v", report.Items)
	}
	if report.OutboundIP != "" || report.OutboundRegion != "" || report.BaseLatencyMS != nil {
		t.Fatalf("outbound/base latency must be absent: %#v", report)
	}
}

func TestProxyTestTransportFailureClassification(t *testing.T) {
	fixture := newProxyTestFixture(t)
	// 已关闭端口：连接拒绝，归类 transport（非 timeout/dns/eof）。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-1', 'sa-1', '断连代理', 'http', '127.0.0.1', ?, 1, 'unknown', '2026-01-01', '2026-01-01')`, closedPort); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('provider-1', 'openai', 'OpenAI', 1, '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, enabled, base_url, updated_at)
		VALUES ('profile-1', 'openai', 1, 'http://127.0.0.1:9/v1/models', '2026-01-01')`); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	recorder := postProxyTest(t, fixture, "proxy-1", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data proxyTestReport `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	report := envelope.Data
	if report.Status != "failed" || report.FailedCount != 2 || report.Score != 30 || report.Grade != "D" {
		t.Fatalf("failed report: %s %d %d %s", report.Status, report.FailedCount, report.Score, report.Grade)
	}
	if report.Message != "代理检测存在 2 项失败" {
		t.Fatalf("message: %q", report.Message)
	}
	if len(report.Items) != 2 || report.Items[1].Status != "failed" || report.Items[1].Message != "transport" {
		t.Fatalf("items: %#v", report.Items)
	}
	if report.Items[0].Status != "failed" || report.Items[0].Message != "供应商默认地址全部发生传输失败" {
		t.Fatalf("base item: %#v", report.Items[0])
	}
	if report.OutboundIP != "" {
		t.Fatalf("outbound must stay absent: %q", report.OutboundIP)
	}
}

func TestProxyTestBusySlotIs503(t *testing.T) {
	fixture := newProxyTestFixture(t)
	capacity := cap(proxyTestProbeSlots)
	for index := 0; index < capacity; index++ {
		proxyTestProbeSlots <- struct{}{}
	}
	recorder := postProxyTest(t, fixture, "proxy-1", true)
	for index := 0; index < capacity; index++ {
		<-proxyTestProbeSlots
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", recorder.Code)
	}
	if header := recorder.Header().Get("Retry-After"); header != proxyTestSlotRetryAfter {
		t.Fatalf("Retry-After: %q", header)
	}
	if !strings.Contains(recorder.Body.String(), proxyTestSlotBusyMessage) {
		t.Fatalf("body: %s", recorder.Body.String())
	}
}

func TestResolveProxyTestSlotCapacity(t *testing.T) {
	// Node runtime.ts:813 integerConfig('JUHE_AI_BACKGROUND_DIAGNOSTIC_TASK_MAX_IN_FLIGHT', 5, 1, 1000)：
	// 默认 5、界 1..1000；未设/空白/不可解析保持默认，越界钳位到最近边界。
	cases := []struct {
		raw  string
		want int
	}{
		{"", proxyTestSlotCapacityDefault},
		{"   ", proxyTestSlotCapacityDefault},
		{"bogus", proxyTestSlotCapacityDefault},
		{"5", 5},
		{"1", 1},
		{"1000", 1000},
		{"0", 1},
		{"-3", 1},
		{"1001", 1000},
		{"99999", 1000},
	}
	for _, item := range cases {
		if got := resolveProxyTestSlotCapacity(item.raw); got != item.want {
			t.Fatalf("resolveProxyTestSlotCapacity(%q) = %d, want %d", item.raw, got, item.want)
		}
	}
	// 包级信号量的实际容量由同一次解析决定；测试进程未设 env 时应为默认 5。
	if os.Getenv(proxyTestSlotCapacityEnv) == "" && cap(proxyTestProbeSlots) != proxyTestSlotCapacityDefault {
		t.Fatalf("proxyTestProbeSlots capacity = %d, want default %d", cap(proxyTestProbeSlots), proxyTestSlotCapacityDefault)
	}
}

func TestProxyTestMountRegistersRouteFamily(t *testing.T) {
	fixture := newProxyTestFixture(t)
	kern := kernel.New(kernel.Options{})
	// 模式冲突会在注册时 panic；通过即证明 {id}/test 与既有 {id} 路由共存。
	Mount(kern, &authsys.Deps{}, fixture.store, fixture.sink)
	if kern.Handler() == nil {
		t.Fatal("kernel handler missing")
	}
}
