package opsjobs

// w7d（listavailability 错误臂批次）：维护批全部错误/旁路分支与 Redis overlay
// 对账分支。全部复用既有 fake repo/probe/overlay，配 w7d 包装注入错误；
// 进程内确定性，不连接真实数据库/Redis。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// w7dFailingListRepo 按方法注入错误，其余委托 inner。
type w7dFailingListRepo struct {
	inner                      *fakeListAvailabilityRepo
	ensureErr                  error
	markErr                    error
	recoveryStartErr           error
	touchErr                   error
	recoveryEnqueueErr         error
	viewerHealthErr            error
	enqueueMissingErr          error
	enqueueDueErr              error
	candidatesErr              error
	refreshErr                 error
	claimErr                   error
	scopesErr                  error
	searchTermsErr             error
	applyClaimsErr             error
	deletionErr                error
	releaseErr                 error
	candidates                 []string
	recoveryCompleteErr        error
	recoveryCompleteSecondCall bool
	completed                  bool
}

func (r *w7dFailingListRepo) EnsureRuntimeDependency(context.Context, string) error {
	return r.ensureErr
}
func (r *w7dFailingListRepo) MarkRuntimeDependencyUnavailable(ctx context.Context, reason, at string) error {
	if r.markErr != nil {
		return r.markErr
	}
	return r.inner.MarkRuntimeDependencyUnavailable(ctx, reason, at)
}
func (r *w7dFailingListRepo) BeginRuntimeDependencyRecovery(context.Context, string) (bool, error) {
	if r.recoveryStartErr != nil {
		return false, r.recoveryStartErr
	}
	return r.inner.BeginRuntimeDependencyRecovery(context.Background(), "")
}
func (r *w7dFailingListRepo) TouchRuntimeDependency(context.Context, string) error {
	return r.touchErr
}
func (r *w7dFailingListRepo) EnqueueAllForRuntimeRecovery(context.Context, int64) (int, error) {
	if r.recoveryEnqueueErr != nil {
		return 0, r.recoveryEnqueueErr
	}
	return r.inner.EnqueueAllForRuntimeRecovery(context.Background(), 0)
}
func (r *w7dFailingListRepo) CompleteRuntimeDependencyRecovery(context.Context, string) (bool, error) {
	if r.recoveryCompleteErr != nil {
		return false, r.recoveryCompleteErr
	}
	if !r.recoveryCompleteSecondCall {
		r.recoveryCompleteSecondCall = true
		return r.completed, nil
	}
	return false, nil
}
func (r *w7dFailingListRepo) EnsureViewerHealth(context.Context, int, string) (int, error) {
	if r.viewerHealthErr != nil {
		return 0, r.viewerHealthErr
	}
	return 0, nil
}
func (r *w7dFailingListRepo) EnqueueMissing(context.Context, int, int64) (int, error) {
	return 0, r.enqueueMissingErr
}
func (r *w7dFailingListRepo) EnqueueDue(context.Context, int, int64) (int, error) {
	return 0, r.enqueueDueErr
}
func (r *w7dFailingListRepo) ListViewerHealthRefreshCandidates(context.Context, int) ([]string, error) {
	if r.candidatesErr != nil {
		return nil, r.candidatesErr
	}
	return r.candidates, nil
}
func (r *w7dFailingListRepo) RefreshViewerHealth(context.Context, string, string) error {
	return r.refreshErr
}
func (r *w7dFailingListRepo) ClaimDirty(context.Context, string, int, int64, int64) ([]DirtyClaim, error) {
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.inner.ClaimDirty(context.Background(), "", 0, 0, 0)
}
func (r *w7dFailingListRepo) ListScopes(ctx context.Context, ids []string) ([]ProjectionScope, error) {
	if r.scopesErr != nil {
		return nil, r.scopesErr
	}
	return r.inner.ListScopes(ctx, ids)
}
func (r *w7dFailingListRepo) LoadSearchTerms(ctx context.Context, ids []string) (map[string][]string, error) {
	if r.searchTermsErr != nil {
		return nil, r.searchTermsErr
	}
	return r.inner.LoadSearchTerms(ctx, ids)
}
func (r *w7dFailingListRepo) ApplyClaims(ctx context.Context, writes []ProjectionWrite) (map[string]bool, error) {
	if r.applyClaimsErr != nil {
		return nil, r.applyClaimsErr
	}
	return r.inner.ApplyClaims(ctx, writes)
}
func (r *w7dFailingListRepo) ApplyDeletionClaim(ctx context.Context, claim DirtyClaim) (bool, error) {
	if r.deletionErr != nil {
		return false, r.deletionErr
	}
	return r.inner.ApplyDeletionClaim(ctx, claim)
}
func (r *w7dFailingListRepo) ReleaseForReplay(ctx context.Context, input ListAvailabilityReplayInput) (bool, error) {
	if r.releaseErr != nil {
		return false, r.releaseErr
	}
	return r.inner.ReleaseForReplay(ctx, input)
}

