package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 账户列表可用性读模型维护，逐语义对齐 Node
// modules/accounts/account-list-availability-projection.service.ts：
// PostgreSQL-only 物化器；dirty/缺失行在本次 worker 提交完整替换载荷前
// 保持不可用。批处理语义（progressed 判定、每轮批次数、并发 worker）、
// claim→scope 按 viewer 分组、删除型 claim、重放退避与全部边界校验
// 与 Node 一致。DB 访问经 port 注入，DB 双模由具体仓储实现。

const (
	listAvailabilityMaxBatchSize             = 100
	listAvailabilityDefaultBatchesPerRun     = 200
	listAvailabilityMaxBatchesPerRun         = 400
	listAvailabilityDefaultWorkerConcurrency = 4
	listAvailabilityMaxWorkerConcurrency     = 8
	listAvailabilityDefaultLeaseMS           = 30_000
	listAvailabilityMinLeaseMS               = 1_000
	listAvailabilityMaxLeaseMS               = 60 * 60_000
	listAvailabilityMaxOverlayEntries        = listAvailabilityMaxBatchSize
)

// ListAvailabilityMaintenanceResult 计数字段与 Node 一致。
type ListAvailabilityMaintenanceResult struct {
	RuntimeDependencyUnavailable int `json:"runtimeDependencyUnavailable"`
	RuntimeRecoveryEnqueued      int `json:"runtimeRecoveryEnqueued"`
	RuntimeRecoveryCompleted     int `json:"runtimeRecoveryCompleted"`
	RuntimeOverlayReconciled     int `json:"runtimeOverlayReconciled"`
	ViewerHealthBootstrapped     int `json:"viewerHealthBootstrapped"`
	Bootstrapped                 int `json:"bootstrapped"`
	DueEnqueued                  int `json:"dueEnqueued"`
	StaleEnqueued                int `json:"staleEnqueued"`
	Claimed                      int `json:"claimed"`
	Projected                    int `json:"projected"`
	Deleted                      int `json:"deleted"`
	StaleClaims                  int `json:"staleClaims"`
	Released                     int `json:"released"`
}

func emptyListAvailabilityMaintenanceResult() ListAvailabilityMaintenanceResult {
	return ListAvailabilityMaintenanceResult{}
}

func addListAvailabilityResult(target *ListAvailabilityMaintenanceResult, source ListAvailabilityMaintenanceResult) {
	target.RuntimeDependencyUnavailable += source.RuntimeDependencyUnavailable
	target.RuntimeRecoveryEnqueued += source.RuntimeRecoveryEnqueued
	target.RuntimeRecoveryCompleted += source.RuntimeRecoveryCompleted
	target.RuntimeOverlayReconciled += source.RuntimeOverlayReconciled
	target.ViewerHealthBootstrapped += source.ViewerHealthBootstrapped
	target.Bootstrapped += source.Bootstrapped
	target.DueEnqueued += source.DueEnqueued
	target.StaleEnqueued += source.StaleEnqueued
	target.Claimed += source.Claimed
	target.Projected += source.Projected
	target.Deleted += source.Deleted
	target.StaleClaims += source.StaleClaims
	target.Released += source.Released
}

// DirtyClaim 是 dirty 队列认领行。
type DirtyClaim struct {
	AccountID             string `json:"account_id"`
	ViewerSystemAccountID string `json:"viewer_system_account_id"`
	Generation            int64  `json:"generation"`
	ClaimToken            string `json:"claim_token"`
	AttemptCount          int    `json:"attempt_count"`
}

// ProjectionScope 是账户可见范围行。
type ProjectionScope struct {
	AccountID             string  `json:"account_id"`
	ViewerSystemAccountID string  `json:"viewer_system_account_id"`
	CreatedAt             *string `json:"created_at,omitempty"`
}

// ProjectionItem 是 loader 物化出的列表项投影（payload 由网关域组装）。
type ProjectionItem struct {
	AccountID                 string
	EffectiveStatus           string // 唯一投影状态（rate_limited 等，由网关分类）
	CurrentConcurrency        int
	SourceAccountID           string // authorization instance source account
	AuthorizationID           string
	ProviderCode              string
	ProviderProtocolProfileID string
	AccountType               string
	BoundGroupID              string
	Name                      string
	Priority                  int
	SuperPriorityEnabled      bool
	FallbackEnabled           bool
	ConcurrencyLimit          int
	AccountExpiresAt          string
	LastUsedAt                string
	Payload                   map[string]any
	TagIDs                    []string
	// NextTransitionCandidates 是候选状态边界（RFC3339）。
	NextTransitionCandidates []string
	// EffectiveAvailable 对齐 Node schedulableBucket 的
	// item.effectiveAvailability.available 输入；nil 时回退
	// payload["effectiveAvailable"]，再回退 true（port 既有默认）。
	EffectiveAvailable *bool
}

