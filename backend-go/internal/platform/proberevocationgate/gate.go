// Package proberevocationgate prevents a probe request from racing with
// PostgreSQL-backed account, authorization, model, proxy, or credential
// changes between its final reload and the request write.
package proberevocationgate

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultHoldTimeout    = 30 * time.Second
	DefaultReleaseTimeout = 2 * time.Second
	DefaultRetryMinDelay  = 5 * time.Millisecond
	DefaultRetryMaxDelay  = 100 * time.Millisecond
	MaxHoldTimeout        = 2 * time.Minute
	MaxReleaseTimeout     = 30 * time.Second
)

var (
	ErrInvalidOptions  = errors.New("probe revocation gate options are invalid")
	ErrLock            = errors.New("probe revocation gate lock failed")
	ErrFinalReload     = errors.New("probe revocation gate final reload failed")
	ErrRelease         = errors.New("probe revocation gate release failed")
	ErrNoWriteEvidence = errors.New("probe revocation gate request returned without successful write evidence")
	ErrMultipleWrites  = errors.New("probe revocation gate observed multiple request write attempts")
)

// protectedTables is deliberately fixed. Every PostgreSQL relation read by
// the cooldown probe's final candidate reload, credential selection, model
// mapping, and proxy hydration must be present here. A missing relation fails
// closed instead of silently weakening the gate.
var protectedTables = [...]string{
	"juhe_business.account_api_key_runtime_states",
	"juhe_business.account_model_mappings",
	"juhe_business.account_supported_models",
	"juhe_business.accounts",
	"juhe_business.group_accounts",
	"juhe_business.group_authorization_settings",
	"juhe_business.groups",
	"juhe_business.provider_protocol_profiles",
	"juhe_business.proxy_profiles",
	"juhe_business.resource_authorizations",
}

const lockTablesSQL = `LOCK TABLE
  juhe_business.account_api_key_runtime_states,
  juhe_business.account_model_mappings,
  juhe_business.account_supported_models,
  juhe_business.accounts,
  juhe_business.group_accounts,
  juhe_business.group_authorization_settings,
  juhe_business.groups,
  juhe_business.provider_protocol_profiles,
  juhe_business.proxy_profiles,
  juhe_business.resource_authorizations
IN SHARE MODE NOWAIT`

// ProtectedTables returns a copy so production validation can compare the
// fixed gate surface with the final-reload query dependencies.
func ProtectedTables() []string {
	result := make([]string, len(protectedTables))
	copy(result, protectedTables[:])
	return result
}

// Queryer is the transaction-bound database surface available to the final
// reload. Callers must use it instead of a pool so every reload query executes
// on the same transaction and connection that owns the table locks.
type Queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type FinalReload func(context.Context, Queryer) error
type ExternalFinalReload func(context.Context) error
type SendRequest func(context.Context) error

// Protector is the narrow interface consumed by a probe transport adapter.
type Protector interface {
	Protect(context.Context, FinalReload, SendRequest) error
}

type Options struct {
	// HoldTimeout bounds lock acquisition and cancels final reload or request
	// writing. The lock is not released until a synchronous callback returns;
	// this preserves strict ordering if cancellation takes time to stop I/O.
	HoldTimeout time.Duration
	// ReleaseTimeout bounds commit or rollback after request cancellation.
	ReleaseTimeout time.Duration
	RetryMinDelay  time.Duration
	RetryMaxDelay  time.Duration
}

type Guard struct {
	acquirer       connectionAcquirer
	holdTimeout    time.Duration
	releaseTimeout time.Duration
	retryMinDelay  time.Duration
	retryMaxDelay  time.Duration
}

func New(pool *pgxpool.Pool, options Options) (*Guard, error) {
	if pool == nil {
		return nil, fmt.Errorf("%w: PostgreSQL pool is required", ErrInvalidOptions)
	}
	return newGuard(pgxPoolAcquirer{pool: pool}, options)
}