// w7dBrokenOverlays 全方法可注入错误的 overlay 对账桩。
type w7dBrokenOverlays struct {
	entries        []OverlayEntry
	existing       map[string]struct{}
	snapshots      []OverlaySnapshot
	listErr        error
	existingErr    error
	ackErr         error
	snapshotErr    error
	upsertErr      error
	skipSnapshots  bool
	ackAfterUpsert bool
}

func (o *w7dBrokenOverlays) ListDirtyEntries(context.Context, int) ([]OverlayEntry, error) {
	if o.listErr != nil {
		return nil, o.listErr
	}
	return o.entries, nil
}
func (o *w7dBrokenOverlays) Acknowledge(context.Context, []OverlayEntry) error {
	if o.ackErr != nil && !o.ackAfterUpsert {
		return o.ackErr
	}
	return nil
}
func (o *w7dBrokenOverlays) LoadSnapshots(context.Context, []string) ([]OverlaySnapshot, error) {
	if o.snapshotErr != nil {
		return nil, o.snapshotErr
	}
	return o.snapshots, nil
}
func (o *w7dBrokenOverlays) UpsertOverlays(context.Context, []OverlayUpsert) error {
	return o.upsertErr
}
func (o *w7dBrokenOverlays) ExistingAccountIDs(context.Context, []string) (map[string]struct{}, error) {
	if o.existingErr != nil {
		return nil, o.existingErr
	}
	return o.existing, nil
}

func w7dListOptions(repo ListAvailabilityRepo, loader ItemLoader, overlays OverlayReconciler, probe RuntimeDependencyProbe) ListAvailabilityOptions {
	return ListAvailabilityOptions{
		OwnerID:           "owner-1",
		BatchSize:         10,
		LeaseMS:           2_000,
		MaxBatchesPerRun:  1,
		WorkerConcurrency: 1,
		NowMS:             func() int64 { return 1_000 },
		Repo:              repo,
		LoadItems:         loader,
		Overlays:          overlays,
		RuntimeProbe:      probe,
	}
}

func w7dScopeFor(accountID string, viewer string) ProjectionScope {
	return ProjectionScope{AccountID: accountID, ViewerSystemAccountID: viewer}
}

func w7dClaimFor(accountID string, viewer string, token string) DirtyClaim {
	return DirtyClaim{AccountID: accountID, ViewerSystemAccountID: viewer, Generation: 1, ClaimToken: token}
}

func TestW7DListAvailabilityBatchInputGuards(t *testing.T) {
	base := &fakeListAvailabilityRepo{}
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }

	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(base, loader, nil, nil), false); err != nil {
		t.Fatalf("非 PG 必须空结果返回: %v", err)
	}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(base, loader, nil, nil), true); err != nil {
		t.Fatalf("PG happy 空批: %v", err)
	}
	badOwner := w7dListOptions(base, loader, nil, nil)
	badOwner.OwnerID = " "
	if _, err := RunListAvailabilityMaintenance(context.Background(), badOwner, true); err == nil {
		t.Fatal("空白 owner 必须拒绝")
	}
	badBatch := w7dListOptions(base, loader, nil, nil)
	badBatch.BatchSize = listAvailabilityMaxBatchSize + 1
	if _, err := RunListAvailabilityMaintenance(context.Background(), badBatch, true); err == nil {
		t.Fatal("批量超限必须拒绝")
	}
	badLease := w7dListOptions(base, loader, nil, nil)
	badLease.LeaseMS = 1
	if _, err := RunListAvailabilityMaintenance(context.Background(), badLease, true); err == nil {
		t.Fatal("租约下限必须拒绝")
	}
	noDeps := w7dListOptions(base, loader, nil, nil)
	noDeps.Repo = nil
	if _, err := RunListAvailabilityMaintenance(context.Background(), noDeps, true); err == nil {
		t.Fatal("缺 repo 必须拒绝")
	}
	noLoader := w7dListOptions(base, nil, nil, nil)
	if _, err := RunListAvailabilityMaintenance(context.Background(), noLoader, true); err == nil {
		t.Fatal("缺 loader 必须拒绝")
	}
	badConcurrency := w7dListOptions(base, loader, nil, nil)
	badConcurrency.WorkerConcurrency = listAvailabilityMaxWorkerConcurrency + 1
	if _, err := RunListAvailabilityMaintenance(context.Background(), badConcurrency, true); err == nil {
		t.Fatal("worker 并发超限必须拒绝")
	}
	badBatches := w7dListOptions(base, loader, nil, nil)
	badBatches.MaxBatchesPerRun = listAvailabilityMaxBatchesPerRun + 1
	if _, err := RunListAvailabilityMaintenance(context.Background(), badBatches, true); err == nil {
		t.Fatal("批次数超限必须拒绝")
	}
}

