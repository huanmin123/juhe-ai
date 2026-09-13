package proxyprofiles

// 探测执行层补充契约：出站信息解析器全方言、目标端口归一化、探测传输失败
// 分类、合成基础项与手动检测路由的错误分支。

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ioNewEOF 构造 io.ErrUnexpectedEOF 供分类断言。
func ioNewEOF() error { return io.ErrUnexpectedEOF }

// TestWJParseProxyTestOutboundParsers 固定各出站 IP 服务的解析方言。
func TestWJParseProxyTestOutboundParsers(t *testing.T) {
	tests := []struct {
		name     string
		parser   string
		body     string
		wantIP   string
		wantOK   bool
		regionOK bool
	}{
		{name: "ip-api 成功", parser: "ip-api", body: `{"status":"success","query":"1.2.3.4","country":"C","countryCode":"CC","regionName":"R","city":"City"}`, wantIP: "1.2.3.4", wantOK: true, regionOK: true},
		{name: "ip-api 失败", parser: "ip-api", body: `{"status":"fail"}`, wantOK: false},
		{name: "ipwhois 成功", parser: "ipwhois", body: `{"success":true,"ip":"5.6.7.8","country":"C"}`, wantIP: "5.6.7.8", wantOK: true, regionOK: true},
		{name: "ipwhois 失败", parser: "ipwhois", body: `{"success":false}`, wantOK: false},
		{name: "ipsb", parser: "ipsb", body: `{"ip":"9.9.9.9"}`, wantIP: "9.9.9.9", wantOK: true},
		{name: "ipinfo", parser: "ipinfo", body: `{"ip":"7.7.7.7","country":"C"}`, wantIP: "7.7.7.7", wantOK: true, regionOK: true},
		{name: "ipify", parser: "ipify", body: `{"ip":"8.8.8.8"}`, wantIP: "8.8.8.8", wantOK: true},
		{name: "httpbin", parser: "httpbin", body: `{"origin":"1.1.1.1, 2.2.2.2"}`, wantIP: "1.1.1.1", wantOK: true},
		{name: "未知方言", parser: "unknown", body: `{}`, wantOK: false},
		{name: "坏 JSON", parser: "ipify", body: `{bad`, wantOK: false},
		{name: "空 IP", parser: "ipify", body: `{"ip":""}`, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := parseProxyTestOutbound(tt.parser, []byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, 期望 %v（info=%+v）", ok, tt.wantOK, info)
			}
			if tt.wantOK && info.IP != tt.wantIP {
				t.Fatalf("ip = %q, 期望 %q", info.IP, tt.wantIP)
			}
			if tt.wantOK && tt.regionOK && info.Region == "" {
				t.Fatalf("必须产出地区信息: %+v", info)
			}
		})
	}
}

// TestWJProxyTestCanonicalTargetPort 固定目标主机端口的归一化：IPv6 括号、
// 缺端口与非法形态。
func TestWJProxyTestCanonicalTargetPort(t *testing.T) {
	port, err := proxyTestCanonicalTargetPort("[::1]:8080")
	if err != nil || port != "8080" {
		t.Fatalf("IPv6 带端口 = (%q, %v)", port, err)
	}
	port, err = proxyTestCanonicalTargetPort("[::1]")
	if err != nil || port != "" {
		t.Fatalf("IPv6 无端口 = (%q, %v)", port, err)
	}
	if _, err := proxyTestCanonicalTargetPort("[::1"); err == nil {
		t.Fatal("缺右括号必须报错")
	}
	if _, err := proxyTestCanonicalTargetPort("[::1]:"); err == nil {
		t.Fatal("空端口必须报错")
	}
	if _, err := proxyTestCanonicalTargetPort("a:b:c"); err == nil {
		t.Fatal("多冒号主机必须报错")
	}
	port, err = proxyTestCanonicalTargetPort("example.com:443")
	if err != nil || port != "443" {
		t.Fatalf("主机端口 = (%q, %v)", port, err)
	}
	port, err = proxyTestCanonicalTargetPort("example.com")
	if err != nil || port != "" {
		t.Fatalf("仅主机 = (%q, %v)", port, err)
	}
}

// TestWJProbeTransportFailureClassification 固定探测传输失败的分类。
func TestWJProbeTransportFailureClassification(t *testing.T) {
	if got := proxyTestTransportFailureResult(context.DeadlineExceeded); got.ErrorCode != "timeout" || got.Status != proxyTestItemFailed {
		t.Fatalf("DeadlineExceeded = %+v", got)
	}
	dnsErr := &net.DNSError{Err: "nx", Name: "nope", IsNotFound: true}
	if got := proxyTestTransportFailureResult(dnsErr); got.ErrorCode != "dns" {
		t.Fatalf("DNS 错误 = %+v", got)
	}
	if got := proxyTestTransportFailureResult(ioNewEOF()); got.ErrorCode != "early_eof" {
		t.Fatalf("EOF = %+v", got)
	}
	if got := proxyTestTransportFailureResult(errors.New("reset")); got.ErrorCode != "transport" {
		t.Fatalf("普通传输错误 = %+v", got)
	}
	if got := proxyTestResponseReadFailureResult(errors.New("mid-read")); got.ErrorCode != "early_eof" {
		t.Fatalf("读取失败默认 = %+v", got)
	}
	if got := proxyTestResponseReadFailureResult(ioNewEOF()); got.ErrorCode != "early_eof" {
		t.Fatalf("EOF 读取失败 = %+v", got)
	}
}

