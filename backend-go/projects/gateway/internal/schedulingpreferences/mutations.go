// Scheduling-preference mutations: guarded create, strict partial patch with
// expectedUpdatedAt optimistic locking (equal-value no-op), and referenced
// delete protection. Mirrors the routestrategies mutation contract adapted to
// the 「调度偏好」 chapter: builtin rows reject every write (403), custom rows
// keep owner-unique names, mode switches carry the config along (speed→cost
// clears the stored JSON), and every committed mutation publishes the global
// gateway-runtime invalidation.
package schedulingpreferences

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Change mirrors the routestrategies patch change record with raw values
// (stringified at the route layer for the operation log).
type Change struct {
	Field  string
	Before any
	After  any
}

// PatchResult is the PATCH response body {id, changedFields, rowPatch,
// updatedAt} plus the log-only fields. rowPatch 只承载变更字段，updatedAt 走
// 顶层字段（契约响应体逐字段的读法）.
type PatchResult struct {
	ID            string         `json:"id"`
	ChangedFields []string       `json:"changedFields"`
	RowPatch      map[string]any `json:"rowPatch"`
	UpdatedAt     string         `json:"updatedAt"`

	OwnerSystemAccountID string   `json:"-"`
	ResourceName         string   `json:"-"`
	Changes              []Change `json:"-"`
}

// DeleteResult mirrors the delete outcome for the operation log.
type DeleteResult struct {
	Deleted              bool
	OwnerSystemAccountID string
	Name                 string
}

