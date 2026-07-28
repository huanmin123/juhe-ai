package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	OAuthRefreshLockTTL      = 90 * time.Second
	OAuthRefreshLockWait     = 30 * time.Second
	OAuthRefreshLockRetry    = 250 * time.Millisecond
	oauthRefreshLockMinTTL   = 75 * time.Millisecond
	oauthRefreshLockKeyspace = "provider-oauth:refresh-locks"
	oauthRefreshReleaseWait  = 5 * time.Second
)

var (
	ErrOAuthRefreshLockBusy = errors.New("OAuth refresh lock is busy")
	ErrOAuthRefreshLockLost = errors.New("OAuth refresh lock ownership lost")
)

const oauthRefreshLockAcquireLua = `
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then
  return 1
end
return 0
`

const oauthRefreshLockRenewLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`

const oauthRefreshLockReleaseLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`

var (
	oauthRefreshLockAcquireScript = goredis.NewScript(oauthRefreshLockAcquireLua)
	oauthRefreshLockRenewScript   = goredis.NewScript(oauthRefreshLockRenewLua)
	oauthRefreshLockReleaseScript = goredis.NewScript(oauthRefreshLockReleaseLua)
)

type OAuthRefreshLockOptions struct {
	TTL            time.Duration
	Wait           time.Duration
	Retry          time.Duration
	FailIfLocked   bool
	OnReleaseError func(error)
}

type OAuthRefreshLockBusyError struct {
	ProviderCode    string
	SourceAccountID string
}

func (e *OAuthRefreshLockBusyError) Error() string {
	return fmt.Sprintf("%s OAuth refresh lock is busy for source account %s", e.ProviderCode, e.SourceAccountID)
}

func (e *OAuthRefreshLockBusyError) Unwrap() error {
	return ErrOAuthRefreshLockBusy
}

type OAuthRefreshLockLostError struct {
	ProviderCode    string
	SourceAccountID string
	Cause           error
}

func (e *OAuthRefreshLockLostError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s OAuth refresh lock ownership was lost for source account %s", e.ProviderCode, e.SourceAccountID)
	}
	return fmt.Sprintf("%s OAuth refresh lock ownership check failed for source account %s: %v", e.ProviderCode, e.SourceAccountID, e.Cause)
}

func (e *OAuthRefreshLockLostError) Unwrap() error {
	if e.Cause != nil {
		return errors.Join(ErrOAuthRefreshLockLost, e.Cause)
	}
	return ErrOAuthRefreshLockLost
}

type OAuthRefreshLock struct {
	ttl            time.Duration
	wait           time.Duration
	retry          time.Duration
	failIfLocked   bool
	onReleaseError func(error)

	key     func(string, string) string
	acquire func(context.Context, string, string, time.Duration) (bool, error)
	renew   func(context.Context, string, string, time.Duration) (bool, error)
	release func(context.Context, string, string) (bool, error)
	token   func() (string, error)
}

type OAuthRefreshLockLease struct {
	providerCode    string
	sourceAccountID string
	key             string
	token           string
	ttl             time.Duration
	renew           func(context.Context, string, string, time.Duration) (bool, error)
	release         func(context.Context, string, string) (bool, error)

	lockCtx    context.Context
	cancelLock context.CancelCauseFunc
	renewCtx   context.Context
	stopRenew  context.CancelFunc
	renewDone  chan struct{}

	opMu          sync.Mutex
	lostOnce      sync.Once
	stopRenewOnce sync.Once
	releaseMu     sync.Mutex
	releaseFinal  bool
	releaseResult bool
}

func (*OAuthRefreshLockLease) String() string   { return "[OAuth refresh lock lease]" }
func (*OAuthRefreshLockLease) GoString() string { return "[OAuth refresh lock lease]" }

type OAuthRefreshLockTask func(
	lockCtx context.Context,
	assertOwned func(context.Context) error,
) error

func NewOAuthRefreshLock(client *Client, options OAuthRefreshLockOptions) (*OAuthRefreshLock, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis state client is required")
	}
	ttl, wait, retry, err := normalizeOAuthRefreshLockOptions(options)
	if err != nil {
		return nil, err
	}
	return &OAuthRefreshLock{
		ttl:            ttl,
		wait:           wait,
		retry:          retry,
		failIfLocked:   options.FailIfLocked,
		onReleaseError: options.OnReleaseError,
		key: func(providerCode, sourceAccountID string) string {
			return client.Key("provider-oauth", "refresh-locks", providerCode, sourceAccountID)
		},
		acquire: func(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
			result, err := oauthRefreshLockAcquireScript.Run(ctx, client.client, []string{key}, token, ttl.Milliseconds()).Int64()
			return result == 1, err
		},
		renew: func(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
			result, err := oauthRefreshLockRenewScript.Run(ctx, client.client, []string{key}, token, ttl.Milliseconds()).Int64()
			return result == 1, err
		},
		release: func(ctx context.Context, key, token string) (bool, error) {
			result, err := oauthRefreshLockReleaseScript.Run(ctx, client.client, []string{key}, token).Int64()
			return result == 1, err
		},
		token: newOAuthRefreshLockToken,
	}, nil
}

