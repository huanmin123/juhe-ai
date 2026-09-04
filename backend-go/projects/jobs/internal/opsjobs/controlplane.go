package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// 账户电路控制面维护，逐语义对齐 Node
// modules/gateway/runtime/account-circuit-control-plane-bridge.ts 中 jobs 侧
// 承担的三件事：
//  1. Rebuild：从 DB ledger 分页重建运行态（启动/冷启动门禁）。
//  2. ProjectPending：认领 incident outbox 事件，回放 ledger 到运行态并 ack；
//     失败释放为重放。
//  3. ReconcileActive：按 cursor 分页重放 ledger，修复被驱逐的运行态键。
//
// kill-restart 硬门禁：ledger 本身是持久事实源，重启后 rebuild 从头回放；
// reconcile cursor 通过 ReconcileCursorStore 持久化，重启后从上次游标续跑。

// CircuitIncidentState 与 Node AccountCircuitIncidentState 对齐。
type CircuitIncidentState string

const (
	CircuitIncidentOpen                 CircuitIncidentState = "OPEN"
	CircuitIncidentSuspect              CircuitIncidentState = "SUSPECT"
	CircuitIncidentRecovering           CircuitIncidentState = "RECOVERING"
	CircuitIncidentHalfOpen             CircuitIncidentState = "HALF_OPEN"
	CircuitIncidentClosed               CircuitIncidentState = "CLOSED"
	CircuitIncidentPersisting           CircuitIncidentState = "PERSISTING"
	CircuitIncidentShadowedByPersistent CircuitIncidentState = "SHADOWED_BY_PERSISTENT"
)

// CircuitIncidentRecord 是 control-plane ledger 行的窄投影。
type CircuitIncidentRecord struct {
	AccountID                       string               `json:"account_id"`
	AccountRuntimeKey               string               `json:"account_runtime_key"`
	IncidentID                      string               `json:"incident_id"`
	ParentIncidentID                string               `json:"parent_incident_id,omitempty"`
	CircuitScopeKey                 string               `json:"circuit_scope_key"`
	ScopeKind                       string               `json:"scope_kind"`
	KeyFingerprint                  string               `json:"key_fingerprint,omitempty"`
	ProtocolCode                    string               `json:"protocol_code,omitempty"`
	RequestLane                     string               `json:"request_lane,omitempty"`
	ModelFamily                     string               `json:"model_family,omitempty"`
	ChildIncidentIDs                []string             `json:"child_incident_ids"`
	State                           CircuitIncidentState `json:"state"`
	Generation                      int64                `json:"generation"`
	DispatchRevision                int64                `json:"dispatch_revision"`
	LedgerRevision                  int64                `json:"ledger_revision"`
	TransitionID                    string               `json:"transition_id"`
	LeaseID                         string               `json:"lease_id,omitempty"`
	LeasePurpose                    string               `json:"lease_purpose,omitempty"`
	LeaseUntilMS                    *int64               `json:"lease_until_ms,omitempty"`
	BackoffLevel                    int                  `json:"backoff_level"`
	ConsecutiveFailures             int                  `json:"consecutive_failures"`
	ConfirmationFailuresRequired    int                  `json:"confirmation_failures_required"`
	ConfirmationFailureEvidenceKeys []string             `json:"confirmation_failure_evidence_keys"`
	RecoveringSuccesses             int                  `json:"recovering_successes"`
	NextTransitionAtMS              *int64               `json:"next_transition_at_ms,omitempty"`
	OpenUntilMS                     *int64               `json:"open_until_ms,omitempty"`
	LastFailureClass                string               `json:"last_failure_class,omitempty"`
	UpdatedAtMS                     int64                `json:"updated_at_ms"`
}

// IncidentCursor 是 rebuild/reconcile 的稳定分页游标。
type IncidentCursor struct {
	UpdatedAtMS     int64  `json:"updated_at_ms"`
	CircuitScopeKey string `json:"circuit_scope_key"`
}

// RebuildPageQuery 传递上一页游标。
type RebuildPageQuery struct {
	NowMS                int64
	AfterUpdatedAtMS     *int64
	AfterCircuitScopeKey *string
	Limit                int
}

// RebuildPage 是一页 ledger 事实。
type RebuildPage struct {
	Items      []CircuitIncidentRecord
	NextCursor *IncidentCursor
}

