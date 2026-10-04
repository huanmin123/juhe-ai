package main

// M2 异步视频任务链（媒体设计 §4.2/§7/§8、契约 §2.2/§2.4/§4.3）：
//
//   - 创建（POST /v1/videos）：走既有派发循环（受理边界生效——引擎 attempt
//     语义原样复用，429/5xx/网络错误换候选、耗尽渲染既有契约，不新写重试）；
//     出站报文经 gatewaymedia 视频注册表 adapter 构造（driver 内短路分支，
//     与 M1 gemini speech adapter 同模式）；2xx + job id = 受理凭据确立，
//     落 media_jobs 后返回统一 job 对象（id 为对外 id，不用上游 id）。
//   - 任务面（GET /v1/videos 列表、GET /v1/videos/{id} 轮询、GET
//     /v1/videos/{id}/content 下载、DELETE /v1/videos/{id} 取消）：不走派发
//     循环，账户亲和直连——查表（id + api_key_id 归属校验）→ 按 account_id
//     水合凭据 → adapter.Build*Request → 复用 gatewayupstream.RequestUpstream
//     直连原上游；轮询响应驱动本地状态与终态 usage 回填（spool 链）。
//   - content：completed 才可下载，http 流式转发（io.Copy 零缓冲落盘，
//     content-type 透传）；上游 404/410 → media_artifact_expired。
//   - 账户已删/禁用 → media_job_unreachable，不换账户（§7 任务面行）。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

// ---------------------------------------------------------------------------
// 路径族判定（/videos 族唯一的 chain 侧词表落点）
// ---------------------------------------------------------------------------

// chainVideoRequestShape 是 /videos 族请求形态。
type chainVideoRequestShape int

const (
	chainVideoShapeNone    chainVideoRequestShape = iota
	chainVideoShapeCreate                         // POST /v1/videos
	chainVideoShapeList                           // GET /v1/videos
	chainVideoShapePoll                           // GET /v1/videos/{id}
	chainVideoShapeContent                        // GET /v1/videos/{id}/content
	chainVideoShapeCancel                         // DELETE /v1/videos/{id}
)

// chainVideoRequestShapeOf 判定方法 + 路径族（媒体设计 §4.2 端点集）。第二
// 返回值是路径上的 job id（poll/content/cancel 形态非空）。剥 /v1 前缀沿链内
// chainStripGatewayVersionPrefix 语义，root 形态（/videos）已由入口改写补 /v1。
func chainVideoRequestShapeOf(method, pathAndQuery string) (chainVideoRequestShape, string) {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	stripped := chainStripGatewayVersionPrefix(strings.ToLower(strings.TrimSpace(path)))
	if stripped == "/videos" {
		switch strings.ToUpper(method) {
		case http.MethodPost:
			return chainVideoShapeCreate, ""
		case http.MethodGet:
			return chainVideoShapeList, ""
		}
		return chainVideoShapeNone, ""
	}
	rest, ok := strings.CutPrefix(stripped, "/videos/")
	if !ok || rest == "" {
		return chainVideoShapeNone, ""
	}
	id, tail, hasTail := strings.Cut(rest, "/")
	if id == "" {
		return chainVideoShapeNone, ""
	}
	if !hasTail {
		switch strings.ToUpper(method) {
		case http.MethodGet:
			return chainVideoShapePoll, id
		case http.MethodDelete:
			return chainVideoShapeCancel, id
		}
		return chainVideoShapeNone, ""
	}
	if tail == "content" && strings.ToUpper(method) == http.MethodGet {
		return chainVideoShapeContent, id
	}
	return chainVideoShapeNone, ""
}

// chainIsVideoCreateRequest 报告请求是否视频创建形态（响应面拦截判定）。
func chainIsVideoCreateRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	shape, _ := chainVideoRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	return shape == chainVideoShapeCreate
}

// chainIsVideoTaskPlaneRequest 报告请求是否任务面形态（列表/轮询/下载/取消，
// 不走派发循环）。
func chainIsVideoTaskPlaneRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	shape, _ := chainVideoRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	switch shape {
	case chainVideoShapeList, chainVideoShapePoll, chainVideoShapeContent, chainVideoShapeCancel:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// provider → adapter 注册键映射（媒体设计 §5：注册表挂载，不进组合根 switch）
// ---------------------------------------------------------------------------

// chainVideoAdapterKeyOfProvider 把账户 provider_code 归一为视频 adapter
// 注册键。映射规则：
//   - openai 族账户 → "openai"：provider_code 为 openai（API Key 直连本体）
//     或 gpt（openai 的 OAuth 子供应商代码，协议同族）都归一到 openai
//     adapter（对外契约即本体，契约 §4.3）；注册键即 videoadapter.go 的
//     videoAdapters 表键（VideoAdapterForProvider 内部小写归一）。
//   - gemini 账户 → "gemini"（M3：api_key 与 google_oauth 两类账户同键，
//     认证头差异由任务面按协议/账户类型构造）：Veo predictLongRunning
//     形态（契约 §5.2），URL 前缀按账户 base_url + /v1beta。
//   - glm 账户 → "glm"（M3：CogVideoX videos/generations 形态，契约 §7.1；
//     账户 provider_code 恒为 glm，无子供应商代码）：api_key Bearer 认证
//     （沿链上 openai 族认权分支），URL 按 chainGlmVideoUpstreamURL 归一
//     （/api/paas/v4 服务根，不走 /v1 强制补缀的 openai 归一）。
//   - minimax 账户 → "minimax"（M3：Hailuo video_generation 形态，契约
//     §8.1）：api_key Bearer 认证（沿链上 openai 族认权分支），出站路径
//     为 /v1 前缀的 openai 族形态（video_generation / query/
//     video_generation），URL 走 gatewayopenai.BuildUpstreamURL 归一
//     （base 强制 /v1 结尾、path 剥 /v1 前缀去重——api.minimax.chat 与
//     api.minimaxi.com 两种官方 host 均适用，无需专用归一函数）。
//   - volcengine 账户 → "volcengine"（M3：Seedance contents/generations/
//     tasks 形态，契约 §9.1）：api_key Bearer 认证（沿链上 openai 族认权
//     分支），出站路径自带 /api/v3 服务根，URL 按
//     chainVolcengineVideoUpstreamURL 归一（官方根 ark.cn-beijing.volces.com
//     去重，不走 /v1 强制补缀的 openai 归一——同 glm /api/paas/v4 先例）。
//   - 其余 provider（anthropic/deepseek/hybrid 等）→ ""：无对应 adapter。
//     创建链在 driver 构造点显式失败（能力缺失不静默回退，媒体设计 §5
//     禁止项：不把厂商差异写进组合根 switch）；qwen 系 M3+ 起只新增
//     注册表条目与本映射分支。
func chainVideoAdapterKeyOfProvider(providerCode string) string {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "openai", "gpt":
		return "openai"
	case "gemini":
		return "gemini"
	case "glm":
		return "glm"
	case "minimax":
		return "minimax"
	case "volcengine":
		return "volcengine"
	default:
		return ""
	}
}

// chainVideoAdapterForAccount 解析账户的视频 adapter；nil 表示该 provider 无
// 视频 adapter（调用方按能力缺失处理）。
func chainVideoAdapterForAccount(providerCode string) gatewaymedia.VideoProviderAdapter {
	key := chainVideoAdapterKeyOfProvider(providerCode)
	if key == "" {
		return nil
	}
	return gatewaymedia.VideoAdapterForProvider(key)
}

// ---------------------------------------------------------------------------
// 创建受理前参数错误短路（媒体设计 §7 创建行、契约 §2.4 规则 2）
// ---------------------------------------------------------------------------

