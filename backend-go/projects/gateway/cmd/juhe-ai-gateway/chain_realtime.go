package main

// M5b2 realtime WS 桥接 handler（实时语音Realtime网关设计 §2/§3/§5，契约
// §4.4）：GET /v1/realtime 的 WebSocket 升级请求在协议门后短路进本面，
// 不进 chain 响应管道（流式管道是 HTTP 语义，WS 是新面）。
//
// 处理流（设计 §3 顺序）：
//
//	认证（API Key Bearer，preauth 全套守卫；或 ?token= ephemeral——Verify 含
//	model 一致性，按 api_key_id 解析运行时后同样过 preauth 守卫；两源二选
//	一，Bearer 优先）→ 模型解析（realtime 目录行）→ 每 API Key 并发连接数
//	检查 → 派发循环选账户（realtime_session endpoint mode + 支持模型 + 可用
//	Bearer 凭据过滤；候选耗尽 503）→ 拨号上游 wss://<base>/v1/realtime（Bearer
//	账户凭据，代理沿账户 ProxyURL）→ 升级客户端（101）→ 双向泵。
//
// 受理边界（设计 §3，M1 §7 裁决的 WS 版）：拨号/握手/升级拒绝失败=受理前，
// 释放该候选并发槽换下一候选；上游 101 成功=受理，此后连接存活期绑定账
// 户，任一侧断开只关另一侧不换账户。
//
// usage（设计 §5）：上游→客户端方向挂旁路观测器，只解析文本帧 JSON 的
// response.done.response.usage 累计会话汇总（解析失败静默忽略不影响转发）；
// 连接关闭终态落 usage_records（endpoint=/v1/realtime、audio token 计量、
// 空会话 0 计费 + usage_missing），经既有 spool 链（沿 media job 先例）。
// 受理前失败（候选耗尽等）不落 usage 行——无会话无账户归属（engine 失败
// 派发器的失败 usage 行属 HTTP 尝试记账面，WS 面首版不引入）。
//
// 零存储（设计 §1）：审计只记会话元数据（模型/账户/时长/断开原因/usage 事
// 件数），音频字节与事件序列不落任何存储。
//
// 注意：gorilla Upgrade 直接 hijack 后把 101 写进裸连接，不经
// kernel.compressionWriter 的 WriteHeader；中间件 defer 的 finish() 在会话
// 结束后对已 hijack 连接补写状态只产生一条 net/http 告警日志，无协议影响。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/realtimetoken"
)

// ---- 设计常量（Realtime 设计 §3/§4）----

const (
	// realtimePingInterval WS 保活 ping 间隔（设计 §3：20s）。
	realtimePingInterval = 20 * time.Second
	// realtimePingMissLimit 连续无 pong 次数上限（设计 §3：3 次）。
	realtimePingMissLimit = 3
	// realtimeUpstreamHandshakeTimeout 上游拨号/握手预算（受理前边界内的
	// 单候选等待；设计未定值，取网关链上游建连的常用量级）。
	realtimeUpstreamHandshakeTimeout = 20 * time.Second
	// realtimeIdleTimeoutDefault / realtimeMaxSessionDefault /
	// realtimeMaxConnectionsDefault 是设置键缺省（手构零值）时的设计默认；
	// 生产值恒经 settings 种子默认到达（M5b1 落键，Store 兼容默认兜底）。
	realtimeIdleTimeoutDefault    = 120 * time.Second
	realtimeMaxSessionDefault     = 30 * time.Minute
	realtimeMaxConnectionsDefault = 5

	realtimeTokenQueryParam = "token"
	realtimeModelQueryParam = "model"
	realtimeUsageEndpoint   = "/v1/realtime"
)

// realtimeClientUpgrader 升级客户端连接。不设 origin 检查：浏览器客户端是
// 本面的目标形态（设计 §2），来源策略不在本面契约内。
var realtimeClientUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// chainRealtimeConnectionLimiter 是每 API Key 并发 WS 连接的进程内计数器
// （设计 §3：realtimeMaxConnectionsPerApiKey）。多实例部署下各进程独立计
// 数——设置键语义是网关进程级限流，与账户并发槽（进程内 tracker + Redis
// 读模型）同层不共享。
type chainRealtimeConnectionLimiter struct {
	counts sync.Map // api key id -> *atomic.Int64
}

// acquire 原子占一个连接位；超出 limit 回退并返回 false（Add 的瞬时过冲不
// 持久——超限连接立即回退，计数不泄漏）。
func (l *chainRealtimeConnectionLimiter) acquire(apiKeyID string, limit int64) bool {
	if l == nil || apiKeyID == "" || limit <= 0 {
		return true
	}
	raw, _ := l.counts.LoadOrStore(apiKeyID, &atomic.Int64{})
	counter := raw.(*atomic.Int64)
	if counter.Add(1) > limit {
		counter.Add(-1)
		return false
	}
	return true
}

// release 释放一个连接位。
func (l *chainRealtimeConnectionLimiter) release(apiKeyID string) {
	if l == nil || apiKeyID == "" {
		return
	}
	if raw, ok := l.counts.Load(apiKeyID); ok {
		raw.(*atomic.Int64).Add(-1)
	}
}

// count 返回当前计数（测试断言面）。
func (l *chainRealtimeConnectionLimiter) count(apiKeyID string) int64 {
	if l == nil {
		return 0
	}
	if raw, ok := l.counts.Load(apiKeyID); ok {
		return raw.(*atomic.Int64).Load()
	}
	return 0
}

// chainIsRealtimePath 报告请求路径是否 realtime WS 升级面（/v1/realtime 精确
// 路径，v1 前缀可剥；方法无关——非 GET 形态在 handler 内按 404 收敛，与
// client_secrets 非 POST 同款契约，不放落派发链）。
func chainIsRealtimePath(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	path := gatewaypreauth.RequestPathWithoutQuery(req)
	return chainStripGatewayVersionPrefix(path) == "/realtime"
}

