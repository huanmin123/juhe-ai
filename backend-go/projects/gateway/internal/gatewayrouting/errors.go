package gatewayrouting

// RangeError mirrors the JS RangeError thrown by route coordination
// normalization helpers; Error() carries the original message text.
type RangeError struct{ Message string }

func (e *RangeError) Error() string { return e.Message }

// TypeError mirrors the JS TypeError thrown by route coordination key
// normalization helpers; Error() carries the original message text.
type TypeError struct{ Message string }

func (e *TypeError) Error() string { return e.Message }
