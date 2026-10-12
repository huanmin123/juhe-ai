package chatassets

import (
	"database/sql"
	"errors"
)

// Asset subdomain persistence for the generation wave: upload pipeline
// persistence (createChatAsset / completeChatAssetProcessing /
// assertChatAssetUploadSlotAvailable). 生成图片/媒体产物提交与图像谱系读取
// 已随站内工具体系退场（PLAN-20261012T121500000Z D2/D7）。The compaction
// source page loader stayed in the facade root (context.go vocabulary).
// SQL semantics and Chinese error strings mirror the Node repositories.

const MaxChatAssetsPerMessage = 5
const ChatAssetUserMaxBytes = int64(2 * 1024 * 1024 * 1024)
const ChatAssetUserMaxCount = 1024

// AssetQuotaExceededError mirrors ChatAssetQuotaExceededError (upload maps to
// 413 chat_asset_quota_exceeded).
type AssetQuotaExceededError struct{}

func (e *AssetQuotaExceededError) Error() string {
	return "聊天图片存储额度已满，请删除不用的会话或等待过期资产清理后重试"
}

// AssetCountExceededError mirrors ChatAssetCountExceededError (upload maps to
// 400 chat_asset_count_exceeded).
type AssetCountExceededError struct{}

func (e *AssetCountExceededError) Error() string {
	return "每条消息最多 5 张图片，请移除图片后重试"
}

// assertUncommittedChatAssetCountAvailable mirrors the private helper.
func (s *AssetStore) assertUncommittedChatAssetCountAvailable(q queryer, ownerID, conversationID, now string) (int, error) {
	var total int64
	err := q.QueryRow(s.ports.Bind(`SELECT COUNT(*) AS total FROM `+s.ports.Table("chat_assets")+`
		WHERE system_account_id = ? AND conversation_id = ?
			AND source_kind = 'user_upload'
			AND turn_id IS NULL AND message_id IS NULL
			AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active'
			AND expires_at > ?`), ownerID, conversationID, now).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total >= MaxChatAssetsPerMessage {
		return int(total), &AssetCountExceededError{}
	}
	return int(total), nil
}

// AssertChatAssetUploadSlotAvailable mirrors assertChatAssetUploadSlotAvailable.
func (s *AssetStore) AssertChatAssetUploadSlotAvailable(ownerID, conversationID, nowValue string) (int, error) {
	now, err := s.ports.RequireRFC3339Instant(nowValue, "聊天资产 now")
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.LockAssetUserQuota(tx, ownerID); err != nil {
		return 0, err
	}
	uncommitted, err := s.assertUncommittedChatAssetCountAvailable(tx, ownerID, conversationID, now)
	if err != nil {
		return 0, err
	}
	return MaxChatAssetsPerMessage - uncommitted, tx.Commit()
}

// CreateChatAssetInput mirrors ChatAssetCreateInput.
type CreateChatAssetInput struct {
	ID               string
	SystemAccountID  string
	ConversationID   string
	SourceKind       string
	OriginalFilename string
	OriginalMimeType string
	OriginalWidth    *int64
	OriginalHeight   *int64
	OriginalBytes    int64
	OriginalSha256   string
	QuotaBytes       int64
	Now              string
	RetentionDays    int
}

