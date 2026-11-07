package poolhealth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DueStore is the PG fact-source surface (Store implements it).
type DueStore interface {
	ListDue(ctx context.Context, now time.Time, limit int) ([]Target, error)
	Record(ctx context.Context, target Target, success bool, checkedAt time.Time) (RecordResult, error)
}

// Prober is the availability check (HTTPProber implements it).
type Prober interface {
	Probe(ctx context.Context, target Target) error
}

// HealthWriter is the Redis projection (RedisHealthWriter implements it).
type HealthWriter interface {
	Write(ctx context.Context, profileID string, state HealthState) error
	Delete(ctx context.Context, profileID string) error
}

// Runner executes one bounded probe round. It never starts a second round while
// the previous one is still running (design §7.2).
type Runner struct {
	store       DueStore
	prober      Prober
	health      HealthWriter
	logger      *slog.Logger
	now         func() time.Time
	batchSize   int
	concurrency int
	running     atomic.Bool
}

type RunnerOptions struct {
	Now         func() time.Time
	BatchSize   int
	Concurrency int
	Logger      *slog.Logger
}

func NewRunner(store DueStore, prober Prober, health HealthWriter, options RunnerOptions) (*Runner, error) {
	if store == nil {
		return nil, errors.New("pool health runner requires a store")
	}
	if prober == nil {
		return nil, errors.New("pool health runner requires a prober")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.BatchSize <= 0 {
		options.BatchSize = DefaultBatchSize
	}
	if options.Concurrency <= 0 {
		options.Concurrency = DefaultConcurrency
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Runner{
		store: store, prober: prober, health: health, logger: options.Logger,
		now: options.Now, batchSize: options.BatchSize, concurrency: options.Concurrency,
	}, nil
}

// Summary is one round's accounting.
type Summary struct {
	Scanned          int
	Succeeded        int
	Failed           int
	SkippedConfig    int
	SkippedStale     int
	SkippedCanceled  int
	ProjectionFailed int
	RedisFailed      int
	Duration         time.Duration
}

// RunOnce probes all currently due members once. Probe errors count as
// availability failures (streak +1); configuration errors, cancellation, and
// projection failures leave state/streak/checked_at unchanged.
func (r *Runner) RunOnce(ctx context.Context) (Summary, error) {
	started := r.now()
	if !r.running.CompareAndSwap(false, true) {
		return Summary{}, nil
	}
	defer r.running.Store(false)

	targets, err := r.store.ListDue(ctx, started, r.batchSize)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Scanned: len(targets)}
	if len(targets) == 0 {
		summary.Duration = r.now().Sub(started)
		return summary, nil
	}
	var mutex sync.Mutex
	semaphore := make(chan struct{}, r.concurrency)
	var wait sync.WaitGroup
	for _, target := range targets {
		target := target
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				mutex.Lock()
				summary.SkippedCanceled++
				mutex.Unlock()
				return
			}
			defer func() { <-semaphore }()
			probeErr := r.prober.Probe(ctx, target)
			if errors.Is(probeErr, ErrProbeConfiguration) {
				r.logger.Warn("池代理探测配置无效，跳过状态变更",
					"profile_id", target.ProfileID, "proxy_name", target.Name, "error", probeErr)
				mutex.Lock()
				summary.SkippedConfig++
				mutex.Unlock()
				return
			}
			if ctx.Err() != nil {
				mutex.Lock()
				summary.SkippedCanceled++
				mutex.Unlock()
				return
			}
			success := probeErr == nil
			checkedAt := r.now().UTC()
			result, recordErr := r.store.Record(ctx, target, success, checkedAt)
			if recordErr != nil {
				if ctx.Err() != nil {
					mutex.Lock()
					summary.SkippedCanceled++
					mutex.Unlock()
					return
				}
				r.logger.Warn("池代理健康投影失败", "profile_id", target.ProfileID, "error", recordErr)
				mutex.Lock()
				summary.ProjectionFailed++
				mutex.Unlock()
				return
			}
			if result.Skipped {
				r.logger.Info("池代理连接配置已变更，丢弃在途探测结果",
					"profile_id", target.ProfileID, "proxy_name", target.Name)
				mutex.Lock()
				summary.SkippedStale++
				mutex.Unlock()
				return
			}
			if success {
				mutex.Lock()
				summary.Succeeded++
				mutex.Unlock()
			} else {
				mutex.Lock()
				summary.Failed++
				mutex.Unlock()
			}
			if result.Transitioned {
				r.logger.Info("池代理健康状态变更", "profile_id", target.ProfileID,
					"proxy_name", target.Name, "status", result.Status,
					"failure_streak", result.Streak, "success", success)
			}
			if r.health != nil {
				if writeErr := r.health.Write(ctx, target.ProfileID, result.Status); writeErr != nil {
					r.logger.Warn("池代理 Redis 健康投影写入失败，回退 PG",
						"profile_id", target.ProfileID, "status", result.Status, "error", writeErr)
					if deleteErr := r.health.Delete(ctx, target.ProfileID); deleteErr != nil {
						r.logger.Warn("池代理 Redis 健康投影删除失败，等待 TTL 过期",
							"profile_id", target.ProfileID, "error", deleteErr)
					}
					mutex.Lock()
					summary.RedisFailed++
					mutex.Unlock()
				}
			}
		}()
	}
	wait.Wait()
	summary.Duration = r.now().Sub(started)
	return summary, nil
}