// chainVideoCreateDeterministicParamStatus 报告状态码是否媒体创建的确定性
// 参数类 4xx：400（参数语义错误）/ 413（请求体过大）/ 422（语义无法表达）
// ——同一请求换任何账户都会原样重演（媒体设计 §7：换账户无效，不切换）。
// 401/403/429/5xx/网络错误不在此列，保持受理前可切换语义。
func chainVideoCreateDeterministicParamStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// chainMediaCreateParamErrorShortCircuit 报告失败派发输入是否命中"创建参数
// 错误短路"：视频 lane（LaneVideo；POST /v1/videos 创建形态——音频同步
// lane 的受理凭据是 2xx 响应头到达，参数 400 属普通失败语义，不在此列）
// + 确定性参数类 4xx。消费点是 chain_ports.go HandleFailedUpstreamResponse
// （审计尝试与 usage 失败记录落账之后、SkipAccount 决策之前）。
func chainMediaCreateParamErrorShortCircuit(requestLane string, statusCode int) bool {
	return requestLane == string(gatewayproto.LaneVideo) &&
		chainVideoCreateDeterministicParamStatus(statusCode)
}

// chainRebuiltUpstreamErrorResponse 用失败面已捕获的响应体重建可读上游响应
// （失败派发器读尽并关闭了原始 body；短路透传路径以捕获体复原，供响应面
// 渲染客户端可见的错误状态与正文）。response 为 nil 返回 nil。
func chainRebuiltUpstreamErrorResponse(response *gatewayupstream.GatewayUpstreamResponse, bodyText string) *gatewayupstream.GatewayUpstreamResponse {
	if response == nil {
		return nil
	}
	return gatewayupstream.NewGatewayUpstreamResponseForTransform(
		response.Status(), response.Header, io.NopCloser(strings.NewReader(bodyText)))
}

// ---------------------------------------------------------------------------
// 创建链计划（driver 出站构造与响应面落表共用的一次性解析）
// ---------------------------------------------------------------------------

// chainVideoCreatePlan 是视频创建请求的解析产物：adapter 出站报文 + 归一
// 参数（params_applied/params_ignored 回显来源）+ provider_options 生效键名
// 摘要（provider_options_applied 回显来源，契约 §2.4 规则 4）。
type chainVideoCreatePlan struct {
	adapter                gatewaymedia.VideoProviderAdapter
	createRequest          gatewaymedia.VideoCreateOutput
	params                 gatewaymedia.NormalizedVideoParams
	providerOptionsApplied []string
}

// chainVideoCreatePlanOf 解析请求体并构造 adapter 出站报文。body 为 nil
// （请求体未解析且无原始体）时返回 (nil, nil)（沿 geminiSpeechRequest 语义：
// 由 capability/url 链路以各自错误语义拒绝）。参数能力边界错误
// （ErrParamUnsupported / L2 形态错误 / provider_options 形态错误）在此包装为
// GatewayRequestValidationError——本地 400 不换账户（媒体设计 §7、契约 §2.4
// 规则 2），沿 dispatch 错误通路渲染 invalid_request_error；不包装会落入 503
// "上游暂时不可用"契约，把客户端参数错误误报成上游故障
// （geminiSpeechAdapterBoundaryError 同裁决）。provider 能力缺失（无 adapter）
// 保持原生错误（该分支在派发侧先被能力门拦截，到达即配置异常）。
func chainVideoCreatePlanOf(body map[string]any, req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) (*chainVideoCreatePlan, error) {
	adapter := chainVideoAdapterForAccount(account.ProviderCode)
	if adapter == nil {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 暂不支持视频生成", account.ID, account.ProviderCode)
	}
	if body == nil {
		return nil, nil
	}
	providerOptions, err := chainVideoProviderOptionsOf(body)
	if err != nil {
		return nil, err
	}
	params, err := gatewaymedia.ParseVideoParams(body, adapter.Capabilities())
	if err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	// 模型映射面 M2 不介入（沿 M1 speech 语义：请求模型直达上游；账户
	// canonical 拼写优先）。
	if canonical := canonicalAccountModel(req, account); canonical != "" {
		params.Model = canonical
	}
	createRequest, err := adapter.Create(context.Background(), gatewaymedia.VideoCreateInput{
		Params:          params,
		ProviderOptions: providerOptions,
	})
	if err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	// 生效键名摘要与 merge 同源匹配（AppliedProviderOptionKeys 与
	// MergeProviderOptions 共用 providerOptionKeyMatches）：回显集合恒等于
	// 实际合并的键名集合；providerCode 取 adapter 注册键（merge 的同一入参）。
	providerOptionsApplied := gatewaymedia.AppliedProviderOptionKeys(adapter.Provider(), providerOptions)
	return &chainVideoCreatePlan{adapter: adapter, createRequest: createRequest, params: params, providerOptionsApplied: providerOptionsApplied}, nil
}

