package gatewayaudit

const (
	ErrorCodeMissingStreamTerminal = "upstream_stream_interrupted"
	ErrorCodeInconsistentTerminal  = "inconsistent_terminal"
)

type Outcome string

const (
	OutcomeSuccess           Outcome = "success"
	OutcomeSuccessAfterRetry Outcome = "success_after_retry"
	OutcomeGatewayFailed     Outcome = "gateway_failed"
	OutcomeUpstreamFailed    Outcome = "upstream_failed"
	OutcomeStreamFailed      Outcome = "stream_failed"
	OutcomeDownstreamClosed  Outcome = "downstream_closed"
)

type TerminalInput struct {
	RequestedOutcome Outcome
	Success          bool
	Stream           bool
	TerminalRequired bool
	TerminalReceived bool
	HadFailedAttempt bool
	DownstreamClosed bool
	ErrorPhase       string
	ErrorCode        string
	ErrorMessage     string
}

type Terminal struct {
	Outcome      Outcome `json:"outcome"`
	Success      bool    `json:"success"`
	ErrorPhase   string  `json:"errorPhase,omitempty"`
	ErrorCode    string  `json:"errorCode,omitempty"`
	ErrorMessage string  `json:"errorMessage,omitempty"`
}

// ResolveTerminal derives success from the final outcome instead of accepting
// the contradictory outcome/success combinations possible in the Node caller API.
func ResolveTerminal(input TerminalInput) Terminal {
	if input.DownstreamClosed {
		return Terminal{
			Outcome:      OutcomeDownstreamClosed,
			Success:      false,
			ErrorPhase:   "downstream",
			ErrorCode:    "downstream_connection_closed",
			ErrorMessage: "下游连接关闭",
		}
	}

	if input.Success {
		if input.Stream && input.TerminalRequired && !input.TerminalReceived {
			return Terminal{
				Outcome:      OutcomeStreamFailed,
				Success:      false,
				ErrorPhase:   firstNonEmpty(input.ErrorPhase, "stream"),
				ErrorCode:    firstNonEmpty(input.ErrorCode, ErrorCodeMissingStreamTerminal),
				ErrorMessage: firstNonEmpty(input.ErrorMessage, "流式响应缺少协议终止事件"),
			}
		}
		if input.HadFailedAttempt {
			return Terminal{Outcome: OutcomeSuccessAfterRetry, Success: true}
		}
		return Terminal{Outcome: OutcomeSuccess, Success: true}
	}

	outcome := input.RequestedOutcome
	if !isFailureOutcome(outcome) {
		outcome = OutcomeGatewayFailed
		if input.ErrorCode == "" {
			input.ErrorCode = ErrorCodeInconsistentTerminal
		}
	}
	return Terminal{
		Outcome:      outcome,
		Success:      false,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
	}
}

func isFailureOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeGatewayFailed, OutcomeUpstreamFailed, OutcomeStreamFailed, OutcomeDownstreamClosed:
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
