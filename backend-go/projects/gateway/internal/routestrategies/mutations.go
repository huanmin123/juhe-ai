// Route-strategy mutations: create, strict partial patch with optimistic
// locking, and guarded delete. Mirrors createRouteStrategyAsync,
// patchRouteStrategyAsync and deleteRouteStrategyAsync in
// storage/route-strategy.repository.ts.
package routestrategies

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BindingInput is one normalized group binding entry from the request.
type BindingInput struct {
	GroupID  string
	Priority *int // nil = default (index+1)
	Weight   *int // nil = 1
	Status   string

	// presence flags mirror hasOwnInput over the parsed request fields so the
	// create audit log renders only what the caller actually sent (Node logs
	// parsed.data.groupBindings verbatim). Store-level constructors leave them
	// false; normalizeBindings never reads them.
	priorityProvided bool
	weightProvided   bool
	statusProvided   bool
}

// MutationInput is the create/patch payload; pointers/flags mirror
// hasOwnInput semantics (absent vs explicit null vs value).
type MutationInput struct {
	Name *string

	Description    *string // nil = absent or explicit null → stores NULL
	HasDescription bool

	Mode   *string
	Status *string

	Bindings    []BindingInput
	HasBindings bool

	NormalConfigRaw any
	HasNormalConfig bool
	HybridConfigRaw any
	HasHybridConfig bool
}

// Empty reports whether no patchable field is present (Node refine:
// 请提供要修改的策略路由内容).
func (m MutationInput) Empty() bool {
	return m.Name == nil && !m.HasDescription && m.Mode == nil && m.Status == nil &&
		!m.HasBindings && !m.HasNormalConfig && !m.HasHybridConfig
}

// Change mirrors RouteStrategyPatchChange with raw values (stringified at the
// route layer for the operation log).
type Change struct {
	Field  string
	Before any
	After  any
}

// PatchResult mirrors RouteStrategyPatchResult — the PATCH response body is
// ok(mutation.result): {id, changedFields, rowPatch}.
type PatchResult struct {
	ID                   string         `json:"id"`
	ChangedFields        []string       `json:"changedFields"`
	RowPatch             map[string]any `json:"rowPatch"`
	OwnerSystemAccountID string         `json:"-"`
	ResourceName         string         `json:"-"`
	Changes              []Change       `json:"-"`
}

