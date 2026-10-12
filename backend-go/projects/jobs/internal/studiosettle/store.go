// Package studiosettle 实现媒体工作室任务的 jobs 结算 worker（媒体工作室设计
// §5）：领取谓词 + 原子租约、有界轮询、幂等三道闸（终态唯一性 / 分条互斥 /
// digest 同用户查重）、崩溃恢复与取消迟到成功收口。
//
// 表 owner 边界：studio_tasks / studio_assets / studio_user_asset_usage 的
// 写面 owner 是 gateway（§4 API 与受理结算）；jobs 仅按 §5 领取与结算
//（lease 列族、settlement、asset_id、error_json、poll_bookmark_json 与结算
// 产物行），不触碰 media_jobs（gateway 任务面 owner，jobs 只读关联）。jobs
// 不 import gateway 模块：表名/列名/状态词表/openai 轮询形态在此镜像（与
// maintenance 已落地 DDL 一致）。
package studiosettle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 任务状态与结算词表（studio_tasks §3.2，六值 status + 三值 settlement，
// 与 gateway internal/studio 镜像，jobs 不 import gateway）。
const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
	StatusExpired    = "expired"

	SettlementPending = "pending_settled"
	SettlementSettled = "settled"
	SettlementFailed  = "failed"

	KindImage = "image"
	KindVideo = "video"
)

// ErrSettleConflict 是结算条件 UPDATE 未命中（任务已被并发方收口，或取消
// 竞态先落 cancelled）的哨兵错误：调用方据此回收已写对象、不落库不虚计
//（§5.4 闸 1 / §4.4 迟到成功语义；gateway studio.ErrTaskSettleConflict 同义镜像）。
var ErrSettleConflict = errors.New("studiosettle: 任务终态/结算已被并发方收口")

// ErrQuotaExceeded / ErrCountExceeded 是配额越界的领域错误（§3.6：生成任务
// 产物入库超限 → settlement='failed' 记配额超限，不虚计不静默丢弃）。
var (
	ErrQuotaExceeded = errors.New("studio: 素材存储容量已满，产物未入库")
	ErrCountExceeded = errors.New("studio: 素材个数已达上限，产物未入库")
)

// 结算错误码（error_json.code，脱敏；与 gateway studio 结算错误码同词表）。
const (
	ErrorCodeSettleFailed    = "studio_settle_failed"
	ErrorCodeSettleQuota     = "studio_settle_quota"
	ErrorCodeAcceptanceGone  = "studio_acceptance_unknown"
	ErrorCodeAttemptsExhaust = "studio_settle_attempts_exhausted"
	ErrorCodeUnreachable     = "studio_task_unreachable"
)

// TaskRow 是 studio_tasks 一行的窄投影（列名严格对齐契约 §3.2 / maintenance
// 已落地 DDL）。
type TaskRow struct {
	ID                  string
	SystemAccountID     string
	ClientOperationID   string
	Kind                string
	Status              string
	Settlement          string
	AccountID           *string
	ProviderCode        *string
	UpstreamJobID       *string
	RequestSnapshotJSON string
	ErrorJSON           string
	AssetID             *string
	PollBookmarkJSON    string
	LeaseOwner          *string
	LeaseExpiresAt      *string
	LeaseAttemptCount   int
	CreatedAt           string
	UpdatedAt           string
}

const taskColumns = `id, system_account_id, client_operation_id, kind, status, settlement,
	account_id, provider_code, upstream_job_id, request_snapshot_json, error_json,
	asset_id, poll_bookmark_json, lease_owner, lease_expires_at, lease_attempt_count,
	created_at, updated_at`