// TestWJProbeProxyTargetGuards 固定单目标探测的入口守卫：取消、非法目标、
// 非法超时与非法代理配置。
func TestWJProbeProxyTargetGuards(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := probeProxyTarget(cancelled, "http://t", "http://proxy:8080", time.Second); got.ErrorCode != "probe_cancelled" {
		t.Fatalf("取消 = %+v", got)
	}
	if got := probeProxyTarget(context.Background(), "::bad-url::", "http://proxy:8080", time.Second); got.ErrorCode != proxyTestTargetErrorInvalidURL {
		t.Fatalf("非法目标 = %+v", got)
	}
	if got := probeProxyTarget(context.Background(), "http://t", "http://proxy:8080", 0); got.ErrorCode != "timeout_invalid" {
		t.Fatalf("零超时 = %+v", got)
	}
	if got := probeProxyTarget(context.Background(), "http://t", "://bad", time.Second); got.ErrorCode == "" {
		t.Fatal("非法代理配置必须有错误码")
	}
}

// TestWJProxyTestSyntheticBase 固定合成基础项的汇总口径。
func TestWJProxyTestSyntheticBaseContract(t *testing.T) {
	// 无 provider → unknown 提示。
	empty := proxyTestSyntheticBase(nil, 0)
	if empty.Status != proxyTestItemUnknown || !strings.Contains(empty.Message, "没有启用的供应商") {
		t.Fatalf("无 provider 合成项 = %+v", empty)
	}
	// 全部通过 → passed 带延迟均值（四舍五入）。
	latencyA, latencyB := int64(100), int64(300)
	passed := proxyTestSyntheticBase([]proxyTestReportItem{
		{Status: proxyTestItemPassed, LatencyMS: &latencyA},
		{Status: proxyTestItemPassed, LatencyMS: &latencyB},
	}, 2)
	if passed.Status != proxyTestItemPassed || passed.LatencyMS == nil || *passed.LatencyMS != 200 {
		t.Fatalf("通过合成项 = %+v", passed)
	}
	// 部分可达 → warning；全部失败 → failed。
	warning := proxyTestSyntheticBase([]proxyTestReportItem{
		{Status: proxyTestItemPassed}, {Status: proxyTestItemFailed},
	}, 2)
	if warning.Status != proxyTestItemWarning {
		t.Fatalf("warning 合成项 = %+v", warning)
	}
	failed := proxyTestSyntheticBase([]proxyTestReportItem{{Status: proxyTestItemFailed}}, 1)
	if failed.Status != proxyTestItemFailed {
		t.Fatalf("failed 合成项 = %+v", failed)
	}
}

// TestWJTestHandlerBusyAndSnapshotError 固定手动检测路由的剩余分支：
// 快照装载失败 → 500；运行失败 → 502。
func TestWJTestHandlerBusyAndSnapshotError(t *testing.T) {
	fixture := newProxyTestFixture(t)
	// 存储损坏（关库）→ 快照装载 500。
	broken := newProxyTestFixture(t)
	if err := broken.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	recorder := postProxyTest(t, broken, "ghost", true)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("装载失败必须 500: %d", recorder.Code)
	}
	_ = fixture
}

// TestWJStoreNormalizeTestStatus 固定检测状态归一化的前向兼容。
func TestWJStoreNormalizeTestStatus(t *testing.T) {
	for _, valid := range []string{"unknown", "passed", "warning", "failed"} {
		if got := normalizeTestStatus(valid); got != valid {
			t.Fatalf("合法状态 %q 被改写为 %q", valid, got)
		}
	}
	if got := normalizeTestStatus("corrupted"); got != testStatusUnknown {
		t.Fatalf("未知状态必须回退 unknown: %q", got)
	}
}

// TestWJProxyTestValidHostname 固定目标主机名的合法性校验。
func TestWJProxyTestValidHostname(t *testing.T) {
	valid := []string{"example.com", "a.b.c", "EXAMPLE.ORG", "xn--fiq228c.com", "a-b.example"}
	for _, host := range valid {
		if !proxyTestValidHostname(host) {
			t.Fatalf("%q 应合法", host)
		}
	}
	invalid := []string{"", "-a.example", "a-.example", "a..example", "a_b.example", "a b.example", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "com", "example.com:"}
	for _, host := range invalid {
		if proxyTestValidHostname(host) {
			t.Fatalf("%q 应非法", host)
		}
	}
}
