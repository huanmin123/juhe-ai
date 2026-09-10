package proxylatency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 Runner 生命周期钩子（Run 循环分支、DB 闸门包装、runCycle
// 校验失败路径、健康端点）、manual 报告投影与 outcome 校验，以及 jobs
// Store 的 committed outcome 读取方与执行输入校验。

type wfFakeReader struct {
	drafts []InputDraft
	err    error
}

func (r *wfFakeReader) LoadDue(context.Context, int) ([]InputDraft, error) {
	return r.drafts, r.err
}

func TestWFRunnerRunLoopBranches(t *testing.T) {
	runner := NewRunner(RuntimeConfig{InstanceID: "wf", Interval: time.Hour, OwnerLease: time.Hour}, &Store{}, &wfFakeReader{}, nil)
	newCtx := func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }

	// acquire 失败：钩子先取消 ctx，Run 在记账后以 context.Canceled 退出。
	{
		ctx, cancel := newCtx()
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			cancel()
			return OwnerLease{}, false, errors.New("acquire boom")
		}
		if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire 失败路径 err=%v", err)
		}
		if !strings.Contains(runner.Status().LastError, "acquire boom") {
			t.Fatalf("LastError=%q", runner.Status().LastError)
		}
	}

	// 未获取 lease：静默等待后退出。
	{
		ctx, cancel := newCtx()
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			cancel()
			return OwnerLease{}, false, nil
		}
		if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("未获取 lease 路径 err=%v", err)
		}
	}

	// 完整一轮：获取成功、runOwned 正常、释放成功。
	{
		ctx, cancel := newCtx()
		lease := leaseForTest()
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			cancel()
			return lease, true, nil
		}
		runner.releaseOwnerLease = func(context.Context, OwnerLease) error { return nil }
		runner.runOwnedFn = func(context.Context, OwnerLease) error { return nil }
		if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("正常循环路径 err=%v", err)
		}
	}

	// 释放失败：必须记账（不得吞掉）。
	{
		ctx, cancel := newCtx()
		lease := leaseForTest()
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			cancel()
			return lease, true, nil
		}
		releaseFailed := 0
		runner.releaseOwnerLease = func(context.Context, OwnerLease) error {
			releaseFailed++
			return errors.New("release boom")
		}
		runner.runOwnedFn = func(context.Context, OwnerLease) error { return nil }
		if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("释放失败路径 err=%v", err)
		}
		if releaseFailed != 1 || !strings.Contains(runner.Status().LastError, "release boom") {
			t.Fatalf("释放失败未记账 releaseFailed=%d LastError=%q", releaseFailed, runner.Status().LastError)
		}
	}

	// runOwned 失败：记账后退出。
	{
		ctx, cancel := newCtx()
		lease := leaseForTest()
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			cancel()
			return lease, true, nil
		}
		runner.releaseOwnerLease = func(context.Context, OwnerLease) error { return nil }
		runner.runOwnedFn = func(context.Context, OwnerLease) error { return errors.New("cycle boom") }
		if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("runOwned 失败路径 err=%v", err)
		}
		if !strings.Contains(runner.Status().LastError, "cycle boom") {
			t.Fatalf("LastError=%q", runner.Status().LastError)
		}
	}

	// 未初始化的 runner 必须拒绝。
	if err := (&Runner{}).Run(context.Background()); err == nil {
		t.Fatal("缺 store/reader 必须报错")
	}
	if err := NewRunner(RuntimeConfig{ResultPostgresURL: "postgres://x"}, &Store{}, &wfFakeReader{}, nil).Run(context.Background()); err == nil {
		t.Fatal("配置了结果库但缺 projector 必须报错")
	}
}