func scanTask(scan func(dest ...any) error) (*TaskRow, error) {
	task := &TaskRow{}
	err := scan(&task.ID, &task.SystemAccountID, &task.ClientOperationID, &task.Kind,
		&task.Status, &task.Settlement, &task.AccountID, &task.ProviderCode,
		&task.UpstreamJobID, &task.RequestSnapshotJSON, &task.ErrorJSON, &task.AssetID,
		&task.PollBookmarkJSON, &task.LeaseOwner, &task.LeaseExpiresAt,
		&task.LeaseAttemptCount, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return task, nil
}

// claimPredicate 是领取谓词（§5.1 规范性）：
// (status IN ('queued','in_progress') OR settlement <> 'settled')
// AND (lease_owner IS NULL OR lease_expires_at IS NULL OR lease_expires_at <= ?)
// AND lease_attempt_count < ?。
// 显式覆盖"上游已 completed 但下载/入库失败、进程崩溃"中间态；lease_expires_at
// 文本列为统一 RFC3339 毫秒 UTC（gateway studio now() 同 layout
// 2006-01-02T15:04:05.000Z），字典序即时间序。
const claimPredicate = `((status IN ('queued','in_progress') OR settlement <> 'settled')
	 AND (lease_owner IS NULL OR lease_expires_at IS NULL OR lease_expires_at <= ?)
	 AND lease_attempt_count < ?)`

// TaskStore 是 studio_tasks 结算仓储的双模 SQL 实现（沿 cleanuprepo.DB 的
// DB+Postgres 双字段方言约定；PG 走 juhe_business schema 限定 + $n 占位符，
// SQLite 裸表名 + ? 占位）。组合根供 worker 装配。
type TaskStore struct {
	DB       *sql.DB
	Postgres bool
}

func (s *TaskStore) table(name string) string {
	if s.Postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *TaskStore) bind(query string) string {
	if !s.Postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// lockTaskTx 在事务内取任务行的分条互斥锁（§5.4 闸 2：PG advisory 事务锁
// hashtextextended(task.id, 0)；SQLite 无该锁、以单连接 + 事务串行等价）。
func (s *TaskStore) lockTaskTx(tx *sql.Tx, taskID string) error {
	if !s.Postgres {
		return nil
	}
	_, err := tx.Exec(s.bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`), taskID)
	return err
}

// GetByID 按主键回读任务行；不存在返回 (nil, nil)。
func (s *TaskStore) GetByID(ctx context.Context, id string) (*TaskRow, error) {
	rows, err := s.DB.QueryContext(ctx, s.bind(`SELECT `+taskColumns+` FROM `+s.table("studio_tasks")+`
		WHERE id = ? LIMIT 1`), id)
	if err != nil {
		if IsStudioTableMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanTask(rows.Scan)
}

// ClaimBatch 原子领取一批可结算任务（§5.1 原子领取：单条条件 UPDATE 写
// lease_owner / lease_expires_at / lease_attempt_count+1，RowsAffected=1 才算
// 领到，抢占式不依赖应用锁）。候选按 (created_at ASC, id ASC) 有界预选
//（limit*4 上限吸收并发竞争误判，未领满由下一轮继续），每轮领取 ≤ limit 行。
func (s *TaskStore) ClaimBatch(ctx context.Context, now string, leaseUntil string, leaseOwner string, limit, maxAttempts int) ([]*TaskRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	candidateLimit := limit * 4
	rows, err := s.DB.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("studio_tasks")+`
		WHERE `+claimPredicate+`
		ORDER BY created_at ASC, id ASC
		LIMIT ?`), now, maxAttempts, candidateLimit)
	if err != nil {
		if IsStudioTableMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("预选 studio 任务候选失败: %w", err)
	}
	candidates := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	claimed := make([]*TaskRow, 0, limit)
	for _, id := range candidates {
		if len(claimed) >= limit {
			break
		}
		result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
			lease_owner = ?, lease_expires_at = ?, lease_attempt_count = lease_attempt_count + 1, updated_at = ?
			WHERE id = ? AND `+claimPredicate),
			leaseOwner, leaseUntil, now, id, now, maxAttempts)
		if err != nil {
			return nil, fmt.Errorf("领取 studio 任务失败: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("读取 studio 任务领取行数失败: %w", err)
		}
		if affected != 1 {
			continue
		}
		task, err := s.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if task == nil {
			// 领取后行被删除（保留清理并发）：跳过，不视为本轮成果。
			continue
		}
		claimed = append(claimed, task)
	}
	return claimed, nil
}

// RenewLease 续租（§5.1：长下载/长轮询期间按进度续租）。条件 UPDATE 限定
// lease_owner = 本实例（失去租约后不再续），返回是否续上。
func (s *TaskStore) RenewLease(ctx context.Context, taskID, leaseOwner, leaseUntil, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ?`),
		leaseUntil, now, taskID, leaseOwner)
	if err != nil {
		return false, fmt.Errorf("续租 studio 任务失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务续租行数失败: %w", err)
	}
	return affected == 1, nil
}

// SaveBookmark 写上游轮询书签（§5.3：续轮询接续点；接管者从书签了解上次
// 轮询状态，不重复回溯）。条件 UPDATE 限定 lease_owner（失去租约的过期持有
// 者不得覆写新持有者的书签）。
func (s *TaskStore) SaveBookmark(ctx context.Context, taskID, leaseOwner, bookmarkJSON, now string) error {
	_, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		poll_bookmark_json = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ?`),
		bookmarkJSON, now, taskID, leaseOwner)
	if err != nil {
		return fmt.Errorf("写 studio 任务轮询书签失败: %w", err)
	}
	return nil
}

