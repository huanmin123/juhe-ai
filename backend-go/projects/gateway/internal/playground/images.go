package playground

// POST /images —— 同步生图（设计 §3.4）：固定派发 /v1/images/generations（文生
// 图）或 /v1/images/edits（图生图，multipart），产物字节落服务端暂存（§3.8，
// 暂存是展示缓存非资产库，同批覆盖式替换），响应回 {images:[{id,url}],model}
// ——不再内联 base64 dataUrl，前端 <img src=url> 展示、下载走 url。请求构造与
// 响应解析是 internal/chat/generation_images.go GenerateChatImage 的同构精简
// 版（该包有并行未提交改动且将整体退役，不 import；产物上限 n≤4、gpt-image-2
// 需 response_format=b64_json 的家族差异保留）。grok 只回临时 URL 时按既定
// 回退下载字节（下载客户端带 SSRF 守卫，沿 chat 包同口径）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// 生图限额（设计 §3.4；payload 上限沿 chat 面 chatAssetGeneratedMaxBytes 16MiB）。
const (
	imagesMaxCount         = 4
	imageGeneratedMaxBytes = 16 * 1024 * 1024
	imagesPromptMaxBytes   = 32 * 1024        // 提示词上限（防滥用；/v1 链另有参数校验）
	inputImageMaxBytes     = 16 * 1024 * 1024 // 图生图参考图字节上限
)

// imagesRequest 是 POST /images 请求体（设计 §3.4）。
type imagesRequest struct {
	AccountID        string `json:"accountId"`
	ModelID          string `json:"modelId"`
	Prompt           string `json:"prompt"`
	Size             string `json:"size"`
	InputImageBase64 string `json:"inputImageBase64"`
}

// imageResultEntry 是单张产物（暂存 id + 取流 url，§3.4 契约形状）。
type imageResultEntry struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// imagesResponse 是 POST /images 响应（设计 §3.4）。
type imagesResponse struct {
	Images []imageResultEntry `json:"images"`
	Model  string             `json:"model"`
}

// imagesHandler 服务 POST /images（同步等待，短超时由派发链自身与 ctx 承接）。
func (rt *playgroundRoutes) imagesHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	// 产物落盘是本端点响应契约的一部分（§3.4 响应即 {id,url}），暂存面未接线
	// 时显式降级，不派发不静默改行为。
	if !rt.artifacts.available() {
		writeHTTPError(w, artifactsUnavailable())
		return
	}
	body, err := readJSONBody(r, inputImageMaxBytes*2) // 参考图 base64 膨胀后留倍数余量
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req imagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest("请求体必须是合法 JSON"))
		return
	}
	_, options, candidateErr := rt.resolveAccountModels(r.Context(), scope, strings.TrimSpace(req.AccountID))
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	// options == nil = 账户不存在或不在当前用户可见范围：与 chatstream 同口径
	// 404 playground_account_not_found（区别于模型越界的 400）。
	if options == nil {
		writeHTTPError(w, accountNotFound())
		return
	}
	if modelOptionOf(options, strings.TrimSpace(req.ModelID), KindImage) == nil {
		writeHTTPError(w, modelNotAvailable("模型不在该账户的演练场生图候选内"))
		return
	}
	payload, runErr := rt.runImages(r.Context(), scope, &req)
	if runErr != nil {
		writeHTTPError(w, runErr)
		return
	}
	writeOKPayload(w, payload)
}

