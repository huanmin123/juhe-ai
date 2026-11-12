// Security-policy read paths: paginated list, options and owner-scoped detail
// over security_policies with the builtin-row global read exception (builtin
// rows carry system_account_id NULL and stay visible through every scope,
// read-only). Mirrors the schedulingpreferences store layout (API Key 安全策略
// 与出站内容脱敏设计 §4.3 列表/详情/options 字段；绑定计数
// boundApiKeyCount 与调度偏好 boundAPIKeyCounts 同构，落在
// api_keys.security_policy_id)。
package securitypolicies

import (
	"context"
	"database/sql"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

// ListItem mirrors the list/detail/create row projection: 列表 / 详情字段
// `id`、`name`、`description`、`rules`（规范化数组回显）、`builtIn`、
// `boundApiKeyCount`、`protectionCount`、`updatedAt`；管理侧行附加
// systemAccountId / systemAccountName owner 摘要（内置行 owner NULL 自然缺
// 席），个人侧不返回管理 owner 字段。protectionCount 是运行态防护次数累计
// （写后缓存设计 §4.3.1：直接投影 security_policies.protection_count PG 列，
// Redis 增量由 gateway 进程内刷写任务周期合并；store 纯 SQL 不碰 Redis——
// 内置行恒 0）。
type ListItem struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Description       *string        `json:"description,omitempty"`
	Rules             []SecurityRule `json:"rules"`
	BuiltIn           bool           `json:"builtIn"`
	BoundAPIKeyCount  int            `json:"boundApiKeyCount"`
	ProtectionCount   int64          `json:"protectionCount"`
	UpdatedAt         string         `json:"updatedAt"`
	SystemAccountID   *string        `json:"systemAccountId,omitempty"`
	SystemAccountName *string        `json:"systemAccountName,omitempty"`

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

// ListOptions mirrors the route-level normalized list query (keyword trimmed
// 名称前缀过滤).
type ListOptions struct {
	Page     int
	PageSize int
	Keyword  string
}

// policyRow is the shared scan target for list/detail rows.
type policyRow struct {
	id              string
	systemAccountID string
	name            string
	description     sql.NullString
	rulesJSON       string
	builtIn         bool
	protectionCount int64
	updatedAt       string
}

const policyRowColumns = `security_policies.id, security_policies.system_account_id, security_policies.name,
	security_policies.description, security_policies.rules_json,
	security_policies.built_in, security_policies.protection_count,
	security_policies.updated_at`

func scanPolicyRow(scan func(...any) error) (policyRow, error) {
	row := policyRow{}
	var builtIn int
	// owner 走 NullString：内置行 system_account_id 恒 NULL（全局只读例外）。
	var owner sql.NullString
	var updatedAt dbtime.Nullable
	if err := scan(&row.id, &owner, &row.name, &row.description, &row.rulesJSON,
		&builtIn, &row.protectionCount, &updatedAt); err != nil {
		return policyRow{}, err
	}
	row.systemAccountID = owner.String
	row.builtIn = builtIn == 1
	row.updatedAt = updatedAt.APIString()
	return row, nil
}

// policyOwnerClause mirrors the schedulingpreferences preferenceOwnerClause
// with the builtin exception: the scoped branch keeps every builtin row
// visible (`= ? OR IS NULL`；owner NULL 仅出现于内置行——自定义行 owner 非空),
// the unscoped-admin branch sees all rows. ok=false means the caller has no
// scope at all (empty result set).
func policyOwnerClause(access AccessScope, column string) (string, []any, bool) {
	if scoped := access.manageableID(); scoped != "" {
		return "(" + column + " = ? OR " + column + " IS NULL)", []any{scoped}, true
	}
	if !access.canAccessAll() {
		return "", nil, false
	}
	return "", nil, true
}

// keywordFilter mirrors the schedulingpreferences name-prefix filter:
// case-sensitive literal prefix range (%/_ 非通配符)，钉 C collation。
func (s *Store) keywordFilter(keyword string) (string, []any) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return "", nil
	}
	upper := textPrefixUpperBound(text)
	return `(security_policies.name COLLATE "C" >= ? AND security_policies.name COLLATE "C" < ?)`, []any{text, upper}
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

// policyOrderBy is the fixed list/options ordering (契约：排序固定
// updated_at DESC, created_at DESC, id DESC).
const policyOrderBy = ` ORDER BY security_policies.updated_at DESC, security_policies.created_at DESC, security_policies.id DESC`

