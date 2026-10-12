// Scheduling-preference read paths: paginated list, options and owner-scoped
// detail over scheduling_preferences with the builtin-row global read
// exception (builtin rows carry system_account_id NULL and stay visible
// through every scope, read-only). Mirrors the routestrategies store layout
// (接口契约「调度偏好」章 列表/详情/options 字段).
package schedulingpreferences

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// SpeedFirstConfigDocument is the normalized chatSpeedFirstConfig projection
// (计划 D2 九字段：firstByteDeadlineMs + speedFirstConfig 八字段)；仅
// speed_first 行出现。内层结构复用 routestrategies.SpeedFirstConfig，默认值
// 与合法范围单一来源。
type SpeedFirstConfigDocument struct {
	FirstByteDeadlineMs int                               `json:"firstByteDeadlineMs"`
	SpeedFirstConfig    *routestrategies.SpeedFirstConfig `json:"speedFirstConfig"`
}

// ListItem mirrors the list/detail/create row projection: 列表 / 详情字段
// `id`、`name`、`chatMode`、`chatSpeedFirstConfig`（仅 speed_first 出现）、
// `builtIn`、`enabled`、`boundApiKeyCount`、`updatedAt`；管理侧行附加
// systemAccountId / systemAccountName owner 摘要（内置行 owner NULL 自然缺
// 席），个人侧不返回管理 owner 字段。
type ListItem struct {
	ID                   string                    `json:"id"`
	Name                 string                    `json:"name"`
	ChatMode             string                    `json:"chatMode"`
	ChatSpeedFirstConfig *SpeedFirstConfigDocument `json:"chatSpeedFirstConfig,omitempty"`
	BuiltIn              bool                      `json:"builtIn"`
	Enabled              bool                      `json:"enabled"`
	BoundAPIKeyCount     int                       `json:"boundApiKeyCount"`
	UpdatedAt            string                    `json:"updatedAt"`
	SystemAccountID      *string                   `json:"systemAccountId,omitempty"`
	SystemAccountName    *string                   `json:"systemAccountName,omitempty"`

	OwnerSystemAccountID string `json:"-"`
}

// ListPageResult mirrors the list envelope {items, page, pageSize, total,
// hasMore}：pageSize+1 探测 hasMore，total 只是渐进上界，不执行精确 COUNT(*)。
type ListPageResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

// ListOptions mirrors the route-level normalized list query (keyword trimmed;
// chatMode/status 非法值按未传处理).
type ListOptions struct {
	Page     int
	PageSize int
	Keyword  string
	ChatMode string
	Status   string
}

// preferenceRow is the shared scan target for list/detail rows.
type preferenceRow struct {
	id              string
	systemAccountID string
	name            string
	chatMode        string
	configJSON      sql.NullString
	builtIn         bool
	enabled         bool
	updatedAt       string
}

const preferenceRowColumns = `scheduling_preferences.id, scheduling_preferences.system_account_id, scheduling_preferences.name,
	scheduling_preferences.chat_mode, scheduling_preferences.chat_speed_first_config_json,
	scheduling_preferences.built_in, scheduling_preferences.enabled, scheduling_preferences.updated_at`

func scanPreferenceRow(scan func(...any) error) (preferenceRow, error) {
	row := preferenceRow{}
	var builtIn, enabled int
	// owner 走 NullString：内置行 system_account_id 恒 NULL（全局只读例外）。
	var owner sql.NullString
	if err := scan(&row.id, &owner, &row.name, &row.chatMode, &row.configJSON,
		&builtIn, &enabled, &row.updatedAt); err != nil {
		return preferenceRow{}, err
	}
	row.systemAccountID = owner.String
	row.builtIn = builtIn == 1
	row.enabled = enabled == 1
	return row, nil
}

