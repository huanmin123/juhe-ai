// imageadapter.go 是同步图像请求的厂商适配层（2026-11-11 图像生成统一协议
// 同步批）。对外统一 OpenAI Images JSON 形态；四家均不支持 stream（2026-11-12
// 用户裁决，stream/partial_images 忽略并回显）；gpt 原生 JSON 直通，
// xai/volcengine 走各自 OpenAI 兼容图像端点，qwen 走 DashScope 原生
// multimodal-generation 并由 qwenImageAdapter 转换响应。图像不落 media_jobs
// （异步任务面本批不做）。
//
// 响应宽容（用户 2026-11-11 裁决）：不校验响应 model/厂商/mime_type，只要
// data[] 有 url/b64_json 即成功；响应 model 只进 usage.upstream_response_model。
package gatewaymedia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	// ImageProviderHeader 是同步图像响应回显的供应商标识（不写 body，避免
	// 缓冲/改写 gpt 的 b64_json 大响应）。
	ImageProviderHeader = "X-Juhe-Image-Provider"
	// ImageParamsAppliedHeader 是本次实际生效/换算的参数名（逗号分隔）。
	ImageParamsAppliedHeader = "X-Juhe-Image-Params-Applied"
	// ImageParamsIgnoredHeader 是本次被忽略的公共参数名（逗号分隔）。
	ImageParamsIgnoredHeader = "X-Juhe-Image-Params-Ignored"
	// ImageProviderOptionsAppliedHeader 是命中的 provider_options 子对象键名
	// 摘要（逗号分隔，不含值）。
	ImageProviderOptionsAppliedHeader = "X-Juhe-Image-Provider-Options-Applied"

	qwenImageUpstreamPath = "/api/v1/services/aigc/multimodal-generation/generation"

	// imageMetadataKey 是同步图像公开扩展通道的键名（契约 §2.9/§2.9.1）；
	// 链上投影为该厂商 provider_options 子对象后不转发上游。
	imageMetadataKey = "metadata"
)

// ImageOperation 是同步图像操作（生成/编辑），供同一 ImageIR 与 adapter 复用
// （设计 §4.4：编辑面与生成面共用请求归一、计费与回显）。
type ImageOperation string

const (
	// ImageOperationGenerate 是 POST /v1/images/generations（文生图/图生图）。
	ImageOperationGenerate ImageOperation = "generate"
	// ImageOperationEdit 是 POST /v1/images/edits（图像编辑）。
	ImageOperationEdit ImageOperation = "edit"
)

// ImageFile 是 multipart 编辑请求中的一个上传文件（参考图或 mask）。字节只
// 在内存中转为 data URL，零存储（契约 §2.9.1 规则 3）。
type ImageFile struct {
	Field    string
	Filename string
	MIMEType string
	Data     []byte
}

// ImageRequest 是一次同步图像请求的归一层输入。
type ImageRequest struct {
	// Body 是客户端 OpenAI Images JSON（含 provider_options）；multipart 时
	// 是解码后的文本字段集合。
	Body map[string]any
	// ProviderCode 是账户 vendor_code（gpt/xai/volcengine/qwen）。
	ProviderCode string
	// Operation 标识生成或编辑（空值等价生成，兼容既有调用方）。
	Operation ImageOperation
	// Files 是 multipart 编辑请求的参考图（JSON 形态为空，图片在 Body）。
	Files []ImageFile
	// Mask 是 multipart 编辑请求的 mask 文件（仅 gpt 原生支持）。
	Mask *ImageFile
	// Multipart 保留 multipart 编辑请求的原始分段（仅编辑面非 nil），供 gpt
	// 字节级透传/重建时保持其余 part 与 header 不变。
	Multipart *ImageMultipart
	// Metadata 是公开请求 metadata 的原值（响应转换回显用；multipart 编辑
	// 不承载 metadata，恒为空）。
	Metadata map[string]any
}

