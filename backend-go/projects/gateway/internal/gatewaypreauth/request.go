package gatewaypreauth

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysecpol"
)

// GatewayRequest is the gateway-facing request view shared by the pre-auth
// and preflight pipeline. It mirrors the consumed surface of the express
// request in request/pre-auth.ts + request/preflight.ts:
//
//   - HTTP mirrors the raw request (headers, method, URL);
//   - Body mirrors the gateway body pipeline state
//     (request.rawBody / request.body / request.gatewayRequestBody);
//   - ClientIP mirrors express req.ip (trust-proxy resolved upstream);
//   - RemoteAddr mirrors req.socket.remoteAddress.
//
// The gateway server layer populates one GatewayRequest per request; handlers
// never mutate it.
type GatewayRequest struct {
	HTTP       *http.Request
	Body       *gatewaybody.Request
	ClientIP   string
	RemoteAddr string

	// Runtime mirrors req.gatewayRuntime: the runtime snapshot resolved by
	// PreResolveGatewayRuntime and reused by later stages.
	Runtime *gatewayruntimecache.GatewayRuntime

	// InternalOwnerSystemAccountID 非空表示本请求来自进程内内部通道（AI 问答
	// 免 Key 直调）：归属系统账户由此字段给出，preauth 跳过 API Key 抽取/校验/
	// 熔断记录/用户请求限流。该字段只能由进程内代码在构造 GatewayRequest 后
	// 赋值；外部 HTTP 请求经 NewGatewayRequest(r) 构造，没有任何 header / query /
	// body 通道能写入它。
	InternalOwnerSystemAccountID string

	// SuppressUsage 表示本请求的全部使用日志必须丢弃（不落 usage_records、
	// 不写 spool）。由内部通道入口按 InternalOwnerSystemAccountID 置位，随
	// usage 上下文逐层传播到持久化闸口。
	SuppressUsage bool

	// AuthFailureErrorMessage / AuthFailureErrorCode mirror
	// res.locals.gatewayAuthFailureErrorMessage / gatewayAuthFailureErrorCode:
	// the audit copy of the auth failure that produced the response.
	AuthFailureErrorMessage string
	AuthFailureErrorCode    string

	// abortSource mirrors the request/abort-attribution.ts marker.
	abortSource GatewayRequestAbortSource

	// InflightQuotaReserved mirrors the Node inflightQuotaReservedRequests
	// WeakSet membership: one in-flight reservation per request.
	InflightQuotaReserved bool

	// contentFingerprintMemos 是内容指纹会话身份的请求级记忆化（按
	// clientProfile，内容指纹会话身份设计 §4.3）；惰性初始化的指针共享存储，
	// 使 GatewayRequest 的浅复制（model override clone）保持原语义——副本与
	// 原请求共享同一份记忆化（同一逻辑请求）。只能经 ContentFingerprintMemoFor
	// 读写，构造方零值即可。
	contentFingerprintMemos *contentFingerprintMemoStore

	// failureHealthCheckMark 移植 Node 的 per-request dispatched
	// Symbol/WeakMap 语义（失败健康检查「本请求已派发」标记）：标记必须随
	// 请求对象一起被 GC 回收。禁止改回进程级以请求指针为键的全局 map——
	// 强键会把整个 GatewayRequest（含 Body.RawBody 完整请求体）钉在堆里
	// 直至淘汰，生产实测造成 ~1GB 活堆增长（BUG-0339）。指针字段使浅复制
	// （gatewaydispatch gatewayRequestWithModelOverride 的 clone := *req）
	// 与原请求共享同一标记，保持「同一逻辑请求只派发一次」。只能经
	// RequestFailureHealthCheckDispatched / MarkRequestFailureHealthCheckDispatched
	// 读写，NewGatewayRequest 构造即初始化；零值请求由 Mark 方法惰性补齐。
	failureHealthCheckMark *requestFailureHealthCheckMark

	// SecurityPolicy 是 API Key 安全策略的请求级执行事实（API Key 安全策略与
	// 出站内容脱敏设计 §2/§8，批B2）：由链层在出站脱敏单点（body 定型后）挂
	// 载，供 usage 终态（SecurityPolicyApplied / piiHits）与响应侧占位符还原
	// 消费。nil = 未绑定 / 内置默认 / 空规则集（零开销路径，行为与旧版逐字节
	// 一致）。指针字段使浅复制（model override clone）与原请求共享同一份执行
	// 事实——脱敏在请求生命周期内一次定型（§2 映射表请求级唯一）。
	SecurityPolicy *SecurityPolicyRequestState
}

