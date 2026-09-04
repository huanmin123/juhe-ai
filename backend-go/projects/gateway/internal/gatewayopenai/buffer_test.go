package gatewayopenai

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 回归测试：构造器必须把 options.Policies 赋给实例字段（对齐 Node
// response-inspection-buffer.ts 的 this.policies = options.policies ?? []），
// 否则 Engaged() 恒 false、runPolicies 永远空转，响应巡检策略整体失效。
func TestResponseInspectionBufferEngagesRegisteredPolicies(t *testing.T) {
	policyCalled := false
	policy := InspectionPolicy(func(event ParsedStreamEvent, frames []gatewayproto.SemanticFrame) *InspectionDecision {
		policyCalled = true
		return &InspectionDecision{Action: DecisionIntercept, ErrorCode: "test_blocked", Message: "巡检拦截测试"}
	})

	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{policy},
	})
	if !buffer.Engaged() {
		t.Fatalf("buffer with registered policies must report Engaged()=true")
	}

	result := buffer.PushChunk([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	if !policyCalled {
		t.Fatalf("registered policy must be invoked for parsed events")
	}
	if result.Intercepted == nil {
		t.Fatalf("intercept decision must surface: %+v", result)
	}
	if result.Intercepted.ErrorCode != "test_blocked" {
		t.Fatalf("intercept decision = %+v", result.Intercepted)
	}
}

func TestResponseInspectionBufferWithoutPoliciesStaysDisengaged(t *testing.T) {
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{})
	if buffer.Engaged() {
		t.Fatalf("buffer without policies and without client retry must stay disengaged")
	}
	chunk := []byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	result := buffer.PushChunk(chunk)
	if result.Intercepted != nil {
		t.Fatalf("disengaged buffer must never intercept: %+v", result.Intercepted)
	}
	if len(result.Chunks) != 1 || !strings.Contains(string(result.Chunks[0]), "chatcmpl-1") {
		t.Fatalf("disengaged buffer must pass chunks through: %+v", result.Chunks)
	}
}