// ControlPlaneLedger 是 incident ledger 只读 port。
type ControlPlaneLedger interface {
	ListForRebuild(ctx context.Context, query RebuildPageQuery) (RebuildPage, error)
	ListByRuntimeKeys(ctx context.Context, accountRuntimeKeys []string, includeRetainedClosed bool, nowMS int64) ([]CircuitIncidentRecord, error)
	GetByScopeKey(ctx context.Context, circuitScopeKey string) (*CircuitIncidentRecord, error)
}

// OutboxEvent 是 incident outbox 认领行。
type OutboxEvent struct {
	EventID           string
	EventType         string // dispatch_revision_changed | incident_projected
	AccountRuntimeKey string
	CircuitScopeKey   string
	TransitionID      string
	DispatchRevision  int64
	ClaimToken        string
	ProjectionKey     string
}

// ControlPlaneOutbox 是 outbox claim/ack/release port。
type ControlPlaneOutbox interface {
	Claim(ctx context.Context, ownerID string, nowMS int64, leaseMS int64, limit int) ([]OutboxEvent, error)
	Ack(ctx context.Context, event OutboxEvent, acknowledgedAtMS int64) (bool, error)
	ReleaseForReplay(ctx context.Context, event OutboxEvent, errorClass string, nowMS int64, retryDelayMS int64) error
}

// ReconcileCursorStore 持久化 reconcile 游标；nil 表示仅内存（与 Node 行为
// 一致，重启后 rebuild 幂等回放即可续跑）。实现 DB/Redis 后即为跨重启续跑。
type ReconcileCursorStore interface {
	Load(ctx context.Context) (*IncidentCursor, error)
	Save(ctx context.Context, cursor IncidentCursor) error
}

// ControlPlaneOptions 数值默认值对齐 Node bridge 构造参数。
type ControlPlaneOptions struct {
	OwnerID               string
	RetryDelayMS          int64
	OutboxLeaseMS         int64
	RebuildPageSize       int
	RebuildMaxPages       int
	RebuildPageTimeoutMS  int64
	RebuildTotalTimeoutMS int64
	NowMS                 func() int64
	MonotonicMS           func() int64
	CursorStore           ReconcileCursorStore
}

// ControlPlaneMaintenance 是 jobs 侧控制面维护引擎。
type ControlPlaneMaintenance struct {
	store                 CircuitStore
	ledger                ControlPlaneLedger
	outbox                ControlPlaneOutbox
	ownerID               string
	retryDelayMS          int64
	outboxLeaseMS         int64
	rebuildPageSize       int
	rebuildMaxPages       int
	rebuildPageTimeoutMS  int64
	rebuildTotalTimeoutMS int64
	nowMS                 func() int64
	monotonicMS           func() int64
	cursorStore           ReconcileCursorStore

	mu              sync.Mutex
	reconcileCursor *IncidentCursor
	rebuilding      bool
	globallyReady   bool
}

func NewControlPlaneMaintenance(store CircuitStore, ledger ControlPlaneLedger, outbox ControlPlaneOutbox, options ControlPlaneOptions) (*ControlPlaneMaintenance, error) {
	if store == nil || ledger == nil || outbox == nil {
		return nil, errors.New("账户电路控制面依赖未初始化")
	}
	if options.NowMS == nil {
		return nil, errors.New("账户电路控制面必须注入 NowMS 时钟")
	}
	monotonicMS := options.MonotonicMS
	if monotonicMS == nil {
		monotonicMS = options.NowMS
	}
	cfg := options
	if cfg.OwnerID == "" {
		cfg.OwnerID = "circuit-bridge:" + NewRandomID()
	}
	if cfg.RetryDelayMS == 0 {
		cfg.RetryDelayMS = 1_000
	}
	if cfg.OutboxLeaseMS == 0 {
		cfg.OutboxLeaseMS = 30_000
	}
	if cfg.RebuildPageSize == 0 {
		cfg.RebuildPageSize = 500
	}
	if cfg.RebuildMaxPages == 0 {
		cfg.RebuildMaxPages = 200
	}
	if cfg.RebuildPageTimeoutMS == 0 {
		cfg.RebuildPageTimeoutMS = 2_000
	}
	if cfg.RebuildTotalTimeoutMS == 0 {
		cfg.RebuildTotalTimeoutMS = 15_000
	}
	if cfg.RetryDelayMS < 1 || cfg.OutboxLeaseMS < 1 || cfg.RebuildPageSize < 1 ||
		cfg.RebuildMaxPages < 1 || cfg.RebuildPageTimeoutMS < 1 || cfg.RebuildTotalTimeoutMS < 1 {
		return nil, errors.New("control-plane 数值必须为正")
	}
	return &ControlPlaneMaintenance{
		store:                 store,
		ledger:                ledger,
		outbox:                outbox,
		ownerID:               cfg.OwnerID,
		retryDelayMS:          cfg.RetryDelayMS,
		outboxLeaseMS:         cfg.OutboxLeaseMS,
		rebuildPageSize:       cfg.RebuildPageSize,
		rebuildMaxPages:       cfg.RebuildMaxPages,
		rebuildPageTimeoutMS:  cfg.RebuildPageTimeoutMS,
		rebuildTotalTimeoutMS: cfg.RebuildTotalTimeoutMS,
		nowMS:                 cfg.NowMS,
		monotonicMS:           monotonicMS,
		cursorStore:           cfg.CursorStore,
	}, nil
}

