package cooldownaccountretest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultBatchSize      = 20
	DefaultEnqueueWorkers = 8
	DefaultTaskTimeout    = 60 * time.Second
	DefaultOutcomeTimeout = 5 * time.Second
	neutralInitialDelay   = 30 * time.Second
	neutralMaxDelay       = 15 * time.Minute
	neutralJitterRatio    = 0.2
)

var (
	ErrProbeNotConfigured       = errors.New("cooldown account retest probe is not configured")
	ErrUnsupportedProbeOutcome = errors.New("unsupported cooldown account retest probe outcome")
)

type Enqueuer interface {
	EnqueueCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask) (bool, error)
}

type Probe interface {
	Probe(context.Context, port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error)
}

type QueueSnapshot struct {
	PendingCount int
	RunningCount int
	RetryCount   int
}

type QueueCapacity interface {
	CooldownAccountRetestQueueSnapshot(context.Context) (QueueSnapshot, error)
}

type QuotaChecker interface {
	EligibleByAccountID(context.Context, []port.CooldownAccountRetestCandidate, time.Time) (map[string]bool, error)
}

type Scheduler struct {
	Store            port.CooldownAccountRetestStore
	Enqueuer         Enqueuer
	Capacity         QueueCapacity
	Quota            QuotaChecker
	BatchSize        int
	EnqueueWorkers   int
	MaxPauseMinutes  int
	MaxRecoveryHours int
}

type ScheduleResult struct {
	CandidateCount        int
	EnqueuedCount         int
	DuplicateCount        int
	InvalidCandidateCount int
	QuotaRejectedCount    int
	AvailableSlots        int
}

