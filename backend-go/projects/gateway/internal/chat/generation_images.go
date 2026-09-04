package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Image generation transport, artifact sink and object storage ports ported
// from chat-image-generation-transport.ts, tools/artifact-sink.ts,
// chat-image-edit-references.ts and storage/chat-asset-storage.ts.

// GenerationExecutor is the upstream dispatch port (Node dispatchChatGatewayRequest
// as consumed by the chat generation pipeline: model rounds, compaction
// summarization, image generation and image observations). The composition
// root (G20) binds the production implementation; tests use mock executors.
type GenerationExecutor interface {
	Dispatch(ctx requestContext, req GenerationDispatchRequest) (*GenerationDispatchResponse, error)
}

// requestContext aliases context.Context so executor signatures stay stable.
type requestContext = context.Context

// GenerationDispatchRequest mirrors the RequestInit passed to
// dispatchChatGatewayRequest.
type GenerationDispatchRequest struct {
	Path    string
	Method  string
	Headers map[string]string
	Body    []byte
}

// GenerationDispatchResponse mirrors the consumed subset of the upstream
// Response: status plus the (streaming) body.
type GenerationDispatchResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// ChatImageEditReference mirrors ChatImageEditReference with in-memory bytes
// instead of a file stream.
type ChatImageEditReference struct {
	AssetID  string
	Data     []byte
	Bytes    int64
	MimeType string
	Filename string
}

const (
	chatImageEditMaxReferenceImages = 5
	chatImageEditMaxReferenceBytes  = 48 * 1024 * 1024
	chatAssetGeneratedMaxBytes      = 16 * 1024 * 1024
	chatAssetPreviewMaxBytes        = 512 * 1024
	chatAssetOriginalMaxBytes       = 3 * 1024 * 1024
	chatAssetProcessedMaxBytes      = 3 * 1024 * 1024
)

// validateChatImageEditReferenceLimits mirrors validateChatImageEditReferenceLimits.
func validateChatImageEditReferenceLimits(references []ChatImageEditReference) error {
	if len(references) == 0 {
		return errors.New("编辑图片必须至少引用一张图片")
	}
	if len(references) > chatImageEditMaxReferenceImages {
		return errors.New("编辑图片最多引用 5 张图片")
	}
	var totalBytes int64
	for _, reference := range references {
		if reference.Bytes <= 0 || reference.Bytes > chatAssetGeneratedMaxBytes {
			return errors.New("引用图片字节数无效")
		}
		totalBytes += reference.Bytes
		if totalBytes > chatImageEditMaxReferenceBytes {
			return errors.New("编辑图片引用总大小不能超过 48 MiB")
		}
	}
	return nil
}

// ChatImageGenerationRequest mirrors the context.imageGeneration input.
type ChatImageGenerationRequest struct {
	Operation    string // generate|edit
	Model        string
	Prompt       string
	Size         string
	Quality      string
	OutputFormat string
	References   []ChatImageEditReference
}

// ChatImageGenerationToolResult mirrors ChatImageToolTransportResult with
// in-memory bytes (Node spools to a temp file).
type ChatImageGenerationToolResult struct {
	Data          []byte
	Bytes         int64
	SHA256        string
	MimeType      string // image/jpeg|image/png|image/webp
	Width         int64
	Height        int64
	RevisedPrompt string
}

// ChatImageGenerationRequestError mirrors ChatImageGenerationRequestError.
type ChatImageGenerationRequestError struct {
	Message    string
	StatusCode int
	Code       PublicChatGenerationErrorCode
}

func (e *ChatImageGenerationRequestError) Error() string { return e.Message }

