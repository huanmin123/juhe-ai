package gatewayresponse

// gatewayresponse 流管道场景补充（wd_ 前缀，独占新增）：语义提交后的协议失败
// 处理中，提交后失败信号回调自身报错时管道仍继续发送协议终态（Node
// catch(() => undefined) 语义）。分片序列固定，无真实网络与时钟依赖。

import (
	"errors"
	"testing"
)

const wdResponsesDelta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"你好\"}\n\n"

const wdResponsesFailed = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"provider_error\",\"message\":\"供应商失败\"}}}\n\n"

// TestWdCommittedFailureSignalCallbackErrorStillSignals：语义提交后解析到协议
// 失败时，BeforeCommittedFailureSignal 回调错误只记日志、不改变失败处置
// （失败仍进入 HandleStreamFailure，管道不向上层抛错）。
func TestWdCommittedFailureSignalCallbackErrorStillSignals(t *testing.T) {
	recorder := &failureRecorder{}
	commit := &DownstreamCommitState{}
	options := StreamPipeOptions{
		DownstreamProtocol:    "responses_sse",
		DownstreamCommitState: commit,
		BeforeCommittedFailureSignal: func(CommittedStreamFailureSignalContext) error {
			return errors.New("记录失败")
		},
		NowMs: func() int64 { return 1000 },
	}
	body := NewSliceUpstreamBody([]byte(wdResponsesDelta), []byte(wdResponsesFailed))
	result, err := runPipe(body, nil, options, recorder, 1000)
	if err != nil {
		t.Fatalf("回调错误不应中断管道: %v", err)
	}
	if recorder.count() != 1 {
		t.Fatalf("提交后失败仍应记录一次失败: %d %v", recorder.count(), result)
	}
	if last := recorder.last(); last.errorCode != "provider_error" || last.message != "供应商失败" {
		t.Fatalf("失败记录错误: %+v", last)
	}
}