// chainIsRealtimeUpgradeRequest 是 chain_v1 短路判定（含 GET 方法门）。
func chainIsRealtimeUpgradeRequest(req *gatewaypreauth.GatewayRequest) bool {
	return chainIsRealtimePath(req) && req.MethodUpper() == http.MethodGet
}

// serveRealtimeUpgrade 是 WS 桥接入口（chain_v1 在协议门后、preauth 前短
// 路——?token= 浏览器形态没有 Bearer，不能先过 PreResolveGatewayRuntime 的
// 强制 Bearer 认证）。traceID/stage 记录器由链入口统一装配，此处补 request
// 受理埋点、审计捕获与完成信号（镜像主链顺序）。
func (c *gatewayChain) serveRealtimeUpgrade(ctx context.Context, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, traceID, endpoint string) {
	if req.MethodUpper() != http.MethodGet {
		// 唯一合法形态是 GET 升级请求；其余方法维持链面 404 JSON 契约。
		writeChainNotFound(res)
		return
	}
	startedAt := c.preauth.NowMs()
	model := chainRealtimeRequestModel(req)
	c.observability.LogRequestStage("request.accepted", map[string]any{
		"traceId":       traceID,
		"method":        req.MethodUpper(),
		"endpoint":      endpoint,
		"requestLane":   string(gatewayproto.LaneRealtime),
		"trafficSource": gatewayTrafficSource,
		"model":         model,
		"stream":        false,
	}, "success", c.clock.Now())
	httpCompletion := newGatewayHTTPCompletion()
	if req.HTTP != nil {
		req.HTTP = req.HTTP.WithContext(context.WithValue(req.HTTP.Context(), chainHTTPCompletionKey, httpCompletion))
	}
	auditCapture := c.newAuditCapture(req, traceID, startedAt, httpCompletion)
	defer gatewaypreauth.CancelAuditCapture(auditCapture)
	defer httpCompletion.complete(time.Now().UnixMilli())

	session, failure := c.prepareRealtimeSession(ctx, req, res, auditCapture, traceID, model)
	if failure != nil {
		// prepare 已渲染错误响应；此处只做最小审计收尾（身份 + 状态码）。
		c.finalizeRealtimeUpgradeAudit(auditCapture, req, res, nil)
		return
	}
	// 资源释放（LIFO）：先释放 per-key 连接位与账户并发槽，再回收审计捕
	// 获/完成信号（上方 defer）。
	defer c.realtimeConnections.release(session.apiKeyID)
	defer func() {
		if slot := session.concurrencySlot; slot != nil && slot.Release != nil {
			slot.Release()
		}
	}()
	c.runRealtimeBridge(session)
	c.enqueueRealtimeTerminalUsage(session)
	c.finalizeRealtimeUpgradeAudit(auditCapture, req, res, session)
}

// realtimeSession 是一次已受理会话的全部状态（受理后绑定）。
type realtimeSession struct {
	c             *gatewayChain
	ctx           context.Context
	req           *gatewaypreauth.GatewayRequest
	traceID       string
	model         string
	apiKeyID      string
	systemAccount string
	groupID       string
	authSource    string // "bearer" | "token"
	account       gatewaydispatch.AccountCandidate
	upstream      *websocket.Conn
	client        *websocket.Conn
	runtime       *gatewayruntimecache.GatewayRuntime
	// concurrencySlot 是账户并发槽（受理前获取，终态释放）。
	concurrencySlot *gatewaydispatch.ConcurrencySlot
	// usage 是会话级汇总器（含目录行引用，终态计费用）。
	usage          realtimeUsageAccumulator
	catalogItem    *gatewayruntimecache.ProviderModelCatalogItem
	idleTimeout    time.Duration
	maxSession     time.Duration
	usageStartedAt time.Time
	closeReason    string
	closedAt       time.Time
}

// realtimeUsageAccumulator 是会话级 usage 汇总器（设计 §5：观测器旁路累计，
// 不改动事件帧）。
type realtimeUsageAccumulator struct {
	inputTokens       int64
	outputTokens      int64
	inputTextTokens   int64
	inputAudioTokens  int64
	outputTextTokens  int64
	outputAudioTokens int64
	usageEvents       int64
}

// realtimeUsageEvent 是观测器解析的报文子集（契约 §4.4 计量点）；多余字段
// 由 json.Unmarshal 静默丢弃。
type realtimeUsageEvent struct {
	Type     string `json:"type"`
	Response *struct {
		Usage *struct {
			InputTokens       int64 `json:"input_tokens"`
			InputTokenDetails *struct {
				TextTokens  int64 `json:"text_tokens"`
				AudioTokens int64 `json:"audio_tokens"`
			} `json:"input_token_details"`
			OutputTokens       int64 `json:"output_tokens"`
			OutputTokenDetails *struct {
				TextTokens  int64 `json:"text_tokens"`
				AudioTokens int64 `json:"audio_tokens"`
			} `json:"output_token_details"`
		} `json:"usage"`
	} `json:"response"`
}

// observe 解析一帧上游→客户端方向消息：只认文本帧的 response.done.usage，
// 解析失败静默忽略（设计 §5：不影响转发）。
func (a *realtimeUsageAccumulator) observe(messageType int, payload []byte) {
	if a == nil || messageType != websocket.TextMessage {
		return
	}
	var event realtimeUsageEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}
	if event.Type != "response.done" || event.Response == nil || event.Response.Usage == nil {
		return
	}
	usage := event.Response.Usage
	a.inputTokens += usage.InputTokens
	a.outputTokens += usage.OutputTokens
	if usage.InputTokenDetails != nil {
		a.inputTextTokens += usage.InputTokenDetails.TextTokens
		a.inputAudioTokens += usage.InputTokenDetails.AudioTokens
	}
	if usage.OutputTokenDetails != nil {
		a.outputTextTokens += usage.OutputTokenDetails.TextTokens
		a.outputAudioTokens += usage.OutputTokenDetails.AudioTokens
	}
	a.usageEvents++
}