// ProjectionWrite 是完整替换载荷。
type ProjectionWrite struct {
	Claim       DirtyClaim
	Scope       ProjectionScope
	Item        ProjectionItem
	SearchTerms []string
	Now         time.Time
}

// ListAvailabilityRepo 是投影持久化 port。
type ListAvailabilityRepo interface {
	EnsureRuntimeDependency(ctx context.Context, updatedAt string) error
	MarkRuntimeDependencyUnavailable(ctx context.Context, reason, updatedAt string) error
	BeginRuntimeDependencyRecovery(ctx context.Context, updatedAt string) (bool, error)
	TouchRuntimeDependency(ctx context.Context, updatedAt string) error
	EnqueueAllForRuntimeRecovery(ctx context.Context, nowMS int64) (int, error)
	CompleteRuntimeDependencyRecovery(ctx context.Context, updatedAt string) (bool, error)
	EnsureViewerHealth(ctx context.Context, limit int, updatedAt string) (int, error)
	EnqueueMissing(ctx context.Context, limit int, nowMS int64) (int, error)
	EnqueueDue(ctx context.Context, limit int, nowMS int64) (int, error)
	ListViewerHealthRefreshCandidates(ctx context.Context, limit int) ([]string, error)
	RefreshViewerHealth(ctx context.Context, viewerSystemAccountID, updatedAt string) error
	ClaimDirty(ctx context.Context, ownerID string, limit int, leaseMS, nowMS int64) ([]DirtyClaim, error)
	ListScopes(ctx context.Context, accountIDs []string) ([]ProjectionScope, error)
	LoadSearchTerms(ctx context.Context, accountIDs []string) (map[string][]string, error)
	ApplyClaims(ctx context.Context, writes []ProjectionWrite) (map[string]bool, error)
	ApplyDeletionClaim(ctx context.Context, claim DirtyClaim) (bool, error)
	ReleaseForReplay(ctx context.Context, input ListAvailabilityReplayInput) (bool, error)
}

// ListAvailabilityReplayInput 是重放释放输入。
type ListAvailabilityReplayInput struct {
	AccountID    string
	Generation   int64
	ClaimToken   string
	Reason       string
	RetryDelayMS int64
	NowMS        int64
}

// RuntimeDependencyProbe 探测运行态可用性（fail closed）。
type RuntimeDependencyProbe interface {
	Probe(ctx context.Context) (runtimeAvailabilityAvailable, concurrencyAvailable bool, err error)
}

// OverlayEntry 是并发 overlay 的 dirty 事件。
type OverlayEntry struct {
	AccountID       string
	NextReconcileAt *string
}

// OverlaySnapshot 是 Redis 并发快照。
type OverlaySnapshot struct {
	AccountID          string
	CurrentConcurrency int
	NextReconcileAt    *string
}

// OverlayReconciler 是 Redis overlay 对账 port。
type OverlayReconciler interface {
	ListDirtyEntries(ctx context.Context, limit int) ([]OverlayEntry, error)
	Acknowledge(ctx context.Context, entries []OverlayEntry) error
	LoadSnapshots(ctx context.Context, accountIDs []string) ([]OverlaySnapshot, error)
	UpsertOverlays(ctx context.Context, overlays []OverlayUpsert) error
	ExistingAccountIDs(ctx context.Context, accountIDs []string) (map[string]struct{}, error)
}

// OverlayUpsert 是 overlay 持久化行。
type OverlayUpsert struct {
	AccountID          string
	CurrentConcurrency int
	ObservedAt         string
	NextReconcileAt    *string
}

// ItemLoader 按可见范围加载物化列表项（网关域职责）。
type ItemLoader func(ctx context.Context, viewerSystemAccountID string, accountIDs []string) ([]ProjectionItem, error)

