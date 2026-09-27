package groups

import (
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// FindChatGroup resolves the AI 问答会话 group 绑定对象（存在性、enabled 与
// 名称快照的只读查询）。分组不存在时返回 (nil, nil)；调用方（chat 创建与
// 模型作用域校验）据 Enabled 拒绝停用分组。enabled 在两种方言中均为 integer。
func (s *Store) FindChatGroup(groupID string) (*chat.ChatGroupRef, error) {
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

// ListChatGroupOptions 列出 AI 问答新建会话绑定下拉的分组最小摘要（仅
// enabled = 1；groups 表无软删列，物理删即不可绑定）。只投影 id/name，不
// 暴露归属、供应商等管理面字段；排序 name ASC, id ASC 与下拉展示一致。
func (s *Store) ListChatGroupOptions() ([]chat.ChatBindOption, error) {
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