// prepareRealtimeSession 完成 认证→模型解析→并发检查→派发拨号→客户端升级，
// 失败时渲染错误响应并返回 failure（调用方只做审计收尾）。成功返回的会话
// 已完成受理（上游 101 + 客户端 101），此后绑定账户。
func (c *gatewayChain) prepareRealtimeSession(ctx context.Context, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, auditCapture gatewaypreauth.AuditCaptureContext, traceID, model string) (*realtimeSession, *realtimePrepareFailure) {
	// 模型必须随升级请求显式指定（设计 §2：?model=；会话中途不可换模型）。
	if strings.TrimSpace(model) == "" {
		c.renderRealtimeError(res, http.StatusBadRequest, "Missing required query parameter 'model'.", "invalid_request_error", "missing_model")
		return nil, &realtimePrepareFailure{status: http.StatusBadRequest}
	}
	runtime, authSource, failure := c.resolveRealtimeIdentity(ctx, req, res, model)
	if failure != nil {
		return nil, failure
	}
	if runtime == nil || runtime.APIKey == nil {
		c.renderRealtimeError(res, http.StatusUnauthorized, "缺少网关 API Key 身份", "invalid_request_error", "invalid_api_key")
		return nil, &realtimePrepareFailure{status: http.StatusUnauthorized}
	}
	apiKey := runtime.APIKey
	auditCapture.BindContext(gatewaypreauth.AuditGatewayContext{
		SystemAccountID: apiKey.SystemAccountID,
		APIKeyID:        apiKey.ID,
		GroupID:         apiKey.SelectedGroupID,
		ProviderCode:    gatewaypreauth.ProtocolCodeOpenAI,
		TrafficSource:   gatewayTrafficSource,
	})

	// 模型解析：必须命中 realtime 目录行（active + realtime 协议）。
	catalogItem := c.resolveRealtimeCatalogModel(ctx, apiKey.SystemAccountID, model)
	if catalogItem == nil {
		c.renderRealtimeError(res, http.StatusBadRequest, "model 不在 realtime 目录（需为协议 realtime 的 active 目录模型）", "invalid_request_error", "model_not_in_realtime_catalog")
		return nil, &realtimePrepareFailure{status: http.StatusBadRequest}
	}

	settings := runtime.Settings
	idleTimeout := time.Duration(settings.RealtimeIdleTimeoutSeconds) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = realtimeIdleTimeoutDefault
	}
	maxSession := time.Duration(settings.RealtimeMaxSessionSeconds) * time.Second
	if maxSession <= 0 {
		maxSession = realtimeMaxSessionDefault
	}
	connectionLimit := settings.RealtimeMaxConnectionsPerAPIKey
	if connectionLimit <= 0 {
		connectionLimit = realtimeMaxConnectionsDefault
	}

	// 每 API Key 并发连接数检查（受理前拒绝，不占任何账户资源）。
	if !c.realtimeConnections.acquire(apiKey.ID, connectionLimit) {
		c.renderRealtimeError(res, http.StatusTooManyRequests,
			fmt.Sprintf("realtime 并发连接数已达上限（%d）", connectionLimit), "invalid_request_error", "realtime_connection_limit")
		return nil, &realtimePrepareFailure{status: http.StatusTooManyRequests}
	}
	// 连接位持有标记：只有走到受理成功才移交会话接管释放。
	connectionHeld := false
	defer func() {
		if !connectionHeld {
			c.realtimeConnections.release(apiKey.ID)
		}
	}()

	// 派发循环：realtime_session 候选过滤 + 上游拨号；受理前失败换下一候选，
	// 候选耗尽 503。
	dial, dialFailure := c.dialRealtimeUpstream(ctx, req, runtime, model)
	if dialFailure != nil {
		c.renderRealtimeError(res, dialFailure.status, dialFailure.message, "service_unavailable", dialFailure.code)
		return nil, &realtimePrepareFailure{status: dialFailure.status}
	}

	// 上游受理确立（101），升级客户端。升级失败（客户端握手不完整）只关上
	// 游并释放槽——客户端从未建立，无会话可言。
	clientConn, upgradeErr := realtimeClientUpgrader.Upgrade(res, req.HTTP, nil)
	if upgradeErr != nil {
		_ = dial.conn.Close()
		if dial.slot.Release != nil {
			dial.slot.Release()
		}
		c.observability.Logger().Warn("realtime_client_upgrade_failed", map[string]any{
			"event": "realtime_client_upgrade_failed", "traceId": traceID, "error": upgradeErr.Error(),
		}, "realtime 客户端升级失败")
		return nil, &realtimePrepareFailure{status: http.StatusBadRequest}
	}

	session := &realtimeSession{
		c:               c,
		ctx:             ctx,
		req:             req,
		traceID:         traceID,
		model:           model,
		apiKeyID:        apiKey.ID,
		systemAccount:   apiKey.SystemAccountID,
		groupID:         apiKey.SelectedGroupID,
		authSource:      authSource,
		account:         dial.account,
		upstream:        dial.conn,
		client:          clientConn,
		runtime:         runtime,
		concurrencySlot: dial.slot,
		catalogItem:     catalogItem,
		idleTimeout:     idleTimeout,
		maxSession:      maxSession,
		usageStartedAt:  time.Now(),
	}
	connectionHeld = true
	return session, nil
}

// realtimePrepareFailure 标记 prepare 阶段终态（响应已渲染）。
type realtimePrepareFailure struct {
	status int
}

// realtimeDialFailure 是派发循环的终态失败（响应由调用方渲染）。
type realtimeDialFailure struct {
	status  int
	code    string
	message string
}

