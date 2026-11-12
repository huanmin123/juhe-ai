package main

// M2 异步媒体任务链（媒体设计 §4.2/§7/§8、契约 §2.2/§2.4/§4.3 视频 +
// §10.2 长转写；M3f 起任务面按 media_jobs.kind 泛化）。对外两族端点自
// 2026-11-12 New API 兼容批起都收敛到 New API 形态：视频只有
// /v1/video/generations*（旧 /v1/videos* 硬 404），长音频只有
// /v1/tasks/audio*（旧 /v1/audio/jobs* 硬 404）：
//
//   - 创建（POST /v1/video/generations 与 POST /v1/tasks/audio）：走既有派发循环（受理
//     边界生效——引擎 attempt 语义原样复用，429/5xx/网络错误换候选、耗尽
//     渲染既有契约，不新写重试）；出站报文经 gatewaymedia 注册表 adapter
//     构造（视频表与长转写表按 kind 分离，driver 内短路分支，与 M1 gemini
//     speech adapter 同模式）；2xx + job id = 受理凭据确立，落 media_jobs
//     后返回统一 job 对象（id 为对外 id，不用上游 id；video_ / audiojob_
//     前缀按 kind）。
//   - 任务面（两族同为轮询/content；New API 通用任务面无列表/取消/删除）：
//     不走派发循环，账户亲和直连
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
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
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
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

// ---------------------------------------------------------------------------
// 路径族判定（/videos 与 /audio/jobs 两族媒体任务路径的 chain 侧词表落点）
// ---------------------------------------------------------------------------

// chainVideoRequestShape 是媒体任务族请求形态（/videos 与 /audio/jobs 共用
// 形态词表：创建/列表/轮询/下载/取消五形态，媒体设计 §4.2 端点集——两族
// 按 kind 区分，形态语义同构；2026-11-12 New API 批起公开面只保留创建/
// 轮询/content 三形态，列表与取消在链内按 New API 语义 404）。
type chainVideoRequestShape int

const (
	chainVideoShapeNone    chainVideoRequestShape = iota
	chainVideoShapeCreate                         // POST /v1/videos | POST /v1/audio/jobs（内部词表）
	chainVideoShapeList                           // GET /v1/videos | GET /v1/audio/jobs（内部词表）
	chainVideoShapePoll                           // GET /v1/videos/{id} | GET /v1/audio/jobs/{id}
	chainVideoShapeContent                        // GET .../{id}/content
	chainVideoShapeCancel                         // DELETE .../{id}（内部词表）
)

