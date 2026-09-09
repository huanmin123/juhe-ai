package oauthrefresh

import "errors"

// tokenExchangeFailureLogAttrs builds the bounded log fields for a failed
// token refresh the way Node's buildRequestStageLogFields classifies outcomes
// (shared/request-context.ts): local configuration failures are EXPECTED
// failures logged with a stable reasonCode and sanitized decision inputs;
// everything else is an UNEXPECTED failure logged with the bounded error
// capture (cause chain, stage snapshot, byte budget, hash attribution). The
// returned args slice is flat, mirroring how Node flattens the failure context
// into the top-level log payload.
func tokenExchangeFailureLogAttrs(err error, provider, stage string, decisionInputs map[string]any) []any {
	if err == nil {
		return nil
	}
	if IsLocalConfigurationError(err) {
		if decisionInputs == nil {
			decisionInputs = map[string]any{}
		}
		reasonCode := failureReasonCode(err)
		if reasonCode == "" {
			reasonCode = stage
		}
		context, captureErr := CaptureExpectedFailureContext(reasonCode, decisionInputs)
		if captureErr != nil {
			return []any{"failureClass", "expected", "reasonCode", stage, "error", err.Error()}
		}
		return expectedFailureLogAttrs(context)
	}
	context := CaptureUnexpectedFailureContext(err, FailureCaptureOptions{
		StageSnapshot:  map[string]any{"stage": stage, "provider": provider},
		DecisionInputs: decisionInputs,
	})
	args := []any{
		"failureClass", context.FailureClass,
		"error", context.Error,
		"stageSnapshot", context.StageSnapshot,
		"redactedFields", context.RedactedFields,
		"fieldSizes", context.FieldSizes,
		"fieldHashes", context.FieldHashes,
		"truncationReason", context.TruncationReason,
	}
	return args
}

func expectedFailureLogAttrs(context ExpectedFailureContext) []any {
	args := []any{
		"failureClass", context.FailureClass,
		"reasonCode", context.ReasonCode,
		"decisionInputs", context.DecisionInputs,
		"redactedFields", context.RedactedFields,
		"fieldSizes", context.FieldSizes,
		"fieldHashes", context.FieldHashes,
	}
	if context.TruncationReason != "" {
		args = append(args, "truncationReason", context.TruncationReason)
	}
	return args
}

// failureReasonCode derives a stable reason code from a known expected failure.
func failureReasonCode(err error) string {
	var local *LocalConfigurationError
	if errors.As(err, &local) {
		return "local_configuration"
	}
	return ""
}
