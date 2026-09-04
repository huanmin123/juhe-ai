package gatewaygemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 亲和 TTL 与容量上限（对齐 interaction-affinity.service.ts）。
const (
	InteractionAffinityTTL        = 7 * 24 * time.Hour
	interactionAffinityMaxEntries = 20000
	interactionIDMaxLength        = 512
)

// AffinityScope 对齐 GeminiInteractionAffinityScope。
type AffinityScope struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
}

// AffinityBinding 对齐 GeminiInteractionAffinityBinding。
type AffinityBinding struct {
	InteractionID             string `json:"interactionId"`
	AccountID                 string `json:"accountId"`
	GroupID                   string `json:"groupId"`
	ProviderCode              string `json:"providerCode"`
	ProviderProtocolProfileID string `json:"providerProtocolProfileId,omitempty"`
	CreatedAtMs               int64  `json:"createdAtMs"`
}

// AffinityMutationAction 对齐 'remembered' | 'deleted' | 'refreshed' | 'none'。
type AffinityMutationAction string

const (
	AffinityActionRemembered AffinityMutationAction = "remembered"
	AffinityActionDeleted    AffinityMutationAction = "deleted"
	AffinityActionRefreshed  AffinityMutationAction = "refreshed"
	AffinityActionNone       AffinityMutationAction = "none"
)

// AffinityMutationResult 对齐 GeminiInteractionAffinityMutationResult。
type AffinityMutationResult struct {
	Action        AffinityMutationAction
	InteractionID string
}

// AffinityUnavailableError 对齐 GeminiInteractionAffinityUnavailableError：
// 亲和存储读写失败时对外暴露的 503 校验错误，保留原始错误。
type AffinityUnavailableError struct {
	Op  string // "remember" | "delete"
	Err error
}

func (e *AffinityUnavailableError) Error() string {
	if e.Op == "remember" {
		return "Gemini Interaction 账号亲和记录暂时不可用，请重试"
	}
	return "Gemini Interaction 账号亲和删除暂时不可用，请重试"
}

func (e *AffinityUnavailableError) Unwrap() error { return e.Err }

// StatusCode 对齐 statusCode 503。
func (e *AffinityUnavailableError) StatusCode() int { return http.StatusServiceUnavailable }

// ErrorType 对齐 type 'service_unavailable'（code 为
// 'interaction_affinity_unavailable'）。
func (e *AffinityUnavailableError) ErrorType() string { return "service_unavailable" }

// AffinityStateStore 是运行时状态存储的注入接口（生产环境由 Redis 实现
// 注入，测试可用内存实现）。
type AffinityStateStore interface {
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
	SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// InteractionAffinity 对齐 interaction-affinity.service.ts 的服务主体。
type InteractionAffinity struct {
	store  AffinityStateStore
	now    func() time.Time
	memory *affinityMemoryCache
}

// NewInteractionAffinity 构造亲和服务：store 为 nil 时退化为进程内
// TTL 缓存（对齐 Node runtimeStateDriver !== 'redis' 的回退）。
func NewInteractionAffinity(store AffinityStateStore) *InteractionAffinity {
	return &InteractionAffinity{
		store:  store,
		now:    time.Now,
		memory: newAffinityMemoryCache(interactionAffinityMaxEntries, InteractionAffinityTTL),
	}
}

// WithNowFunc 覆盖时钟（测试用）。
func (a *InteractionAffinity) WithNowFunc(now func() time.Time) *InteractionAffinity {
	a.now = now
	return a
}

// affinityMemoryCache 是无外部依赖时的进程内回退缓存。
type affinityMemoryCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[string]affinityMemoryEntry
	order   []string
}

type affinityMemoryEntry struct {
	binding  AffinityBinding
	expireAt time.Time
}

func newAffinityMemoryCache(max int, ttl time.Duration) *affinityMemoryCache {
	return &affinityMemoryCache{max: max, ttl: ttl, entries: map[string]affinityMemoryEntry{}}
}

