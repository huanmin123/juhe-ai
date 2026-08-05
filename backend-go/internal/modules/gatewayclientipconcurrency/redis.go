package gatewayclientipconcurrency

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
	platformredis "juhe-ai/backend-go/internal/platform/redis"
)

type RedisStore interface {
	Acquire(context.Context, string, int, bool, time.Time, string) (platformredis.ClientIPSlotAcquireResult, error)
	Renew(context.Context, string, time.Time, string) (bool, error)
	Release(context.Context, string, string) error
	Enqueue(context.Context, string, string, time.Time, time.Time, int) (platformredis.ClientIPQueueEnqueueResult, error)
	Position(context.Context, string, string, time.Time) (platformredis.ClientIPQueuePosition, error)
	Remove(context.Context, string, string, time.Time) (int, error)
}

type RedisServiceOptions struct {
	Store             RedisStore
	Now               func() time.Time
	NewToken          func() string
	PollInterval      time.Duration
	RenewInterval     time.Duration
	ReleaseDelays     []time.Duration
	OnBackgroundError func(error)
}

// RedisService mirrors Node's Redis client-IP driver. It is still unregistered:
// a future request owner selects this driver and owns response disposition.
type RedisService struct {
	store                       RedisStore
	now                         func() time.Time
	newToken                    func() string
	pollInterval, renewInterval time.Duration
	releaseDelays               []time.Duration
	report                      func(error)
}

func NewRedisService(options RedisServiceOptions) (*RedisService, error) {
	if options.Store == nil || options.NewToken == nil || options.OnBackgroundError == nil {
		return nil, fmt.Errorf("client IP Redis service dependencies are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.PollInterval <= 0 || options.RenewInterval <= 0 {
		return nil, fmt.Errorf("client IP Redis intervals must be positive")
	}
	if len(options.ReleaseDelays) == 0 {
		options.ReleaseDelays = []time.Duration{0, 250 * time.Millisecond, time.Second, 5 * time.Second}
	}
	for _, delay := range options.ReleaseDelays {
		if delay < 0 {
			return nil, fmt.Errorf("client IP Redis release delay is invalid")
		}
	}
	return &RedisService{store: options.Store, now: options.Now, newToken: options.NewToken, pollInterval: options.PollInterval, renewInterval: options.RenewInterval, releaseDelays: append([]time.Duration(nil), options.ReleaseDelays...), report: options.OnBackgroundError}, nil
}

func (s *RedisService) Acquire(ctx context.Context, input Input) (Decision, error) {
	if s == nil || s.store == nil || s.now == nil || s.newToken == nil || s.report == nil {
		return Decision{}, fmt.Errorf("client IP Redis service is not configured")
	}
	if ctx == nil {
		return Decision{}, fmt.Errorf("client IP Redis context is required")
	}
	policy, err := normalizedPolicy(input.Policy)
	if err != nil {
		return Decision{}, err
	}
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" || policy.ClientIPConcurrencyLimit <= 0 {
		return Decision{Acquired: true}, nil
	}
	if ctx.Err() != nil {
		return rejected(RejectAborted, 0, policy.ClientIPConcurrencyLimit, 0, 0), nil
	}
	scope, err := scopeKey(input, clientIP)
	if err != nil {
		return Decision{}, err
	}
	started := s.now().UTC()
	initialToken := s.newToken()
	first, err := s.store.Acquire(ctx, scope, policy.ClientIPConcurrencyLimit, true, started, initialToken)
	if err != nil {
		return Decision{}, fmt.Errorf("acquire initial client IP Redis slot: %w", err)
	}
	if first.Acquired {
		return s.acquired(scope, initialToken, first.Current, policy, 0, false, 0), nil
	}
	if policy.ClientIPConcurrencyOverflowMode != "queue" {
		return rejected(RejectLimitReached, first.Current, policy.ClientIPConcurrencyLimit, 0, 0), nil
	}
	if policy.MaxQueueWaitMs <= 0 {
		return rejected(RejectQueueDisabled, first.Current, policy.ClientIPConcurrencyLimit, 0, 0), nil
	}
	deadline := started.Add(time.Duration(policy.MaxQueueWaitMs) * time.Millisecond)
	itemID, queuedToken := s.newToken(), s.newToken()
	enqueued, err := s.store.Enqueue(ctx, scope, itemID, deadline, started, policy.PerAPIKeyQueueLimit)
	if err != nil {
		return Decision{}, fmt.Errorf("enqueue client IP Redis queue: %w", err)
	}
	if !enqueued.Enqueued {
		return rejected(RejectQueueFull, first.Current, policy.ClientIPConcurrencyLimit, 0, enqueued.QueueSize), nil
	}
	current := first.Current
	for s.now().Before(deadline) {
		now := s.now().UTC()
		if ctx.Err() != nil {
			size, removeErr := s.store.Remove(context.Background(), scope, itemID, now)
			if removeErr != nil {
				return Decision{}, fmt.Errorf("remove aborted client IP Redis queue item: %w", removeErr)
			}
			return rejected(RejectAborted, current, policy.ClientIPConcurrencyLimit, elapsedMS(now, started), size), nil
		}
		position, positionErr := s.store.Position(ctx, scope, itemID, now)
		if positionErr != nil {
			return Decision{}, fmt.Errorf("read client IP Redis queue position: %w", positionErr)
		}
		if !position.Present {
			return rejected(RejectTimeout, current, policy.ClientIPConcurrencyLimit, elapsedMS(now, started), position.QueueSize), nil
		}
		if position.Rank == 0 {
			attempt, attemptErr := s.store.Acquire(ctx, scope, policy.ClientIPConcurrencyLimit, false, now, queuedToken)
			if attemptErr != nil {
				return Decision{}, fmt.Errorf("acquire queued client IP Redis slot: %w", attemptErr)
			}
			current = attempt.Current
			if attempt.Acquired {
				size, removeErr := s.store.Remove(ctx, scope, itemID, s.now().UTC())
				if removeErr != nil {
					s.releaseAsync(scope, queuedToken)
					return Decision{}, fmt.Errorf("remove acquired client IP Redis queue item: %w", removeErr)
				}
				return s.acquired(scope, queuedToken, attempt.Current, policy, elapsedMS(s.now(), started), true, size+1), nil
			}
		}
		if err := sleepContext(ctx, minDuration(s.pollInterval, time.Until(deadline))); err != nil {
			continue
		}
	}
	now := s.now().UTC()
	size, err := s.store.Remove(context.Background(), scope, itemID, now)
	if err != nil {
		return Decision{}, fmt.Errorf("remove timed out client IP Redis queue item: %w", err)
	}
	return rejected(RejectTimeout, current, policy.ClientIPConcurrencyLimit, elapsedMS(now, started), size), nil
}

