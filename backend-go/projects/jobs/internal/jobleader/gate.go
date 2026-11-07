// Package jobleader implements the jobs process-wide PG leader gate.
//
// The gate holds one row in juhe_stats.background_job_leases. The leader runs
// every supervisor component and scheduled task; standby processes keep only
// the health endpoint alive and wait for the lease. Losing the lease cancels
// the components' leader context; the process is expected to exit and let the
// orchestrator restart it as a standby/leader candidate.
package jobleader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
)

// ErrLeaderLost reports that the process no longer owns the global lease.
var ErrLeaderLost = errors.New("jobs 全局 leader 租约已丢失")

const (
	defaultLeaseTTL       = 30 * time.Second
	defaultRetryInterval  = 5 * time.Second
	defaultRenewSafetyGap = 5 * time.Second
	defaultJobName        = "jobs-global-leader"
	defaultShardKey       = "global"
)

// LeaseStore is the subset of taskruns.Store used by Gate (fake-able in unit
// tests).
type LeaseStore interface {
	AcquireLease(ctx context.Context, input taskruns.LeaseAcquireInput) (bool, error)
	RenewLease(ctx context.Context, leaseKey, ownerID string, leaseUntil time.Time, now *time.Time) (bool, error)
	ReleaseLease(ctx context.Context, leaseKey, ownerID string) error
}

// Options configures the gate. TTL/RetryInterval/RenewInterval are clamped to
// safe defaults when omitted.
type Options struct {
	Store         LeaseStore
	LeaseKey      string
	JobName       string
	ShardKey      string
	OwnerID       string
	TTL           time.Duration
	RetryInterval time.Duration
	RenewInterval time.Duration
	Logger        *slog.Logger
	Now           func() time.Time
}

// Gate owns the process-wide leader lease.
type Gate struct {
	store         LeaseStore
	leaseKey      string
	jobName       string
	shardKey      string
	ownerID       string
	ttl           time.Duration
	retryInterval time.Duration
	renewInterval time.Duration
	renewSafety   time.Duration
	logger        *slog.Logger
	now           func() time.Time

	startOnce   sync.Once
	readyOnce   sync.Once
	lostOnce    sync.Once
	releaseOnce sync.Once

	ready chan struct{}
	lost  chan struct{}
	done  chan struct{}

	leader     atomic.Bool
	leaseUntil atomic.Int64

	releaseMu sync.Mutex
}