func TestW7DListAvailabilityRuntimeDependencyArms(t *testing.T) {
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }

	// EnsureRuntimeDependency 错误。
	failing := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, ensureErr: errors.New("w7d: ensure boom")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(failing, loader, nil, nil), true); err == nil {
		t.Fatal("ensure 错误必须暴露")
	}

	// 探针错误。
	probeErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(probeErrRepo, loader, nil, w7dBrokenProbe{err: errors.New("w7d: probe boom")}), true); err == nil {
		t.Fatal("探针错误必须暴露")
	}

	// 可用性缺失 → mark 依赖不可用。
	unavailable := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	result, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(unavailable, loader, nil, w7dBrokenProbe{available: false, concurrency: true}), true)
	if err != nil || result.RuntimeDependencyUnavailable != 1 {
		t.Fatalf("可用性缺失必须标记: %#v %v", result, err)
	}

	// 并发运行态缺失 → 专用 reason。
	concurrencyOut := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(concurrencyOut, loader, nil, w7dBrokenProbe{available: true, concurrency: false}), true); err != nil {
		t.Fatal(err)
	}
	if got := concurrencyOut.inner.markUnavailable; len(got) != 1 || got[0] != "account_concurrency_runtime_unavailable" {
		t.Fatalf("并发缺失 reason 不正确: %v", got)
	}

	// overlay 对账错误 → mark unavailable。
	overlayBoom := &w7dBrokenOverlays{listErr: errors.New("w7d: overlay boom")}
	overlayRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(overlayRepo, loader, overlayBoom, w7dBrokenProbe{available: true, concurrency: true}), true); err != nil {
		t.Fatal(err)
	}
	if got := overlayRepo.inner.markUnavailable; len(got) != 1 || got[0] != "account_concurrency_overlay_reconcile_failed" {
		t.Fatalf("overlay 错误必须标记: %v", got)
	}

	// SyncSchedules 错误。
	recoveryRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	recoveryRepo.inner.recoveryStarted = true
	syncOpts := w7dListOptions(recoveryRepo, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	syncOpts.SyncSchedules = func(context.Context, int64) error { return errors.New("w7d: sync boom") }
	if _, err := RunListAvailabilityMaintenance(context.Background(), syncOpts, true); err == nil || !strings.Contains(err.Error(), "w7d: sync boom") {
		t.Fatalf("调度同步错误必须暴露: %v", err)
	}

	// 恢复未开始 → touch；恢复开始 → enqueue。
	touchRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	touchOpts := w7dListOptions(touchRepo, loader, nil, w7dBrokenProbe{available: true, concurrency: true})
	if _, err := RunListAvailabilityMaintenance(context.Background(), touchOpts, true); err != nil {
		t.Fatal(err)
	}
}

type w7dBrokenProbe struct {
	available   bool
	concurrency bool
	err         error
}

func (p w7dBrokenProbe) Probe(context.Context) (bool, bool, error) {
	return p.available, p.concurrency, p.err
}

