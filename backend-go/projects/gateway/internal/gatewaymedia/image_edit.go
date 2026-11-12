// image_edit.go 承载同步图像编辑（POST /v1/images/edits）的请求形态解析与
// multipart 重建（2026-11-12 T216，契约 §2.9.1/§4.5/§9.3/§10.3）。对外双
// 形态：legacy multipart/form-data（image/image[]/image[N] 文件 + mask 文件 +
// 文本字段）与 OpenAI 现行 application/json（images:[{image_url|file_id}] +
// mask）。上传字节零存储：只在内存中转为 data URL，不落盘、不缓存、不写审计
// body、不打日志；gpt 的 multipart 保持字节级透传，仅删除 stream/
// partial_images 文本字段时按原 part 顺序与 header 重建（新 boundary）。
package gatewaymedia

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
)

const (
	// imageEditMaxImages 是单次编辑请求的参考图上限（契约 §2.9.1）。
	imageEditMaxImages = 16
	// imageEditMaxImageBytes 是单张图片/mask 的原始字节上限 10MB。
	imageEditMaxImageBytes = 10 << 20
	// imageEditMaxTextBytes 是单个 multipart 文本字段的上限 64KB。
	imageEditMaxTextBytes = 64 << 10
	// imageEditMaxParts 是 multipart 分段总数上限（part ≤64）。
	imageEditMaxParts = 64
)

// ImageMultipart 是 multipart 编辑请求的保真视图：按原始顺序保留每个
// part 的 header 与字节，供 gpt 字节级透传/重建时保持其余 part 不变。
type ImageMultipart struct {
	// Boundary 是客户端原始 boundary（重建时使用新 boundary）。
	Boundary string
	// Parts 是原始分段（text 与 file 均保留）。
	Parts []ImageMultipartPart
}

// ImageMultipartPart 是单个 multipart 分段。
type ImageMultipartPart struct {
	Name     string
	Filename string
	Header   textproto.MIMEHeader
	Data     []byte
	IsFile   bool
}

// ParseImageEditRequest 解析客户端图像编辑请求为归一层 ImageRequest：按
// Content-Type 分派 multipart / JSON，校验 L1 约束（model/prompt 必填、最多
// 16 张图、单张 ≤10MB、mask ≤10MB、文本字段 ≤64KB、part ≤64、multipart 未知
// 文件字段 400、非 gpt 的 JSON file_id 400、非图片 MIME 400）。path 已是
// 调用方判定的编辑路径（保留参数以便与 BuildImageUpstreamRequestForPath /
// ImageEchoForPath 同一入口形态）。
func ParseImageEditRequest(pathWithQuery, contentType string, rawBody []byte, providerCode string) (ImageRequest, error) {
	if !IsImageEditPath(pathWithQuery) {
		return ImageRequest{}, fmt.Errorf("图像编辑请求路径不受支持: %s", pathWithQuery)
	}
	request := ImageRequest{Operation: ImageOperationEdit, ProviderCode: providerCode}
	if imageEditIsMultipart(contentType) {
		if err := parseImageEditMultipart(&request, contentType, rawBody); err != nil {
			return ImageRequest{}, err
		}
	} else if err := parseImageEditJSON(&request, rawBody); err != nil {
		return ImageRequest{}, err
	}
	if err := validateImageEditRequiredFields(request.Body); err != nil {
		return ImageRequest{}, err
	}
	if !imageEditHasImage(request) {
		return ImageRequest{}, fmt.Errorf("图像编辑请求缺少 image（至少需要一张参考图）")
	}
	return request, nil
}