// SecurityPolicyRequestState 是单次请求的出站脱敏执行事实。请求级数据结构：
// Mapping 随请求上下文传播、请求终态销毁，不落盘、不进 Redis、不写日志（设
// 计 §2 硬约束）；字段值在脱敏点一次写入，之后只读。
type SecurityPolicyRequestState struct {
	// PolicyID 是绑定的安全策略 id（security_policies.id，恒非内置默认行——
	// 内置默认走零开销路径不产生本状态）。
	PolicyID string
	// Family 是本次判定的出站协议族（gatewaysecpol 五族词表）；空 = 无法判
	// 定，fail-open 跳过扫描（§9.4），仅保留绑定事实供 usage 记录。
	Family string
	// Applied 报告本次出站脱敏是否实际发生替换/注入（SanitizeResult.Applied）。
	Applied bool
	// Hits 是按规则 label 的命中计数（无原文，含 mask 与 placeholder；§8）。
	// 未执行扫描时为 nil。
	Hits map[string]int
	// TotalHits 是原始命中总数（防护次数口径，设计 §4.3.1：每次实际替换
	// +1，不去重，fail-open 跳过不计），供请求终态 INCRBY 防护计数消费。
	TotalHits int
	// Skips 是 fail-open 跳过计数（内层 JSON 解析失败等，§5）。
	Skips int
	// Mapping 是占位符映射表（响应侧还原消费；纯 mask 策略或未命中为 nil）。
	Mapping *gatewaysecpol.Mapping
	// ProtectionCounted 钉住本请求的防护次数 Redis INCRBY 已执行（API Key 安
	// 全策略防护次数计数，写后缓存设计 §4.3.1）：终态消费点（FinalizationDeps
	// 构造点）随多账户重试 / 多 attempt 每 attempt 进入一次，CAS 让首次到达的
	// 终态入口执行 INCRBY，其余 attempt 直接跳过——脱敏每请求一次，防护计数
	// 同请求只计一次。零值 false 可用；TotalHits 为 0 时该标记不置位（无计
	// 数即无去重需要）。
	ProtectionCounted atomic.Bool
}

// requestFailureHealthCheckMark 是单请求的失败健康检查派发标记；互斥锁保护
// 并发失败分支（流式/非流式、failed-response 与 transport-failure）下的置位。
type requestFailureHealthCheckMark struct {
	mu         sync.Mutex
	dispatched bool
}

// requestFailureHealthCheckMarkInitMu 只保护零值请求（未经 NewGatewayRequest
// 构造）的 failureHealthCheckMark 惰性初始化；生产请求构造即非 nil，不走此锁。
var requestFailureHealthCheckMarkInitMu sync.Mutex

// contentFingerprintMemoStore 是单请求的内容指纹记忆化存储。
type contentFingerprintMemoStore struct {
	mu    sync.Mutex
	items map[string]ContentFingerprintMemo
}

// NewGatewayRequest builds the view from a raw request. ClientIP falls back
// to HTTP.RemoteAddr-based extraction only through ExtractClientIP (metadata
// contract); the struct fields stay verbatim inputs.
func NewGatewayRequest(r *http.Request) *GatewayRequest {
	req := &GatewayRequest{HTTP: r, failureHealthCheckMark: &requestFailureHealthCheckMark{}}
	if r != nil {
		req.RemoteAddr = r.RemoteAddr
	}
	return req
}

// RequestFailureHealthCheckDispatched 报告本请求是否已派发过失败健康检查
// （Node dispatched Symbol 的读取臂）。nil 接收者与未初始化标记返回 false。
func (req *GatewayRequest) RequestFailureHealthCheckDispatched() bool {
	if req == nil || req.failureHealthCheckMark == nil {
		return false
	}
	req.failureHealthCheckMark.mu.Lock()
	defer req.failureHealthCheckMark.mu.Unlock()
	return req.failureHealthCheckMark.dispatched
}

