package gatewaydispatch

import (
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Error taxonomy of the dispatch layer, migrated from
// dispatch/upstream-dispatch.ts and dispatch/upstream-attempts.ts.
//
// REFACTOR-0006 阶段 A：上游传输族错误类型（UpstreamRequestTimeoutError /
// UpstreamRequestAbortedError / FirstByteTimeoutSource / GatewayFirstByteTimeoutError /
// GatewayResponsePrecommitDeadlineError / StartedTransportError /
// StartedBodyTransportError / UpstreamBodyReadIncompleteError /
// UpstreamBodyReadMaxLifetimeError / NonStreamUpstreamBodyPipeError /
// UnsupportedUpstreamResponseEncodingError / UnsafeResolvedUpstreamURLError /
// timeoutLikeText / int64CeilDiv）随传输族迁入 gatewayupstream，本包经
// gatewayupstream_bridge.go 的类型 alias/转发保持消费点零改动；
// OpenAI OAuth Codex 适配器错误族迁入 gatewayoauthcodex（经
// gatewayoauthcodex_bridge.go 转发）。
//
// Node tracks "the request reached the upstream" with WeakSets attached to
// error objects. Go errors are values, so the same facts are carried as
// wrapper error types (StartedTransportError / body transport marker) and
// inspected with errors.As — the predicates keep the Node names.

// PrimaryStartedGatewayTransportError mirrors the secondary WeakSet in
// dispatch/upstream-attempts.ts: a started transport failure that escaped
// performUpstreamRequestAttempt.
type PrimaryStartedGatewayTransportError struct{ Err error }

func (e *PrimaryStartedGatewayTransportError) Error() string { return e.Err.Error() }
func (e *PrimaryStartedGatewayTransportError) Unwrap() error { return e.Err }

// IsPrimaryStartedGatewayTransportError mirrors
// isPrimaryStartedGatewayTransportError.
func IsPrimaryStartedGatewayTransportError(err error) bool {
	var target *PrimaryStartedGatewayTransportError
	return errors.As(err, &target)
}

// IsProvenUpstreamBodyTransportError mirrors isProvenUpstreamBodyTransportError:
// only failures proven to have happened while consuming an already-started
// upstream response body count.
func IsProvenUpstreamBodyTransportError(err error) bool {
	var started *StartedBodyTransportError
	if errors.As(err, &started) {
		return true
	}
	var incomplete *UpstreamBodyReadIncompleteError
	if errors.As(err, &incomplete) {
		return IsProvenUpstreamBodyTransportError(incomplete.Cause)
	}
	var pipe *NonStreamUpstreamBodyPipeError
	if errors.As(err, &pipe) {
		return IsProvenUpstreamBodyTransportError(pipe.OriginalError)
	}
	return false
}

// UpstreamAttemptError mirrors dispatch/upstream-dispatch.ts
// UpstreamAttemptError: the engine exhausted every candidate.
type UpstreamAttemptError struct {
	Message                 string
	LastAttempt             *UpstreamAttempt
	FailedAccountIDs        []string
	AgentGuidanceResponse   *gatewaypreauth.GatewayAgentGuidanceResponse
	RecoverableAccountIDs   []string
	TerminalUpstreamFailure bool
}

func (e *UpstreamAttemptError) Error() string { return e.Message }

// AccountLockHoldError 表示当前账号处于锁死窗口（ENGAGED 且未到死亡窗口）：
// 本次请求已以真实失败结束该账号的 attempt，按设计 §3.4 不得切换到同组其他
// 账号或后备分组，交给客户端重试。AccountID 是触发锁死门的账号。
type AccountLockHoldError struct {
	AccountID string
	Message   string
}

func (e *AccountLockHoldError) Error() string { return e.Message }

// GatewayClientRetryMessage implements the gatewaycodex.GatewayClientRetryError
// narrow interface so the Codex compact summary path renders the same 503
// client-retry handoff instead of 502 (设计 §3.4/§10.6).
func (e *AccountLockHoldError) GatewayClientRetryMessage() string {
	if e.Message != "" {
		return e.Message
	}
	return "账户处于锁死窗口，窗口内不切换其他账户，请客户端重试"
}

// NormalRouteFirstByteCutoverError mirrors
// dispatch/upstream-dispatch.ts NormalRouteFirstByteCutoverError.
type NormalRouteFirstByteCutoverError struct {
	AccountID          string
	AccountName        string
	Deadline           gatewayrouting.NormalRouteAttemptFirstByteDeadline
	Message            string
	CutoverReservation any
}

func (e *NormalRouteFirstByteCutoverError) Error() string { return e.Message }

// Code mirrors the readonly code property.
func (e *NormalRouteFirstByteCutoverError) Code() string { return "normal_route_first_byte_timeout" }

// GatewayRequestWallBudgetExhaustedError mirrors
// dispatch/upstream-dispatch.ts GatewayRequestWallBudgetExhaustedError.
type GatewayRequestWallBudgetExhaustedError struct {
	WallRemainingMs            int64
	MinimumMeaningfulAttemptMs int64
	BudgetKind                 string // 'wall' | 'coordination'
}

func (e *GatewayRequestWallBudgetExhaustedError) Error() string {
	if e.BudgetKind == WallBudgetKindCoordination {
		return "网关请求协调等待预算已耗尽，需要交接客户端重试"
	}
	return "网关请求墙钟预算已进入最终响应预留区"
}

// Code mirrors the readonly code property.
func (e *GatewayRequestWallBudgetExhaustedError) Code() string {
	if e.BudgetKind == WallBudgetKindCoordination {
		return "gateway_request_coordination_budget_exhausted"
	}
	return "gateway_request_wall_budget_exhausted"
}

// Wall budget kinds.
const (
	WallBudgetKindWall         = "wall"
	WallBudgetKindCoordination = "coordination"
)