// imageEditHasImage 报告请求是否至少携带一张参考图：multipart 文件、
// JSON images[].image_url/file_id、或顶层 image 字符串；mask 不计入。gpt 的
// file_id 计入（其原生支持文件引用），非 gpt 的 file_id 已在 JSON 校验阶段
// 拒绝。JSON 顶层 image 只接受字符串——数组形态在校验阶段直接 400，避免
// 绕过 16 张/10MB/MIME 约束后被静默丢弃。
func imageEditHasImage(request ImageRequest) bool {
	if len(request.Files) > 0 {
		return true
	}
	if items, ok := request.Body["images"].([]any); ok {
		for _, rawItem := range items {
			item, isObject := rawItem.(map[string]any)
			if !isObject {
				continue
			}
			if value, ok := item["image_url"].(string); ok && strings.TrimSpace(value) != "" {
				return true
			}
			if value, ok := item["file_id"].(string); ok && strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	if image, ok := request.Body["image"].(string); ok {
		return strings.TrimSpace(image) != ""
	}
	return false
}

// imageEditIsMultipart 判定请求体形态（Content-Type 前缀，大小写不敏感）。
func imageEditIsMultipart(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data")
}

// imageEditTextualMIME 报告 MIME 是否属于可承载文本字段的类型；其余（含
// image/* 与 application/octet-stream）按文件段处理。
func imageEditTextualMIME(mimeType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(mimeType))
	if normalized == "" {
		return false
	}
	if strings.HasPrefix(normalized, "text/") {
		return true
	}
	switch normalized {
	case "application/json", "application/x-www-form-urlencoded":
		return true
	}
	return false
}

// imageEditFileFieldKind 把 multipart 文件字段名归类：image / image[] /
// image[N] 为参考图，mask 为遮罩；未知返回 ("", false)。
func imageEditFileFieldKind(name string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "mask" {
		return "mask", true
	}
	if normalized == "image" || normalized == "image[]" {
		return "image", true
	}
	if strings.HasPrefix(normalized, "image[") && strings.HasSuffix(normalized, "]") {
		index := strings.TrimSuffix(strings.TrimPrefix(normalized, "image["), "]")
		if index != "" && imageEditAllDigits(index) {
			return "image", true
		}
	}
	return "", false
}

// ImageEditFileFieldIsImage 报告 multipart 文件字段名是否为参考图（image /
// image[] / image[N]，大小写不敏感 + TrimSpace；mask 与未知字段不算）。
// gatewayresponse 的输入图计数字段归类与本文件编辑解析器共用同一判定，保证
// 计费口径与解析行为一致（image[N] 变体同样计入输入图）。
func ImageEditFileFieldIsImage(name string) bool {
	kind, _ := imageEditFileFieldKind(name)
	return kind == "image"
}

func imageEditAllDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseImageEditMultipart 解析 multipart 编辑请求：文本字段进 Body，参考图
// 进 Files，mask 进 Mask，并按原始顺序保留全部 part 供 gpt 重建。
func parseImageEditMultipart(request *ImageRequest, contentType string, rawBody []byte) error {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("图像编辑请求 multipart Content-Type 无效: %w", err)
	}
	if !strings.EqualFold(mediaType, "multipart/form-data") {
		return fmt.Errorf("图像编辑请求 Content-Type 必须是 multipart/form-data，收到 %s", mediaType)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return fmt.Errorf("图像编辑请求 multipart 缺少 boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(rawBody), boundary)
	parsed := &ImageMultipart{Boundary: boundary}
	request.Body = map[string]any{}
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			return fmt.Errorf("图像编辑请求 multipart 解析失败: %w", partErr)
		}
		name := strings.TrimSpace(part.FormName())
		filename := part.FileName()
		header := cloneImageMIMEHeader(part.Header)
		data, readErr := io.ReadAll(io.LimitReader(part, int64(imageEditMaxImageBytes)+1))
		_ = part.Close()
		if readErr != nil {
			return fmt.Errorf("图像编辑请求 multipart 分段读取失败: %w", readErr)
		}
		if len(data) > imageEditMaxImageBytes {
			return fmt.Errorf("图像编辑请求文件超过 10MB 上限（字段 %s）", name)
		}
		if len(parsed.Parts) >= imageEditMaxParts {
			return fmt.Errorf("图像编辑请求 multipart 分段超过 %d 个上限", imageEditMaxParts)
		}
		declaredType := strings.TrimSpace(header.Get("Content-Type"))
		isFile := filename != ""
		effectiveType := declaredType
		if !isFile {
			if effectiveType == "" {
				effectiveType = http.DetectContentType(data)
			}
			isFile = !imageEditTextualMIME(effectiveType)
		}
		if isFile {
			mimeType := declaredType
			if mimeType == "" {
				mimeType = http.DetectContentType(data)
			}
			mimeType = normalizeImageMIMEType(mimeType)
			if !strings.HasPrefix(mimeType, "image/") {
				return fmt.Errorf("图像编辑请求包含非图片文件（字段 %s，类型 %s）", name, mimeType)
			}
			kind, known := imageEditFileFieldKind(name)
			if !known {
				return fmt.Errorf("图像编辑请求包含未知文件字段: %s", name)
			}
			file := ImageFile{Field: name, Filename: filename, MIMEType: mimeType, Data: data}
			if kind == "mask" {
				if request.Mask != nil {
					return fmt.Errorf("图像编辑请求只能包含一个 mask 文件")
				}
				mask := file
				request.Mask = &mask
			} else {
				if len(request.Files) >= imageEditMaxImages {
					return fmt.Errorf("图像编辑请求最多支持 %d 张图片", imageEditMaxImages)
				}
				request.Files = append(request.Files, file)
			}
		} else {
			if len(data) > imageEditMaxTextBytes {
				return fmt.Errorf("图像编辑请求文本字段超过 64KB 上限（字段 %s）", name)
			}
			request.Body[name] = imageEditDecodeTextValue(name, string(data))
		}
		parsed.Parts = append(parsed.Parts, ImageMultipartPart{
			Name:     name,
			Filename: filename,
			Header:   header,
			Data:     data,
			IsFile:   isFile,
		})
	}
	request.Multipart = parsed
	return nil
}

