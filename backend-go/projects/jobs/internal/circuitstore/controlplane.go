package circuitstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// 控制面 ledger/outbox 适配器：移植 Node
// storage/account-circuit-control-plane.repository.ts 的 jobs 侧消费面
// （ListForRebuild / ListByRuntimeKeys / GetByScopeKey + outbox claim /
// acknowledge / release-for-replay）。SQL 与 Node 逐字段一致，双模方言差异
// 与 Node 相同：postgres 使用 FOR UPDATE [SKIP LOCKED]，SQLite 退化为
// 单 writer 串行；业务表位于 juhe_business schema（PG）。
//
// 本适配器的 ledger 读面（List* / Get*）带 dispatch_revision / deleted_at
// IS NULL 围栏，与归档 repository 一致，保证已删账户的迟到事实不回放。
// CAS 写侧 2026-10-08 起由本文件承载（jobs 恢复扫描投影
// internal/opsjobs.CircuitIncidentProjector 与孤儿结清扫描
// OrphanIncidentCloser 的落库管道），与 gateway 写侧
// backend-go/projects/gateway/internal/business/circuit_control_plane 同键
// 同契约成对维护（跨 module 不可 import；归档热修 account_not_found 终态
// 语义——账户行缺失或 deleted_at 非空时迟到运行态观察必须终态而非重试——
// 在两侧写路径都有）；outbox ack 中对
// circuit_projection_revision / projected_ledger_revision 的回写与
// Node acknowledge 完全一致。

// ProjectionKey 与 Node accountCircuitProjectionKey 一致。
const ProjectionKey = "account_circuit_runtime_v1"

// ControlPlaneConfig 组装 ledger/outbox 适配器。
type ControlPlaneConfig struct {
	DB       *sql.DB
	Postgres bool
	Now      func() time.Time
}

// ControlPlaneRepo 实现 opsjobs.ControlPlaneLedger 与 opsjobs.ControlPlaneOutbox。
type ControlPlaneRepo struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
}