func TestW7DListAvailabilityViewerHealthAndClaimsArms(t *testing.T) {
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }

	arms := []struct {
		name    string
		repo    *w7dFailingListRepo
		wantSub string
	}{
		{"viewer health 错误", &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, viewerHealthErr: errors.New("w7d: vh boom")}, "w7d: vh boom"},
		{"enqueue missing 错误", &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, enqueueMissingErr: errors.New("w7d: missing boom")}, "w7d: missing boom"},
		{"enqueue due 错误", &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, enqueueDueErr: errors.New("w7d: due boom")}, "w7d: due boom"},
		{"viewer candidates 错误", &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, candidatesErr: errors.New("w7d: cand boom")}, "w7d: cand boom"},
		{"claim 错误", &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, claimErr: errors.New("w7d: claim boom")}, "w7d: claim boom"},
		{"refresh 错误", func() *w7dFailingListRepo {
			repo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, refreshErr: errors.New("w7d: refresh boom")}
			repo.candidates = []string{"viewer-1"}
			return repo
		}(), "w7d: refresh boom"},
		{"恢复完成错误", &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, recoveryCompleteErr: errors.New("w7d: complete boom")}, "w7d: complete boom"},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			_, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(arm.repo, loader, nil, w7dBrokenProbe{available: true, concurrency: true}), true)
			if err == nil || !strings.Contains(err.Error(), arm.wantSub) {
				t.Fatalf("%s 必须暴露: %v", arm.name, err)
			}
		})
	}
}