// ImageBuildResult 是 adapter 构造上游请求的结果。Body 为 nil 表示不修改
// 客户端字节（gpt 无 provider_options/无被忽略字段时的纯透传）。
type ImageBuildResult struct {
	Body []byte
	// ContentType 是 Body 非 nil 时的上游 Content-Type；空值由调用方回落
	// application/json（gpt multipart 重建时为新 boundary 的 multipart）。
	ContentType            string
	Provider               string
	ParamsApplied          []string
	ParamsIgnored          []string
	ProviderOptionsApplied []string
}

// ImageAdapter 是每厂商一个的同步图像出站 adapter。
type ImageAdapter interface {
	Provider() string
	// Build 把 OpenAI Images 生成 body 转为上游 body；不修改入参。
	Build(input ImageRequest) (ImageBuildResult, error)
	// BuildEdit 把 OpenAI Images 编辑请求（multipart 或 JSON）转为上游 body；
	// 返回 nil Body 表示沿用客户端原始字节（gpt multipart 纯透传）。
	BuildEdit(input ImageRequest) (ImageBuildResult, error)
	// UpstreamPath 返回上游操作路径；返回入参原路径表示沿用通用 OpenAI 直拼。
	UpstreamPath(clientPath string) string
	// RequiresResponseTransform 报告是否需要网关把原生响应转为 OpenAI Images。
	RequiresResponseTransform() bool
	// TransformResponse 把原生 JSON 转为 OpenAI Images JSON；passthrough adapter
	// 原样返回。
	TransformResponse(input ImageRequest, body []byte) ([]byte, error)
}

var imageAdapters = map[string]ImageAdapter{
	"openai":     openAIImageAdapter{},
	"xai":        xaiImageAdapter{},
	"volcengine": volcengineImageAdapter{},
	"qwen":       qwenImageAdapter{},
}

// ImageAdapterForProvider 按账户 vendor_code 查 adapter；未注册返回 nil。
func ImageAdapterForProvider(providerCode string) ImageAdapter {
	key := normalizeProviderKey(providerCode)
	if key == "gpt" {
		key = "openai"
	}
	return imageAdapters[key]
}

// IsImageGenerationPath 判定客户端路径是否为同步图像生成端点
// （/v1/images/generations 或裸根 /images/generations）。
func IsImageGenerationPath(pathWithQuery string) bool {
	return normalizeImageOperationPath(pathWithQuery) == "/images/generations"
}

// IsImageEditPath 判定客户端路径是否为同步图像编辑端点
// （/v1/images/edits 或裸根 /images/edits）。
func IsImageEditPath(pathWithQuery string) bool {
	return normalizeImageOperationPath(pathWithQuery) == "/images/edits"
}

// IsImagePath 判定客户端路径是否属于同步图像面（生成或编辑）。响应转换门与
// 回显门使用本判定；生成判定语义（IsImageGenerationPath）保持不变。
func IsImagePath(pathWithQuery string) bool {
	normalized := normalizeImageOperationPath(pathWithQuery)
	return normalized == "/images/generations" || normalized == "/images/edits"
}

