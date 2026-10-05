package main

// M2 异步媒体任务链（媒体设计 §4.2/§7/§8、契约 §2.2/§2.4/§4.3 视频 +
// §10.2 长转写；M3f 起任务面按 media_jobs.kind 泛化，覆盖 /v1/videos* 与
// /v1/audio/jobs* 两族端点）：
//
//   - 创建（POST /v1/videos 与 POST /v1/audio/jobs）：走既有派发循环（受理
//     边界生效——引擎 attempt 语义原样复用，429/5xx/网络错误换候选、耗尽
//     渲染既有契约，不新写重试）；出站报文经 gatewaymedia 注册表 adapter
//     构造（视频表与长转写表按 kind 分离，driver 内短路分支，与 M1 gemini
//     speech adapter 同模式）；2xx + job id = 受理凭据确立，落 media_jobs
//     后返回统一 job 对象（id 为对外 id，不用上游 id；video_ / audiojob_
//     前缀按 kind）。
//   - 任务面（两族的列表/轮询/content/取消）：不走派发循环，账户亲和直连
//     ——查表（id + api_key_id 归属校验）→ 按 account_id 水合凭据 → 按
//     行内 kind 解析 adapter.Build*Request → 复用 gatewayupstream.
//     RequestUpstream 直连原上游；轮询响应驱动本地状态与终态 usage 回填
//     （spool 链）。
//   - content：completed 才可下载，http 流式转发（io.Copy 零缓冲落盘，
//     content-type 透传；视频产物 mp4、长转写产物是转写结果 JSON 文件，
//     契约 §10.2）；上游 404/410 → media_artifact_expired。
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
// 路径族判定（/videos 与 /audio/jobs 两族媒体任务路径的 chain 侧词表落点）
// ---------------------------------------------------------------------------

// chainVideoRequestShape 是媒体任务族请求形态（/videos 与 /audio/jobs 共用
// 形态词表：创建/列表/轮询/下载/取消五形态，媒体设计 §4.2 端点集——两族
// 按 kind 区分，形态语义同构）。
type chainVideoRequestShape int

const (
	chainVideoShapeNone    chainVideoRequestShape = iota
	chainVideoShapeCreate                         // POST /v1/videos | POST /v1/audio/jobs
	chainVideoShapeList                           // GET /v1/videos | GET /v1/audio/jobs
	chainVideoShapePoll                           // GET /v1/videos/{id} | GET /v1/audio/jobs/{id}
	chainVideoShapeContent                        // GET .../{id}/content
	chainVideoShapeCancel                         // DELETE .../{id}
)

// chainMediaJobStrippedPathOf 剥 /v1 前缀并归一路径（root 形态已由入口改写
// 补 /v1，沿 chainStripGatewayVersionPrefix 语义）。
func chainMediaJobStrippedPathOf(pathAndQuery string) string {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return chainStripGatewayVersionPrefix(strings.ToLower(strings.TrimSpace(path)))
}

// chainMediaJobShapeUnderPrefix 判定单一任务族（collection 形如 "/videos" 或
// "/audio/jobs"）的请求形态。第二返回值是路径上的 job id（poll/content/
// cancel 形态非空）。
func chainMediaJobShapeUnderPrefix(method, strippedPath, collection string) (chainVideoRequestShape, string) {
	if strippedPath == collection {
		switch strings.ToUpper(method) {
		case http.MethodPost:
			return chainVideoShapeCreate, ""
		case http.MethodGet:
			return chainVideoShapeList, ""
		}
		return chainVideoShapeNone, ""
	}
	rest, ok := strings.CutPrefix(strippedPath, collection+"/")
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

// chainVideoRequestShapeOf 判定 /videos 族的方法 + 路径形态（媒体设计 §4.2
// 视频端点集；kind 恒为 video）。
func chainVideoRequestShapeOf(method, pathAndQuery string) (chainVideoRequestShape, string) {
	return chainMediaJobShapeUnderPrefix(method, chainMediaJobStrippedPathOf(pathAndQuery), "/videos")
}

// chainAudioJobRequestShapeOf 判定 /audio/jobs 族的方法 + 路径形态（媒体设计
// §4.2 长音频端点集，M3f；kind 恒为 audio_transcription）。
func chainAudioJobRequestShapeOf(method, pathAndQuery string) (chainVideoRequestShape, string) {
	return chainMediaJobShapeUnderPrefix(method, chainMediaJobStrippedPathOf(pathAndQuery), "/audio/jobs")
}

// chainMediaJobRequestShapeOf 判定全媒体任务族（/videos 优先，/audio/jobs
// 次之——两族路径不重叠）：返回形态、路径上的 job id 与任务种类（任务面
// 按 kind 泛化的判定入口，M3f）。
func chainMediaJobRequestShapeOf(method, pathAndQuery string) (chainVideoRequestShape, string, gatewaymedia.MediaJobKind) {
	if shape, id := chainVideoRequestShapeOf(method, pathAndQuery); shape != chainVideoShapeNone {
		return shape, id, gatewaymedia.JobKindVideo
	}
	if shape, id := chainAudioJobRequestShapeOf(method, pathAndQuery); shape != chainVideoShapeNone {
		return shape, id, gatewaymedia.JobKindAudioTranscription
	}
	return chainVideoShapeNone, "", ""
}

// chainIsVideoCreateRequest 报告请求是否视频创建形态（响应面拦截判定）。
func chainIsVideoCreateRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	shape, _ := chainVideoRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	return shape == chainVideoShapeCreate
}

// chainIsAudioJobCreateRequest 报告请求是否长音频创建形态（POST
// /v1/audio/jobs，M3f）。
func chainIsAudioJobCreateRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	shape, _ := chainAudioJobRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	return shape == chainVideoShapeCreate
}

// chainIsMediaJobCreateRequest 报告请求是否任一媒体任务创建形态（/v1/videos
// 或 /v1/audio/jobs 的 POST；chain_v1 响应面拦截按此泛化，M3f）。
func chainIsMediaJobCreateRequest(req *gatewaypreauth.GatewayRequest) bool {
	return chainIsVideoCreateRequest(req) || chainIsAudioJobCreateRequest(req)
}

