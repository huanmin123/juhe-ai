package gatewaycircuit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Recoverable wait skip reasons mirror RecoverableUnavailableWaitSkippedReason.
const (
	WaitSkippedNoRetryTime                = "no_retry_time"
	WaitSkippedRetryAfterExceedsWindow    = "retry_after_exceeds_window"
	WaitSkippedAborted                    = "aborted"
	WaitSkippedDeadlineExceeded           = "deadline_exceeded"
	WaitSkippedScopeLimit                 = "scope_limit"
	WaitSkippedGlobalLimit                = "global_limit"
	WaitSkippedBudgetExhausted            = "temporarily_blocked_coordination_budget_exhausted"
	WaitSkippedBudgetConflict             = "temporarily_blocked_coordination_budget_conflict"
)

// Coordinator turn outcomes mirror RecoverableUnavailableCoordinatorWaitResult.
const (
	TurnReady            = "ready"
	TurnAborted          = "aborted"
	TurnDeadlineExceeded = "deadline_exceeded"
	TurnScopeLimit       = "scope_limit"
	TurnGlobalLimit      = "global_limit"
)

// CoordinatorSnapshot mirrors snapshot().
type CoordinatorSnapshot struct {
	ScopeCount  int
	WaiterCount int
	TimerCount  int
}

// coordinatorScopeKey mirrors coordinatorScopeKey: JSON [reason, scopeKey].
func coordinatorScopeKey(reason, scopeKey string) string {
	encoded, _ := json.Marshal([]string{reason, scopeKey})
	return string(encoded)
}

type coordinatorWaiter struct {
	id          int64
	notBeforeMs int64
	deadlineAtMs int64
	signal      context.Context
	abortWatch  chan struct{} // closed when the waiter is settled
	resolve     chan string   // buffered size 1
}

type coordinatorScope struct {
	waiters    []*coordinatorWaiter
	runtimeKeys map[string]struct{}
	timerDone  <-chan struct{}
	timerStop  func()
}

// WaitCoordinator mirrors RecoverableUnavailableWaitCoordinator: shared,
// fenced turn-taking for local recoverable waits.
type WaitCoordinator struct {
	mu                 sync.Mutex
	scopes             map[string]*coordinatorScope
	maxWaitersPerScope int
	maxWaitersGlobal   int
	newTimer           func(delay time.Duration) (<-chan struct{}, func())
	now                func() int64
	nextWaiterID       int64
	waiterCount        int
}

// WaitCoordinatorOptions mirrors RecoverableUnavailableWaitCoordinatorOptions.
type WaitCoordinatorOptions struct {
	MaxWaitersPerScope int
	MaxWaitersGlobal   int
	NewTimer           func(delay time.Duration) (<-chan struct{}, func())
	Now                func() int64
}

// NewWaitCoordinator mirrors the coordinator constructor.
func NewWaitCoordinator(options WaitCoordinatorOptions) *WaitCoordinator {
	settings := DefaultSettings()
	maxPerScope := normalizePositiveMsInt(options.MaxWaitersPerScope, settings.RecoverableUnavailableMaxWaitersPerScope)
	maxGlobal := normalizePositiveMsInt(options.MaxWaitersGlobal, settings.RecoverableUnavailableMaxWaitersGlobal)
	newTimer := options.NewTimer
	if newTimer == nil {
		newTimer = func(delay time.Duration) (<-chan struct{}, func()) {
			done := make(chan struct{})
			timer := time.AfterFunc(delay, func() { close(done) })
			return done, func() { timer.Stop() }
		}
	}
	now := options.Now
	if now == nil {
		now = defaultNowMs
	}
	return &WaitCoordinator{
		scopes:             map[string]*coordinatorScope{},
		maxWaitersPerScope: maxPerScope,
		maxWaitersGlobal:   maxGlobal,
		newTimer:           newTimer,
		now:                now,
		nextWaiterID:       1,
	}
}