// imageEditDecodeTextValue 把 multipart 文本字段解码为 JSON 等价值：n /
// output_compression / partial_images 解为数字、stream 解为布尔，其余保持
// 字符串（无法解析时保持原字符串，由上游裁决）。
func imageEditDecodeTextValue(name, value string) any {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "n", "output_compression", "partial_images":
		if number, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return number
		}
	case "stream":
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return value
}

// parseImageEditJSON 解析 JSON 编辑请求并校验图片列表约束。
func parseImageEditJSON(request *ImageRequest, rawBody []byte) error {
	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) == 0 {
		return fmt.Errorf("图像编辑请求缺少请求体")
	}
	var body map[string]any
	if err := json.Unmarshal(trimmed, &body); err != nil {
		return fmt.Errorf("图像编辑请求不是有效 JSON: %w", err)
	}
	if body == nil {
		return fmt.Errorf("图像编辑请求体必须是 JSON 对象")
	}
	request.Body = body
	return validateImageEditJSONImages(body, request.ProviderCode)
}

// validateImageEditJSONImages 校验 JSON 形态的 images/mask：张数、data URL
// MIME 与体积、非 gpt 的 file_id 拒绝。
func validateImageEditJSONImages(body map[string]any, providerCode string) error {
	if rawImages, ok := body["images"]; ok && rawImages != nil {
		items, isArray := rawImages.([]any)
		if !isArray {
			return fmt.Errorf("图像编辑请求 images 必须是数组")
		}
		if len(items) > imageEditMaxImages {
			return fmt.Errorf("图像编辑请求最多支持 %d 张图片", imageEditMaxImages)
		}
		for index, rawItem := range items {
			item, isObject := rawItem.(map[string]any)
			if !isObject {
				return fmt.Errorf("图像编辑请求 images[%d] 必须是对象", index)
			}
			fileID, hasFileID := item["file_id"].(string)
			if hasFileID && strings.TrimSpace(fileID) != "" {
				if !imageEditProviderSupportsFileID(providerCode) {
					return fmt.Errorf("图像编辑请求的 file_id 仅支持 gpt 账户，%s 无法跨厂商解析", normalizeProviderKey(providerCode))
				}
				continue
			}
			imageURL, hasImageURL := item["image_url"].(string)
			if !hasImageURL || strings.TrimSpace(imageURL) == "" {
				return fmt.Errorf("图像编辑请求 images[%d] 缺少 image_url 或 file_id", index)
			}
			if err := validateImageEditDataURL(imageURL); err != nil {
				return err
			}
		}
	}
	if rawMask, ok := body["mask"]; ok && rawMask != nil {
		if mask, isObject := rawMask.(map[string]any); isObject {
			if imageURL, hasImageURL := mask["image_url"].(string); hasImageURL {
				if err := validateImageEditDataURL(imageURL); err != nil {
					return err
				}
			}
		}
	}
	// JSON 编辑只接受 images[] 数组或顶层 image 字符串（契约 §2.9.1）。顶层
	// image 数组既不进 images[] 的 16 张/10MB/MIME 校验，也无法被参考图装配
	// 消费——直接 400，避免静默丢图后按文生图计费。
	if rawImage, ok := body["image"]; ok && rawImage != nil {
		if _, isArray := rawImage.([]any); isArray {
			return fmt.Errorf("图像编辑请求的 JSON 编辑请使用 images 数组（顶层 image 仅支持字符串）")
		}
		if image, isString := rawImage.(string); isString {
			if err := validateImageEditDataURL(image); err != nil {
				return err
			}
		}
	}
	return nil
}

func imageEditProviderSupportsFileID(providerCode string) bool {
	key := normalizeProviderKey(providerCode)
	return key == "gpt" || key == "openai"
}

