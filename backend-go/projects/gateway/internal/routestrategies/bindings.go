// Group-binding persistence and validation: bindable-group lookup (owner
// branch of loadRouteStrategyBindableGroups), request normalization basics,
// per-mode binding rules, replace/reconcile writers and the presentation
// ordering. Mirrors storage/route-strategy.repository.ts binding helpers.
package routestrategies

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// queryer abstracts *sql.DB / *sql.Tx so the transactional paths never touch
// s.db while a transaction holds the connection (the SQLite test runtime runs
// with MaxOpenConns(1)).
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// bindingWrite is the normalized write shape of one group binding.
type bindingWrite struct {
	groupID      string
	groupName    *string
	providerCode string
	priority     int
	weight       int
	status       string
	groupEnabled bool
}

// bindingRow is the raw route_strategy_groups scan target.
type bindingRow struct {
	id              string
	routeStrategyID string
	groupID         string
	priority        int
	weight          int
	status          string
	groupName       sql.NullString
	providerCode    sql.NullString
	groupEnabled    bool
}

func (r bindingRow) summary() GroupBinding {
	return GroupBinding{
		ID:           r.id,
		GroupID:      r.groupID,
		GroupName:    nullPtrString(r.groupName),
		ProviderCode: nullPtrString(r.providerCode),
		Priority:     r.priority,
		Weight:       r.weight,
		Status:       r.status,
		GroupEnabled: r.groupEnabled,
	}
}

const bindingRowColumns = `route_strategy_groups.id,
	route_strategy_groups.route_strategy_id,
	route_strategy_groups.group_id,
	route_strategy_groups.priority,
	route_strategy_groups.weight,
	route_strategy_groups.status,
	groups.name AS group_name,
	groups.provider_code,
	CASE
		WHEN groups.id IS NULL THEN 0
		WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN groups.enabled
		ELSE 0
	END AS group_enabled`

const bindingRowOrder = `ORDER BY route_strategy_groups.route_strategy_id ASC,
		CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
		route_strategy_groups.priority ASC,
		route_strategy_groups.created_at ASC,
		route_strategy_groups.id ASC`

func scanBindingRow(scan func(...any) error) (bindingRow, error) {
	row := bindingRow{}
	var groupEnabled int
	err := scan(&row.id, &row.routeStrategyID, &row.groupID, &row.priority, &row.weight, &row.status,
		&row.groupName, &row.providerCode, &groupEnabled)
	if err != nil {
		return bindingRow{}, err
	}
	row.groupEnabled = groupEnabled == 1
	// appendRouteStrategyBindingRows integrity guards.
	if row.priority <= 0 {
		return bindingRow{}, &ValidationError{Message: "策略路由分组绑定优先级无效：" + row.id}
	}
	if row.status != "active" && row.status != "disabled" {
		return bindingRow{}, &ValidationError{Message: "策略路由分组绑定状态无效：" + row.id}
	}
	if groupEnabled != 0 && groupEnabled != 1 {
		return bindingRow{}, &ValidationError{Message: "策略路由分组绑定关联分组状态无效：" + row.id}
	}
	return row, nil
}

