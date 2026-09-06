package internalapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"
)

// account-health-check-dispatch 接线，对齐网关桥
// cmd/juhe-ai-gateway/chain_request_failure_health.go 的发布契约：
//   - 路径 POST /__aiinternal__/v1/account-health-check/dispatch（loopback 专用）；
//   - HMAC-SHA256 over "juhe-ai:account-health-check-dispatch:v1\n" + raw body，
//     X-Juhe-Ai-Signature: v1=<hex>；
//   - payload {version:1, accountId, reason, traceId?, sourceFence?}，字段名与
//     J1 request file 的 source_fence 窄投影一致（snake_case）；
//   - 消费复用本包 DispatchAccountHealthCheckWithOutcome（Node
//     internal-api/account-health-check-dispatch.service.ts 的发布语义：账户在
//     J1 冻结范围内发布 signed request file，范围外静默跳过并结算 source
//     fence = unknown），由 J1 Runner 消费 request 文件。
//
// 响应映射（网关桥只区分 202 与非 202）：
//   - 202 queued（body 为 outcome JSON）；
//   - 400 payload 无效 / dispatch_rejected；
//   - 401 认证失败；403 非 loopback；415 非 JSON / 压缩请求体；413 过大；
//   - 503 input_unavailable（派发依赖未装配）；
//   - 500 依赖或发布错误。

const (
	// HealthCheckDispatchSignatureDomain 是健康检查派发签名域分隔符（含尾部 \n）。
	HealthCheckDispatchSignatureDomain = "juhe-ai:account-health-check-dispatch:v1\n"
	// HealthCheckDispatchPath 是 /__aiinternal__ 前缀下的路由。
	HealthCheckDispatchPath = "/v1/account-health-check/dispatch"
)

// FullHealthCheckDispatchPath 返回挂在 loopback mux 上的完整路径
// （/__aiinternal__ 前缀 + v1 路由，供组合根路由分发）。
func FullHealthCheckDispatchPath() string {
	return AccountTestDispatchInternalPrefix + HealthCheckDispatchPath
}

// CreateHealthCheckDispatchSignature 对齐网关桥 signChainHealthDispatch：
// 签名域为 HealthCheckDispatchSignatureDomain。
func CreateHealthCheckDispatchSignature(secret string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(HealthCheckDispatchSignatureDomain))
	_, _ = mac.Write(rawBody)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// HealthCheckDispatchFunc 派发回调；组合根绑定
// DispatchAccountHealthCheckWithOutcome（注入 Boundary/source fence 结算/J1
// 输入配置）。返回 error 表示依赖或发布错误（500）。
type HealthCheckDispatchFunc func(ctx context.Context, accountID, reason, traceID string, sourceFence *HealthCheckSourceFence) (HealthCheckDispatchOutcome, error)

// HealthCheckDispatchRouterOptions 组合根配置；Dispatch 为 nil 表示派发能力
// 未装配（恒 503，不伪装受理）。
type HealthCheckDispatchRouterOptions struct {
	Secret   string
	Dispatch HealthCheckDispatchFunc
}

// NewHealthCheckDispatchHandler 返回挂在 std http.ServeMux 上的 handler。
func NewHealthCheckDispatchHandler(options HealthCheckDispatchRouterOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case AccountTestDispatchInternalPrefix + HealthCheckDispatchPath:
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			handleHealthCheckDispatch(w, r, options)
		default:
			http.NotFound(w, r)
		}
	})
}

func handleHealthCheckDispatch(w http.ResponseWriter, r *http.Request, options HealthCheckDispatchRouterOptions) {
	w.Header().Set("Cache-Control", "no-store")
	if !IsLoopbackRemoteAddress(r.RemoteAddr) {
		writeJSONError(w, http.StatusForbidden, "禁止访问")
		return
	}
	if !requireJSONContentType(r) {
		writeJSONError(w, http.StatusUnsupportedMediaType, "仅支持 JSON 请求")
		return
	}
	if !requireIdentityContentEncoding(r) {
		writeJSONError(w, http.StatusUnsupportedMediaType, "不支持压缩请求体")
		return
	}
	rawBody, bodyErr := readRawBody(r)
	if bodyErr != nil {
		if errors.Is(bodyErr, errBodyTooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "请求体过大")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "请求体无效")
		return
	}
	if !hasValidSignatureWithDomain(r, options.Secret, rawBody, HealthCheckDispatchSignatureDomain) {
		writeJSONError(w, http.StatusUnauthorized, "认证失败")
		return
	}
	accountID, reason, traceID, sourceFence, ok := parseHealthCheckDispatchPayload(rawBody)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	if options.Dispatch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "服务暂不可用")
		return
	}
	outcome, dispatchErr := options.Dispatch(r.Context(), accountID, reason, traceID, sourceFence)
	if dispatchErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	switch {
	case outcome.Outcome == "queued":
		writeJSON(w, http.StatusAccepted, outcome)
	case outcome.DecisionCode == "input_unavailable":
		writeJSON(w, http.StatusServiceUnavailable, outcome)
	default:
		writeJSON(w, http.StatusBadRequest, outcome)
	}
}

