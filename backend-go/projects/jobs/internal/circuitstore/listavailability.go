package circuitstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// 账户列表可用性投影读模型仓储：移植 Node
// storage/account-list-availability-projection.repository.ts 的 worker 维护面
// （17 个 opsjobs.ListAvailabilityRepo port 方法）。SQL 与 Node 逐字段一致；
// 双模方言差异与 Node 相同：postgres 使用数据库时钟 + FOR UPDATE SKIP LOCKED
// 批量 claim，SQLite 使用进程时钟两段更新；业务表位于 juhe_business schema。
//
// 网关读路径（listAccountListAvailabilityProjectionPage）与 LoadItems 物化
// 载荷归网关域，本包不迁移（见组合根登记说明）。

// ListAvailabilityConfig 组装投影仓储。
type ListAvailabilityConfig struct {
	DB       *sql.DB
	Postgres bool
	Now      func() time.Time
}

// ListAvailabilityRepo 实现 opsjobs.ListAvailabilityRepo 与
// opsjobs.OverlayReconciler 的 PG 持久化半边。
type ListAvailabilityRepo struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
}

// NewListAvailabilityRepo 构建仓储；输入校验失败返回错误。
func NewListAvailabilityRepo(config ListAvailabilityConfig) (*ListAvailabilityRepo, error) {
	if config.DB == nil {
		return nil, errors.New("circuitstore 列表投影缺少业务库句柄")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ListAvailabilityRepo{db: config.DB, postgres: config.Postgres, now: now}, nil
}

func (r *ListAvailabilityRepo) table(name string) string {
	if r.postgres {
		return "juhe_business." + name
	}
	return name
}

const (
	maximumDirtyClaimLimit   = 500
	maximumDirtyLeaseMS      = 60 * 60_000
	maximumDirtyRetryDelayMS = 24 * 60 * 60_000
)

func optionalUpdatedAt(updatedAt string) (string, error) {
	normalized := strings.TrimSpace(updatedAt)
	if normalized == "" {
		return "", nil
	}
	if len(normalized) > 64 {
		return "", errors.New("updatedAt 长度必须为 0..64")
	}
	return normalized, nil
}

func (r *ListAvailabilityRepo) requireUpdatedAt(updatedAt string) (string, error) {
	normalized, err := optionalUpdatedAt(updatedAt)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		normalized = r.now().UTC().Format(time.RFC3339Nano)
	}
	return normalized, nil
}

// ---- runtime dependency 健康（fail-closed 状态机）----

// EnsureRuntimeDependency 对齐 ensure...RuntimeDependencyInClient。
func (r *ListAvailabilityRepo) EnsureRuntimeDependency(ctx context.Context, updatedAt string) error {
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
    INSERT INTO `+r.table("account_list_availability_projection_dependency_health")+` (dependency_name, state, generation, reason, updated_at)
    VALUES ('runtime_state', 'recovering', 1, 'initial_projection_bootstrap', ?)
    ON CONFLICT(dependency_name) DO NOTHING`, updated)
	return err
}

// TouchRuntimeDependency 对齐 touch...RuntimeDependencyInClient（仅 healthy 时刷新）。
func (r *ListAvailabilityRepo) TouchRuntimeDependency(ctx context.Context, updatedAt string) error {
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
    UPDATE `+r.table("account_list_availability_projection_dependency_health")+`
    SET updated_at = ?
    WHERE dependency_name = 'runtime_state' AND state = 'healthy'`, updated)
	if err != nil {
		return err
	}
	_, err = result.RowsAffected()
	return err
}

// MarkRuntimeDependencyUnavailable 对齐 mark...RuntimeDependencyUnavailableInClient。
func (r *ListAvailabilityRepo) MarkRuntimeDependencyUnavailable(ctx context.Context, reason, updatedAt string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 256 {
		return errors.New("reason 长度必须为 1..256")
	}
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return err
	}
	health := r.table("account_list_availability_projection_dependency_health")
	_, err = r.db.ExecContext(ctx, `
    INSERT INTO `+health+` (dependency_name, state, generation, reason, updated_at)
    VALUES ('runtime_state', 'unavailable', 1, ?, ?)
    ON CONFLICT(dependency_name) DO UPDATE SET
      state = 'unavailable',
      generation = CASE
        WHEN `+health+`.state = 'unavailable' THEN `+health+`.generation
        ELSE `+health+`.generation + 1
      END,
      reason = excluded.reason,
      updated_at = excluded.updated_at`, reason, updated)
	return err
}

