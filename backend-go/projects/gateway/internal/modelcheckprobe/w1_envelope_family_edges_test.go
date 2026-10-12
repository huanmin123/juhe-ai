package modelcheckprobe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// These transports keep the family tests local and deterministic. The normal
// response path deliberately reuses the existing all-family echo fixture.
type w1FamilyTransport struct {
	inner  *wlEchoTransport
	failAt int
	mu     sync.Mutex
	calls  int
}

func (t *w1FamilyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	call := t.calls
	t.mu.Unlock()
	if t.failAt > 0 && call == t.failAt {
		return w1W1HTTPFailure(request, http.StatusServiceUnavailable), nil
	}
	return t.inner.RoundTrip(request)
}

func (t *w1FamilyTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type w1SoftFailureTransport struct {
	inner             *wlEchoTransport
	failDistribution  bool
	failConsistency   bool
	mu                sync.Mutex
	calls             int
	distributionFails int
	consistencyFails  int
}

func (t *w1SoftFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	text := string(body)
	t.mu.Lock()
	t.calls++
	if w1IsSamplingDistributionPrompt(text) && t.failDistribution && t.distributionFails == 0 {
		t.distributionFails++
		t.mu.Unlock()
		return w1W1HTTPErrorEnvelope(request, http.StatusOK, "w1 soft distribution failure"), nil
	}
	if strings.Contains(text, "仓库清点锚点题") && t.failConsistency && t.consistencyFails == 0 {
		t.consistencyFails++
		t.mu.Unlock()
		return w1W1HTTPErrorEnvelope(request, http.StatusOK, "w1 soft consistency failure"), nil
	}
	t.mu.Unlock()
	return t.inner.RoundTrip(request)
}

func w1IsSamplingDistributionPrompt(text string) bool {
	for _, unit := range samplingUnitDefinitions {
		for _, variant := range unit.variants {
			if strings.Contains(text, variant) {
				return true
			}
		}
	}
	return false
}

func (t *w1SoftFailureTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// w1DistributionTransport fails only on the first distribution prompt. It is
// used to verify RunTrustedComparison's direct distribution/error boundary
// without disturbing the preceding basic request.
type w1DistributionTransport struct {
	inner         *wlEchoTransport
	fail          bool
	modelUnavail  bool
	mu            sync.Mutex
	distributionN int
}

func (t *w1DistributionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	if strings.Contains(string(body), "向量数据库") {
		t.mu.Lock()
		t.distributionN++
		first := t.distributionN == 1
		t.mu.Unlock()
		if first && t.fail {
			return w1W1HTTPFailure(request, http.StatusServiceUnavailable), nil
		}
		if first && t.modelUnavail {
			return w1W1HTTPModelUnavailable(request), nil
		}
	}
	return t.inner.RoundTrip(request)
}

func (t *w1DistributionTransport) distributionCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.distributionN
}

func w1W1HTTPFailure(request *http.Request, status int) *http.Response {
	return w1W1HTTPErrorEnvelope(request, status, "w1 hard failure")
}

func w1W1HTTPErrorEnvelope(request *http.Request, status int, message string) *http.Response {
	body := `{"error":{"message":"` + message + `"}}`
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func w1W1HTTPModelUnavailable(request *http.Request) *http.Response {
	body := `{"error":{"code":"model_not_found","message":"requested model is unavailable"}}`
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func w1SamplingSuite(endpoint string, transport http.RoundTripper, model string) Suite {
	return Suite{
		Endpoint:                  endpoint,
		Client:                    &http.Client{Transport: transport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     model,
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode:              modelcheckprofile.EndpointModeResponsesJSON,
		UpstreamProtocol:          modelcheckprofile.ProtocolOpenAIResponses,
		UpstreamEndpointMode:      modelcheckprofile.EndpointModeResponsesJSON,
		Retry:                     RetryOptions{AttemptTimeouts: []time.Duration{time.Second}},
		probeCapture:              newProbeCapture(),
	}
}

func w1Anchor(t *testing.T) IdentityAnchorProbe {
	t.Helper()
	anchor, err := BuildIdentityAnchorProbe()
	if err != nil {
		t.Fatal(err)
	}
	return anchor
}

func TestW1RunSamplingStatisticsSoftFailuresRemainPartial(t *testing.T) {
	targetTransport := &w1SoftFailureTransport{inner: &wlEchoTransport{}, failDistribution: true, failConsistency: true}
	comparisonTransport := &w1SoftFailureTransport{inner: &wlEchoTransport{}, failDistribution: true, failConsistency: true}
	target := w1SamplingSuite("https://w1-sampling-target.example", targetTransport, "gpt-6-luna")
	comparison := w1SamplingSuite("https://w1-sampling-comparison.example", comparisonTransport, "gpt-6-luna")
	target.Comparison = &comparison
	item, terminal, err := RunSamplingStatistics(context.Background(), target, time.Second, w1Anchor(t))
	if err != nil || terminal {
		t.Fatalf("HTTP 200 语义失败不得终局: err=%v terminal=%v", err, terminal)
	}
	if item.Status != "passed" || item.Score != 0 || item.MaxScore != 0 {
		t.Fatalf("部分采样应保持 evidence-only 通过且零分: %+v", item)
	}
	if got := item.Evidence["requestFailureCount"]; got != 4 {
		t.Fatalf("requestFailureCount=%v, want 4", got)
	}
	if got := item.Evidence["scoringProbeCount"]; got != 56 {
		t.Fatalf("scoringProbeCount=%v, want 56", got)
	}
	if item.Evidence["partial"] != true {
		t.Fatalf("四个语义失败必须标 partial: %+v", item.Evidence)
	}
	if targetTransport.count() != 30 || comparisonTransport.count() != 30 {
		t.Fatalf("HTTP 200 失败不能缩短采样轮次: target=%d comparison=%d", targetTransport.count(), comparisonTransport.count())
	}
}

func TestW1RunSamplingStatisticsTerminalPositions(t *testing.T) {
	tests := []struct {
		name        string
		targetFail  int
		compareFail int
		wantTarget  int
		wantCompare int
	}{
		{name: "target distribution first", targetFail: 1, wantTarget: 1},
		{name: "comparison distribution first", compareFail: 1, wantTarget: 1, wantCompare: 1},
		{name: "target consistency first", targetFail: 25, wantTarget: 25, wantCompare: 24},
		{name: "comparison consistency first", compareFail: 25, wantTarget: 25, wantCompare: 25},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetTransport := &w1FamilyTransport{inner: &wlEchoTransport{}, failAt: test.targetFail}
			comparisonTransport := &w1FamilyTransport{inner: &wlEchoTransport{}, failAt: test.compareFail}
			target := w1SamplingSuite("https://w1-terminal-target.example", targetTransport, "gpt-6-luna")
			comparison := w1SamplingSuite("https://w1-terminal-comparison.example", comparisonTransport, "gpt-6-luna")
			target.Comparison = &comparison
			item, terminal, err := RunSamplingStatistics(context.Background(), target, time.Second, w1Anchor(t))
			if err != nil || !terminal {
				t.Fatalf("终局 503 必须返回 skipped terminal: err=%v terminal=%v item=%+v", err, terminal, item)
			}
			if item.Status != "skipped" || item.Evidence["terminalFailure"] != true || item.Evidence["requestFailure"] != true || item.Evidence["httpStatus"] != http.StatusServiceUnavailable {
				t.Fatalf("终局证据不完整: %+v", item)
			}
			if errorText, ok := item.Evidence["error"].(string); !ok || !strings.Contains(errorText, "w1 hard failure") {
				t.Fatalf("终局必须保留上游错误信息: %+v", item.Evidence)
			}
			if targetTransport.count() != test.wantTarget || comparisonTransport.count() != test.wantCompare {
				t.Fatalf("终局后不得再发请求: target=%d/%d comparison=%d/%d", targetTransport.count(), test.wantTarget, comparisonTransport.count(), test.wantCompare)
			}
		})
	}
}

func TestW1RunSamplingStatisticsRetryDelayReturnsOriginalError(t *testing.T) {
	delayErr := errors.New("w1 sampling delay sentinel")
	tests := []struct {
		name        string
		targetFail  int
		compareFail int
		wantTarget  int
		wantCompare int
	}{
		{name: "target distribution", targetFail: 1, wantTarget: 1},
		{name: "comparison distribution", compareFail: 1, wantTarget: 1, wantCompare: 1},
		{name: "target consistency", targetFail: 25, wantTarget: 25, wantCompare: 24},
		{name: "comparison consistency", compareFail: 25, wantTarget: 25, wantCompare: 25},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetTransport := &w1FamilyTransport{inner: &wlEchoTransport{}, failAt: test.targetFail}
			comparisonTransport := &w1FamilyTransport{inner: &wlEchoTransport{}, failAt: test.compareFail}
			target := w1SamplingSuite("https://w1-delay-target.example", targetTransport, "gpt-6-luna")
			comparison := w1SamplingSuite("https://w1-delay-comparison.example", comparisonTransport, "gpt-6-luna")
			target.Comparison = &comparison
			retry := RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second, time.Second}, Delay: func(context.Context) error { return delayErr }}
			target.Retry = retry
			comparison.Retry = retry
			item, terminal, err := RunSamplingStatistics(context.Background(), target, time.Second, w1Anchor(t))
			if !errors.Is(err, delayErr) || terminal || item.Kind != "" {
				t.Fatalf("Delay 原始错误必须向上传递: item=%+v terminal=%v err=%v", item, terminal, err)
			}
			if targetTransport.count() != test.wantTarget || comparisonTransport.count() != test.wantCompare {
				t.Fatalf("Delay 失败后不得发第二次 HTTP: target=%d/%d comparison=%d/%d", targetTransport.count(), test.wantTarget, comparisonTransport.count(), test.wantCompare)
			}
		})
	}
}

