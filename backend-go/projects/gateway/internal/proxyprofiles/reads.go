package proxyprofiles

// Read half of the proxy family: paged admin list plus the shared /options
// endpoint (Node listProxiesPageAsync / listProxyOptionsAsync).

import (
	"context"
	"strings"
)

// ListPage mirrors listProxiesPageAsync: pageSize 1..200 (default 20), page
// bounded by the 1001-row window, keyword prefix filter, updated_at DESC.
func (s *Store) ListPage(ctx context.Context, page, pageSize int, keyword string) (ListResult, error) {
	pageSize = clampInt(pageSize, 1, 200)
	maxPage := (1001 - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page < 1 {
		page = 1
	}
	if page > maxPage {
		page = maxPage
	}
	keyword = strings.TrimSpace(keyword)
	clauses := ""
	params := []any{}
	if keyword != "" {
		clauses = " WHERE " + s.keywordFilter()
		params = s.keywordFilterParams(keyword)
	}
	query := `
		SELECT ` + s.summarySelectColumns() + `
		FROM ` + s.table("proxy_profiles") + clauses + `
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), append(params, pageSize+1, (page-1)*pageSize)...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	profiles := []ProfileSummary{}
	for rows.Next() {
		profile, scanErr := s.scanProfile(rows)
		if scanErr != nil {
			return ListResult{}, scanErr
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	hasMore := false
	if len(profiles) > pageSize {
		profiles = profiles[:pageSize]
		hasMore = true
	}
	total := (page-1)*pageSize + len(profiles)
	if hasMore {
		total++
	}
	return ListResult{
		Items:    profiles,
		Total:    total,
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// ListOptions mirrors listProxyOptionsAsync: enabled profiles matching the
// keyword window plus the explicitly selected ids, merged name-ordered.
func (s *Store) ListOptions(ctx context.Context, keyword string, limit int, selectedIds []string) ([]OptionSummary, error) {
	keyword = strings.TrimSpace(keyword)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	keywordClause := ""
	params := []any{}
	if keyword != "" {
		keywordClause = " AND " + s.keywordFilter()
		params = s.keywordFilterParams(keyword)
	}
	windowQuery := `
		SELECT ` + s.optionSelectColumns() + `
		FROM ` + s.table("proxy_profiles") + `
		WHERE ` + s.enabledFilter() + keywordClause + `
		ORDER BY name ASC, updated_at DESC, id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(windowQuery), append(params, limit)...)
	if err != nil {
		return nil, err
	}
	windowRows, err := scanOptionRows(rows)
	if err != nil {
		return nil, err
	}
	selectedRows := []optionRow{}
	if len(selectedIds) > 0 {
		selectedQuery := `
			SELECT ` + s.optionSelectColumns() + `
			FROM ` + s.table("proxy_profiles") + `
			WHERE ` + s.enabledFilter() + ` AND id IN (` + placeholders(len(selectedIds)) + `)
			ORDER BY name ASC, updated_at DESC, id ASC`
		selectedQueryRows, err := s.db.QueryContext(ctx, s.bind(selectedQuery), idsToAny(selectedIds)...)
		if err != nil {
			return nil, err
		}
		selectedRows, err = scanOptionRows(selectedQueryRows)
		if err != nil {
			return nil, err
		}
	}
	return mergeProxyOptionRows(windowRows, selectedRows), nil
}

type optionRow struct {
	id        string
	name      string
	typeCode  string
	updatedAt string
}

// rowScanner is the query-result surface the option scans consume.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

func scanOptionRows(rows rowScanner) ([]optionRow, error) {
	defer rows.Close()
	out := []optionRow{}
	for rows.Next() {
		var (
			id, name, typeCode string
			enabled            any
			updatedAt          string
		)
		if err := rows.Scan(&id, &name, &typeCode, &enabled, &updatedAt); err != nil {
			return nil, err
		}
		out = append(out, optionRow{id: id, name: name, typeCode: typeCode, updatedAt: updatedAt})
	}
	return out, rows.Err()
}

