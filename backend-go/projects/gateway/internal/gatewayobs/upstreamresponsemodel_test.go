package gatewayobs

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// 协议识别（对齐 upstreamResponseModelProtocolForRequest 与官方回归）。
func TestUpstreamResponseModelProtocolForRequest(t *testing.T) {
	cases := []struct {
		name         string
		headers      http.Header
		upstreamURL  string
		providerCode string
		protocolCode string
		want         UpstreamResponseModelProtocol
	}{
		{
			"anthropic-header-wins",
			http.Header{"Anthropic-Version": {"2023-06-01"}},
			"https://example.test/v1/messages",
			"hybrid", "",
			UpstreamResponseModelProtocolAnthropic,
		},
		{
			"goog-api-key-header",
			http.Header{"X-Goog-Api-Key": {"test"}},
			"https://example.test/v1/models/gemini-2.5-pro:generateContent",
			"hybrid", "",
			UpstreamResponseModelProtocolGemini,
		},
		{
			"protocol-code-openai-beats-google",
			http.Header{"X-Goog-User-Project": {"quota-project"}},
			"https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
			"gemini", "openai-v1",
			UpstreamResponseModelProtocolOpenAI,
		},
		{
			"google-openai-compatible-path",
			http.Header{"X-Goog-User-Project": {"quota-project"}},
			"https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
			"gemini", "",
			UpstreamResponseModelProtocolOpenAI,
		},
		{
			"gemini-native-protocol",
			http.Header{},
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent",
			"gemini", "gemini-v1beta",
			UpstreamResponseModelProtocolGemini,
		},
		{
			"gemini-cloudcode-host",
			http.Header{},
			"https://cloudcode-pa.googleapis.com/v1internal:generateContent",
			"gemini", "",
			UpstreamResponseModelProtocolGemini,
		},
		{
			"non-google-openai-path-ignored",
			http.Header{},
			"https://api.openai.com/v1/openai/chat/completions",
			"", "",
			UpstreamResponseModelProtocolOpenAI,
		},
		{
			"provider-code-anthropic",
			http.Header{},
			"https://example.test/v1/messages",
			"Anthropic", "",
			UpstreamResponseModelProtocolAnthropic,
		},
		{
			"protocol-code-gemini-contains",
			http.Header{},
			"https://example.test/anything",
			"", "GEMINI_NATIVE ",
			UpstreamResponseModelProtocolGemini,
		},
		{
			"default-openai",
			http.Header{},
			"https://example.test/v1/chat/completions",
			"gpt", "",
			UpstreamResponseModelProtocolOpenAI,
		},
		{
			"invalid-url-falls-through",
			http.Header{},
			"://bad url",
			"", "",
			UpstreamResponseModelProtocolOpenAI,
		},
		{
			"google-host-openai-subpath",
			http.Header{},
			"https://aiplatform.googleapis.com/v1/projects/x/openai/chat/completions",
			"", "",
			UpstreamResponseModelProtocolOpenAI,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := UpstreamResponseModelProtocolForRequest(UpstreamResponseModelRequestInfo{
				Headers:      testCase.headers,
				UpstreamURL:  testCase.upstreamURL,
				ProviderCode: testCase.providerCode,
				ProtocolCode: testCase.protocolCode,
			})
			if got != testCase.want {
				t.Fatalf("protocol = %q, want %q", got, testCase.want)
			}
		})
	}
}

// SSE / JSON 观察闭环。
func TestUpstreamResponseModelOpenAISseTerminalOverrideAndConflict(t *testing.T) {
	// 与 backend/src/scripts/regression/upstream-response-model-regression.ts 同案。
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI, SSE: true})
	chunks := []string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-5.6-sol\"}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.4-mini-2026-03-17\"}}\n\n",
	}
	forwarded := observeChunksInHalves(t, observation, chunks)
	if forwarded != strings.Join(chunks, "") {
		t.Fatalf("观察器不得改写 OpenAI SSE 原始响应: %q", forwarded)
	}
	if observation.Model() != "gpt-5.4-mini-2026-03-17" {
		t.Fatalf("OpenAI 终态模型应覆盖先前模型: %q", observation.Model())
	}
	if !observation.Conflict() {
		t.Fatal("同一 OpenAI 流内模型变化应记录冲突")
	}
}