// runImages 执行生图派发、产物落盘（§3.8 覆盖式替换）与响应归一（测试断言请
// 求构造与暂存行为经 Mock DispatchPort 回放 + 真实落盘验证）。
func (rt *playgroundRoutes) runImages(ctx context.Context, scope Scope, req *imagesRequest) (*imagesResponse, *HTTPError) {
	model := strings.TrimSpace(req.ModelID)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, invalidRequest("prompt 不能为空")
	}
	if len(prompt) > imagesPromptMaxBytes {
		return nil, invalidRequest("prompt 超过字节上限")
	}
	size := strings.TrimSpace(req.Size)
	headers := map[string]string{
		"accept":         "application/json",
		purposeHeaderKey: playgroundPurposeImages,
	}
	// 单次恒 1 张（2026-10-31 用户裁决：张数功能移除——上游 images 接口对 n
	// 参数恒回 1 张，多张由用户自行多次生成）。
	var editsBody []byte
	if reference := strings.TrimSpace(req.InputImageBase64); reference != "" {
		// 图生图走 /v1/images/edits：multipart（part 必须带真实 MIME——中转
		// 上游按 part Content-Type 解析图，chat 面 BUG-0232 取证同口径）。
		data, mime, httpErr := decodeInputImage(reference)
		if httpErr != nil {
			return nil, httpErr
		}
		form := &bytes.Buffer{}
		writer := multipart.NewWriter(form)
		_ = writer.WriteField("model", model)
		_ = writer.WriteField("prompt", prompt)
		if size != "" {
			_ = writer.WriteField("size", size)
		}
		_ = writer.WriteField("response_format", "b64_json")
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="reference.%s"`, mimeExtension(mime)))
		header.Set("Content-Type", mime)
		part, partErr := writer.CreatePart(header)
		if partErr != nil {
			return nil, invalidRequest("参考图 multipart 构造失败")
		}
		if _, partErr := part.Write(data); partErr != nil {
			return nil, invalidRequest("参考图 multipart 写入失败")
		}
		if err := writer.Close(); err != nil {
			return nil, invalidRequest("参考图 multipart 收尾失败")
		}
		editsBody = form.Bytes()
		headers["content-type"] = writer.FormDataContentType()
	} else {
		headers["content-type"] = "application/json"
	}
	buildDispatch := func() DispatchRequest {
		if editsBody != nil {
			return DispatchRequest{
				AccountID: strings.TrimSpace(req.AccountID), OwnerID: scope.ViewerID,
				Path: "/v1/images/edits", Method: http.MethodPost, Headers: headers, Body: editsBody,
			}
		}
		payloadValues := map[string]any{"model": model, "prompt": prompt}
		if size != "" {
			payloadValues["size"] = size
		}
		// b64_json 优先：演练场把产物字节落服务端暂存（§3.8）；grok 等上游
		// 只回临时 URL 时由下方回退下载字节。
		payloadValues["response_format"] = "b64_json"
		payload, _ := json.Marshal(payloadValues)
		return DispatchRequest{
			AccountID: strings.TrimSpace(req.AccountID), OwnerID: scope.ViewerID,
			Path: "/v1/images/generations", Method: http.MethodPost, Headers: headers, Body: payload,
		}
	}
	response, err := rt.deps.Dispatch.Dispatch(ctx, buildDispatch())
	if err != nil {
		return nil, dispatchFailed("生图派发失败: " + err.Error())
	}
	if response == nil {
		return nil, dispatchFailed("生图派发失败")
	}
	respBody, readErr := io.ReadAll(io.LimitReader(response.Body, imageGeneratedMaxBytes+1024))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, dispatchFailed("生图响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, dispatchFailed(upstreamDispatchMessage("生图", response.Status, respBody))
	}
	decoded, parseErr := decodedImagesFromPayload(ctx, respBody, 1)
	if parseErr != nil {
		return nil, dispatchFailed("生图结果归一失败: " + parseErr.Error())
	}
	// 产物落暂存（§3.8）：同 kind 覆盖式替换——删该用户旧图后写本批；落盘失败
	// 显式 500（响应契约是 {id,url}，没有暂存就没有可交付形状）。
	entries, storeErr := rt.artifacts.replaceImages(scope.ViewerID, decoded)
	if storeErr != nil {
		return nil, artifactsError(storeErr)
	}
	return &imagesResponse{Images: entries, Model: model}, nil
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
