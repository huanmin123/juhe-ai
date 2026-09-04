package gatewaypreauth

// Port of runtime/server-retry-budget.ts: the server-side wait budget shared
// by the preflight orchestration and the dispatch wait paths.

// GatewayAccountAvailability mirrors the availability union.
type GatewayAccountAvailability string

const (
	AvailabilityDispatchableNow  GatewayAccountAvailability = "dispatchable_now"
	AvailabilityRecoverableLater GatewayAccountAvailability = "recoverable_later"
	AvailabilityHardExhausted    GatewayAccountAvailability = "hard_exhausted"
)

// ServerRetryBudgetWaitObserver mirrors the wait observer hooks.
type ServerRetryBudgetWaitObserver struct {
	OnWaitStarted func()
	OnWaitPaused  func()
}

// ShouldHandoffClient mirrors shouldHandoffClient.
func ShouldHandoffClient(availability GatewayAccountAvailability, noAvailableElapsedMs, waitBudgetMs int64) bool {
	if availability == AvailabilityDispatchableNow {
		return false
	}
	if availability == AvailabilityHardExhausted {
		return true
	}
	return normalizedMs(noAvailableElapsedMs) >= maxInt64(1, normalizedMs(waitBudgetMs))
}

// ServerRetryBudget mirrors the ServerRetryBudget class.
type ServerRetryBudget struct {
	WaitBudgetMs int64

	clock             Clock
	accumulatedWaitMs int64
	waitingSinceMs    *int64
	waitObserver      *ServerRetryBudgetWaitObserver
}

// NewServerRetryBudget mirrors the constructor: the budget is at least 1ms.
func NewServerRetryBudget(waitBudgetMs int64, clock Clock) *ServerRetryBudget {
	if clock == nil {
		clock = SystemClock{}
	}
	return &ServerRetryBudget{WaitBudgetMs: maxInt64(1, normalizedMs(waitBudgetMs)), clock: clock}
}

// NowMs returns the injected clock value (tests) or wall time.
func (b *ServerRetryBudget) NowMs() int64 { return b.clock.Now().UnixMilli() }

// BeginNoAvailableWait mirrors beginNoAvailableWait.
func (b *ServerRetryBudget) BeginNoAvailableWait(nowMs *int64) {
	if b.waitingSinceMs != nil {
		return
	}
	start := b.nowOr(nowMs)
	b.waitingSinceMs = &start
	if b.waitObserver != nil && b.waitObserver.OnWaitStarted != nil {
		b.waitObserver.OnWaitStarted()
	}
}

// PauseNoAvailableWait mirrors pauseNoAvailableWait.
func (b *ServerRetryBudget) PauseNoAvailableWait(nowMs *int64) {
	if b.waitingSinceMs == nil {
		return
	}
	now := b.nowOr(nowMs)
	accumulated := b.accumulatedWaitMs + maxInt64(0, now-*b.waitingSinceMs)
	b.accumulatedWaitMs = minInt64(b.WaitBudgetMs, accumulated)
	b.waitingSinceMs = nil
	if b.waitObserver != nil && b.waitObserver.OnWaitPaused != nil {
		b.waitObserver.OnWaitPaused()
	}
}

// SetWaitObserver mirrors setWaitObserver, including the pause/start edges.
func (b *ServerRetryBudget) SetWaitObserver(observer *ServerRetryBudgetWaitObserver) {
	if b.waitObserver == observer {
		return
	}
	if b.waitingSinceMs != nil && b.waitObserver != nil && b.waitObserver.OnWaitPaused != nil {
		b.waitObserver.OnWaitPaused()
	}
	b.waitObserver = observer
	if b.waitingSinceMs != nil && observer != nil && observer.OnWaitStarted != nil {
		observer.OnWaitStarted()
	}
}

// ElapsedMs mirrors elapsedMs.
func (b *ServerRetryBudget) ElapsedMs(nowMs *int64) int64 {
	currentWaitMs := int64(0)
	if b.waitingSinceMs != nil {
		currentWaitMs = maxInt64(0, b.nowOr(nowMs)-*b.waitingSinceMs)
	}
	return minInt64(b.WaitBudgetMs, b.accumulatedWaitMs+currentWaitMs)
}

// RemainingMs mirrors remainingMs.
func (b *ServerRetryBudget) RemainingMs(nowMs *int64) int64 {
	return maxInt64(0, b.WaitBudgetMs-b.ElapsedMs(nowMs))
}

// DeadlineAtMs mirrors deadlineAtMs: begin the wait then return the deadline.
func (b *ServerRetryBudget) DeadlineAtMs(nowMs *int64) int64 {
	now := b.nowOr(nowMs)
	b.BeginNoAvailableWait(&now)
	return now + b.RemainingMs(&now)
}

// HandoffRequired mirrors handoffRequired.
func (b *ServerRetryBudget) HandoffRequired(availability GatewayAccountAvailability, nowMs *int64) bool {
	return ShouldHandoffClient(availability, b.ElapsedMs(nowMs), b.WaitBudgetMs)
}

func (b *ServerRetryBudget) nowOr(nowMs *int64) int64 {
	if nowMs != nil {
		return normalizedTimestamp(*nowMs)
	}
	return b.clock.Now().UnixMilli()
}

func normalizedMs(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normalizedTimestamp(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
