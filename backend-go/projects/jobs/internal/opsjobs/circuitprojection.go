package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// 账户电路恢复扫描 → 业务库 ledger 投影。与 gateway 写侧成对维护（跨 module
// 不可 import，成对复制约定）：
//   - CAS 行事实/输入/结果对照 gateway
//     internal/business/circuit_control_plane/store.go 的 Incident /
//     IncidentMutation / IncidentResult，列集与 gateway incidentColumns 完全
//     一致（同一张物理表 account_circuit_incidents）；
//   - mutation → CAS 输入的映射平移 gateway internal/gatewaycircuit/bridge.go
//     buildPersistIncidentInput（含 durableIncidentID、classifyFailure、
//     CLOSED retainedUntil、expected ledger revision 缓存围栏）；
//   - 新行 CreatedAtMS 落 0、UpdatedAtMS 取运行态 stateUpdatedAtMs，与
//     gateway cmd/juhe-ai-gateway/chain_circuit_controlplane.go 的
//     chainCircuitIncidentMutation 适配语义一致。
//
// 写侧 SQL 实现在 internal/circuitstore/controlplane.go（CompareAndSetIncident
// / Cleanup / ListActiveIncidentsPage）。

// FailureClass 词汇对照 gatewaycircuit 的 FailureClass（镜像 Node
// AccountCircuitFailureClass），写入 ledger last_failure_class 列。
const (
	FailureClassConnectFailed         = "connect_failed"
	FailureClassTimeoutBeforeComplete = "timeout_before_complete"
	FailureClassReadInterrupted       = "read_interrupted"
	FailureClassIncompleteResponse    = "incomplete_response"
	FailureClassExplicitPolicy        = "explicit_policy"
)

// classifyCircuitFailure 与 gateway gatewaycircuit.classifyFailure 逐语义一致
// （纯函数，按 failureReason 关键词归类）。
func classifyCircuitFailure(reason string) string {
	value := strings.ToLower(reason)
	if strings.Contains(value, "timeout") {
		return FailureClassTimeoutBeforeComplete
	}
	if strings.Contains(value, "connect") {
		return FailureClassConnectFailed
	}
	if strings.Contains(value, "read") {
		return FailureClassReadInterrupted
	}
	if strings.Contains(value, "policy") {
		return FailureClassExplicitPolicy
	}
	return FailureClassIncompleteResponse
}

// IncidentCASStatus 与 gateway CompareAndSetIncidentResult.status 词汇一致。
type IncidentCASStatus string

const (
	IncidentCASApplied               IncidentCASStatus = "applied"
	IncidentCASIdempotent            IncidentCASStatus = "idempotent"
	IncidentCASConflict              IncidentCASStatus = "cas_conflict"
	IncidentCASStaleDispatchRevision IncidentCASStatus = "stale_dispatch_revision"
	IncidentCASAccountNotFound       IncidentCASStatus = "account_not_found"
)

// IncidentCASRow 是 ledger 行全字段事实（44 列，列序即
// incidentCASColumns；对照 gateway circuitcontrolplane.Incident）。
// LedgerRevision / ProjectedLedgerRevision / CreatedAtMS 由 CAS 写侧按当前行
// 覆盖，输入值仅为形状占位（新行 CreatedAtMS 落 0，与 gateway 适配一致）。
type IncidentCASRow struct {
	CircuitScopeKey                 string
	AccountID                       string
	AccountRuntimeKey               string
	ScopeKind                       string
	KeyFingerprint                  *string
	ProtocolCode                    *string
	RequestLane                     *string
	ModelFamily                     *string
	ClientModel                     *string
	CapabilityHash                  *string
	CredentialSourceAccountID       *string
	ClientEndpointFamily            *string
	FinalUpstreamModel              *string
	UpstreamEndpointMode            *string
	IncidentID                      string
	ParentIncidentID                *string
	ChildIncidentIDs                []string
	CausedByTerminalOutcomeID       *string
	State                           string
	FailureScope                    *string
	Generation                      int64
	DispatchRevision                int64
	LedgerRevision                  int64
	ProjectedLedgerRevision         int64
	TransitionID                    string
	CooldownObservationGeneration   int64
	OpenUntilMS                     *int64
	NextTransitionAtMS              *int64
	LeaseID                         *string
	LeasePurpose                    *string
	LeaseOwnerRunID                 *string
	LeaseUntilMS                    *int64
	AttemptStartedAtMS              *int64
	AttemptHardDeadlineMS           *int64
	UpstreamAttemptObserved         bool
	BackoffLevel                    int64
	ConsecutiveFailures             int64
	ConfirmationFailuresRequired    int64
	ConfirmationFailureEvidenceKeys []string
	RecoveringSuccesses             int64
	LastFailureClass                *string
	RetainedUntilMS                 *int64
	CreatedAtMS                     int64
	UpdatedAtMS                     int64
}