// NewControlPlaneRepo 构建适配器；输入校验失败返回错误。
func NewControlPlaneRepo(config ControlPlaneConfig) (*ControlPlaneRepo, error) {
	if config.DB == nil {
		return nil, errors.New("circuitstore 控制面缺少业务库句柄")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ControlPlaneRepo{db: config.DB, postgres: config.Postgres, now: now}, nil
}

func (r *ControlPlaneRepo) table(name string) string {
	if r.postgres {
		return "juhe_business." + name
	}
	return name
}

type incidentRow struct {
	scopeKey                            string
	accountID                           string
	accountRuntimeKey                   string
	scopeKind                           string
	keyFingerprint                      sql.NullString
	protocolCode                        sql.NullString
	requestLane                         sql.NullString
	modelFamily                         sql.NullString
	incidentID                          string
	parentIncidentID                    sql.NullString
	childIncidentIDsJSON                string
	state                               string
	generation                          int64
	dispatchRevision                    int64
	ledgerRevision                      int64
	transitionID                        string
	leaseID                             sql.NullString
	leasePurpose                        sql.NullString
	leaseUntilMS                        sql.NullInt64
	backoffLevel                        int64
	consecutiveFailures                 int64
	confirmationFailuresRequired        int64
	confirmationFailureEvidenceKeysJSON string
	recoveringSuccesses                 int64
	nextTransitionAtMS                  sql.NullInt64
	openUntilMS                         sql.NullInt64
	lastFailureClass                    sql.NullString
	updatedAtMS                         int64
}

const incidentScanTargets = `
  circuit_scope_key, account_id, account_runtime_key, scope_kind, key_fingerprint,
  protocol_code, request_lane, model_family,
  incident_id, parent_incident_id,
  child_incident_ids_json, state,
  generation, dispatch_revision, ledger_revision,
  transition_id, lease_id, lease_purpose, lease_until_ms,
  backoff_level, consecutive_failures, confirmation_failures_required,
  confirmation_failure_evidence_keys_json, recovering_successes,
  next_transition_at_ms, open_until_ms, last_failure_class, updated_at_ms
`

func scanIncident(row interface{ Scan(...any) error }) (opsjobs.CircuitIncidentRecord, error) {
	var scanned incidentRow
	var childJSON, evidenceJSON string
	targets := []any{
		&scanned.scopeKey, &scanned.accountID, &scanned.accountRuntimeKey, &scanned.scopeKind,
		&scanned.keyFingerprint, &scanned.protocolCode, &scanned.requestLane, &scanned.modelFamily,
		&scanned.incidentID, &scanned.parentIncidentID,
		&childJSON, &scanned.state,
		&scanned.generation, &scanned.dispatchRevision, &scanned.ledgerRevision,
		&scanned.transitionID, &scanned.leaseID, &scanned.leasePurpose, &scanned.leaseUntilMS,
		&scanned.backoffLevel, &scanned.consecutiveFailures, &scanned.confirmationFailuresRequired,
		&evidenceJSON, &scanned.recoveringSuccesses,
		&scanned.nextTransitionAtMS, &scanned.openUntilMS, &scanned.lastFailureClass, &scanned.updatedAtMS,
	}
	if err := row.Scan(targets...); err != nil {
		return opsjobs.CircuitIncidentRecord{}, err
	}
	scanned.childIncidentIDsJSON = childJSON
	scanned.confirmationFailureEvidenceKeysJSON = evidenceJSON
	return mapIncidentRow(scanned)
}

func mapIncidentRow(row incidentRow) (opsjobs.CircuitIncidentRecord, error) {
	childIncidentIDs, err := parseBoundedIDArray(row.childIncidentIDsJSON)
	if err != nil {
		return opsjobs.CircuitIncidentRecord{}, err
	}
	evidenceKeys, err := parseEvidenceKeys(row.confirmationFailureEvidenceKeysJSON, row.confirmationFailuresRequired)
	if err != nil {
		return opsjobs.CircuitIncidentRecord{}, err
	}
	record := opsjobs.CircuitIncidentRecord{
		AccountID:                       row.accountID,
		AccountRuntimeKey:               row.accountRuntimeKey,
		IncidentID:                      row.incidentID,
		CircuitScopeKey:                 row.scopeKey,
		ScopeKind:                       row.scopeKind,
		ChildIncidentIDs:                childIncidentIDs,
		State:                           opsjobs.CircuitIncidentState(row.state),
		Generation:                      row.generation,
		DispatchRevision:                row.dispatchRevision,
		LedgerRevision:                  row.ledgerRevision,
		TransitionID:                    row.transitionID,
		BackoffLevel:                    int(row.backoffLevel),
		ConsecutiveFailures:             int(row.consecutiveFailures),
		ConfirmationFailuresRequired:    int(row.confirmationFailuresRequired),
		ConfirmationFailureEvidenceKeys: evidenceKeys,
		RecoveringSuccesses:             int(row.recoveringSuccesses),
		UpdatedAtMS:                     row.updatedAtMS,
	}
	if row.parentIncidentID.Valid && row.parentIncidentID.String != "" {
		record.ParentIncidentID = row.parentIncidentID.String
	}
	if row.keyFingerprint.Valid && row.keyFingerprint.String != "" {
		record.KeyFingerprint = row.keyFingerprint.String
	}
	if row.protocolCode.Valid && row.protocolCode.String != "" {
		record.ProtocolCode = row.protocolCode.String
	}
	if row.requestLane.Valid && row.requestLane.String != "" {
		record.RequestLane = row.requestLane.String
	}
	if row.modelFamily.Valid && row.modelFamily.String != "" {
		record.ModelFamily = row.modelFamily.String
	}
	if row.leaseID.Valid && row.leaseID.String != "" {
		record.LeaseID = row.leaseID.String
	}
	if row.leasePurpose.Valid && row.leasePurpose.String != "" {
		record.LeasePurpose = row.leasePurpose.String
	}
	if row.leaseUntilMS.Valid {
		value := row.leaseUntilMS.Int64
		record.LeaseUntilMS = &value
	}
	if row.nextTransitionAtMS.Valid {
		value := row.nextTransitionAtMS.Int64
		record.NextTransitionAtMS = &value
	}
	if row.openUntilMS.Valid {
		value := row.openUntilMS.Int64
		record.OpenUntilMS = &value
	}
	if row.lastFailureClass.Valid && row.lastFailureClass.String != "" {
		record.LastFailureClass = row.lastFailureClass.String
	}
	return record, nil
}

func parseBoundedIDArray(value string) ([]string, error) {
	var parsed []string
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, errors.New("持久化 childIncidentIds 不是合法有界数组")
	}
	if len(parsed) > 64 {
		return nil, errors.New("childIncidentIds 最多包含 64 项")
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(parsed))
	for index, item := range parsed {
		text := strings.TrimSpace(item)
		if text == "" || len(text) > 256 {
			return nil, fmt.Errorf("childIncidentIds[%d] 长度必须为 1..256", index)
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		normalized = append(normalized, text)
	}
	return normalized, nil
}

func parseEvidenceKeys(value string, required int64) ([]string, error) {
	var parsed []string
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, errors.New("持久化 confirmationFailureEvidenceKeys 不是合法 JSON")
	}
	if len(parsed) > int(required)+1 {
		return nil, fmt.Errorf("confirmationFailureEvidenceKeys 最多包含 %d 项", required+1)
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(parsed))
	for _, item := range parsed {
		key := strings.ToLower(strings.TrimSpace(item))
		if len(key) != 64 || !isSHA256Hex(key) {
			return nil, errors.New("confirmationFailureEvidenceKeys 只能包含 SHA256")
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized, nil
}

// ---- opsjobs.ControlPlaneLedger ----

// ListForRebuild 对齐 listAccountCircuitIncidentsForRebuildInClient。
func (r *ControlPlaneRepo) ListForRebuild(ctx context.Context, query opsjobs.RebuildPageQuery) (opsjobs.RebuildPage, error) {
	limit := query.Limit
	if limit < 1 {
		return opsjobs.RebuildPage{}, errors.New("limit 必须是正整数")
	}
	afterUpdatedAt := int64(-1)
	if query.AfterUpdatedAtMS != nil {
		afterUpdatedAt = *query.AfterUpdatedAtMS
	}
	afterScopeKey := ""
	if query.AfterCircuitScopeKey != nil {
		afterScopeKey = strings.TrimSpace(*query.AfterCircuitScopeKey)
		if len(afterScopeKey) > 2048 {
			return opsjobs.RebuildPage{}, errors.New("afterCircuitScopeKey 长度必须为 0..2048")
		}
	}
	// Node SQL：closed 行仅保留 retained tombstone；dispatch_revision 必须仍
	// 等于未删除账户的当前值（账户被删除/推进后旧 incident 不再回放）。
	sqlQuery := `
    SELECT ` + incidentScanTargets + `
    FROM ` + r.table("account_circuit_incidents") + ` circuit_incident
    WHERE (state <> 'CLOSED' OR retained_until_ms > ?)
      AND dispatch_revision = (
        SELECT current_account.dispatch_revision
        FROM ` + r.table("accounts") + ` current_account
        WHERE current_account.id = circuit_incident.account_id
          AND current_account.deleted_at IS NULL
      )
      AND (updated_at_ms > ? OR (updated_at_ms = ? AND circuit_scope_key > ?))
    ORDER BY updated_at_ms ASC, circuit_scope_key ASC
    LIMIT ?`
	rows, err := r.db.QueryContext(ctx, sqlQuery, query.NowMS, afterUpdatedAt, afterUpdatedAt, afterScopeKey, limit)
	if err != nil {
		return opsjobs.RebuildPage{}, err
	}
	defer rows.Close()
	page := opsjobs.RebuildPage{Items: []opsjobs.CircuitIncidentRecord{}}
	for rows.Next() {
		record, err := scanIncident(rows)
		if err != nil {
			return opsjobs.RebuildPage{}, err
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		return opsjobs.RebuildPage{}, err
	}
	if len(page.Items) == limit {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &opsjobs.IncidentCursor{UpdatedAtMS: last.UpdatedAtMS, CircuitScopeKey: last.CircuitScopeKey}
	}
	return page, nil
}

// ListByRuntimeKeys 对齐 listAccountCircuitIncidentsByRuntimeKeysInClient。
func (r *ControlPlaneRepo) ListByRuntimeKeys(ctx context.Context, accountRuntimeKeys []string, includeRetainedClosed bool, nowMS int64) ([]opsjobs.CircuitIncidentRecord, error) {
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(accountRuntimeKeys))
	for _, key := range accountRuntimeKeys {
		normalized := strings.TrimSpace(key)
		if normalized == "" || len(normalized) > 1024 {
			return nil, errors.New("accountRuntimeKey 长度必须为 1..1024")
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		keys = append(keys, normalized)
	}
	if len(keys) == 0 {
		return []opsjobs.CircuitIncidentRecord{}, nil
	}
	if len(keys) > 100 {
		return nil, errors.New("账户 circuit 摘要单次最多查询 100 个运行态键")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(keys)), ", ")
	stateFilter := "state <> 'CLOSED'"
	args := make([]any, 0, len(keys)+1)
	for _, key := range keys {
		args = append(args, key)
	}
	if includeRetainedClosed {
		stateFilter = "(state <> 'CLOSED' OR retained_until_ms > ?)"
		args = append(args, nowMS)
	}
	sqlQuery := `
    SELECT ` + incidentScanTargets + `
    FROM ` + r.table("account_circuit_incidents") + ` circuit_incident
    WHERE account_runtime_key IN (` + placeholders + `)
      AND ` + stateFilter + `
      AND dispatch_revision = (
        SELECT current_account.dispatch_revision
        FROM ` + r.table("accounts") + ` current_account
        WHERE current_account.id = circuit_incident.account_id
          AND current_account.deleted_at IS NULL
      )
    ORDER BY account_runtime_key ASC, updated_at_ms ASC, circuit_scope_key ASC`
	rows, err := r.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []opsjobs.CircuitIncidentRecord{}
	for rows.Next() {
		record, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// GetByScopeKey 对齐 getAccountCircuitIncidentByScopeKeyInClient。
func (r *ControlPlaneRepo) GetByScopeKey(ctx context.Context, circuitScopeKey string) (*opsjobs.CircuitIncidentRecord, error) {
	scopeKey := strings.TrimSpace(circuitScopeKey)
	if scopeKey == "" || len(scopeKey) > 2048 {
		return nil, errors.New("circuitScopeKey 长度必须为 1..2048")
	}
	row := r.db.QueryRowContext(ctx, `
    SELECT `+incidentScanTargets+`
    FROM `+r.table("account_circuit_incidents")+` circuit_incident
    WHERE circuit_scope_key = ?`, scopeKey)
	record, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// ---- ledger CAS 写侧（与 gateway 写侧成对维护）----

// 与 gateway circuitcontrolplane 同名哨兵错误逐字对齐（跨 module 不可
// import，成对复制）。
var (
	// ErrCAS 是 upsert 账户守卫（WHERE account_id=excluded.account_id）未
	// 命中时的冲突错误。
	ErrCAS = errors.New("account circuit compare-and-set conflict")
	// ErrIdentityReplay 是 dedupe 回放身份不一致的错误。
	ErrIdentityReplay = errors.New("account circuit replay identity conflict")
)

// incidentCASColumns 与 gateway store.go incidentColumns 逐字一致（44 列，
// 同一张物理表；成对复制约定——修改任一侧必须同步另一侧并核对列序）。
const incidentCASColumns = "circuit_scope_key,account_id,account_runtime_key,scope_kind,key_fingerprint,protocol_code,request_lane,model_family,client_model,capability_hash,credential_source_account_id,client_endpoint_family,final_upstream_model,upstream_endpoint_mode,incident_id,parent_incident_id,child_incident_ids_json,caused_by_terminal_outcome_id,state,failure_scope,generation,dispatch_revision,ledger_revision,projected_ledger_revision,transition_id,cooldown_observation_generation,open_until_ms,next_transition_at_ms,lease_id,lease_purpose,lease_owner_run_id,lease_until_ms,attempt_started_at_ms,attempt_hard_deadline_ms,upstream_attempt_observed,backoff_level,consecutive_failures,confirmation_failures_required,confirmation_failure_evidence_keys_json,recovering_successes,last_failure_class,retained_until_ms,created_at_ms,updated_at_ms"

// circuitIncidentCleanupBatchLimit 限制 Cleanup 单批删除行数。
const circuitIncidentCleanupBatchLimit = 500

func casBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func casNullStringPtr(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	copied := value.String
	return &copied
}

func casNullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copied := value.Int64
	return &copied
}

// scanIncidentCASRow 按 incidentCASColumns 列序扫描全字段行。
func scanIncidentCASRow(row interface{ Scan(...any) error }) (opsjobs.IncidentCASRow, error) {
	var (
		v                opsjobs.IncidentCASRow
		keyFingerprint   sql.NullString
		protocolCode     sql.NullString
		requestLane      sql.NullString
		modelFamily      sql.NullString
		clientModel      sql.NullString
		capabilityHash   sql.NullString
		credentialSource sql.NullString
		clientEndpoint   sql.NullString
		finalUpstream    sql.NullString
		upstreamMode     sql.NullString
		parentIncidentID sql.NullString
		causedBy         sql.NullString
		failureScope     sql.NullString
		children         string
		evidence         string
		openUntil        sql.NullInt64
		nextTransition   sql.NullInt64
		leaseID          sql.NullString
		leasePurpose     sql.NullString
		leaseOwnerRunID  sql.NullString
		leaseUntil       sql.NullInt64
		attemptStarted   sql.NullInt64
		attemptDeadline  sql.NullInt64
		upstreamObserved int
		lastFailureClass sql.NullString
		retainedUntil    sql.NullInt64
	)
	if err := row.Scan(
		&v.CircuitScopeKey, &v.AccountID, &v.AccountRuntimeKey, &v.ScopeKind,
		&keyFingerprint, &protocolCode, &requestLane, &modelFamily,
		&clientModel, &capabilityHash, &credentialSource, &clientEndpoint,
		&finalUpstream, &upstreamMode,
		&v.IncidentID, &parentIncidentID, &children, &causedBy,
		&v.State, &failureScope,
		&v.Generation, &v.DispatchRevision, &v.LedgerRevision, &v.ProjectedLedgerRevision,
		&v.TransitionID, &v.CooldownObservationGeneration,
		&openUntil, &nextTransition,
		&leaseID, &leasePurpose, &leaseOwnerRunID, &leaseUntil,
		&attemptStarted, &attemptDeadline,
		&upstreamObserved, &v.BackoffLevel, &v.ConsecutiveFailures, &v.ConfirmationFailuresRequired,
		&evidence, &v.RecoveringSuccesses,
		&lastFailureClass, &retainedUntil, &v.CreatedAtMS, &v.UpdatedAtMS,
	); err != nil {
		return opsjobs.IncidentCASRow{}, err
	}
	childIncidentIDs, err := parseBoundedIDArray(children)
	if err != nil {
		return opsjobs.IncidentCASRow{}, err
	}
	v.ChildIncidentIDs = childIncidentIDs
	evidenceKeys, err := parseEvidenceKeys(evidence, v.ConfirmationFailuresRequired)
	if err != nil {
		return opsjobs.IncidentCASRow{}, err
	}
	v.ConfirmationFailureEvidenceKeys = evidenceKeys
	v.KeyFingerprint = casNullStringPtr(keyFingerprint)
	v.ProtocolCode = casNullStringPtr(protocolCode)
	v.RequestLane = casNullStringPtr(requestLane)
	v.ModelFamily = casNullStringPtr(modelFamily)
	v.ClientModel = casNullStringPtr(clientModel)
	v.CapabilityHash = casNullStringPtr(capabilityHash)
	v.CredentialSourceAccountID = casNullStringPtr(credentialSource)
	v.ClientEndpointFamily = casNullStringPtr(clientEndpoint)
	v.FinalUpstreamModel = casNullStringPtr(finalUpstream)
	v.UpstreamEndpointMode = casNullStringPtr(upstreamMode)
	v.ParentIncidentID = casNullStringPtr(parentIncidentID)
	v.CausedByTerminalOutcomeID = casNullStringPtr(causedBy)
	v.FailureScope = casNullStringPtr(failureScope)
	v.OpenUntilMS = casNullInt64Ptr(openUntil)
	v.NextTransitionAtMS = casNullInt64Ptr(nextTransition)
	v.LeaseID = casNullStringPtr(leaseID)
	v.LeasePurpose = casNullStringPtr(leasePurpose)
	v.LeaseOwnerRunID = casNullStringPtr(leaseOwnerRunID)
	v.LeaseUntilMS = casNullInt64Ptr(leaseUntil)
	v.AttemptStartedAtMS = casNullInt64Ptr(attemptStarted)
	v.AttemptHardDeadlineMS = casNullInt64Ptr(attemptDeadline)
	v.UpstreamAttemptObserved = upstreamObserved != 0
	v.LastFailureClass = casNullStringPtr(lastFailureClass)
	v.RetainedUntilMS = casNullInt64Ptr(retainedUntil)
	return v, nil
}

// CompareAndSetIncident 逐语义移植 gateway
// circuitcontrolplane.Store.CompareAndSetIncident：账户行锁 → 归档终态围栏
// （行缺失/deleted_at 非空 → account_not_found；dispatch revision 失配 →
// stale_dispatch_revision）→ outbox dedupe 幂等回放 → incident 行锁 →
// expected/generation CAS → ledger_revision+1 → 全列 upsert（账户守卫）+
// 同事务 incident_changed outbox。PG 对账户行与 incident 行加 FOR UPDATE，
// SQLite 依赖单 writer 串行等价。
func (r *ControlPlaneRepo) CompareAndSetIncident(ctx context.Context, input opsjobs.IncidentCASInput) (opsjobs.IncidentCASResult, error) {
	incident := input.Incident
	if incident.ChildIncidentIDs == nil {
		incident.ChildIncidentIDs = []string{}
	}
	if incident.ConfirmationFailureEvidenceKeys == nil {
		incident.ConfirmationFailureEvidenceKeys = []string{}
	}
	if input.ExpectedLedgerRevision != nil && *input.ExpectedLedgerRevision < 0 {
		return opsjobs.IncidentCASResult{}, errors.New("expected ledger revision cannot be negative")
	}
	if err := validateIncidentCAS(&incident); err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	if err := validateIncidentCASTimes(&incident, incident.UpdatedAtMS); err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	now := incident.UpdatedAtMS
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	lockClause := ""
	if r.postgres {
		lockClause = " FOR UPDATE"
	}
	// 归档热修：物理清理会级联 circuit ledger，账户行锁 SELECT 需带
	// deleted_at；行缺失或已逻辑删除时迟到观察落 account_not_found 终态。
	var (
		currentDispatch  int64
		accountDeletedAt sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		"SELECT dispatch_revision, deleted_at FROM "+r.table("accounts")+" WHERE id=?"+lockClause,
		incident.AccountID).Scan(&currentDispatch, &accountDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASAccountNotFound, CurrentDispatchRevision: 0}, nil
	}
	if err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	if accountDeletedAt.Valid {
		_ = tx.Rollback()
		return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASAccountNotFound, CurrentDispatchRevision: currentDispatch}, nil
	}
	if currentDispatch != incident.DispatchRevision {
		_ = tx.Rollback()
		return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASStaleDispatchRevision, CurrentDispatchRevision: currentDispatch}, nil
	}
	dedupe := "incident:" + incident.TransitionID
	var (
		replayEventType  string
		replayAccountID  string
		replayRuntimeKey string
		replayScopeKey   sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		"SELECT event_type, account_id, account_runtime_key, circuit_scope_key FROM "+
			r.table("account_circuit_outbox")+" WHERE projection_key=? AND dedupe_key=?",
		ProjectionKey, dedupe).Scan(&replayEventType, &replayAccountID, &replayRuntimeKey, &replayScopeKey)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 无回放记录，继续 CAS。
	case err != nil:
		return opsjobs.IncidentCASResult{}, err
	default:
		if replayEventType != "incident_changed" || replayAccountID != incident.AccountID ||
			replayRuntimeKey != incident.AccountRuntimeKey || replayScopeKey.String != incident.CircuitScopeKey {
			return opsjobs.IncidentCASResult{}, ErrIdentityReplay
		}
		current, found, err := r.incidentCASByScope(ctx, tx, incident.CircuitScopeKey, false)
		if err != nil {
			return opsjobs.IncidentCASResult{}, err
		}
		if !found {
			return opsjobs.IncidentCASResult{}, errors.New("deduplicated incident receipt has no incident")
		}
		if err := tx.Commit(); err != nil {
			return opsjobs.IncidentCASResult{}, err
		}
		return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASIdempotent, CurrentDispatchRevision: currentDispatch, Incident: &current}, nil
	}
	// incident 行属于 CAS 临界区：PG 必须先锁已有行再比较/upsert，否则并发
	// 写可同时观察到同一 ledger revision 互相覆盖（注释对齐 gateway 写侧）。
	current, found, err := r.incidentCASByScope(ctx, tx, incident.CircuitScopeKey, true)
	if err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	if (input.ExpectedLedgerRevision == nil && found) ||
		(input.ExpectedLedgerRevision != nil && (!found || current.LedgerRevision != *input.ExpectedLedgerRevision)) {
		_ = tx.Rollback()
		var currentPtr *opsjobs.IncidentCASRow
		if found {
			currentPtr = &current
		}
		return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASConflict, CurrentDispatchRevision: currentDispatch, Incident: currentPtr}, nil
	}
	if found && current.AccountID != incident.AccountID {
		return opsjobs.IncidentCASResult{}, errors.New("circuit scope key belongs to another account")
	}
	if found && current.Generation > incident.Generation {
		_ = tx.Rollback()
		return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASConflict, CurrentDispatchRevision: currentDispatch, Incident: &current}, nil
	}
	if found {
		incident.ProjectedLedgerRevision = current.ProjectedLedgerRevision
		incident.CreatedAtMS = current.CreatedAtMS
		if err := validateIncidentCASTimes(&incident, now); err != nil {
			return opsjobs.IncidentCASResult{}, err
		}
		incident.LedgerRevision = current.LedgerRevision + 1
	} else {
		incident.ProjectedLedgerRevision = 0
		incident.LedgerRevision = 1
	}
	if err := r.upsertIncidentCAS(ctx, tx, incident); err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	if err := r.insertIncidentChangedOutbox(ctx, tx, incident, dedupe, now); err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return opsjobs.IncidentCASResult{}, err
	}
	return opsjobs.IncidentCASResult{Status: opsjobs.IncidentCASApplied, CurrentDispatchRevision: currentDispatch, Incident: &incident}, nil
}