func (c *affinityMemoryCache) get(key string) (AffinityBinding, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return AffinityBinding{}, false
	}
	if time.Now().After(entry.expireAt) {
		c.removeLocked(key)
		return AffinityBinding{}, false
	}
	// 对齐 updateAgeOnGet: true：命中即续期。
	entry.expireAt = time.Now().Add(c.ttl)
	c.entries[key] = entry
	return entry.binding, true
}

func (c *affinityMemoryCache) set(key string, binding AffinityBinding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		if len(c.order) > 0 {
			c.removeLocked(c.order[0])
		}
	}
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = affinityMemoryEntry{binding: binding, expireAt: time.Now().Add(c.ttl)}
}

func (c *affinityMemoryCache) removeLocked(key string) {
	delete(c.entries, key)
	for i, item := range c.order {
		if item == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *affinityMemoryCache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
}

// ResourceIDFromRequest 对齐 geminiInteractionResourceIdFromRequest。
func ResourceIDFromRequest(r *http.Request) string {
	segment, ok := interactionResourcePathMatch(r)
	if !ok {
		return ""
	}
	return normalizeInteractionID(decodePathSegment(segment))
}

// IsInteractionResourceRequest 对齐 isGeminiInteractionResourceRequest。
func IsInteractionResourceRequest(r *http.Request) bool {
	_, ok := interactionResourcePathMatch(r)
	return ok
}

// IsInteractionCreateRequest 对齐 isGeminiInteractionCreateRequest：
// POST /interactions（允许 /v1beta 前缀）。
func IsInteractionCreateRequest(r *http.Request) bool {
	if r == nil || strings.ToUpper(r.Method) != "POST" {
		return false
	}
	return strings.EqualFold(normalizedRequestPath(r), "/interactions")
}

// Resolve 对齐 resolveGeminiInteractionAffinityAsync：命中即续期。
func (a *InteractionAffinity) Resolve(ctx context.Context, r *http.Request, scope AffinityScope) (AffinityBinding, bool, error) {
	interactionID := ResourceIDFromRequest(r)
	if interactionID == "" {
		return AffinityBinding{}, false, nil
	}
	key := affinityKey(scope, interactionID)
	binding, found, err := a.getBinding(ctx, key)
	if err != nil {
		return AffinityBinding{}, false, nil
	}
	if !isValidBinding(binding, found, interactionID) {
		if found {
			_ = a.deleteBinding(ctx, key)
		}
		return AffinityBinding{}, false, nil
	}
	// 对齐 resolve 内的 await setBinding(...)：命中续期；续期失败不改变命中结果。
	_ = a.setBinding(ctx, key, binding)
	return binding, true, nil
}

// UpdateAfterSuccessInput 对齐 updateGeminiInteractionAffinityAfterSuccessAsync 的入参。
type UpdateAfterSuccessInput struct {
	Request            *http.Request
	ResponseBodyText   string
	ResponseResourceID string
	Account            UpstreamAccount
	Scope              AffinityScope
}

// UpdateAfterSuccess 对齐 updateGeminiInteractionAffinityAfterSuccessAsync。
func (a *InteractionAffinity) UpdateAfterSuccess(ctx context.Context, input UpdateAfterSuccessInput) (AffinityMutationResult, error) {
	if input.Account.ProviderCode != ProviderCode {
		return AffinityMutationResult{Action: AffinityActionNone}, nil
	}
	if IsInteractionCreateRequest(input.Request) {
		interactionID := normalizeInteractionID(input.ResponseResourceID)
		if interactionID == "" {
			interactionID = InteractionIDFromResponseBody(input.ResponseBodyText)
		}
		return a.Remember(ctx, interactionID, input.Account, input.Scope)
	}
	interactionID := ResourceIDFromRequest(input.Request)
	if interactionID == "" {
		return AffinityMutationResult{Action: AffinityActionNone}, nil
	}
	if strings.ToUpper(input.Request.Method) == "DELETE" {
		return a.Delete(ctx, interactionID, input.Scope)
	}
	_, found, err := a.Resolve(ctx, input.Request, input.Scope)
	if err != nil {
		return AffinityMutationResult{Action: AffinityActionNone}, nil
	}
	if found {
		return AffinityMutationResult{Action: AffinityActionRefreshed, InteractionID: interactionID}, nil
	}
	return AffinityMutationResult{Action: AffinityActionNone}, nil
}

// Remember 对齐 rememberGeminiInteractionAffinityAsync。
func (a *InteractionAffinity) Remember(ctx context.Context, interactionID string, account UpstreamAccount, scope AffinityScope) (AffinityMutationResult, error) {
	if account.ProviderCode != ProviderCode {
		return AffinityMutationResult{Action: AffinityActionNone}, nil
	}
	normalizedID := normalizeInteractionID(interactionID)
	if normalizedID == "" {
		return AffinityMutationResult{Action: AffinityActionNone}, nil
	}
	binding := AffinityBinding{
		InteractionID:             normalizedID,
		AccountID:                 account.ID,
		GroupID:                   scope.GroupID,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		CreatedAtMs:               a.now().UnixMilli(),
	}
	if err := a.setBinding(ctx, affinityKey(scope, normalizedID), binding); err != nil {
		return AffinityMutationResult{}, &AffinityUnavailableError{Op: "remember", Err: err}
	}
	return AffinityMutationResult{Action: AffinityActionRemembered, InteractionID: normalizedID}, nil
}

// Delete 对齐 deleteGeminiInteractionAffinityAsync。
func (a *InteractionAffinity) Delete(ctx context.Context, interactionID string, scope AffinityScope) (AffinityMutationResult, error) {
	normalizedID := normalizeInteractionID(interactionID)
	if normalizedID == "" {
		return AffinityMutationResult{Action: AffinityActionNone}, nil
	}
	if err := a.deleteBinding(ctx, affinityKey(scope, normalizedID)); err != nil {
		return AffinityMutationResult{}, &AffinityUnavailableError{Op: "delete", Err: err}
	}
	return AffinityMutationResult{Action: AffinityActionDeleted, InteractionID: normalizedID}, nil
}

func (a *InteractionAffinity) getBinding(ctx context.Context, key string) (AffinityBinding, bool, error) {
	if a.store != nil {
		var binding AffinityBinding
		found, err := a.store.GetJSON(ctx, key, &binding)
		if err != nil {
			return AffinityBinding{}, false, err
		}
		return binding, found, nil
	}
	binding, ok := a.memory.get(key)
	return binding, ok, nil
}

func (a *InteractionAffinity) setBinding(ctx context.Context, key string, binding AffinityBinding) error {
	if a.store != nil {
		return a.store.SetJSON(ctx, key, binding, InteractionAffinityTTL)
	}
	a.memory.set(key, binding)
	return nil
}

func (a *InteractionAffinity) deleteBinding(ctx context.Context, key string) error {
	if a.store != nil {
		return a.store.Delete(ctx, key)
	}
	a.memory.delete(key)
	return nil
}

// InteractionIDFromResponseBody 对齐 geminiInteractionIdFromResponseBody：
// 先按整体 JSON 解析，再逐行解析 SSE data 负载。
func InteractionIDFromResponseBody(bodyText string) string {
	normalized := strings.TrimSpace(bodyText)
	if normalized == "" {
		return ""
	}
	if id := parseInteractionIDFromJSON(normalized, true); id != "" {
		return id
	}
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(strings.ToLower(trimmed), "data:") {
			continue
		}
		data := strings.TrimSpace(trimmed[len("data:"):])
		if data == "" || data == "[DONE]" {
			continue
		}
		if id := parseInteractionIDFromJSON(data, false); id != "" {
			return id
		}
	}
	return ""
}

