package studio

// studio_tasks / studio_operations 持久化（媒体工作室设计 §3.2/§3.3）。行列
// 名严格对齐契约 DDL；状态推进一律条件 UPDATE + RowsAffected 门控（终态与
// settlement 只沿 §6 状态机单向落定一次，重复回调不产生第二行第二份账，§5.4
// 闸 1；internal/gatewaymedia/jobsrepo.go UpdateTerminal 同款模式）。SQL 风格
// 与 store.go 同款：SQLite ? 占位 + PG $n 改写（Dialect.Bind）+ juhe_business
// schema 限定（Dialect.Table）。jobs 结算 worker 的领取谓词/租约抢占查询属
// jobs 批次，本仓储只提供行存取与列更新能力。

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Task 是 studio_tasks 一行（列名严格对齐契约 §3.2）。Status 为六值词表
// （queued/in_progress/completed/failed/cancelled/expired），Settlement 与之
// 正交（pending_settled/settled/failed）。
type Task struct {
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

// TaskColumns 是 studio_tasks 的全列投影（顺序与 scanTask 一致）。
const TaskColumns = `id, system_account_id, client_operation_id, kind, status, settlement,
	account_id, provider_code, upstream_job_id, request_snapshot_json, error_json,
	asset_id, poll_bookmark_json, lease_owner, lease_expires_at, lease_attempt_count,
	created_at, updated_at`

func scanTask(scan func(dest ...any) error) (*Task, error) {
	task := &Task{}
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

// StudioTaskStatuses 是任务 status 六值词表（列表过滤校验用，§4.4：单值非法
// 400）。
var StudioTaskStatuses = map[string]bool{
	"queued": true, "in_progress": true, "completed": true,
	"failed": true, "cancelled": true, "expired": true,
}

// StudioTaskKinds 是任务 kind 词表（image/video，§3.2；TTS/STT 是同步端点不
// 产生任务行）。
var StudioTaskKinds = map[string]bool{"image": true, "video": true}

// NewTaskID 生成 stask_ 前缀、32 位十六进制随机的任务 id。
func NewTaskID() string {
	return "stask_" + randomHex32()
}

// CreateTaskInput 携带任务行创建所需的全部事实。
type CreateTaskInput struct {
	ID                  string
	SystemAccountID     string
	ClientOperationID   string
	Kind                string
	RequestSnapshotJSON string
	Now                 string
}

// ErrTaskSettleConflict 是结算条件 UPDATE 未命中（任务已被取消或并发方已收
// 口）的哨兵错误：调用方据此丢弃产物（对象回收、不落库、不虚计，§4.4 迟到
// 成功语义）。
var ErrTaskSettleConflict = errors.New("studio: 任务终态/结算已被并发方收口")

// CreateTask 落一行新任务（status='queued'、settlement='pending_settled'）。
// 同 (system_account_id, client_operation_id) 撞唯一约束时按幂等协议返回既有
// 任务（created=false，调用方不再调上游，§4.0）。
func (s *Store) CreateTask(input CreateTaskInput) (*Task, bool, error) {
	id := input.ID
	if id == "" {
		id = NewTaskID()
	}
	snapshot := strings.TrimSpace(input.RequestSnapshotJSON)
	if snapshot == "" {
		snapshot = "{}"
	}
	_, err := s.db.Exec(s.dialect.Bind(`INSERT INTO `+s.dialect.Table("studio_tasks")+` (
		id, system_account_id, client_operation_id, kind, status, settlement,
		request_snapshot_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, 'queued', 'pending_settled', ?, ?, ?)`),
		id, input.SystemAccountID, input.ClientOperationID, input.Kind, snapshot, input.Now, input.Now)
	if err == nil {
		task, getErr := s.GetTask(id, input.SystemAccountID)
		if getErr != nil {
			return nil, false, getErr
		}
		return task, true, nil
	}
	if !isDuplicateTaskOperationError(err) {
		return nil, false, err
	}
	// 幂等重放：并发/重复提交同 ID，返回既有任务（§4.0 响应丢失确认口径）。
	existing, getErr := s.GetTaskByOperation(input.SystemAccountID, input.ClientOperationID)
	if getErr != nil {
		return nil, false, getErr
	}
	if existing == nil {
		return nil, false, err
	}
	return existing, false, nil
}

// isDuplicateTaskOperationError 识别 UNIQUE(system_account_id,
// client_operation_id) 冲突（PG 具名约束 / SQLite 列序报错两族）。
func isDuplicateTaskOperationError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "studio_tasks.system_account_id, studio_tasks.client_operation_id") ||
		strings.Contains(message, "studio_tasks_system_account_id_client_operation_id_key")
}