// BeginRuntimeDependencyRecovery 对齐 begin...RuntimeDependencyRecoveryInClient。
func (r *ListAvailabilityRepo) BeginRuntimeDependencyRecovery(ctx context.Context, updatedAt string) (bool, error) {
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return false, err
	}
	health := r.table("account_list_availability_projection_dependency_health")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	selectQuery := `SELECT state FROM ` + health + ` WHERE dependency_name = 'runtime_state'`
	if r.postgres {
		selectQuery += " FOR UPDATE"
	}
	err = tx.QueryRowContext(ctx, selectQuery).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `
      INSERT INTO `+health+` (dependency_name, state, generation, reason, updated_at)
      VALUES ('runtime_state', 'recovering', 1, 'initial_projection_bootstrap', ?)`, updated); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if state != "unavailable" {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
    UPDATE `+health+`
    SET state = 'recovering', reason = 'runtime_state_recovery_replay', updated_at = ?
    WHERE dependency_name = 'runtime_state'`, updated); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// CompleteRuntimeDependencyRecovery 对齐 complete...RuntimeDependencyRecoveryInClient。
func (r *ListAvailabilityRepo) CompleteRuntimeDependencyRecovery(ctx context.Context, updatedAt string) (bool, error) {
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `
    UPDATE `+r.table("account_list_availability_projection_dependency_health")+`
    SET state = 'healthy', reason = NULL, updated_at = ?
    WHERE dependency_name = 'runtime_state'
      AND state = 'recovering'
      AND NOT EXISTS (SELECT 1 FROM `+r.table("account_list_availability_dirty")+`)`, updated)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// ---- dirty 入队 ----

const markDirtySQL = `
  INSERT INTO %dirty% (
    account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms,
    claim_token, claimed_by, claim_until_ms, attempt_count,
    created_at_ms, updated_at_ms
  ) SELECT accounts.id, accounts.system_account_id, COALESCE((
    SELECT MAX(source_generation)
    FROM %projections%
    WHERE account_id = ?
  ), 0) + 1, 0, ?, ?, NULL, NULL, NULL, 0, ?, ?
  FROM %accounts% accounts
  WHERE accounts.id = ? AND accounts.deleted_at IS NULL
  ON CONFLICT(account_id) DO UPDATE SET
    viewer_system_account_id = excluded.viewer_system_account_id,
    generation = %dirty%.generation + 1,
    reason = excluded.reason,
    available_at_ms = CASE
      WHEN %dirty%.available_at_ms < excluded.available_at_ms THEN %dirty%.available_at_ms
      ELSE excluded.available_at_ms
    END,
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    updated_at_ms = excluded.updated_at_ms`

func (r *ListAvailabilityRepo) markDirtyTx(ctx context.Context, tx *sql.Tx, accountID, reason string, availableAtMS, nowMS int64) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || len(accountID) > 256 {
		return errors.New("accountId 长度必须为 1..256")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 128 {
		return errors.New("reason 长度必须为 1..128")
	}
	query := strings.NewReplacer(
		"%dirty%", r.table("account_list_availability_dirty"),
		"%projections%", r.table("account_list_availability_projections"),
		"%accounts%", r.table("accounts"),
	).Replace(markDirtySQL)
	if _, err := tx.ExecContext(ctx, query, accountID, reason, availableAtMS, nowMS, nowMS, accountID); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+r.table("account_list_availability_dirty")+` WHERE account_id = ?`, accountID).Scan(&count); err != nil {
		return err
	}
	if count < 1 {
		return fmt.Errorf("账户列表投影脏标记 %s 未写入，账户可能已删除", accountID)
	}
	return nil
}

// EnqueueMissing 对齐 enqueueMissing...InClient（worker 专属 bootstrap 扫描）。
func (r *ListAvailabilityRepo) EnqueueMissing(ctx context.Context, limit int, nowMS int64) (int, error) {
	if limit < 1 || limit > maximumDirtyClaimLimit {
		return 0, fmt.Errorf("limit 必须是 1..%d 的正整数", maximumDirtyClaimLimit)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
    SELECT accounts.id
    FROM `+r.table("accounts")+` accounts
    LEFT JOIN `+r.table("resource_authorizations")+` authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN `+r.table("account_list_availability_projections")+` projections
      ON projections.viewer_system_account_id = accounts.system_account_id
     AND projections.account_id = accounts.id
    LEFT JOIN `+r.table("account_list_availability_dirty")+` dirty_accounts
      ON dirty_accounts.account_id = accounts.id
    WHERE accounts.deleted_at IS NULL
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
      AND projections.account_id IS NULL
      AND dirty_accounts.account_id IS NULL
    ORDER BY accounts.created_at ASC, accounts.id ASC
    LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var accountIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, accountID := range accountIDs {
		if err := r.markDirtyTx(ctx, tx, accountID, "projection_missing", nowMS, nowMS); err != nil {
			return 0, err
		}
	}
	return len(accountIDs), tx.Commit()
}

// EnqueueDue 对齐 enqueueDue...InClient（到期转移转 dirty）。
func (r *ListAvailabilityRepo) EnqueueDue(ctx context.Context, limit int, nowMS int64) (int, error) {
	if limit < 1 || limit > maximumDirtyClaimLimit {
		return 0, fmt.Errorf("limit 必须是 1..%d 的正整数", maximumDirtyClaimLimit)
	}
	now := time.UnixMilli(nowMS).UTC().Format(time.RFC3339Nano)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
    SELECT projections.account_id
    FROM `+r.table("account_list_availability_projections")+` projections
    LEFT JOIN `+r.table("account_list_availability_dirty")+` dirty_accounts
      ON dirty_accounts.account_id = projections.account_id
    WHERE projections.next_transition_at IS NOT NULL
      AND projections.next_transition_at <= ?
      AND dirty_accounts.account_id IS NULL
    ORDER BY projections.next_transition_at ASC, projections.account_id ASC
    LIMIT ?`, instantParam(r.postgres, now, r.now), limit)
	if err != nil {
		return 0, err
	}
	var accountIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, accountID := range accountIDs {
		if err := r.markDirtyTx(ctx, tx, accountID, "projection_due_transition", nowMS, nowMS); err != nil {
			return 0, err
		}
	}
	return len(accountIDs), tx.Commit()
}