// chainIsMediaJobTaskPlaneRequest 报告请求是否任一媒体任务族的任务面形态
// （列表/轮询/下载/取消，不走派发循环；按 kind 泛化，M3f 起覆盖
// /v1/audio/jobs*）。
func chainIsMediaJobTaskPlaneRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	shape, _, _ := chainMediaJobRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
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
//   - qwen 账户 → "qwen"（M3 第五批：万相 video-synthesis 形态，契约
//     §10.1）：api_key Bearer 认证（沿链上 openai 族认权分支），出站路径
//     自带 /api/v1 服务根（创建 /api/v1/services/aigc/video-generation/
//     video-synthesis + 轮询 /api/v1/tasks/{id}），URL 按
//     chainQwenVideoUpstreamURL 归一（官方根 dashscope.aliyuncs.com 去重，
//     不走 /v1 强制补缀的 openai 归一——同 glm/volcengine 先例）；创建
//     请求头带 X-DashScope-Async: enable（adapter ExtraHeaders，DashScope
//     异步任务约定）。
//   - xai 账户 → "xai"（M3 回填池：Grok Imagine Video 形态，契约 §6.1）：
//     api_key Bearer 认证（沿链上 openai 族认权分支——xai 出站认权与
//     openai 图像/文本同构），出站路径为 /v1 前缀的 openai 族形态（创建
//     /v1/videos/generations + 轮询 /v1/videos/{request_id}），URL 走
//     gatewayopenai.BuildUpstreamURL 归一（base 强制 /v1 结尾、path 剥
//     /v1 前缀去重——api.x.ai 官方根 seed base_url 已是 https://api.x.ai/v1，
//     同 minimax 先例，无需专用归一函数）。
//   - hybrid 账户 → "openai"（M4b 媒体设计 §9 hybrid 行）：hybrid 是真实聚合
//     中转账户，媒体执行面 = 该中转的 OpenAI 形态媒体端点（/v1/videos、
//     /v1/audio/speech）——URL 走 openai 归一（base 强制 /v1 结尾）直连中转
//     base_url，模型名经账号媒体映射（video_generation/tts 族）改写出站
//     body（chainVideoMappedModel）；openai adapter 的 sora 形态即统一媒体
//     面契约。
//   - 其余 provider（anthropic/deepseek 等）→ ""：无对应 adapter。
//     创建链在 driver 构造点显式失败（能力缺失不静默回退，媒体设计 §5
//     禁止项：不把厂商差异写进组合根 switch）；后续供应商接入起只新增
//     注册表条目与本映射分支。
func chainVideoAdapterKeyOfProvider(providerCode string) string {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "openai", "gpt", "hybrid":
		return "openai"
	case "gemini":
		return "gemini"
	case "glm":
		return "glm"
	case "minimax":
		return "minimax"
	case "volcengine":
		return "volcengine"
	case "qwen":
		return "qwen"
	case "xai":
		return "xai"
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

// chainAudioJobAdapterKeyOfProvider 把账户 provider_code 归一为长转写
// （kind=audio_transcription）adapter 注册键。映射规则：
//   - qwen 账户 → "qwen"（M3f：paraformer 录音文件识别形态，契约 §10.2；
//     api_key Bearer 认证，出站路径自带 /api/v1 服务根——创建
//     /api/v1/services/audio/asr/transcription + 轮询 /api/v1/tasks/{id}
//     （与万相同一任务接口），URL 按 chainQwenVideoUpstreamURL 归一
//     （/api/v1 去重，两族共用该函数）；创建请求**不带** X-DashScope-Async
//     头（该服务天然异步，与万相 §10.1 的关键差异）。
//   - 其余 provider（openai/gemini/glm/minimax/volcengine/hybrid 等）→ ""：
//     无长转写 adapter（长转写面未回填的供应商接入前不承接，回填后只新增
//     audioJobAdapters 注册表条目与本映射分支）。
func chainAudioJobAdapterKeyOfProvider(providerCode string) string {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "qwen":
		return "qwen"
	default:
		return ""
	}
}

// chainAudioJobAdapterForAccount 解析账户的长转写 adapter；nil 表示该
// provider 无长转写 adapter（调用方按能力缺失处理）。
func chainAudioJobAdapterForAccount(providerCode string) gatewaymedia.AudioJobProviderAdapter {
	key := chainAudioJobAdapterKeyOfProvider(providerCode)
	if key == "" {
		return nil
	}
	return gatewaymedia.AudioJobAdapterForProvider(key)
}

// chainMediaJobTaskAdapter 是媒体任务面（轮询/content/取消 + 创建响应解析）
// 消费的 adapter 公共方法集：VideoProviderAdapter 与 AudioJobProviderAdapter
// 的任务面半部结构同构，Go 结构化接口双双满足——任务面处理按 media_jobs.
// kind 经 chainMediaJobTaskAdapterFor 泛化解析（M3f：不复制链路，kind 选表）。
type chainMediaJobTaskAdapter interface {
	Provider() string
	BuildPollRequest(upstreamJobID string) (method, path string, body []byte)
	ParsePollResponse(statusCode int, body []byte) (*gatewaymedia.MediaJobIR, error)
	BuildContentRequest(upstreamJobID string) (method, path string)
	ContentFromArtifact() bool
	BuildCancelRequest(upstreamJobID string) (method, path string)
	SupportsCancel() bool
}