// chainVideoProviderOptionsOf 从已解析请求体提取 L3 扩展通道（契约 §2.1）：
// 经 ExtractProviderOptions 做形态校验（子值必须为对象），再投影为对象形态
// （键=provider_code，VideoCreateInput.ProviderOptions 消费面）。
func chainVideoProviderOptionsOf(body map[string]any) (map[string]any, error) {
	raw, present := body[gatewaymedia.ProviderOptionsKey]
	if !present || raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError("provider_options 必须是 JSON 对象")
	}
	extracted, err := gatewaymedia.ExtractProviderOptions(encoded)
	if err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	out := make(map[string]any, len(extracted))
	for key, value := range extracted {
		out[key] = value
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 对外 job 对象（契约 §4.3 + §2.4 规则 5 固定回显字段）
// ---------------------------------------------------------------------------

// chainVideoJobObject 是统一 job 对象（创建/轮询/列表共用渲染）。
// params_applied/params_ignored/provider/provider_job_id 固定携带（契约
// §2.4 规则 5、设计 §6 便于排查）；provider_options_applied 回显 L3 命中
// 子对象键名摘要（契约 §2.4 规则 4，创建时冻结，空数组 = 未使用扩展通道）；
// progress/seconds_length 等按可得性回显（media_jobs 不持久化 progress，
// 轮询响应取上游即时值，列表/终态行省略）。
type chainVideoJobObject struct {
	ID                     string                      `json:"id"`
	Object                 string                      `json:"object"`
	Status                 string                      `json:"status"`
	Progress               *int                        `json:"progress,omitempty"`
	Model                  string                      `json:"model,omitempty"`
	Prompt                 string                      `json:"prompt,omitempty"`
	SecondsLength          *int                        `json:"seconds_length,omitempty"`
	Size                   string                      `json:"size,omitempty"`
	Error                  *gatewaymedia.MediaJobError `json:"error,omitempty"`
	Provider               string                      `json:"provider"`
	ProviderJobID          string                      `json:"provider_job_id"`
	ParamsApplied          []string                    `json:"params_applied"`
	ParamsIgnored          []string                    `json:"params_ignored"`
	ProviderOptionsApplied []string                    `json:"provider_options_applied"`
}

// chainVideoJobObjectOf 由行 + 即时状态渲染 job 对象。liveStatus/liveProgress
// 为空/nil 时回落行内状态（列表与终态 GET）。
func chainVideoJobObjectOf(record *gatewaymedia.MediaJobRecord, liveStatus gatewaymedia.MediaJobStatus, liveProgress *int) chainVideoJobObject {
	status := record.Status
	if liveStatus != "" {
		status = liveStatus
	}
	object := chainVideoJobObject{
		ID:                     record.ID,
		Object:                 "video",
		Status:                 string(status),
		Progress:               liveProgress,
		Model:                  record.RequestSnapshot.Model,
		Prompt:                 record.RequestSnapshot.Prompt,
		Size:                   record.RequestSnapshot.Size,
		Error:                  record.Error,
		Provider:               record.ProviderCode,
		ProviderJobID:          record.UpstreamJobID,
		ParamsApplied:          record.ParamsApplied,
		ParamsIgnored:          record.ParamsIgnored,
		ProviderOptionsApplied: record.RequestSnapshot.ProviderOptionsApplied,
	}
	if record.RequestSnapshot.Seconds != nil {
		seconds := int(*record.RequestSnapshot.Seconds)
		object.SecondsLength = &seconds
	}
	if object.ParamsApplied == nil {
		object.ParamsApplied = []string{}
	}
	if object.ParamsIgnored == nil {
		object.ParamsIgnored = []string{}
	}
	if object.ProviderOptionsApplied == nil {
		object.ProviderOptionsApplied = []string{}
	}
	return object
}

// ---------------------------------------------------------------------------
// 任务面运行时（仓储 + 账户水合 + 上游直连传输面）
// ---------------------------------------------------------------------------

// mediaJobsRuntime 是媒体任务面的运行时依赖（组合根装配；nil 仅组合测试——
// 任务面端点显式 503 降级）。Transport 与派发引擎同源（URL 安全策略、全局
// 并发槽、keep-alive 客户端池共用），账户水合沿 chainAccountsSelector 的凭据
// 解密先例（DecryptJSON + chainBaseURLOf + chainRuntimeCredentialSource）。
type mediaJobsRuntime struct {
	repo      *gatewaymedia.MediaJobsRepo
	db        *sql.DB
	postgres  bool
	secret    string
	transport gatewayupstream.TransportDeps
}

// chainMediaJobAccount 是任务面水合的账户亲和事实（media_jobs 三元组回查）。
// Credentials 保留解密后的凭据对象（M3 gemini：google_oauth 账户的认证头
// 沿 applyGeminiUpstreamAuthHeaders 消费 quota_project_id 等字段，不重复
// 实现认权分支）。
type chainMediaJobAccount struct {
	ID                        string
	OwnerSystemAccountID      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	Status                    string
	Deleted                   bool
	BaseURL                   string
	Credential                string
	Credentials               map[string]any
}

// hydrateAccount 按 account_id 水合账户凭据（媒体设计 §7 账户亲和）。返回
// (nil, nil) 表示行不存在；Deleted/Status 非 active 由调用方按
// media_job_unreachable 语义处理（不换账户）。
func (m *mediaJobsRuntime) hydrateAccount(ctx context.Context, accountID string) (*chainMediaJobAccount, error) {
	var (
		account              chainMediaJobAccount
		credentialsEncrypted string
		deletedAt            string
	)
	row := m.db.QueryRowContext(ctx, `SELECT id, system_account_id, provider_code,
		COALESCE(provider_protocol_profile_id, ''), COALESCE(protocol_code, ''), COALESCE(protocol_version, ''),
		type, status, COALESCE(credentials_encrypted, ''), COALESCE(deleted_at, '')
		FROM `+m.mediaAccountsTable()+` WHERE id = ?`, accountID)
	if err := row.Scan(&account.ID, &account.OwnerSystemAccountID, &account.ProviderCode,
		&account.ProviderProtocolProfileID, &account.ProtocolCode, &account.ProtocolVersion,
		&account.Type, &account.Status, &credentialsEncrypted, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("水合媒体任务账户失败: %w", err)
	}
	account.Deleted = strings.TrimSpace(deletedAt) != ""
	credentials := map[string]any{}
	if credentialsEncrypted != "" {
		if err := accounts.DecryptJSON(m.secret, credentialsEncrypted, &credentials); err != nil {
			return nil, fmt.Errorf("解密媒体任务账户凭据失败: %w", err)
		}
	}
	entries := chainAccountAPIKeyEntries(m.secret, credentials)
	account.Credential = chainRuntimeCredentialSource(account.Type, credentials, chainAPIKeyEntriesFirstKey(entries))
	account.Credentials = credentials
	account.BaseURL = chainBaseURLOf(credentials, account.ProtocolCode, account.ProtocolVersion)
	return &account, nil
}

// mediaAccountsTable 按方言限定 accounts 表名。
func (m *mediaJobsRuntime) mediaAccountsTable() string {
	if m.postgres {
		return "juhe_business.accounts"
	}
	return "accounts"
}

// ---------------------------------------------------------------------------
// 创建响应面：受理凭据确立 → 落表 → 统一 job 对象
// ---------------------------------------------------------------------------

// maxMediaJobObjectBytes 限制上游 job 对象读取（创建/轮询响应都是小 JSON；
// 超限即上游协议异常，不落表）。
const maxMediaJobObjectBytes = 1 << 20

// handleVideoCreateUpstreamResponse 处理视频创建的上游 2xx 响应（chain_v1.go
// handleUpstreamResponse 的视频短路分支）：ParsePollResponse 归一创建响应
// （契约 §4.3 创建响应即 video 对象），拿到 job id 后落 media_jobs 并渲染统一
// job 对象。2xx 但无 id / 解析失败 = 上游协议违约（受理凭据未确立）→ 502，
// 不换账户（引擎 2xx 分支已选定账户，受理后永不切换）。
func (c *gatewayChain) handleVideoCreateUpstreamResponse(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	res *gatewaypreauth.TrackingWriter,
	context *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	upstream *gatewayupstream.GatewayUpstreamResponse,
) gatewayresponse.UpstreamResponseHandlingResult {
	body, readErr := io.ReadAll(io.LimitReader(upstream.Body, maxMediaJobObjectBytes+1))
	_ = upstream.Body.Close()
	if readErr != nil {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "读取上游视频任务响应失败", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	if len(body) > maxMediaJobObjectBytes {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游视频任务响应超过网关处理上限", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	if c.mediaJobs == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务面未装配，无法受理视频任务", "service_unavailable")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	adapter := chainVideoAdapterForAccount(dispatched.Account.ProviderCode)
	if adapter == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游账户缺少视频 adapter，任务受理中断", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	ir, parseErr := adapter.ParsePollResponse(upstream.Status(), body)
	if parseErr != nil || ir == nil || ir.UpstreamJobID == "" {
		message := "上游视频任务响应缺少受理凭据（job id）"
		if parseErr != nil {
			message = parseErr.Error()
		}
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	// 归一参数复读（与 driver 出站构造同源解析，确定性纯函数）。失败属防御
	// 分支（派发侧已成功构造过同源报文）。
	plan, planErr := chainVideoCreatePlanOf(chainVideoRequestBodyOf(req), req, dispatched.Account)
	if planErr != nil || plan == nil {
		message := "视频创建请求参数复析失败"
		if planErr != nil {
			message = planErr.Error()
		}
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, message, "internal_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	record := gatewaymedia.MediaJobRecord{
		ID:                        newChainMediaJobID(),
		Kind:                      gatewaymedia.JobKindVideo,
		APIKeyID:                  context.UsageContext.APIKeyID,
		AccountID:                 dispatched.Account.ID,
		ProviderCode:              adapter.Provider(),
		ProviderProtocolProfileID: dispatched.Account.ProviderProtocolProfileID,
		UpstreamJobID:             ir.UpstreamJobID,
		Status:                    ir.Status,
		RequestSnapshot: chainMediaJobSnapshotScopeOf(
			gatewaymedia.MediaJobRequestSnapshot{
				Model:                  plan.params.Model,
				Prompt:                 plan.params.Prompt,
				Seconds:                plan.params.Seconds,
				Size:                   plan.params.Size,
				N:                      plan.params.N,
				ProviderOptionsApplied: plan.providerOptionsApplied,
			},
			dispatched.Account, context),
		Artifact:      ir.Artifact,
		ParamsApplied: plan.params.ParamsApplied,
		ParamsIgnored: plan.params.ParamsIgnored,
	}
	if insertErr := c.mediaJobs.repo.Insert(ctx, record); insertErr != nil {
		// 受理凭据已确立但本地行落库失败：任务已在上游存在，客户端拿到错误
		// 后重试会创建新任务；保留原始错误供排查（不静默降级为成功）。
		slog.Error("视频任务受理后落库失败",
			"event", "media_job_insert_failed", "traceId", context.UsageContext.TraceID,
			"accountId", dispatched.Account.ID, "upstreamJobId", ir.UpstreamJobID,
			"error", insertErr.Error())
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "视频任务受理后落库失败", "internal_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	object := chainVideoJobObjectOf(&record, ir.Status, ir.Progress)
	c.writeMediaVideoJSON(res, http.StatusOK, object)
	return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, ProtocolValidatedSuccess: true}
}

// handleVideoCreateUpstreamErrorPassthrough 处理视频创建参数类 4xx 的短路
// 透传（chain_v1.go handleUpstreamResponse 的视频错误分支）：失败派发器对
// 视频 lane 的 400/413/422 已短路为 ReturnResponse（媒体设计 §7：确定性
// 参数错误不换账户），错误状态与错误体原样透传客户端——不进 chat 语义的
// JSON 检查/协议校验管道（错误改写会把上游参数错误吞成 502 网关错误）。
func (c *gatewayChain) handleVideoCreateUpstreamErrorPassthrough(
	res *gatewaypreauth.TrackingWriter,
	req *gatewaypreauth.GatewayRequest,
	upstream *gatewayupstream.GatewayUpstreamResponse,
) gatewayresponse.UpstreamResponseHandlingResult {
	body, readErr := io.ReadAll(io.LimitReader(upstream.Body, maxMediaJobObjectBytes+1))
	_ = upstream.Body.Close()
	if readErr != nil || len(body) > maxMediaJobObjectBytes {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "读取上游视频任务错误响应失败", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	contentType := strings.TrimSpace(upstream.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	res.Header().Set("Content-Type", contentType)
	res.WriteHeader(upstream.Status())
	_, _ = res.Write(body)
	return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
}

// chainVideoRequestBodyOf 取创建请求的已解析 JSON 对象（body 管线缓存优先，
// 回退原始体解析——创建请求是小 JSON）。
func chainVideoRequestBodyOf(req *gatewaypreauth.GatewayRequest) map[string]any {
	if body := req.ParsedJSONObjectBody(); body != nil {
		return body
	}
	if req.Body == nil || len(req.Body.RawBody) == 0 {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(req.Body.RawBody, &parsed); err != nil {
		return nil
	}
	object, _ := parsed.(map[string]any)
	return object
}

// chainMediaJobSnapshotScopeOf 把创建时派发上下文的记账 scope 五元组冻结进
// request_snapshot（任务终态 usage 行身份维度，媒体设计 §10）。取值与
// applyUsageAccountScope 同源（chain_usage.go 合并路由 3.5）：Account scope
// 取账户视图；账户 BoundGroupID 非空时 Group scope 整体取账户自带五元组，
// 否则取派发 UsageContext 的窗口组五元组——避免"组 ID 属账号组、五元组属
// 窗口组"的混合记录。投影侧（终态 usage 行）经 NormalizeUsageRecordInput
// 的完整性规则兜底（五元组不齐整组清空）。纯元数据，零资源存储不破。
func chainMediaJobSnapshotScopeOf(snapshot gatewaymedia.MediaJobRequestSnapshot, account gatewaydispatch.AccountCandidate, context *gatewaypreauth.DispatchContext) gatewaymedia.MediaJobRequestSnapshot {
	snapshot.AccountAccessType = account.AccountAccessType
	snapshot.AccountAuthorizationID = chainMediaJobDeref(account.AccountAuthorizationID)
	snapshot.AccountAuthorizationSourceType = chainMediaJobDeref(account.AccountAuthorizationSourceType)
	snapshot.AccountAuthorizationSourceTeamID = chainMediaJobDeref(account.AccountAuthorizationSourceTeamID)
	usage := context.UsageContext
	if account.BoundGroupID != nil && *account.BoundGroupID != "" {
		snapshot.GroupID = *account.BoundGroupID
		snapshot.GroupOwnerSystemAccountID = account.GroupOwnerSystemAccountID
		snapshot.GroupAccessType = account.GroupAccessType
		snapshot.GroupAuthorizationID = chainMediaJobDeref(account.GroupAuthorizationID)
		snapshot.GroupAuthorizationSourceType = chainMediaJobDeref(account.GroupAuthorizationSourceType)
		snapshot.GroupAuthorizationSourceTeamID = chainMediaJobDeref(account.GroupAuthorizationSourceTeamID)
		return snapshot
	}
	snapshot.GroupID = usage.GroupID
	snapshot.GroupOwnerSystemAccountID = usage.GroupOwnerSystemAccountID
	snapshot.GroupAccessType = usage.GroupAccessType
	snapshot.GroupAuthorizationID = usage.GroupAuthorizationID
	snapshot.GroupAuthorizationSourceType = usage.GroupAuthorizationSourceType
	snapshot.GroupAuthorizationSourceTeamID = usage.GroupAuthorizationSourceTeamID
	return snapshot
}

// chainMediaJobDeref 解引用可空字符串（账户视图授权字段为 *string）。
func chainMediaJobDeref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// newChainMediaJobID 生成对外 job id（"video_" + 24 hex，媒体设计 §8.2：
// 不使用上游 id，避免跨厂商碰撞与暴露）。
func newChainMediaJobID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 失败无法生成不可预测对外 id；fail loud 而非退化到可
		// 预测 id。
		panic(fmt.Sprintf("生成媒体任务 id 失败: %v", err))
	}
	return "video_" + hex.EncodeToString(buf[:])
}

// ---------------------------------------------------------------------------
// 任务面处理（账户亲和直连）
// ---------------------------------------------------------------------------

// serveMediaJobTaskPlane 处理任务面请求（chain_v1.go 入口短路分支：preauth
// 认证完成之后、派发预检之前）。auditCapture 是链入口已构造的 G17 审计捕获
// （chain_v1 newAuditCapture）：任务面短路路径不经派发引擎的 attempt/finalize
// 面捕获不会自然 Finalize，这里接最小审计——绑定 API Key 身份 + 任务 id 元数
// 据 + 请求级 Finalize（方法/路径/状态码；不记请求/响应 body，媒体设计 §8.1.4
// 零资源存储：任务面 GET/DELETE 无 body，content 产物字节不进审计）。
func (c *gatewayChain) serveMediaJobTaskPlane(ctx context.Context, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, traceID string, auditCapture gatewaypreauth.AuditCaptureContext) {
	if c.mediaJobs == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务面未装配", "service_unavailable")
		c.finalizeMediaJobTaskPlaneAudit(auditCapture, req, res, "")
		return
	}
	if req.Runtime == nil || req.Runtime.APIKey == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusUnauthorized, "缺少网关 API Key 身份", "invalid_api_key")
		c.finalizeMediaJobTaskPlaneAudit(auditCapture, req, res, "")
		return
	}
	shape, jobID := chainVideoRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	apiKeyID := req.Runtime.APIKey.ID
	switch shape {
	case chainVideoShapeList:
		c.serveMediaVideoList(ctx, res, req, apiKeyID)
	case chainVideoShapePoll:
		c.serveMediaVideoPoll(ctx, res, req, traceID, apiKeyID, jobID)
	case chainVideoShapeContent:
		c.serveMediaVideoContent(ctx, res, req, apiKeyID, jobID)
	case chainVideoShapeCancel:
		c.serveMediaVideoCancel(ctx, res, req, apiKeyID, jobID)
	default:
		c.renderMediaVideoLocalError(res, req, http.StatusNotFound, "资源不存在", "not_found")
	}
	c.finalizeMediaJobTaskPlaneAudit(auditCapture, req, res, jobID)
}

// finalizeMediaJobTaskPlaneAudit 收口任务面短路路径的最小审计：身份绑定
// （API Key 属主 + Key id）、任务 id 网关元数据与请求级 Finalize。状态码取
// TrackingWriter 观测值（content 流式转发后仍可观测）；Finalize 幂等，链入口
// 的 defer CancelAuditCapture 对已 Finalize 的捕获是 no-op。capture 为 nil
// （组合测试未装配审计面）保持静默。
func (c *gatewayChain) finalizeMediaJobTaskPlaneAudit(capture gatewaypreauth.AuditCaptureContext, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, jobID string) {
	if capture == nil {
		return
	}
	capture.BindContext(gatewaypreauth.AuditGatewayContext{
		SystemAccountID: apiKeyOwnerSystemAccountIDOf(req),
		APIKeyID:        apiKeyIDOfGatewayRequest(req),
	})
	if jobID != "" {
		capture.AddGatewayMetadata("media_job_task_plane", map[string]any{"jobId": jobID})
	}
	status := res.StatusCode()
	capture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Success:    status >= http.StatusOK && status < http.StatusBadRequest,
		Outcome:    chainMediaJobAuditOutcomeOf(status),
		StatusCode: status,
	})
}