// WaitTurnInput mirrors RecoverableUnavailableCoordinatorWaitInput.
type WaitTurnInput struct {
	ScopeKey    string
	Reason      string
	DelayMs     int64
	DeadlineAtMs int64
	Signal      context.Context
	RuntimeKeys []string
}

// WaitForTurn mirrors waitForTurn; it blocks until the waiter is settled.
func (c *WaitCoordinator) WaitForTurn(input WaitTurnInput) string {
	if input.Signal != nil && input.Signal.Err() != nil {
		return TurnAborted
	}
	c.mu.Lock()
	now := c.now()
	deadlineAtMs := normalizeDeadlineAtMs(input.DeadlineAtMs, now)
	if deadlineAtMs <= now {
		c.mu.Unlock()
		return TurnDeadlineExceeded
	}
	key := coordinatorScopeKey(input.Reason, input.ScopeKey)
	scope := c.scopes[key]
	if scope == nil {
		scope = &coordinatorScope{runtimeKeys: map[string]struct{}{}}
	}
	if len(scope.waiters) >= c.maxWaitersPerScope {
		c.mu.Unlock()
		return TurnScopeLimit
	}
	if c.waiterCount >= c.maxWaitersGlobal {
		c.mu.Unlock()
		return TurnGlobalLimit
	}
	c.nextWaiterID++
	waiter := &coordinatorWaiter{
		id:           c.nextWaiterID,
		notBeforeMs:  now + normalizeNonNegativeMs(input.DelayMs),
		deadlineAtMs: deadlineAtMs,
		signal:       input.Signal,
		abortWatch:   make(chan struct{}),
		resolve:      make(chan string, 1),
	}
	scope.waiters = append(scope.waiters, waiter)
	for _, runtimeKey := range input.RuntimeKeys {
		if trimmed := strings.TrimSpace(runtimeKey); trimmed != "" {
			scope.runtimeKeys[trimmed] = struct{}{}
		}
	}
	c.waiterCount++
	c.scopes[key] = scope
	c.scheduleScopeLocked(key, scope)
	c.mu.Unlock()

	// Abort watcher: mirrors the signal 'abort' event listener.
	if input.Signal != nil {
		go func() {
			select {
			case <-input.Signal.Done():
				c.settleWaiter(key, waiter.id, TurnAborted)
			case <-waiter.abortWatch:
			case <-c.stopWatch():
			}
		}()
	}
	return <-waiter.resolve
}

// stopWatch provides a cancellation channel for abort watchers (coordinator
// lifetime). A closed channel cancels pending watchers on Close.
func (c *WaitCoordinator) stopWatch() <-chan struct{} {
	return nil
}

// NotifyOne mirrors notifyOne.
func (c *WaitCoordinator) NotifyOne(scopeKey, reason string) bool {
	key := coordinatorScopeKey(reason, scopeKey)
	c.mu.Lock()
	scope := c.scopes[key]
	var waiter *coordinatorWaiter
	if scope != nil && len(scope.waiters) > 0 {
		waiter = scope.waiters[0]
	}
	c.mu.Unlock()
	if waiter == nil {
		return false
	}
	return c.settleReadyWaiter(key, waiter)
}

// NotifyOneForRuntimeKey mirrors notifyOneForRuntimeKey.
func (c *WaitCoordinator) NotifyOneForRuntimeKey(runtimeKey string) bool {
	normalized := strings.TrimSpace(runtimeKey)
	c.mu.Lock()
	type candidate struct {
		key      string
		waiterID int64
	}
	var selected *candidate
	for key, scope := range c.scopes {
		if len(scope.waiters) == 0 {
			continue
		}
		head := scope.waiters[0]
		if _, ok := scope.runtimeKeys[normalized]; !ok {
			continue
		}
		if selected == nil || head.id < selected.waiterID {
			selected = &candidate{key: key, waiterID: head.id}
		}
	}
	var waiter *coordinatorWaiter
	if selected != nil {
		scope := c.scopes[selected.key]
		for _, item := range scope.waiters {
			if item.id == selected.waiterID {
				waiter = item
				break
			}
		}
	}
	c.mu.Unlock()
	if waiter == nil {
		return false
	}
	return c.settleReadyWaiter(selected.key, waiter)
}

