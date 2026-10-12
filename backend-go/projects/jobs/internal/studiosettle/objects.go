// studiosettle 的对象存储写面（媒体工作室设计 §3.1/§5.5：产物对象复用
// chat_assets.storage_key 同源端口语义，不新造存储栈）。
//
// 选型说明（交付报告口径）：对象存储访问为 jobs 侧自建最小 writer——镜像
// gateway chatAssetObjectStore（chain_chat.go）的本地文件实现语义：一个
// storage key 一个文件、写入按期望 SHA-256 与大小上限双守卫、路径清洗防目
// 录穿越。不经共享 platform 包（gateway 的实现位于 cmd 包内非共享位；studio
// 域 storage key 段为 stasset 前缀，与 chat 键空间不交）；生产装配指向与
// gateway 同根的 JUHE_AI_CHAT_ASSETS_ROOT（组合根已有配置项），单机本地文
// 件系统形态下同根即同源。
package studiosettle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ObjectWriter 是产物对象写删端口（gateway studio.ObjectStore 同款三面：
// Write/Open/Delete；jobs 结算只消费 Write/Delete，Open 保留给排障）。
type ObjectWriter interface {
	Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error
	Delete(storageKeys []string) error
}

// LocalObjectWriter 是对象存储端口的最小本地实现（chatAssetObjectStore 镜像：
// 单文件 per storage key、SHA-256 写入守卫、目录穿越拒绝）。
type LocalObjectWriter struct {
	Root string
}

// NewLocalObjectWriter 构造并确保根目录存在。
func NewLocalObjectWriter(root string) (*LocalObjectWriter, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("studio 资产根目录未配置（JUHE_AI_CHAT_ASSETS_ROOT）")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建 studio 资产根目录失败: %w", err)
	}
	return &LocalObjectWriter{Root: root}, nil
}

func (w *LocalObjectWriter) path(storageKey string) (string, error) {
	clean := filepath.Clean("/" + storageKey)
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("studio 资产存储键不合法: %q", storageKey)
	}
	return filepath.Join(w.Root, filepath.FromSlash(clean[1:])), nil
}

// Write 按 storage key 落一个对象文件（期望 SHA-256 与大小上限双守卫，
// gateway chatAssetObjectStore.Write 同语义）。
func (w *LocalObjectWriter) Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error {
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("studio 资产超过大小上限 %d", maxBytes)
	}
	sum := sha256.Sum256(data)
	if expectedSHA256 != "" && !strings.EqualFold(hex.EncodeToString(sum[:]), expectedSHA256) {
		return errors.New("studio 资产内容校验失败")
	}
	target, err := w.path(storageKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

// Delete 按 storage key 删对象（不存在按成功结算，排障幂等）。
func (w *LocalObjectWriter) Delete(storageKeys []string) error {
	for _, storageKey := range storageKeys {
		target, err := w.path(storageKey)
		if err != nil {
			return err
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// StorageKey 承载对象存储键（gateway studioStorageKey 同款端口语义：摘要分
// 桶 + 资产段 + 摘要短码 + 扩展名；stasset 段保证与 chat 键空间不交）。
func StorageKey(assetID, sha256Value, mimeType string) string {
	digest := strings.ToLower(strings.TrimSpace(sha256Value))
	if len(digest) < 4 {
		// sha256 hex 恒 64 位；防御分支保持可定位形状。
		digest = digest + "0000"
	}
	extension := assetExtension(mimeType)
	short := digest
	if len(short) > 16 {
		short = short[:16]
	}
	return digest[:2] + "/" + digest[2:4] + "/" + assetID + "-" + short + extension
}

// assetExtension 是 gateway studio assetExtension 的镜像（结算产物只落
// image/video 两族键；未知 mime 落无扩展名键，内容以 sha256 定位）。
func assetExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	return ""
}

// ImageMimeFromBytes 是图像魔数识别（gateway studioImageMimeFromBytes 镜像：
// http.DetectContentType 限 png/jpeg/webp 词表，识别失败返回空串由调用方
// 结算失败收口，不猜测格式）。
func ImageMimeFromBytes(data []byte) string {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	detected := http.DetectContentType(head)
	switch detected {
	case "image/png", "image/jpeg", "image/webp":
		return detected
	}
	return ""
}