// Create mirrors createRouteStrategyAsync: owner stamping, mode/config
// validation, transactional binding normalization and replace, owner-scoped
// unique name, gateway invalidation and the created RouteStrategyListItem.
func (s *Store) Create(ctx context.Context, input MutationInput, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	ownerID, err := access.writeSystemAccountID()
	if err != nil {
		return nil, err
	}
	mode, err := normalizeMode(input.Mode)
	if err != nil {
		return nil, err
	}
	if !input.HasBindings || len(input.Bindings) == 0 {
		return nil, &ValidationError{Message: "策略路由至少需要绑定一个分组"}
	}
	normalConfig, hybridConfig, err := normalizeConfigForWrite(input.NormalConfigRaw, input.HybridConfigRaw, mode)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(derefOrEmpty(input.Name))
	if name == "" {
		return nil, &ValidationError{Message: "策略路由名称不能为空"}
	}
	description, err := nullableDescription(input)
	if err != nil {
		return nil, err
	}
	status, err := normalizeStatus(input.Status, "active")
	if err != nil {
		return nil, err
	}
	configJSON := routeStrategyConfigJSON(normalConfig, hybridConfig)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Node createRouteStrategyAsync: normalizeRouteStrategyGroupBindingsAsync(
	// ..., lockRows=true) — PostgreSQL locks the group rows inside the write
	// transaction.
	bindings, err := s.normalizeBindings(ctx, tx, input.Bindings, ownerID, true)
	if err != nil {
		return nil, err
	}
	if err := validateModeBindings(mode, bindings); err != nil {
		return nil, err
	}
	now := s.nowISO()
	id := s.newI("route_strategy")
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("route_strategies")+`
		(id, system_account_id, name, description, mode, status, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, ownerID, name, description, mode, status, nullString(configJSON), now, now); err != nil {
		if duplicate := duplicateNameError(err, name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	written, err := s.replaceBindings(ctx, tx, id, ownerID, mode, bindings, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("route_strategy_created")

	item := &ListItem{
		ID:                   id,
		OwnerSystemAccountID: ownerID,
		Name:                 name,
		Description:          description,
		Mode:                 mode,
		Status:               status,
		IsDefault:            false,
		NormalRoutingConfig:  normalConfigForMode(mode, normalConfig),
		BindingCount:         len(written),
		APIKeyCount:          0,
		GroupBindingPreview:  previewsFromBindings(written, 3),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if access.canAccessAll() {
		item.SystemAccountID = ptrString(ownerID)
	}
	return item, nil
}

// nullableDescription mirrors normalizeNullableTextInput: trimmed value or
// NULL (explicit null and blank both store NULL); length follows the zod
// max(200) bound counted in UTF-16 code units (JavaScript String.length).
func nullableDescription(input MutationInput) (*string, error) {
	if !input.HasDescription || input.Description == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*input.Description)
	if utf16CodeUnits(trimmed) > 200 {
		return nil, &ValidationError{Message: "策略路由说明不能超过 200 个字符"}
	}
	if trimmed == "" {
		return nil, nil
	}
	return &trimmed, nil
}

// Patch mirrors patchRouteStrategyAsync: expectedUpdatedAt optimistic lock
// (409 with currentUpdatedAt), strict partial update, mode-specific binding
// validation, whole-set binding replacement and the gateway invalidation
// hook. (nil, nil) renders 404 策略路由不存在.
func (s *Store) Patch(ctx context.Context, id string, input MutationInput, expectedUpdatedAt string, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	managedOwner := access.manageableID()
	conflict := func() (*PatchResult, error) {
		currentUpdatedAt, lookupErr := s.currentUpdatedAtForAccess(ctx, tx, id, managedOwner)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if currentUpdatedAt != "" {
			return nil, &VersionConflictError{Message: "策略路由已被其他操作更新，请刷新后重试", CurrentUpdatedAt: currentUpdatedAt}
		}
		return nil, nil
	}

	where := " AND route_strategies.updated_at = ?"
	args := []any{id, expectedUpdatedAt}
	if managedOwner != "" {
		where += " AND route_strategies.system_account_id = ?"
		args = append(args, managedOwner)
	}
	// loadLockedRouteStrategyPatchCurrentAsync: PostgreSQL locks the strategy
	// row for the duration of the patch transaction.
	lockClause := ""
	if s.pg {
		lockClause = " FOR UPDATE"
	}
	current, err := scanStrategyRow(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT `+strategyRowColumns+`
			FROM `+s.table("route_strategies")+` route_strategies
			WHERE route_strategies.id = ?`+where+` LIMIT 1`+lockClause), args...).Scan(targets...)
	}, false)
	if isNoRows(err) {
		return conflict()
	}
	if err != nil {
		return nil, err
	}
	if !access.canManageOwner(current.systemAccountID) {
		return conflict()
	}

	currentNormal, currentHybrid, err := parseStoredConfig(current.configJSON)
	if err != nil {
		return nil, err
	}
	mode := current.mode
	if input.Mode != nil {
		mode, err = normalizeMode(input.Mode)
		if err != nil {
			return nil, err
		}
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
			return nil, &ValidationError{Message: "策略路由名称不能为空"}
		}
		// assertRouteStrategyNameChangeAllowed.
		if current.isDefault && name != current.name {
			return nil, &ValidationError{Message: "默认策略路由不允许修改名称"}
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
		description, descErr := nullableDescription(input)
		if descErr != nil {
			return nil, descErr
		}
		if !sameNullableText(current.description, description) {
			assignments = append(assignments, "description = ?")
			updateArgs = append(updateArgs, description)
			addChange("description", nullText(current.description), description)
			rowPatch["description"] = description
		}
	}
	if mode != current.mode {
		assignments = append(assignments, "mode = ?")
		updateArgs = append(updateArgs, mode)
		addChange("mode", current.mode, mode)
		rowPatch["mode"] = mode
	}
	if input.Status != nil {
		status, statusErr := normalizeStatus(input.Status, current.status)
		if statusErr != nil {
			return nil, statusErr
		}
		if status != current.status {
			assignments = append(assignments, "status = ?")
			updateArgs = append(updateArgs, status)
			addChange("status", current.status, status)
			rowPatch["status"] = status
		}
	}

	// Config recompute when mode or either config is present (Node
	// routeStrategyScalarPatch config block).
	if input.Mode != nil || input.HasNormalConfig || input.HasHybridConfig {
		normalInput := input.normalInput(mode, currentNormal)
		hybridInput := input.hybridInput(mode, currentHybrid)
		nextNormal, nextHybrid, configErr := normalizeConfigForWrite(normalInput, hybridInput, mode)
		if configErr != nil {
			return nil, configErr
		}
		nextJSON := routeStrategyConfigJSON(nextNormal, nextHybrid)
		currentJSON, jsonErr := routeStrategyConfigJSONFromRaw(
			rawForMode(current.mode, ModeNormal, currentNormal),
			rawForMode(current.mode, ModeHybridSmart, currentHybrid))
		if jsonErr != nil {
			return nil, jsonErr
		}
		if !sameNullString(nextJSON, currentJSON) {
			assignments = append(assignments, "config_json = ?")
			updateArgs = append(updateArgs, nullString(nextJSON))
		}
		currentNormalForMode := normalConfigForMode(current.mode, currentNormal)
		nextNormalForMode := normalConfigForMode(mode, nextNormal)
		if !configValuesEqual(currentNormalForMode, nextNormalForMode) {
			addChange("normalRoutingConfig", currentNormalForMode, nextNormalForMode)
			if nextNormalForMode == nil {
				rowPatch["normalRoutingConfig"] = nil
			} else {
				rowPatch["normalRoutingConfig"] = nextNormalForMode
			}
		}
		currentHybridForMode := hybridConfigForMode(current.mode, currentHybrid)
		nextHybridForMode := hybridConfigForMode(mode, nextHybrid)
		if !configValuesEqual(currentHybridForMode, nextHybridForMode) {
			addChange("hybridRoutingConfig", currentHybridForMode, nextHybridForMode)
			if nextHybridForMode == nil {
				rowPatch["hybridRoutingConfig"] = nil
			} else {
				rowPatch["hybridRoutingConfig"] = nextHybridForMode
			}
		}
	}

	// Bindings: whole-set replacement when present; a bare mode switch must
	// leave the current set valid for the new mode.
	var writtenBindings []GroupBinding
	bindingsChanged := false
	var beforeBindings []GroupBinding
	if input.HasBindings || input.Mode != nil {
			currentBindings, loadErr := s.loadBindings(ctx, tx, []string{id})
			if loadErr != nil {
				return nil, loadErr
			}
			beforeBindings = currentBindings[id]
			currentWrites := bindingWritesFromSummaries(beforeBindings)
			if input.HasBindings {
				normalized, normalizeErr := s.normalizeBindings(ctx, tx, input.Bindings, current.systemAccountID, true)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			if err := validateModeBindings(mode, normalized); err != nil {
				return nil, err
			}
			bindingsChanged = !bindingWritesEqual(normalized, currentWrites)
			if bindingsChanged {
				writtenBindings, err = s.reconcileBindings(ctx, tx, id, current.systemAccountID, mode, beforeBindings, normalized, s.nowISO())
				if err != nil {
					return nil, err
				}
			}
		} else if mode != current.mode {
			if err := validateModeBindings(mode, currentWrites); err != nil {
				return nil, err
			}
		}
	}

	if len(assignments) > 0 || bindingsChanged {
		nextUpdatedAt, updatedErr := nextStrategyUpdatedAt(expectedUpdatedAt, s.now())
		if updatedErr != nil {
			return nil, updatedErr
		}
		updateArgs = append(updateArgs, nextUpdatedAt)
		updateArgs = append(updateArgs, id, current.systemAccountID, expectedUpdatedAt)
		// Node prepends updated_at to the assignment list so a binding-only
		// patch still renders `SET updated_at = ?`.
		allAssignments := make([]string, 0, len(assignments)+1)
		allAssignments = append(allAssignments, assignments...)
		allAssignments = append(allAssignments, "updated_at = ?")
		update, updateErr := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("route_strategies")+` SET
			`+strings.Join(allAssignments, ", ")+`
			WHERE id = ? AND system_account_id = ? AND updated_at = ?`), updateArgs...)
		if updateErr != nil {
			if duplicate := duplicateNameError(updateErr, nextName); duplicate != nil {
				return nil, duplicate
			}
			return nil, updateErr
		}
		if affected, _ := update.RowsAffected(); affected != 1 {
			currentUpdatedAt, lookupErr := s.currentUpdatedAtForAccess(ctx, tx, id, current.systemAccountID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			return nil, &VersionConflictError{Message: "策略路由已被其他操作更新，请刷新后重试", CurrentUpdatedAt: currentUpdatedAt}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if bindingsChanged {
			changes = append(changes, Change{Field: "groupBindings", Before: beforeBindings, After: writtenBindings})
			rowPatch["bindingCount"] = len(writtenBindings)
			rowPatch["groupBindingPreview"] = previewsFromBindings(writtenBindings, 3)
		}
		if len(changes) > 0 {
			rowPatch["updatedAt"] = nextUpdatedAt
		}
	}

	changedFields := make([]string, 0, len(changes))
	for _, change := range changes {
		changedFields = append(changedFields, change.Field)
	}
	if gatewayRuntimeChanged(changedFields) {
		s.invalidateRuntime("route_strategy_updated")
		// patchRouteStrategyAsync: the API-Key validation cache follows the
		// runtime invalidation; its failure fails the PATCH (500 at the
		// route, after the row is committed).
		if err := s.invalidateValidationCache("route_strategy_updated"); err != nil {
			return nil, err
		}
	}
	return &PatchResult{
		ID:                   current.id,
		ChangedFields:        changedFields,
		RowPatch:             rowPatch,
		OwnerSystemAccountID: current.systemAccountID,
		ResourceName:         nextName,
		Changes:              changes,
	}, nil
}

