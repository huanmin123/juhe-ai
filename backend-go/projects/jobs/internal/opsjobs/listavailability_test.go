package opsjobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectionReplayDelayMS(t *testing.T) {
	cases := []struct {
		attempt int
		want    int64
	}{
		{0, 1_000},
		{1, 1_000},
		{2, 2_000},
		{3, 4_000},
		{7, 60_000},
		{99, 60_000},
	}
	for _, tc := range cases {
		if got := ProjectionReplayDelayMS(tc.attempt); got != tc.want {
			t.Fatalf("ProjectionReplayDelayMS(%d) = %d, want %d", tc.attempt, got, tc.want)
		}
	}
}

func TestListAvailabilityValidators(t *testing.T) {
	if _, err := boundedProjectionBatchSize(0); err == nil {
		t.Fatal("batchSize 下界")
	}
	if _, err := boundedProjectionBatchSize(101); err == nil {
		t.Fatal("batchSize 上界")
	}
	if _, err := boundedProjectionLeaseMS(999); err == nil {
		t.Fatal("lease 下界")
	}
	if _, err := boundedProjectionLeaseMS(60*60_000 + 1); err == nil {
		t.Fatal("lease 上界")
	}
	if _, err := boundedProjectionBatchesPerRun(401); err == nil {
		t.Fatal("batchesPerRun 上界")
	}
	if _, err := boundedProjectionWorkerConcurrency(9); err == nil {
		t.Fatal("worker 并发上界")
	}
	if _, err := requiredProjectionOwnerID("  "); err == nil {
		t.Fatal("空 ownerId 应报错")
	}
	if _, err := requiredProjectionOwnerID(stringsRepeat("x", 129)); err == nil {
		t.Fatal("超长 ownerId 应报错")
	}
}

func stringsRepeat(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}

func TestSchedulableBucketMatrix(t *testing.T) {
	cases := []struct {
		status    string
		available bool
		want      string
	}{
		{"rate_limited", true, "cooling"},
		{"temporary_unavailable", true, "cooling"},
		{"active", true, "enabled"},
		{"active", false, "disabled"},
	}
	for _, tc := range cases {
		if got := SchedulableBucket(tc.status, tc.available); got != tc.want {
			t.Fatalf("SchedulableBucket(%s,%v) = %s, want %s", tc.status, tc.available, got, tc.want)
		}
	}
}