func (r *ControlPlaneRepo) incidentCASByScope(ctx context.Context, q txLike, scopeKey string, forUpdate bool) (opsjobs.IncidentCASRow, bool, error) {
	query := "SELECT " + incidentCASColumns + " FROM " + r.table("account_circuit_incidents") + " WHERE circuit_scope_key=?"
	if forUpdate && r.postgres {
		query += " FOR UPDATE"
	}
	row, err := scanIncidentCASRow(q.QueryRowContext(ctx, query, scopeKey))
	if errors.Is(err, sql.ErrNoRows) {
		return opsjobs.IncidentCASRow{}, false, nil
	}
	if err != nil {
		return opsjobs.IncidentCASRow{}, false, err
	}
	return row, true, nil
}

func (r *ControlPlaneRepo) upsertIncidentCAS(ctx context.Context, tx *sql.Tx, v opsjobs.IncidentCASRow) error {
	if v.ChildIncidentIDs == nil {
		v.ChildIncidentIDs = []string{}
	}
	if v.ConfirmationFailureEvidenceKeys == nil {
		v.ConfirmationFailureEvidenceKeys = []string{}
	}
	children, err := json.Marshal(v.ChildIncidentIDs)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(v.ConfirmationFailureEvidenceKeys)
	if err != nil {
		return err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", strings.Count(incidentCASColumns, ",")+1), ",")
	// ON CONFLICT 全列覆盖 + 账户守卫，与 gateway upsertIncident 逐字对齐
	// （守卫未命中 0 行受影响 → ErrCAS）。
	query := "INSERT INTO " + r.table("account_circuit_incidents") + " (" + incidentCASColumns + ") VALUES (" + placeholders + ") " +
		"ON CONFLICT(circuit_scope_key) DO UPDATE SET " +
		"account_id=excluded.account_id,account_runtime_key=excluded.account_runtime_key,scope_kind=excluded.scope_kind," +
		"key_fingerprint=excluded.key_fingerprint,protocol_code=excluded.protocol_code,request_lane=excluded.request_lane," +
		"model_family=excluded.model_family,client_model=excluded.client_model,capability_hash=excluded.capability_hash," +
		"credential_source_account_id=excluded.credential_source_account_id,client_endpoint_family=excluded.client_endpoint_family," +
		"final_upstream_model=excluded.final_upstream_model,upstream_endpoint_mode=excluded.upstream_endpoint_mode," +
		"incident_id=excluded.incident_id,parent_incident_id=excluded.parent_incident_id," +
		"child_incident_ids_json=excluded.child_incident_ids_json,caused_by_terminal_outcome_id=excluded.caused_by_terminal_outcome_id," +
		"state=excluded.state,failure_scope=excluded.failure_scope,generation=excluded.generation," +
		"dispatch_revision=excluded.dispatch_revision,ledger_revision=excluded.ledger_revision," +
		"projected_ledger_revision=excluded.projected_ledger_revision,transition_id=excluded.transition_id," +
		"cooldown_observation_generation=excluded.cooldown_observation_generation,open_until_ms=excluded.open_until_ms," +
		"next_transition_at_ms=excluded.next_transition_at_ms,lease_id=excluded.lease_id,lease_purpose=excluded.lease_purpose," +
		"lease_owner_run_id=excluded.lease_owner_run_id,lease_until_ms=excluded.lease_until_ms," +
		"attempt_started_at_ms=excluded.attempt_started_at_ms,attempt_hard_deadline_ms=excluded.attempt_hard_deadline_ms," +
		"upstream_attempt_observed=excluded.upstream_attempt_observed,backoff_level=excluded.backoff_level," +
		"consecutive_failures=excluded.consecutive_failures,confirmation_failures_required=excluded.confirmation_failures_required," +
		"confirmation_failure_evidence_keys_json=excluded.confirmation_failure_evidence_keys_json," +
		"recovering_successes=excluded.recovering_successes,last_failure_class=excluded.last_failure_class," +
		"retained_until_ms=excluded.retained_until_ms,updated_at_ms=excluded.updated_at_ms " +
		"WHERE account_circuit_incidents.account_id=excluded.account_id"
	result, err := tx.ExecContext(ctx, query,
		v.CircuitScopeKey, v.AccountID, v.AccountRuntimeKey, v.ScopeKind,
		v.KeyFingerprint, v.ProtocolCode, v.RequestLane, v.ModelFamily,
		v.ClientModel, v.CapabilityHash, v.CredentialSourceAccountID, v.ClientEndpointFamily,
		v.FinalUpstreamModel, v.UpstreamEndpointMode,
		v.IncidentID, v.ParentIncidentID, string(children), v.CausedByTerminalOutcomeID,
		v.State, v.FailureScope,
		v.Generation, v.DispatchRevision, v.LedgerRevision, v.ProjectedLedgerRevision,
		v.TransitionID, v.CooldownObservationGeneration,
		v.OpenUntilMS, v.NextTransitionAtMS,
		v.LeaseID, v.LeasePurpose, v.LeaseOwnerRunID, v.LeaseUntilMS,
		v.AttemptStartedAtMS, v.AttemptHardDeadlineMS,
		casBoolInt(v.UpstreamAttemptObserved), v.BackoffLevel, v.ConsecutiveFailures, v.ConfirmationFailuresRequired,
		string(evidence), v.RecoveringSuccesses,
		v.LastFailureClass, v.RetainedUntilMS, v.CreatedAtMS, v.UpdatedAtMS)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrCAS
	}
	return nil
}