// AdvanceStatus 把任务 status 沿状态机推进（queued/in_progress → 目标非终态
// 或终态；闸 1 前置臂：条件 UPDATE 只允许从未终态出发，重复推进不产生第二次
// 落定）。返回是否由本次推进。
func (s *TaskStore) AdvanceStatus(ctx context.Context, taskID, ownerID, status, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		status = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress')`),
		status, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("推进 studio 任务状态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务状态推进行数失败: %w", err)
	}
	return affected == 1, nil
}

// AdoptUpstreamJobID 崩溃恢复的创建关联采纳（§5.5：upstream_job_id 为 NULL
// 时按媒体任务面关联重扫命中后接管其 job id）。条件 UPDATE 限定
// upstream_job_id IS NULL：采纳只发生一次，gateway 受理回填竞态先到时 0 行。
// 返回是否由本次采纳。
func (s *TaskStore) AdoptUpstreamJobID(ctx context.Context, taskID, ownerID, unifiedJobID, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		upstream_job_id = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND upstream_job_id IS NULL`),
		unifiedJobID, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("采纳 studio 任务上游关联失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务采纳行数失败: %w", err)
	}
	return affected == 1, nil
}

// SettleNoProduct 无产物收口（§3.2：本地失败/上游终态失败/取消迟到成功/
// 任务面不可达 → settlement='settled'，无产物可结算）。guardStatuses 限定
// 允许收口的来源 status 集（取消迟到成功 = 'cancelled'；失败/过期 = 'failed',
// 'expired'）。settlement='failed' 的可重试行不在此收口（重试臂经
// SettleWithAsset 走产物路径）。返回是否由本次落定（闸 1：重复回调 0 行）。
func (s *TaskStore) SettleNoProduct(ctx context.Context, taskID, ownerID, guardStatuses, errorJSON, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		settlement = 'settled',
		error_json = CASE WHEN ? = '' THEN error_json ELSE ? END,
		updated_at = ?
		WHERE id = ? AND system_account_id = ? AND settlement IN ('pending_settled','failed')
		  AND status IN (`+guardStatuses+`)`),
		errorJSON, errorJSON, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("收口 studio 任务无产物结算失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务无产物结算行数失败: %w", err)
	}
	return affected == 1, nil
}

// FailTaskTerminal 镜像上游终态失败（§6：in_progress → failed）：status 从
// 未终态推进到 failed 且 settlement='settled'（无产物可结算，§3.2）。返回是
// 否由本次落定；0 行 = 并发方已收口。
func (s *TaskStore) FailTaskTerminal(ctx context.Context, taskID, ownerID, errorJSON, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		status = 'failed', settlement = 'settled', error_json = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress')
		  AND settlement IN ('pending_settled','failed')`),
		errorJSON, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("落 studio 任务上游失败终态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务失败终态行数失败: %w", err)
	}
	return affected == 1, nil
}

// ExpireUnreachable 任务面不可达收口（§5.2/§5.3：media_jobs 行被保留清理后
// 任务面返回不可达 → status='expired' + settlement='settled'，无产物可结算）。
// status 条件允许从未终态或 completed（completed 但产物行已不可达）推进；
// settlement 闸 1 限定未 settled。返回是否由本次落定。
func (s *TaskStore) ExpireUnreachable(ctx context.Context, taskID, ownerID, errorJSON, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		status = 'expired', settlement = 'settled', error_json = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND settlement IN ('pending_settled','failed')
		  AND status IN ('queued','in_progress','completed')`),
		errorJSON, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("落 studio 任务不可达终态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务不可达终态行数失败: %w", err)
	}
	return affected == 1, nil
}