// chainMediaJobStrippedPathOf 剥 /v1 前缀并归一路径（root 形态已由入口改写
// 补 /v1，沿 chainStripGatewayVersionPrefix 语义）。对外视频面自 2026-11-12
// New API 兼容批起只暴露 /video/generations*：这里把该前缀改写为内部既有
// /videos 词表（仅内部形态判定复用，公开路由不再注册旧 /videos 路径）。长
// 音频自 2026-11-12 同批迁到 New API 通用任务面：/tasks/audio* 改写为内部
// 既有 /audio/jobs* 词表（旧 /v1/audio/jobs* 公开路由已 404，词表仅供内部
// 形态判定与历史测试复用）。
func chainMediaJobStrippedPathOf(pathAndQuery string) string {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	stripped := chainStripGatewayVersionPrefix(strings.ToLower(strings.TrimSpace(path)))
	if stripped == "/video/generations" {
		return "/videos"
	}
	if rest, ok := strings.CutPrefix(stripped, "/video/generations/"); ok {
		return "/videos/" + rest
	}
	if stripped == "/tasks/audio" {
		return "/audio/jobs"
	}
	if rest, ok := strings.CutPrefix(stripped, "/tasks/audio/"); ok {
		return "/audio/jobs/" + rest
	}
	return stripped
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
// New API 兼容面；旧 /v1/videos 词表内部归一复用；kind 恒为 video）。
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

// chainIsLegacyVideoPath 报告请求是否旧 /v1/videos 族路径（2026-11-12 New
// API 兼容批起对外硬 404）。入口在协议门之后、preauth 之前显式拒绝：/v1
// 子树路由仍由链承接（kernel Register("/v1", chain)），仅删除根形态注册
// 不足以让 /v1/videos 404。
func chainIsLegacyVideoPath(pathAndQuery string) bool {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	stripped := chainStripGatewayVersionPrefix(path)
	return stripped == "/videos" || strings.HasPrefix(stripped, "/videos/")
}

// chainIsLegacyAudioJobPath 报告请求是否旧 /v1/audio/jobs 族路径（2026-11-12
// New API 通用任务面批起对外硬 404，长音频只保留 /v1/tasks/audio*）。判定只
// 命中 /audio/jobs 前缀，同步音频三端点 /audio/speech|transcriptions|
// translations 不受影响。同视频臂：/v1 子树整棵挂在链上，协议门会放行
// /audio/*，必须在协议门前显式拒绝旧路径。
func chainIsLegacyAudioJobPath(pathAndQuery string) bool {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	stripped := chainStripGatewayVersionPrefix(path)
	return stripped == "/audio/jobs" || strings.HasPrefix(stripped, "/audio/jobs/")
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
//   - gpt 账户 → "openai"：gpt 的媒体 adapter 键沿用 openai（对外契约即本体，
//     契约 §4.3）；注册键即 videoadapter.go 的 videoAdapters 表键
//     （VideoAdapterForProvider 内部小写归一）。通用 openai/hybrid 供应商已
//     随通用连接器退场，不再作为账户 vendor 出现。
//   - gemini 账户 → "gemini"（M3：api_key 与 google_oauth 两类账户同键，
//     认证头差异由任务面按协议/账户类型构造）：Veo predictLongRunning
//     形态（契约 §5.2），URL 前缀按账户 base_url + /v1beta。
//   - glm 账户 → "glm"（M3：CogVideoX videos/generations 形态，契约 §7.1；
//     账户 provider_code 恒为 glm，无子供应商代码）：api_key Bearer 认证
//     （沿链上 openai 族认权分支），URL 按 base_url 版本根直拼（v2 契约，
//     /api/paas/v4 服务根由 base_url 承载，opPath 不含版本段）。
//   - minimax 账户 → "minimax"（M3：Hailuo video_generation 形态，契约
//     §8.1）：api_key Bearer 认证（沿链上 openai 族认权分支），出站 opPath
//     不含版本段（video_generation / query/video_generation），URL 按
//     base_url 版本根直拼（v2 契约，/v1 由 base_url 承载——api.minimax.chat
//     与 api.minimaxi.com 两种官方 host 均适用，无需专用归一函数）。
//   - volcengine 账户 → "volcengine"（M3：Seedance contents/generations/
//     tasks 形态，契约 §9.1）：api_key Bearer 认证（沿链上 openai 族认权
//     分支），URL 按 base_url 版本根直拼（v2 契约，/api/v3 服务根由
//     base_url 承载，opPath 不含版本段）。
//   - qwen 账户 → "qwen"（M3 第五批：万相 video-synthesis 形态，契约
//     §10.1）：api_key Bearer 认证（沿链上 openai 族认权分支），出站 opPath
//     自带 /api/v1 全路径（创建 /api/v1/services/aigc/video-generation/
//     video-synthesis + 轮询 /api/v1/tasks/{id}；qwen 双根特例 base=origin），
//     URL 直拼（v2 契约）；创建
//     请求头带 X-DashScope-Async: enable（adapter ExtraHeaders，DashScope
//     异步任务约定）。
//   - xai 账户 → "xai"（M3 回填池：Grok Imagine Video 形态，契约 §6.1）：
//     api_key Bearer 认证（沿链上 openai 族认权分支——xai 出站认权与
//     openai 图像/文本同构），出站 opPath 不含版本段（创建
//     /videos/generations + 轮询 /videos/{request_id}），URL 按 base_url
//     版本根直拼（v2 契约，/v1 由 base_url 承载——api.x.ai 官方根 seed
//     base_url 已是 https://api.x.ai/v1，同 minimax 先例，无需专用归一函数）。
//   - 其余 provider（anthropic/deepseek 等）→ ""：无对应 adapter。
//     创建链在 driver 构造点显式失败（能力缺失不静默回退，媒体设计 §5
//     禁止项：不把厂商差异写进组合根 switch）；后续供应商接入起只新增
//     注册表条目与本映射分支。
func chainVideoAdapterKeyOfProvider(providerCode string) string {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "gpt":
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

// chainVideoAdapterForAccount 解析账户的视频 adapter；nil 表示该账户没有
// 可用的视频 adapter（调用方按能力缺失处理）。只按账户 vendor 解析：空 vendor
// （generic_http 历史形态）不再按协议面回退到 openai/gemini adapter。
func chainVideoAdapterForAccount(account gatewaydispatch.AccountCandidate) gatewaymedia.VideoProviderAdapter {
	key := chainVideoAdapterKeyForIdentity(account.VendorCode)
	if key == "" {
		return nil
	}
	return gatewaymedia.VideoAdapterForProvider(key)
}

// chainVideoAdapterKeyForIdentity 是视频 adapter 的统一身份解析（只按 vendor）。
// 媒体任务面只有 chainMediaJobAccount 的列式快照，复用本函数避免再造候选结构。
func chainVideoAdapterKeyForIdentity(vendorCode string) string {
	return chainVideoAdapterKeyOfProvider(vendorCode)
}

// chainAudioJobAdapterKeyOfProvider 把账户 provider_code 归一为长转写
// （kind=audio_transcription）adapter 注册键。映射规则：
//   - qwen 账户 → "qwen"（M3f：paraformer 录音文件识别形态，契约 §10.2；
//     api_key Bearer 认证，出站 opPath 自带 /api/v1 全路径——创建
//     /api/v1/services/audio/asr/transcription + 轮询 /api/v1/tasks/{id}
//     （与万相同一任务接口；qwen 双根特例 base=origin），URL 直拼（v2
//     契约）；创建请求**不带** X-DashScope-Async
//     头（该服务天然异步，与万相 §10.1 的关键差异）。
//   - volcengine：豆包录音文件识别 2.0 长转写（契约 §9.2 长转写臂，
//     2026-10-17 承接批）——出站 host 恒 openspeech.bytedance.com 语音服务域
//     （chainVolcengineASRUpstreamURL 特例，同 TTS 先例），鉴权 X-Api-Key
//     （speech_token），头驱动受理协议。
//   - 其余 provider（gemini/glm/minimax/gpt 等）→ ""：
//     无长转写 adapter（长转写面未回填的供应商接入前不承接，回填后只新增
//     audioJobAdapters 注册表条目与本映射分支）。
func chainAudioJobAdapterKeyOfProvider(providerCode string) string {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "qwen":
		return "qwen"
	case "volcengine":
		return "volcengine"
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
	// BuildPollRequest 返回轮询请求；header 为厂商专用头（body 驱动厂商
	// nil，头驱动厂商如火山长转写携带任务 ID 与资源 ID——契约 §9.2）。
	BuildPollRequest(upstreamJobID string) (method, path string, header http.Header, body []byte)
	// ParsePollResponse 归一上游响应；header 携带响应头（头驱动厂商经
	// X-Api-Status-Code 承载业务状态）。
	ParsePollResponse(statusCode int, header http.Header, body []byte) (*gatewaymedia.MediaJobIR, error)
	BuildContentRequest(upstreamJobID string) (method, path string, header http.Header, body []byte)
	ContentFromArtifact() bool
	BuildCancelRequest(upstreamJobID string) (method, path string)
	SupportsCancel() bool
}

// chainMediaJobTaskAdapterFor 按任务种类解析账户的任务面 adapter；nil 表示
// 该账户无该 kind 的 adapter（调用方按能力缺失处理）。视频面与创建计划共用
// chainVideoAdapterForAccount 的 vendor/协议面解析；长转写仍只认 qwen/volcengine。
func chainMediaJobTaskAdapterFor(kind gatewaymedia.MediaJobKind, vendorCode, protocolCode, model string) chainMediaJobTaskAdapter {
	switch kind {
	case gatewaymedia.JobKindAudioTranscription:
		return chainAudioJobAdapterForAccount(vendorCode)
	default:
		key := chainVideoAdapterKeyForIdentity(vendorCode)
		if key == "" {
			return nil
		}
		return chainVideoJobTaskAdapterShell{inner: gatewaymedia.VideoAdapterForProvider(key), model: model}
	}
}

// chainVideoJobTaskAdapterShell 把视频任务 adapter 适配到统一任务面消费形状
// （视频面全部为 body 驱动协议，header 位恒 nil；头驱动位由火山长转写臂的
// AudioJobProviderAdapter 直接满足——契约 §9.2 长转写臂）。model 是创建时
// 冻结的模型 id：同一 provider 注册键下 Hailuo v1 与 H3 v2 任务面协议不同
// （VideoModelTaskAdapter 可选扩展），轮询面按它选路径与响应形态。
type chainVideoJobTaskAdapterShell struct {
	inner gatewaymedia.VideoProviderAdapter
	model string
}

func (s chainVideoJobTaskAdapterShell) Provider() string { return s.inner.Provider() }

func (s chainVideoJobTaskAdapterShell) BuildPollRequest(upstreamJobID string) (string, string, http.Header, []byte) {
	if modelAware, ok := s.inner.(gatewaymedia.VideoModelTaskAdapter); ok {
		method, path, body := modelAware.BuildPollRequestForModel(s.model, upstreamJobID)
		return method, path, nil, body
	}
	method, path, body := s.inner.BuildPollRequest(upstreamJobID)
	return method, path, nil, body
}

func (s chainVideoJobTaskAdapterShell) ParsePollResponse(statusCode int, _ http.Header, body []byte) (*gatewaymedia.MediaJobIR, error) {
	if modelAware, ok := s.inner.(gatewaymedia.VideoModelTaskAdapter); ok {
		return modelAware.ParsePollResponseForModel(s.model, statusCode, body)
	}
	return s.inner.ParsePollResponse(statusCode, body)
}

func (s chainVideoJobTaskAdapterShell) BuildContentRequest(upstreamJobID string) (string, string, http.Header, []byte) {
	method, path := s.inner.BuildContentRequest(upstreamJobID)
	return method, path, nil, nil
}

func (s chainVideoJobTaskAdapterShell) ContentFromArtifact() bool {
	return s.inner.ContentFromArtifact()
}

func (s chainVideoJobTaskAdapterShell) BuildCancelRequest(upstreamJobID string) (string, string) {
	return s.inner.BuildCancelRequest(upstreamJobID)
}

func (s chainVideoJobTaskAdapterShell) SupportsCancel() bool { return s.inner.SupportsCancel() }

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

// chainAudioSyncBusinessRejectionStatus 报告音频同步 lane 上游非 2xx 状态码
// 是否确定性业务拒绝（BUG-0326 档案分级）：4xx 中除保留集（401 凭据失效、
// 403 显式策略/系统额度语义、407 上游代理鉴权、408 上游请求超时、429 受理
// 前限流——五者维持既有失败派发语义：错误策略状态机 + 候选切换 + 耗尽 503）
// 之外的状态，对同一请求换任何账户都会原样重演，属于请求侧参数/业务校验
// 拒绝，按档案透传上游状态码与错误信封、不进重试循环。5xx/传输错误不在此
// 列（维持既有重试 + 耗尽 503）。
func chainAudioSyncBusinessRejectionStatus(statusCode int) bool {
	if statusCode < http.StatusBadRequest || statusCode >= http.StatusInternalServerError {
		return false
	}
	switch statusCode {
	case http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusProxyAuthRequired,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests:
		return false
	}
	return true
}

// chainMediaSyncAudioRejectionShortCircuit 报告失败派发输入是否命中"音频同
// 步业务拒绝短路"（BUG-0326 修复）：音频同步 lane（LaneAudio；speech 与
// transcriptions/translations 同步端点族——gatewaymedia/lane.go 路径词表，
// 火山 TTS create/unidirectional 两出站面同属本 lane）+ 确定性业务拒绝 4xx。
// 消费点与创建参数错误短路同位（chain_ports.go HandleFailedUpstreamResponse，
// 审计尝试与 usage 失败记录落账之后、SkipAccount 决策之前）。
func chainMediaSyncAudioRejectionShortCircuit(requestLane string, statusCode int) bool {
	return requestLane == string(gatewayproto.LaneAudio) &&
		chainAudioSyncBusinessRejectionStatus(statusCode)
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
// New API 兼容面请求归一（POST /v1/video/generations，2026-11-12 裁决）
// ---------------------------------------------------------------------------

// chainNewAPIVideoBody 是 New API 兼容面创建请求的归一产物：
//   - body 是内部统一参数体（duration→seconds、width+height→size、
//     image→input_reference、metadata 已知 L2 键提升），供 gatewaymedia.
//     ParseVideoParams 与各厂商 adapter 复用既有映射；
//   - metadata 是回显用元数据（请求 metadata 原样 + fps/response_format/user
//     等无统一语义键，创建时随任务快照冻结）；
//   - providerOptions 是 metadata 中未被提升的其余键（选择账户后并入该厂商
//     provider_options 子对象 deep-merge，键=adapter 注册键）；
//   - extraIgnored 是本地判定为「无统一语义」而计入 params_ignored 的键名
//     （fps/response_format/user、单边 width/height）。
type chainNewAPIVideoBody struct {
	body            map[string]any
	metadata        map[string]any
	providerOptions map[string]any
	extraIgnored    []string
}

// chainNewAPIVideoMetadataPromotedKeys 是 metadata 中提升为公共参数 L2 的
// 已知键（契约 §2.2 metadata 行：negative_prompt/audio/seed）。
var chainNewAPIVideoMetadataPromotedKeys = []string{"negative_prompt", "audio", "seed"}

// chainNewAPIVideoLocalIgnoredKeys 是当前无统一语义、只回显计入
// params_ignored 的顶层键（契约 §2.2：fps/response_format/user）。
var chainNewAPIVideoLocalIgnoredKeys = []string{"fps", "response_format", "user"}

// chainNewAPIVideoBodyOf 把 New API 兼容面创建请求体归一为内部统一参数体。
// 不修改入参（复制 map）。拒绝面（本地 400，不换账户）：
//   - 公开 provider_options 出现即 400（唯一扩展通道已切到 metadata）；
//   - metadata 必须是 JSON 对象；
//   - width/height 必须是正整数（单边缺失进 ignored，不报错）。
func chainNewAPIVideoBodyOf(body map[string]any) (*chainNewAPIVideoBody, error) {
	normalized := make(map[string]any, len(body)+1)
	for key, value := range body {
		normalized[key] = value
	}
	if _, present := normalized[gatewaymedia.ProviderOptionsKey]; present {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(
			"provider_options 已退役，扩展参数请改用 metadata 对象")
	}
	metadata := map[string]any{}
	if raw, present := body["metadata"]; present && raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, gatewaypreauth.NewGatewayRequestValidationError("metadata 必须是 JSON 对象")
		}
		for key, value := range object {
			metadata[key] = value
		}
	}
	// metadata 已知 L2 键提升为公共参数（顶层显式值优先，不覆盖请求者本意）。
	for _, key := range chainNewAPIVideoMetadataPromotedKeys {
		if value, ok := metadata[key]; ok {
			if _, exists := normalized[key]; !exists {
				normalized[key] = value
			}
		}
	}
	// 其余 metadata 键仍须从原始 metadata 中剔除已提升键后才并入厂商
	// provider_options 子对象（提升键是公共参数，不得重复作为厂商私有键）。
	providerOptions := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if slices.Contains(chainNewAPIVideoMetadataPromotedKeys, key) {
			continue
		}
		providerOptions[key] = value
	}
	// duration → seconds（容忍旧 seconds 别名；duration 显式出现时以它为准）。
	if raw, present := body["duration"]; present && raw != nil {
		normalized["seconds"] = raw
	}
	// width + height → size（WxH 像素串）。单边缺失忽略并回显，不猜测另一边。
	width, widthPresent, widthErr := chainNewAPIVideoDimensionOf(body, "width")
	if widthErr != nil {
		return nil, widthErr
	}
	height, heightPresent, heightErr := chainNewAPIVideoDimensionOf(body, "height")
	if heightErr != nil {
		return nil, heightErr
	}
	if widthPresent && heightPresent {
		normalized["size"] = fmt.Sprintf("%dx%d", width, height)
	}
	// image → input_reference（容忍旧 input_reference 别名；image 显式出现时
	// 以它为准）。
	if raw, present := body["image"]; present && raw != nil {
		normalized["input_reference"] = raw
	}
	var extraIgnored []string
	for _, key := range chainNewAPIVideoLocalIgnoredKeys {
		raw, present := body[key]
		if !present || raw == nil {
			continue
		}
		extraIgnored = append(extraIgnored, key)
		metadata[key] = raw
	}
	if widthPresent && !heightPresent {
		extraIgnored = append(extraIgnored, "width")
	}
	if !widthPresent && heightPresent {
		extraIgnored = append(extraIgnored, "height")
	}
	return &chainNewAPIVideoBody{
		body:            normalized,
		metadata:        metadata,
		providerOptions: providerOptions,
		extraIgnored:    extraIgnored,
	}, nil
}

// chainNewAPIVideoDimensionOf 解析 width/height 正整数维度：未传/空值返回
// (0,false,nil)；形态非法返回 400 校验错误（语义无法表达，不猜测）。
func chainNewAPIVideoDimensionOf(body map[string]any, key string) (int, bool, error) {
	raw, present := body[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	value, ok := raw.(float64)
	if !ok || value != math.Trunc(value) || value <= 0 {
		return 0, false, gatewaypreauth.NewGatewayRequestValidationError(
			fmt.Sprintf("视频生成请求 %s 必须是正整数，实际为 %v", key, raw))
	}
	return int(value), true, nil
}

// ---------------------------------------------------------------------------
// New API 通用任务面长音频请求归一（POST /v1/tasks/audio，2026-11-12 裁决）
// ---------------------------------------------------------------------------

// chainNewAPIAudioMetadataPromotedKeys 是长音频创建请求 metadata 中提升为
// 公共参数的已知键（契约：`language` 等已知键提升，顶层显式值优先）。
var chainNewAPIAudioMetadataPromotedKeys = []string{"language"}

// chainNewAPIAudioBody 是 New API 通用任务面长音频创建请求的归一产物：
//   - body 是内部统一参数体（model/input_url 原样保留，metadata 已知键提升，
//     provider_options 由 metadata 其余键注入），供 gatewaymedia.
//     ParseAudioJobParams 与长转写 adapter 复用既有映射；
//   - metadata 是回显用元数据（请求 metadata 原样，创建时随任务快照冻结）；
//   - providerOptions 是 metadata 中未被提升的其余键（并入该厂商
//     provider_options 子对象，键=adapter 注册键）；
//   - extraIgnored 是本地判定为「无统一语义」而计入 params_ignored 的键名
//     （当前长音频面无本地忽略键，保留结构供后续扩展）。
type chainNewAPIAudioBody struct {
	body            map[string]any
	metadata        map[string]any
	providerOptions map[string]any
	extraIgnored    []string
}

// chainNewAPIAudioBodyOf 把 New API 通用任务面长音频创建请求体归一为内部
// 统一参数体。不修改入参（复制 map）。拒绝面（本地 400，不换账户）：
//   - 公开 provider_options 出现即 400（唯一扩展通道已切到 metadata）；
//   - metadata 必须是 JSON 对象。
//
// vendor 是该账户的 adapter 注册键（adapter.Provider()）：metadata 其余键
// 选择账户后并入该厂商 provider_options 子对象（deep-merge 语义与既有 L3
// 完全一致）。
func chainNewAPIAudioBodyOf(body map[string]any, vendor string) (*chainNewAPIAudioBody, error) {
	normalized := make(map[string]any, len(body)+1)
	for key, value := range body {
		normalized[key] = value
	}
	if _, present := normalized[gatewaymedia.ProviderOptionsKey]; present {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(
			"provider_options 已退役，扩展参数请改用 metadata 对象")
	}
	metadata := map[string]any{}
	if raw, present := body["metadata"]; present && raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, gatewaypreauth.NewGatewayRequestValidationError("metadata 必须是 JSON 对象")
		}
		for key, value := range object {
			metadata[key] = value
		}
	}
	// metadata 已知键提升为公共参数（顶层显式值优先，不覆盖请求者本意）。
	for _, key := range chainNewAPIAudioMetadataPromotedKeys {
		if value, ok := metadata[key]; ok {
			if _, exists := normalized[key]; !exists {
				normalized[key] = value
			}
		}
	}
	// 其余 metadata 键并入该厂商 provider_options 子对象（提升键是公共参数，
	// 不得重复作为厂商私有键）。
	providerOptions := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if slices.Contains(chainNewAPIAudioMetadataPromotedKeys, key) {
			continue
		}
		providerOptions[key] = value
	}
	if len(providerOptions) > 0 && strings.TrimSpace(vendor) != "" {
		normalized[gatewaymedia.ProviderOptionsKey] = map[string]any{
			strings.TrimSpace(vendor): providerOptions,
		}
	}
	return &chainNewAPIAudioBody{
		body:            normalized,
		metadata:        metadata,
		providerOptions: providerOptions,
	}, nil
}

// ---------------------------------------------------------------------------
// 创建链计划（driver 出站构造与响应面落表共用的一次性解析）
// ---------------------------------------------------------------------------

// chainVideoCreatePlan 是视频创建请求的解析产物：adapter 出站报文 + 归一
// 参数（params_applied/params_ignored 回显来源）+ provider_options 生效键名
// 摘要（provider_options_applied 回显来源，契约 §2.4 规则 4）+ New API
// metadata 回显源（请求 metadata 原样 + 本地 ignored 键）。
type chainVideoCreatePlan struct {
	adapter                gatewaymedia.VideoProviderAdapter
	createRequest          gatewaymedia.VideoCreateOutput
	params                 gatewaymedia.NormalizedVideoParams
	providerOptionsApplied []string
	metadata               map[string]any
	extraIgnored           []string
}

// jobKind/snapshotFields/paramsApplied/paramsIgnored 实现
// chainMediaJobCreatePlanCommon（创建响应面按 kind 泛化的访问器，M3f）。
func (p *chainVideoCreatePlan) jobKind() gatewaymedia.MediaJobKind {
	return gatewaymedia.JobKindVideo
}

func (p *chainVideoCreatePlan) snapshotFields() gatewaymedia.MediaJobRequestSnapshot {
	// 分辨率档：仅实现 VideoBillingResolutionProvider 的厂商声明（xAI/
	// Volcengine/Qwen），其余返回空串，结算回落主档价（契约 §2.8.3）。
	billingResolution := ""
	if provider, ok := p.adapter.(gatewaymedia.VideoBillingResolutionProvider); ok {
		billingResolution = provider.BillingResolution(p.params)
	}
	return gatewaymedia.MediaJobRequestSnapshot{
		Model:   p.params.Model,
		Prompt:  p.params.Prompt,
		Seconds: p.params.Seconds,
		// 计费秒数基源与请求回显分开冻结：seconds 可能被 adapter 忽略（Veo）
		// 或未传（minimax），此时按厂商默认输出时长计费（契约 §2.8.2）。
		BillingSeconds:         p.params.BillingSeconds(p.adapter.Capabilities()),
		BillingResolution:      billingResolution,
		Size:                   p.params.Size,
		N:                      p.params.N,
		ProviderOptionsApplied: p.providerOptionsApplied,
		Metadata:               p.metadata,
	}
}