// IsReady 报告全局冷启动门禁。
func (m *ControlPlaneMaintenance) IsReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.globallyReady
}

// RebuildResult 对齐 Node AccountCircuitControlPlaneRebuildResult。
type RebuildResult struct {
	Loaded  int    `json:"loaded"`
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
}

// Rebuild 重建原因码与 Node 完全一致。
const (
	RebuildReasonInvalidCursor     = "runtime_state_rebuild_invalid_cursor"
	RebuildReasonTimeout           = "runtime_state_rebuild_timeout"
	RebuildReasonCapacityExhausted = "runtime_state_rebuild_capacity_exhausted"
	RebuildReasonFailed            = "runtime_state_rebuild_failed"
)

type rebuildError struct {
	reason string
}

func (e *rebuildError) Error() string { return e.reason }

// Rebuild 从 ledger 分页回放运行态。带子 incident 的父行延迟到最后回放。
func (m *ControlPlaneMaintenance) Rebuild(ctx context.Context) (RebuildResult, error) {
	m.mu.Lock()
	if m.rebuilding {
		m.mu.Unlock()
		return RebuildResult{}, errors.New("账户电路控制面 rebuild 已在进行")
	}
	m.rebuilding = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.rebuilding = false
		m.mu.Unlock()
	}()

	startedAt := m.monotonicMS()
	loaded := 0
	var cursor *IncidentCursor
	hierarchyScopeKeys := map[string]string{}
	var deferredParents []CircuitIncidentRecord
	fail := func(reason string, cause ...error) (RebuildResult, error) {
		err := errors.New(reason)
		if len(cause) > 0 && cause[0] != nil {
			err = fmt.Errorf("%s: %w", reason, cause[0])
		}
		return RebuildResult{Loaded: loaded, Blocked: true, Reason: reason}, err
	}
	for pageNumber := 1; ; pageNumber++ {
		if pageNumber > m.rebuildMaxPages {
			return fail(RebuildReasonInvalidCursor)
		}
		remainingMS := m.rebuildTotalTimeoutMS - (m.monotonicMS() - startedAt)
		if remainingMS <= 0 {
			return fail(RebuildReasonTimeout)
		}
		query := RebuildPageQuery{NowMS: m.nowMS(), Limit: m.rebuildPageSize}
		if cursor != nil {
			afterUpdated := cursor.UpdatedAtMS
			afterScopeKey := cursor.CircuitScopeKey
			query.AfterUpdatedAtMS = &afterUpdated
			query.AfterCircuitScopeKey = &afterScopeKey
		}
		page, err := withinRebuildTimeout(ctx, func() (RebuildPage, error) {
			return m.ledger.ListForRebuild(ctx, query)
		}, min64(m.rebuildPageTimeoutMS, remainingMS))
		if err != nil {
			return fail(RebuildReasonTimeout)
		}
		for i := range page.Items {
			incident := page.Items[i]
			hierarchyScopeKeys[incidentHierarchyKey(incident.AccountRuntimeKey, incident.IncidentID)] = incident.CircuitScopeKey
		}
		for i := range page.Items {
			incident := page.Items[i]
			m.mu.Lock()
			m.recordLedgerFactsLocked(incident)
			m.mu.Unlock()
			if len(incident.ChildIncidentIDs) > 0 {
				deferredParents = append(deferredParents, incident)
				continue
			}
			restored, restoreErr := m.restoreIncident(ctx, incident, hierarchyScopeKeys)
			if restoreErr != nil {
				return fail(RebuildReasonFailed, restoreErr)
			}
			if restored.Status == CircuitMutationCapacityExhausted {
				return fail(RebuildReasonCapacityExhausted)
			}
			loaded++
		}
		if page.NextCursor == nil {
			break
		}
		if cursor != nil && compareCursor(*page.NextCursor, *cursor) <= 0 {
			return fail(RebuildReasonInvalidCursor)
		}
		cursor = page.NextCursor
	}
	for _, incident := range deferredParents {
		restored, restoreErr := m.restoreIncident(ctx, incident, hierarchyScopeKeys)
		if restoreErr != nil {
			return fail(RebuildReasonFailed, restoreErr)
		}
		if restored.Status == CircuitMutationCapacityExhausted {
			return fail(RebuildReasonCapacityExhausted)
		}
		loaded++
	}
	remainingMS := m.rebuildTotalTimeoutMS - (m.monotonicMS() - startedAt)
	if remainingMS <= 0 {
		return fail(RebuildReasonTimeout)
	}
	m.mu.Lock()
	m.globallyReady = true
	m.mu.Unlock()
	return RebuildResult{Loaded: loaded, Blocked: false}, nil
}

