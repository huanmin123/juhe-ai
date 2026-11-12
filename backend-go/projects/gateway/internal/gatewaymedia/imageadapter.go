// imageadapter.go 是同步图像请求的厂商适配层（2026-11-11 图像生成统一协议
// 同步批）。对外统一 OpenAI Images 形态；gpt 原生直通，xai/volcengine 走各自
// OpenAI 兼容图像端点，qwen 走 DashScope 原生 multimodal-generation 并由
// qwenImageAdapter 转换响应。图像不落 media_jobs（异步任务面本批不做）。
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
)

// ImageRequest 是一次同步图像请求的归一层输入。
type ImageRequest struct {
	// Body 是客户端 OpenAI Images JSON（含 provider_options）。
	Body map[string]any
	// ProviderCode 是账户 vendor_code（gpt/xai/volcengine/qwen）。
	ProviderCode string
}

// ImageBuildResult 是 adapter 构造上游请求的结果。Body 为 nil 表示不修改
// 客户端字节（gpt 无 provider_options 时的纯透传）。
type ImageBuildResult struct {
	Body                   []byte
	Provider               string
	ParamsApplied          []string
	ParamsIgnored          []string
	ProviderOptionsApplied []string
}

// ImageAdapter 是每厂商一个的同步图像出站 adapter。
type ImageAdapter interface {
	Provider() string
	// Build 把 OpenAI Images body 转为上游 body；不修改入参。
	Build(input ImageRequest) (ImageBuildResult, error)
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
	path, _ := splitImagePathAndQuery(pathWithQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	path = strings.TrimPrefix(path, "/v1")
	path = strings.TrimSuffix(path, "/")
	return path == "/images/generations"
}

// ParseImageRequest 把客户端 body 复制为 ImageRequest（不修改 body）。
func ParseImageRequest(body map[string]any, providerCode string) ImageRequest {
	return ImageRequest{Body: deepCopyMap(body), ProviderCode: providerCode}
}

// BuildImageUpstreamRequest 是链上请求侧入口：按 vendor 归一图像 body。
// 返回 nil 表示沿用客户端原始字节（gpt 无扩展参数时的纯透传）。
func BuildImageUpstreamRequest(providerCode string, body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		// 图像请求体必须是 JSON 对象；非对象交给上游或后续通用链裁决，
		// adapter 不在这里新增第二套形态门。
		return nil, nil
	}
	adapter := ImageAdapterForProvider(providerCode)
	if adapter == nil {
		return nil, nil
	}
	result, err := adapter.Build(ParseImageRequest(parsed, providerCode))
	if err != nil {
		return nil, err
	}
	return result.Body, nil
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
}

// ImageEchoForRequest 用与请求侧同一 adapter 计算回显事实（纯函数）。
func ImageEchoForRequest(providerCode string, body map[string]any) ImageEcho {
	echo := ImageEcho{Provider: canonicalImageProvider(providerCode)}
	adapter := ImageAdapterForProvider(providerCode)
	if adapter == nil || len(body) == 0 {
		return echo
	}
	result, err := adapter.Build(ParseImageRequest(body, providerCode))
	if err != nil {
		return echo
	}
	echo.ParamsApplied = result.ParamsApplied
	echo.ParamsIgnored = result.ParamsIgnored
	echo.ProviderOptionsApplied = result.ProviderOptionsApplied
	return echo
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

// openAIImageAdapter 是 gpt 原生直通：只有 provider_options 需要消费，
// 其余字节原样透传（含 stream/partial_images 与 b64_json 大响应）。
type openAIImageAdapter struct{}

func (openAIImageAdapter) Provider() string { return "openai" }

func (openAIImageAdapter) Build(input ImageRequest) (ImageBuildResult, error) {
	body := deepCopyMap(input.Body)
	opts, present, err := takeImageProviderOptions(body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	applied := applyImageProviderOptions(body, "gpt", opts)
	result := ImageBuildResult{
		Provider:               "openai",
		ProviderOptionsApplied: applied,
	}
	if !present {
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
	applied := applyImageProviderOptions(body, "xai", opts)
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
	appliedOptions := applyImageProviderOptions(body, "volcengine", opts)
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

func (volcengineImageAdapter) UpstreamPath(clientPath string) string { return clientPath }
func (volcengineImageAdapter) RequiresResponseTransform() bool       { return false }
func (volcengineImageAdapter) TransformResponse(_ ImageRequest, body []byte) ([]byte, error) {
	return body, nil
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
func applyImageProviderOptions(target map[string]any, providerCode string, opts map[string]map[string]any) []string {
	for key, sub := range opts {
		if !providerOptionKeyMatches(providerCode, key) {
			continue
		}
		mergeProviderValues(target, sub)
		return sortedStringKeys(sub)
	}
	return nil
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
