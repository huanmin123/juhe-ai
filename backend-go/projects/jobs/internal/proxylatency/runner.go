package proxylatency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
)

var (
	ErrManualProxyMissing    = errors.New("J3a manual proxy missing or deleted")
	ErrManualProjectionStale = errors.New("J3a manual projection stale")
)

type inputLoader interface {
	LoadDue(context.Context, int) ([]InputDraft, error)
}

type RunnerStatus struct {
	OwnerHeld         bool
	LastCycleAt       time.Time
	LastSuccess       time.Time
	LastError         string
	Inputs            int
	Executed          int
	ProxyFailures     int
	Selected          int
	Target            int
	Claimed           int
	Started           int
	Processed         int
	SkippedLeases     int
	Deferred          int
	ExecutionFailures int
	ReleaseFailures   int
	Partial           bool
}

// Runner is the J3a owner loop. The ordering in runCycle is intentional:
// owner lease -> read-only candidate-pool LoadDue -> proxy lease ->
// Store IssueInput -> ExecuteIssuedInput. No upstream request is possible
// before all fences hold; busy proxy leases are skipped so the candidate pool
// can provide deferred/partial metrics instead of turning contention into a
// synthetic execution failure.
type Runner struct {
	cfg        RuntimeConfig
	store      *Store
	reader     inputLoader
	projector  *ResultProjector
	logger     *slog.Logger
	mu         sync.RWMutex
	status     RunnerStatus
	ownerLease *OwnerLease
	// These hooks keep lifecycle failure paths executable in unit tests while
	// production defaults remain the Store methods.
	acquireOwnerLease  func(context.Context, string, time.Duration) (OwnerLease, bool, error)
	renewOwnerLease    func(context.Context, OwnerLease, time.Duration) error
	releaseOwnerLease  func(context.Context, OwnerLease) error
	releaseProxyLease  func(context.Context, ProxyLease) error
	acquireProxyLease  func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error)
	issueInput         func(context.Context, InputDraft) (IssuedInput, error)
	executeIssuedInput func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error)
	runOwnedFn         func(context.Context, OwnerLease) error
}

func NewRunner(cfg RuntimeConfig, store *Store, reader inputLoader, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	runner := &Runner{cfg: cfg, store: store, reader: reader, logger: logger, executeIssuedInput: ExecuteIssuedInput}
	if store != nil {
		runner.acquireOwnerLease = store.AcquireOwnerLease
		runner.renewOwnerLease = store.RenewOwnerLease
		runner.releaseOwnerLease = store.ReleaseOwnerLease
		runner.releaseProxyLease = store.ReleaseProxyLease
		runner.acquireProxyLease = store.AcquireProxyLease
		runner.issueInput = store.IssueInput
	}
	return runner
}

const leaseReleaseTimeout = 5 * time.Second

// boundedReleaseContext deliberately detaches cancellation from the request
// or cycle context. A lease/claim release must still be attempted after the
// work is cancelled, but it must have a finite upper bound and report failure
// to the caller rather than disappearing in a defer.
func boundedReleaseContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), leaseReleaseTimeout)
}

func (r *Runner) releaseOwnerLeaseBounded(parent context.Context, lease OwnerLease) error {
	release := r.releaseOwnerLease
	if release == nil && r.store != nil {
		release = r.store.ReleaseOwnerLease
	}
	if release == nil {
		return errors.New("J3a owner lease release hook unavailable")
	}
	releaseCtx, releaseCancel := boundedReleaseContext(parent)
	defer releaseCancel()
	return release(releaseCtx, lease)
}

func (r *Runner) releaseProxyLeaseBounded(parent context.Context, lease ProxyLease) error {
	release := r.releaseProxyLease
	if release == nil && r.store != nil {
		release = r.store.ReleaseProxyLease
	}
	if release == nil {
		return errors.New("J3a proxy lease release hook unavailable")
	}
	releaseCtx, releaseCancel := boundedReleaseContext(parent)
	defer releaseCancel()
	return release(releaseCtx, lease)
}

func joinReleaseFailure(existing error, label string, releaseErr error) error {
	if releaseErr == nil {
		return existing
	}
	wrapped := fmt.Errorf("J3a %s release failed: %w", label, releaseErr)
	if existing == nil {
		return wrapped
	}
	return errors.Join(existing, wrapped)
}

