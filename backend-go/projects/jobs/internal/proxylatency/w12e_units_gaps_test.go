package proxylatency

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// w12e_units_gaps_test.go 覆盖剩余小臂：transport 目标 URL 校验、
// direct_input_reader 错误路径、manual 报告汇总、manual_outbound 解析、
// config/dbgate 配置臂。

// TestW12EParseTargetURLArms：parseTargetURL 的逐段拒绝臂。
func TestW12EParseTargetURLArms(t *testing.T) {
	cases := []struct {
		raw    string
		needle string
	}{
		{"   ", "target URL invalid"},               // trim 后为空
		{"http://[fe80::1%25eth0]/", "host invalid"}, // host 含百分号（IPv6 zone）
		{"http://ok.invalid/a/%2e%2e/b", "invalid"}, // dot 段
		{"http://[::1", "invalid"},                  // 解析失败
		{"http://ok.invalid:0/", "port invalid"},    // 端口 0
		{"http://ok.invalid:/", "port invalid"},     // 空端口
	}
	for _, tc := range cases {
		if _, err := parseTargetURL(tc.raw); err == nil || !strings.Contains(err.Error(), tc.needle) {
			t.Fatalf("parseTargetURL(%q) = %v, 期望含 %q", tc.raw, err, tc.needle)
		}
	}
	// 合法 URL 规范化。
	parsed, err := parseTargetURL("HTTP://Example.Invalid:8080/a")
	if err != nil || parsed.Host != "example.invalid:8080" || parsed.Path != "/a" {
		t.Fatalf("规范化错误: %v %v", parsed, err)
	}
	// IPv6 主机加方括号。
	parsed, err = parseTargetURL("http://[2001:db8::1]/")
	if err != nil || parsed.Host != "[2001:db8::1]" {
		t.Fatalf("IPv6 规范化错误: %v %v", parsed, err)
	}
}

// w12eTimeoutNetError 实现 net.Error 的超时错误（transportFailureResult 臂）。
type w12eTimeoutNetError struct{}

func (w12eTimeoutNetError) Error() string   { return "w12e timeout" }
func (w12eTimeoutNetError) Timeout() bool   { return true }
func (w12eTimeoutNetError) Temporary() bool { return false }

// TestW12ETransportFailureArms：传输失败分类。
func TestW12ETransportFailureArms(t *testing.T) {
	if got := transportFailureResult(context.DeadlineExceeded); got.ErrorCode != "timeout" {
		t.Fatalf("DeadlineExceeded 应为 timeout: %+v", got)
	}
	if got := transportFailureResult(w12eTimeoutNetError{}); got.ErrorCode != "timeout" {
		t.Fatalf("net.Error timeout 应为 timeout: %+v", got)
	}
	if got := transportFailureResult(&net.DNSError{Err: "nx", Name: "x", IsNotFound: true}); got.ErrorCode != "dns" {
		t.Fatalf("DNS 错误应为 dns: %+v", got)
	}
	if got := transportFailureResult(io.ErrUnexpectedEOF); got.ErrorCode != "early_eof" {
		t.Fatalf("EOF 应为 early_eof: %+v", got)
	}
	if got := transportFailureResult(errors.New("boom")); got.ErrorCode != "transport" {
		t.Fatalf("其他错误应为 transport: %+v", got)
	}
	if got := responseReadFailureResult(w12eTimeoutNetError{}); got.ErrorCode != "timeout" {
		t.Fatalf("读超时应为 timeout: %+v", got)
	}
}

// TestW12EProbeItemBodyTruncated：响应体截断 → 读失败臂。
func TestW12EProbeItemBodyTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: "http://target.invalid/",
		ProxyURL:  server.URL,
		Timeout:   2 * time.Second,
	})
	if result.Status != ItemFailed || result.ErrorCode != "early_eof" {
		t.Fatalf("截断响应应 early_eof: %+v", result)
	}
}