func TestWFRunnerRunCycleVerifyFailure(t *testing.T) {
	store := wfOpenJobsStore(t)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf", Interval: time.Hour, OwnerLease: time.Hour}, store, &wfFakeReader{}, nil)
	runner.runOwnedFn = nil
	// 直接走 runCycle：空 owner 表 → ErrOwnerLeaseLost，记入状态。
	err := runner.runCycle(context.Background(), OwnerLease{OwnerID: "wf", FenceToken: 3})
	if err == nil || !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("runCycle err=%v", err)
	}
	if runner.Status().LastError == "" {
		t.Fatal("失败必须记入状态")
	}
	// 再次进入 runOwned：ctx 取消后立即退出。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.runOwned(ctx, leaseForTest()); err == nil && ctx.Err() == nil {
		t.Fatal("取消的 ownedCtx 必须带错误退出")
	}
}

func leaseForTest() OwnerLease { return OwnerLease{OwnerID: "wf", FenceToken: 1} }

func TestWFRunnerDBGateWrappers(t *testing.T) {
	runner := NewRunner(RuntimeConfig{}, &Store{}, &wfFakeReader{}, nil)
	ctx := context.Background()

	called := false
	if err := runner.withDB(ctx, nil, "op", func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("nil 闸门必须直调 fn err=%v called=%v", err, called)
	}
	gate := NewDBConcurrencyGate(1, 1)
	var out int
	if err := withDBValue(runner, ctx, gate, "op", func() (int, error) { return 7, nil }, &out); err != nil || out != 7 {
		t.Fatalf("withDBValue=%d err=%v", out, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	held, _, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("预占闸门失败: %v", err)
	}
	if err := runner.withDB(cancelCtx, gate, "op", func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("闸门取消 err=%v", err)
	}
	var out2 int
	if err := withDBValue(runner, cancelCtx, gate, "op", func() (int, error) { return 0, nil }, &out2); !errors.Is(err, context.Canceled) {
		t.Fatalf("withDBValue 取消 err=%v", err)
	}
	held()

	// projectOutcomeWithGate：nil projector 时无论如何都必须成功。
	if err := runner.projectOutcomeWithGate(ctx, Outcome{}, gate); err != nil {
		t.Fatalf("projectOutcomeWithGate err=%v", err)
	}
	// 手动投影 disposition 的未知分支。
	if _, err := manualProjectionDisposition(ProjectionResult{Disposition: ProjectionDisposition("weird")}); err == nil {
		t.Fatal("未知 disposition 必须报错")
	}
}

func TestWFRunnerSchedulingDefaultsAndHealth(t *testing.T) {
	runner := NewRunner(RuntimeConfig{}, &Store{}, &wfFakeReader{}, nil)
	if runner.batchSize() != 1 || runner.candidatePoolLimit() != 1 || runner.workerConcurrency() != 1 {
		t.Fatalf("默认调度=%d/%d/%d", runner.batchSize(), runner.candidatePoolLimit(), runner.workerConcurrency())
	}
	if runner.dbConcurrency() != 0 || runner.dbQueueSize() != 0 {
		t.Fatal("未配置时闸门必须关闭")
	}
	if _, ok := runner.currentOwnerLease(); ok {
		t.Fatal("未持有 lease 时不得复用")
	}
	lease := leaseForTest()
	runner.setOwnerLease(lease)
	runner.setOwnerHeld(true)
	if got, ok := runner.currentOwnerLease(); !ok || got != lease {
		t.Fatalf("currentOwnerLease=%+v", got)
	}
	runner.clearOwnerLease()
	if _, ok := runner.currentOwnerLease(); ok {
		t.Fatal("清除后不得复用")
	}

	// 健康端点：未就绪 → 503 + JSON 字段；路径/方法不匹配 → 404。
	handler := runner.HealthHandler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("未就绪 status=%d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("健康负载解析失败: %v", err)
	}
	ready, _ := payload["ready"].(bool)
	if ready {
		t.Fatal("未就绪不得报 ready")
	}
	postHealth := httptest.NewRecorder()
	handler.ServeHTTP(postHealth, httptest.NewRequest(http.MethodPost, "/health", nil))
	if postHealth.Code != http.StatusNotFound {
		t.Fatalf("POST /health=%d", postHealth.Code)
	}
	missHealth := httptest.NewRecorder()
	handler.ServeHTTP(missHealth, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if missHealth.Code != http.StatusNotFound {
		t.Fatalf("GET /nope=%d", missHealth.Code)
	}

	if formatRuntimeTime(time.Time{}) != "" {
		t.Fatal("零时间必须输出空串")
	}
	if minRuntime(2*time.Second, 3*time.Second) != 2*time.Second {
		t.Fatal("minRuntime 错误")
	}
	if executionWindowUntil(wfProjBase, wfProjBase.Add(time.Minute), wfProjBase.Add(-time.Second)) != 0 {
		t.Fatal("已过期 lease 窗口必须为 0")
	}
}

func wfManualRequest() ManualRequest {
	return ManualRequest{
		SchemaVersion: 1, ProxyID: "p-1", ProxyName: "代理一", ConfigRevision: wfProjRevision,
		ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
		Targets: []ManualTarget{{Provider: "gpt", ProfileID: "profile-gpt", Name: "OpenAI", URL: "https://api.openai.com/v1"}},
	}
}

func TestWFManualRequestValidation(t *testing.T) {
	if err := wfManualRequest().Validate(25 * time.Second); err != nil {
		t.Fatalf("合法请求 err=%v", err)
	}
	cases := []struct {
		name   string
		mutate func(*ManualRequest)
	}{
		{name: "schema 版本", mutate: func(r *ManualRequest) { r.SchemaVersion = 2 }},
		{name: "代理 id", mutate: func(r *ManualRequest) { r.ProxyID = " " }},
		{name: "代理名", mutate: func(r *ManualRequest) { r.ProxyName = "" }},
		{name: "代理类型", mutate: func(r *ManualRequest) { r.ProxyType = "socks4" }},
		{name: "端口", mutate: func(r *ManualRequest) { r.ProxyPort = 70000 }},
		{name: "凭据缺用户名", mutate: func(r *ManualRequest) {
			r.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: wfTestEnvelope()}
		}},
		{name: "deadline 过小", mutate: func(r *ManualRequest) { r.DeadlineMS = 100 }},
		{name: "deadline 过大", mutate: func(r *ManualRequest) { r.DeadlineMS = 60000 }},
		{name: "revision 非法", mutate: func(r *ManualRequest) { r.ConfigRevision = "yesterday" }},
		{name: "target 名缺失", mutate: func(r *ManualRequest) { r.Targets[0].Name = "" }},
		{name: "target 重复", mutate: func(r *ManualRequest) {
			r.Targets = append(r.Targets, ManualTarget{Provider: "gpt", ProfileID: "p2", Name: "n2", URL: "https://x.invalid"})
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			request := wfManualRequest()
			tt.mutate(&request)
			if err := request.Validate(25 * time.Second); err == nil {
				t.Fatalf("%s 必须拒绝", tt.name)
			}
		})
	}
	// 无 targets 的请求合法（no-provider 报告路径）。
	bare := wfManualRequest()
	bare.Targets = nil
	if err := bare.Validate(25 * time.Second); err != nil {
		t.Fatalf("无 targets err=%v", err)
	}
	// Deadline 上限裁剪到 maxDeadline 之内。
	bounded := wfManualRequest()
	bounded.DeadlineMS = 20000
	if err := bounded.Validate(10 * time.Second); err == nil {
		t.Fatal("deadline 超过 maxDeadline 必须拒绝")
	}
}

func TestWFManualValidateOutcomeAndDraft(t *testing.T) {
	request := wfManualRequest()
	outcome := Outcome{
		ProxyID: "p-1", Trigger: TriggerManual, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed}},
	}
	if err := request.ValidateOutcome(outcome); err != nil {
		t.Fatalf("合法 outcome err=%v", err)
	}
	if err := request.ValidateOutcome(Outcome{}); err == nil {
		t.Fatal("空 outcome 必须拒绝")
	}
	wrongProxy := outcome
	wrongProxy.ProxyID = "other"
	if err := request.ValidateOutcome(wrongProxy); err == nil {
		t.Fatal("代理不匹配必须拒绝")
	}
	noItems := outcome
	noItems.Items = nil
	if err := request.ValidateOutcome(noItems); err == nil {
		t.Fatal("空 items 必须拒绝")
	}
	undeclared := outcome
	undeclared.Items = []ItemResult{{Provider: "glm", ProfileID: "profile-glm", Status: ItemPassed}}
	if err := request.ValidateOutcome(undeclared); err == nil || !strings.Contains(err.Error(), "未声明") {
		t.Fatalf("未声明 provider err=%v", err)
	}
	// 数量一致但 provider 重复（第二个 item 复用已声明的 gpt）。
	dualRequest := wfManualRequest()
	dualRequest.Targets = append(dualRequest.Targets, ManualTarget{Provider: "gemini", ProfileID: "profile-gemini", Name: "Gemini", URL: "https://gemini.invalid/v1"})
	duplicate := outcome
	duplicate.Items = []ItemResult{
		{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed},
		{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed},
	}
	if err := dualRequest.ValidateOutcome(duplicate); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复 provider err=%v", err)
	}
	mismatch := outcome
	mismatch.OverallStatus = OverallFailed
	if err := request.ValidateOutcome(mismatch); err == nil {
		t.Fatal("overall 不匹配必须拒绝")
	}

	// InputDraft：由请求派生规范草稿。
	now := wfProjBase.UTC()
	draft := request.InputDraft(now, time.Minute)
	if draft.ProxyID != "p-1" || draft.Trigger != TriggerManual || len(draft.Targets) != 1 {
		t.Fatalf("draft=%+v", draft)
	}
	if draft.IssuedAt != now || draft.ExpiresAt != now.Add(time.Minute) {
		t.Fatalf("draft 时间=%v/%v", draft.IssuedAt, draft.ExpiresAt)
	}
	if err := validateInputDraft(draft); err != nil {
		t.Fatalf("规范草稿必须通过校验: %v", err)
	}
	badDraft := draft
	badDraft.ProxyType = "socks4"
	if err := validateInputDraft(badDraft); err == nil {
		t.Fatal("非法草稿必须拒绝")
	}
}

