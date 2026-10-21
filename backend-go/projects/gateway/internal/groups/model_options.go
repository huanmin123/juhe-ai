package groups

import (
	"context"
	"strings"
)

// GroupModelOption 是 CC Switch 导出模型参考候选（分组内账户真实支持的
// public_model）。分组只作为模型参考，不决定客户端类型或供应商。
type GroupModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListGroupModelOptions 返回分组内启用账户的 account_supported_models 真实
// models 行（标准分组直接取；超级分组取 enabled 成员的并集）。只读，按
// keyword 模糊过滤并按 public_model 排序；limit 由调用方钳制。
func (s *Store) ListGroupModelOptions(ctx context.Context, groupID string, keyword string, limit int, access AccessScope) ([]GroupModelOption, error) {
	ctx = ensureCtx(ctx)
	facts, err := s.loadGroupFacts(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && facts.systemAccountID != access.ViewerID {
		visible, visErr := s.hasActiveGroupAuthorization(ctx, s.db, groupID, access.ViewerID)
		if visErr != nil {
			return nil, visErr
		}
		if !visible {
			return []GroupModelOption{}, nil
		}
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	args := []any{groupID, groupID}
	keywordClause := ""
	if trimmed := strings.TrimSpace(keyword); trimmed != "" {
		keywordClause = " AND lower(models.public_model) LIKE ?"
		args = append(args, "%"+strings.ToLower(trimmed)+"%")
	}
	args = append(args, limit)
	query := `SELECT models.id, models.public_model
		FROM group_accounts ga
		INNER JOIN accounts a ON a.id = ga.account_id AND a.deleted_at IS NULL
		INNER JOIN account_supported_models asm ON asm.account_id = a.id
		INNER JOIN models ON models.id = asm.model_id
		WHERE ga.enabled = 1 AND models.status = 'active'
		  AND ga.group_id IN (
		    SELECT id FROM ` + s.table("groups") + ` WHERE id = ? AND group_kind = 'standard'
		    UNION
		    SELECT m.member_group_id FROM ` + s.table("super_group_members") + ` m
		      INNER JOIN ` + s.table("groups") + ` mg ON mg.id = m.member_group_id
		      WHERE m.super_group_id = ? AND m.enabled = 1 AND mg.enabled = 1 AND mg.group_kind = 'standard'
		  )` + keywordClause + `
		GROUP BY models.id, models.public_model
		ORDER BY lower(models.public_model) ASC, models.id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupModelOption{}
	for rows.Next() {
		var item GroupModelOption
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
