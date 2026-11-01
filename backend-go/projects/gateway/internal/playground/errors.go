package playground

// 演练场错误面（设计 §3.3 错误码 + chat routes.go writeChatRouteError 的
// HTTP 错误形状同款）。核实记录：chat 面 HTTP 错误载荷为扁平
// {"message":...,"code":...}（kernel.WriteJSON(messageCodePayload)），并非
// {"error":{...}} 嵌套；演练场沿同一形状，前端按 code/message 分支。

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// 演练场错误码（设计 §3.3；codeEndpointDegraded 是装配端口缺失时的显式降级
// 码，设计 §1.1「功能不设开关」——未接线返回错误而非静默空行为）。
// codeArtifactNotFound / codeArtifactStoreFailed 是产物暂存面（§3.8）的专码：
// content 定位失败 404、暂存读写失败 500，错误载荷仍为扁平 {message, code}。
const (
	codeInvalidRequest      = "playground_invalid_request"
	codeAccountNotFound     = "playground_account_not_found"
	codeModelNotAvailable   = "playground_model_not_available"
	codeDispatchFailed      = "playground_dispatch_failed"
	codeEndpointDegraded    = "playground_endpoint_unavailable"
	codeArtifactNotFound    = "playground_artifact_not_found"
	codeArtifactStoreFailed = "playground_artifact_store_failed"
)

// HTTPError 是演练场路由的带码 HTTP 错误（状态码 + code + 中文 message）。
type HTTPError struct {
	Status  int
	Code    string
	Message string
}

func (e *HTTPError) Error() string { return e.Message }

func invalidRequest(message string) *HTTPError {
	return &HTTPError{Status: http.StatusBadRequest, Code: codeInvalidRequest, Message: message}
}

func modelNotAvailable(message string) *HTTPError {
	return &HTTPError{Status: http.StatusBadRequest, Code: codeModelNotAvailable, Message: message}
}

func accountNotFound() *HTTPError {
	return &HTTPError{Status: http.StatusNotFound, Code: codeAccountNotFound, Message: "账户不存在或不在当前用户可见范围内"}
}

func dispatchFailed(message string) *HTTPError {
	return &HTTPError{Status: http.StatusBadGateway, Code: codeDispatchFailed, Message: message}
}

// writeHTTPError 渲染扁平 {message, code} JSON（chat messageCodePayload 同款，
// 经 kernel.WriteJSON 复用既有错误本地化通道）。
func writeHTTPError(w http.ResponseWriter, err *HTTPError) {
	kernel.WriteJSON(w, err.Status, messageCodePayload{Message: err.Message, Code: err.Code})
}

// messageCodePayload 与 chat 面 writeChatRouteError 的载荷同形。
type messageCodePayload struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// upstreamDispatchMessage 把上游非 2xx 收敛为带状态码摘要的 message（设计
// §3.3：playground_dispatch_failed 的 message 带上游状态码摘要；错误体里的
// message 尽力透出，保留原始上游语义）。
func upstreamDispatchMessage(scene string, status int, body []byte) string {
	if message := upstreamErrorMessage(body); message != "" {
		return scene + "请求失败（上游 HTTP " + strconv.Itoa(status) + "）: " + message
	}
	return scene + "请求失败（上游 HTTP " + strconv.Itoa(status) + "）"
}

// upstreamErrorMessage 尽力从上游错误体提取 message 字段（openai 形态
// {"error":{"message":...}} 或顶层 {"message":...}），提取不到返回空串。
func upstreamErrorMessage(body []byte) string {
	var payload struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if payload.Error != nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	return payload.Message
}