// GenerateChatImage mirrors generateChatImage: builds the request, dispatches
// through the executor, decodes b64_json and verifies the payload.
func GenerateChatImage(ctx requestContext, executor GenerationExecutor, input ChatImageGenerationRequest, apiKey, traceID string) (ChatImageGenerationToolResult, error) {
	result := ChatImageGenerationToolResult{}
	model := trimSpace(input.Model)
	prompt := trimSpace(input.Prompt)
	if model == "" {
		return result, errors.New("图像生成模型不能为空")
	}
	if prompt == "" {
		return result, errors.New("图像生成提示词不能为空")
	}
	size, err := normalizeChatImageSize(input.Size)
	if err != nil {
		return result, err
	}
	quality := input.Quality
	if quality == "" {
		quality = "auto"
	} else if _, err := normalizeChatImageQuality(quality); err != nil {
		return result, err
	}
	outputFormat := input.OutputFormat
	if outputFormat == "" {
		outputFormat = "webp"
	} else if normalized, err := normalizeChatImageOutputFormat(outputFormat); err != nil {
		return result, err
	} else {
		outputFormat = normalized
	}
	headers := map[string]string{
		"authorization": "Bearer " + apiKey,
		"accept":        "application/json",
	}
	if traceID != "" {
		headers["x-trace-id"] = traceID
	}
	var dispatch GenerationDispatchRequest
	if len(input.References) > 0 {
		if err := validateChatImageEditReferenceLimits(input.References); err != nil {
			return result, err
		}
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		_ = writer.WriteField("model", model)
		_ = writer.WriteField("prompt", prompt)
		_ = writer.WriteField("size", size)
		_ = writer.WriteField("quality", quality)
		_ = writer.WriteField("output_format", outputFormat)
		for _, reference := range input.References {
			part, partErr := writer.CreateFormFile("image[]", reference.Filename)
			if partErr != nil {
				return result, partErr
			}
			if _, partErr := part.Write(reference.Data); partErr != nil {
				return result, partErr
			}
		}
		if err := writer.Close(); err != nil {
			return result, err
		}
		dispatch = GenerationDispatchRequest{Path: "/v1/images/edits", Method: "POST", Headers: headers, Body: body.Bytes()}
		dispatch.Headers["content-type"] = writer.FormDataContentType()
	} else {
		payload, _ := json.Marshal(map[string]any{
			"model": model, "prompt": prompt, "n": 1, "size": size, "quality": quality, "output_format": outputFormat,
		})
		dispatch = GenerationDispatchRequest{Path: "/v1/images/generations", Method: "POST", Headers: headers, Body: payload}
		dispatch.Headers["content-type"] = "application/json"
	}
	response, err := executor.Dispatch(ctx, dispatch)
	if err != nil {
		return result, err
	}
	if response == nil {
		return result, errors.New("图像生成请求失败")
	}
	bodyBytes, readErr := io.ReadAll(io.LimitReader(response.Body, 128*1024*1024))
	_ = response.Body.Close()
	if readErr != nil {
		return result, readErr
	}
	if response.Status < 200 || response.Status >= 300 {
		fallback := fmt.Sprintf("图像生成请求失败（HTTP %d）", response.Status)
		message, errorType := readImageGenerationErrorPayload(string(bodyBytes), fallback)
		return result, &ChatImageGenerationRequestError{
			Message:    message,
			StatusCode: response.Status,
			Code:       imageGenerationPublicErrorCode(response.Status, message, errorType),
		}
	}
	base64Values := extractImageResultChunksWithFields(string(bodyBytes), "b64_json")
	if len(base64Values) == 0 || base64Values[0] == "" {
		return result, errors.New("图像生成响应缺少 b64_json")
	}
	decoded, err := decodeBase64Payload(base64Values[0], chatAssetGeneratedMaxBytes)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(decoded)
	result.Data = decoded
	result.Bytes = int64(len(decoded))
	result.SHA256 = hexEncode(digest[:])
	result.MimeType = imageMimeTypeFromBytes(decoded)
	if result.MimeType == "" {
		return result, errors.New("生成图片缺少有效 MIME 或尺寸")
	}
	width, height := imageDimensionsFromBytes(decoded)
	if width == 0 || height == 0 {
		return result, errors.New("生成图片缺少有效 MIME 或尺寸")
	}
	result.Width = width
	result.Height = height
	result.RevisedPrompt = extractJSONStringField(string(bodyBytes), "revised_prompt")
	return result, nil
}

func extractImageResultChunksWithFields(data string, fields ...string) []string {
	_, values, err := stripImageResultStrings(data, fields...)
	if err != nil {
		return nil
	}
	return values
}

func readImageGenerationErrorPayload(body, fallback string) (string, string) {
	var payload struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err == nil && payload.Error != nil {
		message := trimSpace(payload.Error.Message)
		if message != "" {
			return message, trimSpace(payload.Error.Type)
		}
	}
	return fallback, ""
}

var imageGenerationNotEnabledPattern = regexp.MustCompile(`(?i)\bimage generation is not enabled for (?:this|the) group\b`)

func imageGenerationPublicErrorCode(statusCode int, message, errorType string) PublicChatGenerationErrorCode {
	if statusCode == 403 && strings.ToLower(errorType) == "permission_error" && imageGenerationNotEnabledPattern.MatchString(message) {
		return GenErrImageNotEnabled
	}
	if statusCode == 401 || statusCode == 403 {
		return GenErrImagePermissionDenied
	}
	if statusCode == 429 {
		return GenErrImageRateLimited
	}
	if statusCode == 400 || statusCode == 422 {
		return GenErrImageRequestRejected
	}
	return GenErrImageFailed
}

func decodeBase64Payload(value string, maxBytes int64) ([]byte, error) {
	cleaned := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(value)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, errors.New("生成图片 Base64 无法解码")
	}
	if int64(len(decoded)) <= 0 {
		return nil, errors.New("生成图片 Base64 不能为空")
	}
	if int64(len(decoded)) > maxBytes {
		return nil, errors.New("生成图片超过 16 MiB 上限")
	}
	return decoded, nil
}