func (p *chainVideoCreatePlan) paramsApplied() []string { return p.params.ParamsApplied }

// paramsIgnored 合并公共参数能力裁决的 ignored 与 New API 面本地无统一语义
// 键（fps/response_format/user、单边 width/height）。顺序稳定：能力裁决在前
// （契约 §2.2 表序），本地键按固定表序追加。
func (p *chainVideoCreatePlan) paramsIgnored() []string {
	merged := make([]string, 0, len(p.params.ParamsIgnored)+len(p.extraIgnored))
	merged = append(merged, p.params.ParamsIgnored...)
	merged = append(merged, p.extraIgnored...)
	return merged
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
	// 供应商身份源（供应商与上游协议面重构设计 §1.1/§2.1 切片）：创建计划
	// 是 driver 出站构造与创建响应面/媒体车道预过滤的共用入口，adapter 身份
	// 键取 VendorCode。
	adapter := chainVideoAdapterForAccount(account)
	if adapter == nil {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 暂不支持视频生成", account.ID, account.VendorCode)
	}
	if body == nil {
		return nil, nil
	}
	// New API 兼容面归一（2026-11-12）：公开扩展通道切到 metadata，旧
	// provider_options 出现即 400；duration/width/height/image 归一为内部
	// seconds/size/input_reference 后由 ParseVideoParams 与 adapter 复用既有
	// 映射（对外字段名不进入内部参数层）。
	newAPIBody, err := chainNewAPIVideoBodyOf(body)
	if err != nil {
		return nil, err
	}
	body = newAPIBody.body
	// 扩展参数唯一通道：metadata 的其余键在选择账户后并入该厂商
	// provider_options 子对象（键=adapter 注册键，deep-merge 语义与既有 L3
	// 完全一致）。命中键名摘要与实际合并同源（AppliedProviderOptionKeys）。
	var providerOptions map[string]any
	if len(newAPIBody.providerOptions) > 0 {
		providerOptions = map[string]any{adapter.Provider(): newAPIBody.providerOptions}
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
	// 按次时长档（契约 §2.2 固定档规则 / 2026-11-12 档位批）：目录行带
	// 「条 × 时长」档位价时，把请求秒数收敛到最近档并回显出站，保证上游
	// 实际生成的时长与终端结算选档一致（如 Hailuo 7s → 6s 档）。
	if params.Seconds != nil {
		if tier := pricing.VideoCallTierSeconds(adapter.Provider(), params.Model, *params.Seconds); tier != nil {
			params.Seconds = tier
		}
	}
	// L3 计费字段封禁（2026-11-13 T219 收口）：metadata/provider_options 中
	// duration/resolution/size/content/image 等会改变实际产出，而计费基源
	// 已按顶层参数冻结；命中 400，不静默丢弃。
	if err := gatewaymedia.ValidateVideoBillingProviderOptions(adapter.Provider(), providerOptions); err != nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
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
	return &chainVideoCreatePlan{
		adapter:                adapter,
		createRequest:          createRequest,
		params:                 params,
		providerOptionsApplied: providerOptionsApplied,
		metadata:               newAPIBody.metadata,
		extraIgnored:           newAPIBody.extraIgnored,
	}, nil
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
		ModelMappings: openAIModelMappingsOf(account.ModelMappings),
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
// 生效键名摘要 + New API metadata 回显源）。
type chainAudioJobCreatePlan struct {
	adapter                gatewaymedia.AudioJobProviderAdapter
	createRequest          gatewaymedia.AudioJobCreateOutput
	params                 gatewaymedia.NormalizedAudioJobParams
	providerOptionsApplied []string
	metadata               map[string]any
	extraIgnored           []string
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
		Metadata:               p.metadata,
	}
}

func (p *chainAudioJobCreatePlan) paramsApplied() []string { return p.params.ParamsApplied }

// paramsIgnored 合并公共参数能力裁决的 ignored 与 New API 面本地无统一语义
// 键（长音频面当前无本地忽略键）。顺序稳定：能力裁决在前，本地键追加。
func (p *chainAudioJobCreatePlan) paramsIgnored() []string {
	merged := make([]string, 0, len(p.params.ParamsIgnored)+len(p.extraIgnored))
	merged = append(merged, p.params.ParamsIgnored...)
	merged = append(merged, p.extraIgnored...)
	return merged
}

// chainAudioJobCreatePlanOf 解析长音频任务创建请求体并构造 adapter 出站报文
// （媒体设计 §4.2/§6）。与 chainVideoCreatePlanOf 的差异：body 为 nil（请求体
// 非 JSON 对象——multipart 文件输入）是**显式 400**而非 (nil, nil)：长音频
// 任务唯一输入形态是 input_url 公网 URL（契约 §10.2 上游只收 file_urls、
// 零存储不暂存），multipart 对任何账户都会重演，直接本地 400 不换账户。
// New API 通用任务面归一：公开 provider_options 退役、metadata 已知键提升为
// 公共参数、其余 metadata 键在选择账户后并入该厂商 provider_options 子对象。
// 长转写无模型映射面（hybrid 不承接 audio jobs，M4b 只放开 video_generation/
// tts 两族），模型名仅走 canonical 拼写优先。
func chainAudioJobCreatePlanOf(body map[string]any, req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) (*chainAudioJobCreatePlan, error) {
	// 供应商身份源切换（与 chainVideoCreatePlanOf 同批，见彼处注释）。
	adapter := chainAudioJobAdapterForAccount(account.VendorCode)
	if adapter == nil {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 暂不支持长音频转写", account.ID, account.VendorCode)
	}
	if body == nil {
		return nil, gatewaypreauth.NewGatewayRequestValidationError("长音频任务请求体必须是 JSON 对象（唯一输入形态 input_url 公网音频 URL，不支持 multipart 文件输入）")
	}
	newAPIBody, err := chainNewAPIAudioBodyOf(body, adapter.Provider())
	if err != nil {
		return nil, err
	}
	providerOptions, err := chainVideoProviderOptionsOf(newAPIBody.body)
	if err != nil {
		return nil, err
	}
	params, err := gatewaymedia.ParseAudioJobParams(newAPIBody.body, adapter.Capabilities())
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
		metadata:               newAPIBody.metadata,
		extraIgnored:           newAPIBody.extraIgnored,
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
// 对外 job 对象（契约 §4.2 New API 通用任务面 + §2.4 规则 5 固定回显字段）
// ---------------------------------------------------------------------------

// chainMediaJobObjectOf 按行内 kind 渲染任务面查询对象（轮询响应共用入口）。
// 两族自 2026-11-12 New API 兼容批起统一用查询形态（扁平字段 +
// code/message/data 包裹）：视频 object=video、format 缺省 mp4；长音频
// object=task、format 缺省 json。
func chainMediaJobObjectOf(record *gatewaymedia.MediaJobRecord, liveStatus gatewaymedia.MediaJobStatus, liveProgress *int, publicBaseURL string) any {
	return chainNewAPIQueryObjectOf(record, liveStatus, liveProgress, publicBaseURL)
}

// ---------------------------------------------------------------------------
// New API 兼容面响应渲染（创建 / 查询，契约 §4.2）
// ---------------------------------------------------------------------------

// chainNewAPIVideoCreateObjectOf 渲染 New API 创建响应
// `{id, task_id, status:"queued", object, model, created_at, metadata}`。
// id 与 task_id 同值（对外任务 id，不用上游 id）。object 按 kind：视频
// "video"、长音频 "task"（New API 通用任务面）。
func chainNewAPIVideoCreateObjectOf(record *gatewaymedia.MediaJobRecord) map[string]any {
	object := "video"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		object = "task"
	}
	return map[string]any{
		"id":         record.ID,
		"task_id":    record.ID,
		"status":     string(gatewaymedia.JobStatusQueued),
		"object":     object,
		"model":      record.RequestSnapshot.Model,
		"created_at": record.CreatedAt.Unix(),
		"metadata":   chainNewAPIMetadataOf(record),
	}
}

// chainNewAPIQueryObjectOf 渲染 New API 查询响应：扁平字段与
// `{code:"success",message:"",data:{...}}` 包裹同时提供（兼容 New API 文档
// 与源码两种客户端），扁平与 data 内均含 task_id/status/url/format/metadata/
// error（长音频另带 object:"task"）。status 词表收敛为
// queued/in_progress/completed/failed（本地 cancelled/expired 归一 failed 并
// 携带 error）。url 优先上游产物直链；为空且 completed 时回退网关 content
// 端点（需 API Key）：配置了公共地址前缀（JUHE_AI_GATEWAY_PUBLIC_BASE_URL，
// New API ServerAddress 同构）时回绝对地址，未配置时回相对路径（客户端自行
// 拼 Host）。format 按 url 扩展名推断，缺省按任务族（视频 mp4、
// 长音频 json）。progress 仅在轮询上游即时可得时附带（契约未定义该字段，
// 额外字段不参与归类）。
func chainNewAPIQueryObjectOf(record *gatewaymedia.MediaJobRecord, liveStatus gatewaymedia.MediaJobStatus, liveProgress *int, publicBaseURL string) map[string]any {
	status := record.Status
	if liveStatus != "" {
		status = liveStatus
	}
	normalizedStatus, jobError := chainNewAPIQueryStatusOf(record, status)
	artifactURL := strings.TrimSpace(record.Artifact.ContentURL)
	// 直链能力判定：openai/Sora 的 content[].url 需要上游账户认证、火山长转
	// 写无产物 URL，不能直接交给客户端；沿用 adapter 的 ContentFromArtifact
	// 能力声明，非直链厂商强制回退网关 content 端点，避免把不可用的上游地址
	// 当 New API url 返回。其余厂商保持上游产物直链（2026-11-12 用户裁决）。
	if !chainMediaJobArtifactDirectURL(record) {
		artifactURL = ""
	}
	if artifactURL == "" && normalizedStatus == string(gatewaymedia.JobStatusCompleted) {
		artifactURL = chainNewAPIContentFallbackURLOf(record, publicBaseURL)
	}
	data := map[string]any{
		"task_id":  record.ID,
		"status":   normalizedStatus,
		"url":      artifactURL,
		"format":   chainNewAPIFormatOf(record.Kind, artifactURL),
		"metadata": chainNewAPIMetadataOf(record),
		"error":    jobError,
	}
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		data["object"] = "task"
	}
	if liveProgress != nil {
		data["progress"] = *liveProgress
	}
	object := make(map[string]any, len(data)+3)
	for key, value := range data {
		object[key] = value
	}
	object["code"] = "success"
	object["message"] = ""
	object["data"] = data
	return object
}

// chainMediaJobArtifactDirectURL 报告任务的产物地址能否作为 New API `url`
// 直出（adapter 声明 ContentFromArtifact=true 才直出；视频/长转写共用同一
// 能力声明语义，注册表键按 kind 经 adapter 键归一解析——vendor 别名
// （gpt→openai）不得靠 nil 短路隐式决定直链面）。
func chainMediaJobArtifactDirectURL(record *gatewaymedia.MediaJobRecord) bool {
	if record == nil {
		return false
	}
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		adapter := gatewaymedia.AudioJobAdapterForProvider(chainAudioJobAdapterKeyOfProvider(record.VendorCode))
		return adapter != nil && adapter.ContentFromArtifact()
	}
	adapter := gatewaymedia.VideoAdapterForProvider(chainVideoAdapterKeyForIdentity(record.VendorCode))
	return adapter != nil && adapter.ContentFromArtifact()
}

// chainNewAPIContentFallbackURLOf 返回 completed 且无直链时的网关 content
// 回退地址：配置了公共地址前缀时为绝对 URL（New API BuildProxyURL 的
// ServerAddress 拼接同构），未配置时为相对路径。
func chainNewAPIContentFallbackURLOf(record *gatewaymedia.MediaJobRecord, publicBaseURL string) string {
	path := "/v1/video/generations/"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		path = "/v1/tasks/audio/"
	}
	fallback := path + record.ID + "/content"
	if publicBaseURL == "" {
		return fallback
	}
	return strings.TrimSuffix(publicBaseURL, "/") + fallback
}

// chainNewAPIQueryStatusOf 归一查询状态到 New API 词表
// queued/in_progress/completed/failed。本地 cancelled/expired 归一 failed，
// 并保证携带 error（code 取本地终态原文，message 为可读说明）；其余状态
// 原样透出，failed 携带行内终态错误摘要（如有）。
func chainNewAPIQueryStatusOf(record *gatewaymedia.MediaJobRecord, status gatewaymedia.MediaJobStatus) (string, any) {
	label := chainMediaJobLabelOf(record)
	switch status {
	case gatewaymedia.JobStatusCancelled:
		return string(gatewaymedia.JobStatusFailed), chainNewAPIErrorOf(&gatewaymedia.MediaJobError{
			Code:    string(gatewaymedia.JobStatusCancelled),
			Message: label + "任务已取消",
		})
	case gatewaymedia.JobStatusExpired:
		return string(gatewaymedia.JobStatusFailed), chainNewAPIErrorOf(&gatewaymedia.MediaJobError{
			Code:    string(gatewaymedia.JobStatusExpired),
			Message: label + "任务已过期",
		})
	case gatewaymedia.JobStatusFailed:
		return string(gatewaymedia.JobStatusFailed), chainNewAPIErrorOf(record.Error)
	default:
		return string(status), nil
	}
}