// EnqueueAllForRuntimeRecovery 对齐 enqueueAll...ForRuntimeRecoveryInClient。
func (r *ListAvailabilityRepo) EnqueueAllForRuntimeRecovery(ctx context.Context, nowMS int64) (int, error) {
	dirty := r.table("account_list_availability_dirty")
	projections := r.table("account_list_availability_projections")
	accounts := r.table("accounts")
	authorizations := r.table("resource_authorizations")
	viewerHealth := r.table("account_list_availability_projection_viewer_health")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
    INSERT INTO `+dirty+` (
      account_id, viewer_system_account_id, generation, applied_generation, reason,
      available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count,
      created_at_ms, updated_at_ms
    )
    SELECT accounts.id, accounts.system_account_id,
      COALESCE(projections.source_generation, 0) + 1,
      0, 'runtime_dependency_recovery', ?, NULL, NULL, NULL, 0, ?, ?
    FROM `+accounts+` accounts
    LEFT JOIN `+authorizations+` authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN `+projections+` projections
      ON projections.viewer_system_account_id = accounts.system_account_id
     AND projections.account_id = accounts.id
    WHERE accounts.deleted_at IS NULL
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    ON CONFLICT(account_id) DO UPDATE SET
      viewer_system_account_id = excluded.viewer_system_account_id,
      generation = `+dirty+`.generation + 1,
      reason = excluded.reason,
      available_at_ms = CASE
        WHEN `+dirty+`.available_at_ms < excluded.available_at_ms THEN `+dirty+`.available_at_ms
        ELSE excluded.available_at_ms
      END,
      claim_token = NULL,
      claimed_by = NULL,
      claim_until_ms = NULL,
      updated_at_ms = excluded.updated_at_ms`, nowMS, nowMS, nowMS)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
    UPDATE `+viewerHealth+`
    SET is_current = 0, updated_at = ?
    WHERE viewer_system_account_id IN (
      SELECT DISTINCT accounts.system_account_id
      FROM `+accounts+` accounts
      LEFT JOIN `+authorizations+` authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      WHERE accounts.deleted_at IS NULL
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
    )`, r.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(changed), nil
}

// ---- viewer health ----

// EnsureViewerHealth 对齐 ensure...ViewerHealthInClient（缺行补 0/非 current）。
func (r *ListAvailabilityRepo) EnsureViewerHealth(ctx context.Context, limit int, updatedAt string) (int, error) {
	if limit < 1 || limit > maximumDirtyClaimLimit {
		return 0, fmt.Errorf("limit 必须是 1..%d 的正整数", maximumDirtyClaimLimit)
	}
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return 0, err
	}
	result, err := r.db.ExecContext(ctx, `
    INSERT INTO `+r.table("account_list_availability_projection_viewer_health")+` (
      viewer_system_account_id, projection_count, oldest_projected_at,
      next_transition_at, is_current, updated_at
    )
    SELECT system_accounts.id, 0, NULL, NULL, 0, ?
    FROM `+r.table("system_accounts")+` system_accounts
    LEFT JOIN `+r.table("account_list_availability_projection_viewer_health")+` health
      ON health.viewer_system_account_id = system_accounts.id
    WHERE health.viewer_system_account_id IS NULL
    ORDER BY system_accounts.id ASC
    LIMIT ?
    ON CONFLICT(viewer_system_account_id) DO NOTHING`, updated, limit)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(changed), nil
}

// ListViewerHealthRefreshCandidates 对齐 list...ViewerHealthRefreshCandidatesInClient。
func (r *ListAvailabilityRepo) ListViewerHealthRefreshCandidates(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > maximumDirtyClaimLimit {
		return nil, fmt.Errorf("limit 必须是 1..%d 的正整数", maximumDirtyClaimLimit)
	}
	rows, err := r.db.QueryContext(ctx, `
    SELECT health.viewer_system_account_id
    FROM `+r.table("account_list_availability_projection_viewer_health")+` health
    WHERE health.is_current = 0
      AND NOT EXISTS (
        SELECT 1
        FROM `+r.table("account_list_availability_dirty")+` dirty_accounts
        WHERE dirty_accounts.viewer_system_account_id = health.viewer_system_account_id
      )
    ORDER BY health.updated_at ASC, health.viewer_system_account_id ASC
    LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var viewers []string
	for rows.Next() {
		var viewer string
		if err := rows.Scan(&viewer); err != nil {
			return nil, err
		}
		viewer = strings.TrimSpace(viewer)
		if viewer == "" || len(viewer) > 256 {
			return nil, errors.New("viewer_system_account_id 长度必须为 1..256")
		}
		viewers = append(viewers, viewer)
	}
	return viewers, rows.Err()
}

// RefreshViewerHealth 对齐 refresh...ViewerHealthInClient（O(1) 水位重算）。
func (r *ListAvailabilityRepo) RefreshViewerHealth(ctx context.Context, viewerSystemAccountID, updatedAt string) error {
	viewer := strings.TrimSpace(viewerSystemAccountID)
	if viewer == "" || len(viewer) > 256 {
		return errors.New("viewerSystemAccountId 长度必须为 1..256")
	}
	updated, err := r.requireUpdatedAt(updatedAt)
	if err != nil {
		return err
	}
	projections := r.table("account_list_availability_projections")
	viewerHealth := r.table("account_list_availability_projection_viewer_health")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		projectionCount   int64
		oldestProjectedAt sql.NullString
		nextTransitionAt  sql.NullString
	)
	if err := tx.QueryRowContext(ctx, `
    SELECT COUNT(*), MIN(projected_at), MIN(next_transition_at)
    FROM `+projections+`
    WHERE viewer_system_account_id = ?`, viewer).Scan(&projectionCount, &oldestProjectedAt, &nextTransitionAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
    INSERT INTO `+viewerHealth+` (
      viewer_system_account_id, projection_count, oldest_projected_at,
      next_transition_at, is_current, updated_at
    ) VALUES (?, ?, ?, ?, 1, ?)
    ON CONFLICT(viewer_system_account_id) DO UPDATE SET
      projection_count = excluded.projection_count,
      oldest_projected_at = excluded.oldest_projected_at,
      next_transition_at = excluded.next_transition_at,
      is_current = 1,
      updated_at = excluded.updated_at`,
		viewer, projectionCount, nullStringParam(oldestProjectedAt), nullStringParam(nextTransitionAt), updated); err != nil {
		return err
	}
	return tx.Commit()
}