func TestUpstreamResponseModelAnthropicAndGemini(t *testing.T) {
	anthropic := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolAnthropic, SSE: true})
	observeChunksInHalves(t, anthropic, []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-20250514\"}}\n\n",
	})
	if anthropic.Model() != "claude-sonnet-4-20250514" {
		t.Fatalf("Anthropic 应读取 message.model: %q", anthropic.Model())
	}

	gemini := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolGemini, SSE: false})
	observeChunksInHalves(t, gemini, []string{"{\"candidates\":[],\"modelVersion\":\"gemini-2.5-pro\"}"})
	if gemini.Model() != "gemini-2.5-pro" {
		t.Fatalf("Gemini 应读取 modelVersion: %q", gemini.Model())
	}

	geminiSse := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolGemini, SSE: true})
	observeChunksInHalves(t, geminiSse, []string{
		"data: {\"modelVersion\":\"gemini-2.5-flash\"}\n\n",
		"data: {\"response\":{\"modelVersion\":\"gemini-2.5-pro\"}}\n\n",
	})
	if geminiSse.Model() != "gemini-2.5-pro" {
		t.Fatalf("Gemini 每个 payload 都是终态，应取最后: %q", geminiSse.Model())
	}
	if !geminiSse.Conflict() {
		t.Fatal("Gemini 模型变化应记录冲突")
	}
}

func TestUpstreamResponseModelTerminalEventNames(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		eventLine string
		terminal  bool
	}{
		{"response.completed", "{\"type\":\"response.completed\",\"response\":{\"model\":\"m-1\"}}", "", true},
		{"response.done", "{\"type\":\"response.done\",\"response\":{\"model\":\"m-1\"}}", "", true},
		{"response.failed", "{\"type\":\"response.failed\",\"response\":{\"model\":\"m-1\"}}", "", true},
		{"response.incomplete", "{\"type\":\"response.incomplete\",\"response\":{\"model\":\"m-1\"}}", "", true},
		{"response.cancelled", "{\"type\":\"response.cancelled\",\"response\":{\"model\":\"m-1\"}}", "", true},
		{"response.canceled", "{\"type\":\"response.canceled\",\"response\":{\"model\":\"m-1\"}}", "", true},
		{"event-name-carries", "{\"response\":{\"model\":\"m-1\"}}", "event: response.completed\n", true},
		{"event-name-blank-falls-to-type", "{\"type\":\"response.done\",\"response\":{\"model\":\"m-1\"}}", "event:    \n", true},
		{"non-terminal", "{\"type\":\"response.created\",\"response\":{\"model\":\"m-1\"}}", "", false},
		{"non-terminal-event", "{\"response\":{\"model\":\"m-1\"}}", "event: response.output_text.delta\n", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI, SSE: true})
			observeChunksInHalves(t, observation, []string{testCase.eventLine + "data: " + testCase.payload + "\n\n"})
			if observation.Model() != "m-1" {
				t.Fatalf("model = %q, want m-1", observation.Model())
			}
			// 终态设置 terminalModel；非终态留在 firstModel。
			observeChunksInHalves(t, observation, []string{"data: " + testCase.payload + "\n\n"})
			if testCase.terminal && observation.Model() != "m-1" {
				t.Fatalf("terminal 后 model 必须保持: %q", observation.Model())
			}
		})
	}
}

