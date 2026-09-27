package accounts

import (
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// FindChatAccount resolves the AI 问答会话 account 绑定对象（存在性、启用口
// 径、名称与 provider 事实、启用分组绑定的只读查询）。账户不存在（含软删、
// 授权实例戳行）时返回 (nil, nil)；Enabled 沿用 /accounts/options 仅启用
// 账户的口径（ownerEffectiveStatusSQL = 'active'：status=active、可调度、
// 未冷却、未过期、无 account_expired 错误），与管理面账户下拉一致；账户是
// 否可调度由发送时网关候选解析做最终裁决。
func (s *Store) FindChatAccount(accountID string) (*chat.ChatAccountRef, error) {
	now := sqlQuoteISO(isoMillis(s.now()))
	effective := ownerEffectiveStatusSQL("accounts", now)
	var name, providerCode string
	var enabled int
	err := s.db.QueryRow(s.bind(`SELECT accounts.name, accounts.provider_code,
			CASE WHEN `+effective+` = 'active' THEN 1 ELSE 0 END AS chat_enabled
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ? AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL`), accountID).Scan(&name, &providerCode, &enabled)
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
