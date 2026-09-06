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
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// internal-api 派发接口，逐字节对齐 Node
// modules/internal-api/account-test-dispatch.routes.ts：
//   - 路径 POST /__aiinternal__/v1/account-test/dispatch；
//   - HMAC-SHA256 + loopback；签名域 juhe-ai:account-test-dispatch:v1\n；
//   - 校验顺序与错误文案/状态码与 Node 完全一致。
//
// Go 形态（G0 冻结点）：独立 loopback HTTP handler（std http.ServeMux），
// 供 gateway 组合根反向调用或运维直调；不引入 express/路由框架依赖。

const (
	// AccountTestDispatchInternalPrefix 与 Node accountTestDispatchInternalPrefix 一致。
	AccountTestDispatchInternalPrefix = "/__aiinternal__"
	// AccountTestDispatchSignatureDomain 是签名域分隔符（含尾部 \n）。
	AccountTestDispatchSignatureDomain = "juhe-ai:account-test-dispatch:v1\n"
	accountTestDispatchPath            = "/v1/account-test/dispatch"
	// accountTestCancelPath 是 Go 侧取消扩展路由（Node 走 worker IPC）。
	accountTestCancelPath = "/v1/account-test/cancel"
	rawBodyLimitBytes     = 1024
	signatureHeader       = "X-Juhe-Ai-Signature"
)

var signaturePattern = regexp.MustCompile(`^v1=([0-9a-f]{64})$`)

// IsLoopbackRemoteAddress 对齐 Node isLoopbackRemoteAddress：
// 仅接受 127.0.0.1 与 ::1（BlockList 精确匹配，不含整个 loopback 网段）。
func IsLoopbackRemoteAddress(remoteAddress string) bool {
	normalized := strings.TrimSpace(remoteAddress)
	if normalized == "" {
		return false
	}
	host, _, err := net.SplitHostPort(normalized)
	if err != nil {
		host = normalized
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.Equal(net.ParseIP("127.0.0.1")) || ip.Equal(net.ParseIP("::1"))
}

// CreateAccountTestDispatchSignature 对齐 Node createAccountTestDispatchSignature。
func CreateAccountTestDispatchSignature(secret string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(AccountTestDispatchSignatureDomain))
	_, _ = mac.Write(rawBody)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// DispatchFunc 派发回调；返回 false 表示服务暂不可用（503）。
type DispatchFunc func(ctx context.Context, taskID string) (bool, error)

// CancelFunc 取消回调（Go 侧扩展：Node 的账号测试取消走 worker IPC
// background_worker_account_test_cancel，Go 单进程拓扑改为 loopback HTTP
// fire-and-forget）；返回 false 表示 worker 未接线（503）。
type CancelFunc func(ctx context.Context, taskID string) bool

// AccountTestDispatchRouterOptions 对齐 Node AccountTestDispatchRouterOptions
// （Cancel 为 Go 侧扩展字段）。
type AccountTestDispatchRouterOptions struct {
	Secret   string
	Dispatch DispatchFunc
	Cancel   CancelFunc
}

// NewAccountTestDispatchHandler 返回挂在 std http.ServeMux 上的 handler。
// 组合根用法：
//
//	mux := http.NewServeMux()
//	mux.Handle(internalapi.AccountTestDispatchInternalPrefix+"/", internalapi.NewAccountTestDispatchHandler(opts))
//
// gateway 进程内反向调用或运维 curl 127.0.0.1 均可直调。
// 路由面：POST {prefix}/v1/account-test/dispatch（Node 逐字节移植）+
// POST {prefix}/v1/account-test/cancel（Go 扩展，鉴权/校验矩阵一致）。
func NewAccountTestDispatchHandler(options AccountTestDispatchRouterOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 前缀与路径精确匹配；大小写敏感（对齐 Router caseSensitive+strict）。
		switch r.URL.Path {
		case AccountTestDispatchInternalPrefix + accountTestDispatchPath:
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			handleAccountTestDispatch(w, r, options)
		case AccountTestDispatchInternalPrefix + accountTestCancelPath:
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			handleAccountTestCancel(w, r, options)
		default:
			http.NotFound(w, r)
		}
	})
}