// insertIncidentChangedOutbox 列集与 gateway insertOutbox 一致（claim 面
// 列保持 NULL，由 claim 更新）。
func (r *ControlPlaneRepo) insertIncidentChangedOutbox(ctx context.Context, tx *sql.Tx, v opsjobs.IncidentCASRow, dedupe string, nowMS int64) error {
	scopeKey := v.CircuitScopeKey
	incidentID := v.IncidentID
	generation := v.Generation
	ledgerRevision := v.LedgerRevision
	query := "INSERT INTO " + r.table("account_circuit_outbox") +
		" (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id," +
		"transition_id,dispatch_revision,generation,ledger_revision,status,available_at_ms,attempt_count,created_at_ms,updated_at_ms)" +
		" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	_, err := tx.ExecContext(ctx, query,
		newClaimToken(), ProjectionKey, dedupe, "incident_changed",
		v.AccountID, v.AccountRuntimeKey, &scopeKey, &incidentID,
		v.TransitionID, v.DispatchRevision, &generation, &ledgerRevision,
		"pending", nowMS, 0, nowMS, nowMS)
	return err
}

// Cleanup 对齐 gateway Cleanup 的 incident 删除条件：CLOSED 且保留期到期、
// 投影水位已覆盖、无未 dispatched outbox。dispatched outbox 的清理由 gateway
// 写侧维护，本方法只删 incident 行。
func (r *ControlPlaneRepo) Cleanup(ctx context.Context, nowMS int64) (int64, error) {
	if nowMS < 0 {
		return 0, errors.New("nowMs 必须是非负整数")
	}
	incidents := r.table("account_circuit_incidents")
	outbox := r.table("account_circuit_outbox")
	condition := "state='CLOSED' AND retained_until_ms<=? AND projected_ledger_revision>=ledger_revision" +
		" AND NOT EXISTS (SELECT 1 FROM " + outbox + " o WHERE o.circuit_scope_key=" + incidents + ".circuit_scope_key AND o.status<>'dispatched')"
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	query := "SELECT circuit_scope_key FROM " + incidents +
		" WHERE " + condition + " ORDER BY retained_until_ms,updated_at_ms,circuit_scope_key LIMIT ?"
	rows, err := tx.QueryContext(ctx, query, nowMS, circuitIncidentCleanupBatchLimit)
	if err != nil {
		return 0, err
	}
	var scopeKeys []string
	for rows.Next() {
		var scopeKey string
		if err := rows.Scan(&scopeKey); err != nil {
			rows.Close()
			return 0, err
		}
		scopeKeys = append(scopeKeys, scopeKey)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(scopeKeys) == 0 {
		return 0, tx.Commit()
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scopeKeys)), ",")
	args := make([]any, len(scopeKeys))
	for i := range scopeKeys {
		args[i] = scopeKeys[i]
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+incidents+" WHERE circuit_scope_key IN ("+placeholders+")", args...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

// ListActiveIncidentsPage 是不带 dispatch_revision 围栏的活动行 keyset 分页
// 读（state<>'CLOSED'），供孤儿结清扫描使用：围栏内的行归 CAS 结清，围栏外
// 的行 CAS 会返回 stale 丢弃——语义正确。
func (r *ControlPlaneRepo) ListActiveIncidentsPage(ctx context.Context, afterUpdatedAtMS int64, afterScopeKey string, limit int) ([]opsjobs.IncidentCASRow, error) {
	if limit < 1 {
		return nil, errors.New("limit 必须是正整数")
	}
	rows, err := r.db.QueryContext(ctx, `
    SELECT `+incidentCASColumns+`
    FROM `+r.table("account_circuit_incidents")+` circuit_incident
    WHERE state <> 'CLOSED'
      AND (updated_at_ms > ? OR (updated_at_ms = ? AND circuit_scope_key > ?))
    ORDER BY updated_at_ms ASC, circuit_scope_key ASC
    LIMIT ?`, afterUpdatedAtMS, afterUpdatedAtMS, afterScopeKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []opsjobs.IncidentCASRow{}
	for rows.Next() {
		record, err := scanIncidentCASRow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ---- CAS 写侧入参校验（对照 gateway validateIncident / validateIncidentTimes，
// 成对复制；唯一刻意偏差见 validateIncidentCAS 内注释）----

var casIncidentStates = map[string]bool{
	"CLOSED": true, "SUSPECT": true, "OPEN": true, "HALF_OPEN": true,
	"RECOVERING": true, "PERSISTING": true, "SHADOWED_BY_PERSISTENT": true,
}
var casScopeKinds = map[string]bool{"account": true, "key": true, "protocol_model": true, "key_model": true}
var casLeasePurposes = map[string]bool{"confirmation": true, "half_open": true, "recovery": true, "cooldown_retest": true, "background_probe": true}
var casFailureClasses = map[string]bool{"connect_failed": true, "timeout_before_complete": true, "read_interrupted": true, "incomplete_response": true, "explicit_policy": true}

func casRequireText(v, name string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func casRequireTextBounded(v string, maxLength int, name string) error {
	if err := casRequireText(v, name); err != nil {
		return err
	}
	if len(v) > maxLength {
		return fmt.Errorf("%s is too long", name)
	}
	return nil
}

func casValidateOptionalText(value *string, maxLength int, name string) error {
	if value == nil {
		return nil
	}
	if err := casRequireText(*value, name); err != nil {
		return err
	}
	if len(*value) > maxLength {
		return fmt.Errorf("%s is too long", name)
	}
	return nil
}

func casValidateRuntimeKey(v string) error {
	// Node 持久化契约把 runtime key 当作有界文本，不限制字符集。
	return casRequireTextBounded(v, 1024, "account runtime key")
}

func casHasKeyModelFields(v *opsjobs.IncidentCASRow) bool {
	return v.ClientModel != nil || v.CapabilityHash != nil || v.CredentialSourceAccountID != nil ||
		v.ClientEndpointFamily != nil || v.FinalUpstreamModel != nil || v.UpstreamEndpointMode != nil
}

func casHasAllKeyModelFields(v *opsjobs.IncidentCASRow) bool {
	return v.ClientModel != nil && v.CapabilityHash != nil && v.CredentialSourceAccountID != nil &&
		v.ClientEndpointFamily != nil && v.FinalUpstreamModel != nil && v.UpstreamEndpointMode != nil
}

func casValidateScopeShape(v *opsjobs.IncidentCASRow) error {
	switch v.ScopeKind {
	case "account":
		if v.KeyFingerprint != nil || v.ProtocolCode != nil || v.RequestLane != nil || v.ModelFamily != nil || casHasKeyModelFields(v) {
			return errors.New("account scope cannot carry key/protocol/model fields")
		}
	case "key":
		if v.KeyFingerprint == nil || v.ProtocolCode != nil || v.RequestLane != nil || v.ModelFamily != nil || casHasKeyModelFields(v) {
			return errors.New("key scope requires only key fingerprint")
		}
	case "protocol_model":
		if v.KeyFingerprint != nil || v.ProtocolCode == nil || v.RequestLane == nil || v.ModelFamily == nil || casHasKeyModelFields(v) {
			return errors.New("protocol_model scope requires protocol, request lane and model family")
		}
	case "key_model":
		if v.KeyFingerprint == nil || v.ProtocolCode != nil || v.RequestLane != nil || v.ModelFamily != nil || !casHasAllKeyModelFields(v) {
			return errors.New("key_model scope requires key fingerprint and complete key-model identity")
		}
	}
	return nil
}

func casNormalizeIncidentText(v *opsjobs.IncidentCASRow) error {
	v.CircuitScopeKey = strings.TrimSpace(v.CircuitScopeKey)
	if err := casRequireTextBounded(v.CircuitScopeKey, 2048, "circuit scope key"); err != nil {
		return err
	}
	v.AccountID = strings.TrimSpace(v.AccountID)
	if err := casRequireTextBounded(v.AccountID, 256, "account id"); err != nil {
		return err
	}
	v.AccountRuntimeKey = strings.TrimSpace(v.AccountRuntimeKey)
	if err := casRequireTextBounded(v.AccountRuntimeKey, 1024, "account runtime key"); err != nil {
		return err
	}
	v.TransitionID = strings.TrimSpace(v.TransitionID)
	if err := casRequireTextBounded(v.TransitionID, 256, "transition id"); err != nil {
		return err
	}
	v.IncidentID = strings.TrimSpace(v.IncidentID)
	if err := casRequireTextBounded(v.IncidentID, 256, "incident id"); err != nil {
		return err
	}
	for name, field := range map[string]*string{
		"parent incident id":            v.ParentIncidentID,
		"caused by terminal outcome id": v.CausedByTerminalOutcomeID,
		"key fingerprint":               v.KeyFingerprint,
		"protocol code":                 v.ProtocolCode,
		"request lane":                  v.RequestLane,
		"model family":                  v.ModelFamily,
		"client model":                  v.ClientModel,
		"capability hash":               v.CapabilityHash,
		"credential source account id":  v.CredentialSourceAccountID,
		"client endpoint family":        v.ClientEndpointFamily,
		"final upstream model":          v.FinalUpstreamModel,
		"upstream endpoint mode":        v.UpstreamEndpointMode,
		"lease id":                      v.LeaseID,
		"lease owner run id":            v.LeaseOwnerRunID,
	} {
		if field == nil {
			continue
		}
		*field = strings.TrimSpace(*field)
		max := 256
		switch name {
		case "key fingerprint":
			max = 256
		case "protocol code", "request lane":
			max = 64
		case "model family", "client model", "final upstream model":
			max = 256
		case "capability hash":
			max = 128
		case "credential source account id":
			max = 256
		case "client endpoint family", "upstream endpoint mode":
			max = 128
		}
		if err := casRequireTextBounded(*field, max, name); err != nil {
			return err
		}
	}
	return nil
}

func validateIncidentCAS(v *opsjobs.IncidentCASRow) error {
	if err := casNormalizeIncidentText(v); err != nil {
		return err
	}
	for name, field := range map[string]struct {
		value string
		max   int
	}{
		"circuit scope key":   {v.CircuitScopeKey, 2048},
		"account id":          {v.AccountID, 256},
		"account runtime key": {v.AccountRuntimeKey, 1024},
		"scope kind":          {v.ScopeKind, 32},
		"incident id":         {v.IncidentID, 256},
		"state":               {v.State, 64},
		"transition id":       {v.TransitionID, 256},
	} {
		if err := casRequireTextBounded(field.value, field.max, name); err != nil {
			return err
		}
	}
	if err := casValidateRuntimeKey(v.AccountRuntimeKey); err != nil {
		return err
	}
	if !casIncidentStates[v.State] {
		return fmt.Errorf("invalid incident state: %s", v.State)
	}
	if !casScopeKinds[v.ScopeKind] {
		return fmt.Errorf("invalid scope kind: %s", v.ScopeKind)
	}
	if v.FailureScope != nil && *v.FailureScope != "" && !casScopeKinds[*v.FailureScope] {
		return fmt.Errorf("invalid failure scope: %s", *v.FailureScope)
	}
	if err := casValidateScopeShape(v); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.KeyFingerprint, 256, "key fingerprint"); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.ProtocolCode, 64, "protocol code"); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.RequestLane, 64, "request lane"); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.ModelFamily, 256, "model family"); err != nil {
		return err
	}
	for name, field := range map[string]struct {
		value *string
		max   int
	}{
		"client model":                 {v.ClientModel, 256},
		"capability hash":              {v.CapabilityHash, 128},
		"credential source account id": {v.CredentialSourceAccountID, 256},
		"client endpoint family":       {v.ClientEndpointFamily, 128},
		"final upstream model":         {v.FinalUpstreamModel, 256},
		"upstream endpoint mode":       {v.UpstreamEndpointMode, 128},
	} {
		if err := casValidateOptionalText(field.value, field.max, name); err != nil {
			return err
		}
	}
	if err := casValidateOptionalText(v.LeaseID, 256, "lease id"); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.LeaseOwnerRunID, 256, "lease owner run id"); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.ParentIncidentID, 256, "parent incident id"); err != nil {
		return err
	}
	if err := casValidateOptionalText(v.CausedByTerminalOutcomeID, 256, "caused by terminal outcome id"); err != nil {
		return err
	}
	if v.LeasePurpose != nil && !casLeasePurposes[*v.LeasePurpose] {
		return fmt.Errorf("invalid lease purpose: %s", *v.LeasePurpose)
	}
	leaseFieldCount := 0
	if v.LeaseID != nil {
		leaseFieldCount++
	}
	if v.LeasePurpose != nil {
		leaseFieldCount++
	}
	if v.LeaseOwnerRunID != nil {
		leaseFieldCount++
	}
	if v.LeaseUntilMS != nil {
		leaseFieldCount++
	}
	if leaseFieldCount != 0 && leaseFieldCount != 4 {
		return errors.New("lease id, purpose, owner run id and until must be provided together")
	}
	if leaseFieldCount == 0 && (v.AttemptStartedAtMS != nil || v.AttemptHardDeadlineMS != nil) {
		return errors.New("attempt timestamps require an active lease")
	}
	// 刻意偏差（相对 gateway validateIncident）：gateway 在"租约四元组齐全"
	// 时还要求 attempt_started_at_ms / attempt_hard_deadline_ms 同时存在，但
	// Node 归档与 gateway 自身 bridge 的 CAS 输入（含本仓库投影器）都不携带
	// attempt 时间戳——照抄该条会让租约态 mutation 的投影全部被入参校验拒绝、
	// pending 永不结清。Node 归档 repository 无此校验，本侧对齐 Node。
	if v.LastFailureClass != nil && !casFailureClasses[*v.LastFailureClass] {
		return fmt.Errorf("invalid last failure class: %s", *v.LastFailureClass)
	}
	if len(v.ChildIncidentIDs) > 64 {
		return errors.New("child incident ids exceed the maximum of 64")
	}
	childIDs := make(map[string]struct{}, len(v.ChildIncidentIDs))
	for i, childID := range v.ChildIncidentIDs {
		childID = strings.TrimSpace(childID)
		v.ChildIncidentIDs[i] = childID
		if err := casRequireTextBounded(childID, 256, "child incident id"); err != nil {
			return err
		}
		if _, duplicate := childIDs[childID]; duplicate {
			return errors.New("child incident ids must be unique")
		}
		childIDs[childID] = struct{}{}
	}
	if len(v.ConfirmationFailureEvidenceKeys) > int(v.ConfirmationFailuresRequired)+1 {
		return errors.New("confirmation evidence keys exceed the configured bound")
	}
	evidenceKeys := make(map[string]struct{}, len(v.ConfirmationFailureEvidenceKeys))
	for i, evidenceKey := range v.ConfirmationFailureEvidenceKeys {
		evidenceKey = strings.ToLower(strings.TrimSpace(evidenceKey))
		v.ConfirmationFailureEvidenceKeys[i] = evidenceKey
		if !isSHA256Hex(evidenceKey) {
			return errors.New("confirmation evidence keys must be SHA256 values")
		}
		if _, duplicate := evidenceKeys[evidenceKey]; duplicate {
			return errors.New("confirmation evidence keys must be unique")
		}
		evidenceKeys[evidenceKey] = struct{}{}
	}
	if v.DispatchRevision < 1 || v.Generation < 0 || v.CooldownObservationGeneration < 0 ||
		v.ConsecutiveFailures < 0 || v.BackoffLevel < 0 || v.ConfirmationFailuresRequired < 1 || v.RecoveringSuccesses < 0 {
		return errors.New("incident numeric values are invalid")
	}
	if v.ConsecutiveFailures > v.ConfirmationFailuresRequired {
		return errors.New("consecutive failures exceed confirmation failures required")
	}
	if v.State == "CLOSED" && v.RetainedUntilMS == nil {
		return errors.New("closed incident requires retained_until_ms")
	}
	if v.State != "CLOSED" && v.RetainedUntilMS != nil {
		return errors.New("non-closed incident cannot have retained_until_ms")
	}
	return nil
}