func TestW7DListAvailabilityProjectionArms(t *testing.T) {
	scope := w7dScopeFor("acc-1", "viewer-1")
	claim := w7dClaimFor("acc-1", "viewer-1", "token-1")
	loader := func(_ context.Context, viewer string, ids []string) ([]ProjectionItem, error) {
		items := make([]ProjectionItem, 0, len(ids))
		for _, id := range ids {
			item := projectionItem(id)
			item.EffectiveStatus = "available"
			items = append(items, item)
		}
		return items, nil
	}
	probe := w7dBrokenProbe{available: true, concurrency: true}

	newRepo := func() *w7dFailingListRepo {
		repo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
		repo.inner.scopes = map[string]ProjectionScope{"acc-1": scope}
		repo.inner.claims = []DirtyClaim{claim}
		return repo
	}

	// happy：applied → projected。
	repo := newRepo()
	repo.inner.applyResults = map[string]bool{"token-1": true}
	result, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(repo, loader, nil, probe), true)
	if err != nil || result.Projected != 1 || result.Claimed != 1 {
		t.Fatalf("happy 投影: %#v %v", result, err)
	}

	// stale claim（applyResults=false）→ staleClaims。
	staleRepo := newRepo()
	staleRepo.inner.applyResults = map[string]bool{"token-1": false}
	result, err = RunListAvailabilityMaintenance(context.Background(), w7dListOptions(staleRepo, loader, nil, probe), true)
	if err != nil || result.StaleClaims != 1 {
		t.Fatalf("stale claim: %#v %v", result, err)
	}

	// scope 缺失 → 删除型 claim。
	deletedRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	deletedRepo.inner.claims = []DirtyClaim{w7dClaimFor("ghost", "viewer-1", "token-g")}
	result, err = RunListAvailabilityMaintenance(context.Background(), w7dListOptions(deletedRepo, loader, nil, probe), true)
	if err != nil || result.Deleted != 1 {
		t.Fatalf("删除型 claim: %#v %v", result, err)
	}

	// 删除型 claim 失败。
	deletionErrRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}, deletionErr: errors.New("w7d: delete boom")}
	deletionErrRepo.inner.claims = []DirtyClaim{w7dClaimFor("ghost", "viewer-1", "token-g")}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(deletionErrRepo, loader, nil, probe), true); err == nil {
		t.Fatal("删除 claim 错误必须暴露")
	}

	// ListScopes 错误。
	scopesErrRepo := newRepo()
	scopesErrRepo.scopesErr = errors.New("w7d: scopes boom")
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(scopesErrRepo, loader, nil, probe), true); err == nil {
		t.Fatal("scopes 错误必须暴露")
	}

	// LoadSearchTerms 错误按 Node 语义被吞：标记依赖不可用并释放重放。
	searchErrRepo := newRepo()
	searchErrRepo.searchTermsErr = errors.New("w7d: terms boom")
	result, err = RunListAvailabilityMaintenance(context.Background(), w7dListOptions(searchErrRepo, loader, nil, probe), true)
	if err != nil {
		t.Fatalf("search terms 错误不得上抛: %v", err)
	}
	if len(searchErrRepo.inner.released) != 0 {
		t.Fatalf("search terms 失败不释放 claim（直接返回错误）: %d", len(searchErrRepo.inner.released))
	}
	if got := searchErrRepo.inner.markUnavailable; len(got) != 1 || got[0] != "projection_runtime_materialization_failed" {
		t.Fatalf("search terms 失败必须标记依赖不可用: %v", got)
	}

	// LoadItems 错误按 Node 语义被吞：释放重放并标记依赖不可用。
	loadErrRepo := newRepo()
	loadErrRepo.inner.recoveryStarted = true
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(loadErrRepo, func(context.Context, string, []string) ([]ProjectionItem, error) {
		return nil, errors.New("w7d: load boom")
	}, nil, probe), true); err != nil {
		t.Fatalf("load 错误不得上抛: %v", err)
	}
	if len(loadErrRepo.inner.markUnavailable) != 1 || len(loadErrRepo.inner.released) != 1 {
		t.Fatalf("load 失败必须标记并释放: mark=%v released=%d", loadErrRepo.inner.markUnavailable, len(loadErrRepo.inner.released))
	}

	// loader 产出缺账户 → 报错在批内被吞：释放重放并标记依赖不可用。
	missingItemRepo := newRepo()
	missingItemLoader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }
	result, err = RunListAvailabilityMaintenance(context.Background(), w7dListOptions(missingItemRepo, missingItemLoader, nil, probe), true)
	if err != nil {
		t.Fatalf("缺失投影项错误不得上抛: %v", err)
	}
	if len(missingItemRepo.inner.released) != 1 {
		t.Fatalf("缺失投影项必须释放 claim: %d", len(missingItemRepo.inner.released))
	}
	if got := missingItemRepo.inner.markUnavailable; len(got) != 1 || got[0] != "projection_runtime_materialization_failed" {
		t.Fatalf("缺失投影项必须标记依赖不可用: %v", got)
	}

	// EffectiveStatus 空 → BuildProjectionWrite 错误（批内被吞：释放+标记）。
	blankStatusRepo := newRepo()
	blankLoader := func(context.Context, string, []string) ([]ProjectionItem, error) {
		return []ProjectionItem{{AccountID: "acc-1"}}, nil
	}
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(blankStatusRepo, blankLoader, nil, probe), true); err != nil {
		t.Fatalf("空状态错误不得上抛: %v", err)
	}
	if len(blankStatusRepo.inner.released) != 1 || len(blankStatusRepo.inner.markUnavailable) != 1 {
		t.Fatalf("空状态必须释放并标记: released=%d mark=%v", len(blankStatusRepo.inner.released), blankStatusRepo.inner.markUnavailable)
	}

	// ApplyClaims 错误按 Node 语义被吞：释放重放并标记。
	applyErrRepo := newRepo()
	applyErrRepo.applyClaimsErr = errors.New("w7d: apply boom")
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(applyErrRepo, loader, nil, probe), true); err != nil {
		t.Fatalf("apply 错误不得上抛: %v", err)
	}
	if len(applyErrRepo.inner.released) != 1 {
		t.Fatalf("apply 失败必须释放 claim: %d", len(applyErrRepo.inner.released))
	}

	// ReleaseForReplay 自身失败会在 mark 阶段与原错误 join 暴露。
	releaseErrRepo := newRepo()
	releaseErrRepo.applyClaimsErr = errors.New("w7d: apply boom")
	releaseErrRepo.releaseErr = errors.New("w7d: release boom")
	releaseErrRepo.markErr = errors.New("w7d: mark boom")
	if _, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(releaseErrRepo, loader, nil, probe), true); err == nil || !strings.Contains(err.Error(), "w7d: release boom") {
		t.Fatalf("release 错误必须随 mark join 暴露: %v", err)
	}
}