func (s *RedisService) acquired(scope, token string, current int, policy groupscheduling.Policy, waitedMS int, queued bool, queueSize int) Decision {
	lease := &Lease{key: scope, policy: clientIPPolicyFingerprint(policy), issued: true, release: func() { s.releaseAsync(scope, token) }}
	s.startRenewal(scope, token, lease)
	return Decision{Enabled: true, Acquired: true, Current: current, Limit: policy.ClientIPConcurrencyLimit, WaitedMS: waitedMS, Queued: queued, QueueSizeBeforeAcquire: queueSize, Lease: lease}
}

func (s *RedisService) startRenewal(scope, token string, lease *Lease) {
	stop := make(chan struct{})
	previous := lease.release
	lease.release = func() { close(stop); previous() }
	go func() {
		ticker := time.NewTicker(s.renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewed, err := s.store.Renew(context.Background(), scope, s.now().UTC(), token)
				if err != nil {
					s.report(fmt.Errorf("renew client IP Redis slot: %w", err))
					continue
				}
				if !renewed {
					return
				}
			}
		}
	}()
}

func (s *RedisService) releaseAsync(scope, token string) {
	go func() {
		for _, delay := range s.releaseDelays {
			if delay > 0 {
				time.Sleep(delay)
			}
			if err := s.store.Release(context.Background(), scope, token); err == nil {
				return
			} else {
				s.report(fmt.Errorf("release client IP Redis slot: %w", err))
			}
		}
	}()
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func minDuration(left, right time.Duration) time.Duration {
	if right < left {
		return right
	}
	return left
}