// SetResultProjector binds the Go-owned business writer. A J3a runner without
// it is deliberately not ready to execute: durable jobs evidence alone is not
// a completed proxy test until Go has fenced and persisted the business state.
func (r *Runner) SetResultProjector(projector *ResultProjector) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.projector = projector
	r.mu.Unlock()
}

func (r *Runner) projectOutcomeResult(ctx context.Context, outcome Outcome) (ProjectionResult, error) {
	// runCycle is intentionally unit-testable without a business database.
	// Production Run and RunManual reject a nil projector before any executor
	// is reached, so this branch cannot create a runtime Node fallback.
	r.mu.RLock()
	projector := r.projector
	r.mu.RUnlock()
	if projector == nil {
		return ProjectionResult{}, nil
	}
	return projector.ProjectOutcome(ctx, outcome)
}

func (r *Runner) projectOutcome(ctx context.Context, outcome Outcome) error {
	_, err := r.projectOutcomeResult(ctx, outcome)
	return err
}

// manualProjectionDisposition is the fail-closed boundary between durable
// outcome projection and the manual HTTP report. Applied outcomes may perform
// the second outbound CAS; stale outcomes still return their report without a
// business write, while a deleted proxy is an explicit not-found result.
func manualProjectionDisposition(result ProjectionResult) (bool, error) {
	switch result.Disposition {
	case ProjectionApplied:
		return true, nil
	case ProjectionStale:
		return false, nil
	case ProjectionIgnored:
		if result.Reason == "proxy_missing_or_deleted" {
			return false, ErrManualProxyMissing
		}
		return false, errors.New("J3a manual projector ignored outcome: " + result.Reason)
	case ProjectionRejected:
		return false, errors.New("J3a manual projector rejected outcome: " + result.Reason)
	default:
		return false, errors.New("J3a manual projector returned unknown disposition")
	}
}

func (r *Runner) Snapshot() (RunnerStatus, bool) {
	if r == nil {
		return RunnerStatus{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := r.status
	ready := status.OwnerHeld && !status.LastSuccess.IsZero() && status.LastError == ""
	return status, ready
}

func (r *Runner) Status() RunnerStatus { status, _ := r.Snapshot(); return status }
func (r *Runner) Ready() bool {
	_, ready := r.Snapshot()
	r.mu.RLock()
	projector := r.projector
	r.mu.RUnlock()
	return ready && projector != nil && projector.Ready()
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil || r.reader == nil {
		return errors.New("J3a runner 未初始化")
	}
	if r.projector == nil && strings.TrimSpace(r.cfg.ResultPostgresURL) != "" {
		return errors.New("J3a Go runner 缺少唯一 business-result projector")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		acquireOwner := r.acquireOwnerLease
		if acquireOwner == nil {
			acquireOwner = r.store.AcquireOwnerLease
		}
		lease, acquired, err := acquireOwner(ctx, r.cfg.InstanceID, r.cfg.OwnerLease)
		if err != nil {
			r.recordError(err)
			if err := waitRuntime(ctx, schedulejitter.Delay(r.cfg.Interval)); err != nil {
				return err
			}
			continue
		}
		if !acquired {
			if err := waitRuntime(ctx, schedulejitter.Delay(minRuntime(r.cfg.Interval, r.cfg.OwnerLease/3))); err != nil {
				return err
			}
			continue
		}
		r.setOwnerLease(lease)
		r.setOwnerHeld(true)
		runOwned := r.runOwnedFn
		if runOwned == nil {
			runOwned = r.runOwned
		}
		err = runOwned(ctx, lease)
		r.setOwnerHeld(false)
		r.clearOwnerLease()
		releaseErr := r.releaseOwnerLeaseBounded(ctx, lease)
		runErr := err
		if releaseErr != nil {
			if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				runErr = releaseErr
			} else {
				runErr = errors.Join(err, releaseErr)
			}
		}
		if releaseErr != nil {
			// Release failures are operationally significant even when the
			// bounded release context itself timed out; never hide them behind
			// the normal context-cancellation filter.
			r.recordError(runErr)
		} else if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			r.recordError(runErr)
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return ctx.Err()
		}
		if err := waitRuntime(ctx, schedulejitter.Delay(r.cfg.Interval)); err != nil {
			return err
		}
	}
}