func TestNextTransitionAtRFC3339(t *testing.T) {
	nowMS := parse("2030-01-01T00:00:00Z")
	cases := []struct {
		name       string
		candidates []string
		want       string
		ok         bool
	}{
		{"取最早未来候选", []string{"2030-01-02T00:00:00Z", "2030-01-01T12:00:00Z", "2030-01-03T00:00:00Z"}, "2030-01-01T12:00:00Z", true},
		{"过滤过去与无效", []string{"2029-12-31T00:00:00Z", "bogus", "2030-01-01T01:00:00Z"}, "2030-01-01T01:00:00Z", true},
		{"全为过去", []string{"2029-12-31T00:00:00Z"}, "", false},
		{"空候选", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NextTransitionAtRFC3339(tc.candidates, nowMS)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got %q %v, want %q %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func parse(value string) int64 {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed.UnixMilli()
}

// ---- 引擎测试：内存 repo ----

type fakeListAvailabilityRepo struct {
	claims               []DirtyClaim
	claimsDrained        bool
	scopes               map[string]ProjectionScope
	items                map[string]ProjectionItem
	applyResults         map[string]bool
	applyErr             error
	markUnavailable      []string
	released             []ListAvailabilityReplayInput
	recoveryStarted      bool
	recoveryCompleted    int
	refreshed            []string
	enqueueCount         int
	recoveryEnqueueCount int
}

func (f *fakeListAvailabilityRepo) EnsureRuntimeDependency(context.Context, string) error { return nil }

func (f *fakeListAvailabilityRepo) MarkRuntimeDependencyUnavailable(_ context.Context, reason, _ string) error {
	f.markUnavailable = append(f.markUnavailable, reason)
	return nil
}

func (f *fakeListAvailabilityRepo) BeginRuntimeDependencyRecovery(context.Context, string) (bool, error) {
	return f.recoveryStarted, nil
}

func (f *fakeListAvailabilityRepo) TouchRuntimeDependency(context.Context, string) error { return nil }

func (f *fakeListAvailabilityRepo) EnqueueAllForRuntimeRecovery(context.Context, int64) (int, error) {
	f.recoveryEnqueueCount++
	if f.recoveryEnqueueCount > 1 {
		return 0, nil
	}
	return 3, nil
}

func (f *fakeListAvailabilityRepo) CompleteRuntimeDependencyRecovery(context.Context, string) (bool, error) {
	f.recoveryCompleted++
	return f.recoveryCompleted == 1, nil
}

func (f *fakeListAvailabilityRepo) EnsureViewerHealth(context.Context, int, string) (int, error) {
	f.enqueueCount++
	if f.enqueueCount > 1 {
		return 0, nil
	}
	return 1, nil
}

func (f *fakeListAvailabilityRepo) EnqueueMissing(_ context.Context, _ int, _ int64) (int, error) {
	f.enqueueCount++
	if f.enqueueCount > 2 {
		return 0, nil
	}
	return 2, nil
}

func (f *fakeListAvailabilityRepo) EnqueueDue(_ context.Context, _ int, _ int64) (int, error) {
	f.enqueueCount++
	if f.enqueueCount > 3 {
		return 0, nil
	}
	return 5, nil
}

func (f *fakeListAvailabilityRepo) ListViewerHealthRefreshCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}

func (f *fakeListAvailabilityRepo) RefreshViewerHealth(_ context.Context, viewer, _ string) error {
	f.refreshed = append(f.refreshed, viewer)
	return nil
}

func (f *fakeListAvailabilityRepo) ClaimDirty(context.Context, string, int, int64, int64) ([]DirtyClaim, error) {
	if f.claimsDrained {
		return nil, nil
	}
	f.claimsDrained = true
	return f.claims, nil
}

func (f *fakeListAvailabilityRepo) ListScopes(_ context.Context, accountIDs []string) ([]ProjectionScope, error) {
	var scopes []ProjectionScope
	for _, id := range accountIDs {
		if scope, found := f.scopes[id]; found {
			scopes = append(scopes, scope)
		}
	}
	return scopes, nil
}

func (f *fakeListAvailabilityRepo) LoadSearchTerms(_ context.Context, accountIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	for _, id := range accountIDs {
		result[id] = []string{"term-" + id}
	}
	return result, nil
}

func (f *fakeListAvailabilityRepo) ApplyClaims(_ context.Context, writes []ProjectionWrite) (map[string]bool, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	result := map[string]bool{}
	for _, write := range writes {
		applied, exists := f.applyResults[write.Claim.ClaimToken]
		result[write.Claim.ClaimToken] = !exists || applied
	}
	return result, nil
}

func (f *fakeListAvailabilityRepo) ApplyDeletionClaim(_ context.Context, claim DirtyClaim) (bool, error) {
	return claim.ClaimToken != "", nil
}

func (f *fakeListAvailabilityRepo) ReleaseForReplay(_ context.Context, input ListAvailabilityReplayInput) (bool, error) {
	f.released = append(f.released, input)
	return true, nil
}

type staticRuntimeProbe struct{ available, concurrency bool }

func (s staticRuntimeProbe) Probe(context.Context) (bool, bool, error) {
	return s.available, s.concurrency, nil
}

type fakeOverlayReconciler struct {
	entries             []OverlayEntry
	existing            map[string]struct{}
	upserts             []OverlayUpsert
	acked               int
	listCalled          bool
	loadSnapshotsBroken bool
}

func (f *fakeOverlayReconciler) ListDirtyEntries(context.Context, int) ([]OverlayEntry, error) {
	if f.listCalled {
		return nil, nil
	}
	f.listCalled = true
	return f.entries, nil
}

func (f *fakeOverlayReconciler) Acknowledge(_ context.Context, entries []OverlayEntry) error {
	f.acked += len(entries)
	return nil
}

func (f *fakeOverlayReconciler) LoadSnapshots(_ context.Context, accountIDs []string) ([]OverlaySnapshot, error) {
	if f.loadSnapshotsBroken {
		return nil, errors.New("redis snapshot unavailable")
	}
	var snapshots []OverlaySnapshot
	for _, id := range accountIDs {
		snapshots = append(snapshots, OverlaySnapshot{AccountID: id, CurrentConcurrency: 2})
	}
	return snapshots, nil
}

func (f *fakeOverlayReconciler) UpsertOverlays(_ context.Context, overlays []OverlayUpsert) error {
	f.upserts = append(f.upserts, overlays...)
	return nil
}

func (f *fakeOverlayReconciler) ExistingAccountIDs(_ context.Context, accountIDs []string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for _, id := range accountIDs {
		if _, found := f.existing[id]; found {
			result[id] = struct{}{}
		}
	}
	return result, nil
}

func testListAvailabilityOptions(repo ListAvailabilityRepo, loader ItemLoader) ListAvailabilityOptions {
	return ListAvailabilityOptions{
		OwnerID:           "owner-1",
		BatchSize:         10,
		LeaseMS:           30_000,
		WorkerConcurrency: 1, // 单 worker：断言与单批次语义一一对应。
		NowMS:             func() int64 { return parse("2030-01-01T00:00:00Z") },
		Repo:              repo,
		RuntimeProbe:      staticRuntimeProbe{available: true, concurrency: true},
		Overlays:          &fakeOverlayReconciler{},
		LoadItems:         loader,
	}
}

func projectionItem(accountID string) ProjectionItem {
	return ProjectionItem{
		AccountID:       accountID,
		EffectiveStatus: "active",
		Name:            "账户",
		Payload:         map[string]any{"id": accountID},
	}
}

// 非 PG 驱动直接返回空结果（对齐 Node databaseDriver 门禁）。
func TestListAvailabilitySkipsNonPostgres(t *testing.T) {
	result, err := RunListAvailabilityMaintenance(context.Background(), ListAvailabilityOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != emptyListAvailabilityMaintenanceResult() {
		t.Fatalf("非 PG 应返回空结果: %+v", result)
	}
}

// 正常路径：claim→scope 分组→投影→viewer 刷新→recovery 完成。
func TestListAvailabilityHappyPath(t *testing.T) {
	repo := &fakeListAvailabilityRepo{
		recoveryStarted: true,
		claims: []DirtyClaim{
			{AccountID: "acc-1", ViewerSystemAccountID: "viewer-1", Generation: 1, ClaimToken: "t1", AttemptCount: 1},
			{AccountID: "acc-2", ViewerSystemAccountID: "viewer-1", Generation: 1, ClaimToken: "t2", AttemptCount: 1},
			{AccountID: "acc-3", ViewerSystemAccountID: "viewer-2", Generation: 1, ClaimToken: "t3", AttemptCount: 1},
			{AccountID: "acc-ghost", ViewerSystemAccountID: "viewer-1", Generation: 1, ClaimToken: "t4", AttemptCount: 1},
		},
		scopes: map[string]ProjectionScope{
			"acc-1": {AccountID: "acc-1", ViewerSystemAccountID: "viewer-1"},
			"acc-2": {AccountID: "acc-2", ViewerSystemAccountID: "viewer-1"},
			"acc-3": {AccountID: "acc-3", ViewerSystemAccountID: "viewer-2"},
		},
	}
	loader := func(_ context.Context, viewer string, accountIDs []string) ([]ProjectionItem, error) {
		items := make([]ProjectionItem, 0, len(accountIDs))
		for _, id := range accountIDs {
			items = append(items, projectionItem(id))
		}
		return items, nil
	}
	result, err := RunListAvailabilityMaintenance(context.Background(), testListAvailabilityOptions(repo, loader), true)
	if err != nil {
		t.Fatalf("维护失败: %v", err)
	}
	if result.Claimed != 4 || result.Projected != 3 || result.Deleted != 1 {
		t.Fatalf("计数不符: %+v", result)
	}
	if result.RuntimeRecoveryEnqueued != 3 || result.RuntimeRecoveryCompleted != 1 {
		t.Fatalf("recovery 计数不符: %+v", result)
	}
	if result.Bootstrapped != 2 || result.DueEnqueued != 5 || result.ViewerHealthBootstrapped != 1 {
		t.Fatalf("enqueue 计数不符: %+v", result)
	}
	if len(repo.refreshed) < 2 {
		t.Fatalf("应刷新 viewer 健康: %+v", repo.refreshed)
	}
}

// 批量物化失败：标记运行态依赖不可用 + claim 释放为重放（带退避）。
func TestListAvailabilityBatchFailureReleasesForReplay(t *testing.T) {
	repo := &fakeListAvailabilityRepo{
		claims: []DirtyClaim{
			{AccountID: "acc-1", ViewerSystemAccountID: "viewer-1", Generation: 4, ClaimToken: "t1", AttemptCount: 2},
		},
		scopes: map[string]ProjectionScope{
			"acc-1": {AccountID: "acc-1", ViewerSystemAccountID: "viewer-1"},
		},
	}
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) {
		return nil, errors.New("runtime snapshot unavailable")
	}
	result, err := RunListAvailabilityMaintenance(context.Background(), testListAvailabilityOptions(repo, loader), true)
	if err != nil {
		t.Fatalf("维护不应中断: %v", err)
	}
	if len(repo.markUnavailable) == 0 || repo.markUnavailable[0] != "projection_runtime_materialization_failed" {
		t.Fatalf("应标记 runtime 依赖不可用: %+v", repo.markUnavailable)
	}
	if len(repo.released) != 1 {
		t.Fatalf("应释放 1 条 claim: %+v", repo.released)
	}
	if repo.released[0].Reason != "projection_refresh_failed" || repo.released[0].RetryDelayMS != ProjectionReplayDelayMS(2) {
		t.Fatalf("重放参数不符: %+v", repo.released[0])
	}
	_ = result
}

// 账户在可见范围缺失：与 Node 一致抛错→标记依赖不可用→释放重放。
func TestListAvailabilityMissingScopeItemFailsClosed(t *testing.T) {
	repo := &fakeListAvailabilityRepo{
		claims: []DirtyClaim{
			{AccountID: "acc-1", ViewerSystemAccountID: "viewer-1", Generation: 1, ClaimToken: "t1", AttemptCount: 1},
		},
		scopes: map[string]ProjectionScope{
			"acc-1": {AccountID: "acc-1", ViewerSystemAccountID: "viewer-1"},
		},
	}
	loader := func(context.Context, string, []string) ([]ProjectionItem, error) {
		return nil, nil // 账户缺失
	}
	if _, err := RunListAvailabilityMaintenance(context.Background(), testListAvailabilityOptions(repo, loader), true); err != nil {
		t.Fatal(err)
	}
	if len(repo.markUnavailable) == 0 {
		t.Fatal("缺失账户应标记依赖不可用")
	}
	if len(repo.released) != 1 {
		t.Fatal("缺失账户应释放 claim")
	}
}

// 运行态依赖不可用：fail closed，不做任何投影。
func TestListAvailabilityRuntimeDependencyUnavailable(t *testing.T) {
	repo := &fakeListAvailabilityRepo{}
	opts := testListAvailabilityOptions(repo, func(context.Context, string, []string) ([]ProjectionItem, error) {
		t.Fatal("依赖不可用时不应加载条目")
		return nil, nil
	})
	opts.RuntimeProbe = staticRuntimeProbe{available: false, concurrency: true}
	result, err := RunListAvailabilityMaintenance(context.Background(), opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeDependencyUnavailable != 1 {
		t.Fatalf("RuntimeDependencyUnavailable = %d", result.RuntimeDependencyUnavailable)
	}
	if repo.markUnavailable[0] != "account_runtime_availability_unavailable" {
		t.Fatalf("reason = %s", repo.markUnavailable[0])
	}
}

// overlay 对账：tombstone 安全确认，缺失快照 fail closed。
func TestListAvailabilityOverlayReconcile(t *testing.T) {
	repo := &fakeListAvailabilityRepo{}
	overlays := &fakeOverlayReconciler{
		entries:  []OverlayEntry{{AccountID: "acc-live"}, {AccountID: "acc-deleted"}},
		existing: map[string]struct{}{"acc-live": {}},
	}
	opts := testListAvailabilityOptions(repo, func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil })
	opts.Overlays = overlays
	opts.RuntimeProbe = staticRuntimeProbe{available: true, concurrency: true}
	result, err := RunListAvailabilityMaintenance(context.Background(), opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if overlays.acked != 2 { // 1 tombstone + 1 active（带 nextReconcileAt 回填）
		t.Fatalf("ack 数 = %d", overlays.acked)
	}
	if len(overlays.upserts) != 1 || overlays.upserts[0].AccountID != "acc-live" {
		t.Fatalf("upserts = %+v", overlays.upserts)
	}
	_ = result

	// 活跃账户缺快照 → 标记依赖不可用（fail closed），不发布投影。
	badOverlays := &fakeOverlayReconciler{
		entries:             []OverlayEntry{{AccountID: "acc-live"}},
		existing:            map[string]struct{}{"acc-live": {}},
		loadSnapshotsBroken: true,
	}
	opts2 := testListAvailabilityOptions(repo, func(context.Context, string, []string) ([]ProjectionItem, error) { return nil, nil })
	opts2.Overlays = badOverlays
	result2, err := RunListAvailabilityMaintenance(context.Background(), opts2, true)
	if err != nil {
		t.Fatalf("overlay 失败不应向上抛错: %v", err)
	}
	if result2.RuntimeDependencyUnavailable != 1 {
		t.Fatalf("RuntimeDependencyUnavailable = %d", result2.RuntimeDependencyUnavailable)
	}
	if repo.markUnavailable[len(repo.markUnavailable)-1] != "account_concurrency_overlay_reconcile_failed" {
		t.Fatalf("应标记 overlay 对账失败: %+v", repo.markUnavailable)
	}
}