// New validates options and builds a gate. OwnerID must be unique per process
// (use UniqueOwnerID when the configured instance ID is shared by replicas).
func New(opts Options) (*Gate, error) {
	if opts.Store == nil {
		return nil, errors.New("jobs leader store 不能为空")
	}
	ownerID := strings.TrimSpace(opts.OwnerID)
	if ownerID == "" {
		return nil, errors.New("jobs leader owner ID 不能为空")
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	retry := opts.RetryInterval
	if retry <= 0 {
		retry = defaultRetryInterval
	}
	renew := opts.RenewInterval
	if renew <= 0 {
		renew = ttl / 3
	}
	if renew <= 0 || renew >= ttl {
		renew = ttl / 3
	}
	safety := ttl / 6
	if safety <= 0 {
		safety = time.Second
	}
	if safety > defaultRenewSafetyGap {
		safety = defaultRenewSafetyGap
	}
	jobName := strings.TrimSpace(opts.JobName)
	if jobName == "" {
		jobName = defaultJobName
	}
	shardKey := strings.TrimSpace(opts.ShardKey)
	if shardKey == "" {
		shardKey = defaultShardKey
	}
	leaseKey := strings.TrimSpace(opts.LeaseKey)
	if leaseKey == "" {
		leaseKey = taskruns.ScheduledLeaseKey(jobName, shardKey)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Gate{
		store:         opts.Store,
		leaseKey:      leaseKey,
		jobName:       jobName,
		shardKey:      shardKey,
		ownerID:       ownerID,
		ttl:           ttl,
		retryInterval: retry,
		renewInterval: renew,
		renewSafety:   safety,
		logger:        logger,
		now:           now,
		ready:         make(chan struct{}),
		lost:          make(chan struct{}),
		done:          make(chan struct{}),
	}, nil
}

// Start begins acquisition/renewal in the background. It is idempotent.
func (g *Gate) Start(ctx context.Context) {
	g.startOnce.Do(func() {
		go g.loop(ctx)
	})
}

// Wait blocks until this process becomes leader, the lease is lost, or ctx is
// done. It never reports success for a gate that has not acquired the lease.
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return errors.New("jobs leader gate 未初始化")
	}
	if g.IsLost() {
		return ErrLeaderLost
	}
	select {
	case <-g.ready:
		if g.IsLost() {
			return ErrLeaderLost
		}
		return nil
	case <-g.lost:
		return ErrLeaderLost
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunComponent runs fn with a context canceled when the leader lease is lost.
// A component that returns because of leader loss yields ErrLeaderLost; callers
// should let the process exit and be restarted by the orchestrator.
func (g *Gate) RunComponent(ctx context.Context, fn func(context.Context) error) error {
	if err := g.Wait(ctx); err != nil {
		return err
	}
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	watchDone := make(chan struct{})
	go func() {
		select {
		case <-g.lost:
			cancel()
		case <-ctx.Done():
			cancel()
		case <-watchDone:
		}
	}()
	err := fn(leaderCtx)
	close(watchDone)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if g.IsLost() {
		return ErrLeaderLost
	}
	return err
}

// IsLeader reports whether the gate currently owns the lease.
func (g *Gate) IsLeader() bool {
	return g != nil && g.leader.Load()
}

// IsLost reports whether the gate has lost an acquired lease.
func (g *Gate) IsLost() bool {
	if g == nil {
		return false
	}
	select {
	case <-g.lost:
		return true
	default:
		return false
	}
}

// LeaseUntil returns the last successfully acquired/renewed expiry (zero when
// no lease has been held).
func (g *Gate) LeaseUntil() time.Time {
	if g == nil {
		return time.Time{}
	}
	ms := g.leaseUntil.Load()
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// Done is closed when the acquisition/renewal loop exits.
func (g *Gate) Done() <-chan struct{} {
	if g == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return g.done
}

// Status returns a health-payload view of the gate.
func (g *Gate) Status() map[string]any {
	if g == nil {
		return map[string]any{"leader": false}
	}
	status := map[string]any{
		"leader":     g.IsLeader(),
		"leaseKey":   g.leaseKey,
		"ownerId":    g.ownerID,
		"leaseTtlMs": g.ttl.Milliseconds(),
	}
	if until := g.LeaseUntil(); !until.IsZero() {
		status["leaseUntil"] = until.Format(time.RFC3339Nano)
	}
	return status
}

// UniqueOwnerID appends a random process token to instanceID so two replicas
// configured with the same JUHE_AI_INSTANCE_ID can never renew each other's
// lease.
func UniqueOwnerID(instanceID string) (string, error) {
	prefix := strings.TrimSpace(instanceID)
	if prefix == "" {
		return "", errors.New("jobs leader instance ID 不能为空")
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("生成 jobs leader owner token 失败: %w", err)
	}
	return prefix + "#" + hex.EncodeToString(token[:]), nil
}

func (g *Gate) loop(ctx context.Context) {
	defer close(g.done)
	defer g.release()

	for {
		acquired, err := g.tryAcquire(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			g.logger.Warn("jobs leader 租约获取失败；稍后重试",
				"event", "jobs_leader_acquire_failed", "leaseKey", g.leaseKey, "error", err.Error())
		}
		if acquired {
			break
		}
		if !waitContext(ctx, g.retryInterval) {
			return
		}
	}

	g.leader.Store(true)
	g.readyOnce.Do(func() { close(g.ready) })
	g.logger.Info("jobs leader 租约已获取",
		"event", "jobs_leader_acquired", "leaseKey", g.leaseKey, "ownerId", g.ownerID,
		"leaseUntil", g.LeaseUntil().Format(time.RFC3339Nano))
	g.renewLoop(ctx)
}

func (g *Gate) tryAcquire(ctx context.Context) (bool, error) {
	now := g.now().UTC()
	until := now.Add(g.ttl)
	acquired, err := g.store.AcquireLease(ctx, taskruns.LeaseAcquireInput{
		LeaseKey:   g.leaseKey,
		JobName:    g.jobName,
		ShardKey:   g.shardKey,
		OwnerID:    g.ownerID,
		LeaseUntil: until,
		Now:        &now,
	})
	if err != nil {
		return false, err
	}
	if acquired {
		g.leaseUntil.Store(until.UnixMilli())
	}
	return acquired, nil
}

func (g *Gate) renewLoop(ctx context.Context) {
	ticker := time.NewTicker(g.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !g.renewOnce(ctx) {
				return
			}
		}
	}
}

func (g *Gate) renewOnce(ctx context.Context) bool {
	now := g.now().UTC()
	until := now.Add(g.ttl)
	renewed, err := g.store.RenewLease(ctx, g.leaseKey, g.ownerID, until, &now)
	if ctx.Err() != nil {
		return false
	}
	if err == nil && renewed {
		g.leaseUntil.Store(until.UnixMilli())
		return true
	}
	if err != nil {
		g.logger.Warn("jobs leader 租约续租失败；将在到期前重试",
			"event", "jobs_leader_renew_failed", "leaseKey", g.leaseKey, "error", err.Error())
	} else {
		g.logger.Warn("jobs leader 租约已被其他实例接管",
			"event", "jobs_leader_lease_taken", "leaseKey", g.leaseKey)
		g.markLost()
		return false
	}
	deadline := g.LeaseUntil().Add(-g.renewSafety)
	if !g.now().UTC().Before(deadline) {
		g.logger.Warn("jobs leader 租约即将到期且续租失败；停止 leader 组件",
			"event", "jobs_leader_lease_expired", "leaseKey", g.leaseKey,
			"leaseUntil", g.LeaseUntil().Format(time.RFC3339Nano))
		g.markLost()
		return false
	}
	return true
}

func (g *Gate) markLost() {
	g.leader.Store(false)
	g.lostOnce.Do(func() { close(g.lost) })
}

func (g *Gate) release() {
	if !g.IsLeader() && !g.IsLost() {
		return
	}
	if !g.leader.Load() && !g.IsLost() {
		return
	}
	g.releaseOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.store.ReleaseLease(ctx, g.leaseKey, g.ownerID); err != nil {
			g.logger.Warn("jobs leader 租约释放失败；等待 TTL 过期",
				"event", "jobs_leader_release_failed", "leaseKey", g.leaseKey, "error", err.Error())
		}
	})
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
