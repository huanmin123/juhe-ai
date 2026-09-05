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
// 本适配器只读 ledger（写侧仍归 Node/gateway 的 CAS 写路径），outbox ack
// 中对 circuit_projection_revision / projected_ledger_revision 的回写与
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

// ---- opsjobs.ControlPlaneOutbox ----

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