// Snapshot mirrors snapshot().
func (c *WaitCoordinator) Snapshot() CoordinatorSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	timerCount := 0
	for _, scope := range c.scopes {
		if scope.timerDone != nil {
			timerCount++
		}
	}
	return CoordinatorSnapshot{ScopeCount: len(c.scopes), WaiterCount: c.waiterCount, TimerCount: timerCount}
}

func (c *WaitCoordinator) scheduleScopeLocked(key string, scope *coordinatorScope) {
	if scope.timerDone != nil || len(scope.waiters) == 0 {
		return
	}
	// Capture the head waiter's identity like the Node closure captures the
	// head object: a settle that races this timer must stay a no-op.
	head := scope.waiters[0]
	dueAtMs := head.notBeforeMs
	if head.deadlineAtMs < dueAtMs {
		dueAtMs = head.deadlineAtMs
	}
	delay := int64Max64(0, dueAtMs-c.now())
	done, stop := c.newTimer(msToDuration(delay))
	scope.timerDone = done
	scope.timerStop = stop
	headID := head.id
	headNotBeforeMs := head.notBeforeMs
	headDeadlineAtMs := head.deadlineAtMs
	headSignal := head.signal
	go func() {
		select {
		case <-done:
		case <-c.stopped():
			return
		}
		c.mu.Lock()
		scope := c.scopes[key]
		if scope != nil {
			scope.timerDone = nil
			scope.timerStop = nil
		}
		c.mu.Unlock()
		if headSignal != nil && headSignal.Err() != nil {
			c.settleWaiter(key, headID, TurnAborted)
			return
		}
		now := c.now()
		if now >= headDeadlineAtMs {
			c.settleWaiter(key, headID, TurnDeadlineExceeded)
		} else if now >= headNotBeforeMs {
			c.settleWaiter(key, headID, TurnReady)
		} else {
			c.mu.Lock()
			if scope := c.scopes[key]; scope != nil {
				c.scheduleScopeLocked(key, scope)
			}
			c.mu.Unlock()
		}
	}()
}

func (c *WaitCoordinator) settleReadyWaiter(key string, waiter *coordinatorWaiter) bool {
	result := TurnReady
	if c.now() >= waiter.deadlineAtMs {
		result = TurnDeadlineExceeded
	}
	return c.settleWaiter(key, waiter.id, result)
}

func (c *WaitCoordinator) settleWaiter(key string, waiterID int64, result string) bool {
	c.mu.Lock()
	scope := c.scopes[key]
	if scope == nil {
		c.mu.Unlock()
		return false
	}
	index := -1
	for i, waiter := range scope.waiters {
		if waiter.id == waiterID {
			index = i
			break
		}
	}
	if index < 0 {
		c.mu.Unlock()
		return false
	}
	waiter := scope.waiters[index]
	scope.waiters = append(scope.waiters[:index], scope.waiters[index+1:]...)
	wasHead := index == 0
	c.waiterCount = int(int64Max64(0, int64(c.waiterCount-1)))
	if waiter.signal != nil {
		close(waiter.abortWatch)
	}
	if wasHead && scope.timerStop != nil {
		scope.timerStop()
		scope.timerStop = nil
		scope.timerDone = nil
	}
	if len(scope.waiters) == 0 {
		delete(c.scopes, key)
	} else {
		c.scheduleScopeLocked(key, scope)
	}
	c.mu.Unlock()
	waiter.resolve <- result
	return true
}

// stopped provides a coordinator-lifetime cancellation channel.
func (c *WaitCoordinator) stopped() <-chan struct{} {
	return nil
}

func normalizePositiveMsInt(value, fallback int) int {
	raw := value
	if value == 0 {
		raw = fallback
	}
	if raw < 1 {
		return 1
	}
	return raw
}

