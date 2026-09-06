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
	ID                   string                 `json:"id"`
	SystemAccountID      *string                `json:"systemAccountId,omitempty"`
	SystemAccountName    *string                `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID string                 `json:"-"`
	Name                 string                 `json:"name"`
	Description          *string                `json:"description,omitempty"`
	Mode                 string                 `json:"mode"`
	Status               string                 `json:"status"`
	IsDefault            bool                   `json:"isDefault"`
	NormalRoutingConfig  *NormalRoutingConfig   `json:"normalRoutingConfig,omitempty"`
	BindingCount         int                    `json:"bindingCount"`
	APIKeyCount          int                    `json:"apiKeyCount"`
	GroupBindingPreview  []GroupBindingPreview  `json:"groupBindingPreview"`
	CreatedAt            string                 `json:"createdAt"`
	UpdatedAt            string                 `json:"updatedAt"`
	SpeedFirstLatency    *SpeedFirstRuntimeSummary `json:"speedFirstLatencyRuntime,omitempty"`
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

// keywordFilter mirrors buildRouteStrategyFiltersForClient: case-sensitive
// name prefix range (textPrefixUpperBound). The PostgreSQL branch pins the
// comparison to the `C` collation exactly like the source; SQLite keeps the
// unadorned range.
func (s *Store) keywordFilter(keyword string) (string, []any) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return "", nil
	}
	upper := textPrefixUpperBound(text)
	if s.pg {
		return `(route_strategies.name COLLATE "C" >= ? AND route_strategies.name COLLATE "C" < ?)`, []any{text, upper}
	}
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
	if clause, clauseArgs := s.keywordFilter(options.Keyword); clause != "" {
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
		item, itemErr := newListItem(row, names, bindingCounts[row.id], apiKeyCounts[row.id], previews[row.id], access.canAccessAll())
		if itemErr != nil {
			return nil, itemErr
		}
		result.Items = append(result.Items, item)
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

	// Preview rows read through the shared binding projection (authorization
	// joins included, `expires_at > now` bound first like the source).
	args := append([]any{s.nowISO()}, countArgs...)
	previewRows, err := s.db.QueryContext(ctx, s.bind(`SELECT
		`+s.bindingRowColumns()+`
	`+s.bindingRowFrom()+`
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
	`+bindingRowOrder), args...)
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

func newListItem(row strategyRow, names map[string]string, bindingCount, apiKeyCount int, previews []GroupBindingPreview, includeOwner bool) (ListItem, error) {
	mode, modeErr := normalizeStoredMode(row.mode)
	if modeErr != nil {
		return ListItem{}, modeErr
	}
	status, statusErr := normalizeStoredStatus(row.status, "active")
	if statusErr != nil {
		return ListItem{}, statusErr
	}
	normal, _, configErr := parseStoredConfig(row.configJSON)
	if configErr != nil {
		return ListItem{}, configErr
	}
	item := ListItem{
		ID:                   row.id,
		OwnerSystemAccountID: row.systemAccountID,
		Name:                 row.name,
		Description:          nullPtrString(row.description),
		Mode:                 mode,
		Status:               status,
		IsDefault:            row.isDefault,
		NormalRoutingConfig:  normalConfigForMode(mode, normal),
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
	return item, nil
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
	detail, err := s.newDetail(ctx, row, bindings[row.id], access)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Store) newDetail(ctx context.Context, row strategyRow, bindings []GroupBinding, access AccessScope) (*Detail, error) {
	mode, modeErr := normalizeStoredMode(row.mode)
	if modeErr != nil {
		return nil, modeErr
	}
	status, statusErr := normalizeStoredStatus(row.status, "active")
	if statusErr != nil {
		return nil, statusErr
	}
	normal, hybrid, err := parseStoredConfig(row.configJSON)
	if err != nil {
		return nil, err
	}
	detail := &Detail{
		ID:                   row.id,
		OwnerSystemAccountID: row.systemAccountID,
		Name:                 row.name,
		Description:          nullPtrString(row.description),
		Mode:                 mode,
		Status:               status,
		IsDefault:            row.isDefault,
		NormalRoutingConfig:  normalConfigForMode(mode, normal),
		HybridRoutingConfig:  hybridConfigForMode(mode, hybrid),
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
	return detail, nil
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

// OptionSummary mirrors RouteStrategyOptionSummary (GET /options response).
type OptionSummary struct {
	ID                string `json:"id"`
	SystemAccountID   *string `json:"systemAccountId,omitempty"`
	SystemAccountName *string `json:"systemAccountName,omitempty"`
	Name              string `json:"name"`
	Mode              string `json:"mode"`
	Status            string `json:"status"`
	IsDefault         bool   `json:"isDefault"`
}

// OptionsQuery mirrors RouteStrategyOptionListOptions after the route-level
// query parsing (ids deduped/capped, keyword trimmed, limit clamped).
type OptionsQuery struct {
	IDs        []string
	Keyword    string
	Limit      int
	ActiveOnly bool
}

// ListOptionsPage mirrors listRouteStrategyOptionsAsync: owner-scoped option
// summaries ordered is_default DESC, updated_at DESC, name ASC, id ASC.
func (s *Store) ListOptionsPage(ctx context.Context, access AccessScope, query OptionsQuery) ([]OptionSummary, error) {
	ctx = ensureCtx(ctx)
	limit := query.Limit
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	clauses := []string{}
	args := []any{}
	if clause, clauseArgs, ok := ownerClause(access, "route_strategies.system_account_id"); ok {
		if clause != "" {
			clauses = append(clauses, clause)
			args = append(args, clauseArgs...)
		}
	} else {
		return []OptionSummary{}, nil
	}
	if len(query.IDs) > 0 {
		placeholders := make([]string, len(query.IDs))
		for i, id := range query.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		clauses = append(clauses, "route_strategies.id IN ("+strings.Join(placeholders, ",")+")")
	}
	if clause, clauseArgs := s.keywordFilter(query.Keyword); clause != "" {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	if query.ActiveOnly {
		clauses = append(clauses, "route_strategies.status = 'active'")
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT route_strategies.id, route_strategies.system_account_id, route_strategies.name,
		route_strategies.mode, route_strategies.status, route_strategies.is_default
		FROM `+s.table("route_strategies")+` route_strategies`+where+`
		ORDER BY route_strategies.is_default DESC, route_strategies.updated_at DESC, route_strategies.name ASC, route_strategies.id ASC
		LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accountIDs []string
	type optionRow struct {
		id, systemAccountID, name, mode, status string
		isDefault                               bool
	}
	parsed := []optionRow{}
	for rows.Next() {
		row := optionRow{}
		var isDefault int
		if err := rows.Scan(&row.id, &row.systemAccountID, &row.name, &row.mode, &row.status, &isDefault); err != nil {
			return nil, err
		}
		row.isDefault = isDefault == 1
		// routeStrategyOptionsFromRowsAsync normalizes mode/status per row and
		// fails the whole read on unknown stored values.
		mode, modeErr := normalizeStoredMode(row.mode)
		if modeErr != nil {
			return nil, modeErr
		}
		row.mode = mode
		status, statusErr := normalizeStoredStatus(row.status, "active")
		if statusErr != nil {
			return nil, statusErr
		}
		row.status = status
		parsed = append(parsed, row)
		accountIDs = append(accountIDs, row.systemAccountID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := map[string]string{}
	if access.canAccessAll() {
		names, err = s.systemAccountNames(ctx, accountIDs)
		if err != nil {
			return nil, err
		}
	}
	options := make([]OptionSummary, 0, len(parsed))
	for _, row := range parsed {
		summary := OptionSummary{
			ID:        row.id,
			Name:      row.name,
			Mode:      row.mode,
			Status:    row.status,
			IsDefault: row.isDefault,
		}
		if access.canAccessAll() {
			summary.SystemAccountID = ptrString(row.systemAccountID)
			if name := names[row.systemAccountID]; name != "" {
				summary.SystemAccountName = ptrString(name)
			}
		}
		options = append(options, summary)
	}
	return options, nil
}

// EditBasicDetail mirrors RouteStrategyEditBasicDetail (GET /:id/edit-basic).
type EditBasicDetail struct {
	ID                  string               `json:"id"`
	SystemAccountID     *string              `json:"systemAccountId,omitempty"`
	Name                string               `json:"name"`
	Description         *string              `json:"description,omitempty"`
	Mode                string               `json:"mode"`
	Status              string               `json:"status"`
	IsDefault           bool                 `json:"isDefault"`
	NormalRoutingConfig *NormalRoutingConfig `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig *HybridRoutingConfig `json:"hybridRoutingConfig,omitempty"`
	GroupBindings       []GroupBinding       `json:"groupBindings"`
	UpdatedAt           string               `json:"updatedAt"`
}