// validateImageEditDataURL 校验 data URL 形态的图片来源：MIME 必须是
// image/*，base64 载荷解码后 ≤10MB；http(s)/oss 等普通 URL 不在本地校验。
func validateImageEditDataURL(value string) error {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return nil
	}
	header, payload, found := strings.Cut(trimmed[len("data:"):], ",")
	if !found {
		return fmt.Errorf("图像编辑请求的 data URL 格式无效")
	}
	segments := strings.Split(header, ";")
	mimeType := normalizeImageMIMEType(segments[0])
	if !strings.HasPrefix(mimeType, "image/") {
		return fmt.Errorf("图像编辑请求包含非图片 MIME: %s", mimeType)
	}
	isBase64 := false
	for _, segment := range segments[1:] {
		if strings.EqualFold(strings.TrimSpace(segment), "base64") {
			isBase64 = true
		}
	}
	if !isBase64 {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return fmt.Errorf("图像编辑请求的 data URL base64 无效: %w", err)
	}
	if len(decoded) > imageEditMaxImageBytes {
		return fmt.Errorf("图像编辑请求单张图片超过 10MB 上限")
	}
	return nil
}

// normalizeImageMIMEType 去掉参数段并小写化（"image/png; charset=x" →
// "image/png"）。
func normalizeImageMIMEType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if index := strings.IndexByte(normalized, ';'); index >= 0 {
		normalized = strings.TrimSpace(normalized[:index])
	}
	return normalized
}

// validateImageEditRequiredFields 校验 L1 必填字段（model/prompt）。
func validateImageEditRequiredFields(body map[string]any) error {
	if body == nil {
		return fmt.Errorf("图像编辑请求缺少请求体")
	}
	model, _ := body["model"].(string)
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("图像编辑请求缺少 model")
	}
	prompt, _ := body["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("图像编辑请求缺少 prompt")
	}
	return nil
}

// imageEditSourceURLs 归一编辑请求的参考图来源：multipart 文件优先转为
// data URL；否则取 JSON images[].image_url，再回落顶层 image 字符串。非
// gpt 厂商的 file_id 已在 Parse 阶段拒绝，这里跳过无法解析的项。
func imageEditSourceURLs(request ImageRequest) []string {
	urls := make([]string, 0, len(request.Files)+1)
	for _, file := range request.Files {
		urls = append(urls, imageFileDataURL(file))
	}
	if len(urls) > 0 {
		return urls
	}
	if items, ok := request.Body["images"].([]any); ok {
		for _, rawItem := range items {
			item, isObject := rawItem.(map[string]any)
			if !isObject {
				continue
			}
			imageURL, _ := item["image_url"].(string)
			if strings.TrimSpace(imageURL) != "" {
				urls = append(urls, strings.TrimSpace(imageURL))
			}
		}
	}
	if len(urls) > 0 {
		return urls
	}
	if image, ok := request.Body["image"].(string); ok && strings.TrimSpace(image) != "" {
		urls = append(urls, strings.TrimSpace(image))
	}
	return urls
}