// normalInput mirrors the config recompute input selection: the current
// config feeds forward when the mode keeps it and no new value arrived.
func (m MutationInput) normalInput(mode string, currentNormal *NormalRoutingConfig) any {
	if mode != ModeNormal {
		if m.HasNormalConfig {
			return m.NormalConfigRaw
		}
		return nil
	}
	if m.HasNormalConfig {
		return m.NormalConfigRaw
	}
	return typedToRaw(currentNormal)
}

func (m MutationInput) hybridInput(mode string, currentHybrid *HybridRoutingConfig) any {
	if mode != ModeHybridSmart {
		if m.HasHybridConfig {
			return m.HybridConfigRaw
		}
		return nil
	}
	if m.HasHybridConfig {
		return m.HybridConfigRaw
	}
	return typedToRaw(currentHybrid)
}

// rawForMode passes the current config raw through only when the stored mode
// owns it (routeStrategyConfigJson current projection).
func rawForMode(rowMode string, wantedMode string, value any) any {
	if rowMode != wantedMode {
		return nil
	}
	return typedToRaw(value)
}

func typedToRaw(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	_ = json.Unmarshal(encoded, &decoded)
	return decoded
}

// routeStrategyConfigJSONFromRaw renders the stored JSON shape from raw
// config maps (re-normalized like the source does).
func routeStrategyConfigJSONFromRaw(normalRaw, hybridRaw any) (sql.NullString, error) {
	normal, err := normalizeNormalRoutingConfig(normalRaw)
	if err != nil {
		return sql.NullString{}, err
	}
	hybrid := (*HybridRoutingConfig)(nil)
	if hybridRaw != nil {
		hybrid, err = normalizeHybridRoutingConfig(hybridRaw)
		if err != nil {
			return sql.NullString{}, err
		}
	}
	return routeStrategyConfigJSON(normal, hybrid), nil
}