// InteractionIDFromParsedResponse 对齐 geminiInteractionIdFromParsedResponse。
func InteractionIDFromParsedResponse(value any) string {
	payload, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return interactionIDFromJSONObject(payload, true)
}

func parseInteractionIDFromJSON(value string, allowRootID bool) string {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return ""
	}
	payload, ok := parsed.(map[string]any)
	if !ok {
		return ""
	}
	return interactionIDFromJSONObject(payload, allowRootID)
}

func interactionIDFromJSONObject(payload map[string]any, allowRootID bool) string {
	if interaction, ok := payload["interaction"].(map[string]any); ok {
		if id := normalizeInteractionID(stringField(interaction["id"])); id != "" {
			return id
		}
	}
	if id := normalizeInteractionID(stringField(payload["interaction_id"])); id != "" {
		return id
	}
	if allowRootID {
		return normalizeInteractionID(stringField(payload["id"]))
	}
	return ""
}

// affinityKey 对齐 affinityKey：scope+interactionId 的 sha256 摘要。
func affinityKey(scope AffinityScope, interactionID string) string {
	apiKeyID := strings.TrimSpace(scope.APIKeyID)
	if apiKeyID == "" {
		apiKeyID = "internal:" + strings.TrimSpace(scope.GroupID)
	}
	summary, err := json.Marshal(struct {
		SystemAccountID string `json:"systemAccountId"`
		APIKeyID        string `json:"apiKeyId"`
		InteractionID   string `json:"interactionId"`
	}{
		SystemAccountID: strings.TrimSpace(scope.SystemAccountID),
		APIKeyID:        apiKeyID,
		InteractionID:   interactionID,
	})
	if err != nil {
		// 结构体序列化不会失败；保持与 Node 契约一致的确定性回退。
		summary = []byte(strings.Join([]string{scope.SystemAccountID, apiKeyID, interactionID}, "|"))
	}
	digest := sha256.Sum256(summary)
	return "interaction:" + hex.EncodeToString(digest[:])
}