// normalizeImageOperationPath 归一为去 /v1 前缀、去尾斜杠的小写路径。
func normalizeImageOperationPath(pathWithQuery string) string {
	path, _ := splitImagePathAndQuery(pathWithQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	path = strings.TrimPrefix(path, "/v1")
	path = strings.TrimSuffix(path, "/")
	return path
}

// ParseImageRequest 把客户端 body 复制为 ImageRequest（不修改 body）。
func ParseImageRequest(body map[string]any, providerCode string) ImageRequest {
	return ImageRequest{Body: deepCopyMap(body), ProviderCode: providerCode, Operation: ImageOperationGenerate}
}

// normalizeImageMetadataBody 把公开 metadata 扩展通道投影为内部
// provider_options.<adapter.Provider()> 子对象，并删除 metadata（不转发
// 上游）。JSON 对象以外的请求体保持原字节（沿既有“非对象交给上游”语义，
// 不新增形态门）；公开 provider_options 已退役，出现即返回校验错误；metadata
// 存在但不是对象同样返回校验错误。无 metadata 时返回原字节（零改写）。
func normalizeImageMetadataBody(providerCode string, body []byte) ([]byte, map[string]any, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body, nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(trimmed, &parsed); err != nil || parsed == nil {
		return body, nil, nil
	}
	if _, present := parsed[ProviderOptionsKey]; present {
		return nil, nil, fmt.Errorf("provider_options 已退役，扩展参数请改用 metadata 对象")
	}
	raw, present := parsed[imageMetadataKey]
	if !present {
		return body, nil, nil
	}
	// 显式 null 与视频面同口径：按未携带处理，并从请求体删除，避免把网关
	// 扩展键透传给上游。
	if raw == nil {
		delete(parsed, imageMetadataKey)
		encoded, err := json.Marshal(parsed)
		if err != nil {
			return nil, nil, fmt.Errorf("图像请求体序列化失败: %w", err)
		}
		return encoded, nil, nil
	}
	metadata, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("metadata 必须是 JSON 对象")
	}
	delete(parsed, imageMetadataKey)
	projectionKey := normalizeProviderKey(providerCode)
	if adapter := ImageAdapterForProvider(providerCode); adapter != nil {
		projectionKey = adapter.Provider()
	}
	if projectionKey != "" {
		parsed[ProviderOptionsKey] = map[string]any{projectionKey: metadata}
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return nil, nil, fmt.Errorf("图像请求体序列化失败: %w", err)
	}
	return encoded, metadata, nil
}

// ImageRequestMetadataOf 取出请求体 metadata 对象（响应侧回显用）；缺失或
// 非对象返回 nil（请求侧已由 normalizeImageMetadataBody 拒绝非对象形态）。
func ImageRequestMetadataOf(body map[string]any) map[string]any {
	if len(body) == 0 {
		return nil
	}
	metadata, _ := body[imageMetadataKey].(map[string]any)
	return metadata
}

// BuildImageUpstreamRequestForPath 是链上请求侧统一入口（生成 + 编辑）：
// 编辑面按 Content-Type 解析 multipart/JSON 后交给 adapter.BuildEdit；生成面
// 维持既有字节语义（Body 为 nil 时纯透传）。
// JSON 形态先经 normalizeImageMetadataBody 把公开 metadata 投影为内部
// provider_options（公开 provider_options 出现即校验错误）；multipart 不承载
// metadata，保持原字节解析。返回的 ContentType 仅在 Body 非 nil 时由调用方
// 写入上游头（空值回落 application/json）。
func BuildImageUpstreamRequestForPath(providerCode, pathWithQuery, contentType string, rawBody []byte) (ImageBuildResult, error) {
	if IsImageEditPath(pathWithQuery) {
		normalizedBody := rawBody
		if !imageEditIsMultipart(contentType) {
			var err error
			normalizedBody, _, err = normalizeImageMetadataBody(providerCode, rawBody)
			if err != nil {
				return ImageBuildResult{}, err
			}
		}
		request, err := ParseImageEditRequest(pathWithQuery, contentType, normalizedBody, providerCode)
		if err != nil {
			return ImageBuildResult{}, err
		}
		adapter := ImageAdapterForProvider(providerCode)
		if adapter == nil {
			return ImageBuildResult{}, nil
		}
		return adapter.BuildEdit(request)
	}
	if !IsImageGenerationPath(pathWithQuery) {
		return ImageBuildResult{}, nil
	}
	normalizedBody, _, err := normalizeImageMetadataBody(providerCode, rawBody)
	if err != nil {
		return ImageBuildResult{}, err
	}
	// 形态门：空/非法 JSON 交上游，adapter 未注册保持原样；直接调用
	// adapter.Build 以保留回显事实（params/options）。
	trimmed := bytes.TrimSpace(normalizedBody)
	if len(trimmed) == 0 {
		return ImageBuildResult{}, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return ImageBuildResult{}, nil
	}
	adapter := ImageAdapterForProvider(providerCode)
	if adapter == nil {
		return ImageBuildResult{}, nil
	}
	result, err := adapter.Build(ParseImageRequest(parsed, providerCode))
	if err != nil {
		return ImageBuildResult{}, err
	}
	// adapter 返回 nil 表示沿用客户端原始字节；但 metadata 归一（如显式
	// null 删除）可能已改写 body，此时必须用归一后的字节，否则删除不生效。
	if result.Body == nil && !bytes.Equal(normalizedBody, rawBody) {
		result.Body = normalizedBody
	}
	if result.Body != nil {
		result.ContentType = "application/json"
	}
	return result, nil
}