// AvailabilityScheduleSyncer 对齐 syncAccountAvailabilityScheduleStatusesAsync。
type AvailabilityScheduleSyncer func(ctx context.Context, nowMS int64) error

// ListAvailabilityOptions 全部可注入依赖。
type ListAvailabilityOptions struct {
	OwnerID           string
	BatchSize         int
	LeaseMS           int
	MaxBatchesPerRun  int
	WorkerConcurrency int
	NowMS             func() int64
	Repo              ListAvailabilityRepo
	RuntimeProbe      RuntimeDependencyProbe
	Overlays          OverlayReconciler
	LoadItems         ItemLoader
	SyncSchedules     AvailabilityScheduleSyncer
}

// ProjectionReplayDelayMS 对齐 projectionReplayDelayMs：
// 1s * 2^(clamp(attempt-1,0,6))，上限 60s。
func ProjectionReplayDelayMS(attemptCount int) int64 {
	exponent := attemptCount - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 6 {
		exponent = 6
	}
	delay := int64(1_000) << uint(exponent)
	return min64(delay, 60_000)
}

func boundedProjectionBatchSize(value int) (int, error) {
	if value < 1 || value > listAvailabilityMaxBatchSize {
		return 0, fmt.Errorf("账户列表投影批量大小必须在 1-%d 之间", listAvailabilityMaxBatchSize)
	}
	return value, nil
}

func boundedProjectionLeaseMS(value int) (int, error) {
	if value < listAvailabilityMinLeaseMS || value > listAvailabilityMaxLeaseMS {
		return 0, errors.New("账户列表投影租约必须在 1000-3600000ms 之间")
	}
	return value, nil
}

func boundedProjectionBatchesPerRun(value int) (int, error) {
	if value < 1 || value > listAvailabilityMaxBatchesPerRun {
		return 0, fmt.Errorf("账户列表投影每轮批次数必须在 1-%d 之间", listAvailabilityMaxBatchesPerRun)
	}
	return value, nil
}

func boundedProjectionWorkerConcurrency(value int) (int, error) {
	if value < 1 || value > listAvailabilityMaxWorkerConcurrency {
		return 0, fmt.Errorf("账户列表投影 worker 并发必须在 1-%d 之间", listAvailabilityMaxWorkerConcurrency)
	}
	return value, nil
}

func requiredProjectionOwnerID(value string) (string, error) {
	ownerID := strings.TrimSpace(value)
	if ownerID == "" || len(ownerID) > 128 {
		return "", errors.New("账户列表投影 ownerId 无效")
	}
	return ownerID, nil
}

// SchedulableBucket 对齐 schedulableBucket。
func SchedulableBucket(effectiveStatus string, effectiveAvailable bool) string {
	if effectiveStatus == "rate_limited" || effectiveStatus == "temporary_unavailable" {
		return "cooling"
	}
	if effectiveAvailable {
		return "enabled"
	}
	return "disabled"
}

// NextTransitionAtRFC3339 对齐 nextTransitionAt：全部候选过滤为严格未来时间
// 后按字典序取最早（RFC3339 UTC 同构可比）。
func NextTransitionAtRFC3339(candidates []string, nowMS int64) (string, bool) {
	var future []string
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		timestamp, ok := parseRFC3339Millis(candidate)
		if !ok || timestamp <= nowMS {
			continue
		}
		future = append(future, candidate)
	}
	if len(future) == 0 {
		return "", false
	}
	sort.Strings(future)
	return future[0], true
}

