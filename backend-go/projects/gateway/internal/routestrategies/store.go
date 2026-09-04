// Route-strategy read paths: paginated list with binding snapshot, owner
// detail and shared row scanning. Mirrors
// storage/route-strategy.repository.ts list/find functions (owner branch).

package routestrategies

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// GroupBindingPreview mirrors RouteStrategyGroupBindingPreview.
type GroupBindingPreview struct {
	ID           string  `json:"id"`
	GroupID      string  `json:"groupId"`
	GroupName    *string `json:"groupName,omitempty"`
	ProviderCode *string `json:"providerCode,omitempty"`
	Status       string  `json:"status"`
	GroupEnabled bool    `json:"groupEnabled"`
}

// GroupBinding mirrors RouteStrategyGroupBindingSummary.
type GroupBinding struct {
	ID           string  `json:"id"`
	GroupID      string  `json:"groupId"`
	GroupName    *string `json:"groupName,omitempty"`
	ProviderCode *string `json:"providerCode,omitempty"`
	Priority     int     `json:"priority"`
	Weight       int     `json:"weight"`
	Status       string  `json:"status"`
	GroupEnabled bool    `json:"groupEnabled"`
}

// ListItem mirrors CompleteRouteStrategyListItem (list + create response).
type ListItem struct {
	ID                   string                `json:"id"`
	SystemAccountID      *string               `json:"systemAccountId,omitempty"`
	SystemAccountName    *string               `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID string                `json:"-"`
	Name                 string                `json:"name"`
	Description          *string               `json:"description,omitempty"`
	Mode                 string                `json:"mode"`
	Status               string                `json:"status"`
	IsDefault            bool                  `json:"isDefault"`
	NormalRoutingConfig  *NormalRoutingConfig  `json:"normalRoutingConfig,omitempty"`
	BindingCount         int                   `json:"bindingCount"`
	APIKeyCount          int                   `json:"apiKeyCount"`
	GroupBindingPreview  []GroupBindingPreview `json:"groupBindingPreview"`
	CreatedAt            string                `json:"createdAt"`
	UpdatedAt            string                `json:"updatedAt"`
}

// Detail mirrors RouteStrategySummary (GET /{id} response).
type Detail struct {
	ID                   string               `json:"id"`
	SystemAccountID      *string              `json:"systemAccountId,omitempty"`
	SystemAccountName    *string              `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID string               `json:"-"`
	Name                 string               `json:"name"`
	Description          *string              `json:"description,omitempty"`
	Mode                 string               `json:"mode"`
	Status               string               `json:"status"`
	IsDefault            bool                 `json:"isDefault"`
	NormalRoutingConfig  *NormalRoutingConfig `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig  *HybridRoutingConfig `json:"hybridRoutingConfig,omitempty"`
	GroupBindings        []GroupBinding       `json:"groupBindings"`
	APIKeyCount          int                  `json:"apiKeyCount"`
	CreatedAt            string               `json:"createdAt"`
	UpdatedAt            string               `json:"updatedAt"`
}

// ListPageResult mirrors CompleteRouteStrategyListItemResult.
type ListPageResult struct {
	Items       []ListItem `json:"items"`
	Total       int        `json:"total"`
	HasMore     bool       `json:"hasMore"`
	Page        int        `json:"page"`
	PageSize    int        `json:"pageSize"`
	GeneratedAt string     `json:"generatedAt"`
}

// ListOptions mirrors RouteStrategyListOptions with normalized values already
// applied by the route layer (keyword trimmed; mode/status ” = no filter).
type ListOptions struct {
	Page     int
	PageSize int
	Keyword  string
	Mode     string
	Status   string
}

// strategyRow is the shared scan target for list/detail/patch/delete rows.
type strategyRow struct {
	id              string
	systemAccountID string
	name            string
	description     sql.NullString
	mode            string
	status          string
	isDefault       bool
	configJSON      sql.NullString
	createdAt       string
	updatedAt       string
	apiKeyCount     int
}

const strategyRowColumns = `route_strategies.id, route_strategies.system_account_id, route_strategies.name,
	route_strategies.description, route_strategies.mode, route_strategies.status,
	route_strategies.is_default, route_strategies.config_json,
	route_strategies.created_at, route_strategies.updated_at`

func scanStrategyRow(scan func(...any) error, withAPIKeyCount bool) (strategyRow, error) {
	row := strategyRow{}
	var isDefault int
	targets := []any{&row.id, &row.systemAccountID, &row.name, &row.description, &row.mode, &row.status,
		&isDefault, &row.configJSON, &row.createdAt, &row.updatedAt}
	if withAPIKeyCount {
		targets = append(targets, &row.apiKeyCount)
	}
	if err := scan(targets...); err != nil {
		return strategyRow{}, err
	}
	row.isDefault = isDefault == 1
	return row, nil
}

// ownerClause mirrors buildSystemAccountWhereClause: admins without a filter
// see all rows, everyone else is pinned to the scope id. ok=false means the
// caller has no scope at all (empty result set).
func ownerClause(access AccessScope, column string) (string, []any, bool) {
	if scoped := access.manageableID(); scoped != "" {
		return column + " = ?", []any{scoped}, true
	}
	if !access.canAccessAll() {
		return "", nil, false
	}
	return "", nil, true
}

// keywordFilter mirrors buildRouteStrategyFilters: case-sensitive name prefix
// range (textPrefixUpperBound).
func keywordFilter(keyword string) (string, []any) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return "", nil
	}
	upper := textPrefixUpperBound(text)
	return "(route_strategies.name >= ? AND route_strategies.name < ?)", []any{text, upper}
}

func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			runes[index]++
			return string(runes[:index+1])
		}
	}
	return value + "\uffff"
}

// ListPage mirrors listCompleteRouteStrategyListItemsPageAsync: pageSize+1
// probe, updated_at DESC ordering, then the binding snapshot merged in.
func (s *Store) ListPage(ctx context.Context, access AccessScope, options ListOptions) (*ListPageResult, error) {
	ctx = ensureCtx(ctx)
	page, pageSize := options.Page, options.PageSize
	if page < 1 {
		page = 1
	}
	// normalizeRouteStrategyListOptions: clamp 1..200 (0 clamps to 1).
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 200 {
		pageSize = 200
	}
	clauses := []string{}
	args := []any{}
	if clause, clauseArgs, ok := ownerClause(access, "route_strategies.system_account_id"); ok {
		if clause != "" {
			clauses = append(clauses, clause)
			args = append(args, clauseArgs...)
		}
	} else {
		return s.emptyPage(page, pageSize), nil
	}
	if clause, clauseArgs := keywordFilter(options.Keyword); clause != "" {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	if options.Mode != "" {
		clauses = append(clauses, "route_strategies.mode = ?")
		args = append(args, options.Mode)
	}
	if options.Status != "" {
		clauses = append(clauses, "route_strategies.status = ?")
		args = append(args, options.Status)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+strategyRowColumns+`
		FROM `+s.table("route_strategies")+` route_strategies
		LEFT JOIN `+s.table("system_accounts")+` system_accounts
			ON system_accounts.id = route_strategies.system_account_id`+where+`
		ORDER BY route_strategies.updated_at DESC, route_strategies.created_at DESC, route_strategies.id DESC
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	strategies := []strategyRow{}
	for rows.Next() {
		row, scanErr := scanStrategyRow(rows.Scan, false)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		strategies = append(strategies, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hasMore := len(strategies) > pageSize
	if hasMore {
		strategies = strategies[:pageSize]
	}
	result := &ListPageResult{
		Items:       []ListItem{},
		Page:        page,
		PageSize:    pageSize,
		GeneratedAt: s.nowISO(),
	}
	// pagedTotalUpperBound: count of rows before this page + visible + 1.
	result.Total = (page-1)*pageSize + len(strategies)
	if hasMore {
		result.Total++
	}
	result.HasMore = hasMore
	if len(strategies) == 0 {
		return result, nil
	}
	names, err := s.systemAccountNames(ctx, rowOwnerIDs(strategies))
	if err != nil {
		return nil, err
	}
	bindingCounts, apiKeyCounts, previews, err := s.bindingSnapshot(ctx, strategyIDs(strategies))
	if err != nil {
		return nil, err
	}
	for _, row := range strategies {
		result.Items = append(result.Items, newListItem(row, names, bindingCounts[row.id], apiKeyCounts[row.id], previews[row.id], access.canAccessAll()))
	}
	return result, nil
}

func (s *Store) emptyPage(page, pageSize int) *ListPageResult {
	return &ListPageResult{Items: []ListItem{}, Page: page, PageSize: pageSize, GeneratedAt: s.nowISO()}
}

func strategyIDs(rows []strategyRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	return ids
}

func rowOwnerIDs(rows []strategyRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.systemAccountID)
	}
	return ids
}