// ImageBaseURLForProvider 返回图像出站使用的 base URL。qwen 是唯一特例：
// 官方 base 为 origin；若账户误配 /compatible-mode/v1、/api/v1 或 /v1
// 版本根，出站前剥离，再拼原生全路径。
func ImageBaseURLForProvider(providerCode, baseURL string) string {
	if normalizeProviderKey(providerCode) != "qwen" {
		return baseURL
	}
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/compatible-mode/v1", "/api/v1", "/v1"} {
		if strings.HasSuffix(trimmed, suffix) {
			return strings.TrimSuffix(trimmed, suffix)
		}
	}
	return trimmed
}

// ImageEcho 是同步图像响应头的回显事实。
type ImageEcho struct {
	Provider               string
	ParamsApplied          []string
	ParamsIgnored          []string
	ProviderOptionsApplied []string
	// Metadata 是请求 metadata 原值（qwen 转换响应写 body 的来源；multipart
	// 编辑为空）。
	Metadata map[string]any
}

// ImageEchoForPath 是响应侧统一回显入口（生成 + 编辑，设计 §4.4）：生成面
// 与 JSON 编辑先经 normalizeImageMetadataBody 投影公开 metadata（公开
// provider_options 已在请求侧拒绝）；编辑面按 Content-Type 重新解析原始 body
// （multipart 用同一解析器重建回显事实，不缓存、不落盘），再跑同一 adapter
// 得到 params_applied/params_ignored/provider_options_applied。multipart 不
// 承载 metadata，回显 Metadata 为空。解析失败时只回显 provider（请求侧已按
// invalid_image_request 拒绝，不会走到成功响应）。
func ImageEchoForPath(providerCode, pathWithQuery, contentType string, rawBody []byte) ImageEcho {
	echo := ImageEcho{Provider: canonicalImageProvider(providerCode)}
	adapter := ImageAdapterForProvider(providerCode)
	if adapter == nil || !IsImagePath(pathWithQuery) {
		return echo
	}
	normalizedBody := rawBody
	var metadata map[string]any
	if !imageEditIsMultipart(contentType) {
		normalized, extracted, err := normalizeImageMetadataBody(providerCode, rawBody)
		if err != nil {
			return echo
		}
		normalizedBody, metadata = normalized, extracted
	}
	var result ImageBuildResult
	if IsImageEditPath(pathWithQuery) {
		request, err := ParseImageEditRequest(pathWithQuery, contentType, normalizedBody, providerCode)
		if err != nil {
			return echo
		}
		result, err = adapter.BuildEdit(request)
		if err != nil {
			return echo
		}
	} else {
		body := imageJSONObjectBody(normalizedBody)
		if len(body) == 0 {
			return echo
		}
		var err error
		result, err = adapter.Build(ParseImageRequest(body, providerCode))
		if err != nil {
			return echo
		}
	}
	echo.ParamsApplied = result.ParamsApplied
	echo.ParamsIgnored = result.ParamsIgnored
	echo.ProviderOptionsApplied = result.ProviderOptionsApplied
	echo.Metadata = metadata
	return echo
}