// parseHealthCheckDispatchPayload 校验并提取派发 payload。字段校验与
// buildProbeRequestPayload 的 fence 断言一致（正整数、非空文本、fence 账户
// 与顶层 accountId 一致），把无争议的请求错误前置为 400，剩余 revision 一致
// 性判定（依赖账户事实）留在派发服务内。
func parseHealthCheckDispatchPayload(rawBody []byte) (accountID, reason, traceID string, sourceFence *HealthCheckSourceFence, ok bool) {
	// Node's TextDecoder(..., { fatal: true }) + JSON.parse reject malformed
	// UTF-8 and any trailing JSON value. Keep the same wire boundary before
	// extracting fields so a signed body cannot carry an ambiguous payload.
	if !utf8.Valid(rawBody) {
		return "", "", "", nil, false
	}
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	if err := decoder.Decode(&parsed); err != nil {
		return "", "", "", nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", "", "", nil, false
	}
	record, recordOK := parsed.(map[string]any)
	if !recordOK {
		return "", "", "", nil, false
	}
	if version, exists := record["version"]; !exists || version != float64(1) {
		return "", "", "", nil, false
	}
	accountID, ok = requiredText(record, "accountId")
	if !ok {
		return "", "", "", nil, false
	}
	reason, ok = requiredText(record, "reason")
	if !ok {
		return "", "", "", nil, false
	}
	if value, exists := record["traceId"]; exists && value != nil {
		text, isText := value.(string)
		if !isText {
			return "", "", "", nil, false
		}
		traceID = strings.TrimSpace(text)
	}
	fenceValue, exists := record["sourceFence"]
	if !exists || fenceValue == nil {
		return accountID, reason, traceID, nil, true
	}
	fenceRecord, isObject := fenceValue.(map[string]any)
	if !isObject {
		return "", "", "", nil, false
	}
	stateKey, ok := requiredText(fenceRecord, "state_key")
	if !ok {
		return "", "", "", nil, false
	}
	fenceAccountID, ok := requiredText(fenceRecord, "account_id")
	if !ok || fenceAccountID != accountID {
		return "", "", "", nil, false
	}
	sourceFenceID, ok := requiredText(fenceRecord, "source_fence_id")
	if !ok {
		return "", "", "", nil, false
	}
	runtimeKey, ok := requiredText(fenceRecord, "runtime_key")
	if !ok {
		return "", "", "", nil, false
	}
	sourceGeneration, ok := positiveNumber(fenceRecord, "source_generation")
	if !ok {
		return "", "", "", nil, false
	}
	probeGeneration, ok := positiveNumber(fenceRecord, "probe_generation")
	if !ok {
		return "", "", "", nil, false
	}
	configRevision, ok := positiveNumber(fenceRecord, "config_revision")
	if !ok {
		return "", "", "", nil, false
	}
	return accountID, reason, traceID, &HealthCheckSourceFence{
		StateKey:         stateKey,
		AccountID:        fenceAccountID,
		SourceGeneration: sourceGeneration,
		SourceFenceID:    sourceFenceID,
		RuntimeKey:       runtimeKey,
		ProbeGeneration:  probeGeneration,
		ConfigRevision:   configRevision,
	}, true
}

func requiredText(record map[string]any, key string) (string, bool) {
	value, exists := record[key]
	if !exists {
		return "", false
	}
	text, isText := value.(string)
	if !isText {
		return "", false
	}
	normalized := strings.TrimSpace(text)
	return normalized, normalized != ""
}

func positiveNumber(record map[string]any, key string) (int64, bool) {
	value, exists := record[key]
	if !exists {
		return 0, false
	}
	number, isNumber := value.(float64)
	if !isNumber || number < 1 || number != math.Trunc(number) || number > math.MaxInt64 {
		return 0, false
	}
	return int64(number), true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
