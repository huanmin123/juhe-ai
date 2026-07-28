package proberevocationgate

import (
	"context"
	"errors"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestProtectedTablesAreFixedCompleteAndDefensiveCopy(t *testing.T) {
	want := []string{
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
	got := ProtectedTables()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProtectedTables() = %#v, want %#v", got, want)
	}
	got[0] = "changed"
	if ProtectedTables()[0] != want[0] {
		t.Fatal("ProtectedTables returned mutable package state")
	}
	for _, table := range want {
		if !strings.Contains(lockTablesSQL, table) {
			t.Fatalf("lock SQL does not contain %q", table)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(lockTablesSQL), "IN SHARE MODE NOWAIT") {
		t.Fatalf("lock SQL is not fail-fast SHARE mode: %q", lockTablesSQL)
	}
}

func TestProtectLocksBeforeFinalReloadAndCommitsAtWroteRequest(t *testing.T) {
	manager := newFakeTableLockManager()
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, 500*time.Millisecond)
	writerAcquired := make(chan struct{})
	sendMayReturn := make(chan struct{})

	err := guard.Protect(t.Context(), func(ctx context.Context, query Queryer) error {
		if _, err := query.Exec(ctx, "SELECT final_reload"); err != nil {
			return err
		}
		return nil
	}, func(ctx context.Context) error {
		go func() {
			manager.lockExclusive(t.Context())
			close(writerAcquired)
		}()
		select {
		case <-writerAcquired:
			t.Fatal("writer acquired while probe request had not been written")
		case <-time.After(20 * time.Millisecond):
		}
		trace := httptrace.ContextClientTrace(ctx)
		if trace == nil || trace.WroteRequest == nil {
			t.Fatal("request context has no WroteRequest hook")
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		select {
		case <-writerAcquired:
		case <-time.After(time.Second):
			t.Fatal("writer remained blocked after WroteRequest")
		}
		close(sendMayReturn)
		return nil
	})
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	<-sendMayReturn
	manager.unlockExclusive()
	connections := acquirer.snapshot()
	if len(connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(connections))
	}
	tx := connections[0].tx
	if !reflect.DeepEqual(tx.execs, []string{lockTablesSQL, "SELECT final_reload"}) {
		t.Fatalf("transaction SQL order = %#v", tx.execs)
	}
	if tx.commits != 1 || tx.rollbacks != 0 || connections[0].releases != 1 || connections[0].destroys != 0 {
		t.Fatalf("commit/rollback/release/destroy = %d/%d/%d/%d", tx.commits, tx.rollbacks, connections[0].releases, connections[0].destroys)
	}
}

func TestProtectExternalRunsReloadOnlyAfterTableLocks(t *testing.T) {
	manager := newFakeTableLockManager()
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, 500*time.Millisecond)
	called := false
	err := guard.ProtectExternal(t.Context(), func(context.Context) error {
		called = true
		return nil
	}, func(ctx context.Context) error {
		if !called {
			t.Fatal("external reload did not run before send")
		}
		httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{})
		return nil
	})
	if err != nil {
		t.Fatalf("ProtectExternal() error = %v", err)
	}
}

func TestProtectRetriesNOWAITUntilEarlierWriterFinishes(t *testing.T) {
	manager := newFakeTableLockManager()
	manager.lockExclusive(t.Context())
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, time.Second)
	reloadCalled := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- guard.Protect(t.Context(), func(context.Context, Queryer) error {
			close(reloadCalled)
			return nil
		}, func(ctx context.Context) error {
			httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{})
			return nil
		})
	}()
	select {
	case <-reloadCalled:
		t.Fatal("final reload ran before earlier writer released its lock")
	case <-time.After(25 * time.Millisecond):
	}
	manager.unlockExclusive()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Protect() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Protect did not finish after writer released")
	}
	connections := acquirer.snapshot()
	if len(connections) < 2 {
		t.Fatalf("connections = %d, want at least one NOWAIT retry", len(connections))
	}
	for index, connection := range connections[:len(connections)-1] {
		if connection.tx.rollbacks != 1 || connection.releases != 1 {
			t.Fatalf("retry %d rollback/release = %d/%d", index, connection.tx.rollbacks, connection.releases)
		}
	}
}