// realtimeUpstreamDial 是一次成功拨号的产物。
type realtimeUpstreamDial struct {
	conn    *websocket.Conn
	account gatewaydispatch.AccountCandidate
	slot    *gatewaydispatch.ConcurrencySlot
}

// dialRealtimeUpstream 沿候选序拨号上游 WS：每候选先占账户并发槽（受理前
// 失败即释放），拨号/握手/升级拒绝=受理前换下一候选；候选耗尽 503。gorilla
// Dial 对非 101 响应返回 ErrBadHandshake（resp 携带状态），全部按受理前失败
// 处理（设计 §3：4xx/5xx 拒绝都可换账户重试）。
func (c *gatewayChain) dialRealtimeUpstream(ctx context.Context, req *gatewaypreauth.GatewayRequest, runtime *gatewayruntimecache.GatewayRuntime, model string) (*realtimeUpstreamDial, *realtimeDialFailure) {
	candidates := c.realtimeSessionCandidates(ctx, runtime, model)
	if len(candidates) == 0 {
		return nil, &realtimeDialFailure{
			status:  http.StatusServiceUnavailable,
			code:    "no_candidate_accounts",
			message: "当前分组无 realtime 可用账户（realtime_session 端点模式 + 支持模型 + API Key 凭据）",
		}
	}
	query := chainRealtimeUpstreamQuery(req, model)
	for _, account := range candidates {
		if ctx.Err() != nil {
			break
		}
		// 账户并发槽：占不到视为该候选忙，换下一候选（WS 面不做有界容量
		// 等待——会话是长连接，等待语义属下一次连接请求）。
		slot, slotErr := c.engine.Concurrency.TryAcquireAsync(ctx, account.ID, account.ConcurrencyLimit,
			gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: string(gatewayproto.LaneRealtime)})
		if slotErr != nil {
			return nil, &realtimeDialFailure{
				status: http.StatusServiceUnavailable, code: "concurrency_read_failed",
				message: "realtime 账户并发读取失败: " + slotErr.Error(),
			}
		}
		if !slot.Acquired {
			continue
		}
		conn, dialErr := c.dialRealtimeAccount(ctx, account, query)
		if dialErr == nil {
			return &realtimeUpstreamDial{conn: conn, account: account, slot: &slot}, nil
		}
		// 受理前失败：释放槽换下一候选。
		if slot.Release != nil {
			slot.Release()
		}
		c.observability.Logger().Warn("realtime_upstream_dial_failed", map[string]any{
			"event": "realtime_upstream_dial_failed", "accountId": account.ID,
			"providerCode": account.ProviderCode, "error": dialErr.Error(),
		}, "realtime 上游拨号失败（受理前，换下一候选）")
	}
	return nil, &realtimeDialFailure{
		status:  http.StatusServiceUnavailable,
		code:    "upstream_unavailable",
		message: "realtime 上游暂不可用（候选已耗尽）",
	}
}

// dialRealtimeAccount 拨号一个账户的上游 WS：wss://<base>/v1/realtime（http
// 基址换 ws scheme），Bearer 账户凭据，代理沿账户 ProxyURL，拨号取消跟随
// 请求上下文（客户端放弃升级时中止受理前等待）。
func (c *gatewayChain) dialRealtimeAccount(ctx context.Context, account gatewaydispatch.AccountCandidate, query url.Values) (*websocket.Conn, error) {
	dialer := &websocket.Dialer{
		HandshakeTimeout: realtimeUpstreamHandshakeTimeout,
		Proxy:            chainRealtimeProxyFunc(derefString(account.ProxyURL)),
	}
	target := chainRealtimeUpstreamURL(account.BaseURL, query)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+strings.TrimSpace(account.APIKey))
	conn, response, err := dialer.DialContext(ctx, target, header)
	if err != nil {
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		return nil, err
	}
	return conn, nil
}

// chainRealtimeProxyFunc 把账户 ProxyURL 适配为 gorilla Dialer 的代理函数
// （空串=直连；非法 URL 视为无代理——凭据形态错误不阻断会话面）。
func chainRealtimeProxyFunc(proxyURL string) func(*http.Request) (*url.URL, error) {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return func(*http.Request) (*url.URL, error) { return nil, nil }
	}
	return http.ProxyURL(parsed)
}

// chainRealtimeUpstreamURL 构造上游 WS URL：沿 gatewayopenai.BuildUpstreamURL
// 的 /v1 归一（openai 基址补缀），再把 scheme 换成 ws 族。
func chainRealtimeUpstreamURL(baseURL string, query url.Values) string {
	suffix := "/realtime"
	if encoded := query.Encode(); encoded != "" {
		suffix += "?" + encoded
	}
	httpURL := gatewayopenai.BuildUpstreamURL(baseURL, suffix)
	switch {
	case strings.HasPrefix(httpURL, "https://"):
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	case strings.HasPrefix(httpURL, "http://"):
		return "ws://" + strings.TrimPrefix(httpURL, "http://")
	default:
		// 基址未带 scheme（测试直连形态）按 ws 处理。
		return "ws://" + strings.TrimPrefix(httpURL, "//")
	}
}

// runRealtimeBridge 运行双向泵直到任一侧断开或生命周期截止（设计 §3），
// 终态把断开原因与会话结束时刻写回 session。
func (c *gatewayChain) runRealtimeBridge(session *realtimeSession) {
	bridge := newRealtimeBridge(session)
	bridge.run()
	session.closeReason = bridge.closeReason
	session.closedAt = time.Now()
}

// realtimeBridge 是双向泵 + 生命周期管理的内部载体。
type realtimeBridge struct {
	session     *realtimeSession
	closeOnce   sync.Once
	closeReason string
	done        chan struct{}

	// 双侧写互斥（gorilla Conn 单写者约束：数据帧、ping、close 都经此）。
	clientWriteMu   sync.Mutex
	upstreamWriteMu sync.Mutex

	pingMisses int64
}