// chainNewAPIErrorOf 把行内错误摘要渲染为 New API 查询响应的 error 对象
// （{code,message}；空字段省略）；nil 保持 JSON null（无错误）。
func chainNewAPIErrorOf(jobError *gatewaymedia.MediaJobError) any {
	if jobError == nil {
		return nil
	}
	object := map[string]any{}
	if jobError.Code != "" {
		object["code"] = jobError.Code
	}
	if jobError.Message != "" {
		object["message"] = jobError.Message
	}
	return object
}

// chainNewAPIMetadataOf 渲染 metadata 回显：请求 metadata 原样（创建时冻结
// 进任务快照）+ 诊断字段 provider/provider_job_id/params_applied/
// params_ignored/provider_options_applied（只键名，不含资源字节）。
func chainNewAPIMetadataOf(record *gatewaymedia.MediaJobRecord) map[string]any {
	metadata := make(map[string]any, len(record.RequestSnapshot.Metadata)+5)
	for key, value := range record.RequestSnapshot.Metadata {
		metadata[key] = value
	}
	metadata["provider"] = record.VendorCode
	metadata["provider_job_id"] = record.UpstreamJobID
	metadata["params_applied"] = stringListOrEmpty(record.ParamsApplied)
	metadata["params_ignored"] = stringListOrEmpty(record.ParamsIgnored)
	metadata["provider_options_applied"] = stringListOrEmpty(record.RequestSnapshot.ProviderOptionsApplied)
	return metadata
}

// stringListOrEmpty 返回非 nil 键名列表（JSON 回显恒为数组，不落 null）。
func stringListOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// chainNewAPIFormatOf 按产物 URL 扩展名推断 New API `format` 字段；无扩展名
// （含 content 回退相对路径）按任务族缺省：视频 mp4、长音频 json。
func chainNewAPIFormatOf(kind gatewaymedia.MediaJobKind, rawURL string) string {
	defaultFormat := "mp4"
	if kind == gatewaymedia.JobKindAudioTranscription {
		defaultFormat = "json"
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return defaultFormat
	}
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(parsed.Path)), ".")
	if extension == "" {
		return defaultFormat
	}
	return extension
}

// chainNewAPIVideoQueryObjectOf 是视频行查询渲染入口（泛化实现见
// chainNewAPIQueryObjectOf；保留视频侧既有调用与测试词表）。
func chainNewAPIVideoQueryObjectOf(record *gatewaymedia.MediaJobRecord, liveStatus gatewaymedia.MediaJobStatus, liveProgress *int, publicBaseURL string) map[string]any {
	return chainNewAPIQueryObjectOf(record, liveStatus, liveProgress, publicBaseURL)
}

// chainNewAPIVideoFormatOf 是视频 format 推断入口（缺省 mp4，泛化实现见
// chainNewAPIFormatOf）。
func chainNewAPIVideoFormatOf(rawURL string) string {
	return chainNewAPIFormatOf(gatewaymedia.JobKindVideo, rawURL)
}

// chainNewAPIVideoMetadataOf 是视频 metadata 回显入口（泛化实现见
// chainNewAPIMetadataOf）。
func chainNewAPIVideoMetadataOf(record *gatewaymedia.MediaJobRecord) map[string]any {
	return chainNewAPIMetadataOf(record)
}

// chainNewAPIVideoQueryStatusOf 是视频查询状态归一入口（泛化实现见
// chainNewAPIQueryStatusOf）。
func chainNewAPIVideoQueryStatusOf(record *gatewaymedia.MediaJobRecord, status gatewaymedia.MediaJobStatus) (string, any) {
	return chainNewAPIQueryStatusOf(record, status)
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
	secret    string
	transport gatewayupstream.TransportDeps
	// publicBaseURL 是 New API 任务面回退 content URL 的公共地址前缀
	//（JUHE_AI_GATEWAY_PUBLIC_BASE_URL 归一值；空串 = 未配置，回退相对路径）。
	publicBaseURL string
}

// chainMediaJobAccount 是任务面水合的账户亲和事实（media_jobs 三元组回查）。
// Credentials 保留解密后的凭据对象（M3 gemini：google_oauth 账户的认证头
// 沿 applyGeminiUpstreamAuthHeaders 消费 quota_project_id 等字段，不重复
// 实现认权分支）。
type chainMediaJobAccount struct {
	ID                   string
	OwnerSystemAccountID string
	// ProviderCode 投影位承载 vendor 身份（供应商与上游协议面重构设计 §1.1/§2.1
	// 切片；字段名随任务面既有消费面保留——值源从旧 provider_code 列切到
	// vendor_code 列，适配器解析与出站归一同键消费）。
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	Status                    string
	CapabilityDomain          string
	HealthCheckEndpointMode   string
	CooldownUntil             string
	Deleted                   bool
	BaseURL                   string
	Credential                string
	Credentials               map[string]any
	// ProxyProfileID 是账户行自身绑定的代理档案 id（出站代理消费键；NULL
	// 经 COALESCE 扫成空串 = 直连）。
	ProxyProfileID string
	// 代理池有效目标解析输入（代理池设计 §5.2 授权实例覆盖，C10）：任务面
	// 出站代理统一按有效目标解析——实例行（SourceAccountID 非空）取 source
	// 池 > source 代理 > 实例自身直接代理，实例自身的池绑定不参与；普通账户
	// 用自身池/代理。池绑定出站走当前租约成员 URL（只读解析，不修复租约）。
	SourceAccountID string
	ProxyPoolID     string
	SourceProfileID string
	SourcePoolID    string
}

// hydrateAccount 按 account_id 水合账户凭据（媒体设计 §7 账户亲和）。返回
// (nil, nil) 表示行不存在；Deleted/Status 非 active 由调用方按
// media_job_unreachable 语义处理（不换账户）。
func (m *mediaJobsRuntime) hydrateAccount(ctx context.Context, accountID string) (*chainMediaJobAccount, error) {
	var (
		account              chainMediaJobAccount
		credentialsEncrypted string
		cooldownUntil        dbtime.Nullable
		deletedAt            dbtime.Nullable
		healthCheckPreset    string
	)
	// provider_protocol_profile_id 列已退役（设计 §2.1/§9.11）：档案身份由
	// 健康检查连接的 preset 确定性派生（与 accounts/patch.go 同源映射）。
	// 代理有效目标解析输入（代理池设计 §5.2，C10）：source 账户行一并水合，
	// 实例行出站代理按 source 池 > source 代理 > 实例自身直接代理解析
	// （实例自身 proxy_pool_id 恒不参与；租约键 = source account）。
	row := m.db.QueryRowContext(ctx, `SELECT accounts.id, accounts.system_account_id, COALESCE(accounts.vendor_code, ''),
		COALESCE(conn.connection_preset_code, ''), COALESCE(accounts.protocol_code, ''), COALESCE(accounts.protocol_version, ''),
		accounts.type, accounts.status, COALESCE(accounts.capability_domain, 'chat'), COALESCE(accounts.health_check_endpoint_mode, ''),
		accounts.cooldown_until, COALESCE(accounts.credentials_encrypted, ''), accounts.deleted_at,
		COALESCE(accounts.proxy_profile_id, ''),
		COALESCE(accounts.authorization_instance_source_account_id, ''),
		COALESCE(accounts.proxy_pool_id, ''),
		COALESCE(src.proxy_profile_id, ''),
		COALESCE(src.proxy_pool_id, '')
		FROM `+m.mediaAccountsTable()+` accounts
		LEFT JOIN `+m.mediaConnectionsTable()+` conn ON conn.id = accounts.health_check_connection_id
		LEFT JOIN `+m.mediaAccountsTable()+` src ON src.id = accounts.authorization_instance_source_account_id
			AND src.deleted_at IS NULL
		WHERE accounts.id = ?`, accountID)
	if err := row.Scan(&account.ID, &account.OwnerSystemAccountID, &account.ProviderCode,
		&healthCheckPreset, &account.ProtocolCode, &account.ProtocolVersion,
		&account.Type, &account.Status, &account.CapabilityDomain, &account.HealthCheckEndpointMode,
		&cooldownUntil, &credentialsEncrypted, &deletedAt, &account.ProxyProfileID,
		&account.SourceAccountID, &account.ProxyPoolID, &account.SourceProfileID, &account.SourcePoolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("水合媒体任务账户失败: %w", err)
	}
	if profileID, ok := exactkeyprobe.ConnectionPresetProfileID(healthCheckPreset); ok {
		account.ProviderProtocolProfileID = profileID
	}
	account.CooldownUntil = cooldownUntil.APIString()
	account.Deleted = deletedAt.Valid
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

// mediaAccountsTable 限定 accounts 表名（juhe_business schema）。
func (m *mediaJobsRuntime) mediaAccountsTable() string {
	return "juhe_business.accounts"
}

// mediaConnectionsTable 限定 account_connections 表名（juhe_business schema）。
func (m *mediaJobsRuntime) mediaConnectionsTable() string {
	return "juhe_business.account_connections"
}

// mediaJobProxyUnavailableError 标记任务面出站代理解析失败（档案缺失/停用/
// 凭据不可解密）：渲染层按 503 回带原文（含「代理不可用」与档案 id，不含
// 代理凭据），对应同步 lane 的 proxy_unavailable 语义——绝不静默回落直连。
type mediaJobProxyUnavailableError struct{ message string }

func (e *mediaJobProxyUnavailableError) Error() string { return e.message }

// mediaJobEffectiveProxyBinding 计算任务面出站代理的有效绑定（代理池设计
// §5.2 授权实例覆盖，C10）：实例行（SourceAccountID 非空）取 source 池 >
// source 代理 > 实例自身直接代理，实例自身的池绑定恒不参与；普通账户用自身
// 池/代理；两列同时非空是数据损坏，显式失败不选边。返回
// (kind, targetID, leaseKey)：leaseKey 是池租约所有者（source account 或
// 账户自身）。
func mediaJobEffectiveProxyBinding(account *chainMediaJobAccount) (kind string, targetID string, leaseKey string, err error) {
	ownProfile := strings.TrimSpace(account.ProxyProfileID)
	ownPool := strings.TrimSpace(account.ProxyPoolID)
	if ownProfile != "" && ownPool != "" {
		return "", "", "", &mediaJobProxyUnavailableError{message: "代理不可用（账户同时绑定代理与代理池，数据损坏）"}
	}
	sourceID := strings.TrimSpace(account.SourceAccountID)
	if sourceID != "" {
		sourcePool := strings.TrimSpace(account.SourcePoolID)
		sourceProfile := strings.TrimSpace(account.SourceProfileID)
		if sourcePool != "" && sourceProfile != "" {
			return "", "", "", &mediaJobProxyUnavailableError{message: "代理不可用（来源账户同时绑定代理与代理池，数据损坏）"}
		}
		if sourcePool != "" {
			return "pool", sourcePool, sourceID, nil
		}
		if sourceProfile != "" {
			return "profile", sourceProfile, sourceID, nil
		}
		if ownProfile != "" {
			return "profile", ownProfile, sourceID, nil
		}
		return "none", "", sourceID, nil
	}
	if ownPool != "" {
		return "pool", ownPool, strings.TrimSpace(account.ID), nil
	}
	if ownProfile != "" {
		return "profile", ownProfile, strings.TrimSpace(account.ID), nil
	}
	return "none", "", strings.TrimSpace(account.ID), nil
}

// resolveMediaJobAccountProxyURL 解析任务面出站代理 URL（代理池设计 §5.2/
// §5.3 统一有效目标解析，C10：不再只读本行 proxy_profile_id，消除实例
// source 覆盖差异）：
//
//   - 无绑定 → ("", nil)（直连；空 ProxyURL 走池构建的显式禁环境代理直连
//     transport，未配代理账户的行为与改动前逐字一致）；
//   - 直接代理 → proxy_profiles 档案 → proxy_url（socks5→socks5h 归一与
//     AES-GCM 密码解封沿 chainProxyURLFromRow）；缺失/停用/凭据不可解密 →
//     *mediaJobProxyUnavailableError；
//   - 代理池 → 当前租约成员 URL（mediaJobPoolMemberProxyURL）；租约缺失、
//     池/成员/档案停用或健康 dead → *mediaJobProxyUnavailableError——池绑定
//     账户绝不静默回落直连、不退回旧成员，也不在任务面重分配租约（租约修复
//     只属于 gateway 请求解析路径，§6.6）。
//
// 占位符裸 `?` 与 hydrateAccount 同先例（PG 侧由 sqldialect driver 兜底改写
// $n）；表前缀 PG 方言为 juhe_business.。enabled 列 NullBool 扫描沿
// resolveProxyURLsForProfiles 的 PG boolean 列经 database/sql 可扫（int64
// 0/1 同样接受）。
func (m *mediaJobsRuntime) resolveMediaJobAccountProxyURL(ctx context.Context, account *chainMediaJobAccount) (string, error) {
	if account == nil {
		return "", nil
	}
	kind, targetID, leaseKey, bindErr := mediaJobEffectiveProxyBinding(account)
	if bindErr != nil {
		return "", bindErr
	}
	switch kind {
	case "none":
		return "", nil
	case "profile":
		return m.resolveMediaJobProfileProxyURL(ctx, targetID)
	case "pool":
		return m.resolveMediaJobPoolMemberProxyURL(ctx, leaseKey, targetID)
	default:
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（未知代理绑定）"}
	}
}

// resolveMediaJobProfileProxyURL 是直接代理绑定的出站 URL 解析（原
// resolveMediaJobAccountProxyURL 的 profile 直读臂，行为逐字保持）。
func (m *mediaJobsRuntime) resolveMediaJobProfileProxyURL(ctx context.Context, profileID string) (string, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "", nil
	}
	table := "juhe_business.proxy_profiles"
	var (
		proxyType, host, username, passwordEncrypted sql.NullString
		port                                         sql.NullInt64
		enabled                                      sql.NullBool
	)
	query := `SELECT type, host, port, username, password_encrypted, enabled
		FROM ` + table + ` WHERE id = ? LIMIT 1`
	err := m.db.QueryRowContext(ctx, query, profileID).
		Scan(&proxyType, &host, &port, &username, &passwordEncrypted, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理档案不存在或已停用: " + profileID + "）"}
	}
	if err != nil {
		return "", fmt.Errorf("查询媒体任务账户代理档案失败: %w", err)
	}
	if !enabled.Valid || !enabled.Bool {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理档案不存在或已停用: " + profileID + "）"}
	}
	proxyURL, err := chainProxyURLFromRow(m.secret, proxyType.String, host.String, int(port.Int64), username.String, passwordEncrypted.String)
	if err != nil {
		// 不透传解密错误细节（可能含密文形态信息），归一为不可用语义。
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理凭据不可解密: " + profileID + "）"}
	}
	return proxyURL, nil
}