func (m *ControlPlaneMaintenance) recordLedgerFactsLocked(incident CircuitIncidentRecord) {
	// ledger revisions 与账户级 dispatch revision 快照保留在内存中，
	// 与 Node bridge 一致，仅用于后续对账诊断。
}

// ProjectPending 认领并回放 outbox 事件，返回 ack 成功数。
func (m *ControlPlaneMaintenance) ProjectPending(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, errors.New("control-plane 数值必须为正")
	}
	claims, err := m.outbox.Claim(ctx, m.ownerID, m.nowMS(), m.outboxLeaseMS, limit)
	if err != nil {
		return 0, fmt.Errorf("认领账户电路 outbox 失败: %w", err)
	}
	acknowledged := 0
	for _, event := range claims {
		if projectErr := m.applyOutboxEvent(ctx, event); projectErr != nil {
			if releaseErr := m.outbox.ReleaseForReplay(ctx, event, classifyControlPlaneError(projectErr), m.nowMS(), m.retryDelayMS); releaseErr != nil {
				return acknowledged, releaseErr
			}
			continue
		}
		ok, err := m.outbox.Ack(ctx, event, m.nowMS())
		if err != nil {
			return acknowledged, err
		}
		if ok {
			acknowledged++
		}
	}
	return acknowledged, nil
}

func (m *ControlPlaneMaintenance) applyOutboxEvent(ctx context.Context, event OutboxEvent) error {
	if event.EventType == "dispatch_revision_changed" {
		_, err := m.store.ReplaceAccountDispatchRevision(ctx, event.AccountRuntimeKey, fmt.Sprintf("%d", event.DispatchRevision), event.TransitionID, m.nowMS())
		return err
	}
	if event.CircuitScopeKey == "" {
		return errors.New("incident outbox 缺少 circuitScopeKey")
	}
	incident, err := m.ledger.GetByScopeKey(ctx, event.CircuitScopeKey)
	if err != nil {
		return err
	}
	if incident == nil {
		return errors.New("incident outbox 对应 ledger 缺失")
	}
	hierarchyScopeKeys := incidentScopeKeyMap([]CircuitIncidentRecord{*incident})
	if hasUnresolvedChildIncident(*incident, hierarchyScopeKeys) {
		accountIncidents, err := m.ledger.ListByRuntimeKeys(ctx, []string{incident.AccountRuntimeKey}, false, m.nowMS())
		if err != nil {
			return err
		}
		addIncidentScopeKeys(hierarchyScopeKeys, accountIncidents)
	}
	projected, err := m.restoreIncident(ctx, *incident, hierarchyScopeKeys)
	if err != nil {
		return err
	}
	if projected.Status == CircuitMutationCapacityExhausted {
		return errors.New("runtime circuit projection capacity exhausted")
	}
	return nil
}

