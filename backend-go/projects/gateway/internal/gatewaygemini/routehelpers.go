package gatewaygemini

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

// 协议与供应商标识（对齐 domain/provider-protocol.ts）。
const (
	ProtocolCode    = "gemini"
	ProtocolVersion = "v1beta"
	ProviderCode    = "gemini"
	// defaultGeminiBase 按 v2 直拼契约即版本根：base_url 缺省时自带 /v1beta
	// 版本服务根（上游 Base URL 版本根语义 v2，R3）。
	defaultGeminiBase = "https://generativelanguage.googleapis.com/v1beta"
)

// 端点能力模式（对齐 domain/gemini-endpoint-modes.ts）。
const (
	EndpointModeGenerateContentJSON = "generate_content_json"
	EndpointModeGenerateContentSSE  = "generate_content_sse"
	EndpointModeCountTokens         = "count_tokens"
	EndpointModeEmbedContent        = "embed_content"
	EndpointModeInteractionsJSON    = "interactions_json"
	EndpointModeInteractionsSSE     = "interactions_sse"
)

// interactionResourcePathPattern 对齐 geminiEndpointFamilyFromPath 的
// interactions 族匹配（gemini-endpoint-modes.ts:120
// /^\/interactions(?:\/[^/]+(?:\/cancel)?)?$/）：接受裸 /interactions（create）、
// /interactions/{id} 与 /interactions/{id}/cancel 三形态。affinity 侧的
// resource 匹配（affinity.go interactionResourcePathMatch）要求捕获组非空，
// 裸路径不会误判为资源请求。
var interactionResourcePathPattern = regexp.MustCompile(`(?i)^/interactions(?:/([^/]+)(?:/cancel)?)?$`)

// ModelCatalogItem 是协议层需要的模型目录最小投影。
type ModelCatalogItem struct {
	Model                 string
	CapabilityNotes       string
	Notes                 string
	MaxInputTokens        int
	ContextWindowTokens   int
	MaxOutputTokens       int
	SupportedAPIProtocols []string
}

// ModelListItem 对齐 GeminiModelListItem。
type ModelListItem struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description,omitempty"`
	InputTokenLimit            int      `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int      `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// ModelsListResponse 对齐 GeminiModelsListResponse。
type ModelsListResponse struct {
	Models []ModelListItem `json:"models"`
}

// UpstreamAccount 是路由与亲和层需要的账户最小投影（对齐
// DispatchAccountSecret；api_key 与 google_oauth 账户支持 Gemini 原生转发）。
type UpstreamAccount struct {
	ID                        string
	Type                      string
	BaseURL                   string
	ProviderCode              string
	ProviderProtocolProfileID string
}

// EndpointFamilyFromPath 对齐 geminiEndpointFamilyFromPath：整段小写化后
// 剥离 /v1beta 前缀再匹配端点族。
func EndpointFamilyFromPath(value string) EndpointFamily {
	if value == "" {
		return ""
	}
	path := strings.ToLower(strings.TrimSpace(value))
	if path == "" {
		return ""
	}
	if index := strings.Index(path, "?"); index >= 0 {
		path = path[:index]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = stripV1BetaPrefix(path)
	switch path {
	case "/models":
		return EndpointFamilyModels
	}
	if interactionResourcePathPattern.MatchString(path) {
		return EndpointFamilyInteractions
	}
	match := modelActionPattern.FindStringSubmatch(path)
	if match == nil {
		return ""
	}
	switch match[1] {
	case "generatecontent":
		return EndpointFamilyGenerateContent
	case "streamgeneratecontent":
		return EndpointFamilyStreamGenerateContent
	case "counttokens":
		return EndpointFamilyCountTokens
	case "embedcontent":
		return EndpointFamilyEmbedContent
	default:
		return ""
	}
}

var modelActionPattern = regexp.MustCompile(`^/models/[^/]+:(generatecontent|streamgeneratecontent|counttokens|embedcontent)$`)

// stripV1BetaPrefix 对齐 replace(/^\/v1beta(?=\/|$)/i, ”)。
func stripV1BetaPrefix(path string) string {
	lower := strings.ToLower(path)
	if !strings.HasPrefix(lower, "/v1beta") {
		return path
	}
	rest := path[len("/v1beta"):]
	if rest == "" || strings.HasPrefix(rest, "/") {
		return rest
	}
	return path
}

// IsNativeRequest 对齐 isGeminiNativeRequest。
func IsNativeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	method := strings.ToUpper(r.Method)
	family := EndpointFamilyFromPath(RequestPathAndQuery(r))
	if method == "GET" && family == EndpointFamilyModels {
		return true
	}
	if (method == "GET" || method == "DELETE") && family == EndpointFamilyInteractions {
		return true
	}
	if method != "POST" {
		return false
	}
	return family == EndpointFamilyGenerateContent ||
		family == EndpointFamilyStreamGenerateContent ||
		family == EndpointFamilyCountTokens ||
		family == EndpointFamilyEmbedContent ||
		family == EndpointFamilyInteractions
}