// IncidentCASInput 对照 gateway IncidentMutation；ExpectedLedgerRevision 为
// nil 表示"该 scope 必须尚不存在"（新行插入围栏），非 nil 必须与当前行
// ledger_revision 相等。
type IncidentCASInput struct {
	Incident               IncidentCASRow
	ExpectedLedgerRevision *int64
}

// IncidentCASResult 对照 gateway IncidentResult。
type IncidentCASResult struct {
	Status                  IncidentCASStatus
	CurrentDispatchRevision int64
	Incident                *IncidentCASRow
}

// IncidentCASPort 是 ledger CAS 写侧窄 port；生产实现是
// circuitstore.ControlPlaneRepo，测试注入 mock。
type IncidentCASPort interface {
	CompareAndSetIncident(ctx context.Context, input IncidentCASInput) (IncidentCASResult, error)
}

// DefaultIncidentClosedRetentionMS 对齐 Node bridge closedRetentionMs 默认
// （5 分钟 tombstone 保留窗，供读侧区分"刚关闭"与"可清理"）。
const DefaultIncidentClosedRetentionMS int64 = 5 * 60_000

// projectorMaxConflictRetries 是 cas_conflict 后用响应回填 revision 的即时
// 重试上限；仍冲突则留 pending 等下一轮 FlushPending。
const projectorMaxConflictRetries = 2

// CircuitIncidentProjectorOptions 数值均为可测 options，不新增 env。
type CircuitIncidentProjectorOptions struct {
	// OwnerID 写入 lease_owner_run_id；空则派生进程内随机 owner。
	OwnerID string
	// ClosedRetentionMS 仅 CLOSED 行写入；<=0 用默认 5 分钟。
	ClosedRetentionMS int64
	NowMS             func() int64
	Logger            *slog.Logger
}

// CircuitIncidentProjector 把恢复扫描的运行态 mutation 投影为业务库 ledger
// CAS 写。mutation 按 scopeKey 合并只留最新；CAS 在调用方 goroutine 内同步
// 执行（sweep 并发 ≤4，事务量可承受）。不启动自有 timer goroutine，失败残留
// 由每轮 FlushPending 重放（规避 BUG-0222 类 goroutine 泄漏前科）。
type CircuitIncidentProjector struct {
	cas               IncidentCASPort
	ownerID           string
	closedRetentionMS int64
	nowMS             func() int64
	logger            *slog.Logger

	mu      sync.Mutex
	pending map[string]CircuitRecoveryMutation
	ledgers map[string]int64
	closed  bool
}