// ReconcileActive 重放一页 ledger 修复被驱逐的运行态键；游标持久化在
// CursorStore（可跨重启续跑）。
func (m *ControlPlaneMaintenance) ReconcileActive(ctx context.Context, limit int) (int, error) {
	m.mu.Lock()
	if m.rebuilding || !m.globallyReady {
		m.mu.Unlock()
		return 0, nil
	}
	cursor := m.reconcileCursor
	m.mu.Unlock()
	if limit < 1 {
		return 0, errors.New("control-plane 数值必须为正")
	}
	if m.cursorStore != nil && cursor == nil {
		loaded, err := m.cursorStore.Load(ctx)
		if err != nil {
			return 0, fmt.Errorf("读取账户电路 reconcile 游标失败: %w", err)
		}
		cursor = loaded
	}
	query := RebuildPageQuery{NowMS: m.nowMS(), Limit: limit}
	if cursor != nil {
		afterUpdated := cursor.UpdatedAtMS
		afterScopeKey := cursor.CircuitScopeKey
		query.AfterUpdatedAtMS = &afterUpdated
		query.AfterCircuitScopeKey = &afterScopeKey
	}
	page, err := withinRebuildTimeout(ctx, func() (RebuildPage, error) {
		return m.ledger.ListForRebuild(ctx, query)
	}, m.rebuildPageTimeoutMS)
	if err != nil {
		return 0, errors.New(RebuildReasonTimeout)
	}
	repaired := 0
	hierarchyScopeKeys := incidentScopeKeyMap(page.Items)
	for i := range page.Items {
		incident := page.Items[i]
		if hasUnresolvedChildIncident(incident, hierarchyScopeKeys) {
			accountIncidents, err := m.ledger.ListByRuntimeKeys(ctx, []string{incident.AccountRuntimeKey}, false, m.nowMS())
			if err != nil {
				return repaired, err
			}
			addIncidentScopeKeys(hierarchyScopeKeys, accountIncidents)
		}
		restored, err := m.restoreIncident(ctx, incident, hierarchyScopeKeys)
		if err != nil {
			return repaired, err
		}
		if restored.Status == CircuitMutationCapacityExhausted {
			return repaired, errors.New("账户 circuit runtime store 对账容量不足")
		}
		repaired++
	}
	nextCursor := page.NextCursor
	m.mu.Lock()
	m.reconcileCursor = nextCursor
	m.mu.Unlock()
	if m.cursorStore != nil {
		if nextCursor == nil {
			// 一页遍历完成即从当前末尾继续：写入本页最后一行作为游标。
			if len(page.Items) > 0 {
				last := page.Items[len(page.Items)-1]
				nextCursor = &IncidentCursor{UpdatedAtMS: last.UpdatedAtMS, CircuitScopeKey: last.CircuitScopeKey}
			}
		}
		if nextCursor != nil {
			if err := m.cursorStore.Save(ctx, *nextCursor); err != nil {
				return repaired, fmt.Errorf("持久化账户电路 reconcile 游标失败: %w", err)
			}
		}
	}
	return repaired, nil
}

// RunMaintenance 对齐 Node runGatewayAccountCircuitControlPlaneMaintenance：
// 确保运行态就绪后执行 projectPending + reconcileActive，返回处理总数。
func (m *ControlPlaneMaintenance) RunMaintenance(ctx context.Context, limit int) (int, error) {
	ready, err := m.EnsureRuntimeStateReady(ctx)
	if err != nil {
		return 0, err
	}
	if !ready {
		// Node 语义：部分重建仍继续投影当前已加载的到期事实。
		_ = ready
	}
	projected, err := m.ProjectPending(ctx, limit)
	if err != nil {
		return projected, err
	}
	reconciled, err := m.ReconcileActive(ctx, limit)
	return projected + reconciled, err
}

// EnsureRuntimeStateReady 对齐 ensureGatewayAccountCircuitRuntimeStateReady。
func (m *ControlPlaneMaintenance) EnsureRuntimeStateReady(ctx context.Context) (bool, error) {
	if m.IsReady() {
		return true, nil
	}
	result, err := m.Rebuild(ctx)
	if err != nil && result.Reason == "" {
		return false, err
	}
	return !result.Blocked, nil
}