// chainMediaJobTaskAdapterFor 按任务种类解析账户的任务面 adapter；nil 表示
// 该 provider 无该 kind 的 adapter（调用方按能力缺失处理）。
func chainMediaJobTaskAdapterFor(kind gatewaymedia.MediaJobKind, providerCode string) chainMediaJobTaskAdapter {
	switch kind {
	case gatewaymedia.JobKindAudioTranscription:
		return chainAudioJobAdapterForAccount(providerCode)
	default:
		return chainVideoAdapterForAccount(providerCode)
	}
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
// 错误短路"：媒体异步任务 lane（LaneVideo；POST /v1/videos 与 POST
// /v1/audio/jobs 两个创建形态——M3f 起长音频任务同走 LaneVideo，媒体设计
// §3 车道裁决；音频同步 lane 的受理凭据是 2xx 响应头到达，参数 400 属普通
// 失败语义，不在此列）+ 确定性参数类 4xx。消费点是 chain_ports.go
// HandleFailedUpstreamResponse（审计尝试与 usage 失败记录落账之后、
// SkipAccount 决策之前）。
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

// jobKind/snapshotFields/paramsApplied/paramsIgnored 实现
// chainMediaJobCreatePlanCommon（创建响应面按 kind 泛化的访问器，M3f）。
func (p *chainVideoCreatePlan) jobKind() gatewaymedia.MediaJobKind {
	return gatewaymedia.JobKindVideo
}

func (p *chainVideoCreatePlan) snapshotFields() gatewaymedia.MediaJobRequestSnapshot {
	return gatewaymedia.MediaJobRequestSnapshot{
		Model:                  p.params.Model,
		Prompt:                 p.params.Prompt,
		Seconds:                p.params.Seconds,
		Size:                   p.params.Size,
		N:                      p.params.N,
		ProviderOptionsApplied: p.providerOptionsApplied,
	}
}

func (p *chainVideoCreatePlan) paramsApplied() []string { return p.params.ParamsApplied }
func (p *chainVideoCreatePlan) paramsIgnored() []string { return p.params.ParamsIgnored }

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
	// M4b 媒体映射（媒体设计 §9 hybrid 行）：source model → upstream model
	// 改写出站报文；映射优先于账户 canonical 拼写（与 chat 链 modelMapping
	// 优先级一致，buildUpstreamRequestParts 先例）。M2 的"视频无模型映射面"
	// 语义自此由映射面取代；无映射账户维持请求模型直达 + canonical 优先。
	if mapped := chainVideoMappedModel(req, account); mapped != "" {
		params.Model = mapped
	} else if canonical := canonicalAccountModel(req, account); canonical != "" {
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

// chainVideoMappedModel 解析账号媒体映射（M4b，媒体设计 §9 hybrid 行）：
// POST /v1/videos 创建形态的 source model → upstream model（video_generation
// 族，经 requestMappingSourceFamilyOf 与构造/候选过滤/记账侧同词表解析）。
// 无映射或请求缺模型返回空串（调用方回落 canonical 拼写）。
func chainVideoMappedModel(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) string {
	if req == nil {
		return ""
	}
	requestedModel, ok := gatewaypreauth.RequestModel(req)
	if !ok || strings.TrimSpace(requestedModel) == "" {
		return ""
	}
	runtime := &gatewayopenai.RuntimeAccount{
		ModelMappings:             openAIModelMappingsOf(account.ModelMappings),
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
	}
	mapping := gatewayopenai.ResolveAccountModelMapping(runtime, requestedModel, requestMappingSourceFamilyOf(req))
	if mapping == nil {
		return ""
	}
	return strings.TrimSpace(mapping.UpstreamModel)
}

// chainVideoProviderOptionsOf 从已解析请求体提取 L3 扩展通道（契约 §2.1）：
// 经 ExtractProviderOptions 做形态校验（子值必须为对象），再投影为对象形态
// （键=provider_code，VideoCreateInput/AudioJobCreateInput.ProviderOptions 共用
// 消费面——M3f 起长音频任务同走本通道）。
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
// 长音频任务创建链计划（/v1/audio/jobs，kind=audio_transcription，M3f）
// ---------------------------------------------------------------------------

// chainAudioJobCreatePlan 是长音频任务创建请求的解析产物（结构与
// chainVideoCreatePlan 同构：adapter 出站报文 + 归一参数 + provider_options
// 生效键名摘要）。
type chainAudioJobCreatePlan struct {
	adapter                gatewaymedia.AudioJobProviderAdapter
	createRequest          gatewaymedia.AudioJobCreateOutput
	params                 gatewaymedia.NormalizedAudioJobParams
	providerOptionsApplied []string
}

// jobKind/snapshotFields/paramsApplied/paramsIgnored 实现
// chainMediaJobCreatePlanCommon（创建响应面按 kind 泛化的访问器）。
func (p *chainAudioJobCreatePlan) jobKind() gatewaymedia.MediaJobKind {
	return gatewaymedia.JobKindAudioTranscription
}

func (p *chainAudioJobCreatePlan) snapshotFields() gatewaymedia.MediaJobRequestSnapshot {
	return gatewaymedia.MediaJobRequestSnapshot{
		Model:                  p.params.Model,
		Language:               p.params.Language,
		ProviderOptionsApplied: p.providerOptionsApplied,
	}
}

func (p *chainAudioJobCreatePlan) paramsApplied() []string { return p.params.ParamsApplied }
func (p *chainAudioJobCreatePlan) paramsIgnored() []string { return p.params.ParamsIgnored }

// chainAudioJobCreatePlanOf 解析长音频任务创建请求体并构造 adapter 出站报文
// （媒体设计 §4.2/§6）。与 chainVideoCreatePlanOf 的差异：body 为 nil（请求体
// 非 JSON 对象——multipart 文件输入）是**显式 400**而非 (nil, nil)：长音频
// 任务唯一输入形态是 input_url 公网 URL（契约 §10.2 上游只收 file_urls、
// 零存储不暂存），multipart 对任何账户都会重演，直接本地 400 不换账户。
// 长转写无模型映射面（hybrid 不承接 audio jobs，M4b 只放开 video_generation/
// tts 两族），模型名仅走 canonical 拼写优先。
func chainAudioJobCreatePlanOf(body map[string]any, req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) (*chainAudioJobCreatePlan, error) {
	adapter := chainAudioJobAdapterForAccount(account.ProviderCode)
	if adapter == nil {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 暂不支持长音频转写", account.ID, account.ProviderCode)
	}
	if body == nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError("长音频任务请求体必须是 JSON 对象（唯一输入形态 input_url 公网音频 URL，不支持 multipart 文件输入）")
	}
	providerOptions, err := chainVideoProviderOptionsOf(body)
	if err != nil {
		return nil, err
	}
	params, err := gatewaymedia.ParseAudioJobParams(body, adapter.Capabilities())
	if err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	if canonical := canonicalAccountModel(req, account); canonical != "" {
		params.Model = canonical
	}
	createRequest, err := adapter.Create(context.Background(), gatewaymedia.AudioJobCreateInput{
		Params:          params,
		ProviderOptions: providerOptions,
	})
	if err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	providerOptionsApplied := gatewaymedia.AppliedProviderOptionKeys(adapter.Provider(), providerOptions)
	return &chainAudioJobCreatePlan{
		adapter:                adapter,
		createRequest:          createRequest,
		params:                 params,
		providerOptionsApplied: providerOptionsApplied,
	}, nil
}

// chainMediaJobCreatePlanCommon 是创建计划的任务面公共访问器（video/audio
// 两 plan 结构实现；创建响应面 handleMediaJobCreateUpstreamResponse 按 kind
// 泛化消费——落表快照、params 回显与种类来源，不复制链路）。
type chainMediaJobCreatePlanCommon interface {
	jobKind() gatewaymedia.MediaJobKind
	snapshotFields() gatewaymedia.MediaJobRequestSnapshot
	paramsApplied() []string
	paramsIgnored() []string
}

// chainMediaJobCreatePlanOf 按 kind 分发到对应创建计划解析（chain_v1 创建
// 响应面与 driver 出站构造共用入口的响应侧半部）。
func chainMediaJobCreatePlanOf(body map[string]any, req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, kind gatewaymedia.MediaJobKind) (chainMediaJobCreatePlanCommon, error) {
	if kind == gatewaymedia.JobKindAudioTranscription {
		return chainAudioJobCreatePlanOf(body, req, account)
	}
	return chainVideoCreatePlanOf(body, req, account)
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

// chainAudioJobObject 是长音频任务（kind=audio_transcription）的统一 job
// 对象（创建/轮询/列表共用渲染，媒体设计 §4.2 长音频行：平台自有扩展面，
// 同一 job 语义）。params/provider/provider_job_id 固定携带（契约 §2.4 规则
// 5、设计 §6）；progress 按可得性回显（media_jobs 不持久化 progress，轮询
// 响应取上游即时值）。
type chainAudioJobObject struct {
	ID                     string                      `json:"id"`
	Object                 string                      `json:"object"`
	Status                 string                      `json:"status"`
	Progress               *int                        `json:"progress,omitempty"`
	Model                  string                      `json:"model,omitempty"`
	Language               string                      `json:"language,omitempty"`
	Error                  *gatewaymedia.MediaJobError `json:"error,omitempty"`
	Provider               string                      `json:"provider"`
	ProviderJobID          string                      `json:"provider_job_id"`
	ParamsApplied          []string                    `json:"params_applied"`
	ParamsIgnored          []string                    `json:"params_ignored"`
	ProviderOptionsApplied []string                    `json:"provider_options_applied"`
}

// chainAudioJobObjectOf 由行 + 即时状态渲染长音频 job 对象。
func chainAudioJobObjectOf(record *gatewaymedia.MediaJobRecord, liveStatus gatewaymedia.MediaJobStatus, liveProgress *int) chainAudioJobObject {
	status := record.Status
	if liveStatus != "" {
		status = liveStatus
	}
	object := chainAudioJobObject{
		ID:                     record.ID,
		Object:                 "audio_job",
		Status:                 string(status),
		Progress:               liveProgress,
		Model:                  record.RequestSnapshot.Model,
		Language:               record.RequestSnapshot.Language,
		Error:                  record.Error,
		Provider:               record.ProviderCode,
		ProviderJobID:          record.UpstreamJobID,
		ParamsApplied:          record.ParamsApplied,
		ParamsIgnored:          record.ParamsIgnored,
		ProviderOptionsApplied: record.RequestSnapshot.ProviderOptionsApplied,
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

// chainMediaJobObjectOf 按行内 kind 渲染对应族 job 对象（任务面列表/轮询/
// 创建响应共用，M3f 泛化入口）。
func chainMediaJobObjectOf(record *gatewaymedia.MediaJobRecord, liveStatus gatewaymedia.MediaJobStatus, liveProgress *int) any {
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		return chainAudioJobObjectOf(record, liveStatus, liveProgress)
	}
	return chainVideoJobObjectOf(record, liveStatus, liveProgress)
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

// handleMediaJobCreateUpstreamResponse 处理媒体任务创建（POST /v1/videos 与
// POST /v1/audio/jobs）的上游 2xx 响应（chain_v1.go handleUpstreamResponse 的
// 媒体短路分支，按 kind 泛化——M3f）：ParsePollResponse 归一创建响应（视频
// 契约 §4.3 创建响应即 video 对象；长音频契约 §10.2 创建响应即任务对象），
// 拿到 job id 后落 media_jobs 并渲染对应族的统一 job 对象。2xx 但无 id /
// 解析失败 = 上游协议违约（受理凭据未确立）→ 502，不换账户（引擎 2xx 分支
// 已选定账户，受理后永不切换）。
func (c *gatewayChain) handleMediaJobCreateUpstreamResponse(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	res *gatewaypreauth.TrackingWriter,
	context *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	upstream *gatewayupstream.GatewayUpstreamResponse,
) gatewayresponse.UpstreamResponseHandlingResult {
	_, _, kind := chainMediaJobRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	jobLabel := "视频"
	if kind == gatewaymedia.JobKindAudioTranscription {
		jobLabel = "长音频"
	}
	body, readErr := io.ReadAll(io.LimitReader(upstream.Body, maxMediaJobObjectBytes+1))
	_ = upstream.Body.Close()
	if readErr != nil {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "读取上游"+jobLabel+"任务响应失败", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	if len(body) > maxMediaJobObjectBytes {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游"+jobLabel+"任务响应超过网关处理上限", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	if c.mediaJobs == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务面未装配，无法受理"+jobLabel+"任务", "service_unavailable")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	adapter := chainMediaJobTaskAdapterFor(kind, dispatched.Account.ProviderCode)
	if adapter == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游账户缺少"+jobLabel+"任务 adapter，任务受理中断", "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	ir, parseErr := adapter.ParsePollResponse(upstream.Status(), body)
	if parseErr != nil || ir == nil || ir.UpstreamJobID == "" {
		message := "上游" + jobLabel + "任务响应缺少受理凭据（job id）"
		if parseErr != nil {
			message = parseErr.Error()
		}
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	// 归一参数复读（与 driver 出站构造同源解析，确定性纯函数）。失败属防御
	// 分支（派发侧已成功构造过同源报文）。
	plan, planErr := chainMediaJobCreatePlanOf(chainVideoRequestBodyOf(req), req, dispatched.Account, kind)
	if planErr != nil || plan == nil {
		message := jobLabel + "任务创建请求参数复析失败"
		if planErr != nil {
			message = planErr.Error()
		}
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, message, "internal_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	record := gatewaymedia.MediaJobRecord{
		ID:                        newChainMediaJobID(kind),
		Kind:                      kind,
		APIKeyID:                  context.UsageContext.APIKeyID,
		AccountID:                 dispatched.Account.ID,
		ProviderCode:              adapter.Provider(),
		ProviderProtocolProfileID: dispatched.Account.ProviderProtocolProfileID,
		UpstreamJobID:             ir.UpstreamJobID,
		Status:                    ir.Status,
		RequestSnapshot:           chainMediaJobSnapshotScopeOf(plan.snapshotFields(), dispatched.Account, context),
		Artifact:                  ir.Artifact,
		ParamsApplied:             plan.paramsApplied(),
		ParamsIgnored:             plan.paramsIgnored(),
	}
	if insertErr := c.mediaJobs.repo.Insert(ctx, record); insertErr != nil {
		// 受理凭据已确立但本地行落库失败：任务已在上游存在，客户端拿到错误
		// 后重试会创建新任务；保留原始错误供排查（不静默降级为成功）。
		slog.Error("媒体任务受理后落库失败",
			"event", "media_job_insert_failed", "traceId", context.UsageContext.TraceID,
			"accountId", dispatched.Account.ID, "upstreamJobId", ir.UpstreamJobID,
			"error", insertErr.Error())
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, jobLabel+"任务受理后落库失败", "internal_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	object := chainMediaJobObjectOf(&record, ir.Status, ir.Progress)
	c.writeMediaVideoJSON(res, http.StatusOK, object)
	return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, ProtocolValidatedSuccess: true}
}

// handleVideoCreateUpstreamErrorPassthrough 处理媒体任务创建（/v1/videos 与
// /v1/audio/jobs）参数类 4xx 的短路透传（chain_v1.go handleUpstreamResponse
// 的媒体错误分支）：失败派发器对媒体任务 lane（LaneVideo）的 400/413/422
// 已短路为 ReturnResponse（媒体设计 §7：确定性参数错误不换账户），错误状态
// 与错误体原样透传客户端——不进 chat 语义的 JSON 检查/协议校验管道（错误
// 改写会把上游参数错误吞成 502 网关错误）。
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

// newChainMediaJobID 生成对外 job id（媒体设计 §8.2：不使用上游 id，避免
// 跨厂商碰撞与暴露；前缀按 kind：video → "video_"、audio_transcription →
// "audiojob_"，M3f 起长音频任务独立前缀）+ 24 hex。
func newChainMediaJobID(kind gatewaymedia.MediaJobKind) string {
	prefix := "video_"
	if kind == gatewaymedia.JobKindAudioTranscription {
		prefix = "audiojob_"
	}
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 失败无法生成不可预测对外 id；fail loud 而非退化到可
		// 预测 id。
		panic(fmt.Sprintf("生成媒体任务 id 失败: %v", err))
	}
	return prefix + hex.EncodeToString(buf[:])
}

// ---------------------------------------------------------------------------
// 任务面处理（账户亲和直连）
// ---------------------------------------------------------------------------

// serveMediaJobTaskPlane 处理任务面请求（chain_v1.go 入口短路分支：preauth
// 认证完成之后、派发预检之前；M3f 起按 kind 泛化覆盖 /v1/videos* 与
// /v1/audio/jobs* 两族）。auditCapture 是链入口已构造的 G17 审计捕获
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
	shape, jobID, kind := chainMediaJobRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	apiKeyID := req.Runtime.APIKey.ID
	switch shape {
	case chainVideoShapeList:
		c.serveMediaJobList(ctx, res, req, apiKeyID, kind)
	case chainVideoShapePoll:
		c.serveMediaJobPoll(ctx, res, req, traceID, apiKeyID, jobID)
	case chainVideoShapeContent:
		c.serveMediaJobContent(ctx, res, req, apiKeyID, jobID)
	case chainVideoShapeCancel:
		c.serveMediaJobCancel(ctx, res, req, apiKeyID, jobID)
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
// （归属信息不泄露；按 kind 渲染族内错误码，M3f 起长音频行用 audio_job 域）。
func (c *gatewayChain) loadMediaVideoJob(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID, jobID string) *gatewaymedia.MediaJobRecord {
	record, err := c.mediaJobs.repo.GetByIDAndAPIKey(ctx, jobID, apiKeyID)
	if err != nil {
		if errors.Is(err, gatewaymedia.ErrMediaJobNotFound) {
			if strings.HasPrefix(jobID, "audiojob_") {
				c.renderMediaVideoLocalError(res, req, http.StatusNotFound, "Audio job not found: "+jobID, "audio_job_not_found")
				return nil
			}
			c.renderMediaVideoLocalError(res, req, http.StatusNotFound, "Video not found: "+jobID, "video_not_found")
			return nil
		}
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "查询媒体任务失败", "internal_error")
		return nil
	}
	return record
}

// loadMediaVideoAccount 水合账户亲和三元组：行不存在/已删/禁用统一
// media_job_unreachable（503，不换账户——媒体设计 §7 任务面行）。adapter
// 解析按行内 kind（M3f 泛化：video 行查视频注册表、audio_transcription 行查
// 长转写注册表）。
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
	if chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode) == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务账户供应商暂不支持该种类媒体任务面", "media_job_unreachable")
		return nil
	}
	return account
}

// serveMediaJobList GET /v1/videos 与 GET /v1/audio/jobs：本地行列表（按归属
// Key + kind 倒序过滤，两族互不混行），不经上游（媒体设计 §8.2：不为闲置
// 任务消耗上游配额）。
func (c *gatewayChain) serveMediaJobList(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID string, kind gatewaymedia.MediaJobKind) {
	limit := chainMediaVideoListLimitOf(req.PathAndQuery())
	records, err := c.mediaJobs.repo.ListByAPIKeyAndKind(ctx, apiKeyID, kind, limit, 0)
	if err != nil {
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, "查询媒体任务列表失败", "internal_error")
		return
	}
	data := make([]any, 0, len(records))
	for _, record := range records {
		data = append(data, chainMediaJobObjectOf(record, "", nil))
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

// chainMediaJobLabelOf 按行内 kind 返回任务族中文名（错误消息渲染）。
func chainMediaJobLabelOf(record *gatewaymedia.MediaJobRecord) string {
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		return "长音频"
	}
	return "视频"
}

// serveMediaJobPoll GET /v1/videos/{id} 与 GET /v1/audio/jobs/{id}：终态行
// 直接回本地对象（终态后不再被轮询推进，mediajobir Terminal 语义）；非终态行
// 账户亲和转发（adapter 按行内 kind 解析），响应归一后推进本地状态，
// completed/failed 终态触发 usage spool 回填。
func (c *gatewayChain) serveMediaJobPoll(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, traceID, apiKeyID, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, apiKeyID, jobID)
	if record == nil {
		return
	}
	if record.Status.Terminal() {
		c.writeMediaVideoJSON(res, http.StatusOK, chainMediaJobObjectOf(record, "", nil))
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode)
	method, path, _ := adapter.BuildPollRequest(record.UpstreamJobID)
	response, upstreamErr := c.mediaJobRequestUpstream(ctx, account, record.Kind, method, path, nil)
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
				c.writeMediaVideoJSON(res, http.StatusOK, chainMediaJobObjectOf(record, "", nil))
				return
			}
			c.renderMediaVideoLocalError(res, req, http.StatusBadGateway,
				fmt.Sprintf("上游%s任务轮询失败（状态码 %d）", chainMediaJobLabelOf(record), statusErr.StatusCode), "upstream_error")
			return
		}
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游媒体任务响应解析失败: "+parseErr.Error(), "upstream_error")
		return
	}
	if ir.Status.Terminal() {
		// 终态计费（媒体设计 §10：任务终态由轮询响应驱动回填）：completed 按
		// 计量维度 × 静态目录价估成本（视频 OutputVideoSeconds × 秒价、长音频
		// AudioInputSeconds × 秒价，契约 §2.8 网关自算口径），失败/无计量
		//（usage_missing）不虚计；成本随终态一次落行。
		costUsd := chainMediaJobTerminalCostUsd(record, ir)
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
			c.enqueueMediaJobTerminalUsage(ctx, traceID, apiKeyOwnerSystemAccountIDOf(req), record, account, ir, costUsd)
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
	c.writeMediaVideoJSON(res, http.StatusOK, chainMediaJobObjectOf(record, ir.Status, ir.Progress))
}