// Create mirrors the contract create: owner stamping (admin 面 query 必填
// owner；个人面 owner=会话), strict name/mode validation, speed config via the
// shared normalize, owner-unique name and the created row projection. enabled
// 创建固定为 1、built_in 固定 0.
func (s *Store) Create(ctx context.Context, input MutationInput, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	ownerID, err := access.writeSystemAccountID()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(derefOrEmpty(input.Name))
	if name == "" {
		return nil, &routestrategies.ValidationError{Message: "调度偏好名称不能为空"}
	}
	// 创建 speed_first 未携带配置（缺省或显式 null）按 D2 缺省表补齐，与
	// PATCH 切入路径共用 resolveChatModeConfig 同一分支；显式提交损坏/越界
	// 配置仍由 normalizeSpeedConfig 统一 400。
	chatMode, document, configJSON, _, err := resolveChatModeConfig(nil, input.ChatMode, input.HasConfig, input.ConfigRaw)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	id := s.newI("schpref")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("scheduling_preferences")+`
		(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)`),
		id, ownerID, name, chatMode, nullString(configJSON), now, now); err != nil {
		if duplicate := duplicateNameError(err, name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("scheduling_preference_created")

	item := &ListItem{
		ID:                   id,
		OwnerSystemAccountID: ownerID,
		Name:                 name,
		ChatMode:             chatMode,
		ChatSpeedFirstConfig: document,
		BuiltIn:              false,
		Enabled:              true,
		BoundAPIKeyCount:     0,
		UpdatedAt:            now,
	}
	if access.canAccessAll() {
		item.SystemAccountID = ptrString(ownerID)
	}
	return item, nil
}

// resolveChatModeConfig normalizes the chatMode/config pair. current is the
// stored mode for patches (nil at create). keepCurrent=true 表示「speed_first
// 保持现配置」——调用方沿用行内 configJSON / 投影，不产生赋值。规则：
//   - chatMode 只允许 cost_first/speed_first（创建缺省亦无效）；
//   - cost_first 携带配置 400 成本优先不接受速度参数配置，存储恒 NULL；
//   - speed_first 创建或从 cost_first 切入未携带配置（缺省/显式 null）时按
//     D2 缺省表生成；已处于 speed_first 且未携带配置时保留现配置。
func resolveChatModeConfig(current *string, chatMode *string, hasConfig bool, raw any) (mode string, document *SpeedFirstConfigDocument, stored sql.NullString, keepCurrent bool, err error) {
	if chatMode == nil || (*chatMode != ChatModeCostFirst && *chatMode != ChatModeSpeedFirst) {
		return "", nil, sql.NullString{}, false, &routestrategies.ValidationError{Message: "调度偏好对话形态无效"}
	}
	mode = *chatMode
	if mode == ChatModeCostFirst {
		if hasConfig && raw != nil {
			return "", nil, sql.NullString{}, false, &routestrategies.ValidationError{Message: "成本优先不接受速度参数配置"}
		}
		return mode, nil, sql.NullString{}, false, nil
	}
	if hasConfig && raw != nil {
		document, stored, err = normalizeSpeedConfig(raw)
		if err != nil {
			return "", nil, sql.NullString{}, false, err
		}
		return mode, document, stored, false, nil
	}
	if current != nil && *current == ChatModeSpeedFirst {
		// 已是 speed_first 且未携带配置：保留现配置。
		return mode, nil, sql.NullString{}, true, nil
	}
	// 创建 speed_first 或从 cost_first 切入且未携带配置：按 D2 缺省默认生成。
	document, stored, err = normalizeSpeedConfig(map[string]any{})
	if err != nil {
		return "", nil, sql.NullString{}, false, err
	}
	return mode, document, stored, false, nil
}

// normalizeSpeedConfig reuses the strategy-side normalize (单一默认值来源：
// 九字段缺省、合法范围、旧别名与双字段拒绝、中文校验文案全部同源) and renders
// the stored document + JSON.
func normalizeSpeedConfig(raw any) (*SpeedFirstConfigDocument, sql.NullString, error) {
	record, ok := raw.(map[string]any)
	if !ok {
		return nil, sql.NullString{}, &routestrategies.ValidationError{Message: "速度优先配置无效"}
	}
	normal, err := routestrategies.NormalizeNormalRoutingConfig(map[string]any{
		"schedulingPreference": ChatModeSpeedFirst,
		"firstByteDeadlineMs":  record["firstByteDeadlineMs"],
		"speedFirstConfig":     record["speedFirstConfig"],
	})
	if err != nil {
		return nil, sql.NullString{}, err
	}
	document := newSpeedConfigDocument(normal)
	stored, err := speedConfigJSON(document)
	if err != nil {
		return nil, sql.NullString{}, err
	}
	return document, stored, nil
}

// Patch mirrors the contract patch: expectedUpdatedAt CAS (409 调度偏好已被
// 其他操作更新，请刷新后重试 + currentUpdatedAt), builtin guard (403), strict
// equal-value stripping (同值请求零 DML、不推进 updatedAt、不写日志、不失效)
// and the mode-switch config carry/clear rules. (nil, nil) renders 404
// 调度偏好不存在.
func (s *Store) Patch(ctx context.Context, id string, input MutationInput, expectedUpdatedAt string, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	clause, clauseArgs, ok := preferenceOwnerClause(access, "scheduling_preferences.system_account_id")
	if !ok {
		return nil, nil
	}
	where := ""
	if clause != "" {
		where = " AND " + clause
	}
	lockClause := ""
	if s.pg {
		lockClause = " FOR UPDATE"
	}
	current, err := scanPreferenceRow(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT `+preferenceRowColumns+`
			FROM `+s.table("scheduling_preferences")+` scheduling_preferences
			WHERE scheduling_preferences.id = ?`+where+` LIMIT 1`+lockClause),
			append([]any{id}, clauseArgs...)...).Scan(targets...)
	})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// 内置行任何业务写入口（PATCH、DELETE）一律 403，先于版本判定。
	if current.builtIn {
		return nil, &BuiltinWriteError{Message: "系统内置调度偏好不可修改或删除"}
	}
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	if current.updatedAt != expectedUpdatedAt {
		return nil, &routestrategies.VersionConflictError{Message: "调度偏好已被其他操作更新，请刷新后重试", CurrentUpdatedAt: current.updatedAt}
	}

	currentDocument, err := storedSpeedConfigDocument(current)
	if err != nil {
		return nil, err
	}
	assignments := []string{}
	updateArgs := []any{}
	changes := []Change{}
	rowPatch := map[string]any{}
	addChange := func(field string, before, after any) {
		changes = append(changes, Change{Field: field, Before: before, After: after})
	}

	nextName := current.name
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, &routestrategies.ValidationError{Message: "调度偏好名称不能为空"}
		}
		if name != current.name {
			assignments = append(assignments, "name = ?")
			updateArgs = append(updateArgs, name)
			addChange("name", current.name, name)
			rowPatch["name"] = name
		}
		nextName = name
	}

	// chatMode + config 联动：chatMode 未携带而仅携带配置时，目标形态取现行值
	// （speed_first 替换配置；cost_first 携带配置在 resolve 内拒绝）。
	if input.ChatMode != nil || input.HasConfig {
		modeSource := &current.chatMode
		if input.ChatMode != nil {
			modeSource = input.ChatMode
		}
		nextMode, nextDocument, stored, keepCurrent, resolveErr := resolveChatModeConfig(&current.chatMode, modeSource, input.HasConfig, input.ConfigRaw)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if keepCurrent {
			// speed_first 保持现配置分支：currentDocument 已由存储投影规范化
			// （speed_first 行缺配置在投影处显式上抛），比较与赋值沿用行内值。
			stored = current.configJSON
			nextDocument = currentDocument
		}
		if nextMode != current.chatMode {
			assignments = append(assignments, "chat_mode = ?")
			updateArgs = append(updateArgs, nextMode)
			addChange("chatMode", current.chatMode, nextMode)
			rowPatch["chatMode"] = nextMode
		}
		if !sameNullString(stored, current.configJSON) {
			assignments = append(assignments, "chat_speed_first_config_json = ?")
			updateArgs = append(updateArgs, nullString(stored))
		}
		if !speedConfigDocumentsEqual(currentDocument, nextDocument) {
			addChange("chatSpeedFirstConfig", currentDocument, nextDocument)
			if nextDocument == nil {
				rowPatch["chatSpeedFirstConfig"] = nil
			} else {
				rowPatch["chatSpeedFirstConfig"] = nextDocument
			}
		}
	}

	if input.Enabled != nil && *input.Enabled != current.enabled {
		nextEnabled := 0
		if *input.Enabled {
			nextEnabled = 1
		}
		assignments = append(assignments, "enabled = ?")
		updateArgs = append(updateArgs, nextEnabled)
		addChange("enabled", current.enabled, *input.Enabled)
		rowPatch["enabled"] = *input.Enabled
	}

	if len(assignments) == 0 {
		// 同值 no-op：零 DML、不推进 updatedAt、不写操作日志、不发失效。
		return &PatchResult{
			ID:                   current.id,
			ChangedFields:        []string{},
			RowPatch:             map[string]any{},
			UpdatedAt:            current.updatedAt,
			OwnerSystemAccountID: current.systemAccountID,
			ResourceName:         current.name,
		}, nil
	}

	nextUpdatedAt, err := nextPreferenceUpdatedAt(expectedUpdatedAt, s.now())
	if err != nil {
		return nil, err
	}
	updateArgs = append(updateArgs, nextUpdatedAt)
	updateArgs = append(updateArgs, id, current.systemAccountID, expectedUpdatedAt)
	allAssignments := make([]string, 0, len(assignments)+1)
	allAssignments = append(allAssignments, assignments...)
	allAssignments = append(allAssignments, "updated_at = ?")
	update, updateErr := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("scheduling_preferences")+` SET
		`+strings.Join(allAssignments, ", ")+`
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`), updateArgs...)
	if updateErr != nil {
		if duplicate := duplicateNameError(updateErr, nextName); duplicate != nil {
			return nil, duplicate
		}
		return nil, updateErr
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return nil, &routestrategies.VersionConflictError{Message: "调度偏好已被其他操作更新，请刷新后重试", CurrentUpdatedAt: current.updatedAt}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("scheduling_preference_updated")

	changedFields := make([]string, 0, len(changes))
	for _, change := range changes {
		changedFields = append(changedFields, change.Field)
	}
	return &PatchResult{
		ID:                   current.id,
		ChangedFields:        changedFields,
		RowPatch:             rowPatch,
		UpdatedAt:            nextUpdatedAt,
		OwnerSystemAccountID: current.systemAccountID,
		ResourceName:         nextName,
		Changes:              changes,
	}, nil
}

// speedConfigDocumentsEqual compares normalized documents (JSON stringify，
// 规范化后比较，不比较原始字节).
func speedConfigDocumentsEqual(left, right *SpeedFirstConfigDocument) bool {
	leftJSON, leftErr := speedConfigJSON(left)
	rightJSON, rightErr := speedConfigJSON(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return sameNullString(leftJSON, rightJSON)
}

// Delete mirrors the contract delete: builtin 403, still-bound 409 +
// referenceCount, otherwise hard delete + the global runtime invalidation.
// Deleted=false renders 404 调度偏好不存在.
func (s *Store) Delete(ctx context.Context, id string, access AccessScope) (*DeleteResult, error) {
	ctx = ensureCtx(ctx)
	var name string
	var builtIn int
	// owner 走 NullString：内置行 system_account_id 恒 NULL。
	var ownerID sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT system_account_id, name, built_in FROM `+s.table("scheduling_preferences")+`
		WHERE id = ? LIMIT 1`), id).Scan(&ownerID, &name, &builtIn)
	if isNoRows(err) {
		return &DeleteResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	// 内置行是全局只读例外：任何 caller（含 self 面可见者）的写入口一律 403，
	// 先于 owner 可管理性判定（可见行不得伪装成 404）。
	if builtIn == 1 {
		return nil, &BuiltinWriteError{Message: "系统内置调度偏好不可修改或删除"}
	}
	if !access.canManageOwner(ownerID.String) {
		return &DeleteResult{}, nil
	}
	owner := ownerID.String
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if s.pg {
		var lockedID string
		lockErr := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("scheduling_preferences")+`
			WHERE id = ? AND system_account_id = ? LIMIT 1 FOR UPDATE`), id, owner).Scan(&lockedID)
		if isNoRows(lockErr) {
			return &DeleteResult{}, nil
		}
		if lockErr != nil {
			return nil, lockErr
		}
	}
	var referenceCount int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(1) FROM `+s.table("api_keys")+`
		WHERE scheduling_preference_id = ?`), id).Scan(&referenceCount); err != nil {
		return nil, err
	}
	if referenceCount > 0 {
		return nil, &ReferenceConflictError{Message: "调度偏好仍被 API Key 绑定，不能删除", ReferenceCount: referenceCount}
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("scheduling_preferences")+`
		WHERE id = ? AND system_account_id = ?`), id, owner)
	if err != nil {
		return nil, err
	}
	deleted, _ := result.RowsAffected()
	if deleted == 0 {
		return &DeleteResult{}, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("scheduling_preference_deleted")
	return &DeleteResult{Deleted: true, OwnerSystemAccountID: owner, Name: name}, nil
}

// nextPreferenceUpdatedAt mirrors nextStrategyUpdatedAt: monotonic RFC3339
// millis (never equal to the expected version).
func nextPreferenceUpdatedAt(current string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", &routestrategies.ValidationError{Message: "调度偏好 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + current}
	}
	next := now.UnixMilli()
	if floor := parsed.UnixMilli() + 1; next < floor {
		next = floor
	}
	return isoMillis(time.UnixMilli(next)), nil
}

// duplicateNameError mirrors the routestrategies probe over the preference
// unique indexes (owner 内精确 + lower 归一双唯一索引，均挂 WHERE built_in=0；
// lower 索引命中时消息同样含 idx_scheduling_preferences_owner_name_unique
// 前缀——SQLite 报告的是索引名本身).
func duplicateNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_scheduling_preferences_owner_name_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: scheduling_preferences.system_account_id, scheduling_preferences.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.scheduling_preferences.system_account_id, juhe_business.scheduling_preferences.name") {
		return &routestrategies.ConflictError{Message: "调度偏好名称已存在：" + name}
	}
	return nil
}
