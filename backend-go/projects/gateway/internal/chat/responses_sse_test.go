package chat

// CollectOpenAIResponsesSse 终态矩阵（PLAN-20261012T121500000Z D4 响应消费
// 契约）：仅合法 response.completed 算成功；failed/incomplete/error/EOF 无
// 终态明确失败并保留已收正文；预算边界（正文上限、事件数、单事件 64 KiB、
// UTF-8 跨 chunk）与 Chat collector 同语义。全部为纯流单测。

import (
	"io"
	"strings"
	"testing"
)

// responsesSseEvent 拼一个 data-only SSE 事件块。
func responsesSseEvent(payload string) string {
	return "data: " + payload + "\n\n"
}

func TestCollectOpenAIResponsesSseTerminalMatrix(t *testing.T) {
	cases := []struct {
		name string
		stream string
		maxContentBytes int
		maxEvents int
		wantContent string
		wantErr      string
		wantInputTokens  *int64
		wantOutputTokens *int64
	}{
		{
			name: "增量 + completed 成功（含 usage）",
			stream: responsesSseEvent(`{"type":"response.output_text.delta","delta":"你好"}`) +
				responsesSseEvent(`{"type":"response.output_text.delta","delta":"世界"}`) +
				responsesSseEvent(`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":20,"output_tokens":4}}}`),
			wantContent:     "你好世界",
			wantInputTokens:  int64PtrT(20), wantOutputTokens: int64PtrT(4),
		},
		{
			name: "增量缺失 → completed.output_text 聚合兜底",
			stream: responsesSseEvent(`{"type":"response.completed","response":{"output_text":"聚合正文","usage":{"input_tokens":1,"output_tokens":2}}}`),
			wantContent:     "聚合正文",
			wantInputTokens:  int64PtrT(1), wantOutputTokens: int64PtrT(2),
		},
		{
			name: "增量缺失 → completed.output[].content 聚合兜底",
			stream: responsesSseEvent(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"块一"},{"type":"output_text","text":"块二"}]}]}}`),
			wantContent: "块一\n块二",
		},
		{
			name:    "response.failed → 失败并保留已收正文",
			stream: responsesSseEvent(`{"type":"response.output_text.delta","delta":"半截"}`) +
				responsesSseEvent(`{"type":"response.failed","response":{"error":{"message":"上游额度不足"}}}`),
			wantContent: "半截", wantErr: "上游额度不足",
		},
		{
			name:    "response.incomplete → 失败且标注不完整",
			stream: responsesSseEvent(`{"type":"response.incomplete","response":{"status_detail":"max_output_tokens"}}`),
			wantErr: "上游响应不完整（max_output_tokens）",
		},
		{
			name:    "error 事件 → 失败保留 message",
			stream: responsesSseEvent(`{"type":"error","message":"rate limited"}`),
			wantErr: "rate limited",
		},
		{
			name:    "EOF 无 completed 终态 → 失败",
			stream: responsesSseEvent(`{"type":"response.output_text.delta","delta":"未完"}`),
			wantContent: "未完", wantErr: "缺少 response.completed",
		},
		{
			name:    "非法 JSON 事件 → 失败",
			stream:  responsesSseEvent(`{not-json`),
			wantErr: "无效的 SSE JSON",
		},
		{
			name: "[DONE] 帧忽略",
			stream: responsesSseEvent(`{"type":"response.output_text.delta","delta":"完"}`) +
				responsesSseEvent(`{"type":"response.completed","response":{}}`) + "data: [DONE]\n\n",
			wantContent: "完",
		},
		{
			name: "reasoning summary 增量不计正文",
			stream: responsesSseEvent(`{"type":"response.reasoning_summary_text.delta","delta":"思考"}`) +
				responsesSseEvent(`{"type":"response.output_text.delta","delta":"答案"}`) +
				responsesSseEvent(`{"type":"response.completed","response":{}}`),
			wantContent: "答案",
		},
		{
			name:            "正文超上限 → 失败",
			stream: responsesSseEvent(`{"type":"response.output_text.delta","delta":"0123456789"}`),
			maxContentBytes: 4,
			wantErr:         "上限",
		},
		{
			name:      "事件数超上限 → 失败",
			stream: responsesSseEvent(`{"type":"response.output_text.delta","delta":"a"}`) +
				responsesSseEvent(`{"type":"response.completed","response":{}}`),
			maxEvents: 1,
			wantContent: "a",
			wantErr:   "事件数量超过",
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			maxBytes := item.maxContentBytes
			if maxBytes == 0 {
				maxBytes = 1 << 20
			}
			deltas := []string{}
			result, err := CollectOpenAIResponsesSse(strings.NewReader(item.stream), maxBytes,
				func(delta string) { deltas = append(deltas, delta) }, nil, item.maxEvents)
			if item.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), item.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, item.wantErr)
				}
				if result.Content != item.wantContent {
					t.Fatalf("failure content = %q, want %q", result.Content, item.wantContent)
				}
				return
			}
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if result.Content != item.wantContent || !result.Done || result.FinishReason != "stop" {
				t.Fatalf("result = %+v", result)
			}
			if item.wantInputTokens != nil && (result.InputTokens == nil || *result.InputTokens != *item.wantInputTokens) {
				t.Fatalf("inputTokens = %v, want %v", result.InputTokens, item.wantInputTokens)
			}
			if item.wantOutputTokens != nil && (result.OutputTokens == nil || *result.OutputTokens != *item.wantOutputTokens) {
				t.Fatalf("outputTokens = %v, want %v", result.OutputTokens, item.wantOutputTokens)
			}
		})
	}
}

func TestCollectOpenAIResponsesSseCallbacksAndBounds(t *testing.T) {
	t.Run("onDelta 与 onReasoningDelta 分流", func(t *testing.T) {
		stream := responsesSseEvent(`{"type":"response.reasoning_summary_text.delta","delta":"想"}`) +
			responsesSseEvent(`{"type":"response.output_text.delta","delta":"答"}`) +
			responsesSseEvent(`{"type":"response.completed","response":{}}`)
		answers, reasonings := []string{}, []string{}
		result, err := CollectOpenAIResponsesSse(strings.NewReader(stream), 1<<20,
			func(delta string) { answers = append(answers, delta) },
			func(delta string) { reasonings = append(reasonings, delta) }, 0)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if strings.Join(answers, "") != "答" || strings.Join(reasonings, "") != "想" {
			t.Fatalf("answers=%v reasonings=%v", answers, reasonings)
		}
		if result.Content != "答" {
			t.Fatalf("content = %q", result.Content)
		}
	})

	t.Run("单事件超 64KiB → 失败", func(t *testing.T) {
		huge := strings.Repeat("x", sseMaxEventBytes+1)
		stream := responsesSseEvent(`{"type":"response.output_text.delta","delta":"` + huge + `"}`)
		_, err := CollectOpenAIResponsesSse(strings.NewReader(stream), 1<<30, nil, nil, 0)
		if err == nil || !strings.Contains(err.Error(), "64 KiB") {
			t.Fatalf("err = %v, want 64 KiB 上限", err)
		}
	})

	t.Run("UTF-8 多字节字符跨 chunk 不断裂", func(t *testing.T) {
		// 「验收直通」每个汉字 3 字节；OneByteReader 逐字节喂入，保证 read
		// 边界落在多字节字符内部（与真实 TCP 分片同构）。
		stream := responsesSseEvent(`{"type":"response.output_text.delta","delta":"验收直通"}`) +
			responsesSseEvent(`{"type":"response.completed","response":{"output_text":""}}`)
		result, err := CollectOpenAIResponsesSse(iotestOneByte(strings.NewReader(stream)), 1<<20, nil, nil, 0)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if result.Content != "验收直通" {
			t.Fatalf("content = %q, want 验收直通", result.Content)
		}
	})
}

// iotestOneByte 返回逐字节读取器（testing/iotest.OneByteReader 同构，避免
// 引入额外 import 别名）。
func iotestOneByte(reader io.Reader) io.Reader {
	return &oneByteReader{source: reader}
}

type oneByteReader struct{ source io.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.source.Read(p[:1])
}

func TestAggregateResponsesSseTextMatrix(t *testing.T) {
	t.Run("completed 聚合成功", func(t *testing.T) {
		stream := responsesSseEvent(`{"type":"response.output_text.delta","delta":"辅助"}`) +
			responsesSseEvent(`{"type":"response.completed","response":{}}`)
		text, err := AggregateResponsesSseText(strings.NewReader(stream), 1<<17)
		if err != nil || text != "辅助" {
			t.Fatalf("text=%q err=%v", text, err)
		}
	})
	t.Run("failed 保留原始错误", func(t *testing.T) {
		stream := responsesSseEvent(`{"type":"response.failed","response":{"error":{"message":"boom"}}}`)
		_, err := AggregateResponsesSseText(strings.NewReader(stream), 1<<17)
		if err == nil || err.Error() != "boom" {
			t.Fatalf("err = %v, want boom", err)
		}
	})
	t.Run("EOF 无终态报错", func(t *testing.T) {
		_, err := AggregateResponsesSseText(strings.NewReader(responsesSseEvent(`{"type":"response.output_text.delta","delta":"x"}`)), 1<<17)
		if err == nil || !strings.Contains(err.Error(), "response.completed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("正文超预算报错", func(t *testing.T) {
		stream := responsesSseEvent(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("y", 64) + `"}`)
		_, err := AggregateResponsesSseText(strings.NewReader(stream), 16)
		if err == nil || !strings.Contains(err.Error(), "payload_too_large") {
			t.Fatalf("err = %v", err)
		}
	})
}
