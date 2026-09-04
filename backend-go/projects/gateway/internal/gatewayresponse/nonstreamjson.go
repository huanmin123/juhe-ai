package gatewayresponse

import (
	"encoding/json"
	"net/http"
	"strings"
)

// GatewayNonStreamJsonBody 对齐 non-stream-json-body.ts 的 union。
type GatewayNonStreamJsonBody struct {
	// Status is 'valid' | 'empty' | 'not_json' | 'invalid'.
	Status string
	Value  any
}

// GatewayNonStreamJsonBodyStatus 常量。
const (
	NonStreamJSONStatusValid   = "valid"
	NonStreamJSONStatusEmpty   = "empty"
	NonStreamJSONStatusNotJSON = "not_json"
	NonStreamJSONStatusInvalid = "invalid"
)

// ParseGatewayNonStreamJsonBody 对齐 parseGatewayNonStreamJsonBody。
func ParseGatewayNonStreamJsonBody(bodyText string, hasBodyText bool, header http.Header) GatewayNonStreamJsonBody {
	if !hasBodyText {
		return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusEmpty}
	}
	trimmed := strings.TrimSpace(bodyText)
	if trimmed == "" {
		return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusEmpty}
	}
	contentType := ""
	if header != nil {
		contentType = strings.ToLower(header.Get("Content-Type"))
	}
	if !strings.Contains(contentType, "json") && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusNotJSON}
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusInvalid}
	}
	return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: value}
}

// IsOpenAIJsonResponseContentType 对齐 isOpenAIJsonResponseContentType
//（responses.ts；Node 在 finalization / non-stream 管道同样使用）。
func IsOpenAIJsonResponseContentType(contentType string) bool {
	mimeType := responseMimeTypeOf(contentType)
	return mimeType == "application/json" || strings.HasSuffix(mimeType, "+json")
}

// IsOpenAIBinaryResponseContentType 对齐 isOpenAIBinaryResponseContentType。
func IsOpenAIBinaryResponseContentType(contentType string) bool {
	mimeType := responseMimeTypeOf(contentType)
	return mimeType == "application/octet-stream" ||
		strings.HasPrefix(mimeType, "image/") ||
		strings.HasPrefix(mimeType, "audio/") ||
		strings.HasPrefix(mimeType, "video/") ||
		binaryApplicationMimeTypes[mimeType]
}

// ShouldHandleOpenAIUpstreamResponseAsStream 对齐
// shouldHandleOpenAIUpstreamResponseAsStream。
func ShouldHandleOpenAIUpstreamResponseAsStream(contentType string, streamRequest bool) bool {
	if IsOpenAIStreamContentType(contentType) {
		return true
	}
	if !streamRequest {
		return false
	}
	if IsOpenAIJsonResponseContentType(contentType) {
		return false
	}
	if IsOpenAIBinaryResponseContentType(contentType) {
		return false
	}
	return true
}

func responseMimeTypeOf(contentType string) string {
	part, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(part))
}

var binaryApplicationMimeTypes = map[string]bool{
	"application/pdf":               true,
	"application/zip":               true,
	"application/x-zip-compressed": true,
	"application/gzip":              true,
	"application/x-gzip":            true,
	"application/x-tar":             true,
	"application/x-7z-compressed":   true,
}

// ---- 协议校验（non-stream-json-inspection.ts）----

// ProtocolStructureFailure 对齐 protocolStructureFailure。
func ProtocolStructureFailure(message string) ProtocolFailure {
	return ProtocolFailure{Message: message, ErrorCode: "upstream_protocol_error"}
}

// ProtocolFailure 对齐 validateBufferedJsonProtocolResponse 的失败形状。
type ProtocolFailure struct {
	Message   string
	ErrorCode string
}

