package gatewayhybrid

import "fmt"

// HybridError carries the byte-identical Chinese error messages the Node
// hybrid modules throw (Error with `message`).
type HybridError struct {
	Message string
}

func (err *HybridError) Error() string { return err.Message }

// HybridTypeError / HybridRangeError mirror TypeError / RangeError throws in
// hot-quality-candidate-selection.ts; messages are byte-identical.
type HybridTypeError struct {
	Message string
}

func (err *HybridTypeError) Error() string { return err.Message }

type HybridRangeError struct {
	Message string
}

func (err *HybridRangeError) Error() string { return err.Message }

func typeError(format string, args ...any) error {
	return &HybridTypeError{Message: fmt.Sprintf(format, args...)}
}

func rangeError(format string, args ...any) error {
	return &HybridRangeError{Message: fmt.Sprintf(format, args...)}
}