// preferenceOwnerClause mirrors ownerClause with the builtin exception: the
// scoped branch keeps every builtin row visible (`= ? OR IS NULL`；owner NULL
// 仅出现于内置行——自定义行 owner 非空), the unscoped-admin branch sees all
// rows. ok=false means the caller has no scope at all (empty result set).
func preferenceOwnerClause(access AccessScope, column string) (string, []any, bool) {
	if scoped := access.manageableID(); scoped != "" {
		return "(" + column + " = ? OR " + column + " IS NULL)", []any{scoped}, true
	}
	if !access.canAccessAll() {
		return "", nil, false
	}
	return "", nil, true
}

// keywordFilter mirrors the routestrategies name-prefix filter: case-sensitive
// literal prefix range (%/_ 非通配符)；PG 分支钉 C collation。
func (s *Store) keywordFilter(keyword string) (string, []any) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return "", nil
	}
	upper := textPrefixUpperBound(text)
	if s.pg {
		return `(scheduling_preferences.name COLLATE "C" >= ? AND scheduling_preferences.name COLLATE "C" < ?)`, []any{text, upper}
	}
	return "(scheduling_preferences.name >= ? AND scheduling_preferences.name < ?)", []any{text, upper}
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

// preferenceOrderBy is the fixed list/options ordering (契约：排序固定
// updated_at DESC, created_at DESC, id DESC).
const preferenceOrderBy = ` ORDER BY scheduling_preferences.updated_at DESC, scheduling_preferences.created_at DESC, scheduling_preferences.id DESC`