// loadBindings mirrors loadRouteStrategyGroupBindingSummariesByRouteStrategyIds
// (owner branch): presentation-ordered summaries grouped by strategy id.
func (s *Store) loadBindings(ctx context.Context, q queryer, strategyIDs []string) (map[string][]GroupBinding, error) {
	result := map[string][]GroupBinding{}
	unique := uniqueStrings(strategyIDs)
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT `+bindingRowColumns+`
		FROM `+s.table("route_strategy_groups")+` route_strategy_groups
		LEFT JOIN `+s.table("groups")+` groups ON groups.id = route_strategy_groups.group_id
		WHERE route_strategy_groups.route_strategy_id IN (`+strings.Join(placeholders, ",")+`)
		`+bindingRowOrder), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row, scanErr := scanBindingRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		result[row.routeStrategyID] = append(result[row.routeStrategyID], row.summary())
	}
	return result, rows.Err()
}

// normalizeBindings mirrors normalizeRouteStrategyGroupBindings: uniqueness,
// active-priority uniqueness, at-least-one-active, bindable-group boundary
// (owner branch; authorized-grantee bindings belong to the authorization
// slice) and disabled-group activation guard. Result is sorted by
// (priority, groupId) like the source.
func (s *Store) normalizeBindings(ctx context.Context, q queryer, inputs []BindingInput, ownerID string) ([]bindingWrite, error) {
	if len(inputs) == 0 {
		return nil, &ValidationError{Message: "策略路由至少需要绑定一个分组"}
	}
	if len(inputs) > maxRouteStrategyGroupBindings {
		return nil, &ValidationError{Message: "策略路由最多绑定 " + itoa(maxRouteStrategyGroupBindings) + " 个分组"}
	}
	seenGroupIDs := map[string]bool{}
	activePriorities := map[int]bool{}
	for _, input := range inputs {
		groupID := strings.TrimSpace(input.GroupID)
		if groupID == "" {
			return nil, &ValidationError{Message: "策略路由分组无效"}
		}
		if seenGroupIDs[groupID] {
			return nil, &ValidationError{Message: "策略路由绑定分组不能重复"}
		}
		seenGroupIDs[groupID] = true
		priority := derefIntOr(input.Priority, 0)
		if input.Status == "active" {
			if activePriorities[priority] {
				return nil, &ValidationError{Message: "策略路由启用分组优先级不能重复"}
			}
			activePriorities[priority] = true
		}
	}
	hasActive := false
	for _, input := range inputs {
		if input.Status == "active" {
			hasActive = true
			break
		}
	}
	if !hasActive {
		return nil, &ValidationError{Message: "策略路由至少需要一个启用分组"}
	}

	groups, err := s.loadBindableGroups(ctx, q, inputs, ownerID)
	if err != nil {
		return nil, err
	}
	writes := make([]bindingWrite, 0, len(inputs))
	for _, input := range inputs {
		groupID := strings.TrimSpace(input.GroupID)
		group, ok := groups[groupID]
		if !ok || !group.canBind {
			return nil, &ValidationError{Message: routeStrategyGroupBoundaryError}
		}
		if input.Status == "active" && !group.enabled {
			name := groupID
			if group.name != "" {
				name = group.name
			}
			return nil, &ValidationError{Message: "策略路由不能启用已停用分组：" + name}
		}
		write := bindingWrite{
			groupID:      groupID,
			groupName:    group.namePtr(),
			providerCode: group.providerCode,
			priority:     derefIntOr(input.Priority, 0),
			weight:       derefIntOr(input.Weight, 1),
			status:       input.Status,
			groupEnabled: group.enabled,
		}
		writes = append(writes, write)
	}
	sort.SliceStable(writes, func(i, j int) bool {
		if writes[i].priority != writes[j].priority {
			return writes[i].priority < writes[j].priority
		}
		return writes[i].groupID < writes[j].groupID
	})
	return writes, nil
}

// bindableGroup mirrors RouteStrategyBindableGroupRow (owner branch).
type bindableGroup struct {
	id           string
	ownerID      string
	providerCode string
	name         string
	enabled      bool
	canBind      bool
}

func (g bindableGroup) namePtr() *string {
	if g.name == "" {
		return nil
	}
	return &g.name
}

// loadBindableGroups mirrors loadRouteStrategyBindableGroupsAsync: owned
// groups bind with their own enabled flag; foreign groups stay unbindable
// until the authorization slice adds the grantee branch.
func (s *Store) loadBindableGroups(ctx context.Context, q queryer, inputs []BindingInput, ownerID string) (map[string]bindableGroup, error) {
	ids := uniqueGroupIDs(inputs)
	result := map[string]bindableGroup{}
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(ids))
	args := []any{ownerID}
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT groups.id, groups.system_account_id, groups.provider_code, groups.name,
			groups.enabled,
			CASE WHEN groups.system_account_id = ? THEN 1 ELSE 0 END AS can_bind
		FROM `+s.table("groups")+` groups
		WHERE groups.id IN (`+strings.Join(placeholders, ",")+")"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var group bindableGroup
		var enabled, canBind int
		var name sql.NullString
		if err := rows.Scan(&group.id, &group.ownerID, &group.providerCode, &name, &enabled, &canBind); err != nil {
			return nil, err
		}
		group.name = name.String
		group.enabled = enabled == 1
		group.canBind = canBind == 1
		result[group.id] = group
	}
	return result, rows.Err()
}

