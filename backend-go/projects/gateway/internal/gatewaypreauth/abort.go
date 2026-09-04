package gatewaypreauth

import (
	"errors"
	"regexp"
	"time"
)

// Port of request/abort-attribution.ts (consumed by error-response.ts): the
// server-side diagnostic abort source marker. The source rides on the
// GatewayRequest state (Node stores it on the request object).

// GatewayRequestAbortSource mirrors the union.
type GatewayRequestAbortSource string

const (
	AbortSourceServerDiagnosticTimeout GatewayRequestAbortSource = "server_diagnostic_timeout"
	AbortSourceServerDiagnosticCancel  GatewayRequestAbortSource = "server_diagnostic_cancel"
)

// MarkGatewayRequestAbortSource mirrors markGatewayRequestAbortSource.
func MarkGatewayRequestAbortSource(req *GatewayRequest, source GatewayRequestAbortSource) {
	if req == nil {
		return
	}
	req.abortSource = source
}

// GatewayRequestAbortSourceOf mirrors gatewayRequestAbortSource.
func GatewayRequestAbortSourceOf(req *GatewayRequest) (GatewayRequestAbortSource, bool) {
	if req == nil || req.abortSource == "" {
		return "", false
	}
	return req.abortSource, true
}

// GatewayDiagnosticAbortSourceFromSignal mirrors
// gatewayDiagnosticAbortSourceFromSignal: timeout-like abort reasons carry
// the timeout source, everything else counts as cancellation.
func GatewayDiagnosticAbortSourceFromSignal(reason string, err error) GatewayRequestAbortSource {
	if isTimeoutLikeAbortReason(reason, err) {
		return AbortSourceServerDiagnosticTimeout
	}
	return AbortSourceServerDiagnosticCancel
}

var timeoutLikePattern = regexp.MustCompile(`(?i)timeout|deadline`)

// isTimeoutLikeAbortReason mirrors isTimeoutLikeAbortReason: string reasons
// matching /timeout|deadline/i, Error names of TimeoutError and messages with
// the same pattern.
func isTimeoutLikeAbortReason(reason string, err error) bool {
	if reason != "" && timeoutLikePattern.MatchString(reason) {
		return true
	}
	if err == nil {
		return false
	}
	var timeoutError interface{ Timeout() bool }
	if asErr(err, &timeoutError) && timeoutError.Timeout() {
		return true
	}
	var contextError interface{ DeadlineExceeded() bool }
	if asErr(err, &contextError) && contextError.DeadlineExceeded() {
		return true
	}
	return timeoutLikePattern.MatchString(err.Error())
}

// UpstreamAbortedError is implemented by the upstream transport aborted
// error (G15, UpstreamRequestAbortedError). isUpstreamRequestAbortedError
// also accepts the plain '请求已取消' message.
type UpstreamAbortedError interface {
	error
	UpstreamRequestAborted() bool
}

// IsUpstreamRequestAbortedError mirrors isUpstreamRequestAbortedError.
func IsUpstreamRequestAbortedError(err error) bool {
	if err == nil {
		return false
	}
	var aborted UpstreamAbortedError
	if asErr(err, &aborted) && aborted.UpstreamRequestAborted() {
		return true
	}
	return err.Error() == "请求已取消"
}

// asErr wraps errors.As with an interface target.
func asErr(err error, target any) bool { return errors.As(err, target) }

// downstreamConnectionClosedMessage mirrors the client-abort constant.
const downstreamConnectionClosedMessage = "下游连接关闭"

// guidanceCreatedSeconds mirrors Math.floor(Date.now() / 1000) with the
// injected clock.
func guidanceCreatedSeconds(now time.Time) int64 { return now.Unix() }