// chainMediaJobAuditOutcomeOf 归一任务面状态码到审计 Outcome 词表：4xx/5xx
// 是网关本地裁决（任务面无派发引擎 upstream attempt 面），统一 gateway_failed。
func chainMediaJobAuditOutcomeOf(status int) gatewayusage.AuditOutcome {
	if status >= http.StatusOK && status < http.StatusBadRequest {
		return gatewayusage.AuditOutcomeSuccess
	}
	return gatewayusage.AuditOutcomeGatewayFailed
}

// apiKeyIDOfGatewayRequest 取网关 API Key id（任务面认证后可得；缺身份时空）。
func apiKeyIDOfGatewayRequest(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.Runtime == nil || req.Runtime.APIKey == nil {
		return ""
	}
	return req.Runtime.APIKey.ID
}

// loadMediaVideoJob 取行并做归属校验：未命中（不存在或非本人任务）统一 404
// （归属信息不泄露）。
func (c *gatewayChain) loadMediaVideoJob(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID, jobID string) *gatewaymedia.MediaJobRecord {
	record, err := c.mediaJobs.repo.GetByIDAndAPIKey(ctx, jobID, apiKeyID)
	if err != nil {
		if errors.Is(err, gatewaymedia.ErrMediaJobNotFound) {
			c.renderMediaVideoLocalError(res, req, http.StatusNotFound, "Video not found: "+jobID, "video_not_found")
			return nil
		}
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "查询媒体任务失败", "internal_error")
		return nil
	}
	return record
}