// resolveMediaJobPoolMemberProxyURL 解析池绑定账户的当前租约成员出站 URL：
// 租约（键 = source account 或账户自身）→ 池（enabled）→ 成员行（enabled 且
// 在池内）→ 代理档案（enabled、健康非 dead 且 streak < 3）。健康读 PG
// pool_health_status/pool_health_failure_streak
// 投影（§5.3 规则 4 回退语义；任务面无 Redis 读取器，Redis dead 投影由
// gateway 请求解析路径与 60s 探测消费）。租约缺失/成员不可用一律
// *mediaJobProxyUnavailableError：池绑定账户不直连、不退回旧成员、不在任务
// 面重分配（§7.5 后台/任务面失败语义）。
func (m *mediaJobsRuntime) resolveMediaJobPoolMemberProxyURL(ctx context.Context, leaseKey, poolID string) (string, error) {
	leaseKey = strings.TrimSpace(leaseKey)
	poolID = strings.TrimSpace(poolID)
	if leaseKey == "" || poolID == "" {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理池绑定无效）"}
	}
	var (
		proxyType, host, username, passwordEncrypted sql.NullString
		port                                         sql.NullInt64
		poolEnabled, memberEnabled, profileEnabled   sql.NullBool
		health                                       sql.NullString
		streak                                       sql.NullInt64
	)
	query := `SELECT pool.enabled, member.enabled, profile.enabled,
		COALESCE(profile.pool_health_status, 'unknown'), profile.pool_health_failure_streak,
		profile.type, profile.host, profile.port, profile.username, profile.password_encrypted
		FROM juhe_business.account_proxy_leases lease
		JOIN juhe_business.proxy_pools pool ON pool.id = lease.pool_id AND pool.id = ?
		JOIN juhe_business.proxy_pool_members member ON member.pool_id = lease.pool_id
			AND member.proxy_profile_id = lease.proxy_profile_id
		JOIN juhe_business.proxy_profiles profile ON profile.id = lease.proxy_profile_id
		WHERE lease.account_id = ?
		LIMIT 1`
	err := m.db.QueryRowContext(ctx, query, poolID, leaseKey).
		Scan(&poolEnabled, &memberEnabled, &profileEnabled, &health, &streak,
			&proxyType, &host, &port, &username, &passwordEncrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理池当前不可用：租约缺失或成员已移除，池: " + poolID + "）"}
	}
	if err != nil {
		return "", fmt.Errorf("查询媒体任务池租约成员失败: %w", err)
	}
	if !poolEnabled.Valid || !poolEnabled.Bool {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理池已停用: " + poolID + "）"}
	}
	if !memberEnabled.Valid || !memberEnabled.Bool || !profileEnabled.Valid || !profileEnabled.Bool {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理池成员已停用，池: " + poolID + "）"}
	}
	if !streak.Valid || streak.Int64 >= 3 || strings.EqualFold(strings.TrimSpace(health.String), "dead") {
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理池成员健康不可用，池: " + poolID + "）"}
	}
	proxyURL, urlErr := chainProxyURLFromRow(m.secret, proxyType.String, host.String, int(port.Int64), username.String, passwordEncrypted.String)
	if urlErr != nil {
		// 不透传解密错误细节（可能含密文形态信息），归一为不可用语义。
		return "", &mediaJobProxyUnavailableError{message: "代理不可用（代理池成员凭据不可解密，池: " + poolID + "）"}
	}
	return proxyURL, nil
}

// markMediaAccountActiveAfterJobSuccess 是视频异步任务成功落定后的账户状态回写
// （媒体账户生命周期定案表·成功回写臂）：视频域账户不参与对话健康检查状态机
// （无后台探针），视频任务轮询推进到 completed 即等价一次成功的上游
// 健康观测——CAS 把滞留在 pending_test/rate_limited/temporary_unavailable 的
// 账户搬回 active；冷却门（cooldown_until 已过期或为空）与候选窗口准入臂
// （chain_accounts.go chainMediaAdmission*）同一谓词。长音频转写不使用该 CAS。
// 投影镜像 jobs
// accounthealth 的 cooldown_success/activation_success 臂（schedulable=1、
// cooldown_until/next_health_check_at/last_error_*/retest 观察列/失败计数
// 清空；不写 last_health_*——媒体域无探针语义）；不加 config/dispatch
// revision 与时间计划门，按定案 CAS 谓词单语句执行。active 行不在 WHERE
// status 集内（已是 active 的成功任务零写入）。失败静默容忍——回写失败不
// 影响已成功的媒体响应，warn 日志留痕供排查；占位符裸 `?` 与 hydrateAccount
// 同先例（PG 侧由 sqldialect driver 兜底改写 $n）。
func (m *mediaJobsRuntime) markMediaAccountActiveAfterJobSuccess(ctx context.Context, accountID string) {
	now := time.Now().UTC().Format(chainTimeLayout)
	exec, err := m.db.ExecContext(ctx, `UPDATE `+m.mediaAccountsTable()+` SET
		status = 'active',
		schedulable = 1,
		cooldown_until = NULL,
		next_health_check_at = NULL,
		last_error_code = NULL,
		last_error_message = NULL,
		last_error_trace_id = NULL,
		cooldown_retest_failure_count = 0,
		cooldown_retest_observation_started_at = NULL,
		cooldown_retest_generation = NULL,
		cooldown_retest_last_at = NULL,
		cooldown_retest_last_status_code = NULL,
		health_check_failure_count = 0,
		health_check_failure_started_at = NULL,
		last_health_check_error_code = NULL,
		last_health_check_error_message = NULL,
		updated_at = ?
		WHERE id = ?
			AND capability_domain = 'video'
			AND health_check_endpoint_mode = 'video_create'
			AND status IN ('pending_test', 'rate_limited', 'temporary_unavailable')
			AND (cooldown_until IS NULL OR cooldown_until <= ?)`, now, accountID, now)
	if err != nil {
		slog.Warn("媒体任务成功后账户状态回写失败（不影响本次媒体响应）",
			"event", "media_job_account_reactivate_failed", "accountId", accountID, "error", err.Error())
		return
	}
	if affected, _ := exec.RowsAffected(); affected > 0 {
		slog.Info("媒体任务成功后账户状态回写为 active",
			"event", "media_job_account_reactivated", "accountId", accountID)
	}
}

// ---------------------------------------------------------------------------
// 创建响应面：受理凭据确立 → 落表 → 统一 job 对象
// ---------------------------------------------------------------------------

// maxMediaJobObjectBytes 限制上游 job 对象读取（创建/轮询响应都是小 JSON；
// 超限即上游协议异常，不落表）。
const maxMediaJobObjectBytes = 1 << 20

// handleMediaJobCreateUpstreamResponse 处理媒体任务创建（POST
// /v1/video/generations 与 POST /v1/tasks/audio）的上游 2xx 响应
// （chain_v1.go handleUpstreamResponse 的
// 媒体短路分支，按 kind 泛化——M3f）：ParsePollResponse 归一创建响应（视频
// 契约 §4.3 创建响应即 video 对象；长音频契约 §10.2 创建响应即任务对象），
// 拿到 job id 后落 media_jobs 并渲染对应族的统一 job 对象。2xx 但无 id /
// 解析失败 = 上游协议违约（受理凭据未确立）→ 502，不换账户（引擎 2xx 分支
// 已选定账户，受理后永不切换）。
func (c *gatewayChain) handleMediaJobCreateUpstreamResponse(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	res *gatewaypreauth.TrackingWriter,
	auditCapture gatewaypreauth.AuditCaptureContext,
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
		message := "读取上游" + jobLabel + "任务响应失败"
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusBadGateway, nil, "", gatewaypreauth.AuditPartGatewayError, "upstream_response", "upstream_error", message)
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	if len(body) > maxMediaJobObjectBytes {
		message := "上游" + jobLabel + "任务响应超过网关处理上限"
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusBadGateway, nil, "", gatewaypreauth.AuditPartGatewayError, "upstream_response", "upstream_error", message)
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	if c.mediaJobs == nil {
		message := "媒体任务面未装配，无法受理" + jobLabel + "任务"
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusServiceUnavailable, nil, "", gatewaypreauth.AuditPartGatewayError, "gateway_response", "service_unavailable", message)
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, message, "service_unavailable")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	// 供应商身份源（供应商与上游协议面重构设计 §1.1/§2.1 切片）：任务面
	// adapter 解析键与创建计划一致，取 VendorCode。
	// 创建响应解析用 v1 形态（H3 v2 受理凭据同为顶层 task_id、无 status）；
	// 轮询面才按冻结模型分派 v2 路径/响应形态。
	adapter := chainMediaJobTaskAdapterFor(kind, dispatched.Account.VendorCode, dispatched.Account.ProtocolCode, "")
	if adapter == nil {
		message := "上游账户缺少" + jobLabel + "任务 adapter，任务受理中断"
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusBadGateway, nil, "", gatewaypreauth.AuditPartGatewayError, "upstream_response", "upstream_error", message)
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	ir, parseErr := adapter.ParsePollResponse(upstream.Status(), upstream.Header, body)
	// 受理失败三态：解析错误（协议违约/上游错误信封）、无受理凭据、归一 failed
	//（火山长转写臂：submit 业务失败在响应头 X-Api-Status-Code 450000xx，
	// ParsePollResponse 归一 failed——受理凭据未确立，不落任务行）。
	if parseErr != nil || ir == nil || ir.UpstreamJobID == "" || ir.Status == gatewaymedia.JobStatusFailed {
		message := "上游" + jobLabel + "任务响应缺少受理凭据（job id）"
		if parseErr != nil {
			message = parseErr.Error()
		} else if ir != nil && ir.Error != nil {
			message = "上游" + jobLabel + "任务受理被上游拒绝: " + ir.Error.Message
		}
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusBadGateway, nil, "", gatewaypreauth.AuditPartGatewayError, "upstream_response", "upstream_error", message)
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	// 产物下载地址 scheme 白名单（SEC-01）：创建响应里的产物定位只接受
	// http/https 绝对地址，javascript:/data:/file: 等其他 scheme（或相对路径、
	// 无 host 形态）按上游协议违约拒绝——受理凭据虽已确立但不落库，避免非法
	// 地址进入 media_jobs 后被管理面/前端当作裸链接渲染。
	if ir != nil && ir.Artifact.ContentURL != "" {
		if urlErr := gatewaymedia.ValidateArtifactURL(ir.Artifact.ContentURL); urlErr != nil {
			message := "上游" + jobLabel + "任务响应包含无效的产物下载地址"
			c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusBadGateway, nil, "", gatewaypreauth.AuditPartGatewayError, "upstream_response", "upstream_error", message)
			c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
			return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
		}
	}
	// 归一参数复读（与 driver 出站构造同源解析，确定性纯函数）。失败属防御
	// 分支（派发侧已成功构造过同源报文）。
	plan, planErr := chainMediaJobCreatePlanOf(chainVideoRequestBodyOf(req), req, dispatched.Account, kind)
	if planErr != nil || plan == nil {
		message := jobLabel + "任务创建请求参数复析失败"
		if planErr != nil {
			message = planErr.Error()
		}
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusInternalServerError, nil, "", gatewaypreauth.AuditPartGatewayError, "gateway_response", "internal_error", message)
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, message, "internal_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	// provider_code / provider_protocol_profile_id 持久列已随 B4 存储迁移批
	// 退役：列名是 provider 维度但语义已是 vendor，且 adapter.Provider() 对
	// gpt 账户持久化的是 legacy token "openai"。读面供应商一律经
	// accounts.vendor_code 水合；创建响应的内存行直接携带派发账户 vendor，
	// 协议档案维度随派发账户视图保留在 usage 记录链（不入任务行）。
	// OwnerSystemAccountID 是归属列（AI 问答免 Key
	// 直调计划 §3.3）：内部通道写注入的会话 owner，外部写 API Key 属主系统
	// 账户；APIKeyID 在内部通道下为空串（免 Key 直调无 Key 身份），保留为
	// 历史审计列。
	record := gatewaymedia.MediaJobRecord{
		ID:                   newChainMediaJobID(kind),
		Kind:                 kind,
		APIKeyID:             context.UsageContext.APIKeyID,
		OwnerSystemAccountID: apiKeyOwnerSystemAccountIDOf(req),
		AccountID:            dispatched.Account.ID,
		VendorCode:           dispatched.Account.VendorCode,
		UpstreamJobID:        ir.UpstreamJobID,
		Status:               ir.Status,
		RequestSnapshot:      chainMediaJobSnapshotScopeOf(plan.snapshotFields(), dispatched.Account, context),
		Artifact:             ir.Artifact,
		ParamsApplied:        plan.paramsApplied(),
		ParamsIgnored:        plan.paramsIgnored(),
		// 创建响应 created_at 来源（New API 契约字段）；落库时间列由仓储
		// 时间源写入，这里与响应同刻取值。
		CreatedAt: time.Now().UTC(),
	}
	if insertErr := c.mediaJobs.repo.Insert(ctx, record); insertErr != nil {
		// 受理凭据已确立但本地行落库失败：任务已在上游存在，客户端拿到错误
		// 后重试会创建新任务；保留原始错误供排查（不静默降级为成功）。
		slog.Error("媒体任务受理后落库失败",
			"event", "media_job_insert_failed", "traceId", context.UsageContext.TraceID,
			"accountId", dispatched.Account.ID, "upstreamJobId", ir.UpstreamJobID,
			"error", insertErr.Error())
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusInternalServerError, nil, "", gatewaypreauth.AuditPartGatewayError, "gateway_response", "internal_error", jobLabel+"任务受理后落库失败")
		c.renderMediaVideoLocalError(res, req, http.StatusInternalServerError, jobLabel+"任务受理后落库失败", "internal_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	// 创建响应按 kind 泛化（New API 面）：视频 object=video、长音频
	// object=task，字段集同为 id/task_id/status/created_at/metadata。
	object := chainNewAPIVideoCreateObjectOf(&record)
	responseBody, _ := json.Marshal(object)
	c.writeMediaVideoJSON(res, http.StatusOK, object)
	c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeSuccess, true, http.StatusOK, responseHeadersOf(upstream), string(responseBody), gatewaypreauth.AuditPartGatewayResponse, "", "", "")
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
	auditCapture gatewaypreauth.AuditCaptureContext,
	upstream *gatewayupstream.GatewayUpstreamResponse,
) gatewayresponse.UpstreamResponseHandlingResult {
	body, readErr := io.ReadAll(io.LimitReader(upstream.Body, maxMediaJobObjectBytes+1))
	_ = upstream.Body.Close()
	if readErr != nil || len(body) > maxMediaJobObjectBytes {
		message := "读取上游视频任务错误响应失败"
		c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeGatewayFailed, false, http.StatusBadGateway, nil, "", gatewaypreauth.AuditPartGatewayError, "upstream_response", "upstream_error", message)
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, message, "upstream_error")
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
	}
	contentType := strings.TrimSpace(upstream.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	res.Header().Set("Content-Type", contentType)
	res.WriteHeader(upstream.Status())
	_, _ = res.Write(body)
	c.finalizeMediaAudit(auditCapture, gatewaypreauth.AuditOutcomeUpstreamFailed, false, upstream.Status(), responseHeadersOf(upstream), string(body), gatewaypreauth.AuditPartGatewayError, "upstream_response", "", "")
	return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
}