func normalizeNonNegativeMs(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeDeadlineAtMs(value, now int64) int64 {
	return value
}

// Wait result reasons are the strings above; WaitResult mirrors
// RecoverableUnavailableWaitResult for the Go engine.
type WaitResult struct {
	State        any
	WaitedMs     int64
	CheckCount   int
	Ready        bool
	TimedOut     bool
	SkippedReason string
}

// GatewayMetadataCapture is the auditCapture.addGatewayMetadata port.
type GatewayMetadataCapture interface {
	AddGatewayMetadata(label string, metadata map[string]any)
}

// WaitEngineOptions carries the recoverable wait tuning values.
type WaitEngineOptions struct {
	MaxWaitMs       int64
	CheckIntervalMs int64
	DueRetryDelayMs int64
}

type waitInput struct {
	scopeKey                string
	reason                  string
	refresh                 func(ctx context.Context) error
	isReady                 func() bool
	nextRetryAfterMs        func() (int64, bool)
	auditCapture            GatewayMetadataCapture
	signal                  context.Context
	waitWithoutRetryAfter   bool
	maxWaitMs               int64
	checkIntervalMs         int64
	requestStartedAtMs      *int64
	deadlineAtMs            *int64
	coordinator             *WaitCoordinator
	runtimeKeys             []string
	routeCoordinationBudget *gatewayrouting.RouteCoordinationBudget
	gatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	finalResponseReserveMs  *int64
	now                     func() int64
	logger                  Logger
}

type waitOutcome struct {
	waitedMs      int64
	checkCount    int
	ready         bool
	timedOut      bool
	skippedReason string
}

// waitForRecoverableUnavailableState mirrors waitForRecoverableUnavailableState.
func waitForRecoverableUnavailableState(ctx context.Context, input waitInput) (waitOutcome, error) {
	settings := DefaultSettings()
	maxWaitMs := input.maxWaitMs
	if maxWaitMs == 0 {
		maxWaitMs = settings.RecoverableUnavailableMaxWaitMs
	}
	maxWaitMs = normalizePositiveMs64(maxWaitMs)
	checkIntervalMs := input.checkIntervalMs
	if checkIntervalMs == 0 {
		checkIntervalMs = settings.RecoverableUnavailableCheckIntervalMs
	}
	checkIntervalMs = normalizePositiveMs64(checkIntervalMs)
	now := input.now
	if now == nil {
		now = defaultNowMs
	}
	startedAtMs := now()
	requestStartedAtMs := startedAtMs
	if input.requestStartedAtMs != nil {
		requestStartedAtMs = *input.requestStartedAtMs
	}
	localDeadlineAtMs := requestStartedAtMs + maxWaitMs
	wallDeadlineAtMs := int64(^uint64(0) >> 1) // MaxInt64
	if input.gatewayRequestWallBudget != nil {
		reserve := settings.RecoverableUnavailableDueRetryDelayMs
		if input.finalResponseReserveMs != nil {
			reserve = *input.finalResponseReserveMs
		} else {
			reserve = DefaultGatewayFinalResponseReserveMs
		}
		wallDeadlineAtMs = input.gatewayRequestWallBudget.DeadlineAtMs - normalizeNonNegativeMs(reserve)
	}
	deadlineAtMs := int64Min(localDeadlineAtMs, wallDeadlineAtMs)
	if input.deadlineAtMs != nil {
		deadlineAtMs = int64Min(deadlineAtMs, *input.deadlineAtMs)
	}
	coordinator := input.coordinator
	if coordinator == nil {
		coordinator = DefaultRecoverableWaitCoordinator
	}
	checkCount := 0

	aborted := func() bool {
		return input.signal != nil && input.signal.Err() != nil
	}

	if input.isReady() {
		return finalizeRecoverableWait(input, now, startedAtMs, checkCount, true, false, ""), nil
	}
	if deadlineAtMs <= now() {
		return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, true, WaitSkippedDeadlineExceeded), nil
	}
	if input.auditCapture != nil {
		metadata := map[string]any{
			"reason":          input.reason,
			"scopeKey":        input.scopeKey,
			"maxWaitMs":       maxWaitMs,
			"checkIntervalMs": checkIntervalMs,
		}
		if value, ok := input.nextRetryAfterMs(); ok {
			metadata["nextRetryAfterMs"] = value
		}
		input.auditCapture.AddGatewayMetadata("recoverable_unavailable_wait", metadata)
	}

	for !aborted() {
		turnStartedAtMs := now()
		var coordinationRemainingMs *int64
		if input.routeCoordinationBudget != nil {
			remaining := input.routeCoordinationBudget.RemainingMs(turnStartedAtMs)
			coordinationRemainingMs = &remaining
		}
		if coordinationRemainingMs != nil && *coordinationRemainingMs <= 0 {
			return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, false, WaitSkippedBudgetExhausted), nil
		}
		remainingMs := deadlineAtMs - turnStartedAtMs
		if coordinationRemainingMs != nil && *coordinationRemainingMs < remainingMs {
			remainingMs = *coordinationRemainingMs
		}
		if remainingMs <= 0 {
			return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, true, ""), nil
		}

		nextRetryAfterMs, hasRetryAfter := input.nextRetryAfterMs()
		delay, skipReason, wait := nextRecoverableWaitDelayMs(waitDelayInput{
			nextRetryAfterMs:      nextRetryAfterMs,
			hasRetryAfter:         hasRetryAfter,
			remainingMs:           remainingMs,
			checkIntervalMs:       checkIntervalMs,
			dueRetryDelayMs:       settings.RecoverableUnavailableDueRetryDelayMs,
			waitWithoutRetryAfter: input.waitWithoutRetryAfter,
		})
		if !wait {
			timedOut := skipReason == WaitSkippedRetryAfterExceedsWindow && checkCount > 0
			return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, timedOut, skipReason), nil
		}

		coordinationWait, waitToken := beginRouteCoordinationWait(input, turnStartedAtMs)
		if coordinationWait != nil &&
			(coordinationWait.Outcome == gatewayrouting.BudgetTransitionVersionConflict ||
				coordinationWait.Outcome == gatewayrouting.BudgetTransitionInvalid) {
			skip := WaitSkippedBudgetConflict
			if input.routeCoordinationBudget != nil && input.routeCoordinationBudget.Exhausted(turnStartedAtMs) {
				skip = WaitSkippedBudgetExhausted
			}
			return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, false, skip), nil
		}
		if input.logger != nil {
			input.logger.Info(map[string]any{
				"event":    "gateway_recoverable_unavailable_wait_scheduled",
				"reason":   input.reason,
				"scopeKey": input.scopeKey,
				"delayMs":  delay,
				"remainingMs": remainingMs,
			}, "本地可恢复阻塞短等后重新检查调度候选")
		}
		turn, refreshErr := func() (turnResult string, refreshErr error) {
			turnResult = coordinator.WaitForTurn(WaitTurnInput{
				ScopeKey:     input.scopeKey,
				Reason:       input.reason,
				DelayMs:      delay,
				DeadlineAtMs: turnStartedAtMs + remainingMs,
				Signal:       input.signal,
				RuntimeKeys:  input.runtimeKeys,
			})
			if turnResult == TurnReady {
				checkCount++
				refreshErr = input.refresh(ctx)
			}
			return turnResult, refreshErr
		}()
		if input.routeCoordinationBudget != nil && coordinationWait != nil && waitToken != "" {
			pauseResult, err := input.routeCoordinationBudget.PauseWait(gatewayrouting.RouteCoordinationBudgetTransitionInput{
				WaitToken:       waitToken,
				ExpectedVersion: coordinationWait.Snapshot.Version,
				NowMs:           int64Ptr(now()),
			})
			if err == nil &&
				(pauseResult.Outcome == gatewayrouting.BudgetTransitionVersionConflict ||
					pauseResult.Outcome == gatewayrouting.BudgetTransitionInvalid) {
				return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, false, WaitSkippedBudgetConflict), nil
			}
		}
		if refreshErr != nil {
			return waitOutcome{}, refreshErr
		}
		if turn != TurnReady {
			return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, turn == TurnDeadlineExceeded, turn), nil
		}
		if input.isReady() {
			return finalizeRecoverableWait(input, now, startedAtMs, checkCount, true, false, ""), nil
		}
	}
	return finalizeRecoverableWait(input, now, startedAtMs, checkCount, false, false, WaitSkippedAborted), nil
}