func parseRFC3339Millis(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// BuildProjectionWrite 对齐 accountListAvailabilityProjectionWrite。
func BuildProjectionWrite(scope ProjectionScope, item ProjectionItem, claim DirtyClaim, now time.Time, searchTerms []string) (ProjectionWrite, error) {
	if item.EffectiveStatus == "" {
		return ProjectionWrite{}, fmt.Errorf("账户 %s 无法归类为唯一投影状态", item.AccountID)
	}
	return ProjectionWrite{
		Claim:       claim,
		Scope:       scope,
		Item:        item,
		SearchTerms: searchTerms,
		Now:         now,
	}, nil
}

// RunListAvailabilityMaintenance 执行一批次维护聚合。
// driverPostgres=false 时与 Node 一致返回空结果（非 PG 不物化）。
func RunListAvailabilityMaintenance(ctx context.Context, opts ListAvailabilityOptions, driverPostgres bool) (ListAvailabilityMaintenanceResult, error) {
	if !driverPostgres {
		return emptyListAvailabilityMaintenanceResult(), nil
	}
	maxBatchesPerRun := opts.MaxBatchesPerRun
	if maxBatchesPerRun == 0 {
		maxBatchesPerRun = listAvailabilityDefaultBatchesPerRun
	}
	batchesPerRun, err := boundedProjectionBatchesPerRun(maxBatchesPerRun)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	workerConcurrency := opts.WorkerConcurrency
	if workerConcurrency == 0 {
		workerConcurrency = listAvailabilityDefaultWorkerConcurrency
	}
	if !driverPostgres {
		// Node：SQLite 单 writer，批量维护 worker 并发固定为 1
		// （workerConcurrency 仅在 PostgreSQL SKIP LOCKED 路径生效）。
		workerConcurrency = 1
	}
	concurrency, err := boundedProjectionWorkerConcurrency(workerConcurrency)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	var aggregate ListAvailabilityMaintenanceResult
	for index := 0; index < batchesPerRun; index += concurrency {
		batchCount := min(concurrency, batchesPerRun-index)
		if err := ctx.Err(); err != nil {
			return aggregate, err
		}
		var (
			batchMu   sync.Mutex
			batches   []ListAvailabilityMaintenanceResult
			batchErrs []error
			wg        sync.WaitGroup
		)
		for i := 0; i < batchCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				batch, batchErr := runListAvailabilityMaintenanceBatch(ctx, opts)
				batchMu.Lock()
				defer batchMu.Unlock()
				batches = append(batches, batch)
				if batchErr != nil {
					batchErrs = append(batchErrs, batchErr)
				}
			}()
		}
		wg.Wait()
		if len(batchErrs) > 0 {
			return aggregate, errors.Join(batchErrs...)
		}
		progressed := false
		for _, batch := range batches {
			addListAvailabilityResult(&aggregate, batch)
			if batch.ViewerHealthBootstrapped != 0 || batch.Bootstrapped != 0 ||
				batch.DueEnqueued != 0 || batch.StaleEnqueued != 0 || batch.Claimed != 0 {
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return aggregate, nil
}

func runListAvailabilityMaintenanceBatch(ctx context.Context, opts ListAvailabilityOptions) (ListAvailabilityMaintenanceResult, error) {
	ownerID, err := requiredProjectionOwnerID(opts.OwnerID)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = listAvailabilityMaxBatchSize
	}
	batchSize, err = boundedProjectionBatchSize(batchSize)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	leaseMS := opts.LeaseMS
	if leaseMS == 0 {
		leaseMS = listAvailabilityDefaultLeaseMS
	}
	leaseMS, err = boundedProjectionLeaseMS(leaseMS)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	if opts.NowMS == nil || opts.Repo == nil || opts.LoadItems == nil {
		return ListAvailabilityMaintenanceResult{}, errors.New("账户列表投影依赖未初始化")
	}
	nowMS := opts.NowMS()
	now := time.UnixMilli(nowMS).UTC()
	updatedAt := now.Format(time.RFC3339Nano)
	repo := opts.Repo

	var (
		runtimeOverlayReconciled int
		runtimeRecoveryEnqueued  int
	)
	if err := repo.EnsureRuntimeDependency(ctx, updatedAt); err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	if opts.RuntimeProbe != nil {
		availabilityAvailable, concurrencyAvailable, probeErr := opts.RuntimeProbe.Probe(ctx)
		if probeErr != nil {
			return ListAvailabilityMaintenanceResult{}, probeErr
		}
		if !availabilityAvailable || !concurrencyAvailable {
			reason := "account_runtime_availability_unavailable"
			if availabilityAvailable {
				reason = "account_concurrency_runtime_unavailable"
			}
			if err := repo.MarkRuntimeDependencyUnavailable(ctx, reason, updatedAt); err != nil {
				return ListAvailabilityMaintenanceResult{}, err
			}
			result := emptyListAvailabilityMaintenanceResult()
			result.RuntimeDependencyUnavailable = 1
			return result, nil
		}
		if opts.Overlays != nil {
			runtimeOverlayReconciled, err = reconcileRuntimeOverlays(ctx, opts.Overlays)
			if err != nil {
				if markErr := repo.MarkRuntimeDependencyUnavailable(ctx, "account_concurrency_overlay_reconcile_failed", updatedAt); markErr != nil {
					return ListAvailabilityMaintenanceResult{}, errors.Join(err, markErr)
				}
				result := emptyListAvailabilityMaintenanceResult()
				result.RuntimeDependencyUnavailable = 1
				return result, nil
			}
		}
		runtimeRecoveryStarted, err := repo.BeginRuntimeDependencyRecovery(ctx, updatedAt)
		if err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
		if !runtimeRecoveryStarted {
			if err := repo.TouchRuntimeDependency(ctx, updatedAt); err != nil {
				return ListAvailabilityMaintenanceResult{}, err
			}
		} else if runtimeRecoveryEnqueued, err = repo.EnqueueAllForRuntimeRecovery(ctx, nowMS); err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
		// 调度边界变化会改持久化状态：在认领投影刷新前应用全部到期转移。
		if opts.SyncSchedules != nil {
			if err := opts.SyncSchedules(ctx, nowMS); err != nil {
				return ListAvailabilityMaintenanceResult{}, err
			}
		}
	}

	viewerHealthBootstrapped, err := repo.EnsureViewerHealth(ctx, batchSize, updatedAt)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	bootstrapped, err := repo.EnqueueMissing(ctx, batchSize, nowMS)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	dueEnqueued, err := repo.EnqueueDue(ctx, batchSize, nowMS)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	viewerCandidates, err := repo.ListViewerHealthRefreshCandidates(ctx, batchSize)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	viewersToRefresh := map[string]struct{}{}
	for _, viewer := range viewerCandidates {
		viewersToRefresh[viewer] = struct{}{}
	}

	claims, err := repo.ClaimDirty(ctx, ownerID, batchSize, int64(leaseMS), nowMS)
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	if len(claims) == 0 {
		for viewer := range viewersToRefresh {
			if err := repo.RefreshViewerHealth(ctx, viewer, updatedAt); err != nil {
				return ListAvailabilityMaintenanceResult{}, err
			}
		}
		runtimeRecoveryCompleted := 0
		if opts.RuntimeProbe != nil {
			if completed, err := repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt); err != nil {
				return ListAvailabilityMaintenanceResult{}, err
			} else if completed {
				runtimeRecoveryCompleted = 1
			}
		}
		result := emptyListAvailabilityMaintenanceResult()
		result.RuntimeRecoveryEnqueued = runtimeRecoveryEnqueued
		result.RuntimeRecoveryCompleted = runtimeRecoveryCompleted
		result.RuntimeOverlayReconciled = runtimeOverlayReconciled
		result.ViewerHealthBootstrapped = viewerHealthBootstrapped
		result.Bootstrapped = bootstrapped
		result.DueEnqueued = dueEnqueued
		return result, nil
	}

	scopes, err := repo.ListScopes(ctx, claimAccountIDs(claims))
	if err != nil {
		return ListAvailabilityMaintenanceResult{}, err
	}
	scopeByAccountID := map[string]ProjectionScope{}
	for _, scope := range scopes {
		scopeByAccountID[scope.AccountID] = scope
	}
	claimsByViewer := map[string][]DirtyClaim{}
	claimsInOrder := []string{}
	for _, claim := range claims {
		scope, found := scopeByAccountID[claim.AccountID]
		if !found {
			continue
		}
		if _, exists := claimsByViewer[scope.ViewerSystemAccountID]; !exists {
			claimsInOrder = append(claimsInOrder, scope.ViewerSystemAccountID)
		}
		claimsByViewer[scope.ViewerSystemAccountID] = append(claimsByViewer[scope.ViewerSystemAccountID], claim)
	}

	projected := 0
	deleted := 0
	staleClaims := 0
	released := 0
	for _, viewerSystemAccountID := range claimsInOrder {
		if err := ctx.Err(); err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
		viewerClaims := claimsByViewer[viewerSystemAccountID]
		viewersToRefresh[viewerSystemAccountID] = struct{}{}
		projectedThisViewer, releasedThisViewer, staleThisViewer, batchErr := projectClaimsForViewer(ctx, repo, opts, viewerSystemAccountID, viewerClaims, scopeByAccountID, now, nowMS)
		projected += projectedThisViewer
		released += releasedThisViewer
		staleClaims += staleThisViewer
		if batchErr != nil {
			// Node 行为：批量物化失败标记运行态依赖不可用，并把该 viewer
			// 未完成 claim 释放为重放后继续处理其余 viewer。
			if markErr := repo.MarkRuntimeDependencyUnavailable(ctx, "projection_runtime_materialization_failed",
				time.UnixMilli(opts.NowMS()).UTC().Format(time.RFC3339Nano)); markErr != nil {
				return ListAvailabilityMaintenanceResult{}, errors.Join(batchErr, markErr)
			}
			continue
		}
	}

	for _, claim := range claims {
		if _, found := scopeByAccountID[claim.AccountID]; found {
			continue
		}
		if err := ctx.Err(); err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
		applied, err := repo.ApplyDeletionClaim(ctx, claim)
		if err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
		if applied {
			deleted++
		} else {
			staleClaims++
		}
		viewersToRefresh[claim.ViewerSystemAccountID] = struct{}{}
	}

	updatedAt = time.UnixMilli(opts.NowMS()).UTC().Format(time.RFC3339Nano)
	for viewer := range viewersToRefresh {
		if err := ctx.Err(); err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
		if err := repo.RefreshViewerHealth(ctx, viewer, updatedAt); err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		}
	}

	runtimeRecoveryCompleted := 0
	if opts.RuntimeProbe != nil {
		if completed, err := repo.CompleteRuntimeDependencyRecovery(ctx, updatedAt); err != nil {
			return ListAvailabilityMaintenanceResult{}, err
		} else if completed {
			runtimeRecoveryCompleted = 1
		}
	}

	return ListAvailabilityMaintenanceResult{
		RuntimeRecoveryEnqueued:  runtimeRecoveryEnqueued,
		RuntimeRecoveryCompleted: runtimeRecoveryCompleted,
		RuntimeOverlayReconciled: runtimeOverlayReconciled,
		ViewerHealthBootstrapped: viewerHealthBootstrapped,
		Bootstrapped:             bootstrapped,
		DueEnqueued:              dueEnqueued,
		Claimed:                  len(claims),
		Projected:                projected,
		Deleted:                  deleted,
		StaleClaims:              staleClaims,
		Released:                 released,
	}, nil
}