// handleAudioSyncUpstreamErrorPassthrough 处理音频同步 lane（/v1/audio/speech
// 与 STT 同步端点族）业务拒绝 4xx 的短路透传（BUG-0326，chain_v1.go
// handleUpstreamResponse 的音频错误分支）：失败派发器对确定性业务拒绝 4xx
// 已短路为 ReturnResponse，错误状态与上游错误体原样透传客户端——不进 chat
// 语义的 JSON 检查管道（响应检查改写会把上游业务拒绝吞成 502
// response_inspection_failed，与视频创建参数短路同款裁决）。诊断/非网关
// 流量的同类透传响应同臂受益（诊断必须观察上游真实终态）。错误体读取失败
// 的本地兜底与视频臂同码族（502 upstream_error）。
func (c *gatewayChain) handleAudioSyncUpstreamErrorPassthrough(
	res *gatewaypreauth.TrackingWriter,
	req *gatewaypreauth.GatewayRequest,
	upstream *gatewayupstream.GatewayUpstreamResponse,
) gatewayresponse.UpstreamResponseHandlingResult {
	body, readErr := io.ReadAll(io.LimitReader(upstream.Body, maxMediaJobObjectBytes+1))
	_ = upstream.Body.Close()
	if readErr != nil || len(body) > maxMediaJobObjectBytes {
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "读取上游语音错误响应失败", "upstream_error")
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
// applyUsageAccountScope 同源（chain_usage.go 超级分组设计 §4.2）：Account scope
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

// mediaJobOwnership 是媒体任务面的归属键（AI 问答免 Key 直调计划 §3.3）：
// 外部 /v1 请求按 API Key 隔离（Key 级，现状语义不变）；内部身份通道请求按
// 系统账户归属（会话 owner）。两者互斥，恰有一个非空。
type mediaJobOwnership struct {
	apiKeyID             string
	ownerSystemAccountID string
}

func (o mediaJobOwnership) empty() bool { return o.apiKeyID == "" && o.ownerSystemAccountID == "" }

// mediaJobOwnershipOf 从网关请求解析任务面归属键：内部身份通道请求（preauth
// 跳过 Key 鉴权，Runtime 为 nil）按注入的会话 owner；外部请求按认证后的
// API Key。
func mediaJobOwnershipOf(req *gatewaypreauth.GatewayRequest) mediaJobOwnership {
	if req == nil {
		return mediaJobOwnership{}
	}
	if req.InternalOwnerSystemAccountID != "" {
		return mediaJobOwnership{ownerSystemAccountID: req.InternalOwnerSystemAccountID}
	}
	if req.Runtime != nil && req.Runtime.APIKey != nil {
		return mediaJobOwnership{apiKeyID: req.Runtime.APIKey.ID}
	}
	return mediaJobOwnership{}
}

// serveMediaJobTaskPlane 处理任务面请求（chain_v1.go 入口短路分支：preauth
// 认证完成之后、派发预检之前；M3f 起按 kind 泛化覆盖 /v1/video/generations*
// 与 /v1/tasks/audio* 两族）。任务面是网关本地应答（不发起上游账户尝试），按
// 2026-10-16 审计收录范围收窄不再落审计；链入口 defer 的 CancelAuditCapture
// 对未 Finalize 的捕获兜底。
func (c *gatewayChain) serveMediaJobTaskPlane(ctx context.Context, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter, traceID string) {
	if c.mediaJobs == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务面未装配", "service_unavailable")
		return
	}
	ownership := mediaJobOwnershipOf(req)
	if ownership.empty() {
		c.renderMediaVideoLocalError(res, req, http.StatusUnauthorized, "缺少网关 API Key 身份", "invalid_api_key")
		return
	}
	shape, jobID, _ := chainMediaJobRequestShapeOf(req.MethodUpper(), req.PathAndQuery())
	// New API 兼容面（2026-11-12 裁决）：视频与长音频两族都不提供列表/取消/
	// 删除（New API 通用任务面无对应面）——GET 集合与 DELETE 一律 404（不保留
	// 别名，也不读本地行）；content 仅作无直链时的回退保留。
	if shape == chainVideoShapeList || shape == chainVideoShapeCancel {
		c.renderMediaVideoLocalError(res, req, http.StatusNotFound, "资源不存在", "not_found")
		return
	}
	switch shape {
	case chainVideoShapePoll:
		c.serveMediaJobPoll(ctx, res, req, traceID, ownership, jobID)
	case chainVideoShapeContent:
		c.serveMediaJobContent(ctx, res, req, ownership, jobID)
	default:
		c.renderMediaVideoLocalError(res, req, http.StatusNotFound, "资源不存在", "not_found")
	}
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
// 归属键按 ownership 分流：内部通道按系统账户（GetByIDAndOwner），外部请求
// 按 API Key（GetByIDAndAPIKey，Key 级隔离不变）。
func (c *gatewayChain) loadMediaVideoJob(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, ownership mediaJobOwnership, jobID string) *gatewaymedia.MediaJobRecord {
	var (
		record *gatewaymedia.MediaJobRecord
		err    error
	)
	if ownership.ownerSystemAccountID != "" {
		record, err = c.mediaJobs.repo.GetByIDAndOwner(ctx, jobID, ownership.ownerSystemAccountID)
	} else {
		record, err = c.mediaJobs.repo.GetByIDAndAPIKey(ctx, jobID, ownership.apiKeyID)
	}
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
	if account == nil || account.Deleted || !chainMediaJobAccountPollEligible(account, record, time.Now().UTC()) {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务账户不可用（已删除或禁用）", "media_job_unreachable")
		return nil
	}
	if chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode, account.ProtocolCode, record.RequestSnapshot.Model) == nil {
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "媒体任务账户供应商暂不支持该种类媒体任务面", "media_job_unreachable")
		return nil
	}
	return account
}

// Recovery-state hydration is limited to asynchronous video jobs. Long audio
// transcription remains active-only, and a future cooldown blocks polling.
func chainMediaJobAccountPollEligible(account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord, now time.Time) bool {
	if account == nil || record == nil {
		return false
	}
	if strings.EqualFold(account.Status, "active") {
		return true
	}
	if record.Kind != gatewaymedia.JobKindVideo ||
		account.CapabilityDomain != "video" || account.HealthCheckEndpointMode != "video_create" {
		return false
	}
	switch account.Status {
	case "pending_test", "rate_limited", "temporary_unavailable":
	default:
		return false
	}
	if strings.TrimSpace(account.CooldownUntil) == "" {
		return true
	}
	cooldown, err := chainRFC3339Millis(account.CooldownUntil)
	if err != nil {
		return false
	}
	return cooldown <= now.UnixMilli()
}

// chainMediaJobLabelOf 按行内 kind 返回任务族中文名（错误消息渲染）。
func chainMediaJobLabelOf(record *gatewaymedia.MediaJobRecord) string {
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		return "长音频"
	}
	return "视频"
}

// mediaJobsPublicBaseURLOf 取 New API 任务面回退 content URL 的公共地址前缀
// （组合根装配；mediaJobs 运行时缺席（组合测试）时返回空串——查询响应回退
// 相对路径，与未配置语义一致）。
func (c *gatewayChain) mediaJobsPublicBaseURLOf() string {
	if c.mediaJobs == nil {
		return ""
	}
	return c.mediaJobs.publicBaseURL
}

// serveMediaJobPoll GET /v1/video/generations/{id} 与 GET /v1/tasks/audio/{id}：
// 终态行
// 直接回本地对象（终态后不再被轮询推进，mediajobir Terminal 语义）；非终态行
// 账户亲和转发（adapter 按行内 kind 解析），响应归一后推进本地状态，
// completed/failed 终态触发 usage spool 回填。
func (c *gatewayChain) serveMediaJobPoll(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, traceID string, ownership mediaJobOwnership, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, ownership, jobID)
	if record == nil {
		return
	}
	if record.Status.Terminal() {
		c.writeMediaVideoJSON(res, http.StatusOK, chainMediaJobObjectOf(record, "", nil, c.mediaJobsPublicBaseURLOf()))
		return
	}
	account := c.loadMediaVideoAccount(ctx, res, req, record)
	if account == nil {
		return
	}
	adapter := chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode, account.ProtocolCode, record.RequestSnapshot.Model)
	method, path, pollHeader, pollBody := adapter.BuildPollRequest(record.UpstreamJobID)
	response, upstreamErr := c.mediaJobRequestUpstream(ctx, account, record.Kind, method, path, pollHeader, pollBody)
	if upstreamErr != nil {
		c.renderMediaVideoTransportError(res, req, upstreamErr)
		return
	}
	body, status := c.readMediaJobUpstreamObject(res, req, response)
	if body == nil {
		return
	}
	var statusErr *gatewaymedia.UpstreamStatusError
	ir, parseErr := adapter.ParsePollResponse(status, response.Header, body)
	if parseErr != nil {
		if errors.As(parseErr, &statusErr) {
			if statusErr.StatusCode == http.StatusNotFound {
				// 契约 §2.6：上游 404 查询且本地非终态 → 保持本地状态直至
				// TTL 过期（不得伪造终态）；回显本地对象。
				c.writeMediaVideoJSON(res, http.StatusOK, chainMediaJobObjectOf(record, "", nil, c.mediaJobsPublicBaseURLOf()))
				return
			}
			c.renderMediaVideoLocalError(res, req, http.StatusBadGateway,
				fmt.Sprintf("上游%s任务轮询失败（状态码 %d）", chainMediaJobLabelOf(record), statusErr.StatusCode), "upstream_error")
			return
		}
		c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游媒体任务响应解析失败: "+parseErr.Error(), "upstream_error")
		return
	}
	// 产物下载地址 scheme 白名单（SEC-01）：轮询响应里的产物定位同样只接受
	// http/https 绝对地址；非法协议按上游协议违约 502 直接返回，不推进本地
	// 状态、不进入 UpdateTerminal（非法 URL 不落库）。
	if ir != nil && ir.Artifact.ContentURL != "" {
		if urlErr := gatewaymedia.ValidateArtifactURL(ir.Artifact.ContentURL); urlErr != nil {
			c.renderMediaVideoLocalError(res, req, http.StatusBadGateway, "上游媒体任务响应包含无效的产物下载地址", "upstream_error")
			return
		}
	}
	if ir.Status.Terminal() {
		// 终态计费（媒体设计 §10：任务终态由轮询响应驱动回填）：completed 按
		// 静态目录价估成本——视频先按次（目录行有按次价 → PerCall × 1），再
		// 按秒计量（上游回报优先、无回报按生效请求 seconds 自算）× 秒价；长
		// 音频 AudioInputSeconds × 秒价（契约 §2.8 网关自算口径 + 按次计费），
		// 失败/无计量依据（usage_missing）不虚计；成本随终态一次落行。
		costUsd := chainMediaJobTerminalCostUsd(account, record, ir)
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
			c.enqueueMediaJobTerminalUsage(ctx, traceID, apiKeyOwnerSystemAccountIDOf(req), req.SuppressUsage, record, account, ir, costUsd)
			// 成功回写臂（媒体账户生命周期定案表）：视频任务 completed 是视频
			// 域账户的真实上游成功观测，CAS 把滞留 pending_test/冷却态搬回
			// active（失败静默容忍，方法内留日志）。挂在 updated==1 之后：
			// 并发重复终态只回写一次。
			if record.Kind == gatewaymedia.JobKindVideo && ir.Status == gatewaymedia.JobStatusCompleted && c.mediaJobs != nil {
				c.mediaJobs.markMediaAccountActiveAfterJobSuccess(ctx, record.AccountID)
			}
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
	c.writeMediaVideoJSON(res, http.StatusOK, chainMediaJobObjectOf(record, ir.Status, ir.Progress, c.mediaJobsPublicBaseURLOf()))
}