// loadMediaVideoAccount 水合账户亲和三元组：行不存在/已删/禁用统一
// media_job_unreachable（503，不换账户——媒体设计 §7 任务面行）。
func (c *gatewayChain) loadMediaVideoAccount(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, record *gatewaymedia.MediaJobRecord) *chainMediaJobAccount {
	account, err := c.mediaJobs.hydrateAccount(ctx, record.AccountID)
	if err != nil {
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "水合媒体任务账户失败", "internal_error")
		return nil
	}
	if account == nil || account.Deleted || !strings.EqualFold(account.Status, "active") {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务账户不可用（已删除或禁用）", "media_job_unreachable")
		return nil
	}
	if chainVideoAdapterForAccount(account.ProviderCode) == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务账户供应商暂不支持视频任务面", "media_job_unreachable")
		return nil
	}
	return account
}

// serveMediaVideoList GET /v1/videos：本地行列表（按归属 Key 倒序），不经上游
// （媒体设计 §8.2：不为闲置任务消耗上游配额）。
func (c *gatewayChain) serveMediaVideoList(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID string) {
	limit := chainMediaVideoListLimitOf(req.PathAndQuery())
	records, err := c.mediaJobs.repo.ListByAPIKey(ctx, apiKeyID, limit, 0)
	if err != nil {
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "查询媒体任务列表失败", "internal_error")
		return
	}
	data := make([]chainVideoJobObject, 0, len(records))
	for _, record := range records {
		data = append(data, chainVideoJobObjectOf(record, "", nil))
	}
	c.writeMediaVideoJSON(res, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// chainMediaVideoListLimitOf 解析 ?limit=（默认 20，上限 100，非法回落默认）。
func chainMediaVideoListLimitOf(pathAndQuery string) int {
	_, query := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	for _, pair := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(pair, "=")
		if key != "limit" {
			continue
		}
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 100 {
			return parsed
		}
	}
	return 20
}

// serveMediaVideoPoll GET /v1/videos/{id}：终态行直接回本地对象（终态后不再
// 被轮询推进，mediajobir Terminal 语义）；非终态行账户亲和转发，响应归一后
// 推进本地状态，completed/failed 终态触发 usage spool 回填。
func (c *gatewayChain) serveMediaVideoPoll(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, traceID, apiKeyID, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, apiKeyID, jobID)
	if record == nil {
		return
	}
	if record.Status.Terminal() {
		c.writeMediaVideoJSON(res, http.StatusOK, chainVideoJobObjectOf(record, "", nil))
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainVideoAdapterForAccount(account.ProviderCode)
	method, path, _ := adapter.BuildPollRequest(record.UpstreamJobID)
	response, upstreamErr := c.mediaJobRequestUpstream(ctx, account, method, path, nil)
	if upstreamErr != nil {
		c.renderMediaVideoTransportError(res, req, upstreamErr)
		return
	}
	body, status := c.readMediaJobUpstreamObject(res, req, response)
	if body == nil {
		return
	}
	var statusErr *gatewaymedia.UpstreamStatusError
	ir, parseErr := adapter.ParsePollResponse(status, body)
	if parseErr != nil {
		if errors.As(parseErr, &statusErr) {
			if statusErr.StatusCode == http.StatusNotFound {
				// 契约 §2.6：上游 404 查询且本地非终态 → 保持本地状态直至
				// TTL 过期（不得伪造终态）；回显本地对象。
				c.writeMediaVideoJSON(res, http.StatusOK, chainVideoJobObjectOf(record, "", nil))
				return
			}
			c.renderMediaVideoLocalError(res, req, http.StatusBadGateway,
				fmt.Sprintf("上游视频任务轮询失败（状态码 %d）", statusErr.StatusCode), "upstream_error")
			return
		}
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游视频任务响应解析失败: "+parseErr.Error(), "upstream_error")
		return
	}
	if ir.Status.Terminal() {
		// 终态计费（媒体设计 §10：任务终态由轮询响应驱动回填）：completed 按
		// OutputVideoSeconds × 静态目录秒价估成本（契约 §2.8 视频网关自算
		// 口径），失败/无秒（usage_missing）不虚计；成本随终态一次落行。
		costUsd := chainMediaVideoTerminalCostUsd(record, ir)
		updated, updateErr := c.mediaJobs.repo.UpdateTerminal(ctx, record.ID, ir.Status, ir.Error, ir.Artifact, ir.Usage, costUsd)
		if updateErr != nil {
			c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "回填媒体任务终态失败", "internal_error")
			return
		}
		// RowsAffected==1 才入队 usage 与计费：0 表示并发方（另一轮询
		// goroutine 或清理 job 的 expired 终态化）已先落终态，本次是重复
		// 终态——终态只入队一次（否则同 job 产生两条 usage 行/双份计费）。
		// 行内终态已是事实，本响应照常回显终态对象。
		if updated == 1 {
			record.CostUsd = costUsd
			c.enqueueMediaVideoTerminalUsage(ctx, traceID, apiKeyOwnerSystemAccountIDOf(req), record, account, ir, costUsd)
		}
	} else if ir.Status != record.Status {
		// 0 行 = 行已被并发方终态化（UpdateStatus 的非终态守卫），非终态
		// 推进让位于已落终态，无副作用可跳过。
		if _, updateErr := c.mediaJobs.repo.UpdateStatus(ctx, record.ID, ir.Status); updateErr != nil {
			c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "更新媒体任务状态失败", "internal_error")
			return
		}
	}
	record.Status = ir.Status
	if ir.Error != nil {
		record.Error = ir.Error
	}
	c.writeMediaVideoJSON(res, http.StatusOK, chainVideoJobObjectOf(record, ir.Status, ir.Progress))
}

