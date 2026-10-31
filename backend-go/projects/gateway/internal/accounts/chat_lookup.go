package accounts

import (
	"context"
	"database/sql"
	"errors"
)

// AccountScope 是 AI 账户演练场/会话绑定查询的数据范围（原 chat.ChatBindScope
// 随 my-chat 会话面退役迁为 accounts 本地类型，字段语义不变）：admin/
// super_admin 读全量号池；普通用户只读自己名下（ViewerID 命中）。
type AccountScope struct {
	// ViewerID 是发起查询的登录系统账户 ID（普通用户的数据范围键）。
	ViewerID string
	// IsAdmin 表示发起者是 admin/super_admin（全量号池范围）。
	IsAdmin bool
}

// AccountRef 是数据范围内单个 AI 账户的事实投影：名称/provider 事实、生效
// 状态投影与启用分组绑定。
type AccountRef struct {
	ID              string
	Name            string
	ProviderCode    string
	Enabled         bool
	EnabledGroupIDs []string
}

// AccountOption 是账户选择下拉的最小条目投影。
type AccountOption struct {
	ID           string
	Name         string
	ProviderCode string
	Status       string
}

// FindChatAccount resolves the AI 账户绑定对象（数据范围内存在性、名称与
// provider 事实、启用分组绑定的只读查询；演练场账户端口消费）。数据范围按
// AccountScope 收敛：admin/super_admin 读全量号池；普通用户只读自己名下
// （system_account_id 命中，与 ListOptionSummaries 的 owner 面一致；授权账
// 户以实例行落地且实例戳行本就被现有排除条件挡住）。账户不存在或范围外
// （含软删、授权实例戳行）时返回 (nil, nil)。2026-10-10 修订：绑定与发送
// 不再因账户生效状态拒绝（禁用/限流/冷却/过期账户均可测试），Enabled 沿用
// 生效 active 口径为账户状态投影的保留语义，发送/模型列表链路不消费该字段
// （站内工具绑定候选机制已随 2026-10-12 简化退场）。
func (s *Store) FindChatAccount(bindScope AccountScope, accountID string) (*AccountRef, error) {
	now := sqlQuoteISO(isoMillis(s.now()))
	effective := ownerEffectiveStatusSQL("accounts", now)
	ownerClause, ownerArgs := accountOwnerScopeClause(bindScope)
	args := append([]any{accountID}, ownerArgs...)
	var name, providerCode string
	var enabled int
	err := s.db.QueryRow(s.bind(`SELECT accounts.name, COALESCE(accounts.vendor_code, '') AS provider_code,
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
	ref := &AccountRef{ID: accountID, Name: name, ProviderCode: providerCode, Enabled: enabled == 1}
	groupIDs, groupErr := s.chatEnabledGroupIDs(accountID)
	if groupErr != nil {
		return nil, groupErr
	}
	ref.EnabledGroupIDs = groupIDs
	return ref, nil
}

// accountOwnerScopeClause 是绑定账户查询的普通用户数据范围子句：非 admin
// 追加 system_account_id 命中（admin/super_admin 全量号池，无额外条件）。
func accountOwnerScopeClause(bindScope AccountScope) (string, []any) {
	if bindScope.IsAdmin {
		return "", nil
	}
	return ` AND accounts.system_account_id = ?`, []any{bindScope.ViewerID}
}

// ListChatAccountOptions 列出用户授权范围内全部未删除账户（演练场账户列表
// 端口的账户查询，与 FindChatAccount 完全同口径：数据范围内（admin/
// super_admin 全量号池，普通用户仅自己名下）deleted_at IS NULL、非授权实例
// 戳行（authorization_instance_authorization_id IS NULL），不设可用性过滤
// ——禁用、错误、限流、冷却、过期、禁调度等全部状态账户均可见可选（账户被
// 禁用时正是不稳定、需要测试的场景）。投影 id/name/provider_code/status
// （status 为生效状态表达式原值：active/pending_test/disabled/error/
// rate_limited/temporary_unavailable/quality_isolated，含冷却→
// temporary_unavailable、过期/禁调度→disabled 的合成，供前端状态标注），不
// 暴露归属、授权状态等管理面字段；排序 name ASC, id ASC 与下拉展示一致。
func (s *Store) ListChatAccountOptions(ctx context.Context, bindScope AccountScope) ([]AccountOption, error) {
	now := sqlQuoteISO(isoMillis(s.now()))
	effective := ownerEffectiveStatusSQL("accounts", now)
	ownerClause, ownerArgs := accountOwnerScopeClause(bindScope)
	args := append([]any{}, ownerArgs...)
	rows, err := s.db.Query(s.bind(`SELECT accounts.id, accounts.name, COALESCE(accounts.vendor_code, '') AS provider_code, `+effective+`
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL`+ownerClause+`
		ORDER BY accounts.name ASC, accounts.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []AccountOption{}
	for rows.Next() {
		var option AccountOption
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