func TestProtectAllowsConcurrentReadersAndBlocksWriterUntilBothWrite(t *testing.T) {
	manager := newFakeTableLockManager()
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, time.Second)
	entered := make(chan struct{}, 2)
	write := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- guard.Protect(t.Context(), func(context.Context, Queryer) error {
				entered <- struct{}{}
				return nil
			}, func(ctx context.Context) error {
				<-write
				httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{})
				return nil
			})
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("concurrent shared probe did not enter final reload")
		}
	}
	writer := make(chan struct{})
	go func() {
		manager.lockExclusive(t.Context())
		close(writer)
	}()
	select {
	case <-writer:
		t.Fatal("writer acquired while concurrent probes held SHARE locks")
	case <-time.After(20 * time.Millisecond):
	}
	close(write)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Protect() error = %v", err)
		}
	}
	select {
	case <-writer:
	case <-time.After(time.Second):
		t.Fatal("writer remained blocked after both probes committed")
	}
	manager.unlockExclusive()
}

func TestProtectRollsBackAllPreWriteExitPaths(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Guard) error
	}{
		{name: "final reload failure", run: func(guard *Guard) error {
			return guard.Protect(t.Context(), func(context.Context, Queryer) error { return errors.New("stale") }, func(context.Context) error { return nil })
		}},
		{name: "send failure", run: func(guard *Guard) error {
			return guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(context.Context) error { return errors.New("dial") })
		}},
		{name: "wrote request failure", run: func(guard *Guard) error {
			return guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
				httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("write")})
				return errors.New("write")
			})
		}},
		{name: "hold timeout", run: func(_ *Guard) error { return nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acquirer := &fakeAcquirer{manager: newFakeTableLockManager()}
			hold := 200 * time.Millisecond
			if test.name == "hold timeout" {
				hold = 10 * time.Millisecond
			}
			guard := mustTestGuard(t, acquirer, hold)
			var err error
			if test.name == "hold timeout" {
				err = guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
					<-ctx.Done()
					return context.Cause(ctx)
				})
			} else {
				err = test.run(guard)
			}
			if err == nil {
				t.Fatal("Protect() error = nil")
			}
			connection := acquirer.snapshot()[0]
			if connection.tx.commits != 0 || connection.tx.rollbacks != 1 || connection.releases != 1 || connection.destroys != 0 {
				t.Fatalf("commit/rollback/release/destroy = %d/%d/%d/%d", connection.tx.commits, connection.tx.rollbacks, connection.releases, connection.destroys)
			}
		})
	}
}

func TestFinalReloadIgnoringCancellationKeepsWriterBlockedUntilItReturns(t *testing.T) {
	manager := newFakeTableLockManager()
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, 10*time.Millisecond)
	writerAcquired := make(chan struct{})
	deadlineObserved := make(chan struct{})
	allowReloadReturn := make(chan struct{})
	sendCalled := false
	done := make(chan error, 1)
	go func() {
		done <- guard.Protect(t.Context(), func(ctx context.Context, _ Queryer) error {
			go func() {
				manager.lockExclusive(t.Context())
				close(writerAcquired)
			}()
			<-ctx.Done()
			close(deadlineObserved)
			<-allowReloadReturn
			return nil
		}, func(context.Context) error {
			sendCalled = true
			return nil
		})
	}()
	select {
	case <-deadlineObserved:
	case <-time.After(time.Second):
		t.Fatal("final reload did not observe the hold deadline")
	}
	select {
	case <-writerAcquired:
		t.Fatal("writer acquired before canceled final reload returned")
	case <-time.After(20 * time.Millisecond):
	}
	close(allowReloadReturn)
	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Protect() error = %v, want deadline exceeded", err)
	}
	if sendCalled {
		t.Fatal("send ran after the hold deadline")
	}
	select {
	case <-writerAcquired:
	case <-time.After(time.Second):
		t.Fatal("writer remained blocked after final reload returned and gate rolled back")
	}
	manager.unlockExclusive()
	connection := acquirer.snapshot()[0]
	if connection.tx.rollbacks != 1 || connection.releases != 1 {
		t.Fatalf("rollback/release = %d/%d", connection.tx.rollbacks, connection.releases)
	}
}