func uniqueGroupIDs(inputs []BindingInput) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(inputs))
	for _, input := range inputs {
		id := strings.TrimSpace(input.GroupID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// validateModeBindings mirrors validateRouteStrategyModeBindings.
func validateModeBindings(mode string, bindings []bindingWrite) error {
	activeCount := 0
	for _, binding := range bindings {
		if binding.status == "active" {
			activeCount++
		}
	}
	if mode == ModeNormal && (len(bindings) != 1 || activeCount != 1) {
		return &ValidationError{Message: "普通路由只能绑定一个启用分组"}
	}
	if mode == ModeFailover {
		if len(bindings) < 2 {
			return &ValidationError{Message: "故障回退路由需要一个主用分组和至少一个备用分组"}
		}
		if bindings[0].status != "active" {
			return &ValidationError{Message: "故障回退路由的主用分组必须启用"}
		}
		backupActive := false
		for _, binding := range bindings[1:] {
			if binding.status == "active" {
				backupActive = true
				break
			}
		}
		if !backupActive {
			return &ValidationError{Message: "故障回退路由至少需要一个启用备用分组"}
		}
	}
	return nil
}

// bindingWritesFromSummaries mirrors routeStrategyGroupBindingWritesFromSummary
// (sorted by priority then groupId).
func bindingWritesFromSummaries(bindings []GroupBinding) []bindingWrite {
	writes := make([]bindingWrite, 0, len(bindings))
	for _, binding := range bindings {
		writes = append(writes, bindingWrite{
			groupID:      binding.GroupID,
			groupName:    binding.GroupName,
			providerCode: derefOrEmpty(binding.ProviderCode),
			priority:     binding.Priority,
			weight:       binding.Weight,
			status:       binding.Status,
			groupEnabled: binding.GroupEnabled,
		})
	}
	sort.SliceStable(writes, func(i, j int) bool {
		if writes[i].priority != writes[j].priority {
			return writes[i].priority < writes[j].priority
		}
		return writes[i].groupID < writes[j].groupID
	})
	return writes
}

// bindingWritesEqual mirrors routeStrategyGroupBindingsEqual.
func bindingWritesEqual(left, right []bindingWrite) bool {
	if len(left) != len(right) {
		return false
	}
	for index, item := range left {
		other := right[index]
		if item.groupID != other.groupID || item.priority != other.priority ||
			item.weight != other.weight || item.status != other.status {
			return false
		}
	}
	return true
}

// replaceBindings mirrors replaceRouteStrategyGroups: hard delete then insert
// the full set; returns the presentation-ordered summaries.
func (s *Store) replaceBindings(ctx context.Context, q queryer, strategyID, ownerID, mode string, bindings []bindingWrite, now string) ([]GroupBinding, error) {
	if err := validateModeBindings(mode, bindings); err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("route_strategy_groups")+`
		WHERE route_strategy_id = ?`), strategyID); err != nil {
		return nil, err
	}
	written := make([]GroupBinding, 0, len(bindings))
	for _, binding := range bindings {
		id := s.newI("rsg")
		if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("route_strategy_groups")+`
			(id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			id, strategyID, ownerID, binding.groupID, binding.priority, binding.weight, binding.status, now, now); err != nil {
			return nil, err
		}
		written = append(written, bindingSummaryFromWrite(id, binding))
	}
	sortBindingsForPresentation(written)
	return written, nil
}

// reconcileBindings mirrors reconcileRouteStrategyGroups: remove missing
// group ids, update changed rows in place, insert new ones.
func (s *Store) reconcileBindings(ctx context.Context, q queryer, strategyID, ownerID, mode string, current []GroupBinding, bindings []bindingWrite, now string) ([]GroupBinding, error) {
	if err := validateModeBindings(mode, bindings); err != nil {
		return nil, err
	}
	currentByGroupID := map[string]GroupBinding{}
	for _, binding := range current {
		currentByGroupID[binding.GroupID] = binding
	}
	nextGroupIDs := map[string]bool{}
	for _, binding := range bindings {
		nextGroupIDs[binding.groupID] = true
	}
	removed := []string{}
	for _, binding := range current {
		if !nextGroupIDs[binding.GroupID] {
			removed = append(removed, binding.GroupID)
		}
	}
	if len(removed) > 0 {
		placeholders := make([]string, len(removed))
		args := []any{strategyID, ownerID}
		for i, id := range removed {
			placeholders[i] = "?"
			args = append(args, id)
		}
		if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("route_strategy_groups")+`
			WHERE route_strategy_id = ? AND system_account_id = ?
				AND group_id IN (`+strings.Join(placeholders, ",")+")"), args...); err != nil {
			return nil, err
		}
	}
	written := make([]GroupBinding, 0, len(bindings))
	for _, binding := range bindings {
		existing, ok := currentByGroupID[binding.groupID]
		if !ok {
			id := s.newI("rsg")
			if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("route_strategy_groups")+`
				(id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
				id, strategyID, ownerID, binding.groupID, binding.priority, binding.weight, binding.status, now, now); err != nil {
				return nil, err
			}
			written = append(written, bindingSummaryFromWrite(id, binding))
			continue
		}
		if existing.Priority != binding.priority || existing.Weight != binding.weight || existing.Status != binding.status {
			if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("route_strategy_groups")+`
				SET priority = ?, weight = ?, status = ?, updated_at = ?
				WHERE id = ? AND route_strategy_id = ? AND system_account_id = ? AND group_id = ?`),
				binding.priority, binding.weight, binding.status, now,
				existing.ID, strategyID, ownerID, binding.groupID); err != nil {
				return nil, err
			}
		}
		written = append(written, bindingSummaryFromWrite(existing.ID, binding))
	}
	sortBindingsForPresentation(written)
	return written, nil
}