func TestW1RunSamplingStatisticsComparisonModeRejectedBeforeHTTP(t *testing.T) {
	targetTransport := &w1FamilyTransport{inner: &wlEchoTransport{}}
	comparisonTransport := &w1FamilyTransport{inner: &wlEchoTransport{}}
	target := w1SamplingSuite("https://w1-mode-target.example", targetTransport, "gpt-6-luna")
	comparison := w1SamplingSuite("https://w1-mode-comparison.example", comparisonTransport, "gpt-6-luna")
	comparison.EndpointMode = modelcheckprofile.EndpointModeChatJSON
	target.Comparison = &comparison
	_, terminal, err := RunSamplingStatistics(context.Background(), target, time.Second, w1Anchor(t))
	if err == nil || !strings.Contains(err.Error(), "does not match protocol") || terminal {
		t.Fatalf("对照端点模式冲突必须 fail-closed: err=%v terminal=%v", err, terminal)
	}
	if targetTransport.count() != 0 || comparisonTransport.count() != 0 {
		t.Fatalf("模式校验失败前不得发 HTTP: target=%d comparison=%d", targetTransport.count(), comparisonTransport.count())
	}
}

func w1TrustedSuite(endpoint, model string, transport http.RoundTripper) Suite {
	return Suite{
		Endpoint:                  endpoint,
		Client:                    &http.Client{Transport: transport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     model,
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode:              modelcheckprofile.EndpointModeResponsesJSON,
		UpstreamProtocol:          modelcheckprofile.ProtocolOpenAIResponses,
		UpstreamEndpointMode:      modelcheckprofile.EndpointModeResponsesJSON,
	}
}

func TestW1RunTrustedComparisonEmptyProfileUsesDirectPairs(t *testing.T) {
	targetTransport := &wlEchoTransport{}
	comparisonTransport := &wlEchoTransport{}
	items, err := RunTrustedComparison(context.Background(),
		w1TrustedSuite("https://w1-trusted-target.example", "gpt-6-luna", targetTransport),
		w1TrustedSuite("https://w1-trusted-comparison.example", "gpt-6-luna", comparisonTransport),
		time.Second)
	if err != nil {
		t.Fatalf("空 profile 的独立对比不应失败: %v", err)
	}
	if len(targetTransport.requests) != len(distributionDefinitions)+1 || len(comparisonTransport.requests) != len(distributionDefinitions)+1 {
		t.Fatalf("空 profile 应为每侧 1 个 basic + 6 个 distribution: target=%d comparison=%d", len(targetTransport.requests), len(comparisonTransport.requests))
	}
	if !strings.Contains(targetTransport.requests[0], "OK-MODEL-CHECK") || !strings.Contains(comparisonTransport.requests[0], "CROSS-MODEL-OK") {
		t.Fatalf("首个请求必须是两侧 basic: target=%q comparison=%q", targetTransport.requests[0], comparisonTransport.requests[0])
	}
	if len(items) != 2 || items[0].Kind != "distribution_similarity" || items[1].Kind != "comparison" {
		t.Fatalf("直接对比应返回 distribution + comparison: %+v", items)
	}
}

func TestW1RunTrustedComparisonDistributionDelayReturnsOriginalError(t *testing.T) {
	delayErr := errors.New("w1 trusted delay sentinel")
	for _, failTarget := range []bool{true, false} {
		name := "target distribution"
		if !failTarget {
			name = "comparison distribution"
		}
		t.Run(name, func(t *testing.T) {
			targetTransport := &w1DistributionTransport{inner: &wlEchoTransport{}, fail: failTarget}
			comparisonTransport := &w1DistributionTransport{inner: &wlEchoTransport{}, fail: !failTarget}
			target := w1TrustedSuite("https://w1-delay-trusted-target.example", "gpt-6-luna", targetTransport)
			comparison := w1TrustedSuite("https://w1-delay-trusted-comparison.example", "gpt-6-luna", comparisonTransport)
			retry := RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second, time.Second}, Delay: func(context.Context) error { return delayErr }}
			target.Retry = retry
			comparison.Retry = retry
			_, err := RunTrustedComparison(context.Background(), target, comparison, time.Second)
			if !errors.Is(err, delayErr) {
				t.Fatalf("应返回 Delay 原始错误: %v", err)
			}
			wantComparisonCalls := 1
			if failTarget {
				wantComparisonCalls = 0
			}
			if targetTransport.distributionCalls() != 1 || comparisonTransport.distributionCalls() != wantComparisonCalls {
				t.Fatalf("延迟失败后不得继续发 distribution: target=%d comparison=%d", targetTransport.distributionCalls(), comparisonTransport.distributionCalls())
			}
		})
	}
}

