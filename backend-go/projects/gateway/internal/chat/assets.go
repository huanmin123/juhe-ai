package chat

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

// Chat asset persistence mirrors the chat-assets.repository.ts subset used by
// the my-chat routes plus the upload pipeline inputs (createChatAsset,
// completeChatAssetProcessing, references) and chat_user_asset_usage quota.

// Asset mirrors ChatAssetRecord.
type Asset struct {
	ID                   string
	SystemAccountID      string
	ConversationID       string
	SourceKind           string
	OriginalFilename     string
	OriginalMimeType     string
	OriginalWidth        *int64
	OriginalHeight       *int64
	OriginalBytes        int64
	OriginalSha256       string
	ProcessedMimeType    *string
	ProcessedWidth       *int64
	ProcessedHeight      *int64
	ProcessedBytes       *int64
	ProcessedSha256      *string
	StorageKey           *string
	PreviewMimeType      *string
	PreviewWidth         *int64
	PreviewHeight        *int64
	PreviewBytes         *int64
	PreviewSha256        *string
	PreviewStorageKey    *string
	ProcessingStatus     string
	ProcessingErrorCode  *string
	ObservationStatus    string
	Observation          map[string]any
	ObservationRevision  int64
	ObservationClaimID   *string
	ObservationClaimedAt *string
	QuotaBytes           int64
	TurnID               *string
	MessageID            *string
	CommittedAt          *string
	CleanupStatus        string
	CleanupAttemptCount  int64
	CleanupClaimedAt     *string
	CleanupRetryAt       *string
	CleanupErrorCode     *string
	CreatedAt            string
	UpdatedAt            string
	ExpiresAt            string
}

const assetColumns = `id, system_account_id, conversation_id, source_kind, original_filename,
	original_mime_type, original_width, original_height, original_bytes, original_sha256,
	processed_mime_type, processed_width, processed_height, processed_bytes, processed_sha256,
	storage_key, preview_mime_type, preview_width, preview_height, preview_bytes, preview_sha256,
	preview_storage_key, processing_status, processing_error_code, observation_status, observation_json,
	observation_revision, observation_claim_id, observation_claimed_at, quota_bytes,
	turn_id, message_id, committed_at, cleanup_status, cleanup_attempt_count, cleanup_claimed_at,
	cleanup_retry_at, cleanup_error_code, created_at, updated_at, expires_at`

