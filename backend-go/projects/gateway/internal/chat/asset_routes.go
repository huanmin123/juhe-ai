package chat

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// Asset lifecycle routes ported from chat.routes.ts (POST .../assets,
// GET .../assets/:assetId/content, DELETE .../assets/:assetId) and
// chat-asset-upload.ts. Limits, storage keys, headers and Chinese error
// strings mirror Node byte for byte; image decoding/preview generation ride
// the ImageProcessor port.

// uploadChatAsset mirrors uploadChatAsset for one multipart request.
func (rt *chatRoutes) uploadChatAsset(r *http.Request, ownerID, conversationID string) (*Asset, error) {
	nowValue := rt.now()
	availableSlots, err := rt.deps.Store.AssertChatAssetUploadSlotAvailable(ownerID, conversationID, nowValue)
	if err != nil {
		var countExceeded *AssetCountExceededError
		if errors.As(err, &countExceeded) {
			return nil, &AssetUploadError{Code: "chat_asset_count_exceeded", StatusCode: http.StatusBadRequest, Message: err.Error()}
		}
		return nil, err
	}
	if availableSlots <= 0 {
		return nil, &AssetUploadError{Code: "chat_asset_count_exceeded", StatusCode: http.StatusBadRequest, Message: "每条消息最多 5 张图片，请移除图片后重试"}
	}
	if rt.deps.ImageProcessor == nil || rt.deps.ObjectStore == nil {
		return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "图片上传必须使用 multipart/form-data"}
	}
	uploaded, err := readMultipartImage(r)
	if err != nil {
		return nil, err
	}
	processed, err := rt.deps.ImageProcessor.ProcessUpload(uploaded.data, uploaded.declaredMimeType)
	if err != nil {
		var processingErr *ImageProcessingError
		if errors.As(err, &processingErr) {
			return nil, &AssetUploadError{Code: "chat_asset_unsupported_type", StatusCode: http.StatusUnsupportedMediaType, Message: processingErr.Message}
		}
		return nil, &AssetUploadError{Code: "chat_asset_unsupported_type", StatusCode: http.StatusUnsupportedMediaType, Message: "图片无法完成解码或压缩，请更换图片后重试"}
	}
	if processed.OriginalMimeType != uploaded.declaredMimeType {
		return nil, &AssetUploadError{Code: "chat_asset_unsupported_type", StatusCode: http.StatusUnsupportedMediaType, Message: "图片实际格式与上传 MIME 不一致"}
	}
	asset, err := rt.deps.Store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID:  ownerID,
		ConversationID:   conversationID,
		SourceKind:       "user_upload",
		OriginalFilename: uploaded.filename,
		OriginalMimeType: processed.OriginalMimeType,
		OriginalWidth:    nilIfZero(processed.OriginalWidth),
		OriginalHeight:   nilIfZero(processed.OriginalHeight),
		OriginalBytes:    uploaded.bytes,
		OriginalSha256:   uploaded.sha256,
		QuotaBytes:       processed.ByteSize,
		Now:              nowValue,
		RetentionDays:    rt.deps.RetentionDays,
	})
	if err != nil {
		var quotaExceeded *AssetQuotaExceededError
		if errors.As(err, &quotaExceeded) {
			return nil, &AssetUploadError{Code: "chat_asset_quota_exceeded", StatusCode: http.StatusRequestEntityTooLarge, Message: err.Error()}
		}
		var countExceeded *AssetCountExceededError
		if errors.As(err, &countExceeded) {
			return nil, &AssetUploadError{Code: "chat_asset_count_exceeded", StatusCode: http.StatusBadRequest, Message: err.Error()}
		}
		return nil, err
	}
	storageKey := StorageKeyForChatAsset(asset.ID, processed.SHA256, processed.MimeType, "")
	if err := rt.deps.ObjectStore.Write(storageKey, processed.Buffer, chatAssetProcessedMaxBytes, processed.SHA256); err != nil {
		_, _ = rt.deps.Store.FailChatAssetProcessing(asset.ID, ownerID, conversationID, "chat_asset_processing_failed", nowValue)
		return nil, err
	}
	completed, err := rt.deps.Store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID:           asset.ID,
		SystemAccountID:   ownerID,
		ConversationID:    conversationID,
		ProcessedMimeType: processed.MimeType,
		ProcessedWidth:    processed.Width,
		ProcessedHeight:   processed.Height,
		ProcessedBytes:    processed.ByteSize,
		ProcessedSha256:   processed.SHA256,
		StorageKey:        storageKey,
		Now:               nowValue,
	})
	if err != nil {
		return nil, err
	}
	return completed, nil
}

