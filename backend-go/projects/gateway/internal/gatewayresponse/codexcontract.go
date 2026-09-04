package gatewayresponse

import (
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// Codex Remote Compaction V2 契约（codex-compaction-contract.ts 的契约部分：
// 计数、失配帧与请求判定）。codex 桥（G18）通过本包导出的函数消费。

// CodexCompactionContractMismatchErrorCode 对齐
// codexCompactionContractMismatchErrorCode。
const CodexCompactionContractMismatchErrorCode = "codex_compaction_contract_mismatch"

// CodexCompactionContractCounts 对齐 CodexCompactionContractCounts。
type CodexCompactionContractCounts struct {
	OutputItemCount     int
	CompactionItemCount int
}

// CodexCompactionContractMismatchInput 对齐 CodexCompactionContractMismatchInput。
type CodexCompactionContractMismatchInput struct {
	OutputItemCount     int
	CompactionItemCount int
	Transport           string // 'json' | 'sse'
	EventType           string
	Force               bool
	Message             string
}

// codexCompactionRawBodyScanEdgeBytes 对齐同名常量。
const codexCompactionRawBodyScanEdgeBytes = 64 * 1024

// codexCompactionRequestSearchPattern 对齐
// /"type"\s*:\s*"compaction_trigger"/。
var codexCompactionRequestSearchPattern = regexp.MustCompile(`"type"\s*:\s*"compaction_trigger"`)

// CodexCompactionExpectedForRequest 对齐 codexCompactionExpectedForRequest。
// 请求体状态经由 gatewaypreauth.GatewayRequest（gatewaybody 承载扫描结果）。
func CodexCompactionExpectedForRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "POST" {
		return false
	}
	normalizedPath := normalizedOpenAIRequestPath(req)
	if normalizedPath == "/responses/compact" {
		return true
	}
	return normalizedPath == "/responses" && requestBodyHasCompactionTrigger(req)
}

func requestBodyHasCompactionTrigger(req *gatewaypreauth.GatewayRequest) bool {
	if parsedBody := req.ParsedJSONObjectBody(); parsedBody != nil {
		if jsonValueHasCompactionTrigger(parsedBody, 0) {
			return true
		}
	}
	bodyState := req.BodyState()
	if bodyState != nil && bodyState.CodexCompactionTrigger {
		return true
	}
	if bodyState != nil && bodyState.JSONParseStatus == gatewaybody.JSONParseStatusScannedJSON {
		return false
	}
	// 原始正文兜底扫描由 gatewaybody 的 BodyState 承担（rawBody 未在
	// GatewayRequest 视图导出）；此处保持 Node 的“其余情况 false”语义。
	return false
}