func scanAsset(scan func(...any) error) (*Asset, error) {
	asset := &Asset{}
	var observationJSON sql.NullString
	err := scan(&asset.ID, &asset.SystemAccountID, &asset.ConversationID, &asset.SourceKind,
		&asset.OriginalFilename, &asset.OriginalMimeType, &asset.OriginalWidth, &asset.OriginalHeight,
		&asset.OriginalBytes, &asset.OriginalSha256, &asset.ProcessedMimeType, &asset.ProcessedWidth,
		&asset.ProcessedHeight, &asset.ProcessedBytes, &asset.ProcessedSha256, &asset.StorageKey,
		&asset.PreviewMimeType, &asset.PreviewWidth, &asset.PreviewHeight, &asset.PreviewBytes,
		&asset.PreviewSha256, &asset.PreviewStorageKey, &asset.ProcessingStatus, &asset.ProcessingErrorCode,
		&asset.ObservationStatus, &observationJSON, &asset.ObservationRevision, &asset.ObservationClaimID,
		&asset.ObservationClaimedAt, &asset.QuotaBytes, &asset.TurnID, &asset.MessageID, &asset.CommittedAt,
		&asset.CleanupStatus, &asset.CleanupAttemptCount, &asset.CleanupClaimedAt, &asset.CleanupRetryAt,
		&asset.CleanupErrorCode, &asset.CreatedAt, &asset.UpdatedAt, &asset.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if observationJSON.Valid && observationJSON.String != "" {
		_ = json.Unmarshal([]byte(observationJSON.String), &asset.Observation)
	}
	return asset, nil
}

func (s *Store) queryAssets(q queryer, query string, args ...any) ([]*Asset, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Asset{}
	for rows.Next() {
		asset, err := scanAsset(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}

// GetAsset mirrors getChatAsset (active + optional expiry guard).
func (s *Store) GetAsset(assetID, ownerID, conversationID, now string) (*Asset, error) {
	normalizedNow, err := requireRFC3339Instant(now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	assets, err := s.queryAssets(s.db, s.bind(`SELECT `+assetColumns+` FROM `+s.table("chat_assets")+`
		WHERE id = ? AND system_account_id = ? AND conversation_id = ?
			AND cleanup_status = 'active' AND expires_at > ?
		LIMIT 1`), assetID, ownerID, conversationID, normalizedNow)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return assets[0], nil
}

var assetIDPattern = regexp.MustCompile(`^chat_asset_[a-f0-9]{32}$`)

// normalizedAssetID mirrors normalizedAssetId.
func normalizedAssetID(value string) (string, error) {
	normalized := trimSpace(value)
	if !assetIDPattern.MatchString(normalized) {
		return "", &DomainError{Message: "聊天资产 ID 无效"}
	}
	return normalized, nil
}

func normalizedAssetIDs(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized, err := normalizedAssetID(value)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

// ListReadyAssetsByID mirrors listReadyChatAssetsByIds (input order kept).
func (s *Store) ListReadyAssetsByID(assetIDs []string, ownerID, conversationID, now string) ([]*Asset, error) {
	normalizedNow, err := requireRFC3339Instant(now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	ids, err := normalizedAssetIDs(assetIDs)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []*Asset{}, nil
	}
	rows, err := s.queryAssets(s.db, s.bind(`SELECT `+assetColumns+` FROM `+s.table("chat_assets")+`
		WHERE id IN (`+placeholders(len(ids))+`)
			AND system_account_id = ? AND conversation_id = ?
			AND processing_status = 'ready' AND cleanup_status = 'active' AND expires_at > ?`),
		append(append([]any{}, toAnySlice(ids)...), ownerID, conversationID, normalizedNow)...)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Asset{}
	for _, asset := range rows {
		byID[asset.ID] = asset
	}
	out := make([]*Asset, 0, len(ids))
	for _, id := range ids {
		if asset, ok := byID[id]; ok {
			out = append(out, asset)
		}
	}
	return out, nil
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// cleanupClaimID mirrors cleanupClaimId.
func cleanupClaimID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "chat_asset_cleanup_" + hex.EncodeToString(buf)
}

// AssetDeletionClaim mirrors the claimUncommittedChatAssetForDeletion return.
type AssetDeletionClaim struct {
	ClaimID string
	Asset   *Asset
}

// ClaimUncommittedAssetForDeletion mirrors claimUncommittedChatAssetForDeletion.
func (s *Store) ClaimUncommittedAssetForDeletion(assetID, ownerID, conversationID, now string) (*AssetDeletionClaim, error) {
	normalizedNow, err := requireRFC3339Instant(now, "聊天资产 now")
	if err != nil {
		return nil, err
	}
	claimID := cleanupClaimID()
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_assets")+` AS asset
		SET cleanup_status = 'claimed', cleanup_claim_id = ?, cleanup_claimed_at = ?,
			cleanup_attempt_count = cleanup_attempt_count + 1, cleanup_retry_at = NULL,
			cleanup_error_code = NULL, updated_at = ?
		WHERE asset.id = ? AND asset.system_account_id = ? AND asset.conversation_id = ?
			AND asset.source_kind = 'user_upload'
			AND asset.turn_id IS NULL AND asset.message_id IS NULL
			AND NOT EXISTS (
				SELECT 1 FROM `+s.table("chat_asset_references")+` AS reference
				WHERE reference.asset_id = asset.id AND reference.conversation_id = asset.conversation_id
					AND reference.expires_at > ?
			)
			AND asset.cleanup_status IN ('active', 'failed')`),
		claimID, normalizedNow, normalizedNow, assetID, ownerID, conversationID, normalizedNow)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	asset, err := s.getClaimedAsset(assetID, claimID)
	if err != nil || asset == nil {
		return nil, err
	}
	return &AssetDeletionClaim{ClaimID: claimID, Asset: asset}, nil
}

func (s *Store) getClaimedAsset(assetID, claimID string) (*Asset, error) {
	assets, err := s.queryAssets(s.db, s.bind(`SELECT `+assetColumns+` FROM `+s.table("chat_assets")+`
		WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
		LIMIT 1`), assetID, claimID)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return assets[0], nil
}

// getClaimedAssetTx reads the claimed row inside an open transaction.
func (s *Store) getClaimedAssetTx(tx queryer, assetID, claimID string) (*Asset, error) {
	assets, err := s.queryAssets(tx, s.bind(`SELECT `+assetColumns+` FROM `+s.table("chat_assets")+`
		WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
		LIMIT 1`), assetID, claimID)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return assets[0], nil
}

// CompleteAssetDeletion mirrors completeChatAssetDeletion: claimed-row delete
// plus chat_user_asset_usage decrement (lock via transaction). The claimed
// row must be read inside the transaction: with MaxOpenConns(1) a second
// s.db query while the tx holds the only connection self-deadlocks.
func (s *Store) CompleteAssetDeletion(assetID, claimID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	asset, err := s.getClaimedAssetTx(tx, assetID, claimID)
	if err != nil || asset == nil {
		return false, err
	}
	if err := s.lockAssetUserQuota(tx, asset.SystemAccountID); err != nil {
		return false, err
	}
	result, err := tx.Exec(s.bind(`DELETE FROM `+s.table("chat_assets")+`
		WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?`), assetID, claimID)
	if err != nil {
		return false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return false, nil
	}
	if err := s.decrementAssetUserUsage(tx, asset.SystemAccountID, asset.QuotaBytes, s.nowISO()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) lockAssetUserQuota(tx queryer, ownerID string) error {
	if !s.pg {
		return nil
	}
	_, err := tx.Exec(s.bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`), "juhe-ai:chat-asset-usage:"+ownerID)
	return err
}

func (s *Store) decrementAssetUserUsage(tx queryer, ownerID string, quotaBytes int64, now string) error {
	_, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_user_asset_usage")+`
		SET asset_bytes = CASE WHEN asset_bytes >= ? THEN asset_bytes - ? ELSE 0 END,
			asset_count = CASE WHEN asset_count > 0 THEN asset_count - 1 ELSE 0 END,
			updated_at = ?
		WHERE system_account_id = ?`), quotaBytes, quotaBytes, now, ownerID)
	return err
}

// ReleaseAssetDeletionClaim mirrors releaseChatAssetDeletionClaim.
func (s *Store) ReleaseAssetDeletionClaim(assetID, claimID, errorCode, retryAt, now string) (bool, error) {
	normalizedRetryAt, err := requireRFC3339Instant(retryAt, "聊天资产 cleanupRetryAt")
	if err != nil {
		return false, err
	}
	normalizedNow, err := requireRFC3339Instant(now, "聊天资产 now")
	if err != nil {
		return false, err
	}
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
		SET cleanup_status = 'failed', cleanup_claim_id = NULL, cleanup_claimed_at = NULL,
			cleanup_retry_at = ?, cleanup_error_code = ?, updated_at = ?
		WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?`),
		normalizedRetryAt, normalizedErrorCode(errorCode), normalizedNow, assetID, claimID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

// normalizedErrorCode mirrors normalizedErrorCode (bounded, control-free).
func normalizedErrorCode(value string) string {
	normalized := trimSpace(value)
	if normalized == "" {
		normalized = "chat_asset_delete_failed"
	}
	runes := []rune(normalized)
	if len(runes) > 128 {
		runes = runes[:128]
	}
	return string(runes)
}

// expireChatAssetsForConversation mirrors expireChatAssetsForConversationInClient.
func (s *Store) expireChatAssetsForConversation(tx queryer, conversationID, ownerID, now string) error {
	normalizedNow, err := requireRFC3339Instant(now, "聊天资产 now")
	if err != nil {
		return err
	}
	_, err = tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
		SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
			cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
			updated_at = ?
		WHERE system_account_id = ? AND conversation_id = ?
			AND cleanup_status IN ('active', 'failed')`),
		normalizedNow, normalizedNow, normalizedNow, normalizedNow, ownerID, conversationID)
	return err
}

// removeChatAssetReferencesForMessage mirrors removeChatAssetReferencesForMessage:
// chat_asset_references carries no owner column, ownership filters through
// the referenced chat_assets row.
func (s *Store) removeChatAssetReferencesForMessage(tx queryer, ownerID, conversationID, messageID, now string) error {
	_, err := tx.Exec(s.bind(`DELETE FROM `+s.table("chat_asset_references")+` AS reference
		WHERE reference.conversation_id = ? AND reference.message_id = ?
			AND EXISTS (
				SELECT 1 FROM `+s.table("chat_assets")+` AS asset
				WHERE asset.id = reference.asset_id AND asset.conversation_id = reference.conversation_id
					AND asset.system_account_id = ?
			)`), conversationID, messageID, ownerID)
	return err
}

func (s *Store) queryUserInputAssetIDs(tx queryer, conversationID, messageID, now string) ([]string, error) {
	rows, err := tx.Query(s.bind(`SELECT asset_id FROM `+s.table("chat_asset_references")+`
		WHERE conversation_id = ? AND message_id = ? AND reference_kind = 'user_input' AND expires_at > ?`),
		conversationID, messageID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			return nil, err
		}
		normalized := trimSpace(assetID)
		if normalized != "" {
			out = append(out, normalized)
		}
	}
	return out, rows.Err()
}

// commitChatAssetsToMessage mirrors commitChatAssetsToMessageInClient: turn
// binding, ready-state validation, per-kind expiry update, image-generation
// root lineage retention and user_input reference inserts.
func (s *Store) commitChatAssetsToMessage(tx queryer, rawAssetIDs []string, ownerID, conversationID, messageID, now string, retentionDays int) error {
	normalizedNow, err := requireRFC3339Instant(now, "聊天资产 now")
	if err != nil {
		return err
	}
	assetIDs, err := normalizedAssetIDs(rawAssetIDs)
	if err != nil {
		return err
	}
	if len(assetIDs) == 0 {
		return nil
	}
	var turnID sql.NullString
	err = tx.QueryRow(s.bind(`SELECT turn_id FROM `+s.table("chat_messages")+`
		WHERE id = ? AND conversation_id = ? AND system_account_id = ?
			AND role = 'user' AND status = 'completed'
		LIMIT 1`+s.lockSuffix()), messageID, conversationID, ownerID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return &DomainError{Message: "聊天资产只能绑定到已写入的用户消息"}
	}
	if err != nil {
		return err
	}
	if !turnID.Valid {
		return &DomainError{Message: "聊天资产只能绑定到已写入的用户消息"}
	}
	rows, err := s.queryAssets(tx, s.bind(`SELECT `+assetColumns+` FROM `+s.table("chat_assets")+`
		WHERE id IN (`+placeholders(len(assetIDs))+`)
			AND system_account_id = ? AND conversation_id = ?`+s.lockSuffix()),
		append(toAnySlice(assetIDs), ownerID, conversationID)...)
	if err != nil {
		return err
	}
	byID := map[string]*Asset{}
	for _, asset := range rows {
		byID[asset.ID] = asset
	}
	nowMs, okNow := rfc3339Millis(normalizedNow)
	if !okNow {
		return &DomainError{Message: "聊天资产 now 必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	for _, assetID := range assetIDs {
		asset := byID[assetID]
		if asset == nil || asset.ProcessingStatus != "ready" || asset.CleanupStatus != "active" {
			return &DomainError{Message: "聊天资产不存在、未处理完成或已过期"}
		}
		expiresMs, ok := rfc3339Millis(asset.ExpiresAt)
		if !ok {
			return &DomainError{Message: "聊天资产 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间"}
		}
		if expiresMs <= nowMs {
			return &DomainError{Message: "聊天资产不存在、未处理完成或已过期"}
		}
		if asset.SourceKind == "user_upload" &&
			((asset.TurnID != nil && *asset.TurnID != turnID.String) || (asset.MessageID != nil && *asset.MessageID != messageID)) {
			return &DomainError{Message: "聊天资产已绑定其他消息"}
		}
	}
	expiresAt, err := addDays(normalizedNow, retentionDays, "聊天资产 expiresAt 基准时间")
	if err != nil {
		return err
	}
	for _, assetID := range assetIDs {
		asset := byID[assetID]
		var result sql.Result
		if asset.SourceKind == "assistant_generated" {
			result, err = tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
				SET expires_at = CASE WHEN expires_at < ? THEN ? ELSE expires_at END, updated_at = ?
				WHERE id = ? AND system_account_id = ? AND conversation_id = ?
					AND source_kind = 'assistant_generated' AND processing_status = 'ready' AND cleanup_status = 'active'`),
				expiresAt, expiresAt, normalizedNow, assetID, ownerID, conversationID)
		} else {
			result, err = tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
				SET turn_id = ?, message_id = ?, committed_at = COALESCE(committed_at, ?),
					expires_at = CASE WHEN expires_at < ? THEN ? ELSE expires_at END, updated_at = ?
				WHERE id = ? AND system_account_id = ? AND conversation_id = ?
					AND source_kind = 'user_upload' AND cleanup_status = 'active'
					AND (turn_id IS NULL OR turn_id = ?) AND (message_id IS NULL OR message_id = ?)`),
				turnID.String, messageID, normalizedNow, expiresAt, expiresAt, normalizedNow,
				assetID, ownerID, conversationID, turnID.String, messageID)
		}
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return &DomainError{Message: "聊天资产绑定消息时发生并发冲突"}
		}
	}
	rootAssetIDs, err := s.imageGenerationRootAssetIDs(tx, assetIDs, conversationID, ownerID)
	if err != nil {
		return err
	}
	filteredRoots := []string{}
	for _, root := range rootAssetIDs {
		if _, ok := byID[root]; !ok {
			filteredRoots = append(filteredRoots, root)
		}
	}
	if len(filteredRoots) > 0 {
		result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
			SET expires_at = CASE WHEN expires_at < ? THEN ? ELSE expires_at END,
				updated_at = CASE WHEN expires_at < ? THEN ? ELSE updated_at END
			WHERE id IN (`+placeholders(len(filteredRoots))+`)
				AND system_account_id = ? AND conversation_id = ?
				AND source_kind = 'assistant_generated' AND processing_status = 'ready' AND cleanup_status = 'active'`),
			append([]any{expiresAt, expiresAt, expiresAt, normalizedNow}, append(toAnySlice(filteredRoots), ownerID, conversationID)...)...)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != int64(len(filteredRoots)) {
			return &DomainError{Message: "聊天图片根谱系保留期限更新失败"}
		}
	}
	if err := s.renewImageGenerationExpiry(tx, assetIDs, conversationID, ownerID, expiresAt); err != nil {
		return err
	}
	if err := s.renewImageGenerationExpiry(tx, filteredRoots, conversationID, ownerID, expiresAt); err != nil {
		return err
	}
	for contentOrder, assetID := range assetIDs {
		if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_asset_references")+` (
			asset_id, conversation_id, turn_id, message_id, reference_kind, content_order, created_at, expires_at
		) VALUES (?, ?, ?, ?, 'user_input', ?, ?, ?)
		ON CONFLICT (message_id, content_order) DO NOTHING`),
			assetID, conversationID, turnID.String, messageID, contentOrder, normalizedNow, expiresAt); err != nil {
			return err
		}
	}
	return nil
}

// imageGenerationRootAssetIDs mirrors listChatImageGenerationRootAssetIdsInClient.
func (s *Store) imageGenerationRootAssetIDs(q queryer, assetIDs []string, conversationID, ownerID string) ([]string, error) {
	ids := uniqueStrings(assetIDs)
	if len(ids) == 0 {
		return []string{}, nil
	}
	rows, err := q.Query(s.bind(`SELECT root_asset_id FROM `+s.table("chat_image_generations")+`
		WHERE asset_id IN (`+placeholders(len(ids))+`)
			AND conversation_id = ? AND system_account_id = ?`),
		append(toAnySlice(ids), conversationID, ownerID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, err
		}
		normalized, err := normalizedAssetID(root)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return uniqueStrings(out), nil
}

// renewImageGenerationExpiry mirrors renewChatImageGenerationExpiryInClient.
func (s *Store) renewImageGenerationExpiry(q queryer, assetIDs []string, conversationID, ownerID, expiresAt string) error {
	ids := uniqueStrings(assetIDs)
	if len(ids) == 0 {
		return nil
	}
	args := append([]any{expiresAt, expiresAt}, toAnySlice(ids)...)
	args = append(args, conversationID, ownerID)
	_, err := q.Exec(s.bind(`UPDATE `+s.table("chat_image_generations")+`
		SET expires_at = CASE WHEN expires_at < ? THEN ? ELSE expires_at END
		WHERE asset_id IN (`+placeholders(len(ids))+`)
			AND conversation_id = ? AND system_account_id = ?`), args...)
	return err
}

// AssetAPIMetadata mirrors ChatAssetApiMetadata.
type AssetAPIMetadata struct {
	ID        string `json:"id"`
	FileName  string `json:"fileName"`
	MimeType  string `json:"mimeType"`
	Width     int64  `json:"width"`
	Height    int64  `json:"height"`
	ByteSize  int64  `json:"byteSize"`
}

// AssetAPIMetadataOf mirrors chatAssetApiMetadata.
func AssetAPIMetadataOf(asset *Asset) (*AssetAPIMetadata, error) {
	if asset.ProcessingStatus != "ready" || asset.ProcessedMimeType == nil ||
		asset.ProcessedWidth == nil || asset.ProcessedHeight == nil || asset.ProcessedBytes == nil {
		return nil, &DomainError{Message: "只有处理完成的聊天资产才能转换为上传响应"}
	}
	return &AssetAPIMetadata{
		ID:       asset.ID,
		FileName: asset.OriginalFilename,
		MimeType: *asset.ProcessedMimeType,
		Width:    *asset.ProcessedWidth,
		Height:   *asset.ProcessedHeight,
		ByteSize: *asset.ProcessedBytes,
	}, nil
}