func (l *OAuthRefreshLock) Acquire(ctx context.Context, providerCode, sourceAccountID string) (*OAuthRefreshLockLease, error) {
	if l == nil || l.acquire == nil || l.renew == nil || l.release == nil || l.token == nil {
		return nil, fmt.Errorf("OAuth refresh lock is not initialized")
	}
	if ctx == nil {
		return nil, fmt.Errorf("OAuth refresh lock context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	providerCode, err := requiredOAuthRefreshLockPart(providerCode, "provider code")
	if err != nil {
		return nil, err
	}
	sourceAccountID, err = requiredOAuthRefreshLockPart(sourceAccountID, "source account id")
	if err != nil {
		return nil, err
	}
	token, err := l.token()
	if err != nil {
		return nil, fmt.Errorf("generate OAuth refresh lock token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("generated OAuth refresh lock token is empty")
	}
	key := oauthRefreshLockKeyspace + ":" + providerCode + ":" + sourceAccountID
	if l.key != nil {
		key = l.key(providerCode, sourceAccountID)
	}
	deadline := time.Now().Add(l.wait)
	for {
		acquired, acquireErr := l.acquire(ctx, key, token, l.ttl)
		if acquireErr != nil {
			return nil, fmt.Errorf("acquire OAuth refresh lock: %w", acquireErr)
		}
		if acquired {
			return l.newLease(ctx, providerCode, sourceAccountID, key, token), nil
		}
		if l.failIfLocked {
			return nil, &OAuthRefreshLockBusyError{ProviderCode: providerCode, SourceAccountID: sourceAccountID}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, &OAuthRefreshLockBusyError{ProviderCode: providerCode, SourceAccountID: sourceAccountID}
		}
		delay := min(l.retry, remaining)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *OAuthRefreshLock) WithLock(
	ctx context.Context,
	providerCode, sourceAccountID string,
	task OAuthRefreshLockTask,
) (resultErr error) {
	if task == nil {
		return fmt.Errorf("OAuth refresh lock task is required")
	}
	lease, err := l.Acquire(ctx, providerCode, sourceAccountID)
	if err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRefreshReleaseWait)
		defer cancel()
		_, releaseErr := lease.Release(releaseCtx)
		if releaseErr != nil {
			l.reportReleaseError(releaseErr)
		}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}
	resultErr = task(lease.Context(), lease.AssertOwned)
	if cause := context.Cause(lease.Context()); cause != nil && !errors.Is(resultErr, cause) {
		resultErr = errors.Join(resultErr, cause)
	}
	return resultErr
}

func (l *OAuthRefreshLock) reportReleaseError(err error) {
	if l == nil || l.onReleaseError == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	l.onReleaseError(err)
}

func (l *OAuthRefreshLock) newLease(
	ctx context.Context,
	providerCode, sourceAccountID, key, token string,
) *OAuthRefreshLockLease {
	leaseParent := context.WithoutCancel(ctx)
	lockCtx, cancelLock := context.WithCancelCause(leaseParent)
	renewCtx, stopRenew := context.WithCancel(leaseParent)
	lease := &OAuthRefreshLockLease{
		providerCode: providerCode, sourceAccountID: sourceAccountID,
		key: key, token: token, ttl: l.ttl, renew: l.renew, release: l.release,
		lockCtx: lockCtx, cancelLock: cancelLock,
		renewCtx: renewCtx, stopRenew: stopRenew, renewDone: make(chan struct{}),
	}
	go lease.renewLoop()
	return lease
}

func (l *OAuthRefreshLockLease) Context() context.Context {
	if l == nil || l.lockCtx == nil {
		return context.Background()
	}
	return l.lockCtx
}

func (l *OAuthRefreshLockLease) ProviderCode() string {
	if l == nil {
		return ""
	}
	return l.providerCode
}

func (l *OAuthRefreshLockLease) SourceAccountID() string {
	if l == nil {
		return ""
	}
	return l.sourceAccountID
}

func (l *OAuthRefreshLockLease) AssertOwned(ctx context.Context) error {
	return l.Renew(ctx)
}

func (l *OAuthRefreshLockLease) Renew(ctx context.Context) error {
	if l == nil || l.renew == nil {
		return fmt.Errorf("OAuth refresh lock lease is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("OAuth refresh lock context is required")
	}
	if cause := context.Cause(l.lockCtx); cause != nil {
		return cause
	}
	opCtx, cancel := mergeOAuthRefreshLockContexts(ctx, l.renewCtx)
	defer cancel()
	l.opMu.Lock()
	defer l.opMu.Unlock()
	if cause := context.Cause(l.lockCtx); cause != nil {
		return cause
	}
	renewed, err := l.renew(opCtx, l.key, l.token, l.ttl)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if renewErr := l.renewCtx.Err(); renewErr != nil {
			return renewErr
		}
		lost := l.lostError(err)
		l.lose(lost)
		return lost
	}
	if !renewed {
		lost := l.lostError(nil)
		l.lose(lost)
		return lost
	}
	return nil
}

func (l *OAuthRefreshLockLease) Release(ctx context.Context) (bool, error) {
	if l == nil || l.release == nil || l.stopRenew == nil || l.renewDone == nil {
		return false, fmt.Errorf("OAuth refresh lock lease is not initialized")
	}
	if ctx == nil {
		return false, fmt.Errorf("OAuth refresh lock context is required")
	}
	l.stopRenewOnce.Do(func() {
		l.stopRenew()
		l.cancelLock(context.Canceled)
	})
	select {
	case <-l.renewDone:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	l.releaseMu.Lock()
	defer l.releaseMu.Unlock()
	if l.releaseFinal {
		return l.releaseResult, nil
	}
	l.opMu.Lock()
	released, err := l.release(ctx, l.key, l.token)
	l.opMu.Unlock()
	if err != nil {
		return false, err
	}
	l.releaseFinal = true
	l.releaseResult = released
	return released, nil
}

func (l *OAuthRefreshLockLease) renewLoop() {
	defer close(l.renewDone)
	interval := max(25*time.Millisecond, min(l.ttl/3, l.ttl-time.Millisecond))
	lastSuccess := time.Now()
	delay := interval
	for {
		timer := time.NewTimer(delay)
		select {
		case <-l.renewCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}

		l.opMu.Lock()
		renewed, err := l.renew(l.renewCtx, l.key, l.token, l.ttl)
		l.opMu.Unlock()
		if err == nil && renewed {
			lastSuccess = time.Now()
			delay = interval
			continue
		}
		if err == nil {
			l.lose(l.lostError(nil))
			return
		}
		if l.renewCtx.Err() != nil {
			return
		}
		if time.Since(lastSuccess) >= l.ttl {
			l.lose(l.lostError(err))
			return
		}
		delay = min(time.Second, interval)
	}
}

func (l *OAuthRefreshLockLease) lose(err error) {
	l.lostOnce.Do(func() {
		l.cancelLock(err)
		l.stopRenew()
	})
}

func (l *OAuthRefreshLockLease) lostError(cause error) error {
	return &OAuthRefreshLockLostError{
		ProviderCode: l.providerCode, SourceAccountID: l.sourceAccountID, Cause: cause,
	}
}

func mergeOAuthRefreshLockContexts(primary, lease context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(primary)
	stop := context.AfterFunc(lease, cancel)
	if lease.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

func normalizeOAuthRefreshLockOptions(options OAuthRefreshLockOptions) (time.Duration, time.Duration, time.Duration, error) {
	ttl := options.TTL
	if ttl == 0 {
		ttl = OAuthRefreshLockTTL
	}
	wait := options.Wait
	if wait == 0 {
		wait = OAuthRefreshLockWait
	}
	retry := options.Retry
	if retry == 0 {
		retry = OAuthRefreshLockRetry
	}
	if ttl < oauthRefreshLockMinTTL || ttl.Milliseconds() <= 0 {
		return 0, 0, 0, fmt.Errorf("OAuth refresh lock TTL must be at least %s", oauthRefreshLockMinTTL)
	}
	if wait <= 0 {
		return 0, 0, 0, fmt.Errorf("OAuth refresh lock wait must be positive")
	}
	if retry <= 0 {
		return 0, 0, 0, fmt.Errorf("OAuth refresh lock retry must be positive")
	}
	return ttl, wait, retry, nil
}

func requiredOAuthRefreshLockPart(value, label string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" || len(normalized) > 256 || strings.ContainsAny(normalized, ":\r\n\x00") {
		return "", fmt.Errorf("OAuth refresh lock %s is required", label)
	}
	return normalized, nil
}

func newOAuthRefreshLockToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