func validateIncidentCASTimes(v *opsjobs.IncidentCASRow, nowMS int64) error {
	if v.CreatedAtMS < 0 {
		return errors.New("createdAtMs cannot be negative")
	}
	if v.UpdatedAtMS < 0 {
		return errors.New("updatedAtMs cannot be negative")
	}
	if v.CreatedAtMS > v.UpdatedAtMS {
		return errors.New("created_at_ms cannot follow updated_at_ms")
	}
	for name, value := range map[string]*int64{
		"openUntilMs": v.OpenUntilMS, "nextTransitionAtMs": v.NextTransitionAtMS,
		"leaseUntilMs": v.LeaseUntilMS, "attemptStartedAtMs": v.AttemptStartedAtMS,
		"attemptHardDeadlineMs": v.AttemptHardDeadlineMS, "retainedUntilMs": v.RetainedUntilMS,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	if v.State == "CLOSED" && v.RetainedUntilMS != nil && *v.RetainedUntilMS < nowMS {
		return errors.New("closed incident retained_until_ms cannot precede updated_at_ms")
	}
	if v.LeaseUntilMS != nil && v.AttemptStartedAtMS != nil && v.AttemptHardDeadlineMS != nil {
		if *v.AttemptStartedAtMS > *v.AttemptHardDeadlineMS || *v.AttemptHardDeadlineMS > *v.LeaseUntilMS {
			return errors.New("lease timestamps must satisfy attempt start <= hard deadline <= lease until")
		}
	}
	return nil
}

type outboxClaimRow struct {
	eventID           string
	projectionKey     string
	eventType         string
	accountID         string
	accountRuntimeKey string
	circuitScopeKey   sql.NullString
	transitionID      string
	dispatchRevision  int64
	claimToken        sql.NullString
}

// Claim 对齐 claimAccountCircuitOutboxInClient（PG FOR UPDATE SKIP LOCKED /
// SQLite 串行两段更新）。
func (r *ControlPlaneRepo) Claim(ctx context.Context, ownerID string, nowMS int64, leaseMS int64, limit int) ([]opsjobs.OutboxEvent, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || len(ownerID) > 128 {
		return nil, errors.New("ownerId 长度必须为 1..128")
	}
	if nowMS < 0 {
		return nil, errors.New("nowMs 必须是非负整数")
	}
	if leaseMS < 1 || leaseMS > 60*60_000 {
		return nil, errors.New("leaseMs 必须是 1..3600000 的正整数")
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit 必须是 1..500 的正整数")
	}
	outboxTable := r.table("account_circuit_outbox")
	if r.postgres {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		rows, err := tx.QueryContext(ctx, `
      SELECT event_id, projection_key, event_type, account_id, account_runtime_key,
        circuit_scope_key, transition_id, dispatch_revision
      FROM `+outboxTable+`
      WHERE (status = 'pending' AND available_at_ms <= ?)
         OR (status = 'processing' AND claim_until_ms <= ?)
      ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC
      LIMIT ? FOR UPDATE SKIP LOCKED`, nowMS, nowMS, limit)
		if err != nil {
			return nil, err
		}
		var candidates []outboxClaimRow
		for rows.Next() {
			row, err := scanOutboxClaimRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			candidates = append(candidates, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		claimed := make([]opsjobs.OutboxEvent, 0, len(candidates))
		for _, row := range candidates {
			claimToken := newClaimToken()
			result, err := tx.ExecContext(ctx, `
        UPDATE `+outboxTable+`
        SET status = 'processing', claim_token = ?, claimed_by = ?, claim_until_ms = ?,
            attempt_count = attempt_count + 1, updated_at_ms = ?
        WHERE event_id = ?
          AND ((status = 'pending' AND available_at_ms <= ?)
            OR (status = 'processing' AND claim_until_ms <= ?))`,
				claimToken, ownerID, nowMS+leaseMS, nowMS, row.eventID, nowMS, nowMS)
			if err != nil {
				return nil, err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if changed != 1 {
				continue
			}
			row.claimToken = sql.NullString{String: claimToken, Valid: true}
			claimed = append(claimed, mapOutboxEvent(row))
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return claimed, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
    SELECT event_id, projection_key, event_type, account_id, account_runtime_key,
      circuit_scope_key, transition_id, dispatch_revision
    FROM `+outboxTable+`
    WHERE (status = 'pending' AND available_at_ms <= ?)
       OR (status = 'processing' AND claim_until_ms <= ?)
    ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC
    LIMIT ?`, nowMS, nowMS, limit)
	if err != nil {
		return nil, err
	}
	var candidates []outboxClaimRow
	for rows.Next() {
		row, err := scanOutboxClaimRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	claimed := make([]opsjobs.OutboxEvent, 0, len(candidates))
	for _, row := range candidates {
		claimToken := newClaimToken()
		result, err := tx.ExecContext(ctx, `
      UPDATE `+outboxTable+`
      SET status = 'processing', claim_token = ?, claimed_by = ?, claim_until_ms = ?,
          attempt_count = attempt_count + 1, updated_at_ms = ?
      WHERE event_id = ?
        AND ((status = 'pending' AND available_at_ms <= ?)
          OR (status = 'processing' AND claim_until_ms <= ?))`,
			claimToken, ownerID, nowMS+leaseMS, nowMS, row.eventID, nowMS, nowMS)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			continue
		}
		row.claimToken = sql.NullString{String: claimToken, Valid: true}
		claimed = append(claimed, mapOutboxEvent(row))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func scanOutboxClaimRow(row interface{ Scan(...any) error }) (outboxClaimRow, error) {
	var scanned outboxClaimRow
	var scopeKey sql.NullString
	err := row.Scan(&scanned.eventID, &scanned.projectionKey, &scanned.eventType, &scanned.accountID,
		&scanned.accountRuntimeKey, &scopeKey, &scanned.transitionID, &scanned.dispatchRevision)
	if err != nil {
		return outboxClaimRow{}, err
	}
	scanned.circuitScopeKey = scopeKey
	return scanned, nil
}

func mapOutboxEvent(row outboxClaimRow) opsjobs.OutboxEvent {
	event := opsjobs.OutboxEvent{
		EventID:           row.eventID,
		EventType:         row.eventType,
		AccountRuntimeKey: row.accountRuntimeKey,
		TransitionID:      row.transitionID,
		DispatchRevision:  row.dispatchRevision,
		ProjectionKey:     row.projectionKey,
	}
	if row.circuitScopeKey.Valid && row.circuitScopeKey.String != "" {
		event.CircuitScopeKey = row.circuitScopeKey.String
	}
	if row.claimToken.Valid {
		event.ClaimToken = row.claimToken.String
	}
	return event
}

// Ack 对齐 acknowledgeAccountCircuitOutboxInClient：claim 围栏内标记
// dispatched，并回写投影 revision 水位（dispatch_revision → accounts、
// incident_changed → account_circuit_incidents）。
func (r *ControlPlaneRepo) Ack(ctx context.Context, event opsjobs.OutboxEvent, acknowledgedAtMS int64) (bool, error) {
	eventID := strings.TrimSpace(event.EventID)
	if eventID == "" || len(eventID) > 256 {
		return false, errors.New("eventId 长度必须为 1..256")
	}
	projectionKey := strings.TrimSpace(event.ProjectionKey)
	if projectionKey == "" || len(projectionKey) > 128 {
		return false, errors.New("projectionKey 长度必须为 1..128")
	}
	claimToken := strings.TrimSpace(event.ClaimToken)
	if claimToken == "" || len(claimToken) > 256 {
		return false, errors.New("claimToken 长度必须为 1..256")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	outboxTable := r.table("account_circuit_outbox")
	var (
		rowEventType     string
		rowAccountID     string
		rowScopeKey      sql.NullString
		rowIncidentID    sql.NullString
		rowDispatch      int64
		rowLedger        sql.NullInt64
		rowStatus        string
		rowClaimToken    sql.NullString
		rowProjectionKey string
	)
	selectQuery := `
    SELECT event_type, account_id, circuit_scope_key, incident_id, dispatch_revision,
      ledger_revision, status, claim_token, projection_key
    FROM ` + outboxTable + `
    WHERE event_id = ?`
	if r.postgres {
		selectQuery += " FOR UPDATE"
	}
	err = tx.QueryRowContext(ctx, selectQuery, eventID).Scan(
		&rowEventType, &rowAccountID, &rowScopeKey, &rowIncidentID, &rowDispatch,
		&rowLedger, &rowStatus, &rowClaimToken, &rowProjectionKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rowProjectionKey != projectionKey {
		return false, nil
	}
	if rowStatus == "dispatched" {
		return true, tx.Commit()
	}
	if rowStatus != "processing" || rowClaimToken.String != claimToken {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
    UPDATE `+outboxTable+`
    SET status = 'dispatched', claim_token = NULL, claimed_by = NULL, claim_until_ms = NULL,
        acknowledged_at_ms = ?, last_error_class = NULL, updated_at_ms = ?
    WHERE event_id = ? AND status = 'processing' AND claim_token = ? AND projection_key = ?`,
		acknowledgedAtMS, acknowledgedAtMS, eventID, claimToken, projectionKey)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, nil
	}
	if rowEventType == "dispatch_revision_changed" {
		if _, err := tx.ExecContext(ctx, `
      UPDATE `+r.table("accounts")+`
      SET circuit_projection_revision = CASE
        WHEN circuit_projection_revision < ? THEN ?
        ELSE circuit_projection_revision
      END
      WHERE id = ? AND dispatch_revision >= ?`,
			rowDispatch, rowDispatch, rowAccountID, rowDispatch); err != nil {
			return false, err
		}
	} else if rowEventType == "incident_changed" && rowScopeKey.Valid && rowScopeKey.String != "" &&
		rowIncidentID.Valid && rowIncidentID.String != "" && rowLedger.Valid {
		if _, err := tx.ExecContext(ctx, `
      UPDATE `+r.table("account_circuit_incidents")+`
      SET projected_ledger_revision = CASE
        WHEN projected_ledger_revision < ? THEN ?
        ELSE projected_ledger_revision
      END
      WHERE circuit_scope_key = ? AND incident_id = ? AND ledger_revision >= ?`,
			rowLedger.Int64, rowLedger.Int64, rowScopeKey.String, rowIncidentID.String, rowLedger.Int64); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// ReleaseForReplay 对齐 releaseAccountCircuitOutboxForReplayInClient。
func (r *ControlPlaneRepo) ReleaseForReplay(ctx context.Context, event opsjobs.OutboxEvent, errorClass string, nowMS int64, retryDelayMS int64) error {
	eventID := strings.TrimSpace(event.EventID)
	if eventID == "" || len(eventID) > 256 {
		return errors.New("eventId 长度必须为 1..256")
	}
	claimToken := strings.TrimSpace(event.ClaimToken)
	if claimToken == "" || len(claimToken) > 256 {
		return errors.New("claimToken 长度必须为 1..256")
	}
	errorClass = strings.TrimSpace(errorClass)
	if errorClass == "" || len(errorClass) > 64 {
		return errors.New("errorClass 长度必须为 1..64")
	}
	if nowMS < 0 || retryDelayMS < 0 || retryDelayMS > 24*60*60_000 {
		return errors.New("retryDelayMs 必须是 0..86400000 的非负整数")
	}
	result, err := r.db.ExecContext(ctx, `
    UPDATE `+r.table("account_circuit_outbox")+`
    SET status = 'pending', available_at_ms = ?, claim_token = NULL, claimed_by = NULL,
        claim_until_ms = NULL, last_error_class = ?, updated_at_ms = ?
    WHERE event_id = ? AND status = 'processing' AND claim_token = ?`,
		nowMS+retryDelayMS, errorClass, nowMS, eventID, claimToken)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("账户 circuit outbox 释放重放未命中 claim")
	}
	return nil
}

// ---- ReconcileCursorStore（PG/SQLite 双模持久化游标）----

// ReconcileCursorTable 是游标持久化表（jobs 专属辅助表，幂等建表）。
const ReconcileCursorTable = "account_circuit_reconcile_cursors"

// NewReconcileCursorStore 构建 opsjobs.ReconcileCursorStore 的 DB 实现
// （重启后 reconcile 从上次游标续跑；Node 为内存实现，本实现是向前兼容的
// 加法扩展：同键语义、首次为空即从头回放）。
func NewReconcileCursorStore(config ControlPlaneConfig) (opsjobs.ReconcileCursorStore, error) {
	repo, err := NewControlPlaneRepo(config)
	if err != nil {
		return nil, err
	}
	return (*reconcileCursorStore)(repo), nil
}

type reconcileCursorStore ControlPlaneRepo

// EnsureCursorSchema 幂等创建 reconcile 游标表。
func (r *ControlPlaneRepo) EnsureCursorSchema(ctx context.Context) error {
	table := r.table(ReconcileCursorTable)
	_, err := r.db.ExecContext(ctx, `
    CREATE TABLE IF NOT EXISTS `+table+` (
      cursor_name TEXT PRIMARY KEY,
      updated_at_ms BIGINT NOT NULL,
      circuit_scope_key TEXT NOT NULL,
      saved_at_ms BIGINT NOT NULL
    )`)
	if err != nil {
		return fmt.Errorf("初始化账户电路 reconcile 游标表失败: %w", err)
	}
	return nil
}

const reconcileCursorName = "incident_ledger_reconcile"

func (s *reconcileCursorStore) Load(ctx context.Context) (*opsjobs.IncidentCursor, error) {
	var (
		updatedAtMS int64
		scopeKey    string
	)
	repo := (*ControlPlaneRepo)(s)
	err := repo.db.QueryRowContext(ctx, `
    SELECT updated_at_ms, circuit_scope_key FROM `+repo.table(ReconcileCursorTable)+`
    WHERE cursor_name = ?`, reconcileCursorName).Scan(&updatedAtMS, &scopeKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &opsjobs.IncidentCursor{UpdatedAtMS: updatedAtMS, CircuitScopeKey: scopeKey}, nil
}

func (s *reconcileCursorStore) Save(ctx context.Context, cursor opsjobs.IncidentCursor) error {
	repo := (*ControlPlaneRepo)(s)
	table := repo.table(ReconcileCursorTable)
	_, err := repo.db.ExecContext(ctx, `
    INSERT INTO `+table+` (cursor_name, updated_at_ms, circuit_scope_key, saved_at_ms)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(cursor_name) DO UPDATE SET
      updated_at_ms = excluded.updated_at_ms,
      circuit_scope_key = excluded.circuit_scope_key,
      saved_at_ms = excluded.saved_at_ms`,
		reconcileCursorName, cursor.UpdatedAtMS, cursor.CircuitScopeKey, s.now().UnixMilli())
	return err
}

func newClaimToken() string { return newRandomUUID() }
