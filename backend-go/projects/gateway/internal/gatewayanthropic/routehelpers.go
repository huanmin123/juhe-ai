package gatewayanthropic

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// 协议标识（对齐 domain/provider-protocol.ts）。
const (
	ProtocolCode    = "anthropic"
	ProtocolVersion = "v1"
)

// 端点能力模式（对齐 domain/anthropic-endpoint-modes.ts）。
const (
	EndpointModeMessagesJSON         = "messages_json"
	EndpointModeMessagesSSE          = "messages_sse"
	EndpointModeMessageTokenCounting = "message_token_counting"
)

// ModelCatalogItem 是协议层需要的模型目录最小投影（对齐 ProviderModelCatalogItem
// 中 Anthropic models 响应实际消费的字段）。
type ModelCatalogItem struct {
	Model       string
	ReleaseDate string // YYYY-MM-DD
	CreatedAt   string // RFC3339
}

// ModelListItem 对齐 AnthropicModelListItem。
type ModelListItem struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// ModelsListResponse 对齐 AnthropicModelsListResponse。
type ModelsListResponse struct {
	Data    []ModelListItem `json:"data"`
	HasMore bool            `json:"has_more"`
	FirstID *string         `json:"first_id"`
	LastID  *string         `json:"last_id"`
}

// UpstreamAccount 是路由层需要的账户最小投影（对齐 OpenAIAccountSecret 的
// type/baseUrl；api_key 与 oauth 账户支持 Anthropic 原生转发）。
type UpstreamAccount struct {
	Type    string
	BaseURL string
}

// RequestPathAndQuery 提取请求的 originalUrl 等价物（转义路径 + 原始 query）。
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

// IsNativeRequest 对齐 isAnthropicNativeRequest / isSupportedAnthropicRequest：
// POST /messages、POST /messages/count_tokens、GET /models。
func IsNativeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	method := strings.ToUpper(r.Method)
	path := normalizedAnthropicPath(RequestPathAndQuery(r))
	if method == "POST" && path == "/messages" {
		return true
	}
	if method == "POST" && path == "/messages/count_tokens" {
		return true
	}
	if method == "GET" && path == "/models" {
		return true
	}
	return false
}

// IsModelsRequest 对齐 isAnthropicModelsRequest。
func IsModelsRequest(r *http.Request) bool {
	if r == nil || strings.ToUpper(r.Method) != "GET" {
		return false
	}
	return normalizedAnthropicPath(RequestPathAndQuery(r)) == "/models"
}

// IsMessagesPostRequest 对齐 client-compatibility.ts 的 isAnthropicMessagesPostRequest。
func IsMessagesPostRequest(r *http.Request, pathAndQuery string) bool {
	if r == nil || strings.ToUpper(r.Method) != "POST" {
		return false
	}
	if pathAndQuery == "" {
		pathAndQuery = RequestPathAndQuery(r)
	}
	path := splitPathAndQuery(pathAndQuery).Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return stripV1Prefix(path) == "/messages"
}

// EndpointModeForRequestShape 对齐 anthropicEndpointModeForRequestShape。
func EndpointModeForRequestShape(endpoint string, stream bool) string {
	family := anthropicEndpointFamilyFromPath(endpoint)
	switch family {
	case EndpointFamilyMessages:
		if stream {
			return EndpointModeMessagesSSE
		}
		return EndpointModeMessagesJSON
	case EndpointFamilyMessageTokenCount:
		return EndpointModeMessageTokenCounting
	default:
		return ""
	}
}

// anthropicEndpointFamilyFromPath 对齐 domain/anthropic-endpoint-modes.ts 的
// anthropicEndpointFamilyFromPath（小写化后匹配）。
func anthropicEndpointFamilyFromPath(value string) EndpointFamily {
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
	path = stripV1Prefix(path)
	switch path {
	case "/messages":
		return EndpointFamilyMessages
	case "/messages/count_tokens":
		return EndpointFamilyMessageTokenCount
	case "/models":
		return EndpointFamilyModels
	default:
		return ""
	}
}

// BuildUpstreamURL 对齐 buildAnthropicUpstreamUrl：base 规范化补 /v1，
// 请求路径去 /v1 前缀后拼接并保留原 query。
func BuildUpstreamURL(baseURL, pathAndQuery string) (string, error) {
	normalizedBase, err := normalizeAnthropicBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	suffix := anthropicPathSuffix(pathAndQuery)
	return normalizedBase + suffix, nil
}