// TestW12EDirectInputReaderArms：LoadDue 参数/事务/游标臂。
func TestW12EDirectInputReaderArms(t *testing.T) {
	ctx := context.Background()
	reader, err := NewPostgresDirectInputReader(wfOpenRecorderDB(t, newWFRecorder()), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadDue(ctx, 0); err == nil {
		t.Fatalf("limit 0 应报错")
	}
	if _, err := reader.LoadDue(ctx, maxProxyLatencyInputLimit+1); err == nil {
		t.Fatalf("limit 超界应报错")
	}
	// beginReadOnly 失败。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e begin")
	failing, err := NewPostgresDirectInputReader(wfOpenRecorderDB(t, rec), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.LoadDue(ctx, 10); err == nil {
		t.Fatalf("begin 失败应透传")
	}
	// 候选行解码失败（列数与 Scan 目标数不匹配）。
	rec = newWFRecorder()
	rec.scripts = append(rec.scripts, wfScript{match: "FROM selected_proxies proxy", columns: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m"}, rows: [][]driver.Value{{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m"}}})
	broken, err := NewPostgresDirectInputReader(wfOpenRecorderDB(t, rec), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.LoadDue(ctx, 10); err == nil {
		t.Fatalf("候选解码失败应透传")
	}
	// 说明：rows.Close 的独立错误臂（LoadDue 中 closeErr 检查）在
	// database/sql 的 Next-on-EOF 自动关闭语义下会先被 rows.Err() 捕获，
	// 无法经由公开路径单独触发，故不构造该子用例。
	// commit 失败。
	rec = newWFRecorder()
	rec.commitErr = errors.New("w12e commit boom")
	committing, err := NewPostgresDirectInputReader(wfOpenRecorderDB(t, rec), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := committing.LoadDue(ctx, 10); err == nil || !strings.Contains(err.Error(), "提交") {
		t.Fatalf("commit 失败应包装: %v", err)
	}
}

// TestW12EManualReportArms：Validate 目标上限、InputDraft 回退、评分档位。
func TestW12EManualReportArms(t *testing.T) {
	// 目标数超限。
	request := testManualReleaseRequest()
	request.Targets = nil
	for index := 0; index < maxProxyLatencyWorkItems+1; index++ {
		request.Targets = append(request.Targets, ManualTarget{Provider: strings.Repeat("p", 8) + string(rune('a'+index%26)) + itoa(index), ProfileID: "x", Name: "x"})
	}
	if err := request.Validate(time.Second); err == nil {
		t.Fatalf("目标超限应报错")
	}
	// UnmarshalJSON 错误。
	var item ProxyTestItem
	if err := json.Unmarshal([]byte("{"), &item); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	// InputDraft 的规范化回退（非法 URL 目标保留原值）。
	request = testManualReleaseRequest()
	request.Targets = []ManualTarget{{Provider: "openai", ProfileID: "profile", Name: "OpenAI", URL: "::::"}}
	draft := request.InputDraft(time.Now().UTC(), time.Minute)
	if len(draft.Targets) != 1 || draft.Targets[0].ProbeError != targetProbeErrorInvalidURL {
		t.Fatalf("非法 URL 应固化为 ProbeError: %+v", draft.Targets)
	}
	// 评分档位：score<0、C、B。
	many := summarizeReport([]ProxyTestItem{
		{Name: "a", Status: ItemWarning}, {Name: "b", Status: ItemWarning},
		{Name: "c", Status: ItemWarning}, {Name: "d", Status: ItemWarning},
		{Name: "e", Status: ItemWarning}, {Name: "f", Status: ItemWarning},
		{Name: "g", Status: ItemWarning}, {Name: "h", Status: ItemWarning},
		{Name: "i", Status: ItemWarning}, {Name: "j", Status: ItemWarning},
		{Name: "k", Status: ItemWarning},
	})
	if many.score != 0 || many.grade != "D" {
		t.Fatalf("score<0 应归零: %+v", many)
	}
	c := summarizeReport([]ProxyTestItem{{Name: "a", Status: ItemFailed}})
	if c.score != 65 || c.grade != "C" {
		t.Fatalf("65 分应为 C: %+v", c)
	}
	b := summarizeReport([]ProxyTestItem{{Name: "a", Status: ItemWarning}, {Name: "b", Status: ItemWarning}})
	if b.score != 80 || b.grade != "B" {
		t.Fatalf("80 分应为 B: %+v", b)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestW12EParseManualOutboundArms：出站解析器的地区回退与失败分支。
func TestW12EParseManualOutboundArms(t *testing.T) {
	if _, ok := parseManualOutbound("unknown", []byte(`{"ip":"1.2.3.4"}`)); ok {
		t.Fatalf("未知解析器应失败")
	}
	if _, ok := parseManualOutbound("ip-api", []byte("not-json")); ok {
		t.Fatalf("非法 JSON 应失败")
	}
	if _, ok := parseManualOutbound("ip-api", []byte(`{"status":"fail"}`)); ok {
		t.Fatalf("status=fail 应失败")
	}
	info, ok := parseManualOutbound("ip-api", []byte(`{"status":"success","query":"1.2.3.4","regionName":"区域名"}`))
	if !ok || info.IP != "1.2.3.4" || info.Region != "区域名" {
		t.Fatalf("ip-api 地区回退错误: %+v %v", info, ok)
	}
	info, ok = parseManualOutbound("ipwhois", []byte(`{"success":false}`))
	if ok {
		t.Fatalf("ipwhois success=false 应失败")
	}
	info, ok = parseManualOutbound("ipwhois", []byte(`{"ip":"1.2.3.4","country_code":"cn"}`))
	if !ok || info.Region != "CN" {
		t.Fatalf("ipwhois 国家码应大写: %+v", info)
	}
	info, ok = parseManualOutbound("ipinfo", []byte(`{"ip":"1.2.3.4","city":"城市"}`))
	if !ok || info.Region != "城市" {
		t.Fatalf("ipinfo 地区回退错误: %+v", info)
	}
	info, ok = parseManualOutbound("ipify", []byte(`{"ip":"1.2.3.4"}`))
	if !ok || info.IP != "1.2.3.4" {
		t.Fatalf("ipify 错误: %+v", info)
	}
	info, ok = parseManualOutbound("httpbin", []byte(`{"origin":"1.2.3.4,5.6.7.8"}`))
	if !ok || info.IP != "1.2.3.4" {
		t.Fatalf("httpbin 应取首个 IP: %+v", info)
	}
	if info, ok := parseManualOutbound("httpbin", []byte(`{"origin":""}`)); ok {
		t.Fatalf("空 IP 应失败: %+v %v", info, ok)
	}
}

// TestW12ELoadRuntimeConfigPoolArms：连接池环境变量的逐项校验臂。
func TestW12ELoadRuntimeConfigPoolArms(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
			"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
			"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w12e",
			"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
			"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        "postgres://jobs",
			"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  "postgres://business",
			"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": "postgres://writer",
			"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "secret",
		}
	}
	env := base()
	env["JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_OPEN_CONNS"] = "0"
	if _, err := LoadRuntimeConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatalf("非法 MAX_OPEN_CONNS 应报错")
	}
	env = base()
	env["JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_IDLE_CONNS"] = "-1"
	if _, err := LoadRuntimeConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatalf("非法 MAX_IDLE_CONNS 应报错")
	}
	env = base()
	env["JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_MAX_OPEN_CONNS"] = "abc"
	if _, err := LoadRuntimeConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatalf("非法 INPUT_MAX_OPEN_CONNS 应报错")
	}
}