func newRealtimeBridge(session *realtimeSession) *realtimeBridge {
	return &realtimeBridge{session: session, done: make(chan struct{})}
}

// realtimeReadResult 是泵读循环的一帧产物。
type realtimeReadResult struct {
	messageType int
	payload     []byte
	err         error
}

// run 阻塞直到会话终态。泵结构：
//   - goroutine A：上游→客户端转发（含 usage 观测器）
//   - goroutine B：客户端读循环（结果经 channel 回主循环）
//   - 主循环：客户端→上游转发 + ping/pong 保活（20s / 3 次未 pong）
//   - 定时器：最大会话时长（到期 close 1000，设计 §3）
func (b *realtimeBridge) run() {
	s := b.session
	// 初始读截止：空闲超时双向生效（任意一帧续期）。
	_ = s.client.SetReadDeadline(time.Now().Add(s.idleTimeout))
	_ = s.upstream.SetReadDeadline(time.Now().Add(s.idleTimeout))
	s.client.SetPongHandler(func(string) error {
		atomic.StoreInt64(&b.pingMisses, 0)
		return s.client.SetReadDeadline(time.Now().Add(s.idleTimeout))
	})

	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		b.pumpUpstreamToClient()
	}()
	clientReads := make(chan realtimeReadResult)
	go b.readClientLoop(clientReads)
	pingTicker := time.NewTicker(realtimePingInterval)
	defer pingTicker.Stop()
	maxSessionTimer := time.AfterFunc(s.maxSession, func() {
		b.shutdown("max_session", websocket.CloseNormalClosure, "realtime 最大会话时长已到")
	})
	defer maxSessionTimer.Stop()

	for {
		select {
		case <-b.done:
			b.shutdown("", realtimeCloseCodeOf(b.closeReason), "")
			<-upstreamDone
			return
		case <-pingTicker.C:
			b.sendClientPing()
		case result := <-clientReads:
			if result.err != nil {
				if isRealtimeTimeoutError(result.err) {
					b.shutdown("idle_timeout", websocket.CloseGoingAway, "realtime 空闲超时")
				} else {
					code, _ := realtimeCloseErrorOf(result.err)
					b.shutdown("client_closed", code, "")
				}
				<-upstreamDone
				return
			}
			_ = s.client.SetReadDeadline(time.Now().Add(s.idleTimeout))
			b.upstreamWriteMu.Lock()
			writeErr := s.upstream.WriteMessage(result.messageType, result.payload)
			b.upstreamWriteMu.Unlock()
			if writeErr != nil {
				b.shutdown("upstream_write_failed", websocket.CloseNormalClosure, "")
				<-upstreamDone
				return
			}
		}
	}
}

// readClientLoop 持续读客户端帧并投递结果 channel（错误即退出循环）。
func (b *realtimeBridge) readClientLoop(out chan<- realtimeReadResult) {
	s := b.session
	for {
		messageType, payload, err := s.client.ReadMessage()
		select {
		case out <- realtimeReadResult{messageType: messageType, payload: payload, err: err}:
		case <-b.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// pumpUpstreamToClient 转发上游→客户端帧并旁路观测 usage。上游断开（含
// close_after_established 场景）时把 close 帧语义通知客户端后退出。
func (b *realtimeBridge) pumpUpstreamToClient() {
	s := b.session
	for {
		messageType, payload, err := s.upstream.ReadMessage()
		if err != nil {
			closeCode, closeText := realtimeCloseErrorOf(err)
			b.shutdown("upstream_closed", closeCode, closeText)
			return
		}
		_ = s.upstream.SetReadDeadline(time.Now().Add(s.idleTimeout))
		s.usage.observe(messageType, payload)
		b.clientWriteMu.Lock()
		writeErr := s.client.WriteMessage(messageType, payload)
		b.clientWriteMu.Unlock()
		if writeErr != nil {
			b.shutdown("client_write_failed", websocket.CloseGoingAway, "")
			return
		}
	}
}

// sendClientPing 发一轮保活 ping；连续 realtimePingMissLimit 次未 pong 断开
// （pong handler 归零计数）。写失败按客户端断开收尾。
func (b *realtimeBridge) sendClientPing() {
	s := b.session
	misses := atomic.AddInt64(&b.pingMisses, 1)
	b.clientWriteMu.Lock()
	err := s.client.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
	b.clientWriteMu.Unlock()
	if err != nil {
		b.shutdown("client_write_failed", websocket.CloseGoingAway, "")
		return
	}
	if misses >= realtimePingMissLimit {
		b.shutdown("ping_timeout", websocket.CloseGoingAway, "realtime ping 无响应超时")
	}
}

// shutdown 幂等收尾：记录断开原因（空原因保留既有值）并双向 close——
// close 帧 + 连接关闭，两侧泵的后续读写随即错误退出。
func (b *realtimeBridge) shutdown(reason string, code int, text string) {
	b.closeOnce.Do(func() {
		if reason != "" {
			b.closeReason = reason
		}
		close(b.done)
		s := b.session
		if code <= 0 {
			code = websocket.CloseNormalClosure
		}
		deadline := time.Now().Add(time.Second)
		b.clientWriteMu.Lock()
		_ = s.client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), deadline)
		b.clientWriteMu.Unlock()
		b.upstreamWriteMu.Lock()
		_ = s.upstream.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), deadline)
		b.upstreamWriteMu.Unlock()
		_ = s.client.Close()
		_ = s.upstream.Close()
	})
}

// realtimeCloseCodeOf 归一断开原因到 close code（max_session 用 1000——设计
// §3；其余按语义就近）。
func realtimeCloseCodeOf(reason string) int {
	switch reason {
	case "idle_timeout", "ping_timeout":
		return websocket.CloseGoingAway
	default:
		return websocket.CloseNormalClosure
	}
}