// ListPage mirrors the paginated list: owner scope (+builtin), keyword/chatMode/
// status filters, pageSize+1 probe and the batched boundApiKeyCount + owner
// name aggregation (单批聚合，避免 N+1).
func (s *Store) ListPage(ctx context.Context, access AccessScope, options ListOptions) (*ListPageResult, error) {
	ctx = ensureCtx(ctx)
	page, pageSize := options.Page, options.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 200 {
		pageSize = 200
	}
	clauses := []string{}
	args := []any{}
	if clause, clauseArgs, ok := preferenceOwnerClause(access, "scheduling_preferences.system_account_id"); ok {
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
	if options.ChatMode != "" {
		clauses = append(clauses, "scheduling_preferences.chat_mode = ?")
		args = append(args, options.ChatMode)
	}
	if options.Status != "" {
		clauses = append(clauses, "scheduling_preferences.enabled = ?")
		args = append(args, enabledFlag(options.Status))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+preferenceRowColumns+`
		FROM `+s.table("scheduling_preferences")+` scheduling_preferences`+where+preferenceOrderBy+`
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	preferences := []preferenceRow{}
	for rows.Next() {
		row, scanErr := scanPreferenceRow(rows.Scan)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		preferences = append(preferences, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hasMore := len(preferences) > pageSize
	if hasMore {
		preferences = preferences[:pageSize]
	}
	result := &ListPageResult{Items: []ListItem{}, Page: page, PageSize: pageSize}
	// pagedTotalUpperBound: rows before this page + visible + 1.
	result.Total = (page-1)*pageSize + len(preferences)
	if hasMore {
		result.Total++
	}
	result.HasMore = hasMore
	if len(preferences) == 0 {
		return result, nil
	}
	items, err := s.newListItems(ctx, preferences, access.canAccessAll())
	if err != nil {
		return nil, err
	}
	result.Items = items
	return result, nil
}

func enabledFlag(status string) int {
	if status == "disabled" {
		return 0
	}
	return 1
}

func (s *Store) emptyPage(page, pageSize int) *ListPageResult {
	return &ListPageResult{Items: []ListItem{}, Page: page, PageSize: pageSize}
}

// newListItems renders one page of rows: one batched bound-key count query and
// one batched owner-name query per page.
func (s *Store) newListItems(ctx context.Context, rows []preferenceRow, includeOwner bool) ([]ListItem, error) {
	ids := make([]string, 0, len(rows))
	owners := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
		owners = append(owners, row.systemAccountID)
	}
	counts, err := s.boundAPIKeyCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if includeOwner {
		names, err = s.systemAccountNames(ctx, owners)
		if err != nil {
			return nil, err
		}
	}
	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		document, err := storedSpeedConfigDocument(row)
		if err != nil {
			return nil, err
		}
		item := ListItem{
			ID:                   row.id,
			OwnerSystemAccountID: row.systemAccountID,
			Name:                 row.name,
			ChatMode:             row.chatMode,
			ChatSpeedFirstConfig: document,
			BuiltIn:              row.builtIn,
			Enabled:              row.enabled,
			BoundAPIKeyCount:     counts[row.id],
			UpdatedAt:            row.updatedAt,
		}
		if includeOwner {
			item.SystemAccountID = ptrString(row.systemAccountID)
			item.SystemAccountName = ptrString(names[row.systemAccountID])
		}
		items = append(items, item)
	}
	return items, nil
}

// storedSpeedConfigDocument renders chatSpeedFirstConfig：仅 speed_first 行出
// 现（cost_first 行存储列为 NULL，直接缺席）；损坏 JSON / 结构非法上抛显式错
// 误（不静默降级，契约「配置形状与速度参数契约」章）。
func storedSpeedConfigDocument(row preferenceRow) (*SpeedFirstConfigDocument, error) {
	if row.chatMode != ChatModeSpeedFirst {
		return nil, nil
	}
	if !row.configJSON.Valid || row.configJSON.String == "" {
		// speed_first 行缺配置不是合法存储态（写侧恒有配置或由缺省补齐）；
		// 缺列按损坏上抛，避免把初始化错误误渲染为成本优先。
		return nil, &routestrategies.ValidationError{Message: "调度偏好配置无效"}
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(row.configJSON.String), &document); err != nil {
		return nil, &routestrategies.ValidationError{Message: "调度偏好配置无效"}
	}
	normal, err := routestrategies.NormalizeNormalRoutingConfig(map[string]any{
		"schedulingPreference": ChatModeSpeedFirst,
		"firstByteDeadlineMs":  document["firstByteDeadlineMs"],
		"speedFirstConfig":     document["speedFirstConfig"],
	})
	if err != nil {
		return nil, err
	}
	return newSpeedConfigDocument(normal), nil
}

// newSpeedConfigDocument renders the normalized store/projection document from
// the strategy-side normalize output.
func newSpeedConfigDocument(normal *routestrategies.NormalRoutingConfig) *SpeedFirstConfigDocument {
	if normal == nil {
		return nil
	}
	document := &SpeedFirstConfigDocument{SpeedFirstConfig: normal.SpeedFirstConfig}
	if normal.FirstByteDeadlineMs != nil {
		document.FirstByteDeadlineMs = *normal.FirstByteDeadlineMs
	}
	return document
}

// speedConfigJSON renders the stored chat_speed_first_config_json (D2 形状：
// {firstByteDeadlineMs, speedFirstConfig}，规范化 struct 序列化——键序稳定，
// 供等值比较与存储).
func speedConfigJSON(document *SpeedFirstConfigDocument) (sql.NullString, error) {
	if document == nil {
		return sql.NullString{}, nil
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(encoded), Valid: true}, nil
}

// systemAccountNames mirrors the batched owner-name lookup.
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

// boundAPIKeyCounts 是 boundApiKeyCount 的批量聚合（未删除且绑定该偏好的
// API Key 精确计数，含停用 Key；Go 侧 api_keys 为硬删除，无 deleted_at）。
// 单条 GROUP BY 覆盖整页，避免 N+1。
func (s *Store) boundAPIKeyCounts(ctx context.Context, ids []string) (map[string]int, error) {
	counts := map[string]int{}
	unique := uniqueStrings(ids)
	if len(unique) == 0 {
		return counts, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT scheduling_preference_id, COUNT(1) AS count
		FROM `+s.table("api_keys")+` api_keys
		WHERE api_keys.scheduling_preference_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY api_keys.scheduling_preference_id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}

// Find returns the detail row (列表行同型投影，无独立 detail 投影) or nil when
// the preference is missing or outside the scope (route renders 404
// 调度偏好不存在).
func (s *Store) Find(ctx context.Context, id string, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	clause, clauseArgs, ok := preferenceOwnerClause(access, "scheduling_preferences.system_account_id")
	if !ok {
		return nil, nil
	}
	where := ""
	args := []any{id}
	if clause != "" {
		where = " AND " + clause
		args = append(args, clauseArgs...)
	}
	row, err := scanPreferenceRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+preferenceRowColumns+`
		FROM `+s.table("scheduling_preferences")+` scheduling_preferences
		WHERE scheduling_preferences.id = ?`+where+` LIMIT 1`), args...).Scan(targets...)
	})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items, err := s.newListItems(ctx, []preferenceRow{row}, access.canAccessAll())
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// OptionSummary mirrors the options row: admin rows add the owner summary
// (builtin rows absent), self rows stay owner-free; options 不返回配置 JSON、
// 绑定数量或 AI 账号信息.
type OptionSummary struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	ChatMode          string  `json:"chatMode"`
	BuiltIn           bool    `json:"builtIn"`
	Enabled           bool    `json:"enabled"`
	SystemAccountID   *string `json:"systemAccountId,omitempty"`
	SystemAccountName *string `json:"systemAccountName,omitempty"`
}

// OptionsQuery mirrors the /options query parsing (ids 去重封顶 50、keyword
// 名称前缀、limit 1..100、activeOnly 默认 true).
type OptionsQuery struct {
	IDs        []string
	Keyword    string
	Limit      int
	ActiveOnly bool
}

// ListOptionsPage mirrors the options read: owner-scoped summaries + the
// builtin exception; activeOnly=true keeps enabled rows (内置行 seed 恒启用，
// 自然通过启用过滤).
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
	if clause, clauseArgs, ok := preferenceOwnerClause(access, "scheduling_preferences.system_account_id"); ok {
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
		clauses = append(clauses, "scheduling_preferences.id IN ("+strings.Join(placeholders, ",")+")")
	}
	if clause, clauseArgs := s.keywordFilter(query.Keyword); clause != "" {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	if query.ActiveOnly {
		clauses = append(clauses, "scheduling_preferences.enabled = 1")
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT scheduling_preferences.id, scheduling_preferences.system_account_id,
		scheduling_preferences.name, scheduling_preferences.chat_mode, scheduling_preferences.built_in, scheduling_preferences.enabled
		FROM `+s.table("scheduling_preferences")+` scheduling_preferences`+where+preferenceOrderBy+`
		LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type optionRow struct {
		id, name, chatMode string
		systemAccountID    sql.NullString
		builtIn, enabled   bool
	}
	parsed := []optionRow{}
	var accountIDs []string
	for rows.Next() {
		row := optionRow{}
		var builtIn, enabled int
		if err := rows.Scan(&row.id, &row.systemAccountID, &row.name, &row.chatMode, &builtIn, &enabled); err != nil {
			return nil, err
		}
		row.builtIn = builtIn == 1
		row.enabled = enabled == 1
		parsed = append(parsed, row)
		accountIDs = append(accountIDs, row.systemAccountID.String)
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
			ID:       row.id,
			Name:     row.name,
			ChatMode: row.chatMode,
			BuiltIn:  row.builtIn,
			Enabled:  row.enabled,
		}
		if access.canAccessAll() {
			summary.SystemAccountID = ptrString(row.systemAccountID.String)
			summary.SystemAccountName = ptrString(names[row.systemAccountID.String])
		}
		options = append(options, summary)
	}
	return options, nil
}