func newGuard(acquirer connectionAcquirer, options Options) (*Guard, error) {
	if acquirer == nil {
		return nil, fmt.Errorf("%w: connection acquirer is required", ErrInvalidOptions)
	}
	holdTimeout := options.HoldTimeout
	if holdTimeout == 0 {
		holdTimeout = DefaultHoldTimeout
	}
	releaseTimeout := options.ReleaseTimeout
	if releaseTimeout == 0 {
		releaseTimeout = DefaultReleaseTimeout
	}
	retryMinDelay := options.RetryMinDelay
	if retryMinDelay == 0 {
		retryMinDelay = DefaultRetryMinDelay
	}
	retryMaxDelay := options.RetryMaxDelay
	if retryMaxDelay == 0 {
		retryMaxDelay = DefaultRetryMaxDelay
	}
	if holdTimeout < time.Millisecond || holdTimeout > MaxHoldTimeout ||
		releaseTimeout < time.Millisecond || releaseTimeout > MaxReleaseTimeout ||
		retryMinDelay < 0 || retryMaxDelay < retryMinDelay || retryMaxDelay > holdTimeout {
		return nil, fmt.Errorf("%w: timeout or retry bounds are invalid", ErrInvalidOptions)
	}
	return &Guard{
		acquirer: acquirer, holdTimeout: holdTimeout, releaseTimeout: releaseTimeout,
		retryMinDelay: retryMinDelay, retryMaxDelay: retryMaxDelay,
	}, nil
}

// Protect obtains all dependency table locks before finalReload performs its
// first query. A successful WroteRequest trace commits immediately; any other
// path rolls back after the synchronous callback returns. send must perform one
// non-redirected, non-retried attempt, honor its context, and return only after
// it can no longer write request bytes. Panic is rethrown after rollback.
func (g *Guard) Protect(ctx context.Context, finalReload FinalReload, send SendRequest) (returnErr error) {
	if g == nil || g.acquirer == nil || ctx == nil || finalReload == nil || send == nil {
		return fmt.Errorf("%w: guard, context, final reload, and send callback are required", ErrInvalidOptions)
	}
	gateCtx, cancelGate := context.WithTimeout(ctx, g.holdTimeout)
	defer cancelGate()

	lease, err := g.acquireLockedTransaction(gateCtx)
	if err != nil {
		return err
	}
	defer func() {
		finishErr := lease.finish(false)
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
		returnErr = errors.Join(returnErr, finishErr)
	}()

	sendCtx, cancelSend := context.WithCancelCause(ctx)
	defer cancelSend(context.Canceled)
	go func() {
		select {
		case <-gateCtx.Done():
			cancelSend(context.Cause(gateCtx))
		case <-lease.done:
		}
	}()

	if err := finalReload(gateCtx, lease.tx); err != nil {
		return fmt.Errorf("%w: %w", ErrFinalReload, err)
	}
	if err := context.Cause(gateCtx); err != nil {
		return err
	}
	if !lease.startSend() {
		return errors.Join(ErrLock, context.Cause(gateCtx))
	}

	writes := &writeEvidence{}
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if writes.observe(info.Err == nil) {
			_ = lease.finish(true)
		}
	}}
	sendErr := send(httptrace.WithClientTrace(sendCtx, trace))
	successfulWrite, multipleWrites := writes.result()
	if multipleWrites {
		return errors.Join(sendErr, ErrMultipleWrites)
	}
	if !successfulWrite && sendErr == nil {
		return errors.Join(ErrNoWriteEvidence, context.Cause(gateCtx))
	}
	return sendErr
}

// ProtectExternal is the integration form for an existing pool-backed reader.
// finalReload must issue only fresh autocommit READ COMMITTED statements after
// this method acquires the locks; it must not reuse an earlier transaction or
// snapshot. Its pool must reserve at least one additional reload connection per
// concurrent gate (or be a separately bounded pool), otherwise probes can hold
// every gate connection while deadlocking on pool acquisition. Correctness also
// requires a static audit that ProtectedTables contains every relation read by
// finalReload. Redis and relations absent from that list are not protected.
func (g *Guard) ProtectExternal(ctx context.Context, finalReload ExternalFinalReload, send SendRequest) error {
	if finalReload == nil {
		return fmt.Errorf("%w: external final reload is required", ErrInvalidOptions)
	}
	return g.Protect(ctx, func(reloadCtx context.Context, _ Queryer) error {
		return finalReload(reloadCtx)
	}, send)
}

