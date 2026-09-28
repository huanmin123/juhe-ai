package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// FindChatAccount resolves the AI 问答会话 account 绑定对象（数据范围内存在
// 性、启用口径、名称与 provider 事实、启用分组绑定的只读查询）。数据范围按
// ChatBindScope 收敛：admin/super_admin 读全量号池；普通用户只读自己名下
// （system_account_id 命中，与 ListOptionSummaries 的 owner 面一致；授权账
// 户以实例行落地且实例戳行本就被现有排除条件挡住）。账户不存在或范围外
// （含软删、授权实例戳行）时返回 (nil, nil)；Enabled 沿用 /accounts/options
// 仅启用账户的口径（ownerEffectiveStatusSQL = 'active'：status=active、可调
// 度、未冷却、未过期、无 account_expired 错误），与管理面账户下拉一致；账户
// 是否可调度由发送时网关候选解析做最终裁决。
func (s *Store) FindChatAccount(bindScope chat.ChatBindScope, accountID string) (*chat.ChatAccountRef, error) {
	now := sqlQuoteISO(isoMillis(s.now()))
	effective := ownerEffectiveStatusSQL("accounts", now)
	ownerClause, ownerArgs := chatOwnerScopeClause(bindScope)
	args := append([]any{accountID}, ownerArgs...)
	var name, providerCode string
	var enabled int
	err := s.db.QueryRow(s.bind(`SELECT accounts.name, accounts.provider_code,
			CASE WHEN `+effective+` = 'active' THEN 1 ELSE 0 END AS chat_enabled
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ? AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL`+ownerClause), args...).Scan(&name, &providerCode, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := &chat.ChatAccountRef{ID: accountID, Name: name, ProviderCode: providerCode, Enabled: enabled == 1}
	groupIDs, groupErr := s.chatEnabledGroupIDs(accountID)
	if groupErr != nil {
		return nil, groupErr
	}
	ref.EnabledGroupIDs = groupIDs
	return ref, nil
}

// chatOwnerScopeClause 是 chat 绑定账户查询的普通用户数据范围子句：非 admin
// 追加 system_account_id 命中（admin/super_admin 全量号池，无额外条件）。
func chatOwnerScopeClause(bindScope chat.ChatBindScope) (string, []any) {
	if bindScope.IsAdmin {
		return "", nil
	}
	return ` AND accounts.system_account_id = ?`, []any{bindScope.ViewerID}
}

// ListChatAccountOptions 列出用户授权范围内全部可派发账户（GET /my-chat/
// accounts，AI 问答会话账户唯一绑定设计 §5.2），与 FindChatAccount 完全同
// 口径：数据范围内（admin/super_admin 全量号池，普通用户仅自己名下）
// deleted_at IS NULL、非授权实例戳行（authorization_instance_authorization_id
// IS NULL）且 ownerEffectiveStatusSQL = 'active'（status=active、可调度、未
// 冷却、未过期、无 account_expired 错误）。投影 id/name/provider_code/status
// （status 为生效状态表达式取值，查询过滤后恒 'active'，保留字段供后续工具
// 绑定阶段展示绑定失效态），不暴露归属、授权状态等管理面字段；排序
// name ASC, id ASC 与下拉展示一致。
func (s *Store) ListChatAccountOptions(ctx context.Context, bindScope chat.ChatBindScope) ([]chat.ChatAccountOption, error) {
	now := sqlQuoteISO(isoMillis(s.now()))
	effective := ownerEffectiveStatusSQL("accounts", now)
	ownerClause, ownerArgs := chatOwnerScopeClause(bindScope)
	args := append([]any{}, ownerArgs...)
	rows, err := s.db.Query(s.bind(`SELECT accounts.id, accounts.name, accounts.provider_code, `+effective+`
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL
			AND `+effective+` = 'active'`+ownerClause+`
		ORDER BY accounts.name ASC, accounts.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []chat.ChatAccountOption{}
	for rows.Next() {
		var option chat.ChatAccountOption
		if err := rows.Scan(&option.ID, &option.Name, &option.ProviderCode, &option.Status); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

// chatEnabledGroupIDs lists the account's enabled group bindings (模型作用域
// 经运行时账户快照按 ID 收敛时使用). Deterministic order by group_id.
func (s *Store) chatEnabledGroupIDs(accountID string) ([]string, error) {
	rows, err := s.db.Query(s.bind(`SELECT group_accounts.group_id
		FROM `+s.table("group_accounts")+` group_accounts
		WHERE group_accounts.account_id = ? AND group_accounts.enabled = 1
		ORDER BY group_accounts.group_id ASC`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groupIDs := []string{}
	for rows.Next() {
		var groupID string
		if err := rows.Scan(&groupID); err != nil {
			return nil, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	return groupIDs, rows.Err()
}