func imageMimeTypeFromBytes(data []byte) string {
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' && data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png"
	}
	return ""
}

func imageDimensionsFromBytes(data []byte) (int64, int64) {
	switch imageMimeTypeFromBytes(data) {
	case "image/png":
		if len(data) >= 24 {
			return int64(uint32At(data, 16)), int64(uint32At(data, 20))
		}
	case "image/jpeg":
		return jpegDimensions(data)
	case "image/webp":
		return webpDimensions(data)
	}
	return 0, 0
}

func uint32At(data []byte, offset int) uint32 {
	return uint32(data[offset])<<24 | uint32(data[offset+1])<<16 | uint32(data[offset+2])<<8 | uint32(data[offset+3])
}

func jpegDimensions(data []byte) (int64, int64) {
	index := 2
	for index+9 < len(data) {
		if data[index] != 0xFF {
			index++
			continue
		}
		marker := data[index+1]
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 || marker == 0xC3 {
			height := int64(uint16At(data, index+5))
			width := int64(uint16At(data, index+7))
			return width, height
		}
		length := int(uint16At(data, index+2))
		if length <= 0 {
			return 0, 0
		}
		index += 2 + length
	}
	return 0, 0
}

func uint16At(data []byte, offset int) uint16 {
	return uint16(data[offset])<<8 | uint16(data[offset+1])
}

func webpDimensions(data []byte) (int64, int64) {
	if len(data) < 30 {
		return 0, 0
	}
	chunk := string(data[12:16])
	switch chunk {
	case "VP8 ":
		width := int64(uint16At(data, 26) & 0x3FFF)
		height := int64(uint16At(data, 28) & 0x3FFF)
		return width, height
	case "VP8L":
		bits := uint32(data[24]) | uint32(data[25])<<8 | uint32(data[26])<<16 | uint32(data[27])<<24
		width := int64(bits&0x3FFF + 1)
		height := int64(bits>>14&0x3FFF + 1)
		return width, height
	case "VP8X":
		width := int64(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16) + 1
		height := int64(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16) + 1
		return width, height
	}
	return 0, 0
}

// --- artifact sink ---

// GeneratedImageCommitInput mirrors the commitGeneratedImage input.
type GeneratedImageCommitInput struct {
	Result         ChatImageGenerationToolResult
	Operation      string
	Model          string
	Prompt         string
	SourceAssetIDs []string
	Size           string
	Quality        string
	OutputFormat   string
}

// GeneratedImageCommitResult mirrors the commitGeneratedImage output.
type GeneratedImageCommitResult struct {
	AssetID         string
	MimeType        string
	Width           int64
	Height          int64
	Bytes           int64
	PreviewMimeType string
	PreviewWidth    int64
	PreviewHeight   int64
	PreviewBytes    int64
}

// ChatGeneratedImageArtifactSink mirrors ChatGeneratedImageArtifactSink.
type ChatGeneratedImageArtifactSink interface {
	CommitGeneratedImage(input GeneratedImageCommitInput) (GeneratedImageCommitResult, error)
}

// ObjectStore is the chat asset object storage port (Node
// chat-asset-storage.ts writeChatAssetObject/openChatAssetObject/
// deleteChatAssetObjects). Keys follow storageKeyForChatAsset.
type ObjectStore interface {
	Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error
	Open(storageKey string, maxBytes int64) ([]byte, int64, error)
	Delete(storageKeys []string) error
}

// chatAssetObjectExtension mirrors extensionForChatAssetMimeType.
func chatAssetObjectExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	}
	return ".bin"
}

var storageSegmentSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func safeStorageSegment(value string, maxLength int) string {
	normalized := storageSegmentSanitizer.ReplaceAllString(value, "_")
	if normalized == "" {
		normalized = "_"
	}
	runes := []rune(normalized)
	if len(runes) > maxLength {
		normalized = string(runes[:maxLength])
	}
	return normalized
}

func normalizedSHA256(value string) string {
	normalized := strings.ToLower(trimSpace(value))
	if !digestPattern.MatchString(normalized) {
		return strings.Repeat("0", 64)
	}
	return normalized
}

// StorageKeyForChatAsset mirrors storageKeyForChatAsset.
func StorageKeyForChatAsset(assetID, sha256Value, mimeType, variant string) string {
	segment := safeStorageSegment(assetID, 120)
	digest := normalizedSHA256(sha256Value)
	extension := chatAssetObjectExtension(mimeType)
	variantSuffix := ""
	if variant != "" {
		variantSuffix = "-" + variant
	}
	return fmt.Sprintf("%s/%s/%s%s-%s%s", digest[:2], digest[2:4], segment, variantSuffix, digest[:16], extension)
}

