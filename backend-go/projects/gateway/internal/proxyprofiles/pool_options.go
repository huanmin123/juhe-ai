package proxyprofiles

// Pool options arm of the shared account-form dropdown (代理池设计 §8.2):
// the pool SQL lives inside the proxyprofiles Store so the /proxies/options
// endpoint stays one handler without a cross-package dependency cycle with
// proxypools. Pool items carry enabled pools only (§8.7：普通用户仅 enabled
// 池摘要；与代理 options 的 enabled 口径一致), the id/name/kind/enabled
// summary plus member counts — never members, host, port or credentials.

import (
	"context"
	"strings"
)

// poolOptionCountColumns renders the per-pool member counts:
//   - memberCount: every proxy_pool_members row;
//   - assignableMemberCount: §6.2 可分配成员的 SQL 近似（成员行 enabled、
//     代理档案 enabled、pool_health_status NULL 按 unknown 且不得为 dead、
//     连续失败未达 3）；解密/协议合法性只能在解析期校验，不在 SQL 内。
const poolOptionCountColumns = `
		(SELECT COUNT(*) FROM juhe_business.proxy_pool_members m WHERE m.pool_id = p.id) AS member_count,
		(SELECT COUNT(*) FROM juhe_business.proxy_pool_members m
			JOIN juhe_business.proxy_profiles pr ON pr.id = m.proxy_profile_id
			WHERE m.pool_id = p.id AND m.enabled = true AND pr.enabled = true
				AND (pr.pool_health_status IS NULL OR pr.pool_health_status <> 'dead')
				AND pr.pool_health_failure_streak < 3) AS assignable_member_count`

// ListPoolOptions returns enabled pool summaries for the dropdown: the
// keyword window plus the explicitly selected ids, merged in the canonical
// option order. enabled=true on both arms mirrors the profile options
// contract (selected-but-disabled pools stay hidden).
func (s *Store) ListPoolOptions(ctx context.Context, keyword string, limit int, selectedPoolIds []string) ([]OptionSummary, error) {
	merged, err := s.poolOptionRows(ctx, keyword, limit, selectedPoolIds)
	if err != nil {
		return nil, err
	}
	return optionSummaries(merged), nil
}

// poolOptionRows merges the enabled-pool keyword window with the explicitly
// selected pool ids, name ASC, updated_at DESC, id ASC.
func (s *Store) poolOptionRows(ctx context.Context, keyword string, limit int, selectedPoolIds []string) ([]optionRow, error) {
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
		SELECT p.id, p.name, p.enabled, ` +
		s.revisionSelectExpression() + `,` + poolOptionCountColumns + `
		FROM juhe_business.proxy_pools p
		WHERE p.enabled = true` + keywordClause + `
		ORDER BY p.name ASC, p.updated_at DESC, p.id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(windowQuery), append(params, limit)...)
	if err != nil {
		return nil, err
	}
	windowRows, err := scanPoolOptionRows(rows)
	if err != nil {
		return nil, err
	}
	selectedRows := []optionRow{}
	if len(selectedPoolIds) > 0 {
		selectedQuery := `
			SELECT p.id, p.name, p.enabled, ` +
			s.revisionSelectExpression() + `,` + poolOptionCountColumns + `
			FROM juhe_business.proxy_pools p
			WHERE p.enabled = true AND p.id IN (` + placeholders(len(selectedPoolIds)) + `)
			ORDER BY p.name ASC, p.updated_at DESC, p.id ASC`
		selectedQueryRows, err := s.db.QueryContext(ctx, s.bind(selectedQuery), idsToAny(selectedPoolIds)...)
		if err != nil {
			return nil, err
		}
		selectedRows, err = scanPoolOptionRows(selectedQueryRows)
		if err != nil {
			return nil, err
		}
	}
	return mergeOptionRows(windowRows, selectedRows), nil
}

func scanPoolOptionRows(rows rowScanner) ([]optionRow, error) {
	defer rows.Close()
	out := []optionRow{}
	for rows.Next() {
		var (
			id, name    string
			enabled     any
			updatedAt   string
			memberCount int
			assignable  int
		)
		if err := rows.Scan(&id, &name, &enabled, &updatedAt, &memberCount, &assignable); err != nil {
			return nil, err
		}
		out = append(out, optionRow{
			id:                    id,
			name:                  name,
			kind:                  OptionKindPool,
			updatedAt:             updatedAt,
			memberCount:           memberCount,
			assignableMemberCount: assignable,
		})
	}
	return out, rows.Err()
}