func handleAccountTestDispatch(w http.ResponseWriter, r *http.Request, options AccountTestDispatchRouterOptions) {
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
	if !hasValidSignature(r, options.Secret, rawBody) {
		writeJSONError(w, http.StatusUnauthorized, "认证失败")
		return
	}
	taskID, ok := parseTaskID(rawBody)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	accepted, dispatchErr := options.Dispatch(r.Context(), taskID)
	if dispatchErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !accepted {
		writeJSONError(w, http.StatusServiceUnavailable, "服务暂不可用")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAccountTestCancel(w http.ResponseWriter, r *http.Request, options AccountTestDispatchRouterOptions) {
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
	if !hasValidSignature(r, options.Secret, rawBody) {
		writeJSONError(w, http.StatusUnauthorized, "认证失败")
		return
	}
	taskID, ok := parseTaskID(rawBody)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	if options.Cancel == nil || !options.Cancel(r.Context(), taskID) {
		writeJSONError(w, http.StatusServiceUnavailable, "服务暂不可用")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func requireJSONContentType(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	mediaType := ""
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		mediaType = strings.ToLower(strings.TrimSpace(contentType[:idx]))
	} else {
		mediaType = strings.ToLower(strings.TrimSpace(contentType))
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func requireIdentityContentEncoding(r *http.Request) bool {
	values, found := r.Header["Content-Encoding"]
	if !found {
		return true
	}
	if len(values) == 0 {
		return false
	}
	// Node: contentEncoding !== undefined && trim().toLowerCase() !== 'identity'
	if len(values) > 1 {
		return false
	}
	return strings.ToLower(strings.TrimSpace(values[0])) == "identity"
}

var errBodyTooLarge = errors.New("请求体过大")

func readRawBody(r *http.Request) ([]byte, error) {
	limited := http.MaxBytesReader(nil, r.Body, rawBodyLimitBytes+1)
	rawBody, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	if len(rawBody) > rawBodyLimitBytes {
		return nil, errBodyTooLarge
	}
	return rawBody, nil
}

// hasValidSignatureWithDomain 校验 `v1=<hex>` 签名（常量时间比较）。
// signatureDomain 是含尾部 \n 的签名域分隔符；账户测试面与账户健康检查面
// 各自持有独立域（对齐网关桥 chain_request_failure_health.go 的域常量）。
func hasValidSignatureWithDomain(r *http.Request, secret string, rawBody []byte, signatureDomain string) bool {
	signature := r.Header.Get(signatureHeader)
	if signature == "" {
		return false
	}
	match := signaturePattern.FindStringSubmatch(signature)
	if match == nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signatureDomain))
	_, _ = mac.Write(rawBody)
	expectedBytes := mac.Sum(nil)
	providedBytes, err := hex.DecodeString(match[1])
	if err != nil {
		return false
	}
	// timingSafeEqual 常量时间比较（crypto/hmac）。
	return hmac.Equal(providedBytes, expectedBytes)
}

func hasValidSignature(r *http.Request, secret string, rawBody []byte) bool {
	return hasValidSignatureWithDomain(r, secret, rawBody, AccountTestDispatchSignatureDomain)
}

func parseTaskID(rawBody []byte) (string, bool) {
	if !utf8.Valid(rawBody) {
		return "", false
	}
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	if err := decoder.Decode(&parsed); err != nil {
		return "", false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", false
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return "", false
	}
	if len(record) != 2 {
		return "", false
	}
	if version, exists := record["version"]; !exists || version != float64(1) {
		return "", false
	}
	taskIDValue, exists := record["taskId"]
	if !exists {
		return "", false
	}
	taskID, ok := taskIDValue.(string)
	if !ok {
		return "", false
	}
	normalized := strings.TrimSpace(taskID)
	return normalized, normalized != ""
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}
