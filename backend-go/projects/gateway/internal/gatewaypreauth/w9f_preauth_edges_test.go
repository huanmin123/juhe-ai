package gatewaypreauth

// w9f 覆盖收尾：错误分类与构造器的零覆盖函数，生产逻辑零改动。

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type w9fNamedError struct{}

func (w9fNamedError) Error() string     { return "named" }
func (w9fNamedError) ErrorName() string { return "named_error" }

func TestW9FErrorNameClassification(t *testing.T) {
	if errorName(nil) != "" {
		t.Fatal("nil error renders empty name")
	}
	var named w9fNamedError
	if errorName(named) != "named_error" {
		t.Fatalf("named = %q", errorName(named))
	}
	// errors.Wrap 包装后 errors.As 仍取到 ErrorName。
	wrapped := fmt.Errorf("wrapped: %w", named)
	if errorName(wrapped) != "named_error" {
		t.Fatalf("wrapped = %q", errorName(wrapped))
	}
	if errorName(errors.New("plain")) != "Error" {
		t.Fatal("plain error name")
	}
}

func TestW9FSystemClockNow(t *testing.T) {
	clock := SystemClock{}
	if clock.Now().IsZero() {
		t.Fatal("system clock")
	}
	if _, ok := any(clock).(Clock); !ok {
		t.Fatal("SystemClock implements Clock")
	}
	_ = time.Now()
}

func TestW9FAgentGuidanceAndLocalProtocolResponses(t *testing.T) {
	guidance := &GatewayAgentGuidanceResponse{Message: "m"}
	if guidance.StatusCode() != 200 {
		t.Fatal("guidance status")
	}
	if guidance.ErrorType() != "agent_guidance" {
		t.Fatal("guidance type")
	}
	if guidance.Error() != "m" {
		t.Fatal("guidance message")
	}
	// 构造器：零状态码默认 200。
	local := NewGatewayLocalProtocolResponse(GatewayLocalProtocolResponse{Message: "x", Code: "c", Body: "b", ContentType: "text/plain"})
	if local.StatusCode != 200 {
		t.Fatalf("default status = %d", local.StatusCode)
	}
	if local.ErrorType() != "local_protocol_response" {
		t.Fatal("local type")
	}
	if local.Error() != "x" {
		t.Fatal("local message")
	}
	explicit := NewGatewayLocalProtocolResponse(GatewayLocalProtocolResponse{StatusCode: 418})
	if explicit.StatusCode != 418 {
		t.Fatalf("explicit status = %d", explicit.StatusCode)
	}
}
