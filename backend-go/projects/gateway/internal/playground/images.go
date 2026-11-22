package playground

// 生图共享辅助（v1.2：POST /images 同步生图端点随 §8/v1.2 退役，生图改异步
// 任务链 imagejob_runner.go/imagejob_http.go）。本文件只保留退役同步链路的
// 共用底层：请求/响应解码、MIME 嗅探与 URL 回退下载客户端——图像任务执行器
// （buildEditsMultipart/decodedImagesFromPayload）与视频首帧（videos.go 的
// inputImageBase64）继续复用，不再有独立 HTTP 入口。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 生图限额（设计 §3.4；payload 上限沿 chat 面 chatAssetGeneratedMaxBytes 16MiB）。
const (
	imageGeneratedMaxBytes = 16 * 1024 * 1024
	imagesPromptMaxBytes   = 32 * 1024        // 提示词上限（防滥用；/v1 链另有参数校验）
	inputImageMaxBytes     = 16 * 1024 * 1024 // 参考图字节上限（视频首帧复用）
)

// imageResultEntry 是单张图像条目（暂存 id + 取流 url；§3.4 参考图上传响应、
// §3.4.4 卡片 images/references 共用形状）。
type imageResultEntry struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// decodedImagesFromPayload 把生图响应 data[] 归一为已解码产物列表（字节 + 嗅
// 探 MIME，供暂存落盘）：b64_json 直解码；url 项经守卫客户端下载字节（grok
// 只回临时链接的既定回退，chat 面 downloadGeneratedImage 同口径）。
func decodedImagesFromPayload(ctx context.Context, body []byte, count int) ([]decodedImage, error) {
	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("响应不是合法 JSON")
	}
	if len(payload.Data) == 0 {
		return nil, errors.New("响应缺少 data 数组")
	}
	images := make([]decodedImage, 0, len(payload.Data))
	for _, item := range payload.Data {
		if clean := strings.TrimSpace(item.B64JSON); clean != "" {
			decoded, err := decodeBase64Image(clean)
			if err != nil {
				return nil, err
			}
			mime := sniffImageMIME(decoded)
			if mime == "" {
				return nil, errors.New("生成图片缺少有效 MIME")
			}
			images = append(images, decodedImage{Data: decoded, MIME: mime})
			continue
		}
		if raw := strings.TrimSpace(item.URL); raw != "" {
			decoded, err := downloadGeneratedImageBytes(ctx, raw)
			if err != nil {
				return nil, err
			}
			mime := sniffImageMIME(decoded)
			if mime == "" {
				return nil, errors.New("生成图片缺少有效 MIME")
			}
			images = append(images, decodedImage{Data: decoded, MIME: mime})
			continue
		}
		return nil, errors.New("生成图片条目缺少 b64_json 或 url")
	}
	if len(images) > count {
		images = images[:count]
	}
	return images, nil
}

// decodeInputImage 解码图生图参考图 base64（裸 base64 或 data URL 均接受），
// 返回字节与嗅探 MIME。
func decodeInputImage(reference string) ([]byte, string, *HTTPError) {
	if dataURL, ok := strings.CutPrefix(reference, "data:"); ok {
		_, payload, found := strings.Cut(dataURL, ",")
		if !found {
			return nil, "", invalidRequest("参考图 data URL 缺少 payload")
		}
		reference = payload
	}
	data, err := base64.StdEncoding.DecodeString(cleanBase64(reference))
	if err != nil || len(data) == 0 {
		return nil, "", invalidRequest("参考图 base64 无法解码")
	}
	if len(data) > inputImageMaxBytes {
		return nil, "", invalidRequest("参考图超过 16 MiB 上限")
	}
	mime := sniffImageMIME(data)
	if mime == "" {
		return nil, "", invalidRequest("参考图必须是 PNG/JPEG/WebP 图片")
	}
	return data, mime, nil
}

// decodeBase64Image 解码 b64_json 产物并校验上限。
func decodeBase64Image(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(cleanBase64(value))
	if err != nil {
		return nil, errors.New("生成图片 Base64 无法解码")
	}
	if len(decoded) == 0 {
		return nil, errors.New("生成图片 Base64 不能为空")
	}
	if len(decoded) > imageGeneratedMaxBytes {
		return nil, errors.New("生成图片超过 16 MiB 上限")
	}
	return decoded, nil
}

// cleanBase64 去 base64 载荷中的换行与空白（部分上游按行折叠 b64）。
func cleanBase64(value string) string {
	return strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(value)
}

// sniffImageMIME 按魔数嗅探图片 MIME（来源：internal/chat/generation_images.go
// imageMimeTypeFromBytes，退役复制；仅 webp/jpeg/png 三族）。
func sniffImageMIME(data []byte) string {
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

// mimeExtension 返回 MIME 对应的参考图文件名后缀（multipart filename 用）。
func mimeExtension(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}

// imageUrlDownloadClient 是生图 URL 回退下载客户端（带 SSRF 守卫拨号，重定向
// 不跟随；grok imgen.x.ai 临时链接出站。chat 面 chatImageURLDownloadClient
// 同口径的包内复刻；上游域名不在内网词表，演练场不接代理面）。
var imageUrlDownloadClient = newGuardedDownloadClient()

// newGuardedDownloadClient 构造 URL 回退下载客户端（守卫 DialContext + 不跟随
// 重定向 + 60s 超时，chat 面同口径）。
func newGuardedDownloadClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{DialContext: guardedDialContext, Proxy: nil},
		Timeout:   60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// downloadGeneratedImageBytes 下载上游返回的图片 URL（有界读取 16MiB）。
func downloadGeneratedImageBytes(ctx context.Context, rawURL string) ([]byte, error) {
	if _, err := validateFetchURL(rawURL); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("图片 url 无效: %w", err)
	}
	response, err := imageUrlDownloadClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("图片 url 下载失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("图片 url 下载失败（HTTP %d）", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, imageGeneratedMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("图片 url 下载失败: %w", err)
	}
	if len(data) > imageGeneratedMaxBytes {
		return nil, errors.New("生成图片超过 16 MiB 上限")
	}
	if len(data) == 0 {
		return nil, errors.New("图片 url 下载内容为空")
	}
	return data, nil
}