// NewCircuitIncidentProjector 构建投影器；cas 为 nil 时返回错误。
func NewCircuitIncidentProjector(cas IncidentCASPort, options CircuitIncidentProjectorOptions) (*CircuitIncidentProjector, error) {
	if cas == nil {
		return nil, errors.New("账户电路投影缺少 CAS port")
	}
	if options.NowMS == nil {
		return nil, errors.New("账户电路投影必须注入 NowMS 时钟")
	}
	ownerID := strings.TrimSpace(options.OwnerID)
	if ownerID == "" {
		ownerID = "circuit-projector:" + NewRandomID()
	}
	closedRetentionMS := options.ClosedRetentionMS
	if closedRetentionMS <= 0 {
		closedRetentionMS = DefaultIncidentClosedRetentionMS
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &CircuitIncidentProjector{
		cas:               cas,
		ownerID:           ownerID,
		closedRetentionMS: closedRetentionMS,
		nowMS:             options.NowMS,
		logger:            logger,
		pending:           map[string]CircuitRecoveryMutation{},
		ledgers:           map[string]int64{},
	}, nil
}

// OwnerID 暴露投影 owner（测试断言 lease_owner_run_id 用）。
func (p *CircuitIncidentProjector) OwnerID() string { return p.ownerID }

// OnRecoveryMutation 作为 CircuitRecoveryServiceOptions.OnMutation 挂点。
// not_found 与投影器关闭时跳过（对齐 observeMutation 现状）；fenced/skipped
// 等无状态变化的 mutation 不投影。
func (p *CircuitIncidentProjector) OnRecoveryMutation(m CircuitRecoveryMutation) {
	if p == nil || p.cas == nil {
		return
	}
	if m.Status == CircuitMutationNotFound {
		return
	}
	if m.Status != CircuitMutationApplied && m.Status != CircuitMutationIdempotent {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.pending[m.State.ScopeKey] = m
	p.mu.Unlock()
	p.drainKey(m.State.ScopeKey)
}

// drainKey 单次弹出并投影指定 scope 的最新 pending。project 失败经
// retainPending 放回的条目不在这里同步重试（否则失败会形成同 goroutine 死
// 循环），统一等下一轮 FlushPending；投影期间新到的 mutation 同样留给
// FlushPending（合并只留最新）。
func (p *CircuitIncidentProjector) drainKey(scopeKey string) {
	p.mu.Lock()
	m, ok := p.pending[scopeKey]
	if ok {
		delete(p.pending, scopeKey)
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	p.project(m)
}

// FlushPending 在每轮 Sweep 前重放上一轮失败残留；ctx 取消时剩余项放回
// pending。失败不阻断调用方（Sweep 照常执行）。
func (p *CircuitIncidentProjector) FlushPending(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if len(p.pending) == 0 {
		p.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(p.pending))
	for key := range p.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	snapshot := make(map[string]CircuitRecoveryMutation, len(p.pending))
	for _, key := range keys {
		snapshot[key] = p.pending[key]
	}
	p.pending = make(map[string]CircuitRecoveryMutation, len(p.pending))
	p.mu.Unlock()
	for index, key := range keys {
		if ctx.Err() != nil {
			p.mu.Lock()
			for _, remaining := range keys[index:] {
				if _, exists := p.pending[remaining]; !exists {
					p.pending[remaining] = snapshot[remaining]
				}
			}
			p.mu.Unlock()
			return
		}
		p.project(snapshot[key])
	}
}

// Close 幂等关闭：拒绝后续 Observe；进程收尾由 worker closer 链调用。
func (p *CircuitIncidentProjector) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

// project 执行单条 mutation 的 CAS 投影：applied/idempotent 清 pending 并回
// 填 revision 缓存；cas_conflict 有响应行时回填 expected 即时重试 ≤2 次，仍
// 冲突留 pending，响应无行（行已被 Cleanup 删除）清 revision 缓存留 pending
// 待下轮 expected=nil 重试；account_not_found / stale_dispatch_revision 终态
// 丢弃（清 pending 与该键 revision 缓存）；其他错误留 pending 待
// FlushPending。
func (p *CircuitIncidentProjector) project(m CircuitRecoveryMutation) {
	scopeKey := m.State.ScopeKey
	input, err := p.buildInput(m.State, p.nowMS())
	if err != nil {
		// 输入非法（运行态数据损坏）重放同样非法，终态放弃并留结构化日志
		// （不含凭据与上游响应）。
		p.logger.Warn("账户电路 ledger 投影输入构造失败，放弃本条 mutation",
			"scopeKey", scopeKey, "accountRuntimeKey", m.State.Scope.AccountRuntimeKey, "error", err)
		return
	}
	for attempt := 0; ; attempt++ {
		input.ExpectedLedgerRevision = p.ledgerRevision(scopeKey)
		result, err := p.cas.CompareAndSetIncident(context.Background(), input)
		if err != nil {
			p.logger.Warn("账户电路 ledger 投影失败，留待下轮重放",
				"scopeKey", scopeKey, "accountRuntimeKey", input.Incident.AccountRuntimeKey, "error", err)
			p.retainPending(m)
			return
		}
		switch result.Status {
		case IncidentCASApplied, IncidentCASIdempotent:
			p.setLedgerRevision(scopeKey, incidentRevisionOf(result.Incident))
			return
		case IncidentCASConflict:
			if result.Incident == nil {
				// expected 非 nil 且行不存在（CompareAndSetIncident 围栏：
				// 行缺失 vs expected 非 nil → cas_conflict 且响应无行可回
				// 填）：清 revision 缓存，下轮重试 expected=nil 走新行围栏。
				// 触发链：投影器写 CLOSED（applied，缓存=N）→ Cleanup 到期
				// 删行 → 同 scope 日后复开 → 新 mutation 携带悬空缓存
				// expected=N 永久冲突空转直到进程重启。
				p.deleteLedgerRevision(scopeKey)
			} else {
				p.setLedgerRevision(scopeKey, incidentRevisionOf(result.Incident))
			}
			if attempt < projectorMaxConflictRetries && result.Incident != nil {
				continue
			}
			p.retainPending(m)
			return
		case IncidentCASAccountNotFound, IncidentCASStaleDispatchRevision:
			// 归档热修终态语义（对齐 gateway persistWithRetry）：账户已删或
			// dispatch revision 已推进的迟到观察不保留 pending、不缓存
			// revision、不重试。
			p.deleteLedgerRevision(scopeKey)
			p.logger.Info("账户电路 ledger 投影终态丢弃",
				"scopeKey", scopeKey, "accountRuntimeKey", input.Incident.AccountRuntimeKey, "status", string(result.Status))
			return
		default:
			p.logger.Warn("账户电路 ledger 投影返回未知状态，留待下轮重放",
				"scopeKey", scopeKey, "accountRuntimeKey", input.Incident.AccountRuntimeKey, "status", string(result.Status))
			p.retainPending(m)
			return
		}
	}
}

// retainPending 把失败项放回 pending；若投影期间同一 scope 已有更新的
// mutation，则保留更新项（合并只留最新）。
func (p *CircuitIncidentProjector) retainPending(m CircuitRecoveryMutation) {
	p.mu.Lock()
	if _, exists := p.pending[m.State.ScopeKey]; !exists {
		p.pending[m.State.ScopeKey] = m
	}
	p.mu.Unlock()
}

func (p *CircuitIncidentProjector) ledgerRevision(scopeKey string) *int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if value, ok := p.ledgers[scopeKey]; ok {
		return &value
	}
	return nil
}

func (p *CircuitIncidentProjector) setLedgerRevision(scopeKey string, revision *int64) {
	if revision == nil {
		return
	}
	p.mu.Lock()
	p.ledgers[scopeKey] = *revision
	p.mu.Unlock()
}

func (p *CircuitIncidentProjector) deleteLedgerRevision(scopeKey string) {
	p.mu.Lock()
	delete(p.ledgers, scopeKey)
	p.mu.Unlock()
}

func incidentRevisionOf(incident *IncidentCASRow) *int64 {
	if incident == nil {
		return nil
	}
	value := incident.LedgerRevision
	return &value
}

// buildInput 平移 gateway buildPersistIncidentInput + 链适配层（Created/Updated
// 语义）。确认三元组用 jobs 侧等价归一函数（Normalize/Keys/Count 与 gateway
// NormalizeConfirmationFailuresRequired / FailureEvidenceKeysOf /
// ConfirmationFailureCountOf 逐语义一致）。
func (p *CircuitIncidentProjector) buildInput(state CircuitState, nowMS int64) (IncidentCASInput, error) {
	required, err := NormalizeAccountCircuitConfirmationFailuresRequired(state.ConfirmationFailuresRequired)
	if err != nil {
		return IncidentCASInput{}, err
	}
	evidenceKeys, err := AccountCircuitFailureEvidenceKeys(state)
	if err != nil {
		return IncidentCASInput{}, err
	}
	if evidenceKeys == nil {
		evidenceKeys = []string{}
	}
	consecutiveFailures, err := AccountCircuitConfirmationFailureCount(state)
	if err != nil {
		return IncidentCASInput{}, err
	}
	// Node bridge accountIDFromRuntimeKey：取首个 ':' 之前，空白即错。
	accountID := RuntimeAccountIDFromKey(state.Scope.AccountRuntimeKey)
	if strings.TrimSpace(accountID) == "" {
		return IncidentCASInput{}, errors.New("账户 circuit runtime key 缺少 accountId")
	}
	// 运行态 dispatchRevision 必须可解析为正整数（Redis store 写入侧已保证）；
	// 与 gateway 的"回退缓存 revision"不同，jobs 无该缓存，解析失败按输入非
	// 法终态放弃（刻意偏差，重放同值无意义）。
	dispatchRevision, ok := parseSafePositiveInt64(state.DispatchRevision)
	if !ok {
		return IncidentCASInput{}, fmt.Errorf("账户 circuit 运行态 dispatchRevision 无效: %q", state.DispatchRevision)
	}
	transitionID := strings.TrimSpace(state.TransitionID)
	if transitionID == "" {
		transitionID = fmt.Sprintf("rebuild:%s:%d", state.ScopeKey, state.Generation)
	}
	incidentID := strings.TrimSpace(state.IncidentID)
	if incidentID == "" {
		// durableIncidentID：运行态 incidentId 缺省时以 transitionId 承担。
		incidentID = transitionID
	}
	childIncidentIDs := state.ChildIncidentIDs
	if childIncidentIDs == nil {
		childIncidentIDs = []string{}
	}
	input := IncidentCASInput{Incident: IncidentCASRow{
		CircuitScopeKey:                 state.ScopeKey,
		AccountID:                       accountID,
		AccountRuntimeKey:               state.Scope.AccountRuntimeKey,
		ScopeKind:                       string(state.Scope.Kind),
		IncidentID:                      incidentID,
		ChildIncidentIDs:                childIncidentIDs,
		State:                           string(state.Phase),
		Generation:                      state.Generation,
		DispatchRevision:                dispatchRevision,
		TransitionID:                    transitionID,
		UpstreamAttemptObserved:         true,
		BackoffLevel:                    int64(state.BackoffAttempt),
		ConsecutiveFailures:             int64(consecutiveFailures),
		ConfirmationFailuresRequired:    int64(required),
		ConfirmationFailureEvidenceKeys: evidenceKeys,
		RecoveringSuccesses:             int64(state.RecoverySuccessCount),
		UpdatedAtMS:                     state.UpdatedAtMS,
	}}
	switch state.Scope.Kind {
	case CircuitScopeKey:
		fingerprint := state.Scope.KeyFingerprint
		input.Incident.KeyFingerprint = &fingerprint
	case CircuitScopeProtocolModel:
		protocolCode := state.Scope.ProtocolProfile
		requestLane := state.Scope.RequestLane
		modelFamily := state.Scope.ModelBucket
		input.Incident.ProtocolCode = &protocolCode
		input.Incident.RequestLane = &requestLane
		input.Incident.ModelFamily = &modelFamily
	}
	if shadowedBy := strings.TrimSpace(state.ShadowedByIncidentID); shadowedBy != "" {
		input.Incident.ParentIncidentID = &shadowedBy
	}
	if state.RetryAtMS != nil {
		nextTransition := *state.RetryAtMS
		openUntil := *state.RetryAtMS
		input.Incident.NextTransitionAtMS = &nextTransition
		input.Incident.OpenUntilMS = &openUntil
	}
	if state.Lease != nil {
		leaseID := state.Lease.LeaseID
		leasePurpose := string(state.Lease.Kind)
		ownerRunID := p.ownerID
		leaseUntil := state.Lease.LeaseUntilMS
		input.Incident.LeaseID = &leaseID
		input.Incident.LeasePurpose = &leasePurpose
		input.Incident.LeaseOwnerRunID = &ownerRunID
		input.Incident.LeaseUntilMS = &leaseUntil
	}
	if state.FailureReason != "" {
		failureClass := classifyCircuitFailure(state.FailureReason)
		input.Incident.LastFailureClass = &failureClass
	}
	if state.Phase == CircuitPhaseClosed {
		retainedUntil := nowMS + p.closedRetentionMS
		input.Incident.RetainedUntilMS = &retainedUntil
	}
	return input, nil
}