func TestWFManualReportProjection(t *testing.T) {
	// 全部通过：分数/等级/消息契约。
	request := wfManualRequest()
	report := request.Report(Outcome{
		ProxyID: "p-1", ObservedAt: wfProjBase, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 120}},
	})
	if report.Status != OverallPassed || report.Grade != "A" || report.Score != 100 || report.PassedCount != 2 {
		t.Fatalf("通过报告=%+v", report)
	}
	if report.Message != "代理质量检测通过" || report.BaseLatencyMS == nil || *report.BaseLatencyMS != 120 {
		t.Fatalf("通过消息=%q base=%v", report.Message, report.BaseLatencyMS)
	}
	// Items[0] 是合成基础项，provider 明细在其后。
	if !strings.Contains(report.Items[1].Message, "HTTP 200") {
		t.Fatalf("item 消息=%q", report.Items[1].Message)
	}
	if report.Items[0].Name != "基础连通性" {
		t.Fatalf("基础项=%+v", report.Items[0])
	}

	// 失败：分数下限 0、等级 D。
	failed := request.Report(Outcome{
		ProxyID: "p-1", ObservedAt: wfProjBase, OverallStatus: OverallFailed,
		Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemFailed, ErrorCode: "transport"}},
	})
	// 分数 = 100 - 2*35 = 30（基础项+明细项各计一次失败）。
	if failed.Status != OverallFailed || failed.Score != 30 || failed.Grade != "D" || failed.FailedCount != 2 {
		t.Fatalf("失败报告=%+v", failed)
	}
	// itemMessage 分支：HTTP 状态 > legacy 失败码 > 错误码透传 > 状态兜底。
	if failed.Items[1].Message != "transport" {
		t.Fatalf("错误码透传=%q", failed.Items[1].Message)
	}
	if msg := itemMessage(ItemResult{Status: ItemFailed}, "https://x"); msg != "代理传输失败" {
		t.Fatalf("失败消息=%q", msg)
	}
	if msg := itemMessage(ItemResult{Status: ItemUnknown}, "https://x"); msg != "未形成真实代理检测请求" {
		t.Fatalf("unknown 消息=%q", msg)
	}
	if msg := itemMessage(ItemResult{Status: ItemPassed}, "https://x"); msg != "代理目标检测完成" {
		t.Fatalf("passed 消息=%q", msg)
	}
	if failed.Items[0].Message != "供应商默认地址全部发生传输失败" {
		t.Fatalf("失败基础项=%q", failed.Items[0].Message)
	}

	// 无 targets：unknown 合成项。
	bare := wfManualRequest()
	bare.Targets = nil
	empty := bare.Report(Outcome{ProxyID: "p-1", ObservedAt: wfProjBase, OverallStatus: OverallUnknown})
	if empty.Status != OverallUnknown || len(empty.Items) != 1 || empty.Items[0].Name != "基础连通性" {
		t.Fatalf("无目标报告=%+v", empty.Items)
	}

	// legacy 非法 URL 失败码 → Node 消息还原。
	legacy := request.Report(Outcome{
		ProxyID: "p-1", ObservedAt: wfProjBase, OverallStatus: OverallUnknown,
		Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemUnknown, ErrorCode: targetProbeErrorInvalidURL}},
	})
	if !strings.Contains(legacy.Items[1].Message, "Invalid URL") {
		t.Fatalf("legacy 消息=%q", legacy.Items[1].Message)
	}

	// MarshalJSON/UnmarshalJSON 的 targetUrl 契约。
	encoded, err := json.Marshal(report.Items[1])
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if !strings.Contains(string(encoded), "targetUrl") {
		t.Fatal("provider item 必须携带 targetUrl")
	}
	var decoded ProxyTestItem
	if err := json.Unmarshal(encoded, &decoded); err != nil || !decoded.includeTargetURL {
		t.Fatalf("解码=%+v err=%v", decoded, err)
	}
	bareEncoded, err := json.Marshal(ProxyTestItem{Name: "n"})
	if err != nil {
		t.Fatalf("编码合成项失败: %v", err)
	}
	if strings.Contains(string(bareEncoded), "targetUrl") {
		t.Fatal("合成项不得携带 targetUrl")
	}
	var synthetic ProxyTestItem
	if err := json.Unmarshal(bareEncoded, &synthetic); err != nil || synthetic.includeTargetURL {
		t.Fatalf("合成项解码=%+v err=%v", synthetic, err)
	}
}