func (s Scheduler) RunPage(ctx context.Context, cursor *port.CooldownAccountRetestCursor, now time.Time) (ScheduleResult, *port.CooldownAccountRetestCursor, error) {
	if s.Store == nil || s.Enqueuer == nil || s.Quota == nil {
		return ScheduleResult{}, cursor, fmt.Errorf("cooldown account retest store, enqueuer, and quota checker are required")
	}
	limit := clamp(s.BatchSize, 1, port.CooldownAccountRetestMaxPageSize, DefaultBatchSize)
	availableSlots := limit
	if s.Capacity != nil {
		snapshot, err := s.Capacity.CooldownAccountRetestQueueSnapshot(ctx)
		if err != nil {
			return ScheduleResult{}, cursor, fmt.Errorf("read cooldown account retest queue capacity: %w", err)
		}
		occupied := max(snapshot.PendingCount, 0) + max(snapshot.RunningCount, 0) + max(snapshot.RetryCount, 0)
		availableSlots = max(0, limit-occupied)
		if availableSlots == 0 {
			return ScheduleResult{AvailableSlots: 0}, cursor, nil
		}
	}
	workers := clamp(s.EnqueueWorkers, 1, availableSlots, DefaultEnqueueWorkers)
	page, err := s.Store.ListDueCooldownAccountRetests(ctx, port.CooldownAccountRetestListInput{
		Now: now, Limit: port.CooldownAccountRetestMaxPageSize, Cursor: cursor,
	})
	if err != nil {
		return ScheduleResult{}, cursor, fmt.Errorf("list cooldown account retest candidates: %w", err)
	}
	result := ScheduleResult{CandidateCount: len(page.Candidates), AvailableSlots: availableSlots}
	validCandidates := make([]port.CooldownAccountRetestCandidate, 0, len(page.Candidates))
	for _, candidate := range page.Candidates {
		if !validCandidateFence(candidate) {
			result.InvalidCandidateCount++
			continue
		}
		validCandidates = append(validCandidates, candidate)
	}
	quotaEligible, err := s.Quota.EligibleByAccountID(ctx, validCandidates, now)
	if err != nil {
		return result, cursor, fmt.Errorf("check cooldown account retest quota eligibility: %w", err)
	}
	selectedCandidates := make([]port.CooldownAccountRetestCandidate, 0, availableSlots)
	nextCursor := page.NextCursor
	for index, candidate := range page.Candidates {
		if !validCandidateFence(candidate) {
			continue
		}
		if !quotaEligible[strings.TrimSpace(candidate.ID)] {
			result.QuotaRejectedCount++
			continue
		}
		selectedCandidates = append(selectedCandidates, candidate)
		if len(selectedCandidates) == availableSlots {
			if index < len(page.Candidates)-1 || page.NextCursor != nil {
				nextCursor = cooldownAccountRetestCursorAfter(candidate)
			} else {
				nextCursor = nil
			}
			break
		}
	}
	jobs := make(chan port.CooldownAccountRetestCandidate)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	worker := func() {
		defer wg.Done()
		for candidate := range jobs {
			task := port.CooldownAccountRetestTask{
				AccountID: candidate.ID, ConfigRevision: candidate.ConfigRevision,
				DispatchRevision:     candidate.DispatchRevision,
				ObservationStartedAt: candidate.ObservationStartedAt,
				Generation:           accounthealth.NormalizeCooldownRetestGeneration(candidate.Generation),
				SourceConfigRevision: candidate.SourceConfigRevision,
				MaxPauseMinutes:      s.MaxPauseMinutes, MaxRecoveryHours: s.MaxRecoveryHours,
			}
			queued, enqueueErr := s.Enqueuer.EnqueueCooldownAccountRetest(ctx, task)
			mu.Lock()
			if enqueueErr == nil && queued {
				result.EnqueuedCount++
			}
			if enqueueErr == nil && !queued {
				result.DuplicateCount++
			}
			if enqueueErr != nil && firstErr == nil {
				firstErr = enqueueErr
			}
			mu.Unlock()
			if enqueueErr != nil && ctx.Err() != nil {
				return
			}
		}
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}
	for _, candidate := range selectedCandidates {
		select {
		case jobs <- candidate:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return result, cursor, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return result, cursor, err
	}
	if firstErr != nil {
		return result, cursor, fmt.Errorf("enqueue cooldown account retest: %w", firstErr)
	}
	return result, nextCursor, nil
}

func cooldownAccountRetestCursorAfter(candidate port.CooldownAccountRetestCandidate) *port.CooldownAccountRetestCursor {
	return &port.CooldownAccountRetestCursor{
		CooldownUntil: candidate.CooldownUntil, Priority: candidate.Priority, CreatedAt: candidate.CreatedAt, ID: candidate.ID,
	}
}

func validCandidateFence(candidate port.CooldownAccountRetestCandidate) bool {
	return strings.TrimSpace(candidate.ID) != "" && accounthealth.CooldownRetestTaskVersionValid(candidateVersion(candidate))
}

func validTaskFence(task port.CooldownAccountRetestTask) bool {
	return strings.TrimSpace(task.AccountID) != "" && accounthealth.CooldownRetestTaskVersionValid(taskVersion(task))
}

type Processor struct {
	Store          port.CooldownAccountRetestStore
	Outcomes       port.CooldownAccountRetestOutcomeStore
	Probe          Probe
	Quota          QuotaChecker
	TaskTimeout    time.Duration
	OutcomeTimeout time.Duration
	Now            func() time.Time
}

func (p Processor) RunTask(ctx context.Context, task port.CooldownAccountRetestTask) error {
	if p.Store == nil || p.Outcomes == nil {
		return fmt.Errorf("cooldown account retest stores are required")
	}
	if !validTaskFence(task) {
		return nil
	}
	if p.Quota == nil {
		return fmt.Errorf("cooldown account retest quota checker is required")
	}
	if p.Probe == nil {
		return ErrProbeNotConfigured
	}
	timeout := p.TaskTimeout
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	now := p.currentTime()
	candidate, ok, err := p.Store.FindDueCooldownAccountRetest(taskCtx, task.AccountID, now)
	if err != nil {
		return fmt.Errorf("find cooldown account retest candidate: %w", err)
	}
	if !ok || candidate.ID != task.AccountID || !validCandidateFence(candidate) ||
		!accounthealth.CooldownRetestTaskCurrent(taskVersion(task), candidateVersion(candidate)) {
		return nil
	}
	quotaEligible, err := p.Quota.EligibleByAccountID(taskCtx, []port.CooldownAccountRetestCandidate{candidate}, now)
	if err != nil {
		return fmt.Errorf("recheck cooldown account retest quota eligibility: %w", err)
	}
	if !quotaEligible[strings.TrimSpace(candidate.ID)] {
		return nil
	}
	result, err := p.Probe.Probe(taskCtx, candidate)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("probe cooldown account retest interrupted: %w", ctx.Err())
		}
		// A transport or timeout error has no attributable upstream result.  Move
		// the candidate out of the due set before acknowledging this task; otherwise
		// a queue retry and the scheduler can issue duplicate probes concurrently.
		if deferErr := p.deferOutcome(ctx, task, p.currentTime()); deferErr != nil {
			return fmt.Errorf("defer cooldown account retest after probe error (%v): %w", err, deferErr)
		}
		return nil
	}
	if taskErr := taskCtx.Err(); taskErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("probe cooldown account retest interrupted: %w", ctx.Err())
		}
		return p.deferOutcome(ctx, task, p.currentTime())
	}
	action, supported := accounthealth.CooldownRetestActionFor(accounthealth.ProbeOutcome(result.Outcome))
	if !supported {
		return fmt.Errorf("%w %q", ErrUnsupportedProbeOutcome, result.Outcome)
	}
	switch action {
	case accounthealth.RetestActionRestore:
		return p.recordOutcome(ctx, func(outcomeCtx context.Context) error {
			return p.Outcomes.RecordCooldownAccountRetestSuccess(outcomeCtx, task)
		})
	case accounthealth.RetestActionDefer:
		return p.deferOutcome(ctx, task, p.currentTime())
	case accounthealth.RetestActionRecordFailure:
		return p.recordOutcome(ctx, func(outcomeCtx context.Context) error {
			return p.Outcomes.RecordCooldownAccountRetestFailure(outcomeCtx, task, result)
		})
	}
	return fmt.Errorf("unsupported cooldown account retest action %q", action)
}