// imageFileDataURL 把上传文件字节转为 data URL（MIME 已校验为 image/*）。
func imageFileDataURL(file ImageFile) string {
	mimeType := normalizeImageMIMEType(file.MIMEType)
	if mimeType == "" {
		mimeType = http.DetectContentType(file.Data)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(file.Data)
}

// imageEditAttachURLObjects 按 xAI 编辑形态写图片字段：单图
// image:{url}、多图 images:[{url}]（互斥）。
func imageEditAttachURLObjects(target map[string]any, urls []string) {
	switch len(urls) {
	case 0:
		return
	case 1:
		target["image"] = map[string]any{"url": urls[0]}
	default:
		items := make([]any, 0, len(urls))
		for _, url := range urls {
			items = append(items, map[string]any{"url": url})
		}
		target["images"] = items
	}
}

// imageEditAttachImageStrings 按火山 Ark 编辑形态写 image：单图字符串、多图
// 字符串数组（URL 或 data URL）。
func imageEditAttachImageStrings(target map[string]any, urls []string) {
	switch len(urls) {
	case 0:
		return
	case 1:
		target["image"] = urls[0]
	default:
		items := make([]any, 0, len(urls))
		for _, url := range urls {
			items = append(items, url)
		}
		target["image"] = items
	}
}

// imageEditStructuralKeys 是编辑请求的图片装配结构键：由公共字段装配图片或
// 是 L3 通道本体，不参与“未处理顶层键”回显判定（mask 单列，因为只有非 gpt
// 厂商会忽略它）。
var imageEditStructuralKeys = []string{"image", "images", "mask", ProviderOptionsKey}

// imageEditTakeProviderOptions 解析编辑面的 L3 通道：multipart 不承载
// provider_options（契约 §2.9.1「不支持时按未知字段忽略」）——从 body 副本
// 删除并由 params_ignored 回显，不报 400；JSON 形态沿用
// takeImageProviderOptions 的对象校验。
func imageEditTakeProviderOptions(request ImageRequest, body map[string]any) (map[string]map[string]any, bool, error) {
	if request.Multipart != nil {
		delete(body, ProviderOptionsKey)
		return nil, false, nil
	}
	return takeImageProviderOptions(body)
}

// imageEditSanitizeProviderOptionImageKeys 从命中 provider 的 provider_options
// 子对象删除图片装配键：编辑面的参考图只由公共字段（multipart 文件 /
// images[] / 顶层 image 字符串）决定，L3 不得注入图片，也不得打破 xAI 的
// image/images 互斥。返回实际被剔除的键名（字典序），供 params_ignored 回显，
// 避免 L3 图片键被静默丢弃。
func imageEditSanitizeProviderOptionImageKeys(opts map[string]map[string]any, providerCode string) []string {
	for key, sub := range opts {
		if !providerOptionKeyMatches(providerCode, key) {
			continue
		}
		removed := make([]string, 0, 2)
		for _, name := range []string{"image", "images"} {
			if _, ok := sub[name]; ok {
				delete(sub, name)
				removed = append(removed, name)
			}
		}
		sort.Strings(removed)
		return removed
	}
	return nil
}

// imageEditIgnoredTopLevel 汇总编辑请求的 params_ignored（字典序去重，只回显
// 键名）：
//   - request.Body 中既不属于保留集、也不是图片装配结构键的顶层键——覆盖
//     固定 forbidden 与厂商未处理的未知键（如 xai 顶层 aspect_ratio、
//     volcengine sequential_image_generation/tools、qwen seed）；
//   - merged（provider_options 合并结果）中出现的 forbidden 键——L3 注入的
//     forbidden 参数同样必须删除并回显，不能借扩展通道绕过 ignore；
//   - mask 文件/对象（仅非 gpt adapter 调用本函数）；
//   - multipart 的 provider_options 文本字段（multipart 不承载 L3）。
func imageEditIgnoredTopLevel(request ImageRequest, preserved, forbidden []string, merged map[string]any) []string {
	preservedSet := make(map[string]struct{}, len(preserved))
	for _, key := range preserved {
		preservedSet[key] = struct{}{}
	}
	structuralSet := make(map[string]struct{}, len(imageEditStructuralKeys))
	for _, key := range imageEditStructuralKeys {
		structuralSet[key] = struct{}{}
	}
	seen := make(map[string]struct{}, len(forbidden)+4)
	ignored := make([]string, 0, len(forbidden)+4)
	add := func(key string) {
		// 空键（含纯空白）不参与回显：逗号拼接后会产生前导/重复分隔符。
		if strings.TrimSpace(key) == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		ignored = append(ignored, key)
	}
	for key := range request.Body {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, structural := structuralSet[key]; structural {
			continue
		}
		if _, keep := preservedSet[key]; keep {
			continue
		}
		add(key)
	}
	for _, key := range forbidden {
		if _, injected := merged[key]; injected {
			add(key)
		}
	}
	if request.Mask != nil || request.Body["mask"] != nil {
		add("mask")
	}
	if request.Multipart != nil {
		if _, ok := request.Body[ProviderOptionsKey]; ok {
			add(ProviderOptionsKey)
		}
	}
	sort.Strings(ignored)
	return ignored
}

// rebuildImageMultipart 按原始 part 顺序与 header 重建 multipart（新
// boundary），跳过 dropFields 中的文本字段；文件段原样保留。
func rebuildImageMultipart(source *ImageMultipart, dropFields map[string]struct{}) ([]byte, string, error) {
	if source == nil {
		return nil, "", fmt.Errorf("图像编辑请求缺少 multipart 原始分段")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, part := range source.Parts {
		if !part.IsFile {
			if _, drop := dropFields[strings.ToLower(strings.TrimSpace(part.Name))]; drop {
				continue
			}
		}
		writerPart, err := writer.CreatePart(cloneImageMIMEHeader(part.Header))
		if err != nil {
			return nil, "", fmt.Errorf("图像编辑请求 multipart 重建失败: %w", err)
		}
		if _, err := writerPart.Write(part.Data); err != nil {
			return nil, "", fmt.Errorf("图像编辑请求 multipart 重建失败: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("图像编辑请求 multipart 重建失败: %w", err)
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func cloneImageMIMEHeader(source textproto.MIMEHeader) textproto.MIMEHeader {
	cloned := make(textproto.MIMEHeader, len(source))
	for name, values := range source {
		copied := make([]string, len(values))
		copy(copied, values)
		cloned[name] = copied
	}
	return cloned
}