func TestWFLegacyTargetProtocol(t *testing.T) {
	if protocol, ok := legacyUnsupportedTargetProtocol("ftp://provider.invalid/v1"); !ok || protocol != "ftp" {
		t.Fatalf("ftp protocol=%q ok=%v", protocol, ok)
	}
	if protocol, ok := legacyUnsupportedTargetProtocol("ftp:"); ok {
		t.Fatalf("无 authority 的 ftp=%q", protocol)
	}
	if _, ok := legacyUnsupportedTargetProtocol("http://x.invalid"); ok {
		t.Fatal("http 不得报不支持协议")
	}
	if _, ok := legacyUnsupportedTargetProtocol("https://x.invalid"); ok {
		t.Fatal("https 不得报不支持协议")
	}
	if _, ok := legacyUnsupportedTargetProtocol("::bad"); ok {
		t.Fatal("解析失败不得报协议")
	}
	if _, ok := legacyUnsupportedTargetProtocol(""); ok {
		t.Fatal("空 URL 不得报协议")
	}
}

func TestWFManualOutboundProbe(t *testing.T) {
	// 用 httptest 服务器充当正向代理：目标 URL 固定为外网地址，但请求实际
	// 只到达本地测试服务器，不产生真实外网流量。
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","query":"1.2.3.4","country":"日本"}`))
	}))
	t.Cleanup(server.Close)
	ctx := context.Background()
	info, ok := probeManualOutbound(ctx, server.URL, 5*time.Second)
	if !ok || info.IP != "1.2.3.4" || info.Region != "日本" {
		t.Fatalf("outbound=%+v ok=%v", info, ok)
	}

	// 全部目标都返回损坏 JSON → 失败。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not-json"))
	}))
	t.Cleanup(broken.Close)
	if _, ok := probeManualOutbound(ctx, broken.URL, 5*time.Second); ok {
		t.Fatal("损坏响应必须失败")
	}

	// 非 200 状态。
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(denied.Close)
	if _, ok := probeManualOutbound(ctx, denied.URL, 5*time.Second); ok {
		t.Fatal("非 200 必须失败")
	}

	// 无效代理 URL（代理构造失败）。
	if _, ok := probeManualOutbound(ctx, "://bad-proxy", 5*time.Second); ok {
		t.Fatal("非法代理必须失败")
	}

	// 超时/取消边界。
	if _, ok := probeManualOutbound(ctx, server.URL, 0); ok {
		t.Fatal("零超时必须失败")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := probeManualOutbound(cancelled, server.URL, 5*time.Second); ok {
		t.Fatal("ctx 取消必须失败")
	}

	// parseManualOutbound 各解析器契约。
	tests := []struct {
		name    string
		parser  string
		body    string
		wantIP  string
		wantOK  bool
		wantGeo string
	}{
		{name: "ip-api 失败状态", parser: "ip-api", body: `{"status":"fail"}`, wantOK: false},
		{name: "ip-api 成功", parser: "ip-api", body: `{"status":"success","query":"1.1.1.1","country":"","countryCode":"JP","regionName":"Tokyo"}`, wantIP: "1.1.1.1", wantOK: true, wantGeo: "JP"},
		{name: "ipwhois 失败", parser: "ipwhois", body: `{"success":false}`, wantOK: false},
		{name: "ipwhois 成功", parser: "ipwhois", body: `{"ip":"2.2.2.2","country":"Japan"}`, wantIP: "2.2.2.2", wantOK: true, wantGeo: "Japan"},
		{name: "ipsb 成功", parser: "ipsb", body: `{"ip":" 3.3.3.3 ","country_code":"us"}`, wantIP: "3.3.3.3", wantOK: true, wantGeo: "US"},
		{name: "ipinfo 成功", parser: "ipinfo", body: `{"ip":"4.4.4.4","country":"US","region":"CA","city":"LA"}`, wantIP: "4.4.4.4", wantOK: true, wantGeo: "US"},
		{name: "ipify 成功", parser: "ipify", body: `{"ip":"5.5.5.5"}`, wantIP: "5.5.5.5", wantOK: true},
		{name: "httpbin 多 IP", parser: "httpbin", body: `{"origin":"6.6.6.6, 7.7.7.7"}`, wantIP: "6.6.6.6", wantOK: true},
		{name: "未知解析器", parser: "other", body: `{}`, wantOK: false},
		{name: "非 JSON", parser: "ipify", body: "nope", wantOK: false},
		{name: "缺 IP", parser: "ipify", body: `{"ip":""}`, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := parseManualOutbound(tt.parser, []byte(tt.body))
			if ok != tt.wantOK || (tt.wantOK && info.IP != tt.wantIP) {
				t.Fatalf("info=%+v ok=%v", info, ok)
			}
			if tt.wantGeo != "" && info.Region != tt.wantGeo {
				t.Fatalf("地区=%q want %q", info.Region, tt.wantGeo)
			}
		})
	}
}