// CreateChatAsset mirrors createChatAsset: quota lock, uncommitted-count
// guard, usage quota guard, conversation-owned insert, usage increment.
func (s *AssetStore) CreateChatAsset(input CreateChatAssetInput) (*Asset, error) {
	now, err := s.ports.RequireRFC3339Instant(input.Now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	id := input.ID
	if id == "" {
		id = s.ports.NewID("asset")
	}
	expiresAt, err := s.ports.AddDays(now, input.RetentionDays, "聊天资产 expiresAt 基准时间")
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.LockAssetUserQuota(tx, input.SystemAccountID); err != nil {
		return nil, err
	}
	if _, err := s.assertUncommittedChatAssetCountAvailable(tx, input.SystemAccountID, input.ConversationID, now); err != nil {
		return nil, err
	}
	var assetBytes, assetCount int64
	err = tx.QueryRow(s.ports.Bind(`SELECT asset_bytes, asset_count FROM `+s.ports.Table("chat_user_asset_usage")+`
		WHERE system_account_id = ?`), input.SystemAccountID).Scan(&assetBytes, &assetCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if assetBytes+input.QuotaBytes > ChatAssetUserMaxBytes || assetCount+1 > ChatAssetUserMaxCount {
		return nil, &AssetQuotaExceededError{}
	}
	result, err := tx.Exec(s.ports.Bind(`INSERT INTO `+s.ports.Table("chat_assets")+` (
		id, system_account_id, conversation_id, source_kind, original_filename, original_mime_type,
		original_width, original_height, original_bytes, original_sha256, quota_bytes,
		processing_status, observation_status, cleanup_status, cleanup_attempt_count,
		created_at, updated_at, expires_at
	)
	SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'not_requested', 'active', 0, ?, ?, ?
	FROM `+s.ports.Table("chat_conversations")+`
	WHERE id = ? AND system_account_id = ?`),
		id, input.SystemAccountID, input.ConversationID, input.SourceKind,
		input.OriginalFilename, input.OriginalMimeType, input.OriginalWidth, input.OriginalHeight,
		input.OriginalBytes, input.OriginalSha256, input.QuotaBytes, now, now, expiresAt,
		input.ConversationID, input.SystemAccountID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("聊天会话不存在或不属于当前用户")
	}
	if err := s.incrementAssetUserUsage(tx, input.SystemAccountID, input.QuotaBytes, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAssetNoExpiryGuard(id, input.SystemAccountID, input.ConversationID)
}

// GetAssetNoExpiryGuard reads an asset row regardless of expiry (internal use
// right after insert/update).
func (s *AssetStore) GetAssetNoExpiryGuard(assetID, ownerID, conversationID string) (*Asset, error) {
	assets, err := s.QueryAssets(s.db, s.ports.Bind(`SELECT `+AssetColumns+` FROM `+s.ports.Table("chat_assets")+`
		WHERE id = ? AND system_account_id = ? AND conversation_id = ? LIMIT 1`), assetID, ownerID, conversationID)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return assets[0], nil
}

// CompleteAssetProcessingInput mirrors ChatAssetProcessingResultInput.
type CompleteAssetProcessingInput struct {
	AssetID           string
	SystemAccountID   string
	ConversationID    string
	ProcessedMimeType string
	ProcessedWidth    int64
	ProcessedHeight   int64
	ProcessedBytes    int64
	ProcessedSha256   string
	StorageKey        string
	Now               string
}

// CompleteChatAssetProcessing mirrors completeChatAssetProcessing.
func (s *AssetStore) CompleteChatAssetProcessing(input CompleteAssetProcessingInput) (*Asset, error) {
	now, err := s.ports.RequireRFC3339Instant(input.Now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	result, err := s.db.Exec(s.ports.Bind(`UPDATE `+s.ports.Table("chat_assets")+`
		SET processed_mime_type = ?, processed_width = ?, processed_height = ?, processed_bytes = ?,
			processed_sha256 = ?, storage_key = ?, processing_status = 'ready',
			processing_error_code = NULL, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND conversation_id = ?
			AND processing_status = 'pending' AND cleanup_status = 'active' AND expires_at > ?`),
		input.ProcessedMimeType, input.ProcessedWidth, input.ProcessedHeight, input.ProcessedBytes,
		input.ProcessedSha256, input.StorageKey, now,
		input.AssetID, input.SystemAccountID, input.ConversationID, now)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("聊天资产不存在、已过期或处理状态已变化")
	}
	return s.GetAssetNoExpiryGuard(input.AssetID, input.SystemAccountID, input.ConversationID)
}

// FailChatAssetProcessing mirrors failChatAssetProcessing.
func (s *AssetStore) FailChatAssetProcessing(assetID, ownerID, conversationID, errorCode, nowValue string) (bool, error) {
	now, err := s.ports.RequireRFC3339Instant(nowValue, "聊天资产 now")
	if err != nil {
		return false, err
	}
	result, err := s.db.Exec(s.ports.Bind(`UPDATE `+s.ports.Table("chat_assets")+`
		SET processing_status = 'failed', processing_error_code = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND conversation_id = ?
			AND processing_status = 'pending' AND cleanup_status = 'active'`),
		NormalizedErrorCode(errorCode, s.ports.TrimSpace), now, assetID, ownerID, conversationID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

func (s *AssetStore) incrementAssetUserUsage(tx queryer, ownerID string, quotaBytes int64, now string) error {
	// DO UPDATE 右值必须表名限定：PG 对未限定的自引用列报 42702
	// ambiguous（SQLite 容忍裸列，隔离实例测不出）；与 quota_hourly_dirty
	// 的限定写法同款。
	usageTable := s.ports.Table("chat_user_asset_usage")
	_, err := tx.Exec(s.ports.Bind(`INSERT INTO `+usageTable+` (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT (system_account_id) DO UPDATE SET
			asset_bytes = `+usageTable+`.asset_bytes + ?, asset_count = `+usageTable+`.asset_count + 1, updated_at = ?`),
		ownerID, quotaBytes, now, quotaBytes, now)
	return err
}

// GeneratedAssetCommitInput mirrors ChatGeneratedAssetCommitInput.