// IsModelsRequest 对齐 isGeminiModelsRequest。
func IsModelsRequest(r *http.Request) bool {
	if r == nil || strings.ToUpper(r.Method) != "GET" {
		return false
	}
	return EndpointFamilyFromPath(RequestPathAndQuery(r)) == EndpointFamilyModels
}

// IsInteractionsRequest 对齐 isGeminiInteractionsRequest。
func IsInteractionsRequest(r *http.Request) bool {
	return EndpointFamilyFromPath(RequestPathAndQuery(r)) == EndpointFamilyInteractions
}

// EndpointModeForRequestShape 对齐 geminiEndpointModeForRequestShape。
func EndpointModeForRequestShape(endpoint string, stream bool) string {
	family := EndpointFamilyFromPath(endpoint)
	switch family {
	case EndpointFamilyGenerateContent:
		if stream {
			return EndpointModeGenerateContentSSE
		}
		return EndpointModeGenerateContentJSON
	case EndpointFamilyStreamGenerateContent:
		return EndpointModeGenerateContentSSE
	case EndpointFamilyCountTokens:
		return EndpointModeCountTokens
	case EndpointFamilyEmbedContent:
		return EndpointModeEmbedContent
	case EndpointFamilyInteractions:
		if stream {
			return EndpointModeInteractionsSSE
		}
		return EndpointModeInteractionsJSON
	default:
		return ""
	}
}

// RequestPathAndQuery 提取请求的 originalUrl 等价物。
func RequestPathAndQuery(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		return path + "?" + r.URL.RawQuery
	}
	return path
}

// RequestIndicatesSSE 对齐 requestIndicatesSSE：query stream=true、
// 请求体 stream 标志或 Accept: text/event-stream。
func RequestIndicatesSSE(r *http.Request, bodyStream bool) bool {
	parts := splitPathAndQuery(RequestPathAndQuery(r))
	if parts.Query != "" {
		if params, err := url.ParseQuery(strings.TrimPrefix(parts.Query, "?")); err == nil {
			if strings.EqualFold(params.Get("stream"), "true") {
				return true
			}
		}
	}
	if bodyStream {
		return true
	}
	accept := r.Header.Get("Accept")
	return accept != "" && strings.Contains(strings.ToLower(accept), "text/event-stream")
}

// BuildUpstreamURLsForAccount 对齐 buildGeminiUpstreamUrlsForAccount。
// modelsProbe 对齐 isGatewayUpstreamModelsProbe（由编排层标记的模型探测请求）。
func BuildUpstreamURLsForAccount(account UpstreamAccount, r *http.Request, modelsProbe bool) []string {
	if account.Type != "api_key" && account.Type != "google_oauth" {
		return nil
	}
	if !IsNativeRequest(r) {
		return nil
	}
	if IsModelsRequest(r) {
		if !modelsProbe {
			return nil
		}
		built, err := BuildUpstreamURL(account.BaseURL, RequestPathAndQuery(r), false)
		if err != nil {
			return nil
		}
		return []string{built}
	}
	family := EndpointFamilyFromPath(RequestPathAndQuery(r))
	stream := family == EndpointFamilyStreamGenerateContent ||
		(family == EndpointFamilyInteractions && RequestIndicatesSSE(r, false))
	built, err := BuildUpstreamURL(account.BaseURL, RequestPathAndQuery(r), stream)
	if err != nil {
		return nil
	}
	return []string{built}
}

// BuildUpstreamURL 按 v2 直拼契约构造：入站路径剥 /v1beta 前缀后作为 opPath，
// base_url 即版本根（缺省回落 defaultGeminiBase，自带 /v1beta），base 与
// opPath 的拼接统一走共享 upstreamenvelope.JoinUpstreamURL（R1：不再强制挂
// /v1beta、不做 base 侧补挂或去重）；跨段斜杠折叠（collapseSlashes）与 query
// 归并（一律删除请求 query 中的 key、stream 补 alt=sse、interactions 删除
// alt）保留在调用侧。
func BuildUpstreamURL(baseURL, pathAndQuery string, stream bool) (string, error) {
	normalizedPath, sourceQuery := normalizedGeminiPathAndQuery(pathAndQuery)
	base, err := normalizedGeminiBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	joined, err := upstreamenvelope.JoinUpstreamURL(base.Scheme+"://"+base.Host,
		collapseSlashes(strings.TrimRight(base.Path, "/")+mapSlashToEmpty(normalizedPath)))
	if err != nil {
		return "", err
	}
	target, err := url.Parse(joined)
	if err != nil {
		return "", err
	}
	// base 自带 query（如有）先并入 target，再由 source 覆盖合并——与原实现
	// 在同一 url.Values 上先装 base query 再 merge 的语义一致。
	mergedQuery := target.Query()
	for key, values := range base.Query() {
		for _, value := range values {
			mergedQuery.Set(key, value)
		}
	}
	endpointFamily := EndpointFamilyFromPath(normalizedPath)
	if endpointFamily == EndpointFamilyInteractions {
		sourceQuery.Del("alt")
	}
	mergeGeminiQuery(mergedQuery, sourceQuery, stream)
	target.RawQuery = mergedQuery.Encode()
	return target.String(), nil
}