func TestWFStoreCommittedOutcomeReaders(t *testing.T) {
	store := wfOpenJobsStore(t)
	committer := newWFCommitter(t, store)
	outcome := committer.commit("p-1", wfProjRevision)
	issued := committer.last
	ctx := context.Background()

	// VerifyExecutionInput：为 p-verify 独立签发输入（fence 与代理一致）。
	owner := committer.owner
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-verify", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	issuedVerify, err := store.IssueInput(ctx, InputDraft{
		ProxyID: "p-verify", ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: wfProjBase.UTC(), ExpiresAt: wfProjBase.Add(5 * time.Minute).UTC(), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	})
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	if err := store.VerifyExecutionInput(ctx, owner, proxy, issuedVerify); err != nil {
		t.Fatalf("执行输入校验失败: %v", err)
	}
	mismatched := issuedVerify
	mismatched.InputVersion = 99
	if err := store.VerifyExecutionInput(ctx, owner, proxy, mismatched); !errors.Is(err, ErrInputFence) {
		t.Fatalf("版本不匹配 err=%v", err)
	}
	if err := store.VerifyExecutionInput(ctx, OwnerLease{}, proxy, issuedVerify); err == nil {
		t.Fatal("owner 不匹配必须拒绝")
	}

	// LoadCommittedOutcome：重放已提交 outcome。
	loaded, found, err := store.LoadCommittedOutcome(ctx, issued)
	if err != nil || !found || loaded.OutcomeID != outcome.OutcomeID {
		t.Fatalf("重放 found=%v loaded=%+v err=%v", found, loaded, err)
	}
	if _, found, err := store.LoadCommittedOutcome(ctx, IssuedInput{RequestID: " ", InputVersion: 1}); err == nil || found {
		t.Fatal("非法 input 必须拒绝")
	}
	foreign := IssuedInput{RequestID: "j3a-missing", InputVersion: 1, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: wfProjBase.UTC(), ExpiresAt: wfProjBase.Add(5 * time.Minute).UTC(), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}}}
	if _, _, err := store.LoadCommittedOutcome(ctx, foreign); !errors.Is(err, ErrInputFence) {
		t.Fatalf("缺失重放 err=%v", err)
	}
	tampered := issued
	tampered.InputVersion = 7
	if _, _, err := store.LoadCommittedOutcome(ctx, tampered); !errors.Is(err, ErrInputFence) {
		t.Fatalf("篡改重放 err=%v", err)
	}
	// ErrRequestConflict 的 payload 篡改分支由 store_test 的毒化摘要用例覆盖。

	// FindCommittedOutcome：按 outcome id 读取。
	stored, found, err := store.FindCommittedOutcome(ctx, outcome.OutcomeID)
	if err != nil || !found || stored.Outcome.ProxyID != "p-1" || stored.StoredAt.IsZero() {
		t.Fatalf("find found=%v stored=%+v err=%v", found, stored, err)
	}
	if _, found, err := store.FindCommittedOutcome(ctx, "missing"); err != nil || found {
		t.Fatalf("missing found=%v err=%v", found, err)
	}
	if _, _, err := store.FindCommittedOutcome(ctx, " "); err == nil {
		t.Fatal("空 id 必须拒绝")
	}

	// ListCommittedOutcomes：limit 与 cursor 参数校验。
	if _, err := store.ListCommittedOutcomes(ctx, nil, 0); err == nil {
		t.Fatal("limit=0 必须拒绝")
	}
	if _, err := store.ListCommittedOutcomes(ctx, nil, maxProxyLatencyWorkItems+1); err == nil {
		t.Fatal("超上限 limit 必须拒绝")
	}
	if _, err := store.ListCommittedOutcomes(ctx, &OutcomeCursor{StoredAt: wfProjBase}, 1); err == nil {
		t.Fatal("缺 outcome id 的 cursor 必须拒绝")
	}
	listed, err := store.ListCommittedOutcomes(ctx, nil, 10)
	if err != nil || len(listed) != 1 || listed[0].OutcomeID != outcome.OutcomeID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	after := listed[0].StoredAt
	more, err := store.ListCommittedOutcomes(ctx, &OutcomeCursor{StoredAt: after, OutcomeID: outcome.OutcomeID}, 10)
	if err != nil || len(more) != 0 {
		t.Fatalf("cursor 之后=%d err=%v", len(more), err)
	}

	// ReleaseExecutionClaim：参数校验与真实释放。
	if err := store.ReleaseExecutionClaim(ctx, "", "token"); !errors.Is(err, ErrInputFence) {
		t.Fatalf("空参数 err=%v", err)
	}
	proxyP1, ok, err := store.AcquireProxyLease(ctx, owner, "p-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("重领 p-1 lease ok=%v err=%v", ok, err)
	}
	_, claimToken, replay, err := store.AdmitExecution(ctx, owner, proxyP1, issued)
	// 该 input 已提交 outcome：AdmitExecution 走 committed 重放，claimToken 为空。
	if err != nil {
		t.Fatalf("AdmitExecution 失败: %v", err)
	}
	if claimToken != "" || replay == nil || replay.OutcomeID != outcome.OutcomeID {
		t.Fatalf("已提交 outcome 必须重放 claim=%q replay=%+v", claimToken, replay)
	}
	if _, _, _, err := store.AdmitExecution(ctx, owner, proxy, mismatched); !errors.Is(err, ErrInputFence) {
		t.Fatalf("篡改 input 必须拒绝 err=%v", err)
	}
	// 未提交的输入 admission 会创建 execution claim，随后可释放。
	verifiedClaim, claimToken2, replay2, err := store.AdmitExecution(ctx, owner, proxy, issuedVerify)
	if err != nil || replay2 != nil || claimToken2 == "" || verifiedClaim.RequestID != issuedVerify.RequestID {
		t.Fatalf("首次 admission claim=%q replay=%+v err=%v", claimToken2, replay2, err)
	}
	if err := store.ReleaseExecutionClaim(ctx, issuedVerify.RequestID, claimToken2); err != nil {
		t.Fatalf("释放 claim 失败: %v", err)
	}
}