func nilIfZero(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

type uploadedImage struct {
	data             []byte
	filename         string
	declaredMimeType string
	bytes            int64
	sha256           string
}

// readMultipartImage mirrors readMultipartImage: exactly one `file` field, no
// extra parts, 3 MiB cap, normalized filename and declared MIME.
func readMultipartImage(r *http.Request) (*uploadedImage, error) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "multipart/form-data;") && !strings.HasPrefix(contentType, "multipart/form-data,") {
		return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "图片上传必须使用 multipart/form-data"}
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "multipart 请求格式无效"}
	}
	reader, readerErr := r.MultipartReader()
	if readerErr != nil || reader == nil {
		return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "multipart 请求格式无效"}
	}
	_ = mediaType
	var uploaded *uploadedImage
	partCount := 0
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "图片上传连接已中断"}
		}
		partCount++
		if partCount > 2 {
			_ = part.Close()
			return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "每次只能上传一张图片"}
		}
		if part.FormName() != "file" {
			_ = part.Close()
			if uploaded == nil {
				// Busboy counts non-file parts against fieldsLimit (0): any
				// extra form field is rejected before the file part.
				return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "图片上传不能包含额外表单字段"}
			}
			return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "每次只能上传一张图片"}
		}
		if uploaded != nil {
			_ = part.Close()
			return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "每次只能上传一个 file 图片字段"}
		}
		filename, filenameErr := normalizedUploadFilename(part.FileName())
		if filenameErr != nil {
			_ = part.Close()
			return nil, filenameErr
		}
		declared, declaredErr := normalizedDeclaredMimeType(part.Header.Get("Content-Type"))
		if declaredErr != nil {
			_ = part.Close()
			return nil, declaredErr
		}
		data, readDataErr := io.ReadAll(io.LimitReader(part, chatAssetOriginalMaxBytes+1))
		_ = part.Close()
		if readDataErr != nil {
			return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "图片上传连接已中断"}
		}
		if int64(len(data)) > chatAssetOriginalMaxBytes {
			return nil, &AssetUploadError{Code: "chat_asset_too_large", StatusCode: http.StatusRequestEntityTooLarge, Message: "单张上传图片不能超过 3 MiB"}
		}
		if len(data) == 0 {
			return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "上传图片不能为空"}
		}
		digest := sha256.Sum256(data)
		uploaded = &uploadedImage{
			data:             data,
			filename:         filename,
			declaredMimeType: declared,
			bytes:            int64(len(data)),
			sha256:           hexEncode(digest[:]),
		}
	}
	if uploaded == nil {
		return nil, &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "缺少 file 图片字段"}
	}
	return uploaded, nil
}

func normalizedUploadFilename(value string) (string, error) {
	segments := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' })
	filename := ""
	if len(segments) > 0 {
		filename = strings.TrimSpace(segments[len(segments)-1])
	}
	filename = controlCharPattern.ReplaceAllString(filename, "")
	if filename == "" {
		return "", &AssetUploadError{Code: "chat_asset_invalid_request", StatusCode: http.StatusBadRequest, Message: "图片文件名不能为空"}
	}
	runes := []rune(filename)
	if len(runes) > 255 {
		filename = string(runes[:255])
	}
	return filename, nil
}

func normalizedDeclaredMimeType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if idx := strings.Index(normalized, ";"); idx >= 0 {
		normalized = strings.TrimSpace(normalized[:idx])
	}
	switch normalized {
	case "image/jpg", "image/pjpeg":
		return "image/jpeg", nil
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return normalized, nil
	}
	return "", &AssetUploadError{Code: "chat_asset_unsupported_type", StatusCode: http.StatusUnsupportedMediaType, Message: "仅支持 JPEG、PNG、WebP 或 GIF 图片"}
}

// uploadAsset mirrors POST /conversations/:conversationId/assets.
func (rt *chatRoutes) uploadAsset(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeChatRouteError(w, &ConversationNotFoundError{})
		return
	}
	asset, err := rt.uploadChatAsset(r, ownerID, conversation.ID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	metadata, err := AssetAPIMetadataOf(asset)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	writeOKStatus(w, http.StatusCreated, metadata)
}

