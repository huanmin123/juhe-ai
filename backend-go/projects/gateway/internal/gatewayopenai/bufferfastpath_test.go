package gatewayopenai

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// D-100 fast-path 放开验证：inspectVisibleOutputTextEvents 由策略描述符驱动
//（对齐 Node policies.some(policyRequiresVisibleOutputTextInspection)），
// 不再一律以「存在策略」保守全量检查。

func countingPolicy(calls *int) InspectionPolicy {
	return InspectionPolicy(func(event ParsedStreamEvent, frames []gatewayproto.SemanticFrame) *InspectionDecision {
		*calls++
		return nil
	})
}

// 声明策略不检查可见输出文本：纯文本 delta 事件走快路径直通，不进策略；
// 非可见输出事件（根节点含 error）仍进策略。
func TestBufferFastPathSkipsVisibleOutputWhenDeclaredUnneeded(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies:                            []InspectionPolicy{countingPolicy(&calls)},
		RequiresVisibleOutputTextInspection: []bool{false},
	})
	visibleChunk := []byte("data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	result := buffer.PushChunk(visibleChunk)
	if calls != 0 {
		t.Fatalf("visible-output-only event must take fast path, policy calls = %d", calls)
	}
	if len(result.Chunks) != 1 || string(result.Chunks[0]) != string(visibleChunk) {
		t.Fatalf("fast path must pass the chunk through unchanged: %+v", result.Chunks)
	}

	errorChunk := []byte("data: {\"error\":{\"code\":\"x\",\"message\":\"失败\"}}\n\n")
	_ = buffer.PushChunk(errorChunk)
	if calls != 1 {
		t.Fatalf("error-rooted event must be inspected, policy calls = %d", calls)
	}
}

// 声明策略需要检查可见输出文本：文本 delta 事件进策略（快路径关闭）。
func TestBufferInspectsVisibleOutputWhenDeclared(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies:                            []InspectionPolicy{countingPolicy(&calls)},
		RequiresVisibleOutputTextInspection: []bool{true},
	})
	visibleChunk := []byte("data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	_ = buffer.PushChunk(visibleChunk)
	if calls != 1 {
		t.Fatalf("declared visible-output inspection must invoke policy, calls = %d", calls)
	}
}

// 缺省（无描述符）保持保守：任一策略即全量检查。
func TestBufferStaysConservativeWithoutDescriptors(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{countingPolicy(&calls)},
	})
	visibleChunk := []byte("data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	_ = buffer.PushChunk(visibleChunk)
	if calls != 1 {
		t.Fatalf("no descriptors must stay conservative, calls = %d", calls)
	}

	declaredNone := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies:                            []InspectionPolicy{countingPolicy(&calls)},
		RequiresVisibleOutputTextInspection: []bool{false},
	})
	if declaredNone.inspectVisibleOutputTextEvents {
		t.Fatal("all-false descriptors must disable visible-output inspection")
	}
}
