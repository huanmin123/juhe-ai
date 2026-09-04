package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Store additions for the generation wave: upload pipeline persistence
// (createChatAsset / completeChatAssetProcessing / assertChatAssetUploadSlotAvailable),
// generated-image commits (commitChatGeneratedAsset), image lineage reads
// (listRecentChatImageGenerations) and the compaction source page loader.
// SQL semantics and Chinese error strings mirror the Node repositories.

const maxChatAssetsPerMessage = 5
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
func (s *Store) assertUncommittedChatAssetCountAvailable(q queryer, ownerID, conversationID, now string) (int, error) {
	var total int64
	err := q.QueryRow(s.bind(`SELECT COUNT(*) AS total FROM `+s.table("chat_assets")+`
		WHERE system_account_id = ? AND conversation_id = ?
			AND source_kind = 'user_upload'
			AND turn_id IS NULL AND message_id IS NULL
			AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active'
			AND expires_at > ?`), ownerID, conversationID, now).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total >= maxChatAssetsPerMessage {
		return int(total), &AssetCountExceededError{}
	}
	return int(total), nil
}

// AssertChatAssetUploadSlotAvailable mirrors assertChatAssetUploadSlotAvailable.
func (s *Store) AssertChatAssetUploadSlotAvailable(ownerID, conversationID, nowValue string) (int, error) {
	now, err := requireRFC3339Instant(nowValue, "聊天资产 now")
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.lockAssetUserQuota(tx, ownerID); err != nil {
		return 0, err
	}
	uncommitted, err := s.assertUncommittedChatAssetCountAvailable(tx, ownerID, conversationID, now)
	if err != nil {
		return 0, err
	}
	return maxChatAssetsPerMessage - uncommitted, tx.Commit()
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
func (s *Store) CreateChatAsset(input CreateChatAssetInput) (*Asset, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	id := input.ID
	if id == "" {
		id = s.newID("asset")
	}
	expiresAt, err := addDays(now, input.RetentionDays, "聊天资产 expiresAt 基准时间")
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.lockAssetUserQuota(tx, input.SystemAccountID); err != nil {
		return nil, err
	}
	if _, err := s.assertUncommittedChatAssetCountAvailable(tx, input.SystemAccountID, input.ConversationID, now); err != nil {
		return nil, err
	}
	var assetBytes, assetCount int64
	err = tx.QueryRow(s.bind(`SELECT asset_bytes, asset_count FROM `+s.table("chat_user_asset_usage")+`
		WHERE system_account_id = ?`), input.SystemAccountID).Scan(&assetBytes, &assetCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if assetBytes+input.QuotaBytes > ChatAssetUserMaxBytes || assetCount+1 > ChatAssetUserMaxCount {
		return nil, &AssetQuotaExceededError{}
	}
	result, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_assets")+` (
		id, system_account_id, conversation_id, source_kind, original_filename, original_mime_type,
		original_width, original_height, original_bytes, original_sha256, quota_bytes,
		processing_status, observation_status, cleanup_status, cleanup_attempt_count,
		created_at, updated_at, expires_at
	)
	SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'not_requested', 'active', 0, ?, ?, ?
	FROM `+s.table("chat_conversations")+`
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
func (s *Store) GetAssetNoExpiryGuard(assetID, ownerID, conversationID string) (*Asset, error) {
	assets, err := s.queryAssets(s.db, s.bind(`SELECT `+assetColumns+` FROM `+s.table("chat_assets")+`
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
func (s *Store) CompleteChatAssetProcessing(input CompleteAssetProcessingInput) (*Asset, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
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
func (s *Store) FailChatAssetProcessing(assetID, ownerID, conversationID, errorCode, nowValue string) (bool, error) {
	now, err := requireRFC3339Instant(nowValue, "聊天资产 now")
	if err != nil {
		return false, err
	}
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
		SET processing_status = 'failed', processing_error_code = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND conversation_id = ?
			AND processing_status = 'pending' AND cleanup_status = 'active'`),
		normalizedErrorCode(errorCode), now, assetID, ownerID, conversationID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

func (s *Store) incrementAssetUserUsage(tx queryer, ownerID string, quotaBytes int64, now string) error {
	_, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_user_asset_usage")+` (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT (system_account_id) DO UPDATE SET
			asset_bytes = asset_bytes + ?, asset_count = asset_count + 1, updated_at = ?`),
		ownerID, quotaBytes, now, quotaBytes, now)
	return err
}

// GeneratedAssetCommitInput mirrors ChatGeneratedAssetCommitInput.
type GeneratedAssetCommitInput struct {
	ID                string
	SystemAccountID   string
	ConversationID    string
	TurnID            string
	MessageID         string
	ContentOrder      int64
	MimeType          string
	Width             int64
	Height            int64
	Bytes             int64
	Sha256            string
	StorageKey        string
	PreviewMimeType   string
	PreviewWidth      int64
	PreviewHeight     int64
	PreviewBytes      int64
	PreviewSha256     string
	PreviewStorageKey string
	Now               string
	RetentionDays     int
	Generation        GeneratedImageGenerationRecord
}

// GeneratedImageGenerationRecord mirrors the generation lineage payload.
type GeneratedImageGenerationRecord struct {
	Operation      string
	Model          string
	Prompt         string
	SourceAssetIDs []string
	RootAssetID    string
	Size           string
	Quality        string
	OutputFormat   string
}

// CommitChatGeneratedAsset mirrors commitChatGeneratedAsset: assistant
// message binding, quota guard, ready asset insert, assistant_output
// reference, image-generation lineage row and usage increment.
func (s *Store) CommitChatGeneratedAsset(input GeneratedAssetCommitInput) (*Asset, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	id := input.ID
	if id == "" {
		id = s.newID("asset")
	}
	quotaBytes := input.Bytes + input.PreviewBytes
	expiresAt, err := addDays(now, input.RetentionDays, "聊天资产 expiresAt 基准时间")
	if err != nil {
		return nil, err
	}
	sourceAssetIDs := uniqueStrings(input.Generation.SourceAssetIDs)
	sourceAssetIDsJSON, err := json.Marshal(sourceAssetIDs)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.lockAssetUserQuota(tx, input.SystemAccountID); err != nil {
		return nil, err
	}
	var turnID string
	err = tx.QueryRow(s.bind(`SELECT turn_id FROM `+s.table("chat_messages")+`
		WHERE id = ? AND conversation_id = ? AND system_account_id = ? AND turn_id = ? AND role = 'assistant'
		LIMIT 1`+s.lockSuffix()), input.MessageID, input.ConversationID, input.SystemAccountID, input.TurnID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("生成图片只能绑定到当前助手消息")
	}
	if err != nil {
		return nil, err
	}
	var assetBytes, assetCount int64
	err = tx.QueryRow(s.bind(`SELECT asset_bytes, asset_count FROM `+s.table("chat_user_asset_usage")+`
		WHERE system_account_id = ?`), input.SystemAccountID).Scan(&assetBytes, &assetCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if assetBytes+quotaBytes > ChatAssetUserMaxBytes || assetCount+1 > ChatAssetUserMaxCount {
		return nil, &AssetQuotaExceededError{}
	}
	originalFilename := input.ID
	if originalFilename == "" {
		originalFilename = id
	}
	originalFilename += generatedAssetExtension(input.MimeType)
	result, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_assets")+` (
		id, system_account_id, conversation_id, source_kind, original_filename, original_mime_type,
		original_width, original_height, original_bytes, original_sha256,
		processed_mime_type, processed_width, processed_height, processed_bytes, processed_sha256, storage_key,
		preview_mime_type, preview_width, preview_height, preview_bytes, preview_sha256, preview_storage_key,
		processing_status, observation_status, observation_revision, quota_bytes,
		turn_id, message_id, committed_at, cleanup_status, cleanup_attempt_count, created_at, updated_at, expires_at
	) VALUES (?, ?, ?, 'assistant_generated', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ready', 'not_requested', 0, ?, ?, ?, ?, 'active', 0, ?, ?, ?)`),
		id, input.SystemAccountID, input.ConversationID, originalFilename, input.MimeType,
		input.Width, input.Height, input.Bytes, input.Sha256, input.MimeType,
		input.Width, input.Height, input.Bytes, input.Sha256, input.StorageKey,
		input.PreviewMimeType, input.PreviewWidth, input.PreviewHeight, input.PreviewBytes,
		input.PreviewSha256, input.PreviewStorageKey,
		quotaBytes, input.TurnID, input.MessageID, now, now, now, expiresAt)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("生成图片资产写入失败")
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_asset_references")+` (
		asset_id, conversation_id, turn_id, message_id, reference_kind, content_order, created_at, expires_at
	) VALUES (?, ?, ?, ?, 'assistant_output', ?, ?, ?)`),
		id, input.ConversationID, input.TurnID, input.MessageID, input.ContentOrder, now, expiresAt); err != nil {
		return nil, err
	}
	rootAssetID := input.Generation.RootAssetID
	if rootAssetID == "" && len(sourceAssetIDs) > 0 {
		rootAssetID = sourceAssetIDs[0]
	}
	if rootAssetID == "" {
		rootAssetID = id
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_image_generations")+` (
		asset_id, conversation_id, system_account_id, operation, model, prompt,
		source_asset_ids_json, root_asset_id, size, quality, output_format, created_at, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, input.ConversationID, input.SystemAccountID, input.Generation.Operation, input.Generation.Model,
		input.Generation.Prompt, string(sourceAssetIDsJSON), rootAssetID,
		input.Generation.Size, input.Generation.Quality, input.Generation.OutputFormat, now, expiresAt); err != nil {
		return nil, err
	}
	if err := s.incrementAssetUserUsage(tx, input.SystemAccountID, quotaBytes, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAssetNoExpiryGuard(id, input.SystemAccountID, input.ConversationID)
}

func generatedAssetExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	}
	return ".webp"
}

// ImageGenerationRecord mirrors ChatImageGenerationRecord.
type ImageGenerationRecord struct {
	AssetID        string
	Operation      string
	Model          string
	Prompt         string
	SourceAssetIDs []string
	RootAssetID    string
	Size           string
	Quality        string
	OutputFormat   string
	CreatedAt      string
	ExpiresAt      string
}

// ListRecentImageGenerations mirrors listRecentChatImageGenerations.
func (s *Store) ListRecentImageGenerations(conversationID, ownerID, nowValue string, limit int) ([]ImageGenerationRecord, error) {
	now, err := requireRFC3339Instant(nowValue, "聊天上下文 now")
	if err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 12 {
		limit = 12
	}
	rows, err := s.db.Query(s.bind(`SELECT asset_id, operation, model, prompt, source_asset_ids_json,
			root_asset_id, size, quality, output_format, created_at, expires_at
		FROM `+s.table("chat_image_generations")+`
		WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ?
		ORDER BY created_at DESC, asset_id DESC LIMIT ?`), conversationID, ownerID, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImageGenerationRecord{}
	for rows.Next() {
		var record ImageGenerationRecord
		var sourceJSON string
		if err := rows.Scan(&record.AssetID, &record.Operation, &record.Model, &record.Prompt, &sourceJSON,
			&record.RootAssetID, &record.Size, &record.Quality, &record.OutputFormat,
			&record.CreatedAt, &record.ExpiresAt); err != nil {
			return nil, err
		}
		record.SourceAssetIDs = []string{}
		_ = json.Unmarshal([]byte(sourceJSON), &record.SourceAssetIDs)
		out = append(out, record)
	}
	return out, rows.Err()
}

// CompactionSourcePage mirrors ChatCompactionSourcePage.
type CompactionSourcePage struct {
	Messages           []contextSourceMessage
	NextAfterSequence  int64
	LoadedBytes        int64
	EarliestExpiresAt  string
	BlockedByByteLimit bool
}

// LoadCompactionSourcePage mirrors loadChatCompactionSourcePage.
func (s *Store) LoadCompactionSourcePage(conversationID, ownerID, claimID string, afterSequence int64, nowValue string, limit, maxBytes int) (*CompactionSourcePage, error) {
	now, err := requireRFC3339Instant(nowValue, "聊天上下文 now")
	if err != nil {
		return nil, err
	}
	if afterSequence < 0 {
		return nil, &DomainError{Message: "afterSequence 必须是非负安全整数"}
	}
	if limit < 2 || limit > 512 {
		return nil, &DomainError{Message: "limit 必须是 2..512 的整数"}
	}
	if maxBytes < 1 || maxBytes > maxContextLoadBytes {
		return nil, &DomainError{Message: "maxBytes 必须是 1..16777216 的整数"}
	}
	claim, err := s.findCompactionClaim(s.db, conversationID, ownerID, claimID)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, nil
	}
	if afterSequence != claim.ProgressSequence {
		return nil, errors.New("压缩来源游标超出当前认领范围")
	}
	rowBudget := limit - limit%2
	messagesTable := s.table("chat_messages")
	rows, err := s.db.Query(s.bind(`SELECT source.id, source.turn_id, source.sequence_no, source.role, source.content_text,
			source.content_blocks_json, source.content_bytes, source.model, source.created_at, source.completed_at, source.expires_at
		FROM `+messagesTable+` AS source
		WHERE source.conversation_id = ? AND source.system_account_id = ?
			AND source.status = 'completed' AND source.expires_at > ?
			AND source.sequence_no > ? AND source.sequence_no <= ?
			AND EXISTS (
				SELECT 1 FROM `+messagesTable+` AS pair
				WHERE pair.conversation_id = source.conversation_id
					AND pair.system_account_id = source.system_account_id
					AND pair.turn_id = source.turn_id
					AND pair.status = 'completed' AND pair.expires_at > ?
					AND (
						(source.role = 'user' AND pair.role = 'assistant' AND pair.sequence_no = source.sequence_no + 1)
						OR (source.role = 'assistant' AND pair.role = 'user' AND pair.sequence_no = source.sequence_no - 1)
					)
			)
		ORDER BY source.sequence_no ASC
		LIMIT ?`), conversationID, ownerID, now, afterSequence, claim.SourceThroughSequence, now, rowBudget+2)
	if err != nil {
		return nil, err
	}
	loaded := []contextSourceMessage{}
	for rows.Next() {
		var message contextSourceMessage
		if err := rows.Scan(&message.id, &message.turnID, &message.sequenceNo, &message.role, &message.contentText,
			&message.contentBlocksJSON, &message.contentBytes, &message.modelID, &message.createdAt,
			&message.completedAt, &message.expiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		message.contentBytes = maxI64(message.contentBytes, int64(len(message.contentText)+len(message.contentBlocksJSON)))
		loaded = append(loaded, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	page := &CompactionSourcePage{Messages: []contextSourceMessage{}}
	var loadedBytes int64
	earliest := ""
	blocked := false
	index := 0
	for ; index+1 < len(loaded) && index < rowBudget; index += 2 {
		user := loaded[index]
		assistant := loaded[index+1]
		if user.role != "user" || assistant.role != "assistant" || user.turnID != assistant.turnID || assistant.sequenceNo != user.sequenceNo+1 {
			return nil, errors.New("压缩来源完整轮次顺序不一致")
		}
		pairBytes := user.contentBytes + assistant.contentBytes
		if loadedBytes+pairBytes > int64(maxBytes) && len(page.Messages) > 0 {
			blocked = true
			break
		}
		if pairBytes > maxContextLoadBytes {
			return nil, errors.New("单个完整聊天轮次超过压缩来源绝对大小限制")
		}
		page.Messages = append(page.Messages, user, assistant)
		loadedBytes += pairBytes
		for _, message := range []contextSourceMessage{user, assistant} {
			if earliest == "" || message.expiresAt < earliest {
				earliest = message.expiresAt
			}
		}
	}
	if !blocked {
		if index < len(loaded) && index < rowBudget {
			blocked = true
		} else if len(loaded) > rowBudget {
			blocked = true
		}
	}
	page.LoadedBytes = loadedBytes
	page.BlockedByByteLimit = blocked
	page.NextAfterSequence = afterSequence
	if len(page.Messages) > 0 {
		page.NextAfterSequence = page.Messages[len(page.Messages)-1].sequenceNo
	}
	page.EarliestExpiresAt = earliest
	return page, nil
}