func TestUpstreamResponseModelIgnoresJunkAndOversize(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI, SSE: true})
	observation.Observe([]byte("data: not-json\n\n"))
	observation.Observe([]byte("data: [1,2,3]\n\n"))
	observation.Observe([]byte("data: {\"response\":{\"model\":\"   \"}}\n\n"))
	observation.Observe([]byte(": comment only\n\n"))
	if observation.Model() != "" {
		t.Fatalf("非 JSON/非对象/空模型不得产生观察: %q", observation.Model())
	}
	if observation.Conflict() {
		t.Fatal("垃圾输入不得记录冲突")
	}

	// 模型名超长（>200 码点）被忽略。
	longModel := "{\"response\":{\"model\":\"" + strings.Repeat("长", 201) + "\"}}"
	observation.Observe([]byte("data: " + longModel + "\n\n"))
	if observation.Model() != "" {
		t.Fatalf("超长模型名必须被忽略: %q", observation.Model())
	}
	exactModel := "{\"response\":{\"model\":\"" + strings.Repeat("长", 200) + "\"}}"
	observation.Observe([]byte("data: " + exactModel + "\n\n"))
	if utf16 := len([]rune(observation.Model())); utf16 != 200 {
		t.Fatalf("200 码点模型必须保留: %d", utf16)
	}
}

func TestUpstreamResponseModelSseOversizeEvent(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI, SSE: true})
	// 单行超过 maxSseEventBytes：pendingLine 清空、事件标记 oversized。
	oversized := "data: " + strings.Repeat("x", maxSseEventBytes+1) + "\n\n"
	observation.Observe([]byte(oversized))
	observation.Observe([]byte("data: {\"response\":{\"model\":\"after-oversize\"}}\n\n"))
	observation.Finish()
	if observation.Model() != "after-oversize" {
		t.Fatalf("oversized 后新事件必须恢复观察: %q", observation.Model())
	}
}

func TestUpstreamResponseModelJsonBodyCap(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolGemini, SSE: false})
	observation.Observe(bytes.Repeat([]byte("a"), maxJsonResponseBytes))
	observation.Observe([]byte("{\"modelVersion\":\"gemini-2.5-pro\"}"))
	observation.Finish()
	if observation.Model() != "" {
		t.Fatalf("超过 1MB 的 JSON 正文不得解析: %q", observation.Model())
	}
}

func TestUpstreamResponseModelUtf8SplitAcrossChunks(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI, SSE: true})
	text := "data: {\"response\":{\"model\":\"gpt-5.6-中文模型\"}}\n\n"
	raw := []byte(text)
	// 按字节一切两半，验证 StringDecoder 语义。
	midpoint := strings.Index(text, "中")
	observation.Observe(raw[:midpoint])
	observation.Observe(raw[midpoint:])
	observation.Finish()
	if observation.Model() != "gpt-5.6-中文模型" {
		t.Fatalf("跨 chunk 的多字节 UTF-8 必须正确解码: %q", observation.Model())
	}
}

func TestUpstreamResponseModelFinishFlushesPendingLine(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolGemini, SSE: true})
	observation.Observe([]byte("data: {\"modelVersion\":\"gemini-2.5-pro\"}")) // 无结尾换行
	observation.Finish()
	if observation.Model() != "gemini-2.5-pro" {
		t.Fatalf("Finish 必须冲刷悬挂行: %q", observation.Model())
	}
	// Finish 幂等。
	observation.Finish()
}

func TestUpstreamResponseModelBodyReaderPassthrough(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI, SSE: true})
	payload := "data: {\"model\":\"gpt-x\"}\n\n"
	reader := ObserveUpstreamResponseModelBody(strings.NewReader(payload), observation)
	var forwarded bytes.Buffer
	if _, err := forwarded.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if forwarded.String() != payload {
		t.Fatalf("透传被改写: %q", forwarded.String())
	}
	if observation.Model() != "gpt-x" {
		t.Fatalf("EOF 必须触发 Finish: %q", observation.Model())
	}
}

func observeChunksInHalves(t *testing.T, observation *UpstreamResponseModelObservation, chunks []string) string {
	t.Helper()
	var forwarded bytes.Buffer
	for _, chunk := range chunks {
		raw := []byte(chunk)
		midpoint := len(raw) / 2
		observation.Observe(raw[:midpoint])
		forwarded.Write(raw[:midpoint])
		observation.Observe(raw[midpoint:])
		forwarded.Write(raw[midpoint:])
	}
	observation.Finish()
	return forwarded.String()
}