// RunManual executes one explicit proxy snapshot without entering the periodic
// candidate scheduler. It reuses the currently-held owner fence when the
// periodic loop is active; otherwise it acquires a short-lived owner lease.
// This keeps manual diagnostics available during Go ownership while preserving
// the same owner/proxy fences and committed outcome semantics as periodic work.
func (r *Runner) RunManual(ctx context.Context, request ManualRequest) (report ProxyTestReport, runErr error) {
	if r == nil || r.store == nil || r.projector == nil {
		return ProxyTestReport{}, errors.New("J3a manual runner 未初始化")
	}
	if err := request.Validate(r.cfg.ManualDeadline); err != nil {
		return ProxyTestReport{}, err
	}
	now := r.now()
	deadline := r.cfg.ManualDeadline
	if request.DeadlineMS > 0 && time.Duration(request.DeadlineMS)*time.Millisecond < deadline {
		deadline = time.Duration(request.DeadlineMS) * time.Millisecond
	}
	if deadline <= 0 {
		return ProxyTestReport{}, errors.New("J3a manual deadline 无效")
	}
	// Node preserves a 200 report with an unknown synthetic base item when no
	// enabled provider supplies a target. There is no upstream work to fence or
	// persist in this branch; returning the report directly keeps that boundary
	// without weakening the jobs Store's normal non-empty-target contract.
	if len(request.Targets) == 0 {
		projection, err := r.projector.ProjectManualNoTargets(ctx, request, now)
		if err != nil {
			return ProxyTestReport{}, err
		}
		if _, err := manualProjectionDisposition(projection); err != nil {
			return ProxyTestReport{}, err
		}
		return request.Report(Outcome{ProxyID: request.ProxyID, ObservedAt: now, OverallStatus: OverallUnknown}), nil
	}
	owner, reused := r.currentOwnerLease()
	if !reused {
		acquireOwner := r.acquireOwnerLease
		if acquireOwner == nil {
			acquireOwner = r.store.AcquireOwnerLease
		}
		acquired, ok, err := acquireOwner(ctx, r.cfg.InstanceID, r.cfg.OwnerLease)
		if err != nil {
			return ProxyTestReport{}, err
		}
		if !ok {
			return ProxyTestReport{}, ErrOwnerLeaseHeld
		}
		owner = acquired
	}
	if !reused {
		defer func() {
			if releaseErr := r.releaseOwnerLeaseBounded(ctx, owner); releaseErr != nil {
				runErr = joinReleaseFailure(runErr, "manual owner lease", releaseErr)
				r.recordError(runErr)
			}
		}()
	}
	// Match the periodic path: reject a busy proxy before creating an issued
	// input. A manual 503 is a scheduling result, not probe work, so it must not
	// leave an otherwise unreachable input waiting for expiry.
	acquireProxy := r.acquireProxyLease
	if acquireProxy == nil {
		acquireProxy = r.store.AcquireProxyLease
	}
	proxy, acquired, err := acquireProxy(ctx, owner, request.ProxyID, r.cfg.ProxyLease)
	if err != nil {
		return ProxyTestReport{}, err
	}
	if !acquired {
		return ProxyTestReport{}, ErrProxyLeaseHeld
	}
	defer func() {
		if releaseErr := r.releaseProxyLeaseBounded(ctx, proxy); releaseErr != nil {
			runErr = joinReleaseFailure(runErr, "manual proxy lease", releaseErr)
			r.recordError(runErr)
		}
	}()
	// The durable Store input contract intentionally requires a 1..15 minute
	// expiry window. Manual execution keeps its stricter probe deadline in the
	// execution context while issuing a bounded one-minute durable snapshot.
	draftDeadline := deadline
	if draftDeadline < time.Minute {
		draftDeadline = time.Minute
	}
	draft := request.InputDraft(now, draftDeadline)
	issueInput := r.issueInput
	if issueInput == nil {
		issueInput = r.store.IssueInput
	}
	issued, err := issueInput(ctx, draft)
	if err != nil {
		return ProxyTestReport{}, err
	}
	execCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	proxyURL, err := proxyURLForIssuedInput(issued, r.cfg.CredentialSecret)
	if err != nil {
		return ProxyTestReport{}, err
	}
	defer clearProxyURL(&proxyURL)
	execute := r.executeIssuedInput
	if execute == nil {
		execute = ExecuteIssuedInput
	}
	outcome, _, err := execute(execCtx, r.store, owner, proxy, issued, ExecutorOptions{CredentialSecret: r.cfg.CredentialSecret, Timeout: deadline, Now: r.cfg.Now})
	if err != nil {
		return ProxyTestReport{}, err
	}
	if err := request.ValidateOutcome(outcome); err != nil {
		return ProxyTestReport{}, err
	}
	projection, err := r.projectOutcomeResult(ctx, outcome)
	if err != nil {
		return ProxyTestReport{}, err
	}
	writeOutbound, err := manualProjectionDisposition(projection)
	if err != nil {
		return ProxyTestReport{}, err
	}
	report = request.Report(outcome)
	if !writeOutbound {
		return report, nil
	}
	info, ok := probeManualOutbound(execCtx, proxyURL, deadline)
	if !ok {
		info = manualOutboundInfo{}
	}
	if info.IP != "" {
		report.OutboundIP = info.IP
	}
	if info.Region != "" {
		report.OutboundRegion = info.Region
	}
	if err := r.projector.ProjectManualOutbound(ctx, outcome, report.OutboundIP, report.OutboundRegion); err != nil {
		if errors.Is(err, ErrManualProxyMissing) {
			return ProxyTestReport{}, err
		}
		if errors.Is(err, ErrManualProjectionStale) {
			return report, nil
		}
		return ProxyTestReport{}, err
	}
	return report, nil
}