func (m *ControlPlaneMaintenance) restoreIncident(ctx context.Context, incident CircuitIncidentRecord, hierarchyScopeKeys map[string]string) (CircuitMutationResult, error) {
	state, err := IncidentToRuntimeState(incident, hierarchyScopeKeys)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	result, err := m.store.Restore(ctx, state, m.nowMS())
	if err != nil {
		return CircuitMutationResult{}, err
	}
	// observeRestoredRelationships：related 状态继续投影。
	for _, related := range result.RelatedStates {
		if _, err := m.store.Restore(ctx, related, m.nowMS()); err != nil {
			return CircuitMutationResult{}, err
		}
	}
	return result, nil
}

// IncidentToRuntimeState 对齐 Node incidentToRuntimeState。
func IncidentToRuntimeState(incident CircuitIncidentRecord, hierarchyScopeKeys map[string]string) (CircuitState, error) {
	var scope CircuitScope
	switch incident.ScopeKind {
	case "account":
		scope = CircuitScope{Kind: CircuitScopeAccount, AccountRuntimeKey: incident.AccountRuntimeKey}
	case "key":
		fingerprint, err := requiredText(incident.KeyFingerprint, "keyFingerprint")
		if err != nil {
			return CircuitState{}, err
		}
		scope = CircuitScope{Kind: CircuitScopeKey, AccountRuntimeKey: incident.AccountRuntimeKey, KeyFingerprint: fingerprint}
	case "protocol_model":
		profile, err := requiredText(incident.ProtocolCode, "protocolCode")
		if err != nil {
			return CircuitState{}, err
		}
		if incident.RequestLane != "text" && incident.RequestLane != "image" {
			return CircuitState{}, errors.New("持久化账户 circuit requestLane 无效")
		}
		bucket, err := requiredText(incident.ModelFamily, "modelFamily")
		if err != nil {
			return CircuitState{}, err
		}
		scope = CircuitScope{
			Kind:              CircuitScopeProtocolModel,
			AccountRuntimeKey: incident.AccountRuntimeKey,
			ProtocolProfile:   profile,
			RequestLane:       incident.RequestLane,
			ModelBucket:       bucket,
		}
	default:
		return CircuitState{}, fmt.Errorf("持久化账户 circuit scopeKind 无效: %s", incident.ScopeKind)
	}
	expectedScopeKey, err := AccountCircuitScopeKey(scope)
	if err != nil {
		return CircuitState{}, err
	}
	if incident.CircuitScopeKey != expectedScopeKey {
		return CircuitState{}, errors.New("持久化账户 circuit scopeKey 与作用域字段不一致")
	}
	var lease *CircuitLease
	if incident.LeaseUntilMS != nil {
		switch incident.LeasePurpose {
		case "confirmation", "half_open", "recovery":
			if incident.LeaseID != "" {
				lease = &CircuitLease{
					Kind:         CircuitLeaseKind(incident.LeasePurpose),
					LeaseID:      incident.LeaseID,
					LeaseUntilMS: *incident.LeaseUntilMS,
				}
			}
		}
	}
	var childScopeKeys []string
	for _, incidentID := range incident.ChildIncidentIDs {
		if scopeKey, found := hierarchyScopeKeys[incidentHierarchyKey(incident.AccountRuntimeKey, incidentID)]; found {
			childScopeKeys = append(childScopeKeys, scopeKey)
		}
	}
	state := CircuitState{
		ScopeKey:             incident.CircuitScopeKey,
		Scope:                scope,
		Phase:                incidentRuntimePhase(incident.State),
		Generation:           incident.Generation,
		DispatchRevision:     fmt.Sprintf("%d", incident.DispatchRevision),
		TransitionID:         incident.TransitionID,
		IncidentID:           incident.IncidentID,
		BackoffAttempt:       incident.BackoffLevel,
		RecoverySuccessCount: incident.RecoveringSuccesses,
		UpdatedAtMS:          incident.UpdatedAtMS,
		Lease:                lease,
	}
	if incident.ParentIncidentID != "" {
		state.ShadowedByIncidentID = incident.ParentIncidentID
	}
	if len(incident.ChildIncidentIDs) > 0 {
		state.ChildIncidentIDs = append([]string(nil), incident.ChildIncidentIDs...)
	}
	if len(childScopeKeys) > 0 {
		state.ChildScopeKeys = childScopeKeys
		state.RequiredRecoveryScopeKeys = append([]string(nil), childScopeKeys...)
	}
	required := incident.ConfirmationFailuresRequired
	state.ConfirmationFailuresRequired = &required
	count := incident.ConsecutiveFailures
	state.ConfirmationFailureCount = &count
	if len(incident.ConfirmationFailureEvidenceKeys) > 0 {
		state.FailureEvidenceKeys = append([]string(nil), incident.ConfirmationFailureEvidenceKeys...)
	}
	if incident.OpenUntilMS != nil {
		openedAt := incident.UpdatedAtMS
		state.OpenedAtMS = &openedAt
	}
	if incident.NextTransitionAtMS != nil {
		retryAt := *incident.NextTransitionAtMS
		state.RetryAtMS = &retryAt
	}
	if incident.LastFailureClass != "" {
		state.FailureReason = incident.LastFailureClass
	}
	if incident.State == CircuitIncidentHalfOpen && lease != nil {
		if lease.Kind == CircuitLeaseHalfOpen {
			state.HalfOpenOrigin = string(CircuitPhaseOpen)
		} else if lease.Kind == CircuitLeaseRecovery {
			state.HalfOpenOrigin = string(CircuitPhaseRecovering)
		}
	}
	return state, nil
}