// assetContent mirrors GET /conversations/:conversationId/assets/:assetId/content.
func (rt *chatRoutes) assetContent(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if err := ensureStrictQueryKeys(query, "variant", "download"); err != nil {
		writeChatRouteError(w, err)
		return
	}
	variant := "original"
	if raw := textQuery(query.Get("variant")); raw != nil {
		if *raw != "preview" && *raw != "original" {
			writeChatRouteError(w, &invalidRequestError{Message: "Invalid enum value. Expected 'preview' | 'original', received '" + *raw + "'"})
			return
		}
		variant = *raw
	}
	download := ""
	if raw := textQuery(query.Get("download")); raw != nil {
		if *raw != "0" && *raw != "1" {
			writeChatRouteError(w, &invalidRequestError{Message: "Invalid enum value. Expected '0' | '1', received '" + *raw + "'"})
			return
		}
		download = *raw
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeChatRouteError(w, &ConversationNotFoundError{})
		return
	}
	asset, err := rt.deps.Store.GetAsset(r.PathValue("assetId"), ownerID, conversation.ID, rt.now())
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if asset == nil || asset.ProcessingStatus != "ready" || asset.StorageKey == nil ||
		asset.ProcessedMimeType == nil || asset.ProcessedSha256 == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "图片不存在或已过期"})
		return
	}
	previewAvailable := asset.PreviewStorageKey != nil && asset.PreviewMimeType != nil && asset.PreviewSha256 != nil && asset.PreviewBytes != nil
	usePreview := variant == "preview" && previewAvailable
	storageKey := derefString(asset.StorageKey)
	mimeType := *asset.ProcessedMimeType
	sha256Value := *asset.ProcessedSha256
	maxBytes := int64(chatAssetProcessedMaxBytes)
	if usePreview {
		storageKey = *asset.PreviewStorageKey
		mimeType = *asset.PreviewMimeType
		sha256Value = *asset.PreviewSha256
		maxBytes = chatAssetPreviewMaxBytes
	} else if asset.SourceKind == "assistant_generated" {
		maxBytes = chatAssetGeneratedMaxBytes
	}
	etag := "\"" + sha256Value + "\""
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if requestEtagMatches(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if download == "1" && !usePreview {
		extension := "bin"
		switch mimeType {
		case "image/png":
			extension = "png"
		case "image/jpeg":
			extension = "jpg"
		case "image/webp":
			extension = "webp"
		}
		w.Header().Set("Content-Disposition", "attachment; filename=\"generated-"+asset.ID+"."+extension+"\"")
	} else {
		w.Header().Set("Content-Disposition", "inline")
	}
	if rt.deps.ObjectStore == nil {
		writeChatRouteError(w, &DomainError{Message: "聊天资产存储不可用"})
		return
	}
	data, objectBytes, err := rt.deps.ObjectStore.Open(storageKey, maxBytes)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Length", itoa(int(objectBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func requestEtagMatches(values []string, etag string) bool {
	header := strings.Join(values, ",")
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		normalized := strings.TrimSpace(candidate)
		if normalized == "*" || normalized == etag || normalized == "W/"+etag {
			return true
		}
	}
	return false
}

// deleteAsset mirrors DELETE /conversations/:conversationId/assets/:assetId
// (the object-deletion half).
func (rt *chatRoutes) deleteAsset(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeChatRouteError(w, &ConversationNotFoundError{})
		return
	}
	nowValue := rt.now()
	claim, err := rt.deps.Store.ClaimUncommittedAssetForDeletion(r.PathValue("assetId"), ownerID, conversation.ID, nowValue)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if claim == nil {
		writeMessageCode(w, http.StatusConflict, "图片已发送、已删除或当前不能清理", "chat_asset_not_deletable")
		return
	}
	if rt.deps.ObjectStore != nil {
		_ = rt.deps.ObjectStore.Delete([]string{derefString(claim.Asset.StorageKey), derefString(claim.Asset.PreviewStorageKey)})
	}
	completed, err := rt.deps.Store.CompleteAssetDeletion(claim.Asset.ID, claim.ClaimID)
	if err != nil || !completed {
		retryAt := rt.releaseRetryAt()
		_, _ = rt.deps.Store.ReleaseAssetDeletionClaim(claim.Asset.ID, claim.ClaimID, "chat_asset_delete_failed", retryAt, nowValue)
		if err != nil {
			writeChatRouteError(w, err)
			return
		}
		writeChatRouteError(w, &DomainError{Message: "聊天图片删除认领已变化"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// releaseRetryAt renders now + passiveScheduleDelayMs(60_000) with the base
// interval (jitter is applied by the clock owner).
func (rt *chatRoutes) releaseRetryAt() string {
	if rt.deps.Now != nil {
		return isoMillis(rt.deps.Now().Add(60_000))
	}
	return isoMillis(nowWallclock().Add(60_000))
}
