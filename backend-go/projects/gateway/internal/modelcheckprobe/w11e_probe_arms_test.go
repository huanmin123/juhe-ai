package modelcheckprobe

// w11e modelcheckprobe 错误臂：核心探测终止短路、协议范围跳过、纯函数
// 分支（错误消息提取、UUID、随机 Juice/token nonce、分布相似度、重试证据）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// w11eStaticTransport 返回固定状态码与响应体。
type w11eStaticTransport struct {
	status int
	body   string
}

func (t *w11eStaticTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: http.NoBody}, nil
}

func TestW11ERunSuiteTerminalShortCircuits(t *testing.T) {
	// 基础探测非 200 → 核心族终止并保留失败证据。
	terminal := &w11eStaticTransport{status: http.StatusBadGateway, body: `{"error":{"message":"upstream unavailable"}}`}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint: "https://w11e.invalid", Client: &http.Client{Transport: terminal},
		Model: "gpt-5.6-sol", Profile: "quick", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer: deterministicTokenizer{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || len(items) > 3 {
		t.Fatalf("终止短路条目数=%d items=%+v", len(items), items)
	}
	var usage Evaluation
	for _, item := range items {
		if item.Kind == "usage_shape" {
			usage = item
		}
	}
	if usage.Kind == "" {
		t.Fatalf("终止短路必须包含 usage 证据: %+v", items)
	}
	// 非 Responses 协议：token/identity 探测按协议范围跳过。
	skip := &w11eStaticTransport{status: http.StatusOK, body: `{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`}
	items, err = RunSuite(context.Background(), Suite{
		Endpoint: "https://w11e.invalid", Client: &http.Client{Transport: skip},
		Model: "deepseek-v4-flash", Profile: "quick", Protocol: modelcheckprofile.ProtocolOpenAIChat,
		Tokenizer: deterministicTokenizer{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var tokenSkip Evaluation
	for _, item := range items {
		if item.Kind == "token_integrity" {
			tokenSkip = item
		}
	}
	if tokenSkip.Kind == "" || tokenSkip.Status != "skipped" {
		t.Fatalf("非 Responses 协议必须跳过 token 探测: %+v", items)
	}
}

func TestW11EErrorValueMessageAndUUIDArms(t *testing.T) {
	if got := errorValueMessage("direct"); got != "direct" {
		t.Fatalf("直连消息=%q", got)
	}
	if got := errorValueMessage(map[string]any{"message": "nested"}); got != "nested" {
		t.Fatalf("嵌套消息=%q", got)
	}
	if got := errorValueMessage(map[string]any{"code": "E1"}); got != "E1" {
		t.Fatalf("code 回退=%q", got)
	}
	if got := errorValueMessage(map[string]any{"type": "T"}); got != "T" {
		t.Fatalf("type 回退=%q", got)
	}
	if got := errorValueMessage(map[string]any{"status": "503"}); got != "503" {
		t.Fatalf("status 回退=%q", got)
	}
	if got := errorValueMessage(42); got != "" {
		t.Fatalf("未知形态=%q", got)
	}
	if got := errorValueMessage(map[string]any{}); got != "" {
		t.Fatalf("空 map=%q", got)
	}
	if !isFailureEvent("response.failed") || !isFailureEvent("response.incomplete") || !isFailureEvent("error") || isFailureEvent("response.completed") {
		t.Fatal("失败事件判定错误")
	}
	headers := http.Header{}
	headers.Set("x-request-id", "existing")
	var err error
	if got := mustHeaderUUID(headers, "x-request-id", &err); got != "existing" || err != nil {
		t.Fatalf("既有 header=%q err=%v", got, err)
	}
	generated := mustHeaderUUID(http.Header{}, "session_id", &err)
	if err != nil || !strings.Contains(generated, "-") {
		t.Fatalf("生成 UUID=%q err=%v", generated, err)
	}
	if _, uuidErr := codexUUID(); uuidErr != nil {
		t.Fatalf("codexUUID=%v", uuidErr)
	}
}

func TestW11EJuiceAndTokenRandomArms(t *testing.T) {
	nonce, err := randomJuiceNonce()
	if err != nil || len(nonce) < 4 {
		t.Fatalf("juice nonce=%q err=%v", nonce, err)
	}
	coverage, err := randomJuiceCoverage()
	if err != nil || coverage == "" || strings.HasPrefix(coverage, "8") || strings.HasPrefix(coverage, "16") || strings.HasPrefix(coverage, "40") {
		t.Fatalf("juice coverage=%q err=%v", coverage, err)
	}
	tokenNonce, err := randomTokenNonce()
	if err != nil || tokenNonce == "" {
		t.Fatalf("token nonce=%q err=%v", tokenNonce, err)
	}
}

func TestW11EDistributionHelperArms(t *testing.T) {
	cases := []struct {
		usage map[string]any
		want  float64
	}{
		{map[string]any{"total_tokens": 12.0}, 12},
		{map[string]any{"totalTokens": 14.0}, 14},
		{map[string]any{"totalTokenCount": 16.0}, 16},
		{map[string]any{"input_tokens": 3.0, "output_tokens": 4.0}, 7},
		{map[string]any{"prompt_tokens": 5.0, "completion_tokens": 6.0}, 11},
		{map[string]any{"promptTokenCount": 7.0, "candidatesTokenCount": 8.0}, 15},
	}
	for _, tc := range cases {
		got, ok := distributionTotalTokens(tc.usage)
		if !ok || got != tc.want {
			t.Fatalf("usage=%v got=%v want=%v ok=%t", tc.usage, got, tc.want, ok)
		}
	}
	if _, ok := distributionTotalTokens(map[string]any{}); ok {
		t.Fatal("空 usage 不得产出 token 数")
	}
	if similarity := distributionTextSimilarity("", "text"); similarity != 0 {
		t.Fatalf("空文本相似度=%v", similarity)
	}
	if similarity := distributionTextSimilarity("same text", "same text"); similarity != 1 {
		t.Fatalf("相同文本相似度=%v", similarity)
	}
	if similarity := distributionTextSimilarity("alpha beta", "gamma delta"); similarity <= 0 || similarity >= 1 {
		t.Fatalf("弱交集相似度=%v", similarity)
	}
	if similarity := distributionTextSimilarity("alpha beta", "beta gamma"); similarity <= 0 || similarity >= 1 {
		t.Fatalf("部分交集相似度=%v", similarity)
	}
}

func TestW11ERetryAndEvidenceArms(t *testing.T) {
	item := withRetryEvidence(Evaluation{Kind: "behavior_probe", Status: "failed", Evidence: map[string]any{}}, Result{HTTPStatus: 500})
	if _, exists := item.Evidence["retryAttemptCount"]; exists {
		t.Fatal("无重试不得附加证据")
	}
	withRetries := withRetryEvidence(Evaluation{Kind: "behavior_probe", Status: "failed", Evidence: map[string]any{}}, Result{
		HTTPStatus: 500, RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{500, 502, 503},
		RetryWaitDurations: []time.Duration{time.Second, 2 * time.Second}, AttemptDetails: []AttemptDetail{{HTTPStatus: 500}},
	})
	if withRetries.Evidence["retryAttemptCount"] != 2 || withRetries.Evidence["retryMaxAttempts"] != 3 {
		t.Fatalf("重试证据=%+v", withRetries.Evidence)
	}
	waits, ok := withRetries.Evidence["retryWaitMilliseconds"].([]int64)
	if !ok || len(waits) != 2 || waits[0] != 1000 {
		t.Fatalf("等待毫秒=%v", withRetries.Evidence["retryWaitMilliseconds"])
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := defaultRetryDelay(canceled, 1); err == nil {
		t.Fatal("canceled 重试延迟必须返回错误")
	}
	skip := protocolScopedSkip("token_integrity")
	if skip.Kind != "token_integrity" || skip.Status != "skipped" {
		t.Fatalf("协议跳过形态=%+v", skip)
	}
}

func TestW11ETokenAndLongContextHelperArms(t *testing.T) {
	// buildTokenPrompt 目标 token 生成与上界。
	prompt, tokens, err := buildTokenPrompt(deterministicTokenizer{}, "w11e-prefix", 64)
	if err != nil || tokens <= 0 || !strings.Contains(prompt, "w11e-prefix") {
		t.Fatalf("prompt=%q tokens=%d err=%v", prompt, tokens, err)
	}
	// 桶对齐检测：不足 4 个非基准样本不触发。
	if detectsBucketRounding(nil) {
		t.Fatal("空样本不得检测桶对齐")
	}
	// 长上下文提示词构建。
	definition := LongContextDefinition{Key: "w11e-def", Marker: "W11E_MARKER", TargetInputTokens: 8, MaxOutputTokens: 16}
	longPrompt, err := buildLongContextPrompt(deterministicTokenizer{}, definition)
	if err != nil || !strings.Contains(longPrompt, "w11e-def") {
		t.Fatalf("长上下文 prompt=%q err=%v", longPrompt, err)
	}
	tokenizer, err := NewO200kTokenizer()
	if err != nil {
		t.Fatalf("o200k tokenizer=%v", err)
	}
	if tokenizer.Version() == "" {
		t.Fatal("tokenizer 版本不可为空")
	}
}
