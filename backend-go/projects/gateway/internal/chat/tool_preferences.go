package chat

// 用户级默认工具绑定（AI 问答工具体系与主子模型设计 §2.11/§7，2026-10-02）：
// chat_user_tool_preferences 每用户一行的偏好存取。搜索（web_search）与生图
// （generate_image）的用户级全局默认绑定；新建会话由服务端继承非空列，会话内
// 显式改绑定后 best-effort 回写（routes 层）。双方言读写模式与 windows.go 的
// 用户级表（chat_user_asset_usage 族）一致。

import (
	"database/sql"
	"errors"
)

// UserToolPreferences 是 chat_user_tool_preferences 的行投影：四列空串 = 未设
// 默认（表内为 NULL）。
type UserToolPreferences struct {
	SystemAccountID   string
	SearchAccountID   string
	SearchModelID     string
	ImageAccountID    string
	DefaultImageModel string
}

// GetUserToolPreferences 读取用户工具偏好行；无行返回 (nil, nil)。
func (s *Store) GetUserToolPreferences(ownerID string) (*UserToolPreferences, error) {
	var searchAccountID, searchModelID, imageAccountID, defaultImageModel sql.NullString
	err := s.db.QueryRow(s.bind(`SELECT search_account_id, search_model_id, image_account_id, default_image_model
		FROM `+s.table("chat_user_tool_preferences")+` WHERE system_account_id = ?`), ownerID).
		Scan(&searchAccountID, &searchModelID, &imageAccountID, &defaultImageModel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &UserToolPreferences{
		SystemAccountID:   ownerID,
		SearchAccountID:   searchAccountID.String,
		SearchModelID:     searchModelID.String,
		ImageAccountID:    imageAccountID.String,
		DefaultImageModel: defaultImageModel.String,
	}, nil
}

// UpsertUserToolPreferences 整行写入用户工具偏好（PG ON CONFLICT
// (system_account_id) DO UPDATE，SQLite 同构；updated_at 以注入时钟刷新）。
// 只更新请求键的合并语义由调用方完成；upsert 本身整行覆盖。
func (s *Store) UpsertUserToolPreferences(pref UserToolPreferences) error {
	now := s.nowISO()
	table := s.table("chat_user_tool_preferences")
	if s.pg {
		_, err := s.db.Exec(s.bind(`INSERT INTO `+table+`
			(system_account_id, search_account_id, search_model_id, image_account_id, default_image_model, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (system_account_id)
			DO UPDATE SET search_account_id = EXCLUDED.search_account_id,
			              search_model_id = EXCLUDED.search_model_id,
			              image_account_id = EXCLUDED.image_account_id,
			              default_image_model = EXCLUDED.default_image_model,
			              updated_at = EXCLUDED.updated_at`),
			pref.SystemAccountID, optSQLText(pref.SearchAccountID), optSQLText(pref.SearchModelID),
			optSQLText(pref.ImageAccountID), optSQLText(pref.DefaultImageModel), now)
		return err
	}
	_, err := s.db.Exec(s.bind(`INSERT INTO `+table+`
		(system_account_id, search_account_id, search_model_id, image_account_id, default_image_model, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(system_account_id)
		DO UPDATE SET search_account_id = excluded.search_account_id,
		              search_model_id = excluded.search_model_id,
		              image_account_id = excluded.image_account_id,
		              default_image_model = excluded.default_image_model,
		              updated_at = excluded.updated_at`),
		pref.SystemAccountID, optSQLText(pref.SearchAccountID), optSQLText(pref.SearchModelID),
		optSQLText(pref.ImageAccountID), optSQLText(pref.DefaultImageModel), now)
	return err
}