// serveMediaJobContent GET /v1/videos/{id}/content 与 GET /v1/audio/jobs/{id}/
// content：completed 才可下载；上游产物 http 流式转发（io.Copy 零缓冲落盘，
// content-type 透传——媒体设计 §8.1.2 纯流式透传代理；长音频产物是转写
// 结果 JSON 文件，契约 §10.2）。下载定位按 adapter 声明二分：openai 族由
// base_url + job id 构造（BuildContentRequest，认证头携带）；Veo/qwen 等
// （ContentFromArtifact）直连轮询冻结的绝对 URL（无账户凭据；URL 缺失即
// 产物从未产生或已被清理 → media_artifact_expired）。上游 404/410 →
// media_artifact_expired（产物可取回性由上游时效决定，网关不补救，§8.1.5）。
func (c *gatewayChain) serveMediaJobContent(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, apiKeyID, jobID)
	if record == nil {
		return
	}
	if record.Status != gatewaymedia.JobStatusCompleted {
		c.renderMediaVideoLocalError(res, req, http.StatusConflict,
			chainMediaJobLabelOf(record)+"任务未完成，产物不可下载（当前状态 "+string(record.Status)+"）", "media_job_not_completed")
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode)
	defaultContentType := "video/mp4"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		defaultContentType = "application/json"
	}
	var response *gatewayupstream.GatewayUpstreamResponse
	var upstreamErr error
	if adapter.ContentFromArtifact() {
		artifactURL := strings.TrimSpace(record.Artifact.ContentURL)
		if artifactURL == "" {
			c.renderMediaVideoLocalError(res, req, http.StatusGone, chainMediaJobLabelOf(record)+"产物已过期或已被上游清除", "media_artifact_expired")
			return
		}
		response, upstreamErr = c.mediaJobArtifactRequestUpstream(ctx, artifactURL)
	} else {
		method, path := adapter.BuildContentRequest(record.UpstreamJobID)
		response, upstreamErr = c.mediaJobRequestUpstream(ctx, account, record.Kind, method, path, nil)
	}
	if upstreamErr != nil {
		c.renderMediaVideoTransportError(res, req, upstreamErr)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.Status() == http.StatusNotFound || response.Status() == http.StatusGone {
		c.renderMediaVideoLocalError(res, req, http.StatusGone, chainMediaJobLabelOf(record)+"产物已过期或已被上游清除", "media_artifact_expired")
		return
	}
	if response.Status() < http.StatusOK || response.Status() >= http.StatusMultipleChoices {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway,
			fmt.Sprintf("上游%s产物下载失败（状态码 %d）", chainMediaJobLabelOf(record), response.Status()), "upstream_error")
		return
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = defaultContentType
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
		c.observability.Logger().Warn("media_job_content_stream_interrupted", map[string]any{
			"event": "media_job_content_stream_interrupted", "jobId": record.ID, "error": copyErr.Error(),
		}, "媒体产物流式转发中断")
	}
}