func bindingSummaryFromWrite(id string, binding bindingWrite) GroupBinding {
	summary := GroupBinding{
		ID:           id,
		GroupID:      binding.groupID,
		GroupName:    binding.groupName,
		Priority:     binding.priority,
		Weight:       binding.weight,
		Status:       binding.status,
		GroupEnabled: binding.groupEnabled,
	}
	if binding.providerCode != "" {
		summary.ProviderCode = &binding.providerCode
	}
	return summary
}

// sortBindingsForPresentation mirrors routeStrategyGroupBindingsForPresentation:
// active first, then priority, then binding id.
func sortBindingsForPresentation(bindings []GroupBinding) {
	sort.SliceStable(bindings, func(i, j int) bool {
		statusOrder_i := 0
		if bindings[i].Status != "active" {
			statusOrder_i = 1
		}
		statusOrder_j := 0
		if bindings[j].Status != "active" {
			statusOrder_j = 1
		}
		if statusOrder_i != statusOrder_j {
			return statusOrder_i < statusOrder_j
		}
		if bindings[i].Priority != bindings[j].Priority {
			return bindings[i].Priority < bindings[j].Priority
		}
		return bindings[i].ID < bindings[j].ID
	})
}

// derefIntOr returns the pointed value or the fallback.
func derefIntOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

// ---- shared small helpers ----

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func ptrString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func nullString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameNullableText(current sql.NullString, next *string) bool {
	if next == nil {
		return !current.Valid || current.String == ""
	}
	return current.Valid && current.String == *next
}

func sameNullString(left, right sql.NullString) bool {
	return left.Valid == right.Valid && left.String == right.String
}

var errNoRows = sql.ErrNoRows

func isNoRows(err error) bool { return errors.Is(err, errNoRows) }
