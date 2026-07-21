package accounthealthcheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	job "juhe-ai/backend-go/internal/jobs/accounthealthcheck"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize      = 50
	defaultMaxCandidates = 200
	defaultConcurrency   = 10
	defaultTaskTimeout   = 30 * time.Second
)

type Service struct {
	reader   port.AccountHealthCheckCandidateReader
	enqueuer job.Enqueuer
}

func NewService(reader port.AccountHealthCheckCandidateReader, enqueuer job.Enqueuer) *Service {
	return &Service{reader: reader, enqueuer: enqueuer}
}

type ScheduleConfig struct {
	PageSize      int
	MaxCandidates int
	Concurrency   int
	Now           time.Time
}

type ScheduleResult struct {
	Scanned  int
	Enqueued int
}

func (s *Service) Schedule(ctx context.Context, cfg ScheduleConfig) (ScheduleResult, error) {
	if s == nil || s.reader == nil || s.enqueuer == nil {
		return ScheduleResult{}, errors.New("account health check dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return ScheduleResult{}, err
	}
	pageSize := bound(cfg.PageSize, defaultPageSize, 1, port.AccountHealthCheckMaxPageSize)
	maxCandidates := bound(cfg.MaxCandidates, defaultMaxCandidates, 1, defaultMaxCandidates)
	concurrency := bound(cfg.Concurrency, defaultConcurrency, 1, defaultMaxCandidates)
	now := cfg.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	result := ScheduleResult{}
	afterID := ""
	for result.Scanned < maxCandidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		limit := min(pageSize, maxCandidates-result.Scanned)
		page, err := s.reader.ListAccountHealthCheckCandidates(ctx, afterID, limit, now)
		if err != nil {
			return result, fmt.Errorf("list account health check candidates: %w", err)
		}
		if len(page.Items) == 0 {
			break
		}
		if err := s.enqueueBatch(ctx, page.Items, concurrency, &result); err != nil {
			return result, err
		}
		result.Scanned += len(page.Items)
		if !page.HasMore || page.NextCursor == "" || len(page.Items) < limit {
			break
		}
		afterID = page.NextCursor
	}
	return result, nil
}

func (s *Service) enqueueBatch(ctx context.Context, candidates []port.AccountHealthCheckCandidate, concurrency int, result *ScheduleResult) error {
	jobs := make(chan port.AccountHealthCheckCandidate)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	workers := min(concurrency, len(candidates))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				task := job.Task{AccountID: candidate.ID, ConfigRevision: candidate.ConfigRevision, UniqueKey: job.UniqueKey(candidate.ID, candidate.ConfigRevision)}
				if err := s.enqueuer.Enqueue(ctx, task); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				mu.Lock()
				result.Enqueued++
				mu.Unlock()
			}
		}()
	}
	for _, candidate := range candidates {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- candidate:
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return fmt.Errorf("enqueue account health check: %w", firstErr)
	}
	return nil
}

type Probe interface {
	Probe(ctx context.Context, candidate port.AccountHealthCheckCandidate) (ProbeResult, error)
}

type ResultSink interface {
	Record(ctx context.Context, task job.Task, result ProbeResult) error
}

type ProbeResult struct {
	Success    bool
	StatusCode int
	ErrorCode  string
	Message    string
}

type CurrentReader interface {
	GetAccountHealthCheckCandidate(ctx context.Context, accountID string, now time.Time) (port.AccountHealthCheckCandidate, bool, error)
}

func (s *Service) Run(ctx context.Context, task job.Task, reader CurrentReader, probe Probe, sink ResultSink, timeout time.Duration) error {
	if reader == nil || probe == nil || sink == nil {
		return errors.New("account health check runner dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = defaultTaskTimeout
	}
	if task.UniqueKey == "" {
		task.UniqueKey = job.UniqueKey(task.AccountID, task.ConfigRevision)
	}
	if _, err := job.Encode(task); err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	candidate, ok, err := reader.GetAccountHealthCheckCandidate(runCtx, strings.TrimSpace(task.AccountID), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("load account health check candidate: %w", err)
	}
	if !ok || candidate.ConfigRevision != task.ConfigRevision || !eligible(candidate, time.Now().UTC()) {
		return nil
	}
	result, err := probe.Probe(runCtx, candidate)
	if err != nil {
		return fmt.Errorf("probe account health check: %w", err)
	}
	if err := sink.Record(runCtx, task, result); err != nil {
		return fmt.Errorf("record account health check result: %w", err)
	}
	return nil
}

func eligible(candidate port.AccountHealthCheckCandidate, now time.Time) bool {
	if candidate.BoundGroupID == "" || (candidate.Status != "active" && candidate.Status != "pending_test") {
		return false
	}
	if candidate.Status == "active" && !candidate.Schedulable {
		return false
	}
	if candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(now) {
		return false
	}
	return candidate.NextCheckAt == nil || !candidate.NextCheckAt.After(now)
}

func bound(value, fallback, minValue, maxValue int) int {
	if value <= 0 {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
