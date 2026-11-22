package playground

// 演练场路由（docs/functions/演练场设计.md §3）：经 cmd/juhe-ai-gateway 组合
// 根挂载 GET/POST /__aisys__/api/playground/...（kernel Register，自动获得
// kernel 的 body limit / IP rate limit / no-store 包裹）；每条路由套
// RequireSession(true)。端点族：
//
//	GET  /key-models              所选 API Key 的模型候选（六页共用；query
//	                              apiKeyId）
//	POST /chat/stream             一次性对话 SSE
//	POST /images/references       参考图上传（§3.4.1，v1.2）
//	POST /images/jobs             创建生成任务（§3.4.2，v1.2 异步）
//	GET  /images/jobs             生成历史列表（§3.4.3）
//	GET  /images/jobs/{id}        任务详情轮询（§3.4.4）
//	POST /videos                  异步视频创建（任务引用落暂存）
//	GET  /videos/{jobId}          视频任务轮询
//	GET  /videos/{jobId}/content  视频产物字节流（jobId 代理，不落盘）
//	POST /speech                  同步语音合成（产物落暂存 §3.8）
//	POST /embeddings              同步向量生成（无产物落盘 §3.9）
//	POST /rerank                  同步重排（无产物落盘 §3.10）
//	GET  /artifacts               当前用户暂存清单（恢复端点 §3.8，v1.2 起无
//	                              images 字段）
//	GET  /artifacts/{id}/content  暂存产物字节流（视频不走此端点）
//
// v1.0 的 POST /images（同步生图）已随 §8/v1.2 退役。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// playgroundJSONBodyLimit 兜底请求体上限（kernel body limit 之外的领域面校验，
// chat 面 chatSystemAPIJSONBodyLimit 同模式）。
const playgroundJSONBodyLimit = 64 * 1024 * 1024

// playgroundRoutes 是演练场路由运行时（artifacts 是产物暂存后端、imageJobs
// 是图像任务存储，生产注入 PG 实现；imageRunner 是图像任务执行器，由 Register
// 自动装配——功能默认生效不设开关）。
type playgroundRoutes struct {
	deps        *Deps
	artifacts   ArtifactStore
	imageJobs   ImageJobStore
	imageRunner *ImageJobRunner
}

// Register 挂载演练场路由族到 kernel（pattern 键 "METHOD /prefix/path"，与
// chat Deps.Register 同款挂载风格）。ImageJobs 已注入且未显式装配 ImageRunner
// 时在此构造执行器并回写 Deps（cmd 层随后可取它做启动恢复）。
func (d *Deps) Register(k Registerer, prefix string) {
	if prefix == "" {
		prefix = "/__aisys__/api/playground"
	}
	rt := &playgroundRoutes{deps: d, artifacts: d.Artifacts, imageJobs: d.ImageJobs, imageRunner: d.ImageRunner}
	if rt.imageJobs != nil && rt.imageRunner == nil {
		rt.imageRunner = NewImageJobRunner(d, rt.imageJobs, d.ImageCost)
		d.ImageRunner = rt.imageRunner
	}
	// 参考图上传的 body limit 覆盖（§3.4.1 B1 修复）：10MB 原图经 base64 膨胀
	// 4/3 ≈ 13.34MB，JSON 包裹后仍超 kernel 默认 256KiB，故按领域读取上限
	// referenceImageBodyMaxBytes（20MB，膨胀系数 1.5 倍余量）登记路由级覆盖；
	// 领域校验 10MB（decodeReferenceImage）仍是真实上限。kernel 不支持该可选
	// 能力（测试直挂 mux）时跳过，行为同直挂场景。
	if registrar, ok := k.(bodyLimitOverrideRegistrar); ok {
		registrar.RegisterBodyLimitOverride(http.MethodPost, prefix+"/images/references", referenceImageBodyMaxBytes)
	}
	mount := func(method, pattern string, handler http.HandlerFunc) {
		k.Register(method+" "+prefix+pattern, rt.wrap(handler))
	}
	mount("GET", "/key-models", rt.keyModelsHandler)
	mount("POST", "/chat/stream", rt.chatStreamHandler)
	mount("POST", "/images/references", rt.imageReferencesHandler)
	mount("POST", "/images/jobs", rt.imageJobsCreateHandler)
	mount("GET", "/images/jobs", rt.imageJobsListHandler)
	mount("GET", "/images/jobs/{id}", rt.imageJobGetHandler)
	mount("POST", "/videos", rt.videosCreateHandler)
	mount("GET", "/videos/{jobId}", rt.videosPollHandler)
	mount("GET", "/videos/{jobId}/content", rt.videosContentHandler)
	mount("POST", "/speech", rt.speechHandler)
	mount("POST", "/embeddings", rt.embeddingsHandler)
	mount("POST", "/rerank", rt.rerankHandler)
	mount("GET", "/artifacts", rt.artifactsHandler)
	mount("GET", "/artifacts/{id}/content", rt.artifactContentHandler)
}

// wrap 套登录态中间件（RequireSession(true)，chat 面同款；nil 时原样透传——
// 组合测试直挂场景）。
func (rt *playgroundRoutes) wrap(next http.HandlerFunc) http.Handler {
	handler := http.HandlerFunc(next)
	if rt.deps.RequireSession != nil {
		return rt.deps.RequireSession(handler)
	}
	return handler
}