func TestSendIgnoringCancellationKeepsWriterBlockedUntilItReturns(t *testing.T) {
	manager := newFakeTableLockManager()
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, 10*time.Millisecond)
	writerAcquired := make(chan struct{})
	deadlineObserved := make(chan struct{})
	allowSendReturn := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
			go func() {
				manager.lockExclusive(t.Context())
				close(writerAcquired)
			}()
			<-ctx.Done()
			close(deadlineObserved)
			<-allowSendReturn
			return context.Cause(ctx)
		})
	}()
	select {
	case <-deadlineObserved:
	case <-time.After(time.Second):
		t.Fatal("send did not observe the hold deadline")
	}
	select {
	case <-writerAcquired:
		t.Fatal("writer acquired before canceled send returned")
	case <-time.After(20 * time.Millisecond):
	}
	close(allowSendReturn)
	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Protect() error = %v, want deadline exceeded", err)
	}
	select {
	case <-writerAcquired:
	case <-time.After(time.Second):
		t.Fatal("writer remained blocked after send returned and gate rolled back")
	}
	manager.unlockExclusive()
	connection := acquirer.snapshot()[0]
	if connection.tx.rollbacks != 1 || connection.releases != 1 {
		t.Fatalf("rollback/release = %d/%d", connection.tx.rollbacks, connection.releases)
	}
}

func TestProtectFailsClosedWithoutSuccessfulWroteRequest(t *testing.T) {
	acquirer := &fakeAcquirer{manager: newFakeTableLockManager()}
	guard := mustTestGuard(t, acquirer, time.Second)
	err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(context.Context) error { return nil })
	if !errors.Is(err, ErrNoWriteEvidence) {
		t.Fatalf("Protect() error = %v, want ErrNoWriteEvidence", err)
	}
	connection := acquirer.snapshot()[0]
	if connection.tx.commits != 0 || connection.tx.rollbacks != 1 || connection.releases != 1 {
		t.Fatalf("commit/rollback/release = %d/%d/%d", connection.tx.commits, connection.tx.rollbacks, connection.releases)
	}
}

func TestWroteRequestHooksReleaseOnceAndRejectWriteAfterSuccess(t *testing.T) {
	acquirer := &fakeAcquirer{manager: newFakeTableLockManager()}
	guard := mustTestGuard(t, acquirer, time.Second)
	err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
		trace := httptrace.ContextClientTrace(ctx)
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("stale connection")})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil
	})
	if !errors.Is(err, ErrMultipleWrites) {
		t.Fatalf("Protect() error = %v, want ErrMultipleWrites", err)
	}
	connection := acquirer.snapshot()[0]
	if connection.tx.commits != 1 || connection.tx.rollbacks != 0 || connection.releases != 1 || connection.destroys != 0 {
		t.Fatalf("commit/rollback/release/destroy = %d/%d/%d/%d", connection.tx.commits, connection.tx.rollbacks, connection.releases, connection.destroys)
	}
}

func TestFailedWriteHookThenTransportRetrySuccessStaysProtected(t *testing.T) {
	manager := newFakeTableLockManager()
	acquirer := &fakeAcquirer{manager: manager}
	guard := mustTestGuard(t, acquirer, time.Second)
	writerAcquired := make(chan struct{})
	err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
		trace := httptrace.ContextClientTrace(ctx)
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("reused connection was stale")})
		go func() {
			manager.lockExclusive(t.Context())
			close(writerAcquired)
		}()
		select {
		case <-writerAcquired:
			t.Fatal("failed write hook released the gate before transport retry")
		case <-time.After(20 * time.Millisecond):
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		select {
		case <-writerAcquired:
		case <-time.After(time.Second):
			t.Fatal("writer remained blocked after retry wrote the request")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	manager.unlockExclusive()
	connection := acquirer.snapshot()[0]
	if connection.tx.commits != 1 || connection.tx.rollbacks != 0 || connection.releases != 1 {
		t.Fatalf("commit/rollback/release = %d/%d/%d", connection.tx.commits, connection.tx.rollbacks, connection.releases)
	}
}

func TestHoldDeadlineDoesNotCancelResponseAfterWroteRequest(t *testing.T) {
	acquirer := &fakeAcquirer{manager: newFakeTableLockManager()}
	guard := mustTestGuard(t, acquirer, 10*time.Millisecond)
	err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
		httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{})
		select {
		case <-ctx.Done():
			return errors.New("gate deadline leaked into response context")
		case <-time.After(30 * time.Millisecond):
			return nil
		}
	})
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	connection := acquirer.snapshot()[0]
	if connection.tx.commits != 1 || connection.tx.rollbacks != 0 || connection.releases != 1 {
		t.Fatalf("commit/rollback/release = %d/%d/%d", connection.tx.commits, connection.tx.rollbacks, connection.releases)
	}
}