// FindEditBasic mirrors findRouteStrategyEditBasicDetailAsync: nil when the
// strategy is missing or not owned by the scope (route renders 404).
func (s *Store) FindEditBasic(ctx context.Context, id string, access AccessScope) (*EditBasicDetail, error) {
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
	var rowID, systemAccountID, name, mode, status, updatedAt string
	var description sql.NullString
	var configJSON sql.NullString
	var isDefault int
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT route_strategies.id, route_strategies.system_account_id, route_strategies.name,
		route_strategies.description, route_strategies.mode, route_strategies.status, route_strategies.is_default,
		route_strategies.config_json, route_strategies.updated_at
		FROM `+s.table("route_strategies")+` route_strategies
		WHERE route_strategies.id = ?`+where+` LIMIT 1`), args...).
		Scan(&rowID, &systemAccountID, &name, &description, &mode, &status, &isDefault, &configJSON, &updatedAt)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	normalizedMode, modeErr := normalizeStoredMode(mode)
	if modeErr != nil {
		return nil, modeErr
	}
	normalizedStatus, statusErr := normalizeStoredStatus(status, "active")
	if statusErr != nil {
		return nil, statusErr
	}
	normal, hybrid, configErr := parseStoredConfig(configJSON)
	if configErr != nil {
		return nil, configErr
	}
	bindings, err := s.loadBindings(ctx, s.db, []string{rowID})
	if err != nil {
		return nil, err
	}
	detail := &EditBasicDetail{
		ID:                  rowID,
		Name:                name,
		Description:         nullPtrString(description),
		Mode:                normalizedMode,
		Status:              normalizedStatus,
		IsDefault:           isDefault == 1,
		NormalRoutingConfig: normalConfigForMode(normalizedMode, normal),
		HybridRoutingConfig: hybridConfigForMode(normalizedMode, hybrid),
		GroupBindings:       bindings[rowID],
		UpdatedAt:           updatedAt,
	}
	if detail.GroupBindings == nil {
		detail.GroupBindings = []GroupBinding{}
	}
	if access.canAccessAll() {
		detail.SystemAccountID = ptrString(systemAccountID)
	}
	return detail, nil
}
