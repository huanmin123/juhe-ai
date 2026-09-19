package chat

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat/chatassets"
)

// Asset subdomain bridge (REFACTOR-0006 phase C): the asset persistence
// family lives in chatassets (25 former Store methods on AssetStore, dialect/
// clock/time primitives injected as narrow function ports). The facade root
// keeps type aliases, the const aliases and one-line direct-call forwarders
// so the routes/compaction/generation call surface and every test keep their
// original chat.XXX / unqualified names. No interface indirection: each
// forwarder body is a single direct call.

// 类型别名：资产域词汇随迁 chatassets，门面根前缀引用零改动。
type (
	Asset                          = chatassets.Asset
	AssetDeletionClaim             = chatassets.AssetDeletionClaim
	AssetQuotaExceededError        = chatassets.AssetQuotaExceededError
	AssetCountExceededError        = chatassets.AssetCountExceededError
	CreateChatAssetInput           = chatassets.CreateChatAssetInput
	CompleteAssetProcessingInput   = chatassets.CompleteAssetProcessingInput
	GeneratedAssetCommitInput      = chatassets.GeneratedAssetCommitInput
	GeneratedImageGenerationRecord = chatassets.GeneratedImageGenerationRecord
	ImageGenerationRecord          = chatassets.ImageGenerationRecord
)

// 常量别名（资产配额与列清单，单一事实源在 chatassets）。
const (
	assetColumns            = chatassets.AssetColumns
	maxChatAssetsPerMessage = chatassets.MaxChatAssetsPerMessage
	ChatAssetUserMaxBytes   = chatassets.ChatAssetUserMaxBytes
	ChatAssetUserMaxCount   = chatassets.ChatAssetUserMaxCount
)

// AssetAPIMetadata 与 AssetAPIMetadataOf 留守门面根：纯 API 映射（无 SQL），
// 消费面只有 asset_routes.go 与测试，构造 *DomainError 无需跨包端口。
// 正文与拆分前逐字节一致。

// AssetAPIMetadata mirrors ChatAssetApiMetadata.
type AssetAPIMetadata struct {
	ID       string `json:"id"`
	FileName string `json:"fileName"`
	MimeType string `json:"mimeType"`
	Width    int64  `json:"width"`
	Height   int64  `json:"height"`
	ByteSize int64  `json:"byteSize"`
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

// 自由函数转发（测试原位保持；trim 语义由根包 trimSpace 注入）。
func normalizedErrorCode(value string) string {
	return chatassets.NormalizedErrorCode(value, trimSpace)
}

func toAnySlice(values []string) []any {
	return chatassets.ToAnySlice(values)
}

func generatedAssetExtension(mimeType string) string {
	return chatassets.GeneratedAssetExtension(mimeType)
}

// Store 公有方法转发（一行直调；路由/压缩/生成家族调用面零改动）。

func (s *Store) GetAsset(assetID, ownerID, conversationID, now string) (*Asset, error) {
	return s.assets.GetAsset(assetID, ownerID, conversationID, now)
}

func (s *Store) ListReadyAssetsByID(assetIDs []string, ownerID, conversationID, now string) ([]*Asset, error) {
	return s.assets.ListReadyAssetsByID(assetIDs, ownerID, conversationID, now)
}

func (s *Store) ClaimUncommittedAssetForDeletion(assetID, ownerID, conversationID, now string) (*AssetDeletionClaim, error) {
	return s.assets.ClaimUncommittedAssetForDeletion(assetID, ownerID, conversationID, now)
}

func (s *Store) CompleteAssetDeletion(assetID, claimID string) (bool, error) {
	return s.assets.CompleteAssetDeletion(assetID, claimID)
}

func (s *Store) ReleaseAssetDeletionClaim(assetID, claimID, errorCode, retryAt, now string) (bool, error) {
	return s.assets.ReleaseAssetDeletionClaim(assetID, claimID, errorCode, retryAt, now)
}

func (s *Store) AssertChatAssetUploadSlotAvailable(ownerID, conversationID, nowValue string) (int, error) {
	return s.assets.AssertChatAssetUploadSlotAvailable(ownerID, conversationID, nowValue)
}

func (s *Store) CreateChatAsset(input CreateChatAssetInput) (*Asset, error) {
	return s.assets.CreateChatAsset(input)
}

func (s *Store) GetAssetNoExpiryGuard(assetID, ownerID, conversationID string) (*Asset, error) {
	return s.assets.GetAssetNoExpiryGuard(assetID, ownerID, conversationID)
}

func (s *Store) CompleteChatAssetProcessing(input CompleteAssetProcessingInput) (*Asset, error) {
	return s.assets.CompleteChatAssetProcessing(input)
}

func (s *Store) FailChatAssetProcessing(assetID, ownerID, conversationID, errorCode, nowValue string) (bool, error) {
	return s.assets.FailChatAssetProcessing(assetID, ownerID, conversationID, errorCode, nowValue)
}

func (s *Store) CommitChatGeneratedAsset(input GeneratedAssetCommitInput) (*Asset, error) {
	return s.assets.CommitChatGeneratedAsset(input)
}

func (s *Store) ListRecentImageGenerations(conversationID, ownerID, nowValue string, limit int) ([]ImageGenerationRecord, error) {
	return s.assets.ListRecentImageGenerations(conversationID, ownerID, nowValue, limit)
}

// Store 私有方法转发（生产调用点：windows.go / turns.go；其余为测试原位保持）。

func (s *Store) expireChatAssetsForConversation(tx queryer, conversationID, ownerID, now string) error {
	return s.assets.ExpireChatAssetsForConversation(tx, conversationID, ownerID, now)
}

func (s *Store) removeChatAssetReferencesForMessage(tx queryer, ownerID, conversationID, messageID, now string) error {
	return s.assets.RemoveChatAssetReferencesForMessage(tx, ownerID, conversationID, messageID, now)
}

func (s *Store) queryUserInputAssetIDs(tx queryer, conversationID, messageID, now string) ([]string, error) {
	return s.assets.QueryUserInputAssetIDs(tx, conversationID, messageID, now)
}

func (s *Store) commitChatAssetsToMessage(tx queryer, rawAssetIDs []string, ownerID, conversationID, messageID, now string, retentionDays int) error {
	return s.assets.CommitChatAssetsToMessage(tx, rawAssetIDs, ownerID, conversationID, messageID, now, retentionDays)
}

func (s *Store) queryAssets(q queryer, query string, args ...any) ([]*Asset, error) {
	return s.assets.QueryAssets(q, query, args...)
}

func (s *Store) getClaimedAsset(assetID, claimID string) (*Asset, error) {
	return s.assets.GetClaimedAsset(assetID, claimID)
}

func (s *Store) lockAssetUserQuota(tx queryer, ownerID string) error {
	return s.assets.LockAssetUserQuota(tx, ownerID)
}

func (s *Store) imageGenerationRootAssetIDs(q queryer, assetIDs []string, conversationID, ownerID string) ([]string, error) {
	return s.assets.ImageGenerationRootAssetIDs(q, assetIDs, conversationID, ownerID)
}