func TestProtectRollsBackBeforeRethrowingPanic(t *testing.T) {
	acquirer := &fakeAcquirer{manager: newFakeTableLockManager()}
	guard := mustTestGuard(t, acquirer, time.Second)
	defer func() {
		if recover() != "boom" {
			t.Fatal("Protect did not rethrow callback panic")
		}
		connection := acquirer.snapshot()[0]
		if connection.tx.rollbacks != 1 || connection.releases != 1 {
			t.Fatalf("rollback/release = %d/%d", connection.tx.rollbacks, connection.releases)
		}
	}()
	_ = guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(context.Context) error {
		panic("boom")
	})
}

func TestProtectDestroysConnectionWhenCommitOrRollbackIsUncertain(t *testing.T) {
	tests := []struct {
		name        string
		commitErr   error
		rollbackErr error
		write       bool
	}{
		{name: "commit", commitErr: errors.New("commit failed"), write: true},
		{name: "rollback", rollbackErr: errors.New("rollback failed")},
		{name: "closed transaction is uncertain", rollbackErr: pgx.ErrTxClosed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acquirer := &fakeAcquirer{manager: newFakeTableLockManager(), commitErr: test.commitErr, rollbackErr: test.rollbackErr}
			guard := mustTestGuard(t, acquirer, time.Second)
			err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(ctx context.Context) error {
				if test.write {
					httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{})
				}
				return nil
			})
			if !errors.Is(err, ErrRelease) {
				t.Fatalf("Protect() error = %v, want ErrRelease", err)
			}
			connection := acquirer.snapshot()[0]
			if connection.releases != 0 || connection.destroys != 1 {
				t.Fatalf("release/destroy = %d/%d", connection.releases, connection.destroys)
			}
		})
	}
}

func TestProtectRejectsNonRetryableLockFailure(t *testing.T) {
	acquirer := &fakeAcquirer{manager: newFakeTableLockManager(), lockErr: &pgconn.PgError{Code: "42501", Message: "denied"}}
	guard := mustTestGuard(t, acquirer, time.Second)
	err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(context.Context) error { return nil })
	if !errors.Is(err, ErrLock) {
		t.Fatalf("Protect() error = %v, want ErrLock", err)
	}
	connection := acquirer.snapshot()[0]
	if connection.tx.rollbacks != 1 || connection.releases != 1 {
		t.Fatalf("rollback/release = %d/%d", connection.tx.rollbacks, connection.releases)
	}
}

func TestBeginFailureDestroysConnectionAndJoinsDestroyFailure(t *testing.T) {
	beginErr := errors.New("begin failed")
	destroyErr := errors.New("destroy failed")
	acquirer := &fakeAcquirer{
		manager: newFakeTableLockManager(), beginErr: beginErr, destroyErr: destroyErr,
	}
	guard := mustTestGuard(t, acquirer, time.Second)
	err := guard.Protect(t.Context(), func(context.Context, Queryer) error { return nil }, func(context.Context) error { return nil })
	if !errors.Is(err, ErrLock) || !errors.Is(err, beginErr) || !errors.Is(err, destroyErr) {
		t.Fatalf("Protect() error = %v, want joined lock/begin/destroy errors", err)
	}
	connection := acquirer.snapshot()[0]
	if connection.releases != 0 || connection.destroys != 1 {
		t.Fatalf("release/destroy = %d/%d", connection.releases, connection.destroys)
	}
}