// realtimeCloseErrorOf 从 gorilla 读错误提取 close 帧 code/text（非 close 错
// 误返回 1000/空——只进审计元数据，不影响转发语义）。
func realtimeCloseErrorOf(err error) (int, string) {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code, closeErr.Text
	}
	return websocket.CloseNormalClosure, ""
}

// isRealtimeTimeoutError 报告读错误是否空闲超时（deadline 到达）。
func isRealtimeTimeoutError(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded) || strings.Contains(err.Error(), "i/o timeout")
}

// resolveRealtimeIdentity 认证二选一（Bearer 优先）：Bearer 走 preauth 全套
// （运行时 + IP 黑名单 + 用户请求限流）；?token= 先 Verify（model 一致性），
// 按 api_key_id 解析运行时后预设 req.Runtime 再过同一 preauth 守卫（镜像
// Bearer 路径的来源保护与限流消费）。
func (c *gatewayChain) resolveRealtimeIdentity(ctx context.Context, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, model string) (*gatewayruntimecache.GatewayRuntime, string, *realtimePrepareFailure) {
	if authorization := strings.TrimSpace(req.Header("authorization")); authorization != "" {
		// Bearer 路径：完整 preauth 管道（失败响应由管道内渲染）。
		if err := c.preauth.PreResolveGatewayRuntime(ctx, res, req, func() {}); err != nil {
			c.handleOrchestratorError(err, req, res, c.preauth.NowMs(), "GET /v1/realtime")
			return nil, "", &realtimePrepareFailure{status: http.StatusInternalServerError}
		}
		if res.HeadersSent() || writableEndedOf(res) {
			return nil, "", &realtimePrepareFailure{status: res.StatusCode()}
		}
		return reqRuntimeOf(req), "bearer", nil
	}
	// ephemeral token 路径（浏览器形态：WS 无自定义头能力）。
	if c.realtimeTokens == nil {
		c.renderRealtimeError(res, http.StatusServiceUnavailable, "realtime token 服务未装配（需要 redis 运行态驱动）", "invalid_request_error", "service_unavailable")
		return nil, "", &realtimePrepareFailure{status: http.StatusServiceUnavailable}
	}
	token := chainRealtimeQueryParam(req, realtimeTokenQueryParam)
	if token == "" {
		c.renderRealtimeError(res, http.StatusUnauthorized, "缺少访问令牌", "invalid_request_error", "invalid_api_key")
		return nil, "", &realtimePrepareFailure{status: http.StatusUnauthorized}
	}
	verify, err := c.realtimeTokens.Verify(ctx, token, model)
	if err != nil {
		switch {
		case errors.Is(err, realtimetoken.ErrInvalidToken):
			c.renderRealtimeError(res, http.StatusUnauthorized, "realtime token 无效或已过期", "invalid_request_error", "invalid_realtime_token")
		case errors.Is(err, realtimetoken.ErrModelMismatch):
			c.renderRealtimeError(res, http.StatusForbidden, "realtime token 与请求模型不一致", "invalid_request_error", "realtime_token_model_mismatch")
		default:
			c.renderRealtimeError(res, http.StatusServiceUnavailable, "realtime token 校验失败", "invalid_request_error", "service_unavailable")
		}
		return nil, "", &realtimePrepareFailure{status: res.StatusCode()}
	}
	if c.cache == nil {
		c.renderRealtimeError(res, http.StatusServiceUnavailable, "realtime 运行时缓存未装配", "invalid_request_error", "service_unavailable")
		return nil, "", &realtimePrepareFailure{status: http.StatusServiceUnavailable}
	}
	runtime, err := c.cache.ReadGatewayRuntimeByAPIKeyIDAsync(ctx, verify.APIKeyID)
	if err != nil {
		c.handleOrchestratorError(err, req, res, c.preauth.NowMs(), "GET /v1/realtime")
		return nil, "", &realtimePrepareFailure{status: http.StatusInternalServerError}
	}
	if runtime.APIKey == nil {
		c.renderRealtimeError(res, http.StatusUnauthorized, "realtime token 绑定的 API Key 不可用", "invalid_request_error", "invalid_api_key")
		return nil, "", &realtimePrepareFailure{status: http.StatusUnauthorized}
	}
	// 预设运行时后走同一 preauth 守卫：ResolveGatewayRuntimeAsync 对已解析请
	// 求直接复用 req.Runtime，IP 黑名单与用户请求限流照常消费。
	req.Runtime = &runtime
	if err := c.preauth.PreResolveGatewayRuntime(ctx, res, req, func() {}); err != nil {
		c.handleOrchestratorError(err, req, res, c.preauth.NowMs(), "GET /v1/realtime")
		return nil, "", &realtimePrepareFailure{status: http.StatusInternalServerError}
	}
	if res.HeadersSent() || writableEndedOf(res) {
		return nil, "", &realtimePrepareFailure{status: res.StatusCode()}
	}
	return reqRuntimeOf(req), "token", nil
}

// reqRuntimeOf 投影 preauth 解析出的运行时（nil 安全的快照拷贝）。
func reqRuntimeOf(req *gatewaypreauth.GatewayRequest) *gatewayruntimecache.GatewayRuntime {
	if req == nil || req.Runtime == nil {
		return nil
	}
	runtime := *req.Runtime
	return &runtime
}