// serveMediaVideoContent GET /v1/videos/{id}/content：completed 才可下载；
// 上游产物 http 流式转发（io.Copy 零缓冲落盘，content-type 透传——媒体设计
// §8.1.2 纯流式透传代理）。下载定位按 adapter 声明二分：openai 族由
// base_url + job id 构造（BuildContentRequest，认证头携带）；Veo 形态
// （ContentFromArtifact，M3）直连轮询冻结的绝对签名 URL（无账户凭据，契约
// §5.2；URL 缺失即产物从未产生或已被清理 → media_artifact_expired）。
// 上游 404/410 → media_artifact_expired（产物可取回性由上游时效决定，
// 网关不补救，§8.1.5）。
func (c *gatewayChain) serveMediaVideoContent(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, apiKeyID, jobID)
	if record == nil {
		return
	}
	if record.Status != gatewaymedia.JobStatusCompleted {
		c.renderMediaVideoLocalError(res, req, http.StatusConflict,
			"视频任务未完成，产物不可下载（当前状态 "+string(record.Status)+"）", "media_job_not_completed")
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainVideoAdapterForAccount(account.ProviderCode)
	var response *gatewayupstream.GatewayUpstreamResponse
	var upstreamErr error
	if adapter.ContentFromArtifact() {
		artifactURL := strings.TrimSpace(record.Artifact.ContentURL)
		if artifactURL == "" {
			c.renderMediaVideoLocalError(res, req, http.StatusGone, "视频产物已过期或已被上游清除", "media_artifact_expired")
			return
		}
		response, upstreamErr = c.mediaJobArtifactRequestUpstream(ctx, artifactURL)
	} else {
		method, path := adapter.BuildContentRequest(record.UpstreamJobID)
		response, upstreamErr = c.mediaJobRequestUpstream(ctx, account, method, path, nil)
	}
	if upstreamErr != nil {
		c.renderMediaVideoTransportError(res, req, upstreamErr)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.Status() == http.StatusNotFound || response.Status() == http.StatusGone {
		c.renderMediaVideoLocalError(res, req, http.StatusGone, "视频产物已过期或已被上游清除", "media_artifact_expired")
		return
	}
	if response.Status() < http.StatusOK || response.Status() >= http.StatusMultipleChoices {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway,
			fmt.Sprintf("上游视频产物下载失败（状态码 %d）", response.Status()), "upstream_error")
		return
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "video/mp4"
	}
	res.Header().Set("Content-Type", contentType)
	if contentLength := response.Header.Get("Content-Length"); contentLength != "" {
		res.Header().Set("Content-Length", contentLength)
	}
	res.WriteHeader(http.StatusOK)
	res.Flush()
	// 零资源存储（§8.1）：边收边转发，不落盘不缓存。
	if _, copyErr := io.Copy(res, response.Body); copyErr != nil {
		// 头已发出，无法改写状态码；记录传输中断供排查。
		c.observability.Logger().Warn("media_video_content_stream_interrupted", map[string]any{
			"event": "media_video_content_stream_interrupted", "jobId": record.ID, "error": copyErr.Error(),
		}, "视频产物流式转发中断")
	}
}

// serveMediaVideoCancel DELETE /v1/videos/{id}：转发上游取消；上游 2xx、
// 404（任务已被上游删除）或 405（上游不暴露取消方法——M3 gemini 裁决：
// operations :cancel 不被支持时本地收敛 cancelled，不把能力缺口透传成
// 客户端错误）均置本地 cancelled，回 204。上游其他失败透传错误、本地行
// 不动。glm（M3，契约 §7.1）无上游取消 API：SupportsCancel=false 时不发
// 上游请求，直接本地收敛 cancelled（专用分支而非 405 收敛——不发必然
// 404/405 的垃圾请求，cancelled 本就是 §2.6 的本地终态语义）。
func (c *gatewayChain) serveMediaVideoCancel(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, apiKeyID, jobID)
	if record == nil {
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainVideoAdapterForAccount(account.ProviderCode)
	if !adapter.SupportsCancel() {
		// UpdateStatus 的非终态守卫：0 行 = 行已终态（并发轮询/清理先落终态），
		// 不把终态行回退为 cancelled；本地收敛语义已达成，仍回 204。
		if _, updateErr := c.mediaJobs.repo.UpdateStatus(ctx, record.ID, gatewaymedia.JobStatusCancelled); updateErr != nil {
			c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "更新媒体任务取消状态失败", "internal_error")
			return
		}
		res.WriteHeader(http.StatusNoContent)
		return
	}
	method, path := adapter.BuildCancelRequest(record.UpstreamJobID)
	response, upstreamErr := c.mediaJobRequestUpstream(ctx, account, method, path, nil)
	if upstreamErr != nil {
		c.renderMediaVideoTransportError(res, req, upstreamErr)
		return
	}
	_ = response.Body.Close()
	status := response.Status()
	if (status >= http.StatusOK && status < http.StatusMultipleChoices) || status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		// UpdateStatus 的非终态守卫：0 行 = 行已终态（并发轮询/清理先落终态），
		// 不把终态行回退为 cancelled；上游已确认取消/删除，仍回 204。
		if _, updateErr := c.mediaJobs.repo.UpdateStatus(ctx, record.ID, gatewaymedia.JobStatusCancelled); updateErr != nil {
			c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "更新媒体任务取消状态失败", "internal_error")
			return
		}
		res.WriteHeader(http.StatusNoContent)
		return
	}
	c.renderMediaVideoLocalError(res, req, http.StatusBadGateway,
		fmt.Sprintf("上游视频任务取消失败（状态码 %d）", status), "upstream_error")
}

// ---------------------------------------------------------------------------
// 任务面上游直连（复用派发引擎同源传输面）
// ---------------------------------------------------------------------------

// glmVideoServiceRoot 是 glm 媒体出站路径自带的服务根（契约 §7.1：
// open.bigmodel.cn 官方端点 /api/paas/v4/videos/generations 族）。
const glmVideoServiceRoot = "/api/paas/v4"