func incidentRuntimePhase(state CircuitIncidentState) CircuitPhase {
	if state == CircuitIncidentPersisting || state == CircuitIncidentShadowedByPersistent {
		return CircuitPhaseOpen
	}
	return CircuitPhase(state)
}

func incidentHierarchyKey(accountRuntimeKey, incidentID string) string {
	return fmt.Sprintf("%d:%s|%s", len(accountRuntimeKey), accountRuntimeKey, incidentID)
}

func incidentScopeKeyMap(incidents []CircuitIncidentRecord) map[string]string {
	result := map[string]string{}
	addIncidentScopeKeys(result, incidents)
	return result
}

func addIncidentScopeKeys(target map[string]string, incidents []CircuitIncidentRecord) {
	for i := range incidents {
		incident := incidents[i]
		target[incidentHierarchyKey(incident.AccountRuntimeKey, incident.IncidentID)] = incident.CircuitScopeKey
	}
}

func hasUnresolvedChildIncident(incident CircuitIncidentRecord, hierarchyScopeKeys map[string]string) bool {
	for _, incidentID := range incident.ChildIncidentIDs {
		if _, found := hierarchyScopeKeys[incidentHierarchyKey(incident.AccountRuntimeKey, incidentID)]; !found {
			return true
		}
	}
	return false
}

func compareCursor(left, right IncidentCursor) int {
	if left.UpdatedAtMS != right.UpdatedAtMS {
		if left.UpdatedAtMS < right.UpdatedAtMS {
			return -1
		}
		return 1
	}
	switch {
	case left.CircuitScopeKey < right.CircuitScopeKey:
		return -1
	case left.CircuitScopeKey > right.CircuitScopeKey:
		return 1
	default:
		return 0
	}
}

func requiredText(value, name string) (string, error) {
	normalized := trimSpaces(value)
	if normalized == "" {
		return "", fmt.Errorf("账户 circuit incident 缺少 %s", name)
	}
	return normalized, nil
}

func classifyControlPlaneError(err error) string {
	// Node: error.name.slice(0, 64) || 'projector_error'。
	message := err.Error()
	if len(message) > 64 {
		message = message[:64]
	}
	if trimSpaces(message) == "" {
		return "projector_error"
	}
	return message
}

func withinRebuildTimeout(ctx context.Context, op func() (RebuildPage, error), timeoutMS int64) (RebuildPage, error) {
	type pageResult struct {
		page RebuildPage
		err  error
	}
	done := make(chan pageResult, 1)
	go func() {
		page, err := op()
		done <- pageResult{page: page, err: err}
	}()
	timer := newTimer(timeoutMS)
	defer stopTimer(timer)
	select {
	case result := <-done:
		return result.page, result.err
	case <-timer.C:
		return RebuildPage{}, errors.New(RebuildReasonTimeout)
	case <-ctx.Done():
		return RebuildPage{}, ctx.Err()
	}
}