// serveMediaJobContent GET /v1/video/generations/{id}/content 与 GET
// /v1/tasks/audio/{id}/content：completed 才可下载；上游产物 http 流式转发（io.Copy 零缓冲落盘，
// content-type 透传——媒体设计 §8.1.2 纯流式透传代理；长音频产物是转写
// 结果 JSON 文件，契约 §10.2）。下载定位按 adapter 声明二分：openai 族由
// base_url + job id 构造（BuildContentRequest，认证头携带）；Veo/qwen 等
// （ContentFromArtifact）直连轮询冻结的绝对 URL（无账户凭据；URL 缺失即
// 产物从未产生或已被清理 → media_artifact_expired）。上游 404/410 →
// media_artifact_expired（产物可取回性由上游时效决定，网关不补救，§8.1.5）。
func (c *gatewayChain) serveMediaJobContent(ctx context.Context, res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, ownership mediaJobOwnership, jobID string) {
	record := c.loadMediaVideoJob(ctx, res, req, ownership, jobID)
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
	adapter := chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode, account.ProtocolCode, record.RequestSnapshot.Model)
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
		response, upstreamErr = c.mediaJobArtifactRequestUpstream(ctx, account, artifactURL)
	} else {
		method, path, contentHeader, contentBody := adapter.BuildContentRequest(record.UpstreamJobID)
		response, upstreamErr = c.mediaJobRequestUpstream(ctx, account, record.Kind, method, path, contentHeader, contentBody)
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

// ---------------------------------------------------------------------------
// 任务面上游直连（复用派发引擎同源传输面）
// ---------------------------------------------------------------------------

// chainQwenChatPathPrefix 是 qwen 对话出站的厂商路径前缀：DashScope 官方
// OpenAI 兼容模式服务根 /compatible-mode/v1（契约 §10；与连接器 registry
// preset qwen_dashscope 的 AllowedBasePathSuffixes 声明同源，见
// shared/contracts/connector_registry.go——registry 未提供运行时读取器，
// 以带注释常量实现）。与媒体任务面的 /api/v1 全路径 opPath 不同域，不可
// 混用：该前缀只拼在 chat 通用 opPath 之前（R6）。
const chainQwenChatPathPrefix = "/compatible-mode/v1"

// chainIsChatCompletionsPathAndQuery 报告路径是否 chat completions 面（与
// requestMappingSourceFamilyOf 的 /chat/completions 判定同词表；query 剥离
// 后大小写不敏感匹配，模型映射与 responses→chat 桥改写后的路径一并命中）。
func chainIsChatCompletionsPathAndQuery(pathAndQuery string) bool {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	return strings.Contains(strings.ToLower(path), "/chat/completions")
}

// chainQwenChatUpstreamURL 归一 qwen 对话（chat completions）的上游 URL：
// 客户端版本剥离后的 chat opPath 恒定拼 chainQwenChatPathPrefix 前缀
// （R6，服务根由前缀常量承载）后与 base_url 直拼——官方 origin 形态
// base（https://dashscope.aliyuncs.com）+ /compatible-mode/v1/chat/
// completions。不再有 base 侧 strict 插根/去重分支（v2 契约：版本段只在
// opPath 一侧，base_url 不参与版本判定）。
func chainQwenChatUpstreamURL(baseURL, pathAndQuery string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("qwen 账户 base_url 无效: %q", baseURL)
	}
	path, query := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	suffix := chainStripGatewayVersionPrefix(path)
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + chainQwenChatPathPrefix + suffix
	return parsed.String() + query, nil
}

// chainMinimaxV2UpstreamURL 构造 minimax H3 v2 出站地址：账户 base_url 为
// Hailuo v1 契约的 /v1 版本根，H3 v2 操作路径（/v2/...）必须落在 host 根
// ——剥离 base 末尾 /v1 后直拼；base 已是 host 根时行为等同通用直拼。
func chainMinimaxV2UpstreamURL(baseURL, opPath string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/v1")
	return upstreamenvelope.JoinUpstreamURL(base, opPath)
}

// mediaJobRequestUpstream 账户亲和直连上游（不走派发循环；kind 区分任务族
// 的 adapter 注册表——M3f 起 video/audio_transcription 两族各自解析注册键）：
// URL 由账户 base_url + adapter 出站 opPath 直拼——gemini 协议走 gatewaygemini.
// BuildUpstreamURL（入站剥 /v1beta 后直拼，版本根由 base_url 承载，M1 speech
// 创建链同先例），火山长转写 host 恒 openspeech 语音服务域（chainVolcengine
// ASRUpstreamURL 特例，同 TTS 先例），其余全部走共享 upstreamenvelope.
// JoinUpstreamURL 的 v2 直拼（R1：base_url 即版本根，opPath 不含版本段——
// glm /api/paas/v4 与
// volcengine /api/v3 由 base_url 版本根承载，qwen /api/v1 域 opPath 自带
// 全路径双根特例；openai/xai/minimax 族的 openai 归一同样直拼）；认证头按
// 协议构造：gemini 沿
// applyGeminiUpstreamAuthHeaders（api_key → X-Goog-Api-Key、google_oauth →
// Bearer + 可选 x-goog-user-project，与主链认权分支同一实现，不重复实现），
// 其余维持最小 Bearer 集（任务面身份=网关，不透传客户端头——沿
// buildGeminiCodeAssistRequestParts 先例；glm/volcengine/qwen/xai 账户即 Bearer
// API Key，契约 §7.1/§9.1/§10.1/§10.2）；传输复用引擎 TransportDeps
// （URL 安全策略 / 全局并发槽 / keep-alive 池）。qwen 的 X-DashScope-Async
// 头只在万相视频创建面需要（DashScope 异步任务约定；paraformer 长转写天然
// 异步不需要），任务面轮询仅需 Authorization，不在此注入。出站代理消费账户
// 绑定档案（resolveMediaJobAccountProxyURL：未配代理直连行为不变，缺失/停用
// 失败出站不回落直连）。
func (c *gatewayChain) mediaJobRequestUpstream(ctx context.Context, account *chainMediaJobAccount, kind gatewaymedia.MediaJobKind, method, path string, extraHeader http.Header, body []byte) (*gatewayupstream.GatewayUpstreamResponse, error) {
	adapterKey := ""
	if kind == gatewaymedia.JobKindAudioTranscription {
		adapterKey = chainAudioJobAdapterKeyOfProvider(account.ProviderCode)
	} else {
		adapterKey = chainVideoAdapterKeyForIdentity(account.ProviderCode)
	}
	if adapterKey == "" {
		return nil, fmt.Errorf("账户 %s 的供应商 %s 无该种类媒体任务 adapter", account.ID, account.ProviderCode)
	}
	isVolcengineASR := kind == gatewaymedia.JobKindAudioTranscription && adapterKey == "volcengine"
	var upstreamURL string
	if chainIsGeminiProtocolProfile(account.ProtocolCode, account.ProtocolVersion) {
		built, err := gatewaygemini.BuildUpstreamURL(account.BaseURL, path, false)
		if err != nil {
			return nil, err
		}
		upstreamURL = built
	} else if isVolcengineASR {
		// 火山长转写（契约 §9.2）：host 恒 openspeech.bytedance.com 语音服务域
		// （与账户 ark base_url 无关的 URL 构造特例，同 TTS 先例）。
		upstreamURL = chainVolcengineASRUpstreamURL(path)
	} else if adapterKey == "minimax" && strings.HasPrefix(path, "/v2/") {
		// minimax H3 v2 版本根特例（契约 §8.1）：账户 base_url 承载 Hailuo 的
		// /v1 版本根，而 H3 的 /v2 操作路径必须落在 host 根；仅该前缀剥离
		// base 末尾 /v1，其余路径仍走通用直拼。
		built, err := chainMinimaxV2UpstreamURL(account.BaseURL, path)
		if err != nil {
			return nil, err
		}
		upstreamURL = built
	} else {
		// v2 直拼：adapter opPath 不含版本段（qwen /api/v1 域自带全路径），
		// glm/volcengine 服务根由 base_url 版本根承载；不再有厂商服务根去重
		// 归一分支。
		built, err := upstreamenvelope.JoinUpstreamURL(account.BaseURL, path)
		if err != nil {
			return nil, err
		}
		upstreamURL = built
	}
	headers := http.Header{}
	if chainIsGeminiProtocolProfile(account.ProtocolCode, account.ProtocolVersion) {
		applyGeminiUpstreamAuthHeaders(headers, gatewaydispatch.AccountCandidate{
			Type:        account.Type,
			Credentials: account.Credentials,
		}, account.Credential)
	} else if isVolcengineASR {
		// 火山长转写鉴权：X-Api-Key 单头（凭据 speech_token 单值，与 TTS 同
		// 一把新版语音控制台 API Key）；缺失即该账户无长转写能力，显式失败
		// 不静默回退（同 TTS 缺凭据语义与稳定错误码）。adapter 专用头（资源
		// ID/任务 ID/序列）经 extraHeader 合并。
		speechToken, _ := account.Credentials["speech_token"].(string)
		speechToken = strings.TrimSpace(speechToken)
		if speechToken == "" {
			return nil, gatewaypreauth.NewGatewayRequestValidationError(
				fmt.Sprintf("账户 %s 缺少火山语音凭据（speech_token = 新版语音控制台 API Key），不能承接长音频转写请求", account.ID),
				gatewaypreauth.WithValidationErrorCode(chainSpeechCredentialsMissingErrorCode),
				gatewaypreauth.WithValidationErrorStatusCode(http.StatusBadRequest),
			)
		}
		headers.Set("X-Api-Key", speechToken)
	} else if account.Credential != "" {
		headers.Set("Authorization", "Bearer "+account.Credential)
	}
	for name, values := range extraHeader {
		for _, value := range values {
			headers.Set(name, value)
		}
	}
	headers.Set("Accept", "application/json")
	// 出站代理与同步 lane 同源：统一有效目标解析（代理池设计 §5.2/§5.3，
	// C10——实例 source 覆盖 + 池绑定走当前租约成员）；未配代理 → ProxyURL
	// 零值直连（行为不变）；直接代理缺失/停用、池不可用/租约缺失 → 失败出站
	// （不允许回落直连），错误沿调用方 renderMediaVideoTransportError 收敛为
	// 503 proxy_unavailable 语义。
	proxyURL, proxyErr := c.mediaJobs.resolveMediaJobAccountProxyURL(ctx, account)
	if proxyErr != nil {
		return nil, proxyErr
	}
	return gatewayupstream.RequestUpstream(ctx, upstreamURL, gatewayupstream.UpstreamRequestOptions{
		Method:   method,
		Header:   headers,
		Body:     body,
		ProxyURL: proxyURL,
		Signal:   ctx,
	}, c.mediaJobs.transport)
}