// ValidateBufferedJsonProtocolResponse 对齐 validateBufferedJsonProtocolResponse
// 的结构校验主体。endpointFamily 由调用方按请求+账户解析；
// protocolValidationLimitExceeded 对齐同名入参。
func ValidateBufferedJsonProtocolResponse(parsedJsonBody GatewayNonStreamJsonBody, upstreamOK bool, protocolValidationLimitExceeded bool, endpointFamily string, requestPath string) *ProtocolFailure {
	if !upstreamOK {
		return nil
	}
	if protocolValidationLimitExceeded {
		return &ProtocolFailure{
			Message:   "上游成功响应超过网关协议验证上限，已拒绝透传未验证正文",
			ErrorCode: "upstream_protocol_error",
		}
	}
	if parsedJsonBody.Status != NonStreamJSONStatusValid {
		return &ProtocolFailure{
			Message:   "上游成功响应不是有效 JSON，无法满足请求协议",
			ErrorCode: "upstream_protocol_error",
		}
	}
	root, ok := parsedJsonBody.Value.(map[string]any)
	if !ok {
		return &ProtocolFailure{
			Message:   "上游 JSON 响应根节点无效，无法满足请求协议",
			ErrorCode: "upstream_protocol_error",
		}
	}
	resourceResponse := isManagementResourceResponsePath(requestPath)
	responseError, hasResponseError := plainObject(root["error"])
	if !resourceResponse && endpointFamily != "responses" && (hasResponseError || root["type"] == "error" || root["status"] == "failed") {
		upstreamMessage := ""
		if hasResponseError {
			if message, ok := responseError["message"].(string); ok {
				upstreamMessage = strings.TrimSpace(message)
			}
		}
		if upstreamMessage != "" {
			return &ProtocolFailure{
				Message:   "上游成功 HTTP 响应包含失败终态：" + upstreamMessage,
				ErrorCode: "upstream_protocol_error",
			}
		}
		return &ProtocolFailure{
			Message:   "上游成功 HTTP 响应包含失败终态，无法满足请求协议",
			ErrorCode: "upstream_protocol_error",
		}
	}
	switch endpointFamily {
	case "chat_completions":
		choices, _ := root["choices"].([]any)
		hasValid := false
		for _, choiceValue := range choices {
			if isValidChatCompletionChoice(choiceValue) {
				hasValid = true
				break
			}
		}
		if choices == nil || !hasValid {
			return &ProtocolFailure{
				Message:   "上游 Chat JSON 响应结构无效：choices 必须包含 message 或 text",
				ErrorCode: "upstream_protocol_error",
			}
		}
	case "messages":
		content, _ := root["content"].([]any)
		if root["type"] != "message" || content == nil || len(content) == 0 {
			return &ProtocolFailure{
				Message:   "上游 Anthropic Messages JSON 响应结构无效：content 必须是非空数组",
				ErrorCode: "upstream_protocol_error",
			}
		}
	case "models":
		_, hasData := root["data"].([]any)
		_, hasModels := root["models"].([]any)
		_, nameIsString := root["name"].(string)
		if !hasData && !hasModels && root["object"] != "model" && !nameIsString {
			return &ProtocolFailure{
				Message:   "上游 Models JSON 响应结构无效：缺少 data、models、model 或 name",
				ErrorCode: "upstream_protocol_error",
			}
		}
	case "message_token_counting":
		if _, ok := root["input_tokens"].(float64); !ok {
			return protocolStructureFailurePtr("上游 Anthropic Token Counting JSON 响应结构无效：缺少 input_tokens")
		}
	case "generate_content", "stream_generate_content":
		_, hasCandidates := root["candidates"].([]any)
		_, hasPromptFeedback := plainObject(root["promptFeedback"])
		if !hasCandidates && !hasPromptFeedback {
			return protocolStructureFailurePtr("上游 Gemini Generate Content JSON 响应结构无效：缺少 candidates 或 promptFeedback")
		}
	case "count_tokens":
		if _, ok := root["totalTokens"].(float64); !ok {
			return protocolStructureFailurePtr("上游 Gemini Count Tokens JSON 响应结构无效：缺少 totalTokens")
		}
	case "embed_content":
		_, hasEmbedding := plainObject(root["embedding"])
		_, hasEmbeddings := root["embeddings"].([]any)
		if !hasEmbedding && !hasEmbeddings {
			return protocolStructureFailurePtr("上游 Gemini Embed Content JSON 响应结构无效：缺少 embedding 或 embeddings")
		}
	case "interactions":
		_, idIsString := root["id"].(string)
		_, nameIsString := root["name"].(string)
		if !idIsString && !nameIsString {
			return protocolStructureFailurePtr("上游 Gemini Interactions JSON 响应结构无效：缺少 id 或 name")
		}
	case "unknown":
		if (strings.Contains(requestPath, "/embeddings") || strings.Contains(requestPath, "/images")) {
			if _, ok := root["data"].([]any); !ok {
				return protocolStructureFailurePtr("上游 JSON 响应结构无效：data 必须是数组")
			}
		}
		if strings.Contains(requestPath, "/moderations") {
			if _, ok := root["results"].([]any); !ok {
				return protocolStructureFailurePtr("上游 Moderations JSON 响应结构无效：results 必须是数组")
			}
		}
		if audioPathPattern.MatchString(requestPath) {
			if _, ok := root["text"].(string); !ok {
				return protocolStructureFailurePtr("上游 Audio JSON 响应结构无效：缺少 text")
			}
		}
		if managementPrefixPattern.MatchString(requestPath) || filesRootPattern.MatchString(requestPath) {
			_, idIsString := root["id"].(string)
			_, hasData := root["data"].([]any)
			if !idIsString && !hasData {
				return protocolStructureFailurePtr("上游管理接口 JSON 响应结构无效：缺少 id 或 data")
			}
		}
	case "responses":
		if root["status"] == "failed" {
			upstreamMessage := ""
			if errorObject, ok := plainObject(root["error"]); ok {
				if message, ok := errorObject["message"].(string); ok {
					upstreamMessage = strings.TrimSpace(message)
				}
			}
			if upstreamMessage != "" {
				return &ProtocolFailure{
					Message:   "上游 Responses 返回失败终态：" + upstreamMessage,
					ErrorCode: "upstream_protocol_failure",
				}
			}
			return &ProtocolFailure{
				Message:   "上游 Responses 返回失败终态",
				ErrorCode: "upstream_protocol_failure",
			}
		}
		_, hasOutput := root["output"].([]any)
		_, idIsString := root["id"].(string)
		if (root["object"] != "response" && root["type"] != "response") || !idIsString || !hasOutput {
			return &ProtocolFailure{
				Message:   "上游 Responses JSON 响应结构无效，缺少 response、id 或 output",
				ErrorCode: "upstream_protocol_error",
			}
		}
	}
	return nil
}