// BuildUpstreamURLsForAccount 对齐 buildAnthropicUpstreamUrlsForAccount。
func BuildUpstreamURLsForAccount(account UpstreamAccount, r *http.Request) []string {
	if account.Type != "api_key" && account.Type != "oauth" {
		return nil
	}
	if !IsNativeRequest(r) {
		return nil
	}
	// Anthropic Claude Code 兼容会在 messages 请求上追加 beta=true（见
	// clientcompatibility.go 的 PathAndQueryForRequest）。
	built, err := BuildUpstreamURL(account.BaseURL, PathAndQueryForRequest(r, RequestPathAndQuery(r), nil))
	if err != nil {
		return nil
	}
	return []string{built}
}

func normalizeAnthropicBaseURL(baseURL string) (string, error) {
	normalizedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalizedBase == "" {
		return "", fmt.Errorf("Anthropic 上游 baseUrl 不能为空")
	}
	if !strings.HasSuffix(normalizedBase, "/v1") {
		normalizedBase += "/v1"
	}
	return normalizedBase, nil
}

func anthropicPathSuffix(pathAndQuery string) string {
	parts := splitPathAndQuery(pathAndQuery)
	normalizedPath := normalizedAnthropicPath(pathAndQuery)
	if normalizedPath == "/" {
		return parts.Query
	}
	return normalizedPath + parts.Query
}

// BuildModelsResponse 对齐 buildAnthropicModelsResponse。
func BuildModelsResponse(catalog []ModelCatalogItem) (ModelsListResponse, error) {
	data := make([]ModelListItem, 0, len(catalog))
	for _, item := range catalog {
		createdAt, err := modelCreatedAt(item)
		if err != nil {
			return ModelsListResponse{}, err
		}
		data = append(data, ModelListItem{
			Type:        "model",
			ID:          item.Model,
			DisplayName: item.Model,
			CreatedAt:   createdAt,
		})
	}
	response := ModelsListResponse{Data: data}
	if len(data) > 0 {
		first := data[0].ID
		last := data[len(data)-1].ID
		response.FirstID = &first
		response.LastID = &last
	}
	return response, nil
}

// modelCreatedAt 对齐 route-helpers.ts 的 modelCreatedAt：releaseDate 必须是
// YYYY-MM-DD（按 UTC 零点展开为毫秒精度的 RFC3339）；createdAt 必须是带
// Z 或数值 offset 的 RFC3339 并规范化为 UTC ISO 输出。
func modelCreatedAt(item ModelCatalogItem) (string, error) {
	if item.ReleaseDate != "" {
		releaseDate := strings.TrimSpace(item.ReleaseDate)
		if !YYYYMMDDPattern.MatchString(releaseDate) {
			return "", fmt.Errorf("Anthropic 模型 releaseDate 必须是 YYYY-MM-DD 日期")
		}
		// 与 Node rfc3339InstantMilliseconds(`${releaseDate}T00:00:00.000Z`) 等价。
		return releaseDate + "T00:00:00.000Z", nil
	}
	if item.CreatedAt == "" {
		return "", nil
	}
	canonical, ok := CanonicalizeRFC3339Instant(item.CreatedAt)
	if !ok {
		return "", fmt.Errorf("Anthropic 模型 createdAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return canonical, nil
}

// withQueryParamIfMissing 对齐同名工具：保留既有 query 并确保 name=value。
func withQueryParamIfMissing(pathAndQuery, name, value string) string {
	if pathAndQuery == "" {
		pathAndQuery = "/"
	}
	parts := splitPathAndQuery(pathAndQuery)
	rawQuery := strings.TrimPrefix(parts.Query, "?")
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		params = url.Values{}
	}
	if params.Get(name) != value {
		params.Set(name, value)
	}
	serialized := encodeQuerySorted(params)
	if serialized != "" {
		return parts.Path + "?" + serialized
	}
	return parts.Path
}

// encodeQuerySorted 序列化 query（单参数场景与 Node URLSearchParams 一致；
// 多参数时按键排序保证确定性）。
func encodeQuerySorted(params url.Values) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	// url.Values 是 map，Node URLSearchParams 保持插入序；
	// 这里兼容既有调用只有单个参数的场景，排序保证确定性。
	sortStrings(keys)
	var builder strings.Builder
	for i, key := range keys {
		for _, value := range params[key] {
			if i > 0 || builder.Len() > 0 {
				builder.WriteByte('&')
			}
			builder.WriteString(url.QueryEscape(key))
			builder.WriteByte('=')
			builder.WriteString(url.QueryEscape(value))
		}
	}
	return builder.String()
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
