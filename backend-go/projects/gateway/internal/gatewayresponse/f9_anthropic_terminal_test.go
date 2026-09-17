package gatewayresponse

// F9 回归：anthropic 流式完整回复被误判「缺少协议终止事件」→ 503
// upstream_stream_interrupted。
//
// 根因：cmd 编排层构造 HandleUpstreamResponseInput 时未显式装配 Driver，
// input.driver() 无条件回退 NewOpenAIResponseDriver()，导致 anthropic 上游
// 的流管道使用 openai StreamInspector。openai 终止判定（ClassifyStreamEvent）
// 只认 [DONE]/response.completed 等，不认 anthropic 的 message_stop：
// EventTypeCounts 计入 message_stop:1 但 TerminalReceived 恒为 false，
// EOF 后 finalizeAfterLoop 走 finalizeMissingTerminal。
//
// 修复：input.driver() 回退按账户 protocolCode 解析协议驱动视图。

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// f9AnthropicChunk 构造单个 anthropic SSE 事件（与上游真实分帧一致）。
func f9AnthropicChunk(eventName string, data string) []byte {
	return []byte("event: " + eventName + "\ndata: " + data + "\n\n")
}

// f9AnthropicFullChunks 是 anthropic /v1/messages 完整流式回复的事件序列。
var f9AnthropicFullChunks = [][]byte{
	f9AnthropicChunk("message_start", `{"type":"message_start","message":{"id":"msg_f9","type":"message","role":"assistant","model":"claude-sonnet-4-5"}}`),
	f9AnthropicChunk("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
	f9AnthropicChunk("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`),
	f9AnthropicChunk("content_block_stop", `{"type":"content_block_stop","index":0}`),
	f9AnthropicChunk("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":12}}`),
	f9AnthropicChunk("message_stop", `{"type":"message_stop"}`),
}

// f9AnthropicChunksWithoutStop 去掉终止事件，用于锁定「缺 message_stop 仍报
// missing terminal 不放宽」。
func f9AnthropicChunksWithoutStop() [][]byte {
	return f9AnthropicFullChunks[:len(f9AnthropicFullChunks)-1]
}

// f9AnthropicPipeOptions 对齐 anthropic 精确客户端的生产管道选项
//（finalize.go HandleStreamUpstreamResponse 的固定接线）。
func f9AnthropicPipeOptions(driver StreamDriver) StreamPipeOptions {
	return StreamPipeOptions{
		InterpretProtocolFailures:             true,
		InterpretProtocolFailuresSet:          true,
		RetryBeforeDownstreamWriteUntilOutput: true,
		Driver:                                driver,
	}
}

// TestF9AnthropicStreamTerminalization 表驱动：anthropic 完整事件序列必须
// 正常终结；缺失 message_stop 仍按 missing terminal 收尾，不放宽。
func TestF9AnthropicStreamTerminalization(t *testing.T) {
	cases := []struct {
		name            string
		chunks          [][]byte
		driver          StreamDriver
		intercepted     bool
		wantCompleted   bool
		wantFailureCode string
	}{
		{
			name:          "anthropic驱动完整事件序列经生产拦截器形态正常终结",
			chunks:        f9AnthropicFullChunks,
			driver:        anthropicStreamDriver{},
			intercepted:   true,
			wantCompleted: true,
		},
		{
			name:          "anthropic驱动完整事件序列无拦截器直通形态正常终结",
			chunks:        f9AnthropicFullChunks,
			driver:        anthropicStreamDriver{},
			wantCompleted: true,
		},
		{
			name:            "anthropic驱动缺失message_stop仍报缺少协议终止事件",
			chunks:          f9AnthropicChunksWithoutStop(),
			driver:          anthropicStreamDriver{},
			intercepted:     true,
			wantCompleted:   false,
			wantFailureCode: "upstream_stream_interrupted",
		},
		{
			// F9 现象对照：错误回退 openai inspector 时同一完整序列被误判
			// missing terminal。修复后编排层不再落到该形态，但管道行为本身
			// 保持原样，不放宽 openai 协议的终止判定。
			name:            "openai回退驱动对anthropic序列误判缺少终止事件对照",
			chunks:          f9AnthropicFullChunks,
			driver:          nil,
			intercepted:     true,
			wantCompleted:   false,
			wantFailureCode: "upstream_stream_interrupted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &failureRecorder{}
			options := f9AnthropicPipeOptions(tc.driver)
			if tc.intercepted {
				options.Interceptor = &w11bPassInterceptor{}
			}
			result, err := runPipe(NewSliceUpstreamBody(tc.chunks...), nil, options, recorder, 1000)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if result.Completed != tc.wantCompleted {
				t.Fatalf("Completed = %v, 期望 %v，result.Message = %q，errorCode = %q",
					result.Completed, tc.wantCompleted, result.Message, result.ErrorCode)
			}
			if tc.wantFailureCode == "" {
				if recorder.count() != 0 {
					t.Fatalf("完整流不应记录流式失败，records = %+v", recorder.values)
				}
				return
			}
			if recorder.count() == 0 {
				t.Fatalf("缺失终止事件的流应记录流式失败")
			}
			if last := recorder.last(); last.errorCode != tc.wantFailureCode {
				t.Fatalf("errorCode = %q, 期望 %q", last.errorCode, tc.wantFailureCode)
			}
		})
	}
}

// TestF9DriverFallbackFollowsAccountProtocol 锁定 input.driver() 回退：
// 编排层未显式装配 Driver 时按账户 protocolCode 解析响应驱动视图，
// 而不是无条件回退 openai。
func TestF9DriverFallbackFollowsAccountProtocol(t *testing.T) {
	accountWith := func(protocolCode string) AccountView {
		return OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ProtocolCode: protocolCode}}
	}
	cases := []struct {
		name           string
		account        AccountView
		wantProtocol   string
		wantErrorProto string
		wantProfile    string
	}{
		{
			name:           "anthropic账户回退anthropic响应驱动",
			account:        accountWith("anthropic"),
			wantProtocol:   "anthropic_v1",
			wantErrorProto: "anthropic",
			wantProfile:    "generic_anthropic",
		},
		{
			name:           "gemini账户回退gemini响应驱动",
			account:        accountWith("gemini"),
			wantProtocol:   "gemini_v1beta",
			wantErrorProto: "gemini",
			wantProfile:    "generic_gemini",
		},
		{
			name:           "openai账户回退openai响应驱动",
			account:        accountWith("openai"),
			wantProtocol:   "openai_v1",
			wantErrorProto: "openai",
			wantProfile:    "generic",
		},
		{
			name:           "未知协议码回退openai响应驱动",
			account:        accountWith("future_protocol"),
			wantProtocol:   "openai_v1",
			wantErrorProto: "openai",
			wantProfile:    "generic",
		},
		{
			name:           "账户缺失时回退openai响应驱动",
			account:        nil,
			wantProtocol:   "openai_v1",
			wantErrorProto: "openai",
			wantProfile:    "generic",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &HandleUpstreamResponseInput{Account: tc.account}
			driver := input.driver()
			if got := driver.ResponseProtocol(); got != tc.wantProtocol {
				t.Fatalf("ResponseProtocol = %q, 期望 %q", got, tc.wantProtocol)
			}
			if got := driver.ClientErrorProtocol(); got != tc.wantErrorProto {
				t.Fatalf("ClientErrorProtocol = %q, 期望 %q", got, tc.wantErrorProto)
			}
			if got := driver.DefaultClientProfile(); got != tc.wantProfile {
				t.Fatalf("DefaultClientProfile = %q, 期望 %q", got, tc.wantProfile)
			}
			inspector := driver.NewStreamInspector()
			if inspector == nil {
				t.Fatalf("协议驱动应提供 StreamInspector")
			}
		})
	}
}

// TestF9AnthropicInspectorTerminalMapping 在 gatewayresponse 侧锁定
// anthropic inspector 的事件映射语义：message_stop 置位 TerminalReceived，
// 计数包含完整事件序列。
func TestF9AnthropicInspectorTerminalMapping(t *testing.T) {
	inspector := NewAnthropicResponseDriver().NewStreamInspector()
	for _, chunk := range f9AnthropicFullChunks {
		inspector.PushChunk(chunk)
	}
	final := inspector.Finish()
	if !final.TerminalReceived {
		t.Fatalf("anthropic 完整事件序列应置位 TerminalReceived，LastEventType = %q", final.LastEventType)
	}
	if final.EventTypeCounts["message_stop"] != 1 {
		t.Fatalf("message_stop 计数 = %d, 期望 1", final.EventTypeCounts["message_stop"])
	}
	if final.FailedReceived {
		t.Fatalf("完整成功序列不应置位 FailedReceived")
	}
	if !strings.Contains(final.LastEventType, "message_stop") {
		t.Fatalf("LastEventType = %q, 期望 message_stop", final.LastEventType)
	}
}
