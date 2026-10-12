package chat

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Upstream dispatch port and chat asset object storage, previously hosted in
// generation_images.go（生成工具退场后共享基础设施收敛到本文件）：GenerationExecutor
// 是模型轮次、压缩摘要与图片语义观察共用的上游派发端口；ObjectStore/
// LocalObjectStore/StorageKeyForChatAsset 是用户图片上传链的存储契约。

// GenerationExecutor is the upstream dispatch port (Node dispatchChatGatewayRequest
// as consumed by the chat generation pipeline: model rounds, compaction
// summarization and image observations). The composition root (G20) binds the
// production implementation; tests use mock executors.
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

const (
	chatAssetGeneratedMaxBytes = 16 * 1024 * 1024
	chatAssetPreviewMaxBytes   = 512 * 1024
	chatAssetOriginalMaxBytes  = 3 * 1024 * 1024
	chatAssetProcessedMaxBytes = 3 * 1024 * 1024
	// chatMediaAudioMaxBytes / chatMediaVideoMaxBytes 是存量媒体产物（音频/
	// 视频）资产读取限额（问答音视频工具设计 §2.1：32MB/64MB，与生成侧下载
	// 限额一致；生成执行器已退场，读取侧保留供存量历史 3 天保留期内回看）。
	chatMediaAudioMaxBytes = 32 * 1024 * 1024
	chatMediaVideoMaxBytes = 64 * 1024 * 1024
)

// chatMimeTypeIsAudio / chatMimeTypeIsVideo 报告 MIME 词表归属（存量媒体资产
// 读取侧按类别套用限额）。
func chatMimeTypeIsAudio(mimeType string) bool {
	return strings.HasPrefix(mimeType, "audio/")
}

func chatMimeTypeIsVideo(mimeType string) bool {
	return strings.HasPrefix(mimeType, "video/")
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
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	case "audio/mp4":
		return ".m4a"
	case "video/webm":
		return ".webm"
	case "video/mp4":
		return ".mp4"
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