// chainGlmVideoUpstreamURL 归一 glm 媒体任务面的上游 URL：base + adapter
// 出站路径做直拼，base 已含 /api/paas/v4 服务根时去重（镜像 gatewaygemini.
// BuildUpstreamURL 的 /v1beta 去重先例）。不得走 gatewayopenai.
// BuildUpstreamURL——它对非 /v1 结尾的 base 强制补 /v1（endpoint_test.go 钉
// 住的 openai 族契约），会把官方根 https://open.bigmodel.cn/api/paas/v4 错
// 拼 /api/paas/v4/v1/...（智谱GLM账号接入.md「网关把请求发往 base_url +
// 路径」的既有裁决）。glm coding 根（/api/coding/paas/v4）不含通用服务根，
// 不做去重直拼（coding 计划面向编码模型，视频能力以通用根为准）。
func chainGlmVideoUpstreamURL(baseURL, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("glm 媒体账户 base_url 无效: %q", baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	suffix := path
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	if suffix != glmVideoServiceRoot && strings.HasSuffix(strings.ToLower(basePath), glmVideoServiceRoot) {
		if trimmed, ok := strings.CutPrefix(suffix, glmVideoServiceRoot); ok && trimmed != "" {
			suffix = trimmed
		}
	}
	parsed.Path = basePath + suffix
	return parsed.String(), nil
}

// volcengineVideoServiceRoot 是 volcengine 媒体出站路径自带的服务根（契约
// §9.1：ark.cn-beijing.volces.com 官方端点 /api/v3/contents/generations/
// tasks 族）。
const volcengineVideoServiceRoot = "/api/v3"

// chainVolcengineVideoUpstreamURL 归一 volcengine 媒体任务面的上游 URL：base
// + adapter 出站路径做直拼，base 已含 /api/v3 服务根时去重（镜像
// chainGlmVideoUpstreamURL 的 /api/paas/v4 去重先例）。不得走 gatewayopenai.
// BuildUpstreamURL——它对非 /v1 结尾的 base 强制补 /v1（endpoint_test.go 钉
// 住的 openai 族契约），会把官方根 https://ark.cn-beijing.volces.com 错拼
// /v1/api/v3/...。
func chainVolcengineVideoUpstreamURL(baseURL, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("volcengine 媒体账户 base_url 无效: %q", baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	suffix := path
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	if suffix != volcengineVideoServiceRoot && strings.HasSuffix(strings.ToLower(basePath), volcengineVideoServiceRoot) {
		if trimmed, ok := strings.CutPrefix(suffix, volcengineVideoServiceRoot); ok && trimmed != "" {
			suffix = trimmed
		}
	}
	parsed.Path = basePath + suffix
	return parsed.String(), nil
}

// mediaJobRequestUpstream 账户亲和直连上游（不走派发循环）：URL 由账户
// base_url + adapter 出站路径归一——gemini 协议走 gatewaygemini.
// BuildUpstreamURL（/v1beta 前缀与 base 去重，M1 speech 创建链同先例），
// glm 媒体路径走 chainGlmVideoUpstreamURL（/api/paas/v4 服务根去重，M3
// glm cogvideo adapter），volcengine 媒体路径走 chainVolcengineVideoUpstreamURL
// （/api/v3 服务根去重，M3 seedance adapter，契约 §9.1），其余走
// gatewayopenai.BuildUpstreamURL（/v1 后缀形态）；认证头按协议构造：gemini
// 沿 applyGeminiUpstreamAuthHeaders（api_key → X-Goog-Api-Key、
// google_oauth → Bearer + 可选 x-goog-user-project，与主链认权分支同一实现，
// 不重复实现），其余维持最小 Bearer 集（任务面身份=网关，不透传客户端头
// ——沿 buildGeminiCodeAssistRequestParts 先例；glm/volcengine 账户即
// Bearer API Key，契约 §7.1/§9.1）；传输复用引擎 TransportDeps（URL 安全
// 策略 / 全局并发槽 / keep-alive 池）。
func (c *gatewayChain) mediaJobRequestUpstream(ctx context.Context, account *chainMediaJobAccount, method, path string, body []byte) (*gatewayupstream.GatewayUpstreamResponse, error) {
	adapterKey := chainVideoAdapterKeyOfProvider(account.ProviderCode)
	if adapterKey == "" {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 无视频 adapter", account.ID, account.ProviderCode)
	}
	var upstreamURL string
	if chainIsGeminiProtocolProfile(account.ProtocolCode, account.ProtocolVersion) {
		built, err := gatewaygemini.BuildUpstreamURL(account.BaseURL, path, false)
		if err != nil {
			return nil, err
		}
		upstreamURL = built
	} else if adapterKey == "glm" {
		built, err := chainGlmVideoUpstreamURL(account.BaseURL, path)
		if err != nil {
			return nil, err
		}
		upstreamURL = built
	} else if adapterKey == "volcengine" {
		built, err := chainVolcengineVideoUpstreamURL(account.BaseURL, path)
		if err != nil {
			return nil, err
		}
		upstreamURL = built
	} else {
		upstreamURL = gatewayopenai.BuildUpstreamURL(account.BaseURL, path)
	}
	headers := http.Header{}
	if chainIsGeminiProtocolProfile(account.ProtocolCode, account.ProtocolVersion) {
		applyGeminiUpstreamAuthHeaders(headers, gatewaydispatch.AccountCandidate{
			Type:        account.Type,
			Credentials: account.Credentials,
		}, account.Credential)
	} else if account.Credential != "" {
		headers.Set("Authorization", "Bearer "+account.Credential)
	}
	headers.Set("Accept", "application/json")
	return gatewayupstream.RequestUpstream(ctx, upstreamURL, gatewayupstream.UpstreamRequestOptions{
		Method: method,
		Header: headers,
		Body:   body,
		Signal: ctx,
	}, c.mediaJobs.transport)
}

// mediaJobArtifactRequestUpstream 直连 completed 任务的产物 URL（M3 gemini：
// Veo 的 GCS 签名 URL，凭据内嵌于 URL，契约 §5.2——不携带任何账户认证头，
// 带 Authorization 反而会被 GCS 拒绝）；传输复用引擎 TransportDeps（URL
// 安全策略与任务面直连同源）。
func (c *gatewayChain) mediaJobArtifactRequestUpstream(ctx context.Context, artifactURL string) (*gatewayupstream.GatewayUpstreamResponse, error) {
	return gatewayupstream.RequestUpstream(ctx, artifactURL, gatewayupstream.UpstreamRequestOptions{
		Method: http.MethodGet,
		Signal: ctx,
	}, c.mediaJobs.transport)
}

// readMediaJobUpstreamObject 读取上游 job 对象响应体（有界读取；超限/读失败
// 渲染 502 并返回 nil body）。
func (c *gatewayChain) readMediaJobUpstreamObject(res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, response *gatewayupstream.GatewayUpstreamResponse) ([]byte, int) {
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxMediaJobObjectBytes+1))
	_ = response.Body.Close()
	status := response.Status()
	if readErr != nil {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "读取上游视频任务响应失败", "upstream_error")
		return nil, status
	}
	if len(body) > maxMediaJobObjectBytes {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游视频任务响应超过网关处理上限", "upstream_error")
		return nil, status
	}
	return body, status
}

// ---------------------------------------------------------------------------
// usage 终态回填 + 终态计费（媒体设计 §10：任务终态由轮询响应驱动）
// ---------------------------------------------------------------------------

// chainMediaVideoTerminalCostUsd 计算终态成本（媒体设计 §10；契约 §2.8 视频
// 网关自算口径）：completed 且有秒计量时按静态定价目录（pricing 引擎，与
// M1 音频行项同一引擎）秒数 × 秒价估 USD；其余（failed/无秒 usage_missing/
// 目录未命中）返回 0——失败任务不虚计、无计量不猜测。计费键沿 chain_usage
// costModel 先例（请求模型直达，视频任务无模型映射面，M2 语义）。
func chainMediaVideoTerminalCostUsd(record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) float64 {
	if ir.Status != gatewaymedia.JobStatusCompleted {
		return 0
	}
	if ir.Usage.OutputVideoSeconds == nil || *ir.Usage.OutputVideoSeconds <= 0 {
		return 0
	}
	cost := pricing.EstimateProviderCostUsd(pricing.CostInput{
		ProviderCode:       record.ProviderCode,
		Model:              record.RequestSnapshot.Model,
		OutputVideoSeconds: ir.Usage.OutputVideoSeconds,
	})
	if cost == nil {
		return 0
	}
	return *cost
}