func protocolStructureFailurePtr(message string) *ProtocolFailure {
	failure := ProtocolStructureFailure(message)
	return &failure
}

// ProtocolValidatedNonStreamResponse 对齐 protocolValidatedNonStreamResponse。
func ProtocolValidatedNonStreamResponse(parsedJsonBody GatewayNonStreamJsonBody, statusCode int, endpointFamily string, requestPath string) bool {
	if statusCode < 200 || statusCode >= 300 {
		return false
	}
	if parsedJsonBody.Status != NonStreamJSONStatusValid {
		return false
	}
	root, ok := parsedJsonBody.Value.(map[string]any)
	if !ok {
		return false
	}
	if !isManagementResourceResponsePath(requestPath) {
		_, hasError := plainObject(root["error"])
		if hasError || root["type"] == "error" || root["status"] == "failed" {
			return false
		}
	}
	if requestPath == "/models" || requestPath == "/v1/models" || requestPath == "/v1beta/models" {
		_, hasData := root["data"].([]any)
		_, hasModels := root["models"].([]any)
		_, nameIsString := root["name"].(string)
		return hasData || hasModels || root["object"] == "model" || nameIsString
	}
	switch endpointFamily {
	case "chat_completions":
		choices, _ := root["choices"].([]any)
		for _, choiceValue := range choices {
			choice, ok := plainObject(choiceValue)
			if !ok {
				continue
			}
			if _, hasError := plainObject(choice["error"]); hasError {
				continue
			}
			if message, hasMessage := plainObject(choice["message"]); hasMessage {
				if _, messageHasError := plainObject(message["error"]); !messageHasError {
					return true
				}
				continue
			}
			if _, textIsString := choice["text"].(string); textIsString {
				return true
			}
		}
		return false
	case "responses":
		_, hasOutput := root["output"].([]any)
		_, idIsString := root["id"].(string)
		return (root["object"] == "response" || root["type"] == "response") &&
			idIsString && hasOutput && root["status"] != "failed"
	case "messages":
		_, hasContent := root["content"].([]any)
		return root["type"] == "message" && hasContent
	case "models":
		_, hasData := root["data"].([]any)
		_, hasModels := root["models"].([]any)
		_, nameIsString := root["name"].(string)
		return hasData || hasModels || root["object"] == "model" || nameIsString
	case "message_token_counting":
		_, ok := root["input_tokens"].(float64)
		return ok
	case "generate_content", "stream_generate_content":
		_, hasCandidates := root["candidates"].([]any)
		_, hasPromptFeedback := plainObject(root["promptFeedback"])
		return hasCandidates || hasPromptFeedback
	case "count_tokens":
		_, ok := root["totalTokens"].(float64)
		return ok
	case "embed_content":
		_, hasEmbedding := plainObject(root["embedding"])
		_, hasEmbeddings := root["embeddings"].([]any)
		return hasEmbedding || hasEmbeddings
	case "interactions":
		_, idIsString := root["id"].(string)
		_, nameIsString := root["name"].(string)
		return idIsString || nameIsString
	case "unknown":
		if strings.Contains(requestPath, "/embeddings") || strings.Contains(requestPath, "/images") {
			_, ok := root["data"].([]any)
			return ok
		}
		if strings.Contains(requestPath, "/moderations") {
			_, ok := root["results"].([]any)
			return ok
		}
		if audioPathPattern.MatchString(requestPath) {
			_, ok := root["text"].(string)
			return ok
		}
		if managementPrefixPattern.MatchString(requestPath) || filesRootPattern.MatchString(requestPath) {
			_, idIsString := root["id"].(string)
			_, hasData := root["data"].([]any)
			return idIsString || hasData
		}
		return false
	}
	return false
}

func plainObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func isValidChatCompletionChoice(value any) bool {
	choice, ok := plainObject(value)
	if !ok {
		return false
	}
	if _, hasError := plainObject(choice["error"]); hasError {
		return false
	}
	if message, hasMessage := plainObject(choice["message"]); hasMessage {
		_, messageHasError := plainObject(message["error"])
		return !messageHasError
	}
	_, textIsString := choice["text"].(string)
	return textIsString
}

func isManagementResourceResponsePath(requestPath string) bool {
	return managementPrefixPattern.MatchString(requestPath) || filesRootPattern.MatchString(requestPath)
}

// IsGatewayGeneratedResponsesFailure 对齐 isGatewayGeneratedResponsesFailure。
func IsGatewayGeneratedResponsesFailure(parsedJson any, endpointFamily string) bool {
	root, ok := plainObject(parsedJson)
	if !ok {
		return false
	}
	if root["status"] != "failed" {
		return false
	}
	if endpointFamily != "responses" {
		return false
	}
	metadata, ok := plainObject(root["metadata"])
	if !ok {
		return false
	}
	generated, _ := metadata["gateway_generated_failure"].(bool)
	return generated
}

// IsCodexResponsesCyberPolicyFailedJSON 对齐
// isCodexResponsesCyberPolicyFailedJson。clientProfile 由调用方按
// clientStrategy ?? defaultClientProfile 解析。
func IsCodexResponsesCyberPolicyFailedJSON(upstreamStatus int, endpointFamily string, clientProfile string, parsedJSON any) bool {
	return isCodexResponsesNonRetryableFailedJSON(upstreamStatus, endpointFamily, clientProfile, parsedJSON, map[string]bool{"cyber_policy": true})
}

func isCodexResponsesNonRetryableFailedJSON(upstreamStatus int, endpointFamily string, clientProfile string, parsedJSON any, errorCodes map[string]bool) bool {
	if upstreamStatus >= 200 && upstreamStatus < 300 {
		return false
	}
	if endpointFamily != "responses" {
		return false
	}
	if clientProfile != "codex" {
		return false
	}
	root, ok := plainObject(parsedJSON)
	if !ok {
		return false
	}
	errorObject, hasError := plainObject(root["error"])
	if !hasError {
		return false
	}
	code, isString := errorObject["code"].(string)
	if !isString {
		return false
	}
	return (root["status"] == "failed" || root["status"] == nil) && errorCodes[code]
}