// MarkSettlementFailed 结算失败迁移（pending_settled → failed，§6 可重新领取
// 重试，上限内；attempt 上限收口由 ExhaustAttempts 承担）。返回是否由本次
// 落定。
func (s *TaskStore) MarkSettlementFailed(ctx context.Context, taskID, ownerID, errorJSON, now string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		settlement = 'failed', error_json = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND settlement = 'pending_settled'`),
		errorJSON, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("迁移 studio 任务结算失败态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务结算失败态行数失败: %w", err)
	}
	return affected == 1, nil
}

// ExhaustAttempts 是 attempt 上限收口（§5.1：达到 lease_attempt_count 上限仍
// 未结算 → 终局 settlement='failed'，领取谓词的 attempt 守卫使其不再被领取；
// 任务保持当前展示态、产物缺失标注，error_json 记录原因）。只收 pending_settled
// 行（settlement='failed' 行已被谓词排除，无需再收）。返回收口行数（≤ limit）。
func (s *TaskStore) ExhaustAttempts(ctx context.Context, errorJSON, now string, maxAttempts, limit int) (int64, error) {
	// id 子查询 LIMIT 限流（SQLite UPDATE 不支持 LIMIT，双方言统一走子查询
	// 定位；mediajobs 同款）。
	result, err := s.DB.ExecContext(ctx, s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		settlement = 'failed', error_json = ?, updated_at = ?
		WHERE id IN (SELECT id FROM `+s.table("studio_tasks")+`
			WHERE settlement = 'pending_settled' AND lease_attempt_count >= ?
			LIMIT ?)`),
		errorJSON, now, maxAttempts, limit)
	if err != nil {
		return 0, fmt.Errorf("收口 studio 任务结算重试上限失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 studio 任务重试上限收口行数失败: %w", err)
	}
	return affected, nil
}

// SettleAssetInput 携带产物入库单事务所需的全部事实（§5.5；SourceTaskID 必须
// 已填任务 id，闸 3 的定位键）。
type SettleAssetInput struct {
	TaskID  string
	OwnerID string
	Asset   AssetInsert
	Now     string
}

// AssetInsert 是 studio_assets 插入载荷（列集对齐契约 §3.1）。
type AssetInsert struct {
	ID           string
	Kind         string
	Mime         string
	StorageKey   string
	Bytes        int64
	Width        *int64
	Height       *int64
	DurationMs   *int64
	Sha256       string
	SourceTaskID string
	ParamsJSON   string
}