// realtimeSessionCandidates 解析 realtime 候选账户：优先 runtime 快照（选择
// 分组的派发集；动态路由策略为空集），为空时回退按分组模型感知加载；然后
// 按 realtime_session endpoint mode（非空词表须包含；空=无约束，沿
// images/video 同款 opt-in 语义）、支持模型（非空须包含）与可用 Bearer 凭据
// （api_key 账户——oauth 账户的活期 access token 由引擎刷新面持有，WS 首版
// 不经该面）过滤。顺序保持选择器产出（priority 升序）。
func (c *gatewayChain) realtimeSessionCandidates(ctx context.Context, runtime *gatewayruntimecache.GatewayRuntime, model string) []gatewaydispatch.AccountCandidate {
	accounts := runtime.Accounts
	if len(accounts) == 0 && runtime.APIKey != nil && runtime.APIKey.SelectedGroupID != "" {
		if loaded, err := c.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, runtime.APIKey.SelectedGroupID,
			runtime.APIKey.SystemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
				RequestedModel: model,
			}); err == nil {
			accounts = loaded
		} else {
			c.observability.Logger().Warn("realtime_candidate_reload_failed", map[string]any{
				"event": "realtime_candidate_reload_failed", "error": err.Error(),
			}, "realtime 候选账户回退加载失败")
		}
	}
	out := make([]gatewaydispatch.AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if len(account.SupportedEndpointModes) > 0 &&
			!containsTrimmedString(account.SupportedEndpointModes, gatewaypreauth.EndpointModeRealtimeSession) {
			continue
		}
		if len(account.SupportedModels) > 0 && !containsTrimmedString(account.SupportedModels, model) {
			continue
		}
		if strings.TrimSpace(account.APIKey) == "" {
			continue
		}
		out = append(out, account)
	}
	return out
}

// containsTrimmedString 报告 values 是否含 trimmed 精确匹配项。
func containsTrimmedString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

// resolveRealtimeCatalogModel 解析 realtime 目录行（active + realtime 协议；
// 沿 chain_chat 的 openai 聚合目录消费先例——自定义行 + 子供应商内置行）。
// 返回行引用进终态计费（audio token 单价）。
func (c *gatewayChain) resolveRealtimeCatalogModel(ctx context.Context, systemAccountID, model string) *gatewayruntimecache.ProviderModelCatalogItem {
	if c.cache == nil {
		return nil
	}
	items, err := c.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    gatewaypreauth.ProtocolCodeOpenAI,
		SystemAccountID: systemAccountID,
		IncludeUnpriced: true,
	})
	if err != nil {
		return nil
	}
	for index := range items {
		if items[index].Model != model || items[index].Status != "active" {
			continue
		}
		if !containsTrimmedString(items[index].SupportedAPIProtocols, "realtime") {
			continue
		}
		item := items[index]
		return &item
	}
	return nil
}

// chainRealtimeRequestModel 从升级请求查询串取 model（?model=；设计 §2）。
func chainRealtimeRequestModel(req *gatewaypreauth.GatewayRequest) string {
	return strings.TrimSpace(chainRealtimeQueryParam(req, realtimeModelQueryParam))
}

// chainRealtimeQueryParam 从请求解析首个同名查询参数（req.HTTP.URL 优先，
// PathAndQuery 手工解析兜底）。
func chainRealtimeQueryParam(req *gatewaypreauth.GatewayRequest, name string) string {
	if req == nil {
		return ""
	}
	if req.HTTP != nil && req.HTTP.URL != nil {
		if values, ok := req.HTTP.URL.Query()[name]; ok && len(values) > 0 {
			return values[0]
		}
	}
	pathAndQuery := req.PathAndQuery()
	index := strings.Index(pathAndQuery, "?")
	if index < 0 {
		return ""
	}
	for _, pair := range strings.Split(pathAndQuery[index+1:], "&") {
		key, value := pair, ""
		if eq := strings.Index(pair, "="); eq >= 0 {
			key, value = pair[:eq], pair[eq+1:]
		}
		if key != name {
			continue
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			return decoded
		}
		return value
	}
	return ""
}

// chainRealtimeUpstreamQuery 构造上游查询串：model 固定为解析模型；除 token
// （网关认证参数不得外泄上游）外的其余查询参数透传——透传代理语义（设计
// §2：网关不改写客户端的上游参数形态；mock 场景参数同通道）。
func chainRealtimeUpstreamQuery(req *gatewaypreauth.GatewayRequest, model string) url.Values {
	query := url.Values{}
	if req.HTTP != nil && req.HTTP.URL != nil {
		for name, values := range req.HTTP.URL.Query() {
			if name == realtimeTokenQueryParam {
				continue
			}
			for _, value := range values {
				query.Add(name, value)
			}
		}
	}
	query.Set(realtimeModelQueryParam, model)
	return query
}

// renderRealtimeError 渲染 OpenAI 形态错误包（沿签发面错误形态）。
func (c *gatewayChain) renderRealtimeError(res *gatewaypreauth.TrackingWriter, status int, message, errorType, code string) {
	if res.HeadersSent() || res.WritableEnded() {
		return
	}
	kernel.WriteJSON(res, status, map[string]any{
		"error": map[string]any{"message": message, "type": errorType, "code": code},
	})
}

// finalizeRealtimeUpgradeAudit 收口审计：会话元数据（模型/账户/时长/断开原
// 因/usage 事件数，无内容——设计 §1 零存储）+ 状态码 Finalize。session 为
// nil 表示 prepare 阶段失败（错误响应已渲染）；升级成功后 TrackingWriter
// 记录 gorilla 写出的 101。
func (c *gatewayChain) finalizeRealtimeUpgradeAudit(capture gatewaypreauth.AuditCaptureContext, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, session *realtimeSession) {
	if capture == nil {
		return
	}
	if session != nil {
		capture.AddGatewayMetadata("realtime_session", map[string]any{
			"model":         session.model,
			"accountId":     session.account.ID,
			"providerCode":  session.account.ProviderCode,
			"authSource":    session.authSource,
			"durationMs":    session.usageDurationMs(),
			"closeReason":   session.closeReason,
			"usageEvents":   session.usage.usageEvents,
			"audioInTokens": session.usage.inputAudioTokens,
			"audioOutTkns":  session.usage.outputAudioTokens,
		})
	}
	status := res.StatusCode()
	success := status == http.StatusSwitchingProtocols || (status >= http.StatusOK && status < http.StatusBadRequest)
	capture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Success:    success,
		Outcome:    chainRealtimeSecretsAuditOutcomeOf(status),
		StatusCode: status,
	})
}