// keyModelsHandler 服务 GET /key-models?apiKeyId=（设计 §3.2 真实 Key 化修订：
// 所选 Key 的分组模型并集 ∪ 目录分类，六页共用；Key 不存在/非本人 404）。
func (rt *playgroundRoutes) keyModelsHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if rt.deps.KeyModels == nil {
		writeHTTPError(w, &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "模型候选暂不可用，请稍后重试"})
		return
	}
	apiKeyID := strings.TrimSpace(r.URL.Query().Get("apiKeyId"))
	if apiKeyID == "" {
		writeHTTPError(w, invalidRequest("apiKeyId 必填"))
		return
	}
	rows, httpErr := rt.deps.KeyModels.ListKeyModels(r.Context(), scope.ViewerID, apiKeyID)
	if httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
	writeOKPayload(w, keyModelsResponse{Models: rows})
}

// keyModelsResponse 是 GET /key-models 响应（设计 §3.2：行形状
// {modelId, kind, providerCode, imageEstimate?}）。
type keyModelsResponse struct {
	Models []KeyModelRow `json:"models"`
}

// resolveKeyModels 解析所选 Key 的模型候选并投影为 (modelId, kind) 集合
// （六域请求的候选校验共用）：Key 不存在/非请求者本人/读取失败经 KeyModels
// 端口以 *HTTPError 收敛；端口未装配显式降级 503。
func (rt *playgroundRoutes) resolveKeyModels(ctx context.Context, viewerID, apiKeyID string) ([]ModelOption, *HTTPError) {
	if rt.deps.KeyModels == nil {
		return nil, &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "模型候选暂不可用，请稍后重试"}
	}
	rows, httpErr := rt.deps.KeyModels.ListKeyModels(ctx, viewerID, apiKeyID)
	if httpErr != nil {
		return nil, httpErr
	}
	options := make([]ModelOption, 0, len(rows))
	for _, row := range rows {
		options = append(options, ModelOption{ModelID: row.ModelID, Kind: row.Kind})
	}
	return options, nil
}

// resolveKeyModelRows 解析所选 Key 的模型候选行（KeyModelRow 全投影，图像
// 任务创建需要 providerCode 计价投影；六域候选校验可经 resolveKeyModels 走
// 同一读取链）。Key 不存在/非请求者本人/读取失败经 KeyModels 端口以
// *HTTPError 收敛；端口未装配显式降级 503。
func (rt *playgroundRoutes) resolveKeyModelRows(ctx context.Context, viewerID, apiKeyID string) ([]KeyModelRow, *HTTPError) {
	if rt.deps.KeyModels == nil {
		return nil, &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "模型候选暂不可用，请稍后重试"}
	}
	return rt.deps.KeyModels.ListKeyModels(ctx, viewerID, apiKeyID)
}

// modelOptionOf 取指定 kind 的候选条目（不在候选时返回 nil）。
func modelOptionOf(options []ModelOption, modelID string, kind ModelKind) *ModelOption {
	for index := range options {
		if options[index].Kind == kind && options[index].ModelID == modelID {
			return &options[index]
		}
	}
	return nil
}

// --- 共用 HTTP 辅助 ---

// readJSONBody 读取请求体并施加上限（空体渲染为空对象，chat readJSONBody
// 同语义）。
func readJSONBody(r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	if limit <= 0 || limit > playgroundJSONBodyLimit {
		limit = playgroundJSONBodyLimit
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("body too large")
	}
	trimmed := trimSpaceBytes(body)
	if len(trimmed) == 0 {
		return []byte("{}"), nil
	}
	return trimmed, nil
}

// trimSpaceBytes 去字节载荷首尾空白（readJSONBody 的空体判定用）。
func trimSpaceBytes(body []byte) []byte {
	start := 0
	for start < len(body) && isASCIISpace(body[start]) {
		start++
	}
	end := len(body)
	for end > start && isASCIISpace(body[end-1]) {
		end--
	}
	return body[start:end]
}

// isASCIISpace 报告字节是否为 JSON 语义空白。
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// okEnvelope 是成功载荷信封（chat routes.go okEnvelope / kernel apiResponse
// 同款 {data} 包裹，mirror shared/http.ts ok(data)；错误面仍为扁平
// {message, code}，见 errors.go writeHTTPError）。
type okEnvelope struct {
	Data any `json:"data"`
}

// writeOKPayload 渲染 200 JSON 载荷（{data: payload} 成功信封，全部 JSON 成功
// 端点共用：keyModels/chatStream 外的 imagesReferences/imagesJobsCreate/
// imagesJobsList/imagesJob/videosCreate/videosPoll/embeddings/rerank/speech/
// references/artifacts 清单；SSE 帧与二进制端点（视频 content/语音音频）非
// JSON 载荷，不经此通道）。
func writeOKPayload(w http.ResponseWriter, payload any) {
	kernel.WriteJSON(w, http.StatusOK, okEnvelope{Data: payload})
}

// unauthorizedError 是 ScopeResolver 失败的统一映射（RequireSession 已挡未登
// 录，此处为直挂/中间件缺位的兜底 401）。
func unauthorizedError(err error) *HTTPError {
	return &HTTPError{Status: http.StatusUnauthorized, Code: codeInvalidRequest, Message: "缺少登录身份"}
}