// GetTask 按 id + 归属读取任务；不存在或越权同为 (nil, nil)（路由层统一 404
// 同型，§4.0）。
func (s *Store) GetTask(taskID, ownerID string) (*Task, error) {
	tasks, err := s.queryTasks(s.dialect.Bind(`SELECT `+TaskColumns+` FROM `+s.dialect.Table("studio_tasks")+`
		WHERE id = ? AND system_account_id = ?
		LIMIT 1`), taskID, ownerID)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	return tasks[0], nil
}

// GetTaskByOperation 按用户范围幂等键读取任务（CreateTask 冲突恢复与响应丢
// 失后的受理确认查询共用，§4.0/§4.4）。
func (s *Store) GetTaskByOperation(ownerID, clientOperationID string) (*Task, error) {
	tasks, err := s.queryTasks(s.dialect.Bind(`SELECT `+TaskColumns+` FROM `+s.dialect.Table("studio_tasks")+`
		WHERE system_account_id = ? AND client_operation_id = ?
		LIMIT 1`), ownerID, clientOperationID)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	return tasks[0], nil
}

// TaskListQuery 是任务列表查询参数（空串 = 不过滤，§4.4）。
type TaskListQuery struct {
	Status      string
	Kind        string
	OperationID string
	Limit       int
	Offset      int
}

// ListTasks 按归属过滤任务列表（status/kind/operation_id 精确匹配，排序
// (created_at DESC, id DESC)），返回页行与过滤后总数。
func (s *Store) ListTasks(ownerID string, query TaskListQuery) ([]*Task, int, error) {
	if query.Limit <= 0 || query.Limit > 100 {
		query.Limit = 20
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	clauses := []string{"system_account_id = ?"}
	args := []any{ownerID}
	if query.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, query.Status)
	}
	if query.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, query.Kind)
	}
	if query.OperationID != "" {
		clauses = append(clauses, "client_operation_id = ?")
		args = append(args, query.OperationID)
	}
	where := "WHERE " + strings.Join(clauses, " AND ")
	var total int
	if err := s.db.QueryRow(s.dialect.Bind(`SELECT COUNT(*) FROM `+s.dialect.Table("studio_tasks")+` `+where),
		args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计 studio 任务行失败: %w", err)
	}
	paged := append(args, query.Limit, query.Offset)
	tasks, err := s.queryTasks(s.dialect.Bind(`SELECT `+TaskColumns+` FROM `+s.dialect.Table("studio_tasks")+` `+where+`
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?`), paged...)
	if err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// MarkTaskDispatched 受理确立回填（§4.5.5 受理边界：/v1 返回 2xx + job id 后
