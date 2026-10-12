package accounts

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/studio"
)

// ListStudioAccountOptions 列出用户授权范围内的账户候选（GET /my-studio/
// accounts，媒体工作室设计 §4.1）。范围口径与 ListChatAccountOptions 完全
// 同两档：admin/super_admin 全量号池，普通用户仅自己名下（system_account_id
// 命中），deleted_at IS NULL、非授权实例戳行；不按启用状态过滤——全状态可
// 见（active/pending_test/disabled 等，status 为生效状态表达式原值），可派
// 发性由 /v1 受理结果裁决，前端标注不可调度状态。行投影追加
// accounts.capability_domain（AI 账户能力域拆分列，工作台域过滤与展示依
// 据）；domain 非空时按其精确过滤。排序 name ASC, id ASC 与下拉展示一致。
func (s *Store) ListStudioAccountOptions(ctx context.Context, viewerID string, adminScope bool, domain string) ([]studio.StudioAccountOption, error) {
	now := sqlQuoteISO(isoMillis(s.now()))
	effective := ownerEffectiveStatusSQL("accounts", now)
	clauses := ` WHERE accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL`
	args := []any{}
	if !adminScope {
		clauses += ` AND accounts.system_account_id = ?`
		args = append(args, viewerID)
	}
	domain = strings.TrimSpace(domain)
	if domain != "" {
		clauses += ` AND accounts.capability_domain = ?`
		args = append(args, domain)
	}
	rows, err := s.db.Query(s.bind(`SELECT accounts.id, accounts.name, accounts.provider_code,
			accounts.capability_domain, `+effective+`
		FROM `+s.table("accounts")+` accounts`+clauses+`
		ORDER BY accounts.name ASC, accounts.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []studio.StudioAccountOption{}
	for rows.Next() {
		var option studio.StudioAccountOption
		if err := rows.Scan(&option.AccountID, &option.Name, &option.ProviderCode, &option.Domain, &option.Status); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}