// gatewayRuntimeChanged mirrors routeStrategyGatewayRuntimeChanged.
func gatewayRuntimeChanged(fields []string) bool {
	for _, field := range fields {
		switch field {
		case "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig":
			return true
		}
	}
	return false
}

// DeleteResult mirrors the delete outcome for the operation log.
type DeleteResult struct {
	Deleted              bool
	OwnerSystemAccountID string
	Name                 string
}

// Delete mirrors deleteRouteStrategyAsync: owner visibility via
// canManageApiKeyOwner on the unscoped owner lookup, default-strategy and
// API-Key-reference protection, hard delete (bindings cascade via FK),
// gateway invalidation. Deleted=false renders 404 策略路由不存在.
func (s *Store) Delete(ctx context.Context, id string, access AccessScope) (*DeleteResult, error) {
	ctx = ensureCtx(ctx)
	var ownerID string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("route_strategies")+`
		WHERE id = ? LIMIT 1`), id).Scan(&ownerID)
	if isNoRows(err) {
		return &DeleteResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canManageOwner(ownerID) {
		return &DeleteResult{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// lockRouteStrategyMutationRowAsync: PostgreSQL pins the strategy row
	// before the delete guards run.
	if s.pg {
		var lockedID string
		lockErr := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("route_strategies")+`
			WHERE id = ? AND system_account_id = ? LIMIT 1 FOR UPDATE`), id, ownerID).Scan(&lockedID)
		if isNoRows(lockErr) {
			return &DeleteResult{}, nil
		}
		if lockErr != nil {
			return nil, lockErr
		}
	}
	var isDefault int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT is_default FROM `+s.table("route_strategies")+`
		WHERE id = ? AND system_account_id = ? LIMIT 1`), id, ownerID).Scan(&isDefault)
	if isNoRows(err) {
		return &DeleteResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	if isDefault == 1 {
		return nil, &ValidationError{Message: "默认策略路由不允许删除"}
	}
	var apiKeyCount int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(1) FROM `+s.table("api_keys")+`
		WHERE route_strategy_id = ? AND system_account_id = ?`), id, ownerID).Scan(&apiKeyCount); err != nil {
		return nil, err
	}
	if apiKeyCount > 0 {
		return nil, &ValidationError{Message: fmt.Sprintf("策略路由已被 %d 个 API Key 使用，请先解绑", apiKeyCount)}
	}
	// Explicit cascade (route_strategy_groups FK ON DELETE CASCADE in the
	// Node schema; Go does not assume the pragma is on).
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("route_strategy_groups")+`
		WHERE route_strategy_id = ?`), id); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("route_strategies")+`
		WHERE id = ? AND system_account_id = ?`), id, ownerID)
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
	s.invalidateRuntime("route_strategy_deleted")
	return &DeleteResult{Deleted: true, OwnerSystemAccountID: ownerID}, nil
}