func projectClaimsForViewer(
	ctx context.Context,
	repo ListAvailabilityRepo,
	opts ListAvailabilityOptions,
	viewerSystemAccountID string,
	viewerClaims []DirtyClaim,
	scopeByAccountID map[string]ProjectionScope,
	now time.Time,
	nowMS int64,
) (projected, released, staleClaims int, err error) {
	scopesForViewer := make([]ProjectionScope, 0, len(viewerClaims))
	for _, claim := range viewerClaims {
		scopesForViewer = append(scopesForViewer, scopeByAccountID[claim.AccountID])
	}
	_ = scopesForViewer
	accountIDs := make([]string, 0, len(viewerClaims))
	for _, claim := range viewerClaims {
		accountIDs = append(accountIDs, claim.AccountID)
	}
	searchTermsByAccount, searchErr := repo.LoadSearchTerms(ctx, accountIDs)
	if searchErr != nil {
		return 0, 0, 0, searchErr
	}
	items, loadErr := opts.LoadItems(ctx, viewerSystemAccountID, accountIDs)
	if loadErr != nil {
		// Node：先释放 claim 为重放，再把原始错误交给上层标记依赖不可用。
		released, releaseErr := releaseClaimsForReplay(ctx, repo, viewerClaims, nowMS)
		if releaseErr != nil {
			return 0, released, 0, errors.Join(loadErr, releaseErr)
		}
		return 0, released, 0, loadErr
	}
	itemByID := map[string]ProjectionItem{}
	for _, item := range items {
		itemByID[item.AccountID] = item
	}
	writes := make([]ProjectionWrite, 0, len(viewerClaims))
	for _, claim := range viewerClaims {
		item, found := itemByID[claim.AccountID]
		if !found {
			released, releaseErr := releaseClaimsForReplay(ctx, repo, viewerClaims, nowMS)
			return 0, released, 0, errors.Join(fmt.Errorf("账户列表投影账户 %s 在当前可见范围中缺失", claim.AccountID), releaseErr)
		}
		write, writeErr := BuildProjectionWrite(scopeByAccountID[claim.AccountID], item, claim, now, searchTermsByAccount[item.AccountID])
		if writeErr != nil {
			released, releaseErr := releaseClaimsForReplay(ctx, repo, viewerClaims, nowMS)
			return 0, released, 0, errors.Join(writeErr, releaseErr)
		}
		writes = append(writes, write)
	}
	appliedByClaimToken, applyErr := repo.ApplyClaims(ctx, writes)
	if applyErr != nil {
		released, releaseErr := releaseClaimsForReplay(ctx, repo, viewerClaims, nowMS)
		return 0, released, 0, errors.Join(applyErr, releaseErr)
	}
	for _, claim := range viewerClaims {
		if appliedByClaimToken[claim.ClaimToken] {
			projected++
		} else {
			staleClaims++
		}
	}
	return projected, 0, staleClaims, nil
}