// imageJSONObjectBody 解析原始请求体 JSON 对象；空/非对象/非法 JSON 返回
// nil（回显按“无参数事实”处理，不新增第二套形态门）。
func imageJSONObjectBody(rawBody []byte) map[string]any {
	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) == 0 {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(trimmed, &body); err != nil {
		return nil
	}
	return body
}

// ImageEchoHeaders 把回显事实转为响应头（空列表不设置）。
func ImageEchoHeaders(echo ImageEcho) map[string]string {
	headers := map[string]string{}
	if echo.Provider != "" {
		headers[ImageProviderHeader] = echo.Provider
	}
	if value := strings.Join(echo.ParamsApplied, ","); value != "" {
		headers[ImageParamsAppliedHeader] = value
	}
	if value := strings.Join(echo.ParamsIgnored, ","); value != "" {
		headers[ImageParamsIgnoredHeader] = value
	}
	if value := strings.Join(echo.ProviderOptionsApplied, ","); value != "" {
		headers[ImageProviderOptionsAppliedHeader] = value
	}
	return headers
}

func canonicalImageProvider(providerCode string) string {
	key := normalizeProviderKey(providerCode)
	if key == "" {
		return ""
	}
	return key
}

// openAIImageAdapter 是 gpt 同步 JSON 直通：图像统一契约不提供 stream，
// `stream`/`partial_images` 一律忽略并回显 params_ignored；无这些参数且无
// provider_options 时保持原始字节透传（含 b64_json 大响应）。
type openAIImageAdapter struct{}

func (openAIImageAdapter) Provider() string { return "openai" }

func (openAIImageAdapter) Build(input ImageRequest) (ImageBuildResult, error) {
	body := deepCopyMap(input.Body)
	opts, present, err := takeImageProviderOptions(body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	applied, err := applyImageProviderOptions(body, "gpt", opts)
	if err != nil {
		return ImageBuildResult{}, err
	}
	// 2026-11-12 用户裁决：图像统一只返回 JSON，不支持 stream；gpt 也不再
	// 直通 SSE，与 xai/volcengine/qwen 同样忽略并回显。
	ignored := dropImageParams(body, "stream", "partial_images")
	applied = excludeStrings(applied, ignored)
	result := ImageBuildResult{
		Provider:               "openai",
		ParamsIgnored:          ignored,
		ProviderOptionsApplied: applied,
	}
	if !present && len(ignored) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ImageBuildResult{}, fmt.Errorf("图像请求体序列化失败: %w", err)
	}
	result.Body = encoded
	return result, nil
}

func (openAIImageAdapter) UpstreamPath(clientPath string) string { return clientPath }
func (openAIImageAdapter) RequiresResponseTransform() bool       { return false }
func (openAIImageAdapter) TransformResponse(_ ImageRequest, body []byte) ([]byte, error) {
	return body, nil
}

// BuildEdit 处理 gpt 编辑请求：JSON 形态复用生成面 Build（删 stream/
// partial_images、合并 provider_options）；multipart 形态保持字节级透传，仅
// 当文本字段含 stream/partial_images 时按原 part 顺序与 header 重建（新
// boundary），其余 part 与文件原样保留。mask 原生支持，不删除不回显。
func (openAIImageAdapter) BuildEdit(input ImageRequest) (ImageBuildResult, error) {
	if input.Multipart == nil {
		return openAIImageAdapter{}.Build(input)
	}
	result := ImageBuildResult{Provider: "openai"}
	drop := make(map[string]struct{}, 2)
	seen := make(map[string]struct{}, 2)
	for _, part := range input.Multipart.Parts {
		if part.IsFile {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part.Name))
		if name != "stream" && name != "partial_images" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		drop[name] = struct{}{}
	}
	if len(drop) == 0 {
		return result, nil
	}
	ignored := make([]string, 0, len(drop))
	for name := range drop {
		ignored = append(ignored, name)
	}
	sort.Strings(ignored)
	body, contentType, err := rebuildImageMultipart(input.Multipart, drop)
	if err != nil {
		return ImageBuildResult{}, err
	}
	result.Body = body
	result.ContentType = contentType
	result.ParamsIgnored = ignored
	return result, nil
}