// enqueueRealtimeTerminalUsage 终态落账（设计 §5）：endpoint=/v1/realtime、
// audio token 计量、text token 仅目录行有文本价时同报（当前 realtime 行无文
// 本价——契约 §4.4 不编造）；无 usage 事件（空会话）0 计费 + usage_missing。
// 经既有 spool 链（c.finalizationUsage 即 spooledUsageRecorder，沿 media job
// enqueueMediaJobTerminalUsage 先例）；cost 经同步定价面（目录 audio 单价）。
func (c *gatewayChain) enqueueRealtimeTerminalUsage(session *realtimeSession) {
	if c.finalizationUsage == nil {
		return
	}
	statusCode := http.StatusOK
	input := gatewayusage.UsageRecordInput{
		TraceID:       session.traceID,
		TrafficSource: gatewayTrafficSource,
		// 身份维度对齐既有 usage 记录约定（SystemAccountID 取 API Key 属
		// 主，AccountOwnerSystemAccountID 取账户行属主）。
		SystemAccountID:             session.systemAccount,
		APIKeyID:                    session.apiKeyID,
		AccountID:                   session.account.ID,
		AccountOwnerSystemAccountID: session.account.AccountOwnerSystemAccountID,
		Endpoint:                    realtimeUsageEndpoint,
		ProviderCode:                session.account.ProviderCode,
		ProviderProtocolProfileID:   session.account.ProviderProtocolProfileID,
		Model:                       session.model,
		UpstreamModel:               session.model,
		StatusCode:                  &statusCode,
		Success:                     true,
		RequestSnapshot: map[string]any{
			"authSource":     session.authSource,
			"closeReason":    session.closeReason,
			"usageEvents":    session.usage.usageEvents,
			"sessionSeconds": session.usageSessionSeconds(),
		},
	}
	// 记账 scope 五元组：分组字段来自运行时 GroupAccess，账户字段来自候选
	// 行（归一化由 spool 链 NormalizeUsageRecordInput 完整性规则兜底）。
	if session.runtime != nil && session.runtime.GroupAccess != nil {
		access := session.runtime.GroupAccess
		input.GroupID = session.groupID
		input.GroupOwnerSystemAccountID = access.GroupOwnerSystemAccountID
		input.GroupAccessType = access.GroupAccessType
		input.GroupAuthorizationID = derefString(access.GroupAuthorizationID)
		input.GroupAuthorizationSourceType = derefString(access.GroupAuthorizationSourceType)
		input.GroupAuthorizationSourceTeamID = derefString(access.GroupAuthorizationSourceTeamID)
	}
	input.AccountAccessType = session.account.AccountAccessType
	input.AccountAuthorizationID = derefString(session.account.AccountAuthorizationID)
	input.AccountAuthorizationSourceType = derefString(session.account.AccountAuthorizationSourceType)
	input.AccountAuthorizationSourceTeamID = derefString(session.account.AccountAuthorizationSourceTeamID)

	if session.usage.usageEvents > 0 {
		inputAudio := int(session.usage.inputAudioTokens)
		outputAudio := int(session.usage.outputAudioTokens)
		input.InputAudioTokens = &inputAudio
		input.OutputAudioTokens = &outputAudio
		// text token 仅目录行有文本价时同报（任务约束；当前 realtime 行无
		// 文本价，维度留空不虚计）。
		if item := session.catalogItem; item != nil && (item.InputUsdPer1M != nil || item.OutputUsdPer1M != nil) {
			inputText := int(session.usage.inputTextTokens)
			outputText := int(session.usage.outputTextTokens)
			input.InputTokens = &inputText
			input.OutputTokens = &outputText
		}
		if c.finalizationPricing != nil {
			if cost := c.finalizationPricing.EstimateCost(gatewayusage.PricingCostInput{
				ProviderCode:      session.account.ProviderCode,
				SystemAccountID:   session.systemAccount,
				Model:             session.model,
				InputAudioTokens:  input.InputAudioTokens,
				OutputAudioTokens: input.OutputAudioTokens,
				InputTokens:       input.InputTokens,
				OutputTokens:      input.OutputTokens,
			}); cost != nil && *cost > 0 {
				value := *cost
				input.CostUsd = &value
			}
		}
	} else {
		input.UsageMissing = true
	}
	if duration := session.usageDurationMs(); duration != nil {
		input.DurationMs = duration
	}
	if err := c.finalizationUsage.EnqueueUsageRecord(gatewayusage.Ctx(session.ctx), input); err != nil {
		c.observability.Logger().Warn("realtime_usage_enqueue_failed", map[string]any{
			"event": "realtime_usage_enqueue_failed", "traceId": session.traceID, "error": err.Error(),
		}, "realtime 终态用量记录入队失败")
	}
}

// usageDurationMs 会话时长（受理 → 终态）。
func (s *realtimeSession) usageDurationMs() *int {
	if s.usageStartedAt.IsZero() || s.closedAt.IsZero() {
		return nil
	}
	duration := int(s.closedAt.Sub(s.usageStartedAt).Milliseconds())
	if duration < 0 {
		duration = 0
	}
	return &duration
}

// usageSessionSeconds 是快照里的秒形态（向下取整）。
func (s *realtimeSession) usageSessionSeconds() int64 {
	if s.usageStartedAt.IsZero() || s.closedAt.IsZero() {
		return 0
	}
	seconds := int64(s.closedAt.Sub(s.usageStartedAt).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}