func (g *Guard) acquireLockedTransaction(ctx context.Context) (*transactionLease, error) {
	var lastLockErr error
	for attempt := 0; ; attempt++ {
		if err := context.Cause(ctx); err != nil {
			return nil, errors.Join(ErrLock, lastLockErr, err)
		}
		connection, err := g.acquirer.Acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: acquire dedicated PostgreSQL connection: %w", ErrLock, err)
		}
		tx, err := connection.Begin(ctx)
		if err != nil {
			destroyErr := connection.Destroy(g.releaseTimeout)
			return nil, errors.Join(fmt.Errorf("%w: begin lock transaction: %w", ErrLock, err), destroyErr)
		}
		lease := &transactionLease{tx: tx, connection: connection, releaseTimeout: g.releaseTimeout, done: make(chan struct{})}
		if _, err = tx.Exec(ctx, lockTablesSQL); err == nil {
			return lease, nil
		}
		lastLockErr = err
		rollbackErr := lease.finish(false)
		if !isLockUnavailable(err) {
			return nil, errors.Join(fmt.Errorf("%w: lock dependency tables: %w", ErrLock, err), rollbackErr)
		}
		if rollbackErr != nil {
			return nil, errors.Join(fmt.Errorf("%w: lock dependency tables: %w", ErrLock, err), rollbackErr)
		}
		delay := retryDelay(attempt, g.retryMinDelay, g.retryMaxDelay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, errors.Join(ErrLock, lastLockErr, context.Cause(ctx))
		case <-timer.C:
		}
	}
}

func retryDelay(attempt int, minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	capDelay := minimum
	for range min(attempt, 16) {
		if capDelay >= maximum/2 {
			capDelay = maximum
			break
		}
		capDelay *= 2
	}
	if capDelay > maximum {
		capDelay = maximum
	}
	if capDelay <= minimum {
		return minimum
	}
	return minimum + time.Duration(rand.Int64N(int64(capDelay-minimum)+1))
}

func isLockUnavailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && strings.TrimSpace(pgErr.Code) == "55P03"
}

type transactionLease struct {
	tx             transaction
	connection     dedicatedConnection
	releaseTimeout time.Duration
	once           sync.Once
	stateMu        sync.Mutex
	finished       bool
	done           chan struct{}
	err            error
}

type writeEvidence struct {
	mu                sync.Mutex
	successfulWrite   bool
	writeAfterSuccess bool
}

func (e *writeEvidence) observe(success bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.successfulWrite {
		e.writeAfterSuccess = true
		return false
	}
	if !success {
		return false
	}
	e.successfulWrite = true
	return true
}

func (e *writeEvidence) result() (successful, multiple bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.successfulWrite, e.writeAfterSuccess
}

func (l *transactionLease) startSend() bool {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return !l.finished
}

func (l *transactionLease) finish(commit bool) error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		defer close(l.done)
		l.stateMu.Lock()
		l.finished = true
		l.stateMu.Unlock()
		releaseCtx, cancel := context.WithTimeout(context.Background(), l.releaseTimeout)
		defer cancel()
		if commit {
			l.err = l.tx.Commit(releaseCtx)
		} else {
			l.err = l.tx.Rollback(releaseCtx)
		}
		if l.err == nil {
			l.connection.Release()
			return
		}
		destroyErr := l.connection.Destroy(l.releaseTimeout)
		l.err = errors.Join(fmt.Errorf("%w: finish lock transaction: %w", ErrRelease, l.err), destroyErr)
	})
	<-l.done
	return l.err
}

type transaction interface {
	Queryer
	Commit(context.Context) error
	Rollback(context.Context) error
}

type dedicatedConnection interface {
	Begin(context.Context) (transaction, error)
	Release()
	Destroy(time.Duration) error
}

type connectionAcquirer interface {
	Acquire(context.Context) (dedicatedConnection, error)
}

type pgxPoolAcquirer struct{ pool *pgxpool.Pool }

func (a pgxPoolAcquirer) Acquire(ctx context.Context) (dedicatedConnection, error) {
	connection, err := a.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pgxPoolConnection{connection: connection}, nil
}

type pgxPoolConnection struct{ connection *pgxpool.Conn }

func (c *pgxPoolConnection) Begin(ctx context.Context) (transaction, error) {
	return c.connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
}

func (c *pgxPoolConnection) Release() { c.connection.Release() }

func (c *pgxPoolConnection) Destroy(timeout time.Duration) error {
	connection := c.connection.Hijack()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return connection.Close(ctx)
}

var _ Protector = (*Guard)(nil)