// MarkRequestFailureHealthCheckDispatched 置位本请求的失败健康检查派发标记
// （Node dispatched Symbol 的写入臂）。nil 接收者是 no-op；零值请求经包级
// requestFailureHealthCheckMarkInitMu 惰性补建标记，与并发置位互斥。
func (req *GatewayRequest) MarkRequestFailureHealthCheckDispatched() {
	if req == nil {
		return
	}
	if req.failureHealthCheckMark == nil {
		requestFailureHealthCheckMarkInitMu.Lock()
		if req.failureHealthCheckMark == nil {
			req.failureHealthCheckMark = &requestFailureHealthCheckMark{}
		}
		requestFailureHealthCheckMarkInitMu.Unlock()
	}
	req.failureHealthCheckMark.mu.Lock()
	req.failureHealthCheckMark.dispatched = true
	req.failureHealthCheckMark.mu.Unlock()
}

// PathAndQuery mirrors `req.originalUrl || req.path`: the escaped request
// path plus raw query, with a leading slash guaranteed.
func (req *GatewayRequest) PathAndQuery() string {
	if req == nil || req.HTTP == nil {
		return "/"
	}
	if req.HTTP.RequestURI != "" {
		// RequestURI carries the original (escaped) path + query verbatim.
		return req.HTTP.RequestURI
	}
	return gatewayRequestPathAndQuery(req.HTTP)
}

// Path mirrors express req.path: the (still escaped) pathname without query.
func (req *GatewayRequest) Path() string {
	if req == nil || req.HTTP == nil {
		return "/"
	}
	if req.HTTP.URL == nil {
		return "/"
	}
	path := req.HTTP.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// Header mirrors express req.header(name): case-insensitive lookup with an
// empty string when the header is absent.
func (req *GatewayRequest) Header(name string) string {
	if req == nil || req.HTTP == nil {
		return ""
	}
	return req.HTTP.Header.Get(name)
}

// MethodUpper mirrors req.method.toUpperCase().
func (req *GatewayRequest) MethodUpper() string {
	if req == nil || req.HTTP == nil {
		return ""
	}
	return strings.ToUpper(req.HTTP.Method)
}

// BodyState mirrors getGatewayRequestBodyState(req): the body pipeline state
// or nil before capture.
func (req *GatewayRequest) BodyState() *gatewaybody.BodyState {
	if req == nil || req.Body == nil {
		return nil
	}
	return req.Body.State
}

// ParsedJSONObjectBody mirrors the `isRecord(req.body) ? req.body :
// isRecord(req.gatewayParsedJsonBody) ? ... : undefined` inspection body.
func (req *GatewayRequest) ParsedJSONObjectBody() map[string]any {
	if req == nil || req.Body == nil {
		return nil
	}
	return gatewaybody.GatewayJSONObjectBody(req.Body)
}

// MaterializedParsedJSONObjectBody returns the parsed JSON object body,
// materializing it once through the shared bounded parser on first use.
// Later calls hit the request-scoped parse cache (gatewaybody.Request keeps
// parsedAvailable/parsedBody), so per-candidate dispatch attempts stop re-
// Unmarshal-ing the full raw body. A nil parser, an empty body, a parse
// failure or a non-object JSON body returns nil and leaves the caller's
// previous driver-side fallback behavior untouched.
func (req *GatewayRequest) MaterializedParsedJSONObjectBody(parser *gatewaybody.JSONParser) map[string]any {
	if req == nil || req.Body == nil {
		return nil
	}
	if object := req.ParsedJSONObjectBody(); object != nil {
		return object
	}
	if parser == nil || len(req.Body.RawBody) == 0 {
		return nil
	}
	value, err := parser.ParseRequestJSONBody(req.Body.RequestContext(), req.Body, 0)
	if err != nil || value == nil {
		return nil
	}
	object, _ := value.(map[string]any)
	return object
}

// gatewayRequestPathAndQuery mirrors gatewayanthropic.RequestPathAndQuery.
func gatewayRequestPathAndQuery(r *http.Request) string {
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

// splitPathAndQuery mirrors splitPathAndQuery(originalUrl).
func splitPathAndQuery(pathAndQuery string) (path, query string) {
	index := strings.Index(pathAndQuery, "?")
	if index < 0 {
		return pathAndQuery, ""
	}
	return pathAndQuery[:index], pathAndQuery[index+1:]
}

// splitPathAndQueryTwo mirrors the JS `split('?', 2)` destructuring used by
// requestGeminiInteractionStreamQuery: at most two parts.
func splitPathAndQueryTwo(value string) (string, string) {
	parts := strings.SplitN(value, "?", 2)
	path := parts[0]
	query := ""
	if len(parts) > 1 {
		query = parts[1]
	}
	return path, query
}