// releaseClaimsForReplay 对齐失败路径：未完成 claim 释放为重放并按尝试次数退避。
func releaseClaimsForReplay(ctx context.Context, repo ListAvailabilityRepo, claims []DirtyClaim, nowMS int64) (released int, err error) {
	for _, claim := range claims {
		replayed, releaseErr := repo.ReleaseForReplay(ctx, ListAvailabilityReplayInput{
			AccountID:    claim.AccountID,
			Generation:   claim.Generation,
			ClaimToken:   claim.ClaimToken,
			Reason:       "projection_refresh_failed",
			RetryDelayMS: ProjectionReplayDelayMS(claim.AttemptCount),
			NowMS:        nowMS,
		})
		if releaseErr != nil {
			return released, releaseErr
		}
		if replayed {
			released++
		}
	}
	return released, nil
}

// reconcileRuntimeOverlays 对齐 reconcileAccountListAvailabilityRuntimeOverlays：
// Redis 只被本后台对账读取；lease release 与硬删除竞态时仅确认可安全证明的
// tombstone。
func reconcileRuntimeOverlays(ctx context.Context, overlays OverlayReconciler) (int, error) {
	entries, err := overlays.ListDirtyEntries(ctx, listAvailabilityMaxOverlayEntries)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	accountIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		accountIDs = append(accountIDs, entry.AccountID)
	}
	existing, err := overlays.ExistingAccountIDs(ctx, accountIDs)
	if err != nil {
		return 0, err
	}
	var staleEntries, activeEntries []OverlayEntry
	for _, entry := range entries {
		if _, found := existing[entry.AccountID]; found {
			activeEntries = append(activeEntries, entry)
		} else {
			staleEntries = append(staleEntries, entry)
		}
	}
	if len(staleEntries) > 0 {
		if err := overlays.Acknowledge(ctx, staleEntries); err != nil {
			return 0, err
		}
	}
	if len(activeEntries) == 0 {
		return len(entries), nil
	}
	snapshots, err := overlays.LoadSnapshots(ctx, activeAccountIDs(activeEntries))
	if err != nil {
		return 0, err
	}
	snapshotByAccountID := map[string]OverlaySnapshot{}
	for _, snapshot := range snapshots {
		snapshotByAccountID[snapshot.AccountID] = snapshot
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	upserts := make([]OverlayUpsert, 0, len(activeEntries))
	for _, entry := range activeEntries {
		snapshot, found := snapshotByAccountID[entry.AccountID]
		if !found {
			return 0, fmt.Errorf("账户并发 overlay 缺少 Redis 快照: %s", entry.AccountID)
		}
		upserts = append(upserts, OverlayUpsert{
			AccountID:          entry.AccountID,
			CurrentConcurrency: snapshot.CurrentConcurrency,
			ObservedAt:         observedAt,
			NextReconcileAt:    snapshot.NextReconcileAt,
		})
	}
	if err := overlays.UpsertOverlays(ctx, upserts); err != nil {
		return 0, err
	}
	acked := make([]OverlayEntry, 0, len(activeEntries))
	for _, entry := range activeEntries {
		entry.NextReconcileAt = snapshotByAccountID[entry.AccountID].NextReconcileAt
		acked = append(acked, entry)
	}
	if err := overlays.Acknowledge(ctx, acked); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func claimAccountIDs(claims []DirtyClaim) []string {
	ids := make([]string, 0, len(claims))
	for _, claim := range claims {
		ids = append(ids, claim.AccountID)
	}
	return ids
}

func activeAccountIDs(entries []OverlayEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.AccountID)
	}
	return ids
}