// serveMediaJobCancel DELETE /v1/videos/{id} 与 DELETE /v1/audio/jobs/{id}：
// 转发上游取消；上游 2xx、404（任务已被上游删除）或 405（上游不暴露取消
// 方法——M3 gemini 裁决：operations :cancel 不被支持时本地收敛 cancelled，
// 不把能力缺口透传成客户端错误）均置本地 cancelled，回 204。上游其他失败
// 透传错误、本地行不动。无上游取消 API 的 provider（glm/minimax/volcengine/
// qwen 万相与长转写，契约 §7.1/§9.1/§10.1/§10.2 回填面无取消端点）：
// SupportsCancel=false 时不发上游请求，直接本地收敛 cancelled（专用分支
// 而非 405 收敛——不发必然 404/405 的垃圾请求，cancelled 本就是 §2.6 的
// 本地终态语义）。
func (c *gatewayChain) serveMediaJobCancel(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, apiKeyID, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, apiKeyID, jobID)
	if record == nil {
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode)
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
	response, upstreamErr := c.mediaJobRequestUpstream(ctx, account, record.Kind, method, path, nil)
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
		fmt.Sprintf("上游%s任务取消失败（状态码 %d）", chainMediaJobLabelOf(record), status), "upstream_error")
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

// qwenVideoServiceRoot 是 qwen 媒体出站路径自带的服务根（契约 §10.1：
// dashscope.aliyuncs.com 官方端点 /api/v1/services/aigc/video-generation/
// video-synthesis 创建与 /api/v1/tasks/{task_id} 轮询族；§10.2 长转写
// /api/v1/services/audio/asr/transcription 创建与同一 /api/v1/tasks/{task_id}
// 轮询族共用该服务根）。
const qwenVideoServiceRoot = "/api/v1"