// mediaJobArtifactRequestUpstream 直连 completed 任务的产物 URL（M3 gemini：
// Veo 的 GCS 签名 URL，凭据内嵌于 URL，契约 §5.2——不携带任何账户认证头，
// 带 Authorization 反而会被 GCS 拒绝）；出站代理消费任务账户有效代理目标
// （与轮询/取消同一 resolveMediaJobAccountProxyURL 语义：未配代理直连、
// 直接代理缺失/停用与池不可用失败出站不回落）；传输复用引擎 TransportDeps
// （URL 安全策略与任务面直连同源）。不设 client 总超时——产物是流式下载，
// 总超时会截断长下载。
func (c *gatewayChain) mediaJobArtifactRequestUpstream(ctx context.Context, account *chainMediaJobAccount, artifactURL string) (*gatewayupstream.GatewayUpstreamResponse, error) {
	proxyURL, proxyErr := c.mediaJobs.resolveMediaJobAccountProxyURL(ctx, account)
	if proxyErr != nil {
		return nil, proxyErr
	}
	return gatewayupstream.RequestUpstream(ctx, artifactURL, gatewayupstream.UpstreamRequestOptions{
		Method:   http.MethodGet,
		ProxyURL: proxyURL,
		Signal:   ctx,
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

// chainMediaJobVideoPerCallCostUsd 计算视频任务的按次成本（契约 §2.8 按次
// 计费，2026-10-05 用户裁决：无法返回 usage 的按次上游按官网计费规则、按
// 输入计）：目录行携带 VideoOutputCostPerCall（按次上游单槽位，与秒价互斥）
// 时一次任务 = 一次调用，成本 = PerCall × 1，不依赖秒计量。目录未命中、
// 行无按次价（秒价行经本入口试算只产出 0——无 PerCall 槽位不成行项）返回
// 0，调用方回退秒价链。计费键沿 chain_usage costModel 先例（请求模型直达）。
// 计价目录查找键取水合账户 vendor（B4①：media_jobs 不再持久化 provider_code，
// 与任务面 adapter 解析同键；sync 链 chain_usage.go vendor 同源）。
func chainMediaJobVideoPerCallCostUsd(account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord) float64 {
	vendorCode := ""
	if account != nil {
		vendorCode = account.ProviderCode
	}
	one := 1.0
	cost := pricing.EstimateProviderCostUsd(pricing.CostInput{
		ProviderCode:          vendorCode,
		Model:                 record.RequestSnapshot.Model,
		VideoCalls:            &one,
		VideoCallSeconds:      record.RequestSnapshot.BillingSeconds,
		OutputVideoResolution: record.RequestSnapshot.BillingResolution,
	})
	if cost == nil || *cost <= 0 {
		return 0
	}
	return *cost
}

// chainMediaJobVideoUsageSecondsOf 解析视频任务的秒计量依据（契约 §2.8 网关
// 自算口径；2026-10-05 请求参数自算批 + 2026-11-12 厂商默认时长批）：上游
// 轮询回报优先（IR OutputVideoSeconds）；无回报时取创建时冻结的计费秒数
// 基源（RequestSnapshot.BillingSeconds）——它要么是生效的请求 seconds，要么
// 是厂商默认输出时长（Veo 8s、minimax 6s、volcengine 5s；契约 §2.8.2）。
// ignored 的 seconds 不构成计量基源（§2.4 规则 1/3：不支持时该模型用厂商
// 默认时长、用户值不生效），因此不再回退用户请求值。都没有返回 nil，调用
// 方落 usage_missing，不猜测。
func chainMediaJobVideoUsageSecondsOf(record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) *float64 {
	if ir.Usage.OutputVideoSeconds != nil {
		return ir.Usage.OutputVideoSeconds
	}
	if record.RequestSnapshot.BillingSeconds != nil && *record.RequestSnapshot.BillingSeconds > 0 {
		return record.RequestSnapshot.BillingSeconds
	}
	// 存量行兼容：BillingSeconds 列随 2026-11-12 批新增，上线窗口内已在途的
	// 任务只有旧快照——seconds 已生效（ParamsApplied 含 seconds）时仍按请求
	// 值结算，避免这批任务落到 usage_missing。
	if record.RequestSnapshot.Seconds != nil && *record.RequestSnapshot.Seconds > 0 &&
		slices.Contains(record.ParamsApplied, "seconds") {
		return record.RequestSnapshot.Seconds
	}
	return nil
}

// chainMediaJobTerminalCostUsd 计算终态成本（媒体设计 §10；契约 §2.8 网关
// 自算口径，M3f 起按 kind 分维度；2026-10-05 按次计费 + 请求参数自算批；
// 2026-11-12 厂商默认时长批）。completed 才计费，视频行按三级顺序裁决：
//  1. 按次计费（目录行有 VideoOutputCostPerCall）：成本 = PerCall × 1（一次
//     任务 = 一次调用），不依赖秒计量（Hailuo 等按条计费上游，契约 §2.8
//     按次计费）；
//  2. 秒计量计费：上游轮询回报优先；无回报时取创建时冻结的计费秒数基源
//     （生效请求 seconds，或厂商默认输出时长——Veo 8s/minimax 6s/
//     volcengine 5s，契约 §2.8.2）× 目录秒价；
//  3. 两者都缺 → 0（usage_missing，不猜测）。
//
// 不做跨维度换算、不解析产物字节：每家用自己计费维度对应的目录价，网关只
// 把该维度的最终美元成本落账（用户裁决 2026-11-12）。
//
// 长转写行维持原口径：AudioInputSeconds × 秒价（paraformer 目录未落 USD 秒
// 价 → 估不出返回 0，计量照落成本不虚计，契约 §10.2）。failed/目录未命中
// 返回 0——失败任务不虚计、无计量不猜测。
func chainMediaJobTerminalCostUsd(account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) float64 {
	if ir.Status != gatewaymedia.JobStatusCompleted {
		return 0
	}
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		if ir.Usage.AudioInputSeconds == nil || *ir.Usage.AudioInputSeconds <= 0 {
			return 0
		}
		cost := pricing.EstimateProviderCostUsd(pricing.CostInput{
			ProviderCode:      accountProviderCodeForPricing(account),
			Model:             record.RequestSnapshot.Model,
			AudioInputSeconds: ir.Usage.AudioInputSeconds,
		})
		if cost == nil {
			return 0
		}
		return *cost
	}
	// 按次计费先行（契约 §2.8 按次上游）：按次行不走秒价链。
	if cost := chainMediaJobVideoPerCallCostUsd(account, record); cost > 0 {
		return cost
	}
	seconds := chainMediaJobVideoUsageSecondsOf(record, ir)
	if seconds == nil || *seconds <= 0 {
		return 0
	}
	cost := pricing.EstimateProviderCostUsd(pricing.CostInput{
		ProviderCode:          accountProviderCodeForPricing(account),
		Model:                 record.RequestSnapshot.Model,
		OutputVideoSeconds:    seconds,
		OutputVideoResolution: record.RequestSnapshot.BillingResolution,
	})
	if cost == nil {
		return 0
	}
	return *cost
}

// accountProviderCodeForPricing 取水合账户的 vendor 计价键（chainMediaJobAccount.ProviderCode
// 投影位值源已切 accounts.vendor_code；nil 防御仅测试直呼路径需要）。
func accountProviderCodeForPricing(account *chainMediaJobAccount) string {
	if account == nil {
		return ""
	}
	return account.ProviderCode
}

// enqueueMediaJobTerminalUsage 经既有 usage spool 链写终态 usage_records
// （chain finalizationUsage 即 spooledUsageRecorder：有界异步缓冲 + spool
// 溢出落盘，与同步请求终态记录同一投递面，链上下文零额外依赖；M3f 起按
// kind 泛化——endpoint 与计量维度随任务族）。
// completed 携带任务族计量（视频 OutputVideoSeconds、长音频
// AudioInputSeconds，IR 从轮询响应抽取，契约 §2.8/§10.2；2026-10-05 起
// 视频行上游无回报时按创建请求 seconds 自算——usage_source=request 语义，
// seconds 须生效，ignored 不构成计量基源）。按次计费行（目录行有
// VideoOutputCostPerCall）不落秒计量（按次上游无秒计量概念）、成本随
// CostUsd 落行。completed 但无任何计量依据（无回报且无生效请求参数）→
// usage_missing 标记不猜测；failed 不虚计（success=false 无计量，行项为
// 空）。cancelled/expired 是本地终态，不产生 usage 行（清理不产生计费；
// 取消按上游实际计量为准）。CostUsd 在 enqueue 前已可算（计量/模型/供应商
// 都在行上），直接带值落 usage 行——不存在"已 enqueue 留空回追"的窗口
// （usage 行只在终态轮询时入队一次）；0 成本（usage_missing/未命中）不设
// CostUsd，与完成尝试记录"估算不出保持 NULL"的既有语义一致。记账 scope
// 五元组从行内 request_snapshot 投影（创建时冻结，
// chainMediaJobSnapshotScopeOf）。suppressUsage 是内部通道请求（AI 问答免
// Key 直调）的抑制标记：置位时记录仍入队，由持久化唯一闸口
// （gatewayusage.FinalizationDispatch.DispatchUsageRecord 入口，覆盖正常
// 入队与溢出 spool 两臂）丢弃——不落 usage_records、不写 spool。
func (c *gatewayChain) enqueueMediaJobTerminalUsage(ctx context.Context, traceID, apiKeyOwnerSystemAccountID string, suppressUsage bool, record *gatewaymedia.MediaJobRecord, account *chainMediaJobAccount, ir *gatewaymedia.MediaJobIR, costUsd float64) {
	if c.finalizationUsage == nil || (ir.Status != gatewaymedia.JobStatusCompleted && ir.Status != gatewaymedia.JobStatusFailed) {
		return
	}
	// usage 行 endpoint 记录对外公开端点：视频面自 2026-11-12 起是 New API
	// 兼容路径 /v1/video/generations；长音频同批迁到 New API 通用任务面
	// /v1/tasks/audio（旧路径均已 404）。
	endpoint := "/v1/video/generations"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		endpoint = "/v1/tasks/audio"
	}
	statusCode := http.StatusOK
	input := gatewayusage.UsageRecordInput{
		TraceID:       traceID,
		TrafficSource: gatewayTrafficSource,
		// 身份维度对齐既有 usage 记录约定（chain_usage.go RecordCompleted
		// UpstreamAttempt：SystemAccountID 取 usage context 即 API Key 属主，
		// AccountOwnerSystemAccountID 取账户行属主）。
		SystemAccountID: apiKeyOwnerSystemAccountID,
		APIKeyID:        record.APIKeyID,
		AccountID:       record.AccountID,
		// 内部通道抑制标记（AI 问答免 Key 直调）：持久化唯一闸口据此丢弃。
		SuppressUsage:               suppressUsage,
		AccountOwnerSystemAccountID: account.OwnerSystemAccountID,
		Endpoint:                    endpoint,
		// vendor 身份：账户行 vendor（B4① 拆除 media_jobs.provider_code 后
		// usage 行供应商与 sync 链 chain_usage.go 同源——账号视图 vendor 值）。
		// 协议档案 id 同步切到水合账户视图（B4 两表退役批：任务行不再冻结
		// provider 维度，usage 行身份维度与 sync 链同源）。
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		Model:                     record.RequestSnapshot.Model,
		UpstreamModel:             record.RequestSnapshot.Model,
		StatusCode:                &statusCode,
		Success:                   ir.Status == gatewaymedia.JobStatusCompleted,
		DurationMs:                chainMediaJobDurationMsOf(record),
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
		} else if chainMediaJobVideoPerCallCostUsd(account, record) > 0 {
			// 按次计费行（契约 §2.8 按次上游，2026-10-05 按次计费批）：一次
			// 任务 = 一次调用，成本已随 CostUsd 落行；usage 行不落秒计量
			//（按次上游无秒计量概念），也不打 usage_missing——计量与计费依据
			// 都是目录按次价 × 任务粒度。
		} else if seconds := chainMediaJobVideoUsageSecondsOf(record, ir); seconds != nil {
			// 上游回报优先；无回报时为创建请求参数自算（usage_source=request
			// 语义，见 chainMediaJobVideoUsageSecondsOf）。usage_records/IR 无
			// 计量来源字段，来源以注释与链级测试钉住，不加 schema 列。
			input.OutputVideoSeconds = seconds
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

// apiKeyOwnerSystemAccountIDOf 取请求归属系统账户（media_jobs owner 列写入源
// 与 usage 身份维度来源，AI 问答免 Key 直调计划 §3.3）：内部身份通道请求取
// 注入的会话 owner（Runtime 为 nil），外部请求取认证 API Key 的属主系统账户
// （usage 抑制后内部通道不再入队 usage，该回落路径主要服务外部请求）。
func apiKeyOwnerSystemAccountIDOf(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return ""
	}
	if req.InternalOwnerSystemAccountID != "" {
		return req.InternalOwnerSystemAccountID
	}
	if req.Runtime == nil || req.Runtime.APIKey == nil {
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
// 可重试语义；受理后故障只报错不换账户）。代理解析失败（档案缺失/停用/凭据
// 不可解密）按原文 503 proxy_unavailable 渲染（与同步 lane 失败语义同码），
// 不回落直连；其余维持既有通用文案。
func (c *gatewayChain) renderMediaVideoTransportError(res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, err error) {
	var proxyErr *mediaJobProxyUnavailableError
	if errors.As(err, &proxyErr) {
		slog.Default().Warn("媒体任务面代理不可用",
			"event", "media_job_task_proxy_unavailable",
			"method", req.MethodUpper(), "path", req.PathAndQuery(), "error", proxyErr.Error())
		c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, proxyErr.Error(), "proxy_unavailable")
		return
	}
	// 任务面直连的上游传输失败此前只回泛化 503、服务端零日志（2026-11-06
	// 复审 N2）：轮询/取消/下载的 dial/timeout/TLS/代理故障无法定位账户与
	// 成因。客户端文案保持不变，服务端补一条 Warn。
	slog.Default().Warn("媒体任务面上游传输失败",
		"event", "media_job_task_upstream_transport_failed",
		"method", req.MethodUpper(), "path", req.PathAndQuery(), "error", err.Error())
	c.renderMediaVideoLocalError(res, req, http.StatusServiceUnavailable, "上游暂时不可用，请重试", "upstream_unavailable")
}

// writeMediaVideoJSON 渲染任务面成功 JSON。
func (c *gatewayChain) writeMediaVideoJSON(res *gatewaypreauth.TrackingWriter, statusCode int, payload any) {
	kernel.WriteJSON(res, statusCode, payload)
}

// finalizeMediaAudit 收口媒体创建短路的审计终态。媒体创建不进入通用响应
// 管道（handleUpstreamResponse 的 parser/finalize 链），必须在每个早退分支
// 自行 finalize，否则该请求只有阶段记录、没有终态审计（DR-29）。
func (c *gatewayChain) finalizeMediaAudit(
	capture gatewaypreauth.AuditCaptureContext,
	outcome string,
	success bool,
	statusCode int,
	headers map[string]string,
	body string,
	partType string,
	errorPhase string,
	errorCode string,
	errorMessage string,
) {
	if capture == nil {
		return
	}
	input := gatewaypreauth.AuditFinalizeInput{
		Outcome:          outcome,
		Success:          success,
		StatusCode:       statusCode,
		ResponseBody:     body,
		ResponsePartType: partType,
		ErrorPhase:       errorPhase,
		ErrorCode:        errorCode,
		ErrorMessage:     errorMessage,
	}
	if len(headers) > 0 {
		input.ResponseHeaders = make(map[string]any, len(headers))
		for name, value := range headers {
			input.ResponseHeaders[name] = value
		}
	}
	capture.Finalize(input)
}