func mapSlashToEmpty(path string) string {
	if path == "/" {
		return ""
	}
	return path
}

func collapseSlashes(value string) string {
	for strings.Contains(value, "//") {
		value = strings.ReplaceAll(value, "//", "/")
	}
	return value
}

func normalizedGeminiBaseURL(baseURL string) (*url.URL, error) {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" {
		normalized = defaultGeminiBase
	}
	return url.Parse(normalized)
}

// normalizedGeminiPathAndQuery 按 v2 契约产出 opPath：路径补全后剥去入站
// /v1beta 前缀（入站边界语义保留），不再强制挂 /v1beta——版本段只存在于
// base_url 一侧。
func normalizedGeminiPathAndQuery(pathAndQuery string) (string, *url.Values) {
	parts := splitPathAndQuery(pathAndQuery)
	rawPath := parts.Path
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	normalizedPath := stripV1BetaPrefixInsensitive(rawPath)
	if normalizedPath == "/" {
		normalizedPath = ""
	}
	query := url.Values{}
	if parts.Query != "" {
		if parsed, err := url.ParseQuery(strings.TrimPrefix(parts.Query, "?")); err == nil {
			query = parsed
		}
	}
	return normalizedPath, &query
}

// stripV1BetaPrefixInsensitive 对齐 replace(/^\/v1beta(?=\/|$)/i, ”)。
func stripV1BetaPrefixInsensitive(path string) string {
	return stripV1BetaPrefix(path)
}

// mergeGeminiQuery 对齐 mergeGeminiQuery：删除 key、覆盖合并、stream 补 alt=sse。
func mergeGeminiQuery(target url.Values, source *url.Values, stream bool) {
	source.Del("key")
	for key, values := range *source {
		for _, value := range values {
			target.Set(key, value)
		}
	}
	if stream {
		if _, exists := target["alt"]; !exists {
			target.Set("alt", "sse")
		}
	}
}

// BuildModelsResponse 对齐 buildGeminiModelsResponse。
func BuildModelsResponse(catalog []ModelCatalogItem) ModelsListResponse {
	models := make([]ModelListItem, 0, len(catalog))
	for _, item := range catalog {
		models = append(models, ModelListItem{
			Name:                       modelName(item.Model),
			Version:                    item.Model,
			DisplayName:                item.Model,
			Description:                orString(item.CapabilityNotes, item.Notes),
			InputTokenLimit:            positiveInteger(firstPositive(item.MaxInputTokens, item.ContextWindowTokens)),
			OutputTokenLimit:           positiveInteger(item.MaxOutputTokens),
			SupportedGenerationMethods: supportedGenerationMethods(item.SupportedAPIProtocols),
		})
	}
	return ModelsListResponse{Models: models}
}

func modelName(model string) string {
	if strings.HasPrefix(model, "models/") {
		return model
	}
	return "models/" + model
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// supportedGenerationMethods 对齐 supportedGenerationMethods。
func supportedGenerationMethods(protocols []string) []string {
	set := make(map[string]bool, len(protocols))
	for _, protocol := range protocols {
		set[protocol] = true
	}
	methods := []string{}
	if set["generate_content"] || set["stream_generate_content"] {
		methods = append(methods, "generateContent")
	}
	if set["count_tokens"] {
		methods = append(methods, "countTokens")
	}
	if set["embed_content"] {
		methods = append(methods, "embedContent")
	}
	if len(methods) == 0 {
		return []string{"generateContent"}
	}
	return methods
}

// positiveInteger 对齐 positiveInteger。
func positiveInteger(value int) int {
	if value > 0 {
		return value
	}
	return 0
}

func splitPathAndQuery(pathAndQuery string) (result struct{ Path, Query string }) {
	if index := strings.Index(pathAndQuery, "?"); index >= 0 {
		result.Path = pathAndQuery[:index]
		result.Query = pathAndQuery[index:]
		return result
	}
	result.Path = pathAndQuery
	return result
}
