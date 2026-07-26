package gatewaystreamrelay

import (
	"encoding/json"
	"errors"
)

var (
	// ErrControlledFailureEventNotPermitted means the terminal plan does not
	// allow a client-visible failure event. Callers must honor its retry or
	// disconnect action instead of writing these bytes.
	ErrControlledFailureEventNotPermitted = errors.New("controlled failure event is not permitted")

	// ErrUnsupportedControlledFailureProtocol prevents a generic SSE client
	// from receiving a protocol-specific terminal event it cannot interpret.
	ErrUnsupportedControlledFailureProtocol = errors.New("unsupported controlled failure protocol")
)

// ControlledFailureProtocol identifies the downstream SSE contract. It is
// intentionally caller-owned: Relay cannot infer a client protocol from an
// upstream response or an error string.
type ControlledFailureProtocol string

const (
	ControlledFailureProtocolResponses ControlledFailureProtocol = "openai_responses"
	ControlledFailureProtocolAnthropic ControlledFailureProtocol = "anthropic_messages"
	ControlledFailureProtocolGemini    ControlledFailureProtocol = "gemini_generate_content"
)

const (
	controlledFailureCode    = "upstream_stream_interrupted"
	controlledFailureMessage = "上游流式响应在输出后中断"
)

type responsesFailureEvent struct {
	Type     string                   `json:"type"`
	Response responsesFailureResponse `json:"response"`
}

type responsesFailureResponse struct {
	Status string                  `json:"status"`
	Error  controlledFailureDetail `json:"error"`
}

type controlledFailureDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type anthropicFailureEvent struct {
	Type  string                 `json:"type"`
	Error anthropicFailureDetail `json:"error"`
}

type anthropicFailureDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

type geminiFailureEvent struct {
	Error geminiFailureDetail `json:"error"`
}

type geminiFailureDetail struct {
	Message string `json:"message"`
	Status  string `json:"status"`
	Code    string `json:"code"`
}

// EncodeControlledFailureEvent produces the one safe, public post-commit
// failure event allowed by a TerminalDisposition. It owns neither a listener
// nor a Sink: the future response adapter decides whether and how to write the
// returned bytes, then closes the connection. No raw upstream error text or
// code is accepted here, so those values cannot cross the downstream boundary.
func EncodeControlledFailureEvent(disposition TerminalDisposition, protocol ControlledFailureProtocol) ([]byte, error) {
	if !mayEncodeControlledFailure(disposition) {
		return nil, ErrControlledFailureEventNotPermitted
	}

	var encoded []byte
	var err error
	switch protocol {
	case ControlledFailureProtocolResponses:
		encoded, err = marshalSSEEvent("response.failed", responsesFailureEvent{
			Type: "response.failed",
			Response: responsesFailureResponse{
				Status: "failed",
				Error:  controlledFailureDetail{Code: controlledFailureCode, Message: controlledFailureMessage},
			},
		})
	case ControlledFailureProtocolAnthropic:
		encoded, err = marshalSSEEvent("error", anthropicFailureEvent{
			Type:  "error",
			Error: anthropicFailureDetail{Type: "overloaded_error", Message: controlledFailureMessage, Code: controlledFailureCode},
		})
	case ControlledFailureProtocolGemini:
		encoded, err = marshalSSEEvent("error", geminiFailureEvent{
			Error: geminiFailureDetail{Message: controlledFailureMessage, Status: "UNAVAILABLE", Code: controlledFailureCode},
		})
	default:
		return nil, ErrUnsupportedControlledFailureProtocol
	}
	if err != nil {
		return nil, err
	}
	if !disposition.controlledFailurePermit.consume() {
		return nil, ErrControlledFailureEventNotPermitted
	}
	return encoded, nil
}

func mayEncodeControlledFailure(disposition TerminalDisposition) bool {
	if disposition.controlledFailurePermit == nil || !disposition.EmitControlledEvent || disposition.RetryUpstream || disposition.Disconnect {
		return false
	}
	return disposition.TerminalKind == TerminalKindUpstreamProtocolFailure || disposition.TerminalKind == TerminalKindMissingTerminal || disposition.TerminalKind == TerminalKindReadFailure
}

func marshalSSEEvent(event string, payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append([]byte("event: "+event+"\ndata: "), append(encoded, '\n', '\n')...), nil
}