// LocalObjectStore implements ObjectStore over a filesystem root with the
// Node containment contract (keys must stay inside the root).
type LocalObjectStore struct {
	Root string
}

// NewLocalObjectStore builds the local store, creating the root.
func NewLocalObjectStore(root string) (*LocalObjectStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &LocalObjectStore{Root: root}, nil
}

func (s *LocalObjectStore) path(storageKey string) (string, error) {
	cleaned := filepath.FromSlash(storageKey)
	cleaned = filepath.Clean(cleaned)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", errors.New("聊天资产存储键越出受限目录")
	}
	target := filepath.Join(s.Root, cleaned)
	relative, err := filepath.Rel(s.Root, target)
	if err != nil || relative == "" || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return "", errors.New("聊天资产存储键越出受限目录")
	}
	return target, nil
}

// Write mirrors writeChatAssetObject: temp write with size/sha verification.
func (s *LocalObjectStore) Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error {
	if maxBytes <= 0 {
		maxBytes = chatAssetProcessedMaxBytes
	}
	if int64(len(data)) > maxBytes {
		return errors.New("处理后图片不能超过 " + itoa(int(maxBytes)) + " 字节")
	}
	if len(data) == 0 {
		return errors.New("处理后图片不能为空")
	}
	digest := sha256.Sum256(data)
	actual := hexEncode(digest[:])
	if expectedSHA256 != "" && actual != normalizedSHA256(expectedSHA256) {
		return errors.New("处理后图片哈希校验失败")
	}
	target, err := s.path(storageKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

// Open mirrors openChatAssetObject with full reads.
func (s *LocalObjectStore) Open(storageKey string, maxBytes int64) ([]byte, int64, error) {
	if maxBytes <= 0 {
		maxBytes = chatAssetProcessedMaxBytes
	}
	target, err := s.path(storageKey)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, 0, err
	}
	if info.IsDir() {
		return nil, 0, errors.New("聊天资产不是普通文件")
	}
	if info.Size() <= 0 {
		return nil, 0, errors.New("聊天资产文件为空")
	}
	if info.Size() > maxBytes {
		return nil, 0, errors.New("聊天资产超过读取上限 " + itoa(int(maxBytes)) + " 字节")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, 0, err
	}
	return data, int64(len(data)), nil
}

// Delete mirrors deleteChatAssetObjects: missing files are ignored.
func (s *LocalObjectStore) Delete(storageKeys []string) error {
	for _, key := range storageKeys {
		if trimSpace(key) == "" {
			continue
		}
		target, err := s.path(key)
		if err != nil {
			continue
		}
		_ = os.Remove(target)
	}
	return nil
}

// loadImageEditReferences mirrors loadChatImageEditReferences over the store.
func (s *Store) loadImageEditReferences(objectStore ObjectStore, assetIDs []string, ownerID, conversationID, nowValue string) ([]ChatImageEditReference, error) {
	if len(assetIDs) == 0 {
		return nil, errors.New("编辑图片必须至少引用一张图片")
	}
	if len(assetIDs) > chatImageEditMaxReferenceImages {
		return nil, errors.New("编辑图片最多引用 5 张图片")
	}
	assets, err := s.ListReadyAssetsByID(assetIDs, ownerID, conversationID, nowValue)
	if err != nil {
		return nil, err
	}
	if len(assets) != len(assetIDs) {
		return nil, errors.New("引用图片不存在、已过期或不属于当前会话")
	}
	references := []ChatImageEditReference{}
	var totalBytes int64
	for _, asset := range assets {
		if asset.StorageKey == nil || asset.ProcessedMimeType == nil || asset.ProcessedBytes == nil {
			return nil, errors.New("引用图片没有可读取的处理结果")
		}
		if *asset.ProcessedBytes <= 0 || *asset.ProcessedBytes > chatAssetGeneratedMaxBytes {
			return nil, errors.New("引用图片字节数无效")
		}
		totalBytes += *asset.ProcessedBytes
		if totalBytes > chatImageEditMaxReferenceBytes {
			return nil, errors.New("编辑图片引用总大小不能超过 48 MiB")
		}
		data, objectBytes, err := objectStore.Open(*asset.StorageKey, chatAssetGeneratedMaxBytes)
		if err != nil {
			return nil, err
		}
		if objectBytes != *asset.ProcessedBytes {
			return nil, errors.New("引用图片存储字节与元数据不一致")
		}
		extension := "webp"
		switch *asset.ProcessedMimeType {
		case "image/jpeg":
			extension = "jpg"
		case "image/png":
			extension = "png"
		}
		references = append(references, ChatImageEditReference{
			AssetID:  asset.ID,
			Data:     data,
			Bytes:    objectBytes,
			MimeType: *asset.ProcessedMimeType,
			Filename: asset.ID + "." + extension,
		})
	}
	return references, nil
}