// isValidBinding 对齐 isValidBinding。
func isValidBinding(value AffinityBinding, found bool, interactionID string) bool {
	if !found {
		return false
	}
	return value.InteractionID == interactionID &&
		strings.TrimSpace(value.AccountID) != "" &&
		strings.TrimSpace(value.GroupID) != "" &&
		value.ProviderCode == ProviderCode &&
		value.CreatedAtMs != 0
}

// normalizedRequestPath 对齐 normalizedRequestPath：去 query、补前导斜杠、
// 去掉 /v1beta 前缀（大小写不敏感）。
func normalizedRequestPath(r *http.Request) string {
	rawPath := splitPathAndQuery(RequestPathAndQuery(r)).Path
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	normalized := stripV1BetaPrefix(rawPath)
	if normalized == "" {
		normalized = "/"
	}
	return normalized
}

// interactionResourcePathMatch 对齐 geminiInteractionResourcePathMatch：
// /interactions/{id} 支持 GET/DELETE，/interactions/{id}/cancel 支持 POST。
func interactionResourcePathMatch(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	path := normalizedRequestPath(r)
	method := strings.ToUpper(r.Method)
	match := interactionResourcePathPattern.FindStringSubmatch(path)
	if match == nil || match[1] == "" {
		return "", false
	}
	cancelPath := regexpCancelSuffix.MatchString(path)
	if cancelPath {
		if method != "POST" {
			return "", false
		}
	} else if method != "GET" && method != "DELETE" {
		return "", false
	}
	return match[1], true
}

var regexpCancelSuffix = regexp.MustCompile(`(?i)/cancel$`)

