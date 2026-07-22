package cooldownaccountretest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultBatchSize      = 20
	DefaultEnqueueWorkers = 8
	DefaultTaskTimeout    = 60 * time.Second
	DefaultProbeTaskDelay = 10 * time.Second
	DefaultOutcomeTimeout = 5 * time.Second
)

var ErrProbeNotConfigured = errors.New("cooldown account retest probe is not configured")

type Enqueuer interface {
	EnqueueCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask) (bool, error)
}

type Probe interface {
	Probe(context.Context, port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error)
}

type QueueSnapshot struct {
	PendingCount int
	RunningCount int
}

type QueueCapacity interface {
	CooldownAccountRetestQueueSnapshot(context.Context) (QueueSnapshot, error)
}

type Scheduler struct {
	Store            port.CooldownAccountRetestStore
	Enqueuer         Enqueuer
	Capacity         QueueCapacity
	BatchSize        int
	EnqueueWorkers   int
	MaxPauseMinutes  int
	MaxRecoveryHours int
}

type ScheduleResult struct {
	CandidateCount int
	EnqueuedCount  int
	DuplicateCount int
	AvailableSlots int
}

func (s Scheduler) RunPage(ctx context.Context, cursor *port.CooldownAccountRetestCursor, now time.Time) (ScheduleResult, *port.CooldownAccountRetestCursor, error) {
	if s.Store == nil || s.Enqueuer == nil {
		return ScheduleResult{}, cursor, fmt.Errorf("cooldown account retest store and enqueuer are required")
	}
	limit := clamp(s.BatchSize, 1, port.CooldownAccountRetestMaxPageSize, DefaultBatchSize)
	availableSlots := limit
	if s.Capacity != nil {
		snapshot, err := s.Capacity.CooldownAccountRetestQueueSnapshot(ctx)
		if err != nil {
			return ScheduleResult{}, cursor, fmt.Errorf("read cooldown account retest queue capacity: %w", err)
		}
		occupied := max(snapshot.PendingCount, 0) + max(snapshot.RunningCount, 0)
		availableSlots = max(0, limit-occupied)
		if availableSlots == 0 {
			return ScheduleResult{AvailableSlots: 0}, cursor, nil
		}
		limit = availableSlots
	}
	workers := clamp(s.EnqueueWorkers, 1, limit, DefaultEnqueueWorkers)
	page, err := s.Store.ListDueCooldownAccountRetests(ctx, port.CooldownAccountRetestListInput{Now: now, Limit: limit, Cursor: cursor})
	if err != nil {
		return ScheduleResult{}, cursor, fmt.Errorf("list cooldown account retest candidates: %w", err)
	}
	result := ScheduleResult{CandidateCount: len(page.Candidates), AvailableSlots: availableSlots}
	jobs := make(chan port.CooldownAccountRetestCandidate)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	worker := func() {
		defer wg.Done()
		for candidate := range jobs {
			task := port.CooldownAccountRetestTask{
				AccountID: candidate.ID, ConfigRevision: candidate.ConfigRevision,
				ObservationStartedAt: candidate.ObservationStartedAt,
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
	for _, candidate := range page.Candidates {
		select {
		case jobs <- candidate:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return result, page.NextCursor, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return result, page.NextCursor, err
	}
	if firstErr != nil {
		return result, page.NextCursor, fmt.Errorf("enqueue cooldown account retest: %w", firstErr)
	}
	return result, page.NextCursor, nil
}

type Processor struct {
	Store          port.CooldownAccountRetestStore
	Outcomes       port.CooldownAccountRetestOutcomeStore
	Probe          Probe
	TaskTimeout    time.Duration
	OutcomeTimeout time.Duration
}

func (p Processor) RunTask(ctx context.Context, task port.CooldownAccountRetestTask) error {
	if p.Store == nil || p.Outcomes == nil {
		return fmt.Errorf("cooldown account retest stores are required")
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
	candidate, ok, err := p.Store.FindDueCooldownAccountRetest(taskCtx, task.AccountID, time.Now())
	if err != nil {
		return fmt.Errorf("find cooldown account retest candidate: %w", err)
	}
	if !ok || candidate.ConfigRevision != task.ConfigRevision || !sameObservation(candidate.ObservationStartedAt, task.ObservationStartedAt) {
		return nil
	}
	result, err := p.Probe.Probe(taskCtx, candidate)
	if err != nil {
		// A transport or timeout error has no attributable upstream result.  Move
		// the candidate out of the due set before acknowledging this task; otherwise
		// a queue retry and the scheduler can issue duplicate probes concurrently.
		if deferErr := p.deferOutcome(ctx, task); deferErr != nil {
			return fmt.Errorf("defer cooldown account retest after probe error (%v): %w", err, deferErr)
		}
		return nil
	}
	switch result.Outcome {
	case "complete_success":
		return p.recordOutcome(ctx, func(outcomeCtx context.Context) error {
			return p.Outcomes.RecordCooldownAccountRetestSuccess(outcomeCtx, task)
		})
	case "probe_task_failure":
		return p.deferOutcome(ctx, task)
	case "upstream_failure":
		return p.recordOutcome(ctx, func(outcomeCtx context.Context) error {
			return p.Outcomes.RecordCooldownAccountRetestFailure(outcomeCtx, task, result)
		})
	default:
		return fmt.Errorf("unsupported cooldown account retest outcome %q", result.Outcome)
	}
}

func (p Processor) deferOutcome(ctx context.Context, task port.CooldownAccountRetestTask) error {
	return p.recordOutcome(ctx, func(outcomeCtx context.Context) error {
		return p.Outcomes.DeferCooldownAccountRetest(outcomeCtx, task, DefaultProbeTaskDelay)
	})
}

func (p Processor) recordOutcome(ctx context.Context, write func(context.Context) error) error {
	timeout := p.OutcomeTimeout
	if timeout <= 0 {
		timeout = DefaultOutcomeTimeout
	}
	// The probe budget may already have elapsed. Outcome writes need their own
	// bounded context so an observed result is not silently dropped because the
	// probe context was cancelled immediately before persistence.
	outcomeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return write(outcomeCtx)
}

func sameObservation(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
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