// mergeProxyOptionRows mirrors mergeProxyOptionRows: enabled-only, name ASC,
// updated_at DESC, id ASC.
func mergeProxyOptionRows(windowRows, selectedRows []optionRow) []OptionSummary {
	byID := map[string]optionRow{}
	order := []string{}
	for _, row := range append(append([]optionRow{}, windowRows...), selectedRows...) {
		if _, ok := byID[row.id]; !ok {
			order = append(order, row.id)
		}
		byID[row.id] = row
	}
	options := make([]OptionSummary, 0, len(order))
	for _, id := range order {
		row := byID[id]
		options = append(options, OptionSummary{ID: row.id, Name: row.name, Type: row.typeCode, Enabled: true})
	}
	// Stable sort by name, then updated_at desc, then id asc.
	for i := 1; i < len(options); i++ {
		for j := i; j > 0; j-- {
			left, right := options[j-1], options[j]
			if left.Name != right.Name {
				if left.Name > right.Name {
					options[j-1], options[j] = options[j], options[j-1]
				}
				continue
			}
			leftUpdated := byID[left.ID].updatedAt
			rightUpdated := byID[right.ID].updatedAt
			if leftUpdated != rightUpdated {
				if leftUpdated < rightUpdated {
					options[j-1], options[j] = options[j], options[j-1]
				}
				continue
			}
			if left.ID > right.ID {
				options[j-1], options[j] = options[j], options[j-1]
			}
		}
	}
	return options
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

// enabledFilter renders the enabled predicate per dialect: PG stores
// `enabled boolean` (Node listProxyOptionsAsync proxy.repository.ts:263,273
// uses `enabled = true`), SQLite stores the 0/1 integer (readonly variant
// proxy.repository.ts:234,239 uses `enabled = 1`).
func (s *Store) enabledFilter() string {
	if s.pg {
		return "enabled = true"
	}
	return "enabled = 1"
}

// keywordFilter renders the name prefix window per dialect (Node
// buildProxyKeywordFilter proxy.repository.ts:385-392 for SQLite and
// buildProxyKeywordFilterAsync proxy.repository.ts:394-401 for PG: the PG
// variant pins byte collation and adds starts_with because the C collation
// range scan alone misses locale-sorted rows).
func (s *Store) keywordFilter() string {
	if s.pg {
		return `(name COLLATE "C" >= ? AND name COLLATE "C" < ? AND starts_with(name, ?))`
	}
	return "(name >= ? AND name < ?)"
}

// keywordFilterParams mirrors the clause shape above: the PG variant binds the
// keyword a third time for starts_with (proxy.repository.ts:399).
func (s *Store) keywordFilterParams(keyword string) []any {
	upper := textPrefixUpperBound(keyword)
	if s.pg {
		return []any{keyword, upper, keyword}
	}
	return []any{keyword, upper}
}

// summarySelectColumns mirrors proxySummarySelectColumns
// (proxy.repository.ts:433-453): on PG the timestamps are textified in SQL —
// updated_at keeps the database's microsecond text (US pattern, line 450) and
// last_tested_at mirrors the JS Date.toISOString() millisecond shape the
// driver path produced (normalizePostgresRows database-client.ts:460-476, MS
// pattern). Scanning stays string-typed on both dialects.
func (s *Store) summarySelectColumns() string {
	if s.pg {
		return `id, name, description, type, host, port, username, enabled, test_status,
			latency_ms, outbound_ip, outbound_region, last_test_message,
			to_char(last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS last_tested_at,
			to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at`
	}
	return `id, name, description, type, host, port, username, enabled, test_status,
		latency_ms, outbound_ip, outbound_region, last_test_message, last_tested_at, updated_at`
}

// optionSelectColumns renders the options projection (Node
// listProxyOptionsAsync proxy.repository.ts:261-265): PG textifies updated_at
// with the same US pattern used for the summary revision so the option merge
// ordering keeps comparing UTC text.
func (s *Store) optionSelectColumns() string {
	if s.pg {
		return `id, name, type, enabled,
			to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at`
	}
	return "id, name, type, enabled, updated_at"
}

func textPrefixUpperBound(value string) string {
	chars := []rune(value)
	for index := len(chars) - 1; index >= 0; index-- {
		codePoint := chars[index]
		if codePoint >= 0x10ffff {
			continue
		}
		return string(chars[:index]) + string(codePoint+1)
	}
	return value + "\U0010ffff"
}

func placeholders(count int) string {
	if count < 1 {
		count = 1
	}
	pieces := make([]string, count)
	for index := range pieces {
		pieces[index] = "?"
	}
	return strings.Join(pieces, ",")
}

func idsToAny(ids []string) []any {
	values := make([]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, id)
	}
	return values
}