// enqueueMediaVideoTerminalUsage 经既有 usage spool 链写终态 usage_records
// （chain finalizationUsage 即 spooledUsageRecorder：有界异步缓冲 + spool
// 溢出落盘，与同步请求终态记录同一投递面，链上下文零额外依赖）。
// completed 携带 OutputVideoSeconds（IR 从轮询响应 seconds_length 抽取，
// 契约 §2.8 网关自算口径）；completed 但上游无秒数回报 → usage_missing 标记
// 不猜测；failed 不虚计（success=false 无计量，行项为空）。cancelled/expired
// 是本地终态，不产生 usage 行（清理不产生计费；取消按上游实际计量为准，
// openai 无回报）。CostUsd 在 enqueue 前已可算（秒数/模型/供应商都在行上），
// 直接带值落 usage 行——不存在"已 enqueue 留空回追"的窗口（usage 行只在
// 终态轮询时入队一次）；0 成本（usage_missing/未命中）不设 CostUsd，与完成
// 尝试记录"估算不出保持 NULL"的既有语义一致。记账 scope 五元组从行内
// request_snapshot 投影（创建时冻结，chainMediaJobSnapshotScopeOf）。
func (c *gatewayChain) enqueueMediaVideoTerminalUsage(ctx context.Context, traceID, apiKeyOwnerSystemAccountID string, record *gatewaymedia.MediaJobRecord, account *chainMediaJobAccount, ir *gatewaymedia.MediaJobIR, costUsd float64) {
	if c.finalizationUsage == nil || (ir.Status != gatewaymedia.JobStatusCompleted && ir.Status != gatewaymedia.JobStatusFailed) {
		return
	}
	statusCode := http.StatusOK
	input := gatewayusage.UsageRecordInput{
		TraceID:       traceID,
		TrafficSource: gatewayTrafficSource,
		// 身份维度对齐既有 usage 记录约定（chain_usage.go RecordCompleted
		// UpstreamAttempt：SystemAccountID 取 usage context 即 API Key 属主，
		// AccountOwnerSystemAccountID 取账户行属主）。
		SystemAccountID:             apiKeyOwnerSystemAccountID,
		APIKeyID:                    record.APIKeyID,
		AccountID:                   record.AccountID,
		AccountOwnerSystemAccountID: account.OwnerSystemAccountID,
		Endpoint:                    "/v1/videos",
		ProviderCode:                record.ProviderCode,
		ProviderProtocolProfileID:   record.ProviderProtocolProfileID,
		Model:                       record.RequestSnapshot.Model,
		UpstreamModel:               record.RequestSnapshot.Model,
		StatusCode:                  &statusCode,
		Success:                     ir.Status == gatewaymedia.JobStatusCompleted,
		DurationMs:                  chainMediaJobDurationMsOf(record),
		RequestSnapshot: map[string]any{
			"kind": string(record.Kind), "jobId": record.ID, "upstreamJobId": record.UpstreamJobID,
		},
	}
	// 记账 scope 五元组投影（行内冻结值；归一化由 spool 链
	// NormalizeUsageRecordInput 的完整性规则兜底，五元组不齐整组清空）。
	snapshot := record.RequestSnapshot
	input.AccountAccessType = snapshot.AccountAccessType
	input.AccountAuthorizationID = snapshot.AccountAuthorizationID
	input.AccountAuthorizationSourceType = snapshot.AccountAuthorizationSourceType
	input.AccountAuthorizationSourceTeamID = snapshot.AccountAuthorizationSourceTeamID
	input.GroupID = snapshot.GroupID
	input.GroupOwnerSystemAccountID = snapshot.GroupOwnerSystemAccountID
	input.GroupAccessType = snapshot.GroupAccessType
	input.GroupAuthorizationID = snapshot.GroupAuthorizationID
	input.GroupAuthorizationSourceType = snapshot.GroupAuthorizationSourceType
	input.GroupAuthorizationSourceTeamID = snapshot.GroupAuthorizationSourceTeamID
	if ir.Status == gatewaymedia.JobStatusCompleted {
		if ir.Usage.OutputVideoSeconds != nil {
			input.OutputVideoSeconds = ir.Usage.OutputVideoSeconds
		} else {
			input.UsageMissing = true
		}
		if costUsd > 0 {
			cost := costUsd
			input.CostUsd = &cost
		}
	} else {
		// 终态错误摘要优先取本轮 IR（record.Error 是行内旧值，终态赋值发生
		// 在 enqueue 之后，首次终态时恒空）。
		jobError := record.Error
		if ir.Error != nil {
			jobError = ir.Error
		}
		if jobError != nil {
			input.ErrorCode = jobError.Code
			input.ErrorMessage = jobError.Message
		}
		input.FailureAttribution = gatewayusage.FailureAttributionAccountUpstream
	}
	if err := c.finalizationUsage.EnqueueUsageRecord(gatewayusage.Ctx(ctx), input); err != nil {
		c.observability.Logger().Warn("media_job_usage_enqueue_failed", map[string]any{
			"event": "media_job_usage_enqueue_failed", "jobId": record.ID, "error": err.Error(),
		}, "媒体任务终态用量记录入队失败")
	}
}

// apiKeyOwnerSystemAccountIDOf 取 API Key 属主系统账户（usage 身份维度来源）。
func apiKeyOwnerSystemAccountIDOf(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.Runtime == nil || req.Runtime.APIKey == nil {
		return ""
	}
	return req.Runtime.APIKey.SystemAccountID
}

// chainMediaJobDurationMsOf 任务时长（创建 → 终态轮询到达）。
func chainMediaJobDurationMsOf(record *gatewaymedia.MediaJobRecord) *int {
	if record.CreatedAt.IsZero() {
		return nil
	}
	duration := int(time.Since(record.CreatedAt).Milliseconds())
	if duration < 0 {
		duration = 0
	}
	return &duration
}

// ---------------------------------------------------------------------------
// 渲染工具
// ---------------------------------------------------------------------------

// renderMediaVideoLocalError 渲染任务面本地错误（OpenAI 形态错误信封）。
func (c *gatewayChain) renderMediaVideoLocalError(res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, statusCode int, message, code string) {
	if res.HeadersSent() || res.WritableEnded() {
		return
	}
	errorType := "invalid_request_error"
	if statusCode >= 500 {
		errorType = "server_error"
	}
	gatewaypreauth.SendGatewayJSONError(res, statusCode,
		gatewaypreauth.GatewayErrorPayloadOf(message, errorType, code),
		gatewaypreauth.SendGatewayErrorOptions{Protocol: clientErrorProtocol(req)})
}

// renderMediaVideoTransportError 渲染任务面直连的传输失败（网络错误族：503
// 可重试语义；受理后故障只报错不换账户）。
func (c *gatewayChain) renderMediaVideoTransportError(res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, err error) {
	c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "上游暂时不可用，请重试", "upstream_unavailable")
}

// writeMediaVideoJSON 渲染任务面成功 JSON。
func (c *gatewayChain) writeMediaVideoJSON(res *gatewaypreauth.TrackingWriter, statusCode int, payload any) {
	kernel.WriteJSON(res, statusCode, payload)
}
