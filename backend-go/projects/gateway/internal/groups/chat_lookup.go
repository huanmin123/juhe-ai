package groups

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// chatBindOptionLimit 是普通用户下拉臂复用 Store.Options 的页大小：Options
// 内部 LIMIT clamp <1→50、>500→500（options.go:216-224），直接传 500 取满
// 该上限，覆盖普通用户自有+授权分组在单页内的全集。
const chatBindOptionLimit = 500

// FindChatGroup resolves the AI 问答会话 group 绑定对象（数据范围内存在性、
// enabled 与名称快照的只读查询）。数据范围按 ChatBindScope 收敛：
// admin/super_admin 读全量号池；普通用户只读自有行或被授权行。范围外与不存
// 在同型返回 (nil, nil)；调用方（chat 创建与模型作用域校验）据 Enabled 拒绝
// 停用分组。enabled 在两种方言中均为 integer。
func (s *Store) FindChatGroup(bindScope chat.ChatBindScope, groupID string) (*chat.ChatGroupRef, error) {
	if !bindScope.IsAdmin {
		return s.findChatGroupForViewer(bindScope.ViewerID, groupID)
	}
	var name string
	var enabled int
	err := s.db.QueryRow(s.bind(`SELECT name, enabled FROM `+s.table("groups")+` WHERE id = ?`), groupID).Scan(&name, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &chat.ChatGroupRef{ID: groupID, Name: name, Enabled: enabled == 1}, nil
}

// findChatGroupForViewer 是普通用户臂的单行查询：owner 臂（自有行）enabled
// 取 g.enabled；授权臂（resource_authorizations LEFT JOIN
// group_authorization_settings，status IN ('active','paused','expired')，与
// options.go:145-157 的 authorizedArmWhere / authorizationSettingsJoin 谓词
// 同源）enabled 取 CASE WHEN g.enabled=1 THEN COALESCE(s.enabled,1) ELSE 0
// END——保证 per-grantee 停用的授权分组返回 Enabled=false 而非不可见。两臂
// 任一命中即可见（owner 臂优先），同组多授权行按 MAX 聚合出确定性结果。
func (s *Store) findChatGroupForViewer(viewerID, groupID string) (*chat.ChatGroupRef, error) {
	var name string
	var enabled int
	err := s.db.QueryRow(s.bind(`SELECT g.name,
			MAX(CASE
				WHEN g.system_account_id = ? THEN g.enabled
				ELSE CASE WHEN g.enabled = 1 THEN COALESCE(s.enabled, 1) ELSE 0 END
			END) AS chat_enabled
		FROM `+s.table("groups")+` g
		LEFT JOIN `+s.table("resource_authorizations")+` ra
			ON ra.resource_type = 'group'
			AND ra.resource_id = g.id
			AND ra.grantee_system_account_id = ?
			AND ra.status IN ('active', 'paused', 'expired')
		LEFT JOIN `+s.table("group_authorization_settings")+` s
			ON s.authorization_id = ra.id
			AND s.system_account_id = ra.grantee_system_account_id
			AND s.group_id = ra.resource_id
		WHERE g.id = ? AND (g.system_account_id = ? OR ra.id IS NOT NULL)
		GROUP BY g.id`), viewerID, viewerID, groupID, viewerID).Scan(&name, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &chat.ChatGroupRef{ID: groupID, Name: name, Enabled: enabled == 1}, nil
}

// ListChatGroupOptions 列出 AI 问答新建会话绑定下拉的分组最小摘要（仅有效
// enabled 行）。数据范围与 FindChatGroup 同口径：admin/super_admin 读全量
// enabled = 1（groups 表无软删列，物理删即不可绑定）；普通用户读自有 + 被
// 授权行（复用 Store.Options 的 owner + 授权 union，授权臂 per-grantee
// settings 覆盖 enabled），过滤 Enabled 后投影 id/name 并按 name ASC, id ASC
// 重排。只投影 id/name，不暴露归属、供应商等管理面字段。
func (s *Store) ListChatGroupOptions(ctx context.Context, bindScope chat.ChatBindScope) ([]chat.ChatBindOption, error) {
	if !bindScope.IsAdmin {
		summaries, err := s.Options(ctx, AccessScope{ViewerID: bindScope.ViewerID}, OptionsQuery{Limit: chatBindOptionLimit})
		if err != nil {
			return nil, err
		}
		options := make([]chat.ChatBindOption, 0, len(summaries))
		for _, summary := range summaries {
			if !summary.Enabled {
				continue
			}
			options = append(options, chat.ChatBindOption{ID: summary.ID, Name: summary.Name})
		}
		sort.Slice(options, func(i, j int) bool {
			if options[i].Name != options[j].Name {
				return options[i].Name < options[j].Name
			}
			return options[i].ID < options[j].ID
		})
		return options, nil
	}
	rows, err := s.db.Query(s.bind(`SELECT id, name FROM ` + s.table("groups") + ` WHERE enabled = 1 ORDER BY name ASC, id ASC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []chat.ChatBindOption{}
	for rows.Next() {
		var option chat.ChatBindOption
		if err := rows.Scan(&option.ID, &option.Name); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}