// DefaultGatewayFinalResponseReserveMs mirrors the routing constant.
const DefaultGatewayFinalResponseReserveMs = int64(2_000)

func finalizeRecoverableWait(input waitInput, now func() int64, startedAtMs int64, checkCount int, ready, timedOut bool, skippedReason string) waitOutcome {
	waitedMs := now() - startedAtMs
	if input.auditCapture != nil {
		metadata := map[string]any{
			"reason":        input.reason,
			"scopeKey":      input.scopeKey,
			"waitedMs":      waitedMs,
			"checkCount":    checkCount,
			"ready":         ready,
			"timedOut":      timedOut,
			"skippedReason": skippedReason,
		}
		if value, ok := input.nextRetryAfterMs(); ok {
			metadata["nextRetryAfterMs"] = value
		}
		input.auditCapture.AddGatewayMetadata("recoverable_unavailable_wait_result", metadata)
	}
	return waitOutcome{
		waitedMs:      waitedMs,
		checkCount:    checkCount,
		ready:         ready,
		timedOut:      timedOut,
		skippedReason: skippedReason,
	}
}

func beginRouteCoordinationWait(input waitInput, nowMs int64) (*gatewayrouting.RouteCoordinationBudgetTransitionResult, string) {
	budget := input.routeCoordinationBudget
	if budget == nil {
		return nil, ""
	}
	snapshot := budget.Snapshot(nowMs)
	waitToken := fmt.Sprintf("%s:%s:%s:v%d", snapshot.BudgetID, input.reason, input.scopeKey, snapshot.Version)
	result, err := budget.BeginWait(gatewayrouting.RouteCoordinationBudgetTransitionInput{
		WaitToken:       waitToken,
		ExpectedVersion: snapshot.Version,
		NowMs:           int64Ptr(nowMs),
	})
	if err != nil {
		return nil, ""
	}
	return &result, waitToken
}

