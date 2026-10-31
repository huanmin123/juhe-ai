package playground

// 演练场路由（docs/functions/演练场设计.md §3）：经 cmd/juhe-ai-gateway 组合
// 根挂载 GET/POST /__aisys__/api/playground/...（kernel Register，与 my-chat
// 同级，自动获得 kernel 的 body limit / IP rate limit / no-store 包裹）；每条
// 路由套 RequireSession(true)。五组端点：
//
//	GET  /accounts                账户与模型目录（四页共用）
//	POST /chat/stream             一次性对话 SSE
//	POST /images                  同步生图
//	POST /videos                  异步视频创建
//	GET  /videos/{jobId}          视频任务轮询
//	GET  /videos/{jobId}/content  视频产物字节流
//	POST /speech                  同步语音合成

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// playgroundJSONBodyLimit 兜底请求体上限（kernel body limit 之外的领域面校验，
// chat 面 chatSystemAPIJSONBodyLimit 同模式）。
const playgroundJSONBodyLimit = 64 * 1024 * 1024

// playgroundRoutes 是演练场路由运行时（无状态：会话/任务零落库，设计 §1.2）。
type playgroundRoutes struct {
	deps *Deps
}

// Register 挂载演练场路由族到 kernel（pattern 键 "METHOD /prefix/path"，与
// chat Deps.Register 同款挂载风格）。
func (d *Deps) Register(k Registerer, prefix string) {
	if prefix == "" {
		prefix = "/__aisys__/api/playground"
	}
	if d.FetchURL == nil {
		d.FetchURL = defaultFetchURL
	}
	rt := &playgroundRoutes{deps: d}
	mount := func(method, pattern string, handler http.HandlerFunc) {
		k.Register(method+" "+prefix+pattern, rt.wrap(handler))
	}
	mount("GET", "/accounts", rt.accountsHandler)
	mount("POST", "/chat/stream", rt.chatStreamHandler)
	mount("POST", "/images", rt.imagesHandler)
	mount("POST", "/videos", rt.videosCreateHandler)
	mount("GET", "/videos/{jobId}", rt.videosPollHandler)
	mount("GET", "/videos/{jobId}/content", rt.videosContentHandler)
	mount("POST", "/speech", rt.speechHandler)
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

// accountsHandler 服务 GET /accounts（设计 §3.2：单请求四页共用；仅列 active
// 且启用、当前用户可见者；不可派发对应协议的模型不列）。
func (rt *playgroundRoutes) accountsHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if rt.deps.Accounts == nil {
		writeHTTPError(w, &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "账户列表暂不可用，请稍后重试"})
		return
	}
	options, listErr := rt.deps.Accounts.ListAccountOptions(r.Context(), scope)
	if listErr != nil {
		writeHTTPError(w, &HTTPError{Status: http.StatusInternalServerError, Code: codeEndpointDegraded, Message: "账户列表查询失败"})
		return
	}
	accounts := make([]accountCatalogEntry, 0, len(options))
	for _, option := range options {
		// active 且启用（Option.Status 为生效状态原值；ref.Enabled 为生效
		// active 投影，双口径复核防快照漂移）；无运行时快照（不在任何启用
		// 分组）的账户无法固定派发任何协议，整户剔除（chat 面模型作用域
		// convergeChatAccountScope 空视图同口径）。
		if option.ID == "" || option.Status != "active" {
			continue
		}
		ref, catalog, transportView, viewErr := rt.resolveAccountPlaygroundView(r.Context(), scope, option.ID)
		// 单户查询失败/未启用/无快照时跳过该户继续列表——属列表部分结果边界：
		// 全局「不静默降级」条款约束的是业务写入路径吞错改行为；此处是展示面
		// 目录列表，逐户失败收敛为剔除该条目，整表查询错误已由上方 listErr →
		// 500 显式承接，整单拒绝反而会把单户脏数据放大为整页不可用。
		if viewErr != nil || ref == nil || !ref.Enabled || transportView == nil {
			continue
		}
		accounts = append(accounts, accountCatalogEntry{
			AccountID:   option.ID,
			AccountName: option.Name,
			// 响应供应商码以账户事实（ref）为准：Option 与 ref 同表同列，取
			// ref 保证与目录分类用的 providerCode 单一来源。
			ProviderCode: ref.ProviderCode,
			Models:       classifyCatalogModels(normalizeProviderToken(ref.ProviderCode), catalog, transportView),
		})
	}
	writeOKPayload(w, accountsResponse{Accounts: accounts})
}

// accountsResponse 是 GET /accounts 响应（设计 §3.2）。
type accountsResponse struct {
	Accounts []accountCatalogEntry `json:"accounts"`
}

// accountCatalogEntry 是单账户的目录条目。
type accountCatalogEntry struct {
	AccountID    string        `json:"accountId"`
	AccountName  string        `json:"accountName"`
	ProviderCode string        `json:"providerCode"`
	Models       []ModelOption `json:"models"`
}

// resolveAccountModels 解析账户视图并按 §3.2 分类目录（各端点候选校验共用）。
// 账户不在数据范围时 ref 为 nil（由调用方决定 404 映射），查询面错误以
// HTTPError 返回。
func (rt *playgroundRoutes) resolveAccountModels(ctx context.Context, scope Scope, accountID string) (*AccountRef, []ModelOption, *HTTPError) {
	ref, catalog, transportView, httpErr := rt.resolveAccountPlaygroundView(ctx, scope, accountID)
	if httpErr != nil {
		return nil, nil, httpErr
	}
	if ref == nil {
		return nil, nil, nil
	}
	options := classifyCatalogModels(normalizeProviderToken(ref.ProviderCode), catalog, transportView)
	return ref, options, nil
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

// writeOKPayload 渲染 200 JSON 载荷（{data: payload} 成功信封，四个 JSON 成功
// 端点共用：accounts/images/videosCreate/videosPoll；SSE 帧与二进制端点
// （视频 content/语音音频）非 JSON 载荷，不经此通道）。
func writeOKPayload(w http.ResponseWriter, payload any) {
	kernel.WriteJSON(w, http.StatusOK, okEnvelope{Data: payload})
}

// unauthorizedError 是 ScopeResolver 失败的统一映射（RequireSession 已挡未登
// 录，此处为直挂/中间件缺位的兜底 401）。
func unauthorizedError(err error) *HTTPError {
	return &HTTPError{Status: http.StatusUnauthorized, Code: codeInvalidRequest, Message: "缺少登录身份"}
}