// TestW12ELoadManualAdminConfigArms：管理端口配置的失败臂。
func TestW12ELoadManualAdminConfigArms(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":          "true",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS":   "127.0.0.1:18080",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":     "postgres://business",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_OPEN_CONNS": "10",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_IDLE_CONNS": "0",
	}
	if _, err := LoadManualAdminConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatalf("非法 MAX_IDLE_CONNS 应报错")
	}
	if _, err := NewPostgresManualAdminSource(nil, nil); err == nil {
		t.Fatalf("nil db 应报错")
	}
}

// TestW12EDBGateNilContext：Acquire 显式 nil ctx。
func TestW12EDBGateNilContext(t *testing.T) {
	gate := NewDBConcurrencyGate(1, 1)
	release, _, err := gate.Acquire(nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// TestW12ELoadSnapshotInvalidTarget：快照行的 provider 目标校验臂。
func TestW12ELoadSnapshotInvalidTarget(t *testing.T) {
	rec := newWFRecorder()
	rec.script("FROM juhe_business.proxy_profiles p",
		[]string{"id", "name", "type", "host", "port", "username", "password", "revision", "status", "latency", "outbound_ip", "outbound_region", "message", "tested_at", "provider", "provider_name", "profile_id", "target_url"},
		[][]driver.Value{
			{"w12e-p", "名称", "http", "127.0.0.1", int64(1), "", "", "2026-09-10T00:00:00.000000Z", "unknown", nil, nil, nil, nil, nil, nil, nil, nil, nil},
			{"w12e-p", "名称", "http", "127.0.0.1", int64(1), "", "", "2026-09-10T00:00:00.000000Z", "unknown", nil, nil, nil, nil, nil, nil, "profile-x", nil, nil},
		})
	source := &PostgresManualAdminSource{db: wfOpenRecorderDB(t, rec)}
	if _, err := source.LoadSnapshot(context.Background(), "w12e-p", time.Second); err == nil {
		t.Fatalf("不完整 provider 目标应报错")
	}
}