// xaiImageAdapter 走 xAI 的 OpenAI 兼容 Images 面：只保留 xAI 已文档化/实测
// 字段，未文档化的 OpenAI 专属参数忽略并回显；provider_options.xai 深合并。
type xaiImageAdapter struct{}

func (xaiImageAdapter) Provider() string { return "xai" }

func (xaiImageAdapter) Build(input ImageRequest) (ImageBuildResult, error) {
	body := deepCopyMap(input.Body)
	opts, present, err := takeImageProviderOptions(body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	applied, err := applyImageProviderOptions(body, "xai", opts)
	if err != nil {
		return ImageBuildResult{}, err
	}
	ignored := dropImageParams(body, "stream", "partial_images", "quality", "background",
		"output_compression", "safety_identifier", "moderation", "user")
	result := ImageBuildResult{
		Provider:               "xai",
		ParamsIgnored:          ignored,
		ProviderOptionsApplied: excludeStrings(applied, ignored),
	}
	if !present && len(ignored) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ImageBuildResult{}, fmt.Errorf("xai 图像请求体序列化失败: %w", err)
	}
	result.Body = encoded
	return result, nil
}

func (xaiImageAdapter) UpstreamPath(clientPath string) string { return clientPath }
func (xaiImageAdapter) RequiresResponseTransform() bool       { return false }
func (xaiImageAdapter) TransformResponse(_ ImageRequest, body []byte) ([]byte, error) {
	return body, nil
}

// xaiImageEditIgnoredParams 是 xAI 编辑面无对应语义、删除并回显的参数
// （契约 §2.9.1/§6.3 编辑节）。
var xaiImageEditIgnoredParams = []string{
	"mask", "quality", "background", "output_format", "output_compression",
	"input_fidelity", "safety_identifier", "user", "stream", "partial_images",
}

// xaiImageEditPreservedParams 是 xAI 编辑面保留的公共参数。
var xaiImageEditPreservedParams = []string{"model", "prompt", "n", "size", "response_format"}