func TestW7DReconcileRuntimeOverlaysArms(t *testing.T) {
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil }
	probe := w7dBrokenProbe{available: true, concurrency: true}
	entry := OverlayEntry{AccountID: "acc-ov", NextReconcileAt: strPtr("2030-01-01T00:00:00Z")}

	// 空条目 → 0。
	empty := &w7dBrokenOverlays{}
	repo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	result, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(repo, loader, empty, probe), true)
	if err != nil || result.RuntimeOverlayReconciled != 0 {
		t.Fatalf("空 overlay: %#v %v", result, err)
	}

	// 活跃条目 upsert + ack。
	active := &w7dBrokenOverlays{entries: []OverlayEntry{entry}, existing: map[string]struct{}{"acc-ov": {}}, snapshots: []OverlaySnapshot{{AccountID: "acc-ov", CurrentConcurrency: 4}}}
	repo = &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	result, err = RunListAvailabilityMaintenance(context.Background(), w7dListOptions(repo, loader, active, probe), true)
	if err != nil || result.RuntimeOverlayReconciled != 1 {
		t.Fatalf("活跃 overlay: %#v %v", result, err)
	}

	// 全部 stale → 只 ack。
	stale := &w7dBrokenOverlays{entries: []OverlayEntry{entry}, existing: map[string]struct{}{}}
	repo = &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
	result, err = RunListAvailabilityMaintenance(context.Background(), w7dListOptions(repo, loader, stale, probe), true)
	if err != nil || result.RuntimeOverlayReconciled != 1 {
		t.Fatalf("stale overlay: %#v %v", result, err)
	}

	// overlay 各错误按语义被吞：标记 account_concurrency_overlay_reconcile_failed。
	assertOverlayMarked := func(t *testing.T, name string, overlays OverlayReconciler) {
		t.Helper()
		overlayRepo := &w7dFailingListRepo{inner: &fakeListAvailabilityRepo{}}
		result, err := RunListAvailabilityMaintenance(context.Background(), w7dListOptions(overlayRepo, loader, overlays, probe), true)
		if err != nil {
			t.Fatalf("%s 不得上抛: %v", name, err)
		}
		if result.RuntimeDependencyUnavailable != 1 {
			t.Fatalf("%s 必须标记依赖不可用: %#v", name, result)
		}
		if got := overlayRepo.inner.markUnavailable; len(got) != 1 || got[0] != "account_concurrency_overlay_reconcile_failed" {
			t.Fatalf("%s 标记 reason 不正确: %v", name, got)
		}
	}
	assertOverlayMarked(t, "existing 错误", &w7dBrokenOverlays{entries: []OverlayEntry{entry}, existingErr: errors.New("w7d: existing boom")})
	assertOverlayMarked(t, "缺失快照", &w7dBrokenOverlays{entries: []OverlayEntry{entry}, existing: map[string]struct{}{"acc-ov": {}}, skipSnapshots: true})
	assertOverlayMarked(t, "upsert 错误", &w7dBrokenOverlays{entries: []OverlayEntry{entry}, existing: map[string]struct{}{"acc-ov": {}}, snapshots: []OverlaySnapshot{{AccountID: "acc-ov"}}, upsertErr: errors.New("w7d: upsert boom")})
	assertOverlayMarked(t, "快照错误", &w7dBrokenOverlays{entries: []OverlayEntry{entry}, existing: map[string]struct{}{"acc-ov": {}}, snapshotErr: errors.New("w7d: snapshot boom")})
	assertOverlayMarked(t, "ack 错误", &w7dBrokenOverlays{entries: []OverlayEntry{entry}, ackErr: errors.New("w7d: ack boom")})
}

func strPtr(value string) *string { return &value }

func TestW7DProjectionReplayDelayBounds(t *testing.T) {
	cases := []struct {
		attempt int
		want    int64
	}{
		{-3, 1_000},
		{0, 1_000},
		{1, 1_000},
		{2, 2_000},
		{6, 32_000},
		{7, 60_000},
		{8, 60_000},
		{99, 60_000},
	}
	for _, tc := range cases {
		if got := ProjectionReplayDelayMS(tc.attempt); got != tc.want {
			t.Fatalf("attempt %d delay = %d, want %d", tc.attempt, got, tc.want)
		}
	}
	// BuildProjectionWrite 正常路径字段透传。
	now := time.UnixMilli(1_000)
	write, err := BuildProjectionWrite(w7dScopeFor("a", "v"), projectionItem("a"), w7dClaimFor("a", "v", "t"), now, []string{"term"})
	if err != nil || write.Claim.ClaimToken != "t" || write.Scope.ViewerSystemAccountID != "v" || write.Item.AccountID != "a" || write.Now != now || len(write.SearchTerms) != 1 {
		t.Fatalf("投影写入字段不正确: %#v %v", write, err)
	}
	if _, err := BuildProjectionWrite(w7dScopeFor("a", "v"), ProjectionItem{AccountID: "a"}, w7dClaimFor("a", "v", "t"), now, nil); err == nil {
		t.Fatal("空投影状态必须拒绝")
	}
}
