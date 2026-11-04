package gatewayresponse

import (
	"strings"
	"testing"
)

// BUG-0353：真实生产形态的端到端回归——普通 response.created 事件后跟一条
// 超过 256KB 单行上限的 response.completed，管道必须按成功收尾，不得走
// finalizeMissingTerminal 的 stream_failed 分支。
func TestPipeUpstreamStreamOversizedResponsesTerminalAfterNormalEvent(t *testing.T) {
	recorder := &failureRecorder{}
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	pad := strings.Repeat("x", 300*1024)
	completed := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"type\":\"output_text\",\"text\":\"" + pad + "\"}]}]}}\n\n"

	result, err := runPipe(NewSliceUpstreamBody([]byte(created), []byte(completed)), nil, StreamPipeOptions{}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("超大 response.completed 应成功收尾: %+v", result)
	}
	if !result.ProtocolValidated {
		t.Fatalf("完整终止事件应通过协议校验: %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("不应记录流失败: %+v", recorder.values)
	}
	if result.DownstreamBytesWritten == 0 || !result.TransportCommitted || !result.SemanticCommitted {
		t.Fatalf("下游提交状态异常: %+v", result)
	}
}