func nullStringParam(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

// ---- claim / apply / release ----

// ClaimDirty 对齐 claimAccountListAvailabilityDirtyInClient。
func (r *ListAvailabilityRepo) ClaimDirty(ctx context.Context, ownerID string, limit int, leaseMS, nowMS int64) ([]opsjobs.DirtyClaim, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || len(ownerID) > 128 {
		return nil, errors.New("ownerId 长度必须为 1..128")
	}
	if limit < 1 || limit > maximumDirtyClaimLimit {
		return nil, fmt.Errorf("limit 必须是 1..%d 的正整数", maximumDirtyClaimLimit)
	}
	if leaseMS < 1 || leaseMS > maximumDirtyLeaseMS {
		return nil, fmt.Errorf("leaseMs 必须是 1..%d 的正整数", maximumDirtyLeaseMS)
	}
	if nowMS < 0 {
		return nil, errors.New("nowMs 必须是非负整数")
	}
	dirty := r.table("account_list_availability_dirty")
	if r.postgres {
		// Node PG 分支：数据库时钟 + 单条 CTE 批量 claim。
		claimToken := newRandomUUID()
		query := `
      WITH candidates AS (
        SELECT account_id
        FROM ` + dirty + `
        WHERE available_at_ms <= FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint
          AND (claim_until_ms IS NULL OR claim_until_ms <= FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint)
        ORDER BY available_at_ms ASC, created_at_ms ASC, account_id ASC
        LIMIT ?
        FOR UPDATE SKIP LOCKED
      )
      UPDATE ` + dirty + ` dirty_accounts
      SET claim_token = ?,
          claimed_by = ?,
          claim_until_ms = FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint + ?,
          attempt_count = dirty_accounts.attempt_count + 1,
          updated_at_ms = FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint
      FROM candidates
      WHERE dirty_accounts.account_id = candidates.account_id
      RETURNING dirty_accounts.account_id, dirty_accounts.viewer_system_account_id,
        dirty_accounts.generation, dirty_accounts.attempt_count`
		rows, err := r.db.QueryContext(ctx, query, limit, claimToken, ownerID, leaseMS)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		claims := []opsjobs.DirtyClaim{}
		for rows.Next() {
			claim := opsjobs.DirtyClaim{ClaimToken: claimToken}
			if err := rows.Scan(&claim.AccountID, &claim.ViewerSystemAccountID, &claim.Generation, &claim.AttemptCount); err != nil {
				return nil, err
			}
			claims = append(claims, claim)
		}
		return claims, rows.Err()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
    SELECT account_id, viewer_system_account_id, generation, attempt_count
    FROM `+dirty+`
    WHERE available_at_ms <= ?
      AND (claim_until_ms IS NULL OR claim_until_ms <= ?)
    ORDER BY available_at_ms ASC, created_at_ms ASC, account_id ASC
    LIMIT ?`, nowMS, nowMS, limit)
	if err != nil {
		return nil, err
	}
	var candidates []opsjobs.DirtyClaim
	for rows.Next() {
		var claim opsjobs.DirtyClaim
		if err := rows.Scan(&claim.AccountID, &claim.ViewerSystemAccountID, &claim.Generation, &claim.AttemptCount); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, claim)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	claims := []opsjobs.DirtyClaim{}
	for _, claim := range candidates {
		claimToken := newRandomUUID()
		claimUntil := nowMS + leaseMS
		result, err := tx.ExecContext(ctx, `
      UPDATE `+dirty+`
      SET claim_token = ?, claimed_by = ?, claim_until_ms = ?,
          attempt_count = attempt_count + 1, updated_at_ms = ?
      WHERE account_id = ? AND generation = ?
        AND available_at_ms <= ?
        AND (claim_until_ms IS NULL OR claim_until_ms <= ?)`,
			claimToken, ownerID, claimUntil, nowMS, claim.AccountID, claim.Generation, nowMS, nowMS)
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
		claim.ClaimToken = claimToken
		claim.AttemptCount++
		claims = append(claims, claim)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

// ---- scope / search terms ----

// ListScopes 对齐 listAccountListAvailabilityProjectionScopesInClient。
func (r *ListAvailabilityRepo) ListScopes(ctx context.Context, accountIDs []string) ([]opsjobs.ProjectionScope, error) {
	ids, err := normalizedIDList(accountIDs)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []opsjobs.ProjectionScope{}, nil
	}
	placeholders := placeholdersFor(len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
    SELECT accounts.id, accounts.system_account_id, accounts.created_at
    FROM `+r.table("accounts")+` accounts
    LEFT JOIN `+r.table("resource_authorizations")+` authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    WHERE accounts.id IN (`+placeholders+`)
      AND accounts.deleted_at IS NULL
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    ORDER BY accounts.system_account_id ASC, accounts.id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := []opsjobs.ProjectionScope{}
	for rows.Next() {
		var (
			id        string
			viewer    string
			createdAt sql.NullString
		)
		if err := rows.Scan(&id, &viewer, &createdAt); err != nil {
			return nil, err
		}
		scope := opsjobs.ProjectionScope{AccountID: id, ViewerSystemAccountID: viewer}
		if createdAt.Valid && createdAt.String != "" {
			value := createdAt.String
			scope.CreatedAt = &value
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

// LoadSearchTerms 对齐 loadAccountListAvailabilityProjectionSearchTermsInClient。
func (r *ListAvailabilityRepo) LoadSearchTerms(ctx context.Context, accountIDs []string) (map[string][]string, error) {
	ids, err := normalizedIDList(accountIDs)
	if err != nil {
		return nil, err
	}
	output := map[string][]string{}
	if len(ids) == 0 {
		return output, nil
	}
	placeholders := placeholdersFor(len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
    SELECT search.account_id, search.term
    FROM `+r.table("account_name_search_terms")+` search
    INNER JOIN `+r.table("account_name_search_documents")+` documents
      ON documents.account_id = search.account_id
    WHERE search.account_id IN (`+placeholders+`)
    ORDER BY search.account_id ASC, search.term ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			accountID string
			term      string
		)
		if err := rows.Scan(&accountID, &term); err != nil {
			return nil, err
		}
		output[accountID] = append(output[accountID], term)
	}
	return output, rows.Err()
}

// ---- apply / delete / release ----

type normalizedProjection struct {
	viewerSystemAccountID     string
	accountID                 string
	concurrencyAccountID      string
	currentConcurrency        int64
	sourceAccountID           *string
	authorizationID           *string
	effectiveStatus           string
	schedulableBucket         string
	providerCode              string
	providerProtocolProfileID string
	accountType               string
	boundGroupID              *string
	nameSortKey               string
	prioritySortKey           int64
	superPrioritySortKey      int64
	fallbackSortKey           int64
	concurrencySortKey        int64
	accountExpiresAtSortKey   *string
	lastUsedAtSortKey         *string
	createdAtSortKey          string
	payload                   []byte
	tagIDs                    []string
	searchTerms               []string
	searchIndexComplete       bool
	sourceGeneration          int64
	nextTransitionAt          *string
	projectedAt               string
}

func boolSortKey(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func textPtr(value string) *string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

// normalizeProjectionWrite 对齐 Node normalizeProjectionWrite：把 opsjobs
// ProjectionItem 物化载荷归一为列值（状态、桶、排序键、payload、tags、terms）。
func normalizeProjectionWrite(write opsjobs.ProjectionWrite) (normalizedProjection, error) {
	viewer := strings.TrimSpace(write.Scope.ViewerSystemAccountID)
	if viewer == "" || len(viewer) > 256 {
		return normalizedProjection{}, errors.New("viewerSystemAccountId 长度必须为 1..256")
	}
	accountID := strings.TrimSpace(write.Item.AccountID)
	if accountID == "" || len(accountID) > 256 {
		return normalizedProjection{}, errors.New("accountId 长度必须为 1..256")
	}
	concurrencyAccountID := strings.TrimSpace(write.Item.SourceAccountID)
	if concurrencyAccountID == "" {
		concurrencyAccountID = accountID
	}
	if len(concurrencyAccountID) > 256 {
		return normalizedProjection{}, errors.New("concurrencyAccountId 长度必须为 1..256")
	}
	providerCode := strings.TrimSpace(write.Item.ProviderCode)
	if providerCode == "" || len(providerCode) > 128 {
		return normalizedProjection{}, errors.New("providerCode 长度必须为 1..128")
	}
	profileID := strings.TrimSpace(write.Item.ProviderProtocolProfileID)
	if profileID == "" || len(profileID) > 256 {
		return normalizedProjection{}, errors.New("providerProtocolProfileId 长度必须为 1..256")
	}
	accountType := strings.TrimSpace(write.Item.AccountType)
	if accountType == "" || len(accountType) > 64 {
		return normalizedProjection{}, errors.New("accountType 长度必须为 1..64")
	}
	nameSortKey := normalizeAccountNameSearchText(write.Item.Name)
	if nameSortKey == "" {
		return normalizedProjection{}, errors.New("nameSortKey 不能为空")
	}
	createdAtSortKey := ""
	if write.Scope.CreatedAt != nil {
		createdAtSortKey = strings.TrimSpace(*write.Scope.CreatedAt)
	}
	if createdAtSortKey == "" || len(createdAtSortKey) > 64 {
		return normalizedProjection{}, errors.New("createdAtSortKey 长度必须为 1..64")
	}
	projectedAt := write.Now.UTC().Format(time.RFC3339Nano)
	// effectiveAvailable 对齐 Node schedulableBucket(item, effectiveStatus)
	// 的 item.effectiveAvailability.available：loader 显式给出时优先，
	// payload 适配键次之，缺省视为可用（port 既有默认）。
	effectiveAvailable := true
	if value, ok := write.Item.Payload["effectiveAvailable"].(bool); ok {
		effectiveAvailable = value
	}
	if write.Item.EffectiveAvailable != nil {
		effectiveAvailable = *write.Item.EffectiveAvailable
	}
	payloadBytes, err := json.Marshal(write.Item.Payload)
	if err != nil {
		return normalizedProjection{}, fmt.Errorf("账户列表投影 payload 序列化失败: %w", err)
	}
	normalized := normalizedProjection{
		viewerSystemAccountID:     viewer,
		accountID:                 accountID,
		concurrencyAccountID:      concurrencyAccountID,
		currentConcurrency:        int64(write.Item.CurrentConcurrency),
		sourceAccountID:           textPtr(write.Item.SourceAccountID),
		authorizationID:           textPtr(write.Item.AuthorizationID),
		effectiveStatus:           write.Item.EffectiveStatus,
		schedulableBucket:         opsjobs.SchedulableBucket(write.Item.EffectiveStatus, effectiveAvailable),
		providerCode:              providerCode,
		providerProtocolProfileID: profileID,
		accountType:               accountType,
		boundGroupID:              textPtr(write.Item.BoundGroupID),
		nameSortKey:               nameSortKey,
		prioritySortKey:           int64(write.Item.Priority),
		superPrioritySortKey:      boolSortKey(write.Item.SuperPriorityEnabled),
		fallbackSortKey:           boolSortKey(write.Item.FallbackEnabled),
		concurrencySortKey:        int64(write.Item.ConcurrencyLimit),
		accountExpiresAtSortKey:   textPtr(write.Item.AccountExpiresAt),
		lastUsedAtSortKey:         textPtr(write.Item.LastUsedAt),
		createdAtSortKey:          createdAtSortKey,
		payload:                   payloadBytes,
		sourceGeneration:          write.Claim.Generation,
		projectedAt:               projectedAt,
	}
	if normalized.currentConcurrency < 0 {
		return normalizedProjection{}, errors.New("currentConcurrency 必须是非负整数")
	}
	if write.Claim.Generation < 1 {
		return normalizedProjection{}, errors.New("sourceGeneration 必须大于 0")
	}
	normalized.nextTransitionAt = textPtr(nextTransitionAtOrEmpty(write.Item.NextTransitionCandidates, write.Now))
	tagSeen := map[string]struct{}{}
	for _, tagID := range write.Item.TagIDs {
		normalizedTag := strings.TrimSpace(tagID)
		if normalizedTag == "" || len(normalizedTag) > 256 {
			return normalizedProjection{}, errors.New("tagId 长度必须为 1..256")
		}
		if _, exists := tagSeen[normalizedTag]; exists {
			continue
		}
		tagSeen[normalizedTag] = struct{}{}
		normalized.tagIDs = append(normalized.tagIDs, normalizedTag)
	}
	termSeen := map[string]struct{}{}
	for _, term := range write.SearchTerms {
		if term == "" || len(term) > 256 {
			return normalizedProjection{}, errors.New("searchTerm 必须是 1-256 位文本")
		}
		if _, exists := termSeen[term]; exists {
			continue
		}
		termSeen[term] = struct{}{}
		normalized.searchTerms = append(normalized.searchTerms, term)
	}
	normalized.searchIndexComplete = len(normalized.searchTerms) > 0
	if write.Item.EffectiveStatus == "" {
		return normalizedProjection{}, fmt.Errorf("账户 %s 无法归类为唯一投影状态", accountID)
	}
	return normalized, nil
}

// upsertProjectionTx 对齐 upsertAccountListAvailabilityProjectionInTransaction
// （payload/index/overlay/tags/terms/viewer 全部在同一 fenced 事务内）。
func (r *ListAvailabilityRepo) upsertProjectionTx(ctx context.Context, tx *sql.Tx, value normalizedProjection) (bool, error) {
	projections := r.table("account_list_availability_projections")
	result, err := tx.ExecContext(ctx, `
    INSERT INTO `+projections+` (
      viewer_system_account_id, account_id, source_account_id, authorization_id,
      effective_status, schedulable_bucket, provider_code, provider_protocol_profile_id,
      account_type, bound_group_id, name_sort_key, priority_sort_key,
      super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
      account_expires_at_sort_key, last_used_at_sort_key, created_at_sort_key,
      payload_json, source_generation, next_transition_at, projected_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(viewer_system_account_id, account_id) DO UPDATE SET
      source_account_id = excluded.source_account_id,
      authorization_id = excluded.authorization_id,
      effective_status = excluded.effective_status,
      schedulable_bucket = excluded.schedulable_bucket,
      provider_code = excluded.provider_code,
      provider_protocol_profile_id = excluded.provider_protocol_profile_id,
      account_type = excluded.account_type,
      bound_group_id = excluded.bound_group_id,
      name_sort_key = excluded.name_sort_key,
      priority_sort_key = excluded.priority_sort_key,
      super_priority_sort_key = excluded.super_priority_sort_key,
      fallback_sort_key = excluded.fallback_sort_key,
      concurrency_sort_key = excluded.concurrency_sort_key,
      account_expires_at_sort_key = excluded.account_expires_at_sort_key,
      last_used_at_sort_key = excluded.last_used_at_sort_key,
      created_at_sort_key = excluded.created_at_sort_key,
      payload_json = excluded.payload_json,
      source_generation = excluded.source_generation,
      next_transition_at = excluded.next_transition_at,
      projected_at = excluded.projected_at
    WHERE `+projections+`.source_generation <= excluded.source_generation`,
		value.viewerSystemAccountID, value.accountID, value.sourceAccountID, value.authorizationID,
		value.effectiveStatus, value.schedulableBucket, value.providerCode, value.providerProtocolProfileID,
		value.accountType, value.boundGroupID, value.nameSortKey, value.prioritySortKey,
		value.superPrioritySortKey, value.fallbackSortKey, value.concurrencySortKey,
		value.accountExpiresAtSortKey, value.lastUsedAtSortKey, value.createdAtSortKey,
		string(value.payload), value.sourceGeneration, value.nextTransitionAt, value.projectedAt)
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
	accessType := "owner"
	if payloadMap := map[string]any{}; json.Unmarshal(value.payload, &payloadMap) == nil {
		if text, ok := payloadMap["accessType"].(string); ok && strings.TrimSpace(text) != "" {
			accessType = text
		}
	}
	quotaExceeded := false
	if payloadMap := map[string]any{}; json.Unmarshal(value.payload, &payloadMap) == nil {
		if flag, ok := payloadMap["authorizationQuotaExceeded"].(bool); ok {
			quotaExceeded = flag
		}
	}
	if _, err := tx.ExecContext(ctx, `
    INSERT INTO `+r.table("account_list_availability_projection_index")+` (
      viewer_system_account_id, account_id, effective_status, schedulable_bucket,
      provider_code, provider_protocol_profile_id, account_type, bound_group_id,
      name_sort_key, priority_sort_key, super_priority_sort_key, fallback_sort_key,
      concurrency_sort_key, account_expires_at_sort_key, last_used_at_sort_key,
      created_at_sort_key, access_type_sort_key, search_index_complete, authorization_quota_exceeded
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(viewer_system_account_id, account_id) DO UPDATE SET
      effective_status = excluded.effective_status,
      schedulable_bucket = excluded.schedulable_bucket,
      provider_code = excluded.provider_code,
      provider_protocol_profile_id = excluded.provider_protocol_profile_id,
      account_type = excluded.account_type,
      bound_group_id = excluded.bound_group_id,
      name_sort_key = excluded.name_sort_key,
      priority_sort_key = excluded.priority_sort_key,
      super_priority_sort_key = excluded.super_priority_sort_key,
      fallback_sort_key = excluded.fallback_sort_key,
      concurrency_sort_key = excluded.concurrency_sort_key,
      account_expires_at_sort_key = excluded.account_expires_at_sort_key,
      last_used_at_sort_key = excluded.last_used_at_sort_key,
      created_at_sort_key = excluded.created_at_sort_key,
      access_type_sort_key = excluded.access_type_sort_key,
      search_index_complete = excluded.search_index_complete,
      authorization_quota_exceeded = excluded.authorization_quota_exceeded`,
		value.viewerSystemAccountID, value.accountID, value.effectiveStatus, value.schedulableBucket,
		value.providerCode, value.providerProtocolProfileID, value.accountType, value.boundGroupID,
		value.nameSortKey, value.prioritySortKey, value.superPrioritySortKey, value.fallbackSortKey,
		value.concurrencySortKey, value.accountExpiresAtSortKey, value.lastUsedAtSortKey,
		value.createdAtSortKey, accessType, boolLit(r.postgres, value.searchIndexComplete), boolLit(r.postgres, quotaExceeded)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
    INSERT INTO `+r.table("account_list_availability_runtime_overlays")+` (
      account_id, current_concurrency, observed_at, next_reconcile_at
    ) VALUES (?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      current_concurrency = excluded.current_concurrency,
      observed_at = excluded.observed_at,
      next_reconcile_at = excluded.next_reconcile_at`,
		value.concurrencyAccountID, value.currentConcurrency, value.projectedAt, nil); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
    DELETE FROM `+r.table("account_list_availability_projection_tags")+`
    WHERE viewer_system_account_id = ? AND account_id = ?`,
		value.viewerSystemAccountID, value.accountID); err != nil {
		return false, err
	}
	for _, tagID := range value.tagIDs {
		if _, err := tx.ExecContext(ctx, `
      INSERT INTO `+r.table("account_list_availability_projection_tags")+` (viewer_system_account_id, account_id, tag_id)
      VALUES (?, ?, ?)`, value.viewerSystemAccountID, value.accountID, tagID); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
    DELETE FROM `+r.table("account_list_availability_projection_search_terms")+`
    WHERE viewer_system_account_id = ? AND account_id = ?`,
		value.viewerSystemAccountID, value.accountID); err != nil {
		return false, err
	}
	if value.searchIndexComplete {
		for _, term := range value.searchTerms {
			if _, err := tx.ExecContext(ctx, `
        INSERT INTO `+r.table("account_list_availability_projection_search_terms")+` (
          viewer_system_account_id, account_id, term, name_sort_key, created_at_sort_key
        ) VALUES (?, ?, ?, ?, ?)`,
				value.viewerSystemAccountID, value.accountID, term, value.nameSortKey, value.createdAtSortKey); err != nil {
				return false, err
			}
		}
	}
	if err := r.markViewerHealthStaleTx(ctx, tx, value.viewerSystemAccountID, value.projectedAt); err != nil {
		return false, err
	}
	return true, nil
}

func (r *ListAvailabilityRepo) markViewerHealthStaleTx(ctx context.Context, tx *sql.Tx, viewerSystemAccountID, updatedAt string) error {
	_, err := tx.ExecContext(ctx, `
    INSERT INTO `+r.table("account_list_availability_projection_viewer_health")+` (
      viewer_system_account_id, projection_count, oldest_projected_at,
      next_transition_at, is_current, updated_at
    ) VALUES (?, 0, NULL, NULL, 0, ?)
    ON CONFLICT(viewer_system_account_id) DO UPDATE SET
      is_current = 0,
      updated_at = excluded.updated_at`, viewerSystemAccountID, updatedAt)
	return err
}

// ApplyClaims 对齐 applyAccountListAvailabilityProjectionDirtyClaimsInClient
// （单事务批量提交，每行保留自己的 generation/claim 围栏；SQLite 串行逐行，
// 与 Node 非 PG 分支一致）。
func (r *ListAvailabilityRepo) ApplyClaims(ctx context.Context, writes []opsjobs.ProjectionWrite) (map[string]bool, error) {
	// Node apply 入口校验 sourceGeneration === claim.generation；Go 形状下
	// sourceGeneration 由 claim.Generation 派生（normalizeProjectionWrite），
	// 此处校验 claim 围栏本身有效。
	for _, write := range writes {
		if write.Claim.Generation < 1 || strings.TrimSpace(write.Claim.ClaimToken) == "" {
			return nil, errors.New("账户列表投影 dirty claim 围栏无效")
		}
	}
	result := map[string]bool{}
	if len(writes) == 0 {
		return result, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	dirty := r.table("account_list_availability_dirty")
	for _, write := range writes {
		normalized, err := normalizeProjectionWrite(write)
		if err != nil {
			return nil, err
		}
		claim := write.Claim
		var current string
		err = tx.QueryRowContext(ctx, `
      SELECT account_id
      FROM `+dirty+`
      WHERE account_id = ? AND generation = ? AND claim_token = ?
      LIMIT 1`, claim.AccountID, claim.Generation, claim.ClaimToken).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			result[claim.ClaimToken] = false
			continue
		}
		if err != nil {
			return nil, err
		}
		written, err := r.upsertProjectionTx(ctx, tx, normalized)
		if err != nil {
			return nil, err
		}
		if !written {
			return nil, fmt.Errorf("账户列表投影 %s generation %d 被更高版本覆盖", claim.AccountID, claim.Generation)
		}
		acknowledged, err := tx.ExecContext(ctx, `
      DELETE FROM `+dirty+`
      WHERE account_id = ? AND generation = ? AND claim_token = ?`,
			claim.AccountID, claim.Generation, claim.ClaimToken)
		if err != nil {
			return nil, err
		}
		deleted, err := acknowledged.RowsAffected()
		if err != nil {
			return nil, err
		}
		if deleted != 1 {
			return nil, fmt.Errorf("账户列表投影 %s generation %d 无法确认 dirty claim", claim.AccountID, claim.Generation)
		}
		result[claim.ClaimToken] = true
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// ApplyDeletionClaim 对齐 applyAccountListAvailabilityProjectionDeletionDirtyClaimInClient。
func (r *ListAvailabilityRepo) ApplyDeletionClaim(ctx context.Context, claim opsjobs.DirtyClaim) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	dirty := r.table("account_list_availability_dirty")
	var current string
	err = tx.QueryRowContext(ctx, `
    SELECT account_id
    FROM `+dirty+`
    WHERE account_id = ? AND generation = ? AND claim_token = ?
    LIMIT 1`, claim.AccountID, claim.Generation, claim.ClaimToken).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
    DELETE FROM `+r.table("account_list_availability_projections")+`
    WHERE account_id = ? AND source_generation <= ?`, claim.AccountID, claim.Generation)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if deleted > 0 {
		if err := r.markViewerHealthStaleTx(ctx, tx, claim.ViewerSystemAccountID, r.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
	}
	acknowledged, err := tx.ExecContext(ctx, `
    DELETE FROM `+dirty+`
    WHERE account_id = ? AND generation = ? AND claim_token = ?`,
		claim.AccountID, claim.Generation, claim.ClaimToken)
	if err != nil {
		return false, err
	}
	acknowledgedCount, err := acknowledged.RowsAffected()
	if err != nil {
		return false, err
	}
	if acknowledgedCount != 1 {
		return false, fmt.Errorf("账户列表投影 %s generation %d 无法确认删除", claim.AccountID, claim.Generation)
	}
	return deleted > 0, tx.Commit()
}

// ReleaseForReplay 对齐 releaseAccountListAvailabilityDirtyForReplayInClient。
func (r *ListAvailabilityRepo) ReleaseForReplay(ctx context.Context, input opsjobs.ListAvailabilityReplayInput) (bool, error) {
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" || len(accountID) > 256 {
		return false, errors.New("accountId 长度必须为 1..256")
	}
	if input.Generation < 1 {
		return false, errors.New("generation 必须大于 0")
	}
	claimToken := strings.TrimSpace(input.ClaimToken)
	if claimToken == "" || len(claimToken) > 256 {
		return false, errors.New("claimToken 长度必须为 1..256")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > 128 {
		return false, errors.New("reason 长度必须为 1..128")
	}
	if input.RetryDelayMS < 0 || input.RetryDelayMS > maximumDirtyRetryDelayMS {
		return false, fmt.Errorf("retryDelayMs 必须是 0..%d 的非负整数", maximumDirtyRetryDelayMS)
	}
	if input.NowMS < 0 {
		return false, errors.New("nowMs 必须是非负整数")
	}
	dirty := r.table("account_list_availability_dirty")
	var result sql.Result
	var err error
	if r.postgres {
		result, err = r.db.ExecContext(ctx, `
      UPDATE `+dirty+`
      SET reason = ?, available_at_ms = FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint + ?, claim_token = NULL,
          claimed_by = NULL, claim_until_ms = NULL, updated_at_ms = FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint
      WHERE account_id = ? AND generation = ? AND claim_token = ?`,
			reason, input.RetryDelayMS, accountID, input.Generation, claimToken)
	} else {
		result, err = r.db.ExecContext(ctx, `
      UPDATE `+dirty+`
      SET reason = ?, available_at_ms = ?, claim_token = NULL,
          claimed_by = NULL, claim_until_ms = NULL, updated_at_ms = ?
      WHERE account_id = ? AND generation = ? AND claim_token = ?`,
			reason, input.NowMS+input.RetryDelayMS, input.NowMS, accountID, input.Generation, claimToken)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// ---- 小工具 ----

func normalizedIDList(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" || len(text) > 256 {
			return nil, errors.New("id 长度必须为 1..256")
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		normalized = append(normalized, text)
	}
	return normalized, nil
}

func placeholdersFor(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

func instantParam(postgres bool, value string, now func() time.Time) any {
	if postgres {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	}
	return value
}

func boolLit(postgres bool, value bool) string {
	if postgres {
		if value {
			return "TRUE"
		}
		return "FALSE"
	}
	if value {
		return "1"
	}
	return "0"
}

// normalizeAccountNameSearchText 对齐 Node 同名函数（NFKC + trim）。
func normalizeAccountNameSearchText(value string) string {
	return strings.TrimSpace(norm.NFKC.String(value))
}

// nextTransitionAtOrEmpty 对齐 Node nextTransitionAt：全部候选过滤为严格未来
// 后取最早；无候选为空。
func nextTransitionAtOrEmpty(candidates []string, now time.Time) string {
	value, ok := opsjobs.NextTransitionAtRFC3339(candidates, now.UnixMilli())
	if !ok {
		return ""
	}
	return value
}