// ListPage mirrors the paginated list: owner scope (+builtin), keyword filter,
// pageSize+1 probe and the batched boundApiKeyCount + owner name aggregation
// (单批聚合，避免 N+1).
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
	if clause, clauseArgs, ok := policyOwnerClause(access, "security_policies.system_account_id"); ok {
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
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+policyRowColumns+`
		FROM `+s.table("security_policies")+` security_policies`+where+policyOrderBy+`
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	policies := []policyRow{}
	for rows.Next() {
		row, scanErr := scanPolicyRow(rows.Scan)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		policies = append(policies, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hasMore := len(policies) > pageSize
	if hasMore {
		policies = policies[:pageSize]
	}
	result := &ListPageResult{Items: []ListItem{}, Page: page, PageSize: pageSize}
	// pagedTotalUpperBound: rows before this page + visible + 1.
	result.Total = (page-1)*pageSize + len(policies)
	if hasMore {
		result.Total++
	}
	result.HasMore = hasMore
	if len(policies) == 0 {
		return result, nil
	}
	items, err := s.newListItems(ctx, policies, access.canAccessAll())
	if err != nil {
		return nil, err
	}
	result.Items = items
	return result, nil
}

func (s *Store) emptyPage(page, pageSize int) *ListPageResult {
	return &ListPageResult{Items: []ListItem{}, Page: page, PageSize: pageSize}
}

// newListItems renders one page of rows: one batched bound-key count query and
// one batched owner-name query per page.
func (s *Store) newListItems(ctx context.Context, rows []policyRow, includeOwner bool) ([]ListItem, error) {
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
		document, err := parseStoredRulesDocument(row.rulesJSON)
		if err != nil {
			return nil, err
		}
		item := ListItem{
			ID:                   row.id,
			OwnerSystemAccountID: row.systemAccountID,
			Name:                 row.name,
			Description:          nullPtrString(row.description),
			Rules:                document.Rules,
			BuiltIn:              row.builtIn,
			BoundAPIKeyCount:     counts[row.id],
			ProtectionCount:      row.protectionCount,
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

// boundAPIKeyCounts 是 boundApiKeyCount 的批量聚合（绑定该策略的 API Key 精确
// 计数，含停用 Key；Go 侧 api_keys 为硬删除，无 deleted_at）。单条 GROUP BY
// 覆盖整页，避免 N+1。
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
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT security_policy_id, COUNT(1) AS count
		FROM `+s.table("api_keys")+` api_keys
		WHERE api_keys.security_policy_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY api_keys.security_policy_id`), args...)
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
// the policy is missing or outside the scope (route renders 404 安全策略不存在).
func (s *Store) Find(ctx context.Context, id string, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	clause, clauseArgs, ok := policyOwnerClause(access, "security_policies.system_account_id")
	if !ok {
		return nil, nil
	}
	where := ""
	args := []any{id}
	if clause != "" {
		where = " AND " + clause
		args = append(args, clauseArgs...)
	}
	row, err := scanPolicyRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+policyRowColumns+`
		FROM `+s.table("security_policies")+` security_policies
		WHERE security_policies.id = ?`+where+` LIMIT 1`), args...).Scan(targets...)
	})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items, err := s.newListItems(ctx, []policyRow{row}, access.canAccessAll())
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// OptionSummary mirrors the options row: admin rows add the owner summary
// (builtin rows absent), self rows stay owner-free; options 不返回规则集 JSON、
// 绑定数量或 AI 账号信息。protectionCount 是运行态防护次数（写后缓存设计
// §4.3.1：直接投影 protection_count PG 列；内置行恒 0）。
type OptionSummary struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	BuiltIn           bool    `json:"builtIn"`
	ProtectionCount   int64   `json:"protectionCount"`
	SystemAccountID   *string `json:"systemAccountId,omitempty"`
	SystemAccountName *string `json:"systemAccountName,omitempty"`
}

// OptionsQuery mirrors the /options query parsing (ids 去重封顶 50、keyword
// 名称前缀、limit 1..100).
type OptionsQuery struct {
	IDs     []string
	Keyword string
	Limit   int
}

// ListOptionsPage mirrors the options read: owner-scoped summaries + the
// builtin exception（全部行可选）.
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
	if clause, clauseArgs, ok := policyOwnerClause(access, "security_policies.system_account_id"); ok {
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
		clauses = append(clauses, "security_policies.id IN ("+strings.Join(placeholders, ",")+")")
	}
	if clause, clauseArgs := s.keywordFilter(query.Keyword); clause != "" {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT security_policies.id, security_policies.system_account_id,
		security_policies.name, security_policies.built_in, security_policies.protection_count
		FROM `+s.table("security_policies")+` security_policies`+where+policyOrderBy+`
		LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type optionRow struct {
		id, name        string
		systemAccountID sql.NullString
		builtIn         bool
		protectionCount int64
	}
	parsed := []optionRow{}
	var accountIDs []string
	for rows.Next() {
		row := optionRow{}
		var builtIn int
		if err := rows.Scan(&row.id, &row.systemAccountID, &row.name, &builtIn, &row.protectionCount); err != nil {
			return nil, err
		}
		row.builtIn = builtIn == 1
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
			ID:              row.id,
			Name:            row.name,
			BuiltIn:         row.builtIn,
			ProtectionCount: row.protectionCount,
		}
		if access.canAccessAll() {
			summary.SystemAccountID = ptrString(row.systemAccountID.String)
			summary.SystemAccountName = ptrString(names[row.systemAccountID.String])
		}
		options = append(options, summary)
	}
	return options, nil
}

// storedRulesJSON 是 mutations 比较存储规则集是否变化的统一入口（规范字节比
// 较；损坏存储按规则集错误上抛）。
func storedRulesJSON(row policyRow) (string, error) {
	if _, err := parseStoredRulesDocument(row.rulesJSON); err != nil {
		return "", err
	}
	return row.rulesJSON, nil
}