// currentUpdatedAtForAccess mirrors routeStrategyCurrentUpdatedAtForAccess.
func (s *Store) currentUpdatedAtForAccess(ctx context.Context, q queryer, id, managedOwner string) (string, error) {
	query := `SELECT updated_at FROM ` + s.table("route_strategies") + ` WHERE id = ?`
	args := []any{id}
	if managedOwner != "" {
		query += ` AND system_account_id = ?`
		args = append(args, managedOwner)
	}
	var updatedAt string
	err := q.QueryRowContext(ctx, s.bind(query+` LIMIT 1`), args...).Scan(&updatedAt)
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return updatedAt, nil
}

// nextStrategyUpdatedAt mirrors nextRouteStrategyUpdatedAt: monotonic
// RFC3339 millis (never equal to the expected version).
func nextStrategyUpdatedAt(current string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", &ValidationError{Message: "策略路由 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + current}
	}
	next := now.UnixMilli()
	if floor := parsed.UnixMilli() + 1; next < floor {
		next = floor
	}
	return isoMillis(time.UnixMilli(next)), nil
}

// duplicateNameError mirrors isDuplicateRouteStrategyNameError + the Node
// message wrapper (策略路由名称已存在：name → 409 via the 已存在 probe).
func duplicateNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_route_strategies_owner_name_unique") ||
		strings.Contains(message, "idx_route_strategies_owner_name_unique_lower") ||
		strings.Contains(message, "UNIQUE constraint failed: route_strategies.system_account_id, route_strategies.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.route_strategies.system_account_id, juhe_business.route_strategies.name") {
		return &ConflictError{Message: "策略路由名称已存在：" + name}
	}
	return nil
}