// systemAccountNames mirrors loadSystemAccountNameMapByIds.
func (s *Store) systemAccountNames(ctx context.Context, ids []string) (map[string]string, error) {
	unique := uniqueStrings(ids)
	names := map[string]string{}
	if len(unique) == 0 {
		return names, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, display_name FROM `+s.table("system_accounts")+`
		WHERE id IN (`+strings.Join(placeholders, ",")+")"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// bindingSnapshot mirrors listRouteStrategyListSnapshotAsync (owner branch):
// binding and api_key counts plus the top-3 group binding preview per
// strategy id.
func (s *Store) bindingSnapshot(ctx context.Context, ids []string) (map[string]int, map[string]int, map[string][]GroupBindingPreview, error) {
	bindingCounts := map[string]int{}
	apiKeyCounts := map[string]int{}
	previews := map[string][]GroupBindingPreview{}
	unique := uniqueStrings(ids)
	if len(unique) == 0 {
		return bindingCounts, apiKeyCounts, previews, nil
	}
	for _, id := range unique {
		bindingCounts[id] = 0
		apiKeyCounts[id] = 0
		previews[id] = []GroupBindingPreview{}
	}
	placeholders := make([]string, len(unique))
	countArgs := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		countArgs[i] = id
	}
	joined := strings.Join(placeholders, ",")

	bindingRows, err := s.db.QueryContext(ctx, s.bind(`SELECT route_strategy_groups.route_strategy_id, COUNT(1) AS count
		FROM `+s.table("route_strategy_groups")+` route_strategy_groups
		INNER JOIN `+s.table("route_strategies")+` route_strategies
			ON route_strategies.id = route_strategy_groups.route_strategy_id
			AND route_strategies.system_account_id = route_strategy_groups.system_account_id
		WHERE route_strategy_groups.route_strategy_id IN (`+joined+`)
		GROUP BY route_strategy_groups.route_strategy_id`), countArgs...)
	if err != nil {
		return nil, nil, nil, err
	}
	for bindingRows.Next() {
		var id string
		var count int
		if err := bindingRows.Scan(&id, &count); err != nil {
			bindingRows.Close()
			return nil, nil, nil, err
		}
		bindingCounts[id] = count
	}
	if err := bindingRows.Err(); err != nil {
		bindingRows.Close()
		return nil, nil, nil, err
	}
	bindingRows.Close()

	keyRows, err := s.db.QueryContext(ctx, s.bind(`SELECT api_keys.route_strategy_id, COUNT(1) AS count
		FROM `+s.table("api_keys")+` api_keys
		INNER JOIN `+s.table("route_strategies")+` route_strategies
			ON route_strategies.id = api_keys.route_strategy_id
			AND route_strategies.system_account_id = api_keys.system_account_id
		WHERE api_keys.route_strategy_id IN (`+joined+`)
		GROUP BY api_keys.route_strategy_id`), countArgs...)
	if err != nil {
		return nil, nil, nil, err
	}
	for keyRows.Next() {
		var id string
		var count int
		if err := keyRows.Scan(&id, &count); err != nil {
			keyRows.Close()
			return nil, nil, nil, err
		}
		apiKeyCounts[id] = count
	}
	if err := keyRows.Err(); err != nil {
		keyRows.Close()
		return nil, nil, nil, err
	}
	keyRows.Close()

	previewRows, err := s.db.QueryContext(ctx, s.bind(`SELECT
		route_strategy_groups.id,
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
		END AS group_enabled
	FROM `+s.table("route_strategy_groups")+` route_strategy_groups
	LEFT JOIN `+s.table("groups")+` groups ON groups.id = route_strategy_groups.group_id
	WHERE route_strategy_groups.id IN (
		SELECT ranked.id
		FROM (
			SELECT ranked_groups.id,
				ROW_NUMBER() OVER (
					PARTITION BY ranked_groups.route_strategy_id
					ORDER BY CASE WHEN ranked_groups.status = 'active' THEN 0 ELSE 1 END ASC,
						ranked_groups.priority ASC,
						ranked_groups.created_at ASC,
						ranked_groups.id ASC
				) AS row_number
			FROM `+s.table("route_strategy_groups")+` ranked_groups
			WHERE ranked_groups.route_strategy_id IN (`+joined+`)
		) ranked
		WHERE ranked.row_number <= 3
	)
	ORDER BY route_strategy_groups.route_strategy_id ASC,
		CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
		route_strategy_groups.priority ASC,
		route_strategy_groups.created_at ASC,
		route_strategy_groups.id ASC`), countArgs...)
	if err != nil {
		return nil, nil, nil, err
	}
	for previewRows.Next() {
		binding, scanErr := scanBindingRow(previewRows.Scan)
		if scanErr != nil {
			previewRows.Close()
			return nil, nil, nil, scanErr
		}
		previews[binding.routeStrategyID] = append(previews[binding.routeStrategyID], newGroupBindingPreview(binding.summary()))
	}
	if err := previewRows.Err(); err != nil {
		previewRows.Close()
		return nil, nil, nil, err
	}
	previewRows.Close()
	return bindingCounts, apiKeyCounts, previews, nil
}

func newGroupBindingPreview(binding GroupBinding) GroupBindingPreview {
	return GroupBindingPreview{
		ID:           binding.ID,
		GroupID:      binding.GroupID,
		GroupName:    binding.GroupName,
		ProviderCode: binding.ProviderCode,
		Status:       binding.Status,
		GroupEnabled: binding.GroupEnabled,
	}
}

func previewsFromBindings(bindings []GroupBinding, limit int) []GroupBindingPreview {
	out := make([]GroupBindingPreview, 0, len(bindings))
	for index, binding := range bindings {
		if index >= limit {
			break
		}
		out = append(out, newGroupBindingPreview(binding))
	}
	return out
}

func newListItem(row strategyRow, names map[string]string, bindingCount, apiKeyCount int, previews []GroupBindingPreview, includeOwner bool) ListItem {
	item := ListItem{
		ID:                   row.id,
		OwnerSystemAccountID: row.systemAccountID,
		Name:                 row.name,
		Description:          nullPtrString(row.description),
		Mode:                 row.mode,
		Status:               row.status,
		IsDefault:            row.isDefault,
		NormalRoutingConfig:  listItemNormalConfig(row),
		BindingCount:         bindingCount,
		APIKeyCount:          apiKeyCount,
		GroupBindingPreview:  previews,
		CreatedAt:            row.createdAt,
		UpdatedAt:            row.updatedAt,
	}
	if includeOwner {
		item.SystemAccountID = ptrString(row.systemAccountID)
		item.SystemAccountName = ptrString(names[row.systemAccountID])
	}
	return item
}

// listItemNormalConfig mirrors routeStrategyListItemFromRow: normal mode
// renders the stored config or the cost_first default.
func listItemNormalConfig(row strategyRow) *NormalRoutingConfig {
	if row.mode != ModeNormal {
		return nil
	}
	normal, _, err := parseStoredConfig(row.configJSON)
	if err != nil || normal == nil {
		return &NormalRoutingConfig{SchedulingPreference: defaultNormalSchedulingPreference}
	}
	return normal
}

// FindDetail mirrors findRouteStrategySummaryAsync: nil when the strategy is
// missing or not owned by the scope (route renders 404 策略路由不存在).
func (s *Store) FindDetail(ctx context.Context, id string, access AccessScope) (*Detail, error) {
	ctx = ensureCtx(ctx)
	clause, clauseArgs, ok := ownerClause(access, "route_strategies.system_account_id")
	if !ok {
		return nil, nil
	}
	where := ""
	args := []any{id}
	if clause != "" {
		where = " AND " + clause
		args = append(args, clauseArgs...)
	}
	row, err := scanStrategyRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+strategyRowColumns+`,
			(SELECT COUNT(1) FROM `+s.table("api_keys")+` api_keys
				WHERE api_keys.route_strategy_id = route_strategies.id
				AND api_keys.system_account_id = route_strategies.system_account_id) AS api_key_count
		FROM `+s.table("route_strategies")+` route_strategies
		LEFT JOIN `+s.table("system_accounts")+` system_accounts
			ON system_accounts.id = route_strategies.system_account_id
		WHERE route_strategies.id = ?`+where+` LIMIT 1`), args...).Scan(targets...)
	}, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	bindings, err := s.loadBindings(ctx, s.db, []string{row.id})
	if err != nil {
		return nil, err
	}
	return s.newDetail(ctx, row, bindings[row.id], access), nil
}

func (s *Store) newDetail(ctx context.Context, row strategyRow, bindings []GroupBinding, access AccessScope) *Detail {
	normal, hybrid, err := parseStoredConfig(row.configJSON)
	if err != nil {
		normal, hybrid = nil, nil
	}
	detail := &Detail{
		ID:                   row.id,
		OwnerSystemAccountID: row.systemAccountID,
		Name:                 row.name,
		Description:          nullPtrString(row.description),
		Mode:                 row.mode,
		Status:               row.status,
		IsDefault:            row.isDefault,
		NormalRoutingConfig:  normalConfigForMode(row.mode, normal),
		HybridRoutingConfig:  hybridConfigForMode(row.mode, hybrid),
		GroupBindings:        bindings,
		APIKeyCount:          row.apiKeyCount,
		CreatedAt:            row.createdAt,
		UpdatedAt:            row.updatedAt,
	}
	if detail.GroupBindings == nil {
		detail.GroupBindings = []GroupBinding{}
	}
	if access.canAccessAll() {
		detail.SystemAccountID = ptrString(row.systemAccountID)
		detail.SystemAccountName = s.lookupName(ensureCtx(ctx), row.systemAccountID)
	}
	return detail
}

// normalConfigForMode renders normalRoutingConfig for mode normal (default
// filled), nil otherwise.
func normalConfigForMode(mode string, normal *NormalRoutingConfig) *NormalRoutingConfig {
	if mode != ModeNormal {
		return nil
	}
	if normal == nil {
		return &NormalRoutingConfig{SchedulingPreference: defaultNormalSchedulingPreference}
	}
	return normal
}

// hybridConfigForMode renders hybridRoutingConfig for hybrid_smart only.
func hybridConfigForMode(mode string, hybrid *HybridRoutingConfig) *HybridRoutingConfig {
	if mode != ModeHybridSmart {
		return nil
	}
	return hybrid
}

func (s *Store) lookupName(ctx context.Context, id string) *string {
	names, err := s.systemAccountNames(ctx, []string{id})
	if err != nil || names[id] == "" {
		return nil
	}
	return ptrString(names[id])
}