func TestW1RunTrustedComparisonComparisonDistributionUnavailable(t *testing.T) {
	targetTransport := &w1DistributionTransport{inner: &wlEchoTransport{}}
	comparisonTransport := &w1DistributionTransport{inner: &wlEchoTransport{}, modelUnavail: true}
	items, err := RunTrustedComparison(context.Background(),
		w1TrustedSuite("https://w1-unavailable-target.example", "gpt-6-luna", targetTransport),
		w1TrustedSuite("https://w1-unavailable-comparison.example", "gpt-6-luna", comparisonTransport),
		time.Second)
	if err != nil {
		t.Fatalf("comparison distribution model_not_found 应构造性跳过: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("model unavailable 应返回三个 skip 项: %+v", items)
	}
	for _, item := range items {
		if item.Status != "skipped" || item.Evidence["modelUnavailable"] != true || item.Evidence["reason"] != "comparison_model_unavailable" {
			t.Fatalf("不可用对比证据=%+v", item)
		}
	}
	if targetTransport.distributionCalls() != 1 || comparisonTransport.distributionCalls() != 1 {
		t.Fatalf("首个 comparison distribution 后应停止: target=%d comparison=%d", targetTransport.distributionCalls(), comparisonTransport.distributionCalls())
	}
}

func TestW1RunTrustedComparisonComparisonModeRejectedBeforeHTTP(t *testing.T) {
	targetTransport := &w1FamilyTransport{inner: &wlEchoTransport{}}
	comparisonTransport := &w1FamilyTransport{inner: &wlEchoTransport{}}
	target := w1TrustedSuite("https://w1-mode-trusted-target.example", "gpt-6-luna", targetTransport)
	comparison := w1TrustedSuite("https://w1-mode-trusted-comparison.example", "gpt-6-luna", comparisonTransport)
	comparison.EndpointMode = modelcheckprofile.EndpointModeChatJSON
	_, err := RunTrustedComparison(context.Background(), target, comparison, time.Second)
	if err == nil || !strings.Contains(err.Error(), "does not match protocol") {
		t.Fatalf("对照模式不匹配必须 fail-closed: %v", err)
	}
	if targetTransport.count() != 0 || comparisonTransport.count() != 0 {
		t.Fatalf("模式校验失败前不得发 HTTP: target=%d comparison=%d", targetTransport.count(), comparisonTransport.count())
	}
}