func TestWFStoreTimeAndSchemaHelpers(t *testing.T) {
	want := wfProjBase
	parsed, err := sqlTime(want)
	if err != nil || !parsed.Equal(want) {
		t.Fatalf("time.Time=%v err=%v", parsed, err)
	}
	parsed, err = sqlTime(want.Format(time.RFC3339Nano))
	if err != nil || !parsed.Equal(want) {
		t.Fatalf("string=%v err=%v", parsed, err)
	}
	parsed, err = sqlTime([]byte(want.Format(time.RFC3339Nano)))
	if err != nil || !parsed.Equal(want) {
		t.Fatalf("bytes=%v err=%v", parsed, err)
	}
	if _, err := sqlTime(struct{}{}); err == nil {
		t.Fatal("未知类型必须报错")
	}
	if _, err := sqlTime("not-a-time"); err == nil {
		t.Fatal("非法字符串必须报错")
	}
	for _, forbidden := range []string{"CREATE SCHEMA x", "ALTER TABLE x", "DROP TABLE x", "TRUNCATE x", "juhe_business.proxy_profiles"} {
		if !containsForbiddenPostgresDDL(forbidden) {
			t.Fatalf("%q 必须被判为禁止 DDL", forbidden)
		}
	}
	if containsForbiddenPostgresDDL("CREATE TABLE IF NOT EXISTS juhe_jobs.x (id TEXT)") {
		t.Fatal("合法 jobs DDL 不得误判")
	}
	stamp := canonicalPostgresTimestamp(want.Add(500 * time.Nanosecond))
	if stamp.Nanosecond()%1000 != 0 {
		t.Fatalf("纳秒未截断：%d", stamp.Nanosecond())
	}
}