// jsonValueHasCompactionTrigger 对齐 jsonValueHasCompactionTrigger：有限深度
// 与广度的对象图扫描。encoding/json 解码产物不存在环，无需 Node 的 WeakSet。
func jsonValueHasCompactionTrigger(value any, depth int) bool {
	if depth > 8 {
		return false
	}
	switch typed := value.(type) {
	case []any:
		limit := len(typed)
		if limit > 500 {
			limit = 500
		}
		for index := 0; index < limit; index++ {
			if jsonValueHasCompactionTrigger(typed[index], depth+1) {
				return true
			}
		}
		return false
	case map[string]any:
		if typed == nil {
			return false
		}
		if typeField, exists := typed["type"]; exists {
			if s, isString := typeField.(string); isString && s == "compaction_trigger" {
				return true
			}
		}
		visited := 0
		for _, child := range typed {
			if visited >= 200 {
				break
			}
			visited++
			if jsonValueHasCompactionTrigger(child, depth+1) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// CodexCompactionContractMismatchFrame 对齐
// codexCompactionContractMismatchFrame。
func CodexCompactionContractMismatchFrame(input CodexCompactionContractMismatchInput) *gatewayproto.SemanticFrame {
	if !input.Force && input.CompactionItemCount == 1 {
		return nil
	}
	rawText := ""
	if input.Transport == "sse" {
		rawText = input.EventType
	}
	message := input.Message
	if message == "" {
		message = "Codex Remote Compaction V2 响应结构无效：期望恰好 1 个 compaction output item，实际 " +
			itoa(int64(input.CompactionItemCount)) + " 个，output item 总数 " +
			itoa(int64(input.OutputItemCount)) + " 个"
	}
	return &gatewayproto.SemanticFrame{
		FrameType:      gatewayproto.FrameTypeError,
		Protocol:       "openai_v1",
		EndpointFamily: gatewayproto.EndpointFamilyResponses,
		Transport:      gatewayproto.ResponseTransport(input.Transport),
		ErrorCode:      CodexCompactionContractMismatchErrorCode,
		ErrorType:      "invalid_response_contract",
		ErrorMessage:   message,
		RawJSONPaths:   []string{"output"},
		RawText:        rawText,
		EventType:      input.EventType,
	}
}

// CountCodexCompactionOutputItemsFromJSON 对齐
// countCodexCompactionOutputItemsFromJson。
func CountCodexCompactionOutputItemsFromJSON(value any) *CodexCompactionContractCounts {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	output, ok := root["output"].([]any)
	if !ok {
		return nil
	}
	return countCodexCompactionOutputItems(output)
}

// CountCodexCompactionOutputItemsFromStreamEvent 对齐
// countCodexCompactionOutputItemsFromStreamEvent。
func CountCodexCompactionOutputItemsFromStreamEvent(event gatewayopenai.ParsedStreamEvent) *CodexCompactionContractCounts {
	if event.EventType != "response.output_item.done" && event.EventName != "response.output_item.done" {
		return nil
	}
	item, _ := event.Data["item"].(map[string]any)
	counts := &CodexCompactionContractCounts{OutputItemCount: 1}
	if isCodexDeserializableCompactionItem(item) {
		counts.CompactionItemCount = 1
	}
	return counts
}

func countCodexCompactionOutputItems(output []any) *CodexCompactionContractCounts {
	compactionItemCount := 0
	for _, item := range output {
		object, _ := item.(map[string]any)
		if isCodexDeserializableCompactionItem(object) {
			compactionItemCount++
		}
	}
	return &CodexCompactionContractCounts{
		OutputItemCount:     len(output),
		CompactionItemCount: compactionItemCount,
	}
}

func isCodexDeserializableCompactionItem(item map[string]any) bool {
	if item == nil {
		return false
	}
	if item["type"] != "compaction" && item["type"] != "compaction_summary" {
		return false
	}
	_, isString := item["encrypted_content"].(string)
	return isString
}

func normalizedOpenAIRequestPath(req *gatewaypreauth.GatewayRequest) string {
	rawPath := req.Path()
	if rawPath == "" {
		rawPath = "/"
	}
	return normalizeV1PrefixPath(rawPath)
}

// 供非流式 JSON 检查复用的 compaction 触发判定辅助。
func requestPathHasCompactionTrigger(pathAndQuery string, body *gatewaybody.BodyState, rawBody []byte) bool {
	normalized := normalizeV1PrefixPath(strings.ToLower(splitPathOnly(pathAndQuery)))
	if normalized != "/responses" {
		return false
	}
	if body != nil && body.CodexCompactionTrigger {
		return true
	}
	if body != nil && body.JSONParseStatus == gatewaybody.JSONParseStatusScannedJSON {
		return false
	}
	if len(rawBody) == 0 {
		return false
	}
	if len(rawBody) <= codexCompactionRawBodyScanEdgeBytes*2 {
		return codexCompactionRequestSearchPattern.Match(rawBody)
	}
	prefix := rawBody[:codexCompactionRawBodyScanEdgeBytes]
	if codexCompactionRequestSearchPattern.Match(prefix) {
		return true
	}
	tailStart := len(rawBody) - codexCompactionRawBodyScanEdgeBytes
	if tailStart < 0 {
		tailStart = 0
	}
	return codexCompactionRequestSearchPattern.Match(rawBody[tailStart:])
}

func splitPathOnly(pathAndQuery string) string {
	if index := strings.Index(pathAndQuery, "?"); index >= 0 {
		return pathAndQuery[:index]
	}
	return pathAndQuery
}
