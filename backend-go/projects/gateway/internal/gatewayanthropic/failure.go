package gatewayanthropic

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

// 四分类失败归因（与 G02 OpenAI 协议切片同一套分类）：
// 对齐 Node 网关 durable circuit 记录的 lastFailureClass 取值
// （account-circuit-control-plane-bridge.ts classifyFailure / hot-quality outcomeClass
// transport_failure | timeout | read_interruption | incomplete_response）。
type FailureClass string

const (
	// FailureConnectFailed 连接失败（含连接拒绝、DNS、TLS 握手失败）。
	FailureConnectFailed FailureClass = "connect_failed"
	// FailureTimeoutBeforeComplete 完成前超时。
	FailureTimeoutBeforeComplete FailureClass = "timeout_before_complete"
	// FailureReadInterrupted 流/响应体读取中断。
	FailureReadInterrupted FailureClass = "read_interrupted"
	// FailureIncompleteResponse 响应不完整（干净结束但没有终止事件）。
	FailureIncompleteResponse FailureClass = "incomplete_response"
)

// FailureAttribution 一次上游流式/非流式尝试的失败归因结果。
type FailureAttribution struct {
	// Class 四分类之一；流正常终止或客户端取消时为空。
	Class FailureClass
	// Completed 表示流按协议正常终止（message_stop 或 error 事件均已算终止）。
	Completed bool
	// ClientCancelled 表示客户端主动取消（不属于四分类传输质量失败）。
	ClientCancelled bool
	// Message 归因附带的诊断消息。
	Message string
}

// ClassifyFailureReason 对齐 Node classifyFailure 的关键字规则：
// 含 "timeout" → timeout_before_complete；"connect" → connect_failed；
// "read" → read_interrupted；"policy" → explicit_policy；其余 → incomplete_response。
// explicit_policy 不属于四分类传输质量失败，此处返回空。
func ClassifyFailureReason(reason string) FailureClass {
	value := strings.ToLower(reason)
	switch {
	case strings.Contains(value, "timeout"):
		return FailureTimeoutBeforeComplete
	case strings.Contains(value, "connect"):
		return FailureConnectFailed
	case strings.Contains(value, "read"):
		return FailureReadInterrupted
	case strings.Contains(value, "policy"):
		// explicit_policy 是策略性失败，不计入四分类传输质量归因。
		return ""
	default:
		return FailureIncompleteResponse
	}
}

// ClassifyTransportError 将一次传输错误归入四分类之一。
func ClassifyTransportError(err error) (FailureClass, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return "", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeoutBeforeComplete, true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return FailureTimeoutBeforeComplete, true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return FailureReadInterrupted, true
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return FailureTimeoutBeforeComplete, true
	case strings.Contains(message, "connect"), strings.Contains(message, "connection refused"),
		strings.Contains(message, "no such host"), strings.Contains(message, "tls"), strings.Contains(message, "handshake"):
		return FailureConnectFailed, true
	case strings.Contains(message, "read"), strings.Contains(message, "reset"), strings.Contains(message, "broken pipe"), strings.Contains(message, "eof"):
		return FailureReadInterrupted, true
	default:
		return "", false
	}
}

// AttributeStreamOutcome 对一次上游 Anthropic 响应读取做四分类失败归因：
//   - 客户端取消 → 不归因（ClientCancelled）。
//   - 错误为超时 → timeout_before_complete；错误发生在收到任何事件之前 →
//     connect_failed；收到事件后的读取错误 → read_interrupted。
//   - 干净结束（EOF）但没有 message_stop/error 终止事件 → incomplete_response。
//   - 收到终止事件 → 正常完成。
func AttributeStreamOutcome(inspection StreamInspection, err error, clientAborted bool) FailureAttribution {
	if clientAborted {
		return FailureAttribution{ClientCancelled: true}
	}
	if inspection.TerminalReceived {
		// 协议已终止；随后的尾部读取错误不改变归因。
		return FailureAttribution{Completed: true}
	}
	if err == nil {
		return FailureAttribution{Class: FailureIncompleteResponse}
	}
	if class, ok := ClassifyTransportError(err); ok {
		if class == FailureIncompleteResponse {
			// 未知错误发生在流中途时按读取中断归因（对齐 Node routes.ts
			// 上游读取失败 → read_interruption 的缺省行为）。
			class = FailureReadInterrupted
		}
		return FailureAttribution{Class: class, Message: err.Error()}
	}
	if inspection.EventCount == 0 && !inspection.OutputReceived {
		return FailureAttribution{Class: FailureConnectFailed, Message: err.Error()}
	}
	return FailureAttribution{Class: FailureReadInterrupted, Message: err.Error()}
}