// 一次性写入账户亲和三元组 + in_progress；upstream_job_id 此后永不重建）。条
// 件 UPDATE 限定 status='queued'：取消竞态先落 cancelled 时 0 行，受理结果按
// 取消语义丢弃（任务不复活，§4.4）。返回是否由本次写入确立。
func (s *Store) MarkTaskDispatched(taskID, ownerID, accountID, providerCode, upstreamJobID, now string) (bool, error) {
	result, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		status = 'in_progress', account_id = ?, provider_code = ?, upstream_job_id = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status = 'queued'`),
		accountID, providerCode, upstreamJobID, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("回填 studio 任务受理凭据失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务受理回填行数失败: %w", err)
	}
	return affected == 1, nil
}

// FailTask 受理前失败/上游终态失败收口（无产物可结算 → settlement 直接
// 'settled'，§3.2 要点）。条件 UPDATE 限定非终态：终态行不被覆盖，返回是否
// 由本次落定。
func (s *Store) FailTask(taskID, ownerID, errorJSON, now string) (bool, error) {
	result, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		status = 'failed', settlement = 'settled', error_json = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress')`),
		errorJSON, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("回填 studio 任务失败终态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务失败终态行数失败: %w", err)
	}
	return affected == 1, nil
}

// FailTaskSettlement 落结算终局失败（status='failed' + settlement='failed'，
// §3.6：生成任务产物入库失败 → 任务结算失败，error 记原因，不虚计不静默丢
// 弃）。条件 UPDATE 限定非终态，返回是否由本次落定。
func (s *Store) FailTaskSettlement(taskID, ownerID, errorJSON, now string) (bool, error) {
	result, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		status = 'failed', settlement = 'failed', error_json = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress')`),
		errorJSON, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("回填 studio 任务结算终局失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务结算终局行数失败: %w", err)
	}
	return affected == 1, nil
}

// CancelTask 本地取消收口（§4.4 a/b 态：queued 未受理不调上游；in_progress
// 已受理在上游尽力取消后本地收口，不阻塞）。取消即无产物可结算，settlement
// 一并 'settled'（jobs 领取谓词 status IN ('queued','in_progress') OR
// settlement<>'settled' 对 cancelled+settled 恒不命中，已取消任务不再被领取
// 轮询）。条件 UPDATE 限定非终态，返回是否由本次落定（0 行 = 并发取消/终态
// 先到，调用方回读当前态返回）。
func (s *Store) CancelTask(taskID, ownerID, now string) (bool, error) {
	result, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		status = 'cancelled', settlement = 'settled', updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress')`),
		now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("更新 studio 任务取消状态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务取消行数失败: %w", err)
	}
	return affected == 1, nil
}

// SettleTaskInput 携带产物入库 + 任务收口单事务所需的全部事实（§5.5 产物落
// 库：插入 studio_assets + 任务 completed + settlement='settled' + asset_id +
// 配额增量同一事务）。Asset.SourceTaskID 必须已填任务 id。
type SettleTaskInput struct {
	TaskID  string
	OwnerID string
	Asset   CreateAssetInput
	Now     string
}