// SettleWithAsset 在单事务内完成幂等三道闸结算（§5.4/§5.5）：
//  1. 闸 2 分条互斥：PG advisory 事务锁（task.id）+ 事务串行；
//  2. 闸 3 digest 同用户查重：同 (system_account_id, source_task_id) 既有资
//     产行复用（崩溃于"已入库未标记 settled"窗口的重放不再插入、不再入账；
//     idx_studio_assets_owner_source_task partial 唯一索引是并发重放 DB 兜底）；
//  3. 闸 1 终态唯一性：任务条件 UPDATE 限定未 settled，未命中整体回滚并返回
//     ErrSettleConflict（取消竞态/并发收口 → 调用方回收已写对象，迟到成功）。
//
// 配额预检与增量在同一事务内（§3.6 计量点；超限返回 ErrQuotaExceeded /
// ErrCountExceeded，行与账本不落）。返回资产行 id 与是否复用既有行。
func (s *TaskStore) SettleWithAsset(ctx context.Context, input SettleAssetInput) (assetID string, reused bool, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	// 闸 2：分条互斥。
	if err := s.lockTaskTx(tx, input.TaskID); err != nil {
		return "", false, err
	}

	// 闸 3：同用户同任务产物查重（主键定位 source_task_id，sha256 仅辅助）。
	existing := tx.QueryRow(s.bind(`SELECT id FROM `+s.table("studio_assets")+`
		WHERE system_account_id = ? AND source_task_id = ?
		LIMIT 1`), input.OwnerID, input.TaskID)
	var reuseID string
	if scanErr := existing.Scan(&reuseID); scanErr == nil {
		assetID = reuseID
		reused = true
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return "", false, scanErr
	}

	incomingBytes := int64(0)
	if assetID == "" {
		// 配额预检（§3.6：结算入库前预检，超限 → 结算失败）。
		var usageBytes, usageCount int64
		usageErr := tx.QueryRow(s.bind(`SELECT asset_bytes, asset_count FROM `+s.table("studio_user_asset_usage")+`
			WHERE system_account_id = ?`), input.OwnerID).Scan(&usageBytes, &usageCount)
		if usageErr != nil && !errors.Is(usageErr, sql.ErrNoRows) {
			return "", false, usageErr
		}
		if usageBytes+input.Asset.Bytes > AssetUserMaxBytes {
			return "", false, ErrQuotaExceeded
		}
		if usageCount+1 > AssetUserMaxCount {
			return "", false, ErrCountExceeded
		}
		assetID = input.Asset.ID
		paramsJSON := strings.TrimSpace(input.Asset.ParamsJSON)
		if paramsJSON == "" {
			paramsJSON = "{}"
		}
		if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("studio_assets")+` (
			id, system_account_id, kind, mime, storage_key, bytes, width, height, duration_ms,
			sha256, source_task_id, params_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			assetID, input.OwnerID, input.Asset.Kind, input.Asset.Mime, input.Asset.StorageKey,
			input.Asset.Bytes, input.Asset.Width, input.Asset.Height, input.Asset.DurationMs,
			input.Asset.Sha256, input.Asset.SourceTaskID, paramsJSON, input.Now, input.Now); err != nil {
			// 并发重放撞 partial 唯一索引（闸 3 DB 兜底臂）：复用既有资产行
			// 完成收口，不重复插入不重复入账。
			var fallbackID string
			retryErr := tx.QueryRow(s.bind(`SELECT id FROM `+s.table("studio_assets")+`
				WHERE system_account_id = ? AND source_task_id = ?
				LIMIT 1`), input.OwnerID, input.TaskID).Scan(&fallbackID)
			if retryErr != nil {
				return "", false, err
			}
			assetID = fallbackID
			reused = true
		} else {
			incomingBytes = input.Asset.Bytes
		}
	}

	// 闸 1：终态唯一性（status 未终态或 completed 且 settlement 未 settled；
	// failed → settled 是 §6 重领取重试收口臂）。
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("studio_tasks")+` SET
		status = 'completed', settlement = 'settled', asset_id = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress','completed')
		  AND settlement IN ('pending_settled','failed')`),
		assetID, input.Now, input.TaskID, input.OwnerID)
	if err != nil {
		return "", false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if affected != 1 {
		return "", false, ErrSettleConflict
	}
	if incomingBytes > 0 {
		if err := s.incrementAssetUsage(tx, input.OwnerID, incomingBytes, input.Now); err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return assetID, reused, nil
}

func (s *TaskStore) incrementAssetUsage(tx *sql.Tx, ownerID string, quotaBytes int64, now string) error {
	// DO UPDATE 右值必须表名限定：PG 对未限定的自引用列报 42702 ambiguous
	//（gateway chatassets/studio 同款登记）。
	usageTable := s.table("studio_user_asset_usage")
	_, err := tx.Exec(s.bind(`INSERT INTO `+usageTable+` (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT (system_account_id) DO UPDATE SET
			asset_bytes = `+usageTable+`.asset_bytes + ?, asset_count = `+usageTable+`.asset_count + 1, updated_at = ?`),
		ownerID, quotaBytes, now, quotaBytes, now)
	return err
}

// MediaJobAffinity 是 media_jobs 行的账户亲和窄投影（jobs 只读；列集对齐
// maintenance 已落地 media_jobs DDL）。
type MediaJobAffinity struct {
	UnifiedJobID  string
	APIKeyID      string
	AccountID     string
	ProviderCode  string
	UpstreamJobID string
	Status        string
	UsageJSON     string
}

// LoadMediaJobAffinity 按 studio_tasks.upstream_job_id（= /v1 统一 job id =
// media_jobs.id）回读账户亲和事实（§5.1 三元组：受理确立后查询/下载都定位
// 原账户原上游，永不换账户）。行不存在（含被 7 天保留清理）返回 (nil, false,
// nil)，调用方按任务面不可达语义收口。
func (s *TaskStore) LoadMediaJobAffinity(ctx context.Context, unifiedJobID string) (*MediaJobAffinity, bool, error) {
	row := s.DB.QueryRowContext(ctx, s.bind(`SELECT id, api_key_id, account_id, provider_code,
		upstream_job_id, status, usage_json
		FROM `+s.table("media_jobs")+`
		WHERE id = ? LIMIT 1`), unifiedJobID)
	var affinity MediaJobAffinity
	err := row.Scan(&affinity.UnifiedJobID, &affinity.APIKeyID, &affinity.AccountID,
		&affinity.ProviderCode, &affinity.UpstreamJobID, &affinity.Status, &affinity.UsageJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		if IsStudioTableMissing(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("回读媒体任务关联失败: %w", err)
	}
	return &affinity, true, nil
}

// FindMediaJobForRescan 是崩溃恢复的创建关联重扫（§5.5：upstream_job_id 为
// NULL 时按该用户 studio Key + kind + created_at >= 任务行 created_at 定位
// 唯一关联行；§4.5.7 同用户创建串行化保证唯一，取最早命中）。未找到返回
// (nil, false, nil)。
func (s *TaskStore) FindMediaJobForRescan(ctx context.Context, ownerID, kind, createdSince string) (*MediaJobAffinity, bool, error) {
	row := s.DB.QueryRowContext(ctx, s.bind(`SELECT mj.id, mj.api_key_id, mj.account_id, mj.provider_code,
		mj.upstream_job_id, mj.status, mj.usage_json
		FROM `+s.table("media_jobs")+` mj
		JOIN `+s.table("api_keys")+` ak ON ak.id = mj.api_key_id
		WHERE ak.system_account_id = ? AND ak.purpose = 'studio'
		  AND mj.kind = ? AND mj.created_at >= ?
		ORDER BY mj.created_at ASC, mj.id ASC
		LIMIT 1`), ownerID, kind, createdSince)
	var affinity MediaJobAffinity
	err := row.Scan(&affinity.UnifiedJobID, &affinity.APIKeyID, &affinity.AccountID,
		&affinity.ProviderCode, &affinity.UpstreamJobID, &affinity.Status, &affinity.UsageJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		if IsStudioTableMissing(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("创建关联重扫失败: %w", err)
	}
	return &affinity, true, nil
}

// 配额上限（§3.6 / gateway studio.StudioAssetUserMaxBytes 同值镜像，jobs 不
// import gateway）。
const (
	AssetUserMaxBytes = int64(20) * 1024 * 1024 * 1024
	AssetUserMaxCount = int64(2000)
)

// IsStudioTableMissing 报告错误是否为 studio/media 表缺席（老库未跑
// maintenance 迁移 → 零任务快速返回；错误文本判 no such table / does not
// exist，与 retention.IsMediaJobsTableMissing 同模式）。
func IsStudioTableMissing(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"no such table: studio_tasks", "studio_tasks\" does not exist", "studio_tasks does not exist",
		"no such table: studio_assets", "studio_assets\" does not exist", "studio_assets does not exist",
		"no such table: media_jobs", "media_jobs\" does not exist", "media_jobs does not exist",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