func mustTestGuard(t *testing.T, acquirer connectionAcquirer, hold time.Duration) *Guard {
	t.Helper()
	guard, err := newGuard(acquirer, Options{
		HoldTimeout: hold, ReleaseTimeout: 100 * time.Millisecond,
		RetryMinDelay: time.Millisecond, RetryMaxDelay: min(5*time.Millisecond, hold),
	})
	if err != nil {
		t.Fatalf("newGuard() error = %v", err)
	}
	return guard
}

type fakeAcquirer struct {
	mu          sync.Mutex
	manager     *fakeTableLockManager
	connections []*fakeConnection
	commitErr   error
	rollbackErr error
	lockErr     error
	beginErr    error
	destroyErr  error
}

func (a *fakeAcquirer) Acquire(context.Context) (dedicatedConnection, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	connection := &fakeConnection{manager: a.manager, beginErr: a.beginErr, destroyErr: a.destroyErr}
	connection.tx = &fakeTransaction{
		manager: a.manager, connection: connection, commitErr: a.commitErr,
		rollbackErr: a.rollbackErr, lockErr: a.lockErr,
	}
	a.connections = append(a.connections, connection)
	return connection, nil
}

func (a *fakeAcquirer) snapshot() []*fakeConnection {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*fakeConnection(nil), a.connections...)
}

type fakeConnection struct {
	mu         sync.Mutex
	manager    *fakeTableLockManager
	tx         *fakeTransaction
	releases   int
	destroys   int
	beginErr   error
	destroyErr error
}

func (c *fakeConnection) Begin(context.Context) (transaction, error) {
	if c.beginErr != nil {
		return nil, c.beginErr
	}
	return c.tx, nil
}

func (c *fakeConnection) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases++
}

func (c *fakeConnection) Destroy(time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.destroys++
	c.tx.forceClose()
	return c.destroyErr
}

type fakeTransaction struct {
	mu          sync.Mutex
	manager     *fakeTableLockManager
	connection  *fakeConnection
	execs       []string
	locked      bool
	closed      bool
	commits     int
	rollbacks   int
	commitErr   error
	rollbackErr error
	lockErr     error
}

func (tx *fakeTransaction) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.execs = append(tx.execs, sql)
	if sql != lockTablesSQL {
		return pgconn.NewCommandTag("SELECT 1"), nil
	}
	if tx.lockErr != nil {
		return pgconn.CommandTag{}, tx.lockErr
	}
	if !tx.manager.tryLockShared() {
		return pgconn.CommandTag{}, &pgconn.PgError{Code: "55P03", Message: "lock not available"}
	}
	tx.locked = true
	return pgconn.NewCommandTag("LOCK TABLE"), nil
}

func (*fakeTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (*fakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row        { return fakeRow{} }

func (tx *fakeTransaction) Commit(context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.commits++
	if tx.commitErr != nil {
		return tx.commitErr
	}
	tx.closeLocked()
	return nil
}

func (tx *fakeTransaction) Rollback(context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.rollbacks++
	if tx.rollbackErr != nil {
		return tx.rollbackErr
	}
	tx.closeLocked()
	return nil
}

func (tx *fakeTransaction) forceClose() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.closeLocked()
}

func (tx *fakeTransaction) closeLocked() {
	if tx.closed {
		return
	}
	tx.closed = true
	if tx.locked {
		tx.manager.unlockShared()
		tx.locked = false
	}
}

type fakeRow struct{}

func (fakeRow) Scan(...any) error { return pgx.ErrNoRows }

type fakeTableLockManager struct {
	mu      sync.Mutex
	readers int
	writer  bool
	changed chan struct{}
}

func newFakeTableLockManager() *fakeTableLockManager {
	return &fakeTableLockManager{changed: make(chan struct{})}
}

func (m *fakeTableLockManager) tryLockShared() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writer {
		return false
	}
	m.readers++
	return true
}

func (m *fakeTableLockManager) unlockShared() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readers--
	m.notifyLocked()
}

func (m *fakeTableLockManager) lockExclusive(ctx context.Context) {
	for {
		m.mu.Lock()
		if !m.writer && m.readers == 0 {
			m.writer = true
			m.mu.Unlock()
			return
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}

func (m *fakeTableLockManager) unlockExclusive() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writer = false
	m.notifyLocked()
}

func (m *fakeTableLockManager) notifyLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

var _ connectionAcquirer = (*fakeAcquirer)(nil)