// chainQwenVideoUpstreamURL 归一 qwen 媒体任务面（万相视频 §10.1 与 paraformer
// 长转写 §10.2）的上游 URL：base + adapter 出站路径做直拼，base 已含 /api/v1
// 服务根时去重（镜像 chainGlmVideoUpstreamURL 的 /api/paas/v4 与
// chainVolcengineVideoUpstreamURL 的 /api/v3 去重先例）。不得走
// gatewayopenai.BuildUpstreamURL——它对非 /v1 结尾的 base 强制补 /v1
// （endpoint_test.go 钉住的 openai 族契约），会把官方根
// https://dashscope.aliyuncs.com 错拼 /v1/api/v1/...。任务面（创建/轮询）
// 只走本归一；产物下载直连轮询冻结的 output.video_url / transcription_url
// 绝对 URL，不经本函数。
func chainQwenVideoUpstreamURL(baseURL, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("qwen 媒体账户 base_url 无效: %q", baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	suffix := path
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	if suffix != qwenVideoServiceRoot && strings.HasSuffix(strings.ToLower(basePath), qwenVideoServiceRoot) {
		if trimmed, ok := strings.CutPrefix(suffix, qwenVideoServiceRoot); ok && trimmed != "" {
			suffix = trimmed
		}
	}
	parsed.Path = basePath + suffix
	return parsed.String(), nil
}

// chainQwenChatServiceRoot 是 qwen 对话出站的官方 OpenAI 兼容模式服务根
//（契约 §10：dashscope.aliyuncs.com/compatible-mode/v1/chat/completions）
// ——与媒体任务面的 /api/v1 服务根（qwenVideoServiceRoot）不同域，不可混用。
const chainQwenChatServiceRoot = "/compatible-mode/v1"

// chainIsChatCompletionsPathAndQuery 报告路径是否 chat completions 面（与
// requestMappingSourceFamilyOf 的 /chat/completions 判定同词表；query 剥离
// 后大小写不敏感匹配，模型映射与 responses→chat 桥改写后的路径一并命中）。
func chainIsChatCompletionsPathAndQuery(pathAndQuery string) bool {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	return strings.Contains(strings.ToLower(path), "/chat/completions")
}

// chainVolcengineChatUpstreamURL 归一 volcengine 对话（chat completions）的
// 上游 URL：官方 OpenAI 兼容端点 /api/v3/chat/completions 与媒体任务面共用
// /api/v3 服务根（volcengineVideoServiceRoot），base + 客户端版本剥离后的
// chat 路径直拼，base 已含 /api/v3 服务根时去重（镜像 chainVolcengineVideo
// UpstreamURL 先例）。不得走 gatewayopenai.BuildUpstreamURL——它对非 /v1
// 结尾的 base 强制补 /v1（endpoint_test.go 钉住的 openai 族契约），会把官方
// 根 https://ark.cn-beijing.volces.com 错拼 /v1/chat/completions。
func chainVolcengineChatUpstreamURL(baseURL, pathAndQuery string) (string, error) {
	return chainChatServiceRootUpstreamURL(baseURL, pathAndQuery, volcengineVideoServiceRoot, "volcengine")
}

// chainQwenChatUpstreamURL 归一 qwen 对话（chat completions）的上游 URL：
// 官方 OpenAI 兼容模式端点 /compatible-mode/v1/chat/completions（服务根
// chainQwenChatServiceRoot），base + 客户端版本剥离后的 chat 路径直拼，base
// 已含 /compatible-mode/v1 服务根时去重（镜像 chainQwenVideoUpstreamURL 的
// /api/v1 去重先例，服务根不同）。不得走 gatewayopenai.BuildUpstreamURL
// ——它会把官方根 https://dashscope.aliyuncs.com 错拼 /v1/chat/completions。
func chainQwenChatUpstreamURL(baseURL, pathAndQuery string) (string, error) {
	return chainChatServiceRootUpstreamURL(baseURL, pathAndQuery, chainQwenChatServiceRoot, "qwen")
}

// chainChatServiceRootUpstreamURL 是 volcengine/qwen 对话出站的服务根归一
// 主体：解析并校验 base_url，剥离客户端 /v1 版本前缀，base 未携带服务根时
// 插入服务根、已携带时去重，最后拼回 query（镜像 chainVolcengineVideo
// UpstreamURL 的解析与去重结构）。
func chainChatServiceRootUpstreamURL(baseURL, pathAndQuery, serviceRoot, label string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("%s 账户 base_url 无效: %q", label, baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	path, query := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	suffix := chainStripGatewayVersionPrefix(path)
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	if suffix != serviceRoot && !strings.HasSuffix(strings.ToLower(basePath), strings.ToLower(serviceRoot)) {
		suffix = serviceRoot + suffix
	}
	parsed.Path = basePath + suffix
	return parsed.String() + query, nil
}

// mediaJobRequestUpstream 账户亲和直连上游（不走派发循环；kind 区分任务族
// 的 adapter 注册表——M3f 起 video/audio_transcription 两族各自解析注册键，
// qwen 两族共用 chainQwenVideoUpstreamURL 的 /api/v1 去重）：URL 由账户
// base_url + adapter 出站路径归一——gemini 协议走 gatewaygemini.
// BuildUpstreamURL（/v1beta 前缀与 base 去重，M1 speech 创建链同先例），
// glm 媒体路径走 chainGlmVideoUpstreamURL（/api/paas/v4 服务根去重，M3
// glm cogvideo adapter），volcengine 媒体路径走 chainVolcengineVideoUpstreamURL
// （/api/v3 服务根去重，M3 seedance adapter，契约 §9.1），qwen 媒体路径走
// chainQwenVideoUpstreamURL（/api/v1 服务根去重，M3 万相 adapter 契约 §10.1
// 与 M3f paraformer 长转写契约 §10.2），其余（openai/xai/minimax 族——出站
// 路径均为 /v1 前缀形态）走 gatewayopenai.
// BuildUpstreamURL（/v1 后缀形态；xai 契约 §6.1 同 minimax 先例，无需专用
// 归一函数）；认证头按协议构造：gemini 沿
// applyGeminiUpstreamAuthHeaders（api_key → X-Goog-Api-Key、google_oauth →
// Bearer + 可选 x-goog-user-project，与主链认权分支同一实现，不重复实现），
// 其余维持最小 Bearer 集（任务面身份=网关，不透传客户端头——沿
// buildGeminiCodeAssistRequestParts 先例；glm/volcengine/qwen/xai 账户即 Bearer
// API Key，契约 §7.1/§9.1/§10.1/§10.2）；传输复用引擎 TransportDeps
// （URL 安全策略 / 全局并发槽 / keep-alive 池）。qwen 的 X-DashScope-Async
// 头只在万相视频创建面需要（DashScope 异步任务约定；paraformer 长转写天然
// 异步不需要），任务面轮询仅需 Authorization，不在此注入。
func (c *gatewayChain) mediaJobRequestUpstream(ctx context.Context, account *chainMediaJobAccount, kind gatewaymedia.MediaJobKind, method, path string, body []byte) (*gatewayupstream.GatewayUpstreamResponse, error) {
	adapterKey := ""
	if kind == gatewaymedia.JobKindAudioTranscription {
		adapterKey = chainAudioJobAdapterKeyOfProvider(account.ProviderCode)
	} else {
		adapterKey = chainVideoAdapterKeyOfProvider(account.ProviderCode)
	}
	if adapterKey == "" {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 无该种类媒体任务 adapter", account.ID, account.ProviderCode)
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
	} else if adapterKey == "qwen" {
		built, err := chainQwenVideoUpstreamURL(account.BaseURL, path)
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

// chainMediaJobTerminalCostUsd 计算终态成本（媒体设计 §10；契约 §2.8 网关
// 自算口径，M3f 起按 kind 分维度）：completed 且有计量时按静态定价目录
// （pricing 引擎，与 M1 音频行项同一引擎）秒数 × 秒价估 USD——视频取
// OutputVideoSeconds × 秒价，长转写取 AudioInputSeconds × 秒价（paraformer
// 目录未落 USD 秒价 → 估不出返回 0，计量照落成本不虚计，契约 §10.2）；
// 其余（failed/无计量 usage_missing/目录未命中）返回 0——失败任务不虚计、
// 无计量不猜测。计费键沿 chain_usage costModel 先例（请求模型直达）。
func chainMediaJobTerminalCostUsd(record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) float64 {
	if ir.Status != gatewaymedia.JobStatusCompleted {
		return 0
	}
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		if ir.Usage.AudioInputSeconds == nil || *ir.Usage.AudioInputSeconds <= 0 {
			return 0
		}
		cost := pricing.EstimateProviderCostUsd(pricing.CostInput{
			ProviderCode:      record.ProviderCode,
			Model:             record.RequestSnapshot.Model,
			AudioInputSeconds: ir.Usage.AudioInputSeconds,
		})
		if cost == nil {
			return 0
		}
		return *cost
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

// enqueueMediaJobTerminalUsage 经既有 usage spool 链写终态 usage_records
// （chain finalizationUsage 即 spooledUsageRecorder：有界异步缓冲 + spool
// 溢出落盘，与同步请求终态记录同一投递面，链上下文零额外依赖；M3f 起按
// kind 泛化——endpoint 与计量维度随任务族）。
// completed 携带任务族计量（视频 OutputVideoSeconds、长音频
// AudioInputSeconds，IR 从轮询响应抽取，契约 §2.8/§10.2）；completed 但上游
// 无计量回报 → usage_missing 标记不猜测；failed 不虚计（success=false 无
// 计量，行项为空）。cancelled/expired 是本地终态，不产生 usage 行（清理不
// 产生计费；取消按上游实际计量为准）。CostUsd 在 enqueue 前已可算（计量/
// 模型/供应商都在行上），直接带值落 usage 行——不存在"已 enqueue 留空回追"
// 的窗口（usage 行只在终态轮询时入队一次）；0 成本（usage_missing/未命中）
// 不设 CostUsd，与完成尝试记录"估算不出保持 NULL"的既有语义一致。记账
// scope 五元组从行内 request_snapshot 投影（创建时冻结，
// chainMediaJobSnapshotScopeOf）。
func (c *gatewayChain) enqueueMediaJobTerminalUsage(ctx context.Context, traceID, apiKeyOwnerSystemAccountID string, record *gatewaymedia.MediaJobRecord, account *chainMediaJobAccount, ir *gatewaymedia.MediaJobIR, costUsd float64) {
	if c.finalizationUsage == nil || (ir.Status != gatewaymedia.JobStatusCompleted && ir.Status != gatewaymedia.JobStatusFailed) {
		return
	}
	endpoint := "/v1/videos"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		endpoint = "/v1/audio/jobs"
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
		Endpoint:                    endpoint,
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
		if record.Kind == gatewaymedia.JobKindAudioTranscription {
			if ir.Usage.AudioInputSeconds != nil {
				input.AudioInputSeconds = ir.Usage.AudioInputSeconds
			} else {
				input.UsageMissing = true
			}
		} else if ir.Usage.OutputVideoSeconds != nil {
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