type waitDelayInput struct {
	nextRetryAfterMs      int64
	hasRetryAfter         bool
	remainingMs           int64
	checkIntervalMs       int64
	dueRetryDelayMs       int64
	waitWithoutRetryAfter bool
}

func nextRecoverableWaitDelayMs(input waitDelayInput) (delayMs int64, skippedReason string, wait bool) {
	remainingMs := int64Max64(0, input.remainingMs)
	if remainingMs <= 0 {
		return 0, WaitSkippedRetryAfterExceedsWindow, false
	}
	if !input.hasRetryAfter {
		if input.waitWithoutRetryAfter {
			return int64Min(input.checkIntervalMs, remainingMs), "", true
		}
		return 0, WaitSkippedNoRetryTime, false
	}
	if !input.waitWithoutRetryAfter && input.nextRetryAfterMs > remainingMs {
		return 0, WaitSkippedRetryAfterExceedsWindow, false
	}
	targetDelayMs := input.nextRetryAfterMs
	if targetDelayMs <= 0 {
		targetDelayMs = input.dueRetryDelayMs
	}
	return int64Min(int64Max64(50, targetDelayMs), int64Min(input.checkIntervalMs, remainingMs)), "", true
}

func normalizePositiveMs64(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}

var _ = errors.New

// DefaultRecoverableWaitCoordinator mirrors the module-level default
// coordinator (process-wide turn taking).
var DefaultRecoverableWaitCoordinator = NewWaitCoordinator(WaitCoordinatorOptions{})