// decodePathSegment 对齐 decodePathSegment：解码失败时保留原值。
func decodePathSegment(value string) string {
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

// normalizeInteractionID 对齐 normalizeInteractionId：去除首尾空白、限制
// 长度并拒绝控制字符与路径分隔符。
func normalizeInteractionID(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	normalized := strings.TrimSpace(text)
	if normalized == "" || utf8.RuneCountInString(normalized) > utf16lenLimit {
		return ""
	}
	if containsForbiddenInteractionIDChar(normalized) {
		return ""
	}
	return normalized
}

const utf16lenLimit = interactionIDMaxLength

// containsForbiddenInteractionIDChar 对齐 /[\u0000-\u001f\u007f/\\/]/。
func containsForbiddenInteractionIDChar(value string) bool {
	for _, r := range value {
		if r <= 0x1f || r == 0x7f || r == '/' || r == '\\' {
			return true
		}
	}
	return false
}

// InteractionIDFromJSONPrefix 对齐 geminiInteractionIdFromJsonPrefix：
// 在可能被截断的 JSON 前缀字节流中解析顶层 "id" 字符串属性。
func InteractionIDFromJSONPrefix(rawBody []byte) string {
	index := skipJSONPrefixWhitespace(rawBody, 0)
	if index >= len(rawBody) || rawBody[index] != 0x7b {
		return ""
	}
	index++
	for index < len(rawBody) {
		index = skipJSONPrefixWhitespace(rawBody, index)
		if index >= len(rawBody) {
			return ""
		}
		if rawBody[index] == 0x7d {
			return ""
		}
		if rawBody[index] == 0x2c {
			index++
			continue
		}
		key := readJSONPrefixString(rawBody, index)
		if key == nil {
			return ""
		}
		index = skipJSONPrefixWhitespace(rawBody, key.nextIndex)
		if index >= len(rawBody) || rawBody[index] != 0x3a {
			return ""
		}
		index = skipJSONPrefixWhitespace(rawBody, index+1)
		if key.value == "id" {
			value := readJSONPrefixString(rawBody, index)
			if value == nil {
				return ""
			}
			return normalizeInteractionID(value.value)
		}
		nextIndex := skipJSONPrefixValue(rawBody, index)
		if nextIndex == nil {
			return ""
		}
		index = *nextIndex
	}
	return ""
}

func skipJSONPrefixWhitespace(rawBody []byte, index int) int {
	for index < len(rawBody) {
		switch rawBody[index] {
		case 0x20, 0x09, 0x0a, 0x0d:
			index++
			continue
		}
		break
	}
	return index
}

func readJSONPrefixString(rawBody []byte, index int) *jsonPrefixString {
	if index >= len(rawBody) || rawBody[index] != 0x22 {
		return nil
	}
	escaped := false
	for cursor := index + 1; cursor < len(rawBody); cursor++ {
		byteValue := rawBody[cursor]
		if escaped {
			escaped = false
			continue
		}
		if byteValue == 0x5c {
			escaped = true
			continue
		}
		if byteValue != 0x22 {
			continue
		}
		nextIndex := cursor + 1
		var value string
		if err := json.Unmarshal(rawBody[index:nextIndex], &value); err != nil {
			return nil
		}
		return &jsonPrefixString{value: value, nextIndex: nextIndex}
	}
	return nil
}

type jsonPrefixString struct {
	value     string
	nextIndex int
}

func skipJSONPrefixValue(rawBody []byte, index int) *int {
	index = skipJSONPrefixWhitespace(rawBody, index)
	if index < len(rawBody) && rawBody[index] == 0x22 {
		if value := readJSONPrefixString(rawBody, index); value != nil {
			return &value.nextIndex
		}
		return nil
	}
	if index >= len(rawBody) {
		return nil
	}
	firstByte := rawBody[index]
	if firstByte != 0x7b && firstByte != 0x5b {
		for index < len(rawBody) {
			byteValue := rawBody[index]
			if byteValue == 0x2c || byteValue == 0x7d || byteValue == 0x5d {
				break
			}
			index++
		}
		return &index
	}
	stack := []byte{firstByte}
	for cursor := index + 1; cursor < len(rawBody); cursor++ {
		byteValue := rawBody[cursor]
		if byteValue == 0x22 {
			stringValue := readJSONPrefixString(rawBody, cursor)
			if stringValue == nil {
				return nil
			}
			cursor = stringValue.nextIndex - 1
			continue
		}
		if byteValue == 0x7b || byteValue == 0x5b {
			stack = append(stack, byteValue)
			continue
		}
		if byteValue != 0x7d && byteValue != 0x5d {
			continue
		}
		openByte := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if (byteValue == 0x7d && openByte != 0x7b) || (byteValue == 0x5d && openByte != 0x5b) {
			return nil
		}
		if len(stack) == 0 {
			next := cursor + 1
			return &next
		}
	}
	return nil
}