func (p Processor) currentTime() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Processor) deferOutcome(ctx context.Context, task port.CooldownAccountRetestTask, now time.Time) error {
	return p.recordOutcome(ctx, func(outcomeCtx context.Context) error {
		return p.Outcomes.DeferCooldownAccountRetest(outcomeCtx, task, neutralDeferDelay(task, now))
	})
}

func (p Processor) recordOutcome(ctx context.Context, write func(context.Context) error) error {
	timeout := p.OutcomeTimeout
	if timeout <= 0 {
		timeout = DefaultOutcomeTimeout
	}
	// The probe uses a child budget, while outcome persistence uses the outer task
	// budget. Preserve outer cancellation so shutdown cannot outlive worker ownership.
	outcomeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return write(outcomeCtx)
}

func taskVersion(task port.CooldownAccountRetestTask) accounthealth.RetestTaskVersion {
	return accounthealth.RetestTaskVersion{
		ConfigRevision: task.ConfigRevision, DispatchRevision: task.DispatchRevision,
		ObservationStartedAt: task.ObservationStartedAt, Generation: task.Generation,
		SourceConfigRevision: task.SourceConfigRevision,
	}
}

func candidateVersion(candidate port.CooldownAccountRetestCandidate) accounthealth.RetestTaskVersion {
	return accounthealth.RetestTaskVersion{
		ConfigRevision: candidate.ConfigRevision, DispatchRevision: candidate.DispatchRevision,
		ObservationStartedAt: candidate.ObservationStartedAt, Generation: candidate.Generation,
		SourceConfigRevision: candidate.SourceConfigRevision,
	}
}

func neutralDeferDelay(task port.CooldownAccountRetestTask, now time.Time) time.Duration {
	elapsedSeconds := int64(0)
	if task.ObservationStartedAt != nil && !task.ObservationStartedAt.IsZero() && now.After(*task.ObservationStartedAt) {
		elapsedSeconds = int64(now.Sub(*task.ObservationStartedAt) / time.Second)
	}
	completedInitialIntervals := float64(elapsedSeconds) / neutralInitialDelay.Seconds()
	growthStep := int(math.Floor(math.Log2(completedInitialIntervals + 1)))
	if growthStep < 0 {
		growthStep = 0
	}
	if growthStep > 5 {
		growthStep = 5
	}
	baseSeconds := int(neutralInitialDelay.Seconds()) * (1 << growthStep)
	if maxSeconds := int(neutralMaxDelay.Seconds()); baseSeconds > maxSeconds {
		baseSeconds = maxSeconds
	}
	jitterRange := max(1, int(math.Floor(float64(baseSeconds)*neutralJitterRatio)))
	jitterBuckets := jitterRange*2 + 1
	hashInput := fmt.Sprintf("%s:%s:%d", task.AccountID, task.Generation, growthStep)
	jitterSeconds := int(stableCooldownRetestHash(hashInput)%uint32(jitterBuckets)) - jitterRange
	delaySeconds := baseSeconds + jitterSeconds
	if delaySeconds < 3 {
		delaySeconds = 3
	}
	if maxSeconds := int(neutralMaxDelay.Seconds()); delaySeconds > maxSeconds {
		delaySeconds = maxSeconds
	}
	return time.Duration(delaySeconds) * time.Second
}

func stableCooldownRetestHash(value string) uint32 {
	hash := uint32(2166136261)
	for _, codeUnit := range utf16.Encode([]rune(value)) {
		hash ^= uint32(codeUnit)
		hash *= 16777619
	}
	return hash
}

func clamp(value, min, max, fallback int) int {
	if value == 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
