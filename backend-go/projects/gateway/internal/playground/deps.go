package playground

// 演练场（docs/functions/演练场设计.md §3）依赖端口定义。所需的派发、Key 模型
// 目录能力全部以本包自有端口类型表达，由 cmd/juhe-ai-gateway 组合根适配既有
// 实现（chatGatewayExecutor 直驱 /v1 链 handler、apikeys.Store 的 FindSecret
// 明文解密、runtime cache 的 Key 行/分组模型并集装载）。

import (
	"context"
	"io"
	"net/http"
)

// Scope 是演练场的数据范围（口径沿 chat.ChatBindScope：admin/super_admin 看
// 全量号池，其余用户看自有 + 被授权账户；设计 §3.1）。由 cmd 层 ScopeResolver
// 从登录态解析，playground 只见本结构。RemoteAddr 是控制台请求的客户端地址
// （派发 RemoteAddr 透传源）。
type Scope struct {
	ViewerID   string
	IsAdmin    bool
	RemoteAddr string
}

// ScopeResolver 从请求解析数据范围（RequireSession 中间件之后调用；未登录
// 返回错误）。生产实现经 authsys.AuthContextFrom + IsAdminRole 投影。
type ScopeResolver func(r *http.Request) (Scope, error)

// DispatchRequest 是派发出站请求（进程内直调 /v1 链，Path 空默认
// /v1/chat/completions 由派发执行器裁定）。APIKeyID 是本次派发使用的真实网关
// API Key（派发前由 cmd 适配层解密注入 Authorization: Bearer 明文，Key 归属
// 必须 == OwnerID）；OwnerID 是请求者系统账户（fail-closed 归属校验基准）；
// RemoteAddr 透传控制台请求的客户端地址（IP 黑名单/单 IP 并发/审计同址收敛）。
type DispatchRequest struct {
	APIKeyID   string
	OwnerID    string
	RemoteAddr string
	Path       string
	Method     string
	Headers    map[string]string
	Body       []byte
}

// DispatchResponse 是派发响应的已消费子集：状态码 + 头 +（流式）响应体。
type DispatchResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// DispatchPort 是派发端口（设计 §3.1：playground 定义自己的 Dispatch 端口
// 类型，cmd 层适配器解密所选 Key 并包装 chatGatewayExecutor 直驱 /v1 链；
// Key 归属 fail-closed 校验由适配层承担，非本人 Key 以 *HTTPError 返回）。
type DispatchPort interface {
	Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResponse, error)
}

// CatalogItem 是模型目录条目的最小投影（只保留演练场分类所需字段；原始类型
// gatewayruntimecache.ProviderModelCatalogItem 的其余字段与本包无关）。Custom
// 继承已由目录读取链（runtime cache loader）生效。
type CatalogItem struct {
	Model                 string
	ProviderCode          string
	SupportedAPIProtocols []string
}

// KeyModelRow 是 GET /key-models 响应行（所选 Key 的分组模型并集 ∪ 目录分类）。
type KeyModelRow struct {
	ModelID       string        `json:"modelId"`
	Kind          ModelKind     `json:"kind"`
	ProviderCode  string        `json:"providerCode"`
	ImageEstimate *float64      `json:"imageEstimate,omitempty"`
}

// KeyModelsPort 列出所选 Key 的演练场模型候选（生产实现 = cmd 层经 runtime
// cache 读 Key 行（owner + GroupBindings）→ 分组模型并集 ∪ 目录分类）。Key
// 不存在、非请求者本人（含 admin 选他人 Key）或读取失败以 *HTTPError 返回，
// 错误信封由 playground 现有错误面渲染。
type KeyModelsPort interface {
	ListKeyModels(ctx context.Context, viewerID, apiKeyID string) ([]KeyModelRow, *HTTPError)
}

// Deps 是演练场路由依赖（组合根装配；端口缺失时对应端点显式降级为错误）。
type Deps struct {
	// Kernel 是组合根的 kernel mux（Register 键 "METHOD /path"）。
	Kernel Registerer
	// RequireSession 与 chat 面同款登录态中间件（authDeps.RequireSession(true)）。
	RequireSession func(http.Handler) http.Handler
	// ScopeResolver 从请求解析数据范围（RequireSession 之后调用）。
	ScopeResolver ScopeResolver
	// Dispatch 是派发端口（cmd 层解密所选 Key 后包装 chatGatewayExecutor）。
	Dispatch DispatchPort
	// KeyModels 是 Key 模型候选端口（cmd 层包装 runtime cache + 目录分类）。
	KeyModels KeyModelsPort
	// Artifacts 是产物暂存后端（设计 §3.8）：生产由组合根注入 PG 实现
	// （NewPGArtifactStore(composed.db)），nil = 暂存面未接线，产物端点显式降级
	// 503（功能不设开关，非静默空行为）。2026-11-09 起不再写本地文件。
	Artifacts ArtifactStore
	// ImageJobs 是图像任务存储后端（设计 §3.4/§5.1）：生产由组合根注入 PG
	// 实现（NewPGImageJobStore(composed.db)），nil = 图像任务面未接线，端点
	// 显式降级 503。任务记录行永不删除（§3.8）。
	ImageJobs ImageJobStore
	// ImageCost 是图像任务扣费端口（§3.4.2：按目录行本地计算；cmd 层包装
	// runtime cache 目录 + pricing.BuildCostBreakdown）。nil = 目录无价语义，
	// cost_usd 恒 null（卡片显示「-」，不虚构金额）。
	ImageCost ImageCostPort
	// ImageRunner 是图像任务进程内执行器（§3.4.2：缓冲队列 + 2 worker）。
	// Register 时在 ImageJobs 已注入且本槽为空的情况下自动装配并回写（功能
	// 默认生效）；cmd 层装配后取此槽执行启动恢复（RecoverAtBoot）。
	ImageRunner *ImageJobRunner
}

// Registerer 是演练场挂载所需的 kernel 子集（kernel.Kernel 同形）。
type Registerer interface {
	Register(pattern string, handler http.Handler)
}

// bodyLimitOverrideRegistrar 是路由级 body limit 覆盖的可选 kernel 能力
// （kernel.Kernel 同形）。演练场注册路由时为参考图上传端点登记覆盖值；测试
// 直挂 ServeMux 不支持该能力，按缺失静默跳过（直挂场景本就无 kernel 层上限）。
type bodyLimitOverrideRegistrar interface {
	RegisterBodyLimitOverride(method, path string, limit int64)
}