func (r *Runner) runOwned(ctx context.Context, lease OwnerLease) error {
	ownedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(minRuntime(r.cfg.OwnerLease/3, time.Second*30))
		defer ticker.Stop()
		for {
			select {
			case <-ownedCtx.Done():
				return
			case <-ticker.C:
				if err := r.renewOwnerLease(ownedCtx, lease, r.cfg.OwnerLease); err != nil {
					r.recordError(err)
					select {
					case renewErr <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	if err := r.runCycle(ownedCtx, lease); err != nil {
		if renewal := readRenewalError(renewErr); renewal != nil {
			return errors.Join(err, renewal)
		}
		return err
	}
	timer := time.NewTimer(schedulejitter.Delay(r.cfg.Interval))
	defer timer.Stop()
	for {
		select {
		case <-ownedCtx.Done():
			if renewal := readRenewalError(renewErr); renewal != nil {
				return renewal
			}
			return ownedCtx.Err()
		case <-timer.C:
			if err := r.runCycle(ownedCtx, lease); err != nil {
				if renewal := readRenewalError(renewErr); renewal != nil {
					return errors.Join(err, renewal)
				}
				return err
			}
			timer.Reset(schedulejitter.Delay(r.cfg.Interval))
		}
	}
}

func readRenewalError(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func (r *Runner) runCycle(ctx context.Context, owner OwnerLease) error {
	attempt := r.now()
	if err := r.store.VerifyOwnerLease(ctx, owner); err != nil {
		r.recordCycle(attempt, 0, 0, 1, err)
		return err
	}
	drafts, err := r.reader.LoadDue(ctx, r.candidatePoolLimit())
	if err != nil {
		r.recordCycle(attempt, 0, 0, 1, err)
		return err
	}
	counts := &cycleCounts{selected: len(drafts), target: minInt(r.batchSize(), len(drafts))}
	cycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	nextCandidate := 0
	var cycleErrs []error
	var fatalErr error
	recordProxyReleaseFailure := func(releaseErr error) {
		if releaseErr == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		counts.releaseFailures++
		counts.failures++
		cycleErrs = append(cycleErrs, releaseErr)
		if fatalLeaseError(releaseErr) && fatalErr == nil {
			fatalErr = releaseErr
			cancel()
		}
	}
	worker := func() {
		for {
			if cycleCtx.Err() != nil {
				return
			}
			mu.Lock()
			if counts.started >= counts.target || nextCandidate >= len(drafts) {
				mu.Unlock()
				return
			}
			index := nextCandidate
			nextCandidate++
			mu.Unlock()
			draft := drafts[index]
			if err := r.store.VerifyOwnerLease(cycleCtx, owner); err != nil {
				mu.Lock()
				cycleErrs = append(cycleErrs, err)
				if fatalLeaseError(err) && fatalErr == nil {
					fatalErr = err
					cancel()
				}
				counts.failures++
				counts.executionFailures++
				mu.Unlock()
				return
			}
			acquireProxy := r.acquireProxyLease
			if acquireProxy == nil {
				acquireProxy = r.store.AcquireProxyLease
			}
			proxy, acquired, err := acquireProxy(cycleCtx, owner, draft.ProxyID, r.cfg.ProxyLease)
			if err != nil {
				if errors.Is(err, ErrProxyLeaseHeld) {
					mu.Lock()
					counts.skippedLeases++
					mu.Unlock()
					continue
				}
				mu.Lock()
				cycleErrs = append(cycleErrs, err)
				counts.failures++
				counts.executionFailures++
				if fatalLeaseError(err) && fatalErr == nil {
					fatalErr = err
					cancel()
				}
				mu.Unlock()
				continue
			}
			if !acquired {
				mu.Lock()
				counts.skippedLeases++
				mu.Unlock()
				continue
			}
			mu.Lock()
			counts.claimed++
			if counts.started >= counts.target {
				mu.Unlock()
				recordProxyReleaseFailure(r.releaseProxyLeaseBounded(cycleCtx, proxy))
				continue
			}
			counts.started++
			mu.Unlock()

			issued, err := r.store.IssueInput(cycleCtx, draft)
			if err != nil {
				mu.Lock()
				counts.failures++
				counts.executionFailures++
				cycleErrs = append(cycleErrs, err)
				mu.Unlock()
				recordProxyReleaseFailure(r.releaseProxyLeaseBounded(cycleCtx, proxy))
				r.logger.Warn("J3a IssueInput failed", "error", err)
				continue
			}
			mu.Lock()
			counts.inputs++
			mu.Unlock()
			proxyWindow := executionWindowUntil(r.now(), issued.ExpiresAt, proxy.LeaseUntil)
			if proxyWindow <= 0 {
				mu.Lock()
				counts.failures++
				counts.executionFailures++
				cycleErrs = append(cycleErrs, errors.New("J3a proxy execution window expired"))
				mu.Unlock()
				recordProxyReleaseFailure(r.releaseProxyLeaseBounded(cycleCtx, proxy))
				continue
			}
			execCtx, execCancel := context.WithTimeout(cycleCtx, proxyWindow)
			execute := r.executeIssuedInput
			if execute == nil {
				execute = ExecuteIssuedInput
			}
			outcome, committed, execErr := execute(execCtx, r.store, owner, proxy, issued, ExecutorOptions{CredentialSecret: r.cfg.CredentialSecret, Timeout: r.cfg.ProbeTimeout, Now: r.cfg.Now})
			if execErr == nil {
				execErr = r.projectOutcome(execCtx, outcome)
			}
			execCancel()
			releaseErr := r.releaseProxyLeaseBounded(cycleCtx, proxy)
			mu.Lock()
			if releaseErr != nil {
				counts.releaseFailures++
				counts.failures++
				cycleErrs = append(cycleErrs, releaseErr)
			}
			if execErr != nil {
				counts.executionFailures++
				counts.failures++
				cycleErrs = append(cycleErrs, execErr)
				if fatalLeaseError(execErr) && fatalErr == nil {
					fatalErr = execErr
					cancel()
				}
			} else {
				counts.processed++
				if committed {
					counts.executed++
				}
			}
			mu.Unlock()
			if execErr != nil {
				r.logger.Warn("J3a proxy execution failed", "proxyID", issued.ProxyID, "error", execErr)
			}
			mu.Lock()
			fatal := fatalErr != nil
			mu.Unlock()
			if fatal {
				return
			}
		}
	}
	workers := minInt(r.workerConcurrency(), maxInt(1, counts.target))
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() { defer wg.Done(); worker() }()
	}
	wg.Wait()
	counts.deferred = maxInt(0, counts.target-counts.started)
	cycleErr := error(nil)
	if len(cycleErrs) > 0 {
		cycleErr = errors.Join(cycleErrs...)
	}
	if fatalErr != nil {
		cycleErr = errors.Join(cycleErr, fatalErr)
	}
	if cycleErr == nil && (counts.deferred > 0 || counts.releaseFailures > 0) {
		cycleErr = errors.New("J3a cycle partial completion")
	}
	r.recordCycleSummary(attempt, counts, cycleErr)
	if fatalErr != nil {
		return cycleErr
	}
	return nil
}

func fatalLeaseError(err error) bool {
	return errors.Is(err, ErrOwnerLeaseLost) || errors.Is(err, ErrProxyLeaseLost)
}

func (r *Runner) recordCycle(attempt time.Time, inputs, executed, failures int, cycleErr error) {
	r.recordCycleSummary(attempt, &cycleCounts{inputs: inputs, executed: executed, failures: failures}, cycleErr)
}

type cycleCounts struct {
	selected, target, claimed, started, processed, skippedLeases, deferred int
	inputs, executed, failures, executionFailures, releaseFailures         int
}

func (r *Runner) recordCycleSummary(attempt time.Time, counts *cycleCounts, cycleErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.LastCycleAt = attempt
	r.status.Inputs = counts.inputs
	r.status.Executed += counts.executed
	r.status.ProxyFailures = counts.failures
	r.status.Selected = counts.selected
	r.status.Target = counts.target
	r.status.Claimed = counts.claimed
	r.status.Started = counts.started
	r.status.Processed = counts.processed
	r.status.SkippedLeases = counts.skippedLeases
	r.status.Deferred = counts.deferred
	r.status.ExecutionFailures = counts.executionFailures
	r.status.ReleaseFailures = counts.releaseFailures
	r.status.Partial = counts.deferred > 0 || counts.executionFailures > 0 || counts.releaseFailures > 0
	if cycleErr == nil {
		r.status.LastSuccess = r.now()
		r.status.LastError = ""
	} else {
		r.status.LastError = cycleErr.Error()
	}
}

func (r *Runner) batchSize() int {
	if r.cfg.BatchSize > 0 {
		return r.cfg.BatchSize
	}
	if r.cfg.InputLimit > 0 {
		return r.cfg.InputLimit
	}
	return 1
}

func (r *Runner) candidatePoolLimit() int {
	factor := r.cfg.CandidatePoolFactor
	if factor <= 0 {
		factor = 1
	}
	limit := r.batchSize() * factor
	if r.cfg.InputLimit > 0 && r.cfg.InputLimit < limit {
		limit = r.cfg.InputLimit
	}
	return maxInt(1, limit)
}

func (r *Runner) workerConcurrency() int {
	if r.cfg.WorkerConcurrency > 0 {
		return r.cfg.WorkerConcurrency
	}
	return 1
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (r *Runner) setOwnerHeld(value bool) { r.mu.Lock(); r.status.OwnerHeld = value; r.mu.Unlock() }
func (r *Runner) setOwnerLease(lease OwnerLease) {
	r.mu.Lock()
	copy := lease
	r.ownerLease = &copy
	r.mu.Unlock()
}
func (r *Runner) clearOwnerLease() { r.mu.Lock(); r.ownerLease = nil; r.mu.Unlock() }
func (r *Runner) currentOwnerLease() (OwnerLease, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ownerLease == nil || !r.status.OwnerHeld {
		return OwnerLease{}, false
	}
	return *r.ownerLease, true
}
func (r *Runner) recordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.status.LastError = err.Error()
	r.mu.Unlock()
}
func (r *Runner) now() time.Time {
	if r.cfg.Now != nil {
		return r.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/health" {
			http.NotFound(w, req)
			return
		}
		s, ready := r.Snapshot()
		payload := map[string]any{"ready": ready, "j3aEnabled": r.cfg.Enabled, "ownerHeld": s.OwnerHeld, "lastCycleAt": formatRuntimeTime(s.LastCycleAt), "lastSuccessAt": formatRuntimeTime(s.LastSuccess), "lastError": s.LastError, "inputs": s.Inputs, "executed": s.Executed, "proxyFailures": s.ProxyFailures}
		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func formatRuntimeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func waitRuntime(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func minRuntime(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func executionWindow(now, expiresAt time.Time, lease time.Duration) time.Duration {
	return executionWindowUntil(now, expiresAt, now.Add(lease))
}

func executionWindowUntil(now, expiresAt, leaseUntil time.Time) time.Duration {
	remaining := expiresAt.Sub(now)
	leaseRemaining := leaseUntil.Sub(now)
	if remaining <= 0 || leaseRemaining <= 0 {
		return 0
	}
	return minRuntime(leaseRemaining, remaining)
}
