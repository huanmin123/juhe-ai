// Security-policy mutations: guarded create, strict partial patch with
// expectedUpdatedAt optimistic locking (equal-value no-op), and always-success
// delete. Mirrors the schedulingpreferences mutation contract adapted to the
// 「安全策略」章 (API Key 安全策略与出站内容脱敏设计 §4.3)：builtin rows reject
// every write (403), custom rows keep owner-unique names, rules changes store
// the normalized document, and every committed mutation publishes the global
// gateway-runtime invalidation (绑定 Key 的出站脱敏快照随之重建).
package securitypolicies

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

// Change mirrors the schedulingpreferences patch change record with raw values
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

// DeleteResult is the delete outcome: the operation log fields plus
// resetBindings（同一事务内被重置回内置默认的绑定 Key 数，响应 data 原样透出）.
type DeleteResult struct {
	Deleted              bool
	OwnerSystemAccountID string
	Name                 string
	ResetBindings        int
}

// Create mirrors the contract create: owner stamping (admin 面 query 必填
// owner；个人面 owner=会话), strict name validation, rules via the shared
// normalize（缺省 / 显式 null 落缺省空规则集）、owner-unique name and the
// created row projection. built_in 固定 0；description 可空（缺省 / null / 空
// 串折算 NULL）。
func (s *Store) Create(ctx context.Context, input MutationInput, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	ownerID, err := access.writeSystemAccountID()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(derefOrEmpty(input.Name))
	if name == "" {
		return nil, &routestrategies.ValidationError{Message: "安全策略名称不能为空"}
	}
	description := sql.NullString{}
	if input.HasDescription && input.Description != nil {
		if trimmed := strings.TrimSpace(*input.Description); trimmed != "" {
			description = sql.NullString{String: trimmed, Valid: true}
		}
	}
	_, rulesJSON, err := normalizeRulesInput(input.RulesRaw)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	id := s.newI("secpol")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("security_policies")+`
		(id, system_account_id, name, description, rules_json, built_in, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)`),
		id, ownerID, name, nullString(description), rulesJSON, now, now); err != nil {
		if duplicate := duplicateNameError(err, name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("security_policy_created")

	document, err := parseStoredRulesDocument(rulesJSON)
	if err != nil {
		return nil, err
	}
	item := &ListItem{
		ID:                   id,
		OwnerSystemAccountID: ownerID,
		Name:                 name,
		Description:          nullPtrString(description),
		Rules:                document.Rules,
		BuiltIn:              false,
		BoundAPIKeyCount:     0,
		UpdatedAt:            now,
	}
	if access.canAccessAll() {
		item.SystemAccountID = ptrString(ownerID)
	}
	return item, nil
}

// Patch mirrors the contract patch: expectedUpdatedAt CAS (409 安全策略已被其
// 他操作更新，请刷新后重试 + currentUpdatedAt), builtin guard (403), strict
// equal-value stripping (同值请求零 DML、不推进 updatedAt、不写日志、不失效)
// and the rules whitelist. (nil, nil) renders 404 安全策略不存在.
func (s *Store) Patch(ctx context.Context, id string, input MutationInput, expectedUpdatedAt string, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	clause, clauseArgs, ok := policyOwnerClause(access, "security_policies.system_account_id")
	if !ok {
		return nil, nil
	}
	where := ""
	if clause != "" {
		where = " AND " + clause
	}
	lockClause := " FOR UPDATE"
	current, err := scanPolicyRow(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT `+policyRowColumns+`
			FROM `+s.table("security_policies")+` security_policies
			WHERE security_policies.id = ?`+where+` LIMIT 1`+lockClause),
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
		return nil, &BuiltinWriteError{Message: "系统内置安全策略不可修改或删除"}
	}
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	if !dbtime.SameInstant(current.updatedAt, expectedUpdatedAt) {
		return nil, &routestrategies.VersionConflictError{Message: "安全策略已被其他操作更新，请刷新后重试", CurrentUpdatedAt: current.updatedAt}
	}

	currentRulesJSON, err := storedRulesJSON(current)
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
			return nil, &routestrategies.ValidationError{Message: "安全策略名称不能为空"}
		}
		if name != current.name {
			assignments = append(assignments, "name = ?")
			updateArgs = append(updateArgs, name)
			addChange("name", current.name, name)
			rowPatch["name"] = name
		}
		nextName = name
	}

	if input.HasDescription {
		nextDescription := sql.NullString{}
		if input.Description != nil {
			if trimmed := strings.TrimSpace(*input.Description); trimmed != "" {
				nextDescription = sql.NullString{String: trimmed, Valid: true}
			}
		}
		if !sameNullString(nextDescription, current.description) {
			assignments = append(assignments, "description = ?")
			updateArgs = append(updateArgs, nullString(nextDescription))
			addChange("description", nullStringValue(current.description), nullStringValue(nextDescription))
			rowPatch["description"] = nullStringValue(nextDescription)
		}
	}

	if input.HasRules {
		_, rulesJSON, rulesErr := normalizeRulesInput(input.RulesRaw)
		if rulesErr != nil {
			return nil, rulesErr
		}
		if rulesJSON != currentRulesJSON {
			document, docErr := parseStoredRulesDocument(rulesJSON)
			if docErr != nil {
				return nil, docErr
			}
			assignments = append(assignments, "rules_json = ?")
			updateArgs = append(updateArgs, rulesJSON)
			addChange("rules", currentRulesJSON, rulesJSON)
			rowPatch["rules"] = document.Rules
		}
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

	nextUpdatedAt, err := nextPolicyUpdatedAt(expectedUpdatedAt, s.now())
	if err != nil {
		return nil, err
	}
	updateArgs = append(updateArgs, nextUpdatedAt)
	updateArgs = append(updateArgs, id, current.systemAccountID, expectedUpdatedAt)
	allAssignments := make([]string, 0, len(assignments)+1)
	allAssignments = append(allAssignments, assignments...)
	allAssignments = append(allAssignments, "updated_at = ?")
	update, updateErr := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("security_policies")+` SET
		`+strings.Join(allAssignments, ", ")+`
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`), updateArgs...)
	if updateErr != nil {
		if duplicate := duplicateNameError(updateErr, nextName); duplicate != nil {
			return nil, duplicate
		}
		return nil, updateErr
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return nil, &routestrategies.VersionConflictError{Message: "安全策略已被其他操作更新，请刷新后重试", CurrentUpdatedAt: current.updatedAt}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("security_policy_updated")

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

// Delete mirrors the schedulingpreferences delete contract（设计 §4.3）：删除
// 总是成功——同一事务内先把绑定该策略的 API Key 的 security_policy_id 重置为
// 内置默认 ID（等价「显式回默认」；绑定 Key 必与策略同 owner，写入口矩阵天
// 然成立），再删策略行，然后计数随 DeleteResult 透出供操作日志与响应体使用；
// 数据库侧 NO ACTION 保留为兜底（先重置绑定再删行）。内置行 403 不变；
// Deleted=false 渲染 404 安全策略不存在。提交后照常发布全局 runtime 失效
// （重置与删除都改变绑定 Key 的有效脱敏行为来源）。
func (s *Store) Delete(ctx context.Context, id string, access AccessScope) (*DeleteResult, error) {
	ctx = ensureCtx(ctx)
	var name string
	var builtIn int
	// owner 走 NullString：内置行 system_account_id 恒 NULL。
	var ownerID sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT system_account_id, name, built_in FROM `+s.table("security_policies")+`
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
		return nil, &BuiltinWriteError{Message: "系统内置安全策略不可修改或删除"}
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
	var lockedID string
	lockErr := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("security_policies")+`
		WHERE id = ? AND system_account_id = ? LIMIT 1 FOR UPDATE`), id, owner).Scan(&lockedID)
	if isNoRows(lockErr) {
		return &DeleteResult{}, nil
	}
	if lockErr != nil {
		return nil, lockErr
	}
	// 先重置绑定回内置默认（固定 ID 契约锚点），再删策略行：两步同事务，PG
	// NO ACTION 满足「删行时无引用」。
	reset, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("api_keys")+` SET security_policy_id = ?
		WHERE security_policy_id = ?`), BuiltinSecurityPolicyID, id)
	if err != nil {
		return nil, err
	}
	resetBindings, err := reset.RowsAffected()
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("security_policies")+`
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
	s.invalidateRuntime("security_policy_deleted")
	return &DeleteResult{Deleted: true, OwnerSystemAccountID: owner, Name: name, ResetBindings: int(resetBindings)}, nil
}

// nextPolicyUpdatedAt mirrors nextPreferenceUpdatedAt: monotonic RFC3339
// millis (never equal to the expected version).
func nextPolicyUpdatedAt(current string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", &routestrategies.ValidationError{Message: "安全策略 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + current}
	}
	next := now.UnixMilli()
	if floor := parsed.UnixMilli() + 1; next < floor {
		next = floor
	}
	return isoMillis(time.UnixMilli(next)), nil
}

// duplicateNameError mirrors the schedulingpreferences probe over the policy
// unique indexes (owner 内精确 + lower 归一双唯一索引，均挂 WHERE built_in=0；
// lower 索引命中时消息同样含 idx_security_policies_owner_name_unique 前缀).
func duplicateNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_security_policies_owner_name_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: security_policies.system_account_id, security_policies.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.security_policies.system_account_id, juhe_business.security_policies.name") {
		return &routestrategies.ConflictError{Message: "安全策略名称已存在：" + name}
	}
	return nil
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