// BuildEdit 处理 xAI 编辑请求：出站仍是原生 POST /v1/images/edits（账户 base
// 版本根直拼），单图写 image:{url}、多图写 images:[{url}]（URL 或 data
// URL）；provider_options.xai 深合并（图片装配键先剔除，L3 不得打破
// image/images 互斥）；未文档化参数（含 L3 注入的同名键）无条件删除并回显。
func (xaiImageAdapter) BuildEdit(input ImageRequest) (ImageBuildResult, error) {
	if err := validateImageEditRequiredFields(input.Body); err != nil {
		return ImageBuildResult{}, err
	}
	body := deepCopyMap(input.Body)
	opts, _, err := imageEditTakeProviderOptions(input, body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	removedImageKeys := imageEditSanitizeProviderOptionImageKeys(opts, "xai")
	out := make(map[string]any, len(xaiImageEditPreservedParams)+2)
	for _, key := range xaiImageEditPreservedParams {
		if value, ok := body[key]; ok {
			out[key] = value
		}
	}
	imageEditAttachURLObjects(out, imageEditSourceURLs(input))
	appliedOptions, err := applyImageProviderOptions(out, "xai", opts)
	if err != nil {
		return ImageBuildResult{}, err
	}
	ignored := imageEditIgnoredTopLevel(input, xaiImageEditPreservedParams, xaiImageEditIgnoredParams, out)
	ignored = sortedUniqueStrings(append(ignored, removedImageKeys...))
	// 无条件删除 forbidden：provider_options.xai 注入的同名键也必须清除，
	// 不能借 L3 绕过统一图像契约（stream 可能让上游返回 SSE）。
	for _, key := range xaiImageEditIgnoredParams {
		delete(out, key)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return ImageBuildResult{}, fmt.Errorf("xai 图像编辑请求体序列化失败: %w", err)
	}
	return ImageBuildResult{
		Body:                   encoded,
		ContentType:            "application/json",
		Provider:               "xai",
		ParamsIgnored:          ignored,
		ProviderOptionsApplied: excludeStrings(appliedOptions, ignored),
	}, nil
}

// volcengineImageAdapter 走 Ark OpenAI 兼容图像面：默认显式 watermark=false
// （厂商默认 true 会带“AI 生成”水印），provider_options.volcengine 深合并。
type volcengineImageAdapter struct{}

func (volcengineImageAdapter) Provider() string { return "volcengine" }

func (volcengineImageAdapter) Build(input ImageRequest) (ImageBuildResult, error) {
	body := deepCopyMap(input.Body)
	opts, _, err := takeImageProviderOptions(body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	appliedOptions, err := applyImageProviderOptions(body, "volcengine", opts)
	if err != nil {
		return ImageBuildResult{}, err
	}
	ignored := dropImageParams(body, "stream", "partial_images", "quality",
		"output_compression", "safety_identifier", "moderation", "user")
	paramsApplied := []string{"watermark"}
	if _, ok := body["watermark"]; !ok {
		body["watermark"] = false
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ImageBuildResult{}, fmt.Errorf("volcengine 图像请求体序列化失败: %w", err)
	}
	return ImageBuildResult{
		Body:                   encoded,
		Provider:               "volcengine",
		ParamsApplied:          paramsApplied,
		ParamsIgnored:          ignored,
		ProviderOptionsApplied: excludeStrings(appliedOptions, ignored),
	}, nil
}

func (volcengineImageAdapter) UpstreamPath(clientPath string) string {
	// Ark 无独立 edits 端点：编辑请求改写为同一 /api/v3/images/generations。
	// 账户 base_url 已含 /api/v3 版本根，这里只返回相对操作路径（与生成面
	// openai 直拼口径一致）；其余路径原样。
	if !IsImageEditPath(clientPath) {
		return clientPath
	}
	_, query := splitImagePathAndQuery(clientPath)
	return volcengineImageGenerationPath + query
}

func (volcengineImageAdapter) RequiresResponseTransform() bool { return false }
func (volcengineImageAdapter) TransformResponse(_ ImageRequest, body []byte) ([]byte, error) {
	return body, nil
}

// volcengineImageEditIgnoredParams 是 Ark 编辑面（走 generations）无对应语义、
// 删除并回显的参数（契约 §2.9.1/§9.3 编辑节）。
var volcengineImageEditIgnoredParams = []string{
	"mask", "quality", "output_compression", "input_fidelity",
	"safety_identifier", "user", "stream", "partial_images",
}

// volcengineImageEditPreservedParams 是 Ark 编辑面保留的公共参数。
// watermark 属 L2 可覆盖参数：客户端显式值原样透传（缺省仍默认 false）。
var volcengineImageEditPreservedParams = []string{
	"model", "prompt", "size", "response_format", "output_format", "background", "n", "watermark",
}

// volcengineImageGenerationPath 是 Ark 图像生成操作路径（相对 base 版本根）。
const volcengineImageGenerationPath = "/images/generations"

// BuildEdit 处理火山编辑请求：无独立 edits 端点，改写为 /api/v3/images/
// generations；图片放 image（单图字符串、多图数组，URL 或 data URL）；
// watermark 默认 false（provider_options.volcengine 可覆盖）；mask 等忽略，
// L3 注入的 forbidden 参数（如 stream）同样无条件删除并回显。
func (volcengineImageAdapter) BuildEdit(input ImageRequest) (ImageBuildResult, error) {
	if err := validateImageEditRequiredFields(input.Body); err != nil {
		return ImageBuildResult{}, err
	}
	body := deepCopyMap(input.Body)
	opts, _, err := imageEditTakeProviderOptions(input, body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	removedImageKeys := imageEditSanitizeProviderOptionImageKeys(opts, "volcengine")
	out := make(map[string]any, len(volcengineImageEditPreservedParams)+2)
	for _, key := range volcengineImageEditPreservedParams {
		if value, ok := body[key]; ok {
			out[key] = value
		}
	}
	imageEditAttachImageStrings(out, imageEditSourceURLs(input))
	appliedOptions, err := applyImageProviderOptions(out, "volcengine", opts)
	if err != nil {
		return ImageBuildResult{}, err
	}
	ignored := imageEditIgnoredTopLevel(input, volcengineImageEditPreservedParams, volcengineImageEditIgnoredParams, out)
	ignored = sortedUniqueStrings(append(ignored, removedImageKeys...))
	// 无条件删除 forbidden：provider_options.volcengine 注入的 stream 等键
	// 也必须清除，避免 Seedream 返回 SSE 破坏统一普通 JSON 裁决。
	for _, key := range volcengineImageEditIgnoredParams {
		delete(out, key)
	}
	if _, ok := out["watermark"]; !ok {
		out["watermark"] = false
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return ImageBuildResult{}, fmt.Errorf("volcengine 图像编辑请求体序列化失败: %w", err)
	}
	return ImageBuildResult{
		Body:                   encoded,
		ContentType:            "application/json",
		Provider:               "volcengine",
		ParamsApplied:          []string{"watermark"},
		ParamsIgnored:          ignored,
		ProviderOptionsApplied: excludeStrings(appliedOptions, ignored),
	}, nil
}

// takeImageProviderOptions 取出并删除 provider_options，返回按 provider 归一的
// 子对象；present 表示客户端是否携带该键（用于判断是否需要重新序列化）。
func takeImageProviderOptions(body map[string]any) (map[string]map[string]any, bool, error) {
	raw, ok := body[ProviderOptionsKey]
	if !ok {
		return nil, false, nil
	}
	delete(body, ProviderOptionsKey)
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false, fmt.Errorf("provider_options 序列化失败: %w", err)
	}
	opts, err := ExtractProviderOptions(encoded)
	if err != nil {
		return nil, false, err
	}
	return opts, true, nil
}

// applyImageProviderOptions 把命中 provider 的子对象 deep-merge 进 target，
// 返回实际合并的键名（字典序）。
func applyImageProviderOptions(target map[string]any, providerCode string, opts map[string]map[string]any) ([]string, error) {
	if err := ValidateImageBillingProviderOptions(providerCode, opts); err != nil {
		return nil, err
	}
	for key, sub := range opts {
		if !providerOptionKeyMatches(providerCode, key) {
			continue
		}
		mergeProviderValues(target, sub)
		return sortedStringKeys(sub), nil
	}
	return nil, nil
}

func sortedStringKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if value == "" || (len(out) > 0 && out[len(out)-1] == value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

// excludeStrings 返回 values 中不在 excluded 里的元素（保序、去重）。
func excludeStrings(values, excluded []string) []string {
	if len(values) == 0 {
		return nil
	}
	skip := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		skip[value] = struct{}{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, blocked := skip[value]; blocked {
			continue
		}
		out = append(out, value)
	}
	return out
}

// dropImageParams 删除指定顶层参数并返回实际存在的键名（字典序）。
func dropImageParams(body map[string]any, keys ...string) []string {
	dropped := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := body[key]; !ok {
			continue
		}
		delete(body, key)
		dropped = append(dropped, key)
	}
	sort.Strings(dropped)
	return dropped
}

func splitImagePathAndQuery(pathWithQuery string) (string, string) {
	if index := strings.IndexByte(pathWithQuery, '?'); index >= 0 {
		return pathWithQuery[:index], pathWithQuery[index:]
	}
	return pathWithQuery, ""
}
