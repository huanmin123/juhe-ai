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