// SettleTaskWithAsset 在单事务内完成产物入库 + 任务终态收口 + 配额增量
// （§5.5）。同任务产物已存在（崩溃于"已入库未标记 settled"窗口的重放结算）
// 时复用既有资产行完成收口、不重复插入不重复入账（§5.4 闸 3；
// idx_studio_assets_owner_source_task partial 唯一索引是并发重放的 DB 兜底）。
// 任务条件 UPDATE（非终态且 settlement='pending_settled'）未命中（取消竞态/
// 并发收口）时整体回滚并返回 ErrTaskSettleConflict——资产行、账本与产物一
// 并不落，调用方回收已写对象（迟到成功语义，§4.4）。
func (s *Store) SettleTaskWithAsset(input SettleTaskInput) (*Asset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.dialect.lockAssetUsage(tx, input.OwnerID); err != nil {
		return nil, err
	}
	// 闸 3 同用户查重：同任务产物行全库唯一（source_task_id 定位）。
	var existingAssetID string
	scanErr := tx.QueryRow(s.dialect.Bind(`SELECT id FROM `+s.dialect.Table("studio_assets")+`
		WHERE system_account_id = ? AND source_task_id = ?
		LIMIT 1`), input.OwnerID, input.TaskID).Scan(&existingAssetID)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return nil, scanErr
	}
	assetID := existingAssetID
	incomingBytes := int64(0)
	if assetID == "" {
		var usageBytes, usageCount int64
		err = tx.QueryRow(s.dialect.Bind(`SELECT asset_bytes, asset_count FROM `+s.dialect.Table("studio_user_asset_usage")+`
			WHERE system_account_id = ?`), input.OwnerID).Scan(&usageBytes, &usageCount)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if usageBytes+input.Asset.Bytes > StudioAssetUserMaxBytes {
			return nil, &QuotaExceededError{}
		}
		if usageCount+1 > StudioAssetUserMaxCount {
			return nil, &CountExceededError{}
		}
		assetID = input.Asset.ID
		if assetID == "" {
			assetID = NewAssetID()
		}
		paramsJSON := strings.TrimSpace(input.Asset.ParamsJSON)
		if paramsJSON == "" {
			paramsJSON = "{}"
		}
		if _, err := tx.Exec(s.dialect.Bind(`INSERT INTO `+s.dialect.Table("studio_assets")+` (
			id, system_account_id, kind, mime, storage_key, bytes, width, height, duration_ms,
			sha256, source_task_id, params_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			assetID, input.Asset.SystemAccountID, input.Asset.Kind, input.Asset.Mime, input.Asset.StorageKey,
			input.Asset.Bytes, input.Asset.Width, input.Asset.Height, input.Asset.DurationMs,
			input.Asset.Sha256, input.Asset.SourceTaskID, paramsJSON, input.Asset.Now, input.Asset.Now); err != nil {
			// 并发重放撞 partial 唯一索引：按既有资产行复用重试一次（§5.4 闸 3
			// DB 兜底臂），仍失败则原样返回底层错误。
			var reusedID string
			retryErr := tx.QueryRow(s.dialect.Bind(`SELECT id FROM `+s.dialect.Table("studio_assets")+`
				WHERE system_account_id = ? AND source_task_id = ?
				LIMIT 1`), input.OwnerID, input.TaskID).Scan(&reusedID)
			if retryErr != nil {
				return nil, err
			}
			assetID = reusedID
			incomingBytes = 0
		} else {
			incomingBytes = input.Asset.Bytes
		}
	}
	result, err := tx.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		status = 'completed', settlement = 'settled', asset_id = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND status IN ('queued','in_progress')
		  AND settlement = 'pending_settled'`),
		assetID, input.Now, input.TaskID, input.OwnerID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, ErrTaskSettleConflict
	}
	if incomingBytes > 0 {
		if err := s.incrementAssetUsage(tx, input.OwnerID, incomingBytes, input.Now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAsset(assetID, input.OwnerID)
}

// MarkSettled 结算收口（pending_settled → settled，含 asset_id 回填；failed
// → settled 是 §6 状态机的重领取重试收口臂，同样在此落定）。条件 UPDATE 限定
// settlement 可迁入集，返回是否由本次落定。
func (s *Store) MarkSettled(taskID, ownerID string, assetID *string, now string) (bool, error) {
	result, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		settlement = 'settled', asset_id = COALESCE(?, asset_id), updated_at = ?
		WHERE id = ? AND system_account_id = ? AND settlement IN ('pending_settled','failed')`),
		assetID, now, taskID, ownerID)
	if err != nil {
		return false, fmt.Errorf("收口 studio 任务结算失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 studio 任务结算收口行数失败: %w", err)
	}
	return affected == 1, nil
}

// MarkSettlementFailed 结算失败迁移（pending_settled → failed，可被重新领取
// 重试，上限内，§3.2/§5.4）。条件 UPDATE 限定 settlement='pending_settled'，
// 返回是否由本次落定。
func (s *Store) MarkSettlementFailed(taskID, ownerID, errorJSON, now string) (bool, error) {
	result, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
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

// UpdateLease 更新租约列（lease_owner / lease_expires_at，§5.1 续租写面）。
// 领取谓词与抢占式原子领取属 jobs 批次，本方法只承载列更新。
func (s *Store) UpdateLease(taskID, leaseOwner, leaseExpiresAt, now string) error {
	_, err := s.db.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_tasks")+` SET
		lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ?`), leaseOwner, leaseExpiresAt, now, taskID)
	if err != nil {
		return fmt.Errorf("更新 studio 任务租约失败: %w", err)
	}
	return nil
}

// FilterExistingAssetIDs 在同用户范围内批量确认资产 id 存在性（TaskDTO
// assetId 的"settled 且资产存在"投影；悬空引用不外露，§4.4/§6）。
func (s *Store) FilterExistingAssetIDs(ownerID string, assetIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(assetIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(assetIDs)), ",")
	args := []any{ownerID}
	for _, id := range assetIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(s.dialect.Bind(`SELECT id FROM `+s.dialect.Table("studio_assets")+`
		WHERE system_account_id = ? AND id IN (`+placeholders+`)`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// queryTasks 执行任务行读取。
func (s *Store) queryTasks(query string, args ...any) ([]*Task, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		task, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// studio_operations（同步生成幂等收据，§3.3）
// ---------------------------------------------------------------------------

// Operation 是 studio_operations 一行。Kind ∈ {audio_speech,
// audio_transcription}；speech 行带 AssetID，transcription 行 AssetID 为空、
// ResultJSON 承载 {text, srt?}。
type Operation struct {
	ID                string
	SystemAccountID   string
	ClientOperationID string
	Kind              string
	AssetID           *string
	ResultJSON        string
	CreatedAt         string
	UpdatedAt         string
}

// OperationColumns 是 studio_operations 的全列投影（顺序与 scanOperation 一致）。
const OperationColumns = `id, system_account_id, client_operation_id, kind, asset_id,
	result_json, created_at, updated_at`

func scanOperation(scan func(dest ...any) error) (*Operation, error) {
	op := &Operation{}
	err := scan(&op.ID, &op.SystemAccountID, &op.ClientOperationID, &op.Kind,
		&op.AssetID, &op.ResultJSON, &op.CreatedAt, &op.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return op, nil
}

// NewOperationID 生成 stoper_ 前缀、32 位十六进制随机的收据 id（§3.3）。
func NewOperationID() string {
	return "stoper_" + randomHex32()
}

// GetOperation 按 (归属, client_operation_id, kind) 读取幂等收据；未命中返回
// (nil, nil)。
func (s *Store) GetOperation(ownerID, clientOperationID, kind string) (*Operation, error) {
	rows, err := s.db.Query(s.dialect.Bind(`SELECT `+OperationColumns+` FROM `+s.dialect.Table("studio_operations")+`
		WHERE system_account_id = ? AND client_operation_id = ? AND kind = ?
		LIMIT 1`), ownerID, clientOperationID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		op, err := scanOperation(rows.Scan)
		if err != nil {
			return nil, err
		}
		return op, nil
	}
	return nil, rows.Err()
}

// CreateOperationInput 携带收据行创建所需的全部事实。
type CreateOperationInput struct {
	ID                string
	SystemAccountID   string
	ClientOperationID string
	Kind              string
	AssetID           *string
	ResultJSON        string
	Now               string
}

// ErrOperationExists 是收据唯一约束冲突的哨兵（并发同 ID 插入败者回读既有
// 收据返回，不报错外泄）。
var ErrOperationExists = errors.New("studio: 幂等收据已存在")

// CreateOperation 落一行收据；同 (system_account_id, client_operation_id,
// kind) 撞唯一约束时返回 ErrOperationExists（调用方回读既有收据按 §3.3 返回
// 原结果）。resultJSON 缺省 '{}'。
func (s *Store) CreateOperation(input CreateOperationInput) (*Operation, error) {
	id := input.ID
	if id == "" {
		id = NewOperationID()
	}
	resultJSON := strings.TrimSpace(input.ResultJSON)
	if resultJSON == "" {
		resultJSON = "{}"
	}
	_, err := s.db.Exec(s.dialect.Bind(`INSERT INTO `+s.dialect.Table("studio_operations")+` (
		id, system_account_id, client_operation_id, kind, asset_id, result_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		id, input.SystemAccountID, input.ClientOperationID, input.Kind, input.AssetID,
		resultJSON, input.Now, input.Now)
	if err != nil {
		if isDuplicateOperationError(err) {
			return nil, ErrOperationExists
		}
		return nil, err
	}
	return s.getOperationByID(id, input.SystemAccountID)
}

// CreateOperationWithAsset 在单事务内完成资产入库（配额预检 + 增量，§3.6）+
// speech 收据落行：消除"资产已入库、收据未落"崩溃窗口（该窗口下同 ID 重放会
// 重复调用上游，违背 §3.3 防重复付费）。收据唯一约束冲突时整体回滚并返回
// ErrOperationExists（并发败者回读既有收据返回原资产）。
func (s *Store) CreateOperationWithAsset(asset CreateAssetInput, op CreateOperationInput) (*Asset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.dialect.lockAssetUsage(tx, asset.SystemAccountID); err != nil {
		return nil, err
	}
	var usageBytes, usageCount int64
	err = tx.QueryRow(s.dialect.Bind(`SELECT asset_bytes, asset_count FROM `+s.dialect.Table("studio_user_asset_usage")+`
		WHERE system_account_id = ?`), asset.SystemAccountID).Scan(&usageBytes, &usageCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if usageBytes+asset.Bytes > StudioAssetUserMaxBytes {
		return nil, &QuotaExceededError{}
	}
	if usageCount+1 > StudioAssetUserMaxCount {
		return nil, &CountExceededError{}
	}
	assetID := asset.ID
	if assetID == "" {
		assetID = NewAssetID()
	}
	paramsJSON := strings.TrimSpace(asset.ParamsJSON)
	if paramsJSON == "" {
		paramsJSON = "{}"
	}
	if _, err := tx.Exec(s.dialect.Bind(`INSERT INTO `+s.dialect.Table("studio_assets")+` (
		id, system_account_id, kind, mime, storage_key, bytes, width, height, duration_ms,
		sha256, source_task_id, params_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		assetID, asset.SystemAccountID, asset.Kind, asset.Mime, asset.StorageKey,
		asset.Bytes, asset.Width, asset.Height, asset.DurationMs,
		asset.Sha256, asset.SourceTaskID, paramsJSON, asset.Now, asset.Now); err != nil {
		return nil, err
	}
	if err := s.incrementAssetUsage(tx, asset.SystemAccountID, asset.Bytes, asset.Now); err != nil {
		return nil, err
	}
	resultJSON := strings.TrimSpace(op.ResultJSON)
	if resultJSON == "" {
		resultJSON = "{}"
	}
	opID := op.ID
	if opID == "" {
		opID = NewOperationID()
	}
	if _, err := tx.Exec(s.dialect.Bind(`INSERT INTO `+s.dialect.Table("studio_operations")+` (
		id, system_account_id, client_operation_id, kind, asset_id, result_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		opID, op.SystemAccountID, op.ClientOperationID, op.Kind, &assetID,
		resultJSON, op.Now, op.Now); err != nil {
		if isDuplicateOperationError(err) {
			return nil, ErrOperationExists
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAsset(assetID, asset.SystemAccountID)
}

func (s *Store) getOperationByID(id, ownerID string) (*Operation, error) {
	rows, err := s.db.Query(s.dialect.Bind(`SELECT `+OperationColumns+` FROM `+s.dialect.Table("studio_operations")+`
		WHERE id = ? AND system_account_id = ?
		LIMIT 1`), id, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		return scanOperation(rows.Scan)
	}
	return nil, rows.Err()
}

// isDuplicateOperationError 识别 studio_operations 唯一约束冲突（UNIQUE
// (system_account_id, client_operation_id)；PG 具名/SQLite 列序两族）。
func isDuplicateOperationError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "studio_operations.system_account_id, studio_operations.client_operation_id") ||
		strings.Contains(message, "studio_operations_system_account_id_client_operation_id_key")
}
