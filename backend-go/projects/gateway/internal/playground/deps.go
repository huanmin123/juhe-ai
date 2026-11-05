package playground

// 演练场（docs/functions/演练场设计.md §3）依赖端口定义。本包不 import
// internal/chat（该包有并行未提交改动且随设计 §8 整体退役），所需的派发、
// 账户、目录能力全部以本包自有端口类型表达，由 cmd/juhe-ai-gateway 组合根
// 适配既有实现（chatGatewayExecutor.WithChatDispatchAccount、accounts.Store
// 的 ListChatAccountOptions/FindChatAccount、runtime cache 目录 loader）。

import (
	"context"
	"io"
	"net/http"
)

// Scope 是演练场的数据范围（口径沿 chat.ChatBindScope：admin/super_admin 看
// 全量号池，其余用户看自有 + 被授权账户；设计 §3.1）。由 cmd 层 ScopeResolver
// 从登录态解析，playground 只见本结构。
type Scope struct {
	ViewerID string
	IsAdmin  bool
}

// ScopeResolver 从请求解析数据范围（RequireSession 中间件之后调用；未登录
// 返回错误）。生产实现经 authsys.AuthContextFrom + IsAdminRole 投影。
type ScopeResolver func(r *http.Request) (Scope, error)

// DispatchRequest 是固定派发出站请求（形状同 chat.GenerationDispatchRequest：
// 进程内直调 /v1 链，Path 空默认 /v1/chat/completions 由派发执行器裁定）。
// AccountID 是固定派发目标账户；OwnerID 是请求者系统账户（内部身份通道注入
// 用，与 chat 面工具子调用 toolDispatchExecutor 的 ownerID 同源口径）。
type DispatchRequest struct {
	AccountID string
	OwnerID   string
	Path      string
	Method    string
	Headers   map[string]string
	Body      []byte
}

// DispatchResponse 是派发响应的已消费子集：状态码 + 头 +（流式）响应体。
type DispatchResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// DispatchPort 是固定派发端口（设计 §3.1：playground 定义自己的 Dispatch
// 端口类型，cmd 层适配器包装 chatGatewayExecutor.WithChatDispatchAccount；
// pinned 候选窗口、无跨分组回退、内部身份免 Key、SuppressUsage 均由被包装
// 执行器承担）。
type DispatchPort interface {
	Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResponse, error)
}

// AccountOption 是账户下拉最小投影（与 chat.ChatAccountOption 同口径：id/
// 名称/供应商/生效状态原值，不设可用性过滤）。
type AccountOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderCode string `json:"providerCode"`
	Status       string `json:"status"`
}

// AccountOptionsPort 列出数据范围内全部未删除账户（生产实现 = accounts.Store
// 的 ListChatAccountOptions，经 cmd 适配层换掉 chat.ChatBindScope 入参）。
type AccountOptionsPort interface {
	ListAccountOptions(ctx context.Context, scope Scope) ([]AccountOption, error)
}

// AccountRef 是账户的只读事实视图（名称/provider/启用分组绑定，与
// chat.ChatAccountRef 同口径）。Enabled 为生效 active 投影；设计 §3.2 的
// 「仅列 active 且启用」以 Option.Status=="active" + 本字段复合判定。
type AccountRef struct {
	ID              string
	Name            string
	ProviderCode    string
	Enabled         bool
	EnabledGroupIDs []string
}

// AccountLookupPort 解析数据范围内账户对象（生产实现 = accounts.Store 的
// FindChatAccount；范围外与不存在返回 (nil, nil)）。
type AccountLookupPort interface {
	FindAccount(ctx context.Context, scope Scope, accountID string) (*AccountRef, error)
}

// TransportModelMapping 是账户模型映射的传输投影（chat.ChatTransportModelMapping
// 同形子集）。
type TransportModelMapping struct {
	Enabled                *bool
	SourceModel            string
	UpstreamModel          string
	SourceEndpointFamily   string
	UpstreamEndpointFamily string
}

// TransportAccount 是账户运行时快照的传输投影（chat.ChatTransportAccount
// 同形子集）：生图/媒体白名单的「账户可路由」判定读端点模式与模型清单。
type TransportAccount struct {
	ID                     string
	Type                   string
	ProviderCode           string
	SupportedEndpointModes []string
	SupportedModels        []string
	ModelMappings          []TransportModelMapping
}

// TransportAccountsPort 是 chat-pinned 直取端口（会话绑定作用域的运行时快照
// 视图；生产实现 = runtime cache 的 ListFreshOpenAIAccountsForChatPinnedAsync
// 经 cmd 适配层投影）。按（分组, 账户 ID）直取，含非 active/冷却/过期账户。
type TransportAccountsPort interface {
	ListPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []TransportAccount
}

// CatalogItem 是模型目录条目的最小投影（只保留演练场分类所需字段；原始类型
// gatewayruntimecache.ProviderModelCatalogItem / chat.ProviderModelCatalogItem
// 的其余字段与本包无关）。Custom 继承已由目录读取链（chat 面同款 loader）生效。
type CatalogItem struct {
	Model                    string
	SupportedAPIProtocols    []string
	SupportedToolsByProtocol map[string][]string
}

// ProviderCatalogPort 取供应商目录（生产实现 = runtime cache
// ListCachedProviderModelCatalogAsync，IncludeUnpriced 与 chat 面同口径）。
type ProviderCatalogPort interface {
	ListProviderCatalog(providerCode, systemAccountID string) []CatalogItem
}

// FetchResult 是 fetch_url 工具的执行结果（正文文本 + 供 tool_end 摘要的首行）。
type FetchResult struct {
	Text string
}

// FetchURLFunc 是 fetch_url 的执行端口（设计 §3.3：后端执行的函数工具）。
// 失败不产生 HTTP 错误，由调用方把「抓取失败:<原因>」作为工具结果回喂模型；
// 独立函数端口让测试以 Mock 回放成功/失败路径。
type FetchURLFunc func(ctx context.Context, rawURL string) (FetchResult, error)

// Deps 是演练场路由依赖（组合根装配；端口缺失时对应端点显式降级为错误）。
type Deps struct {
	// Kernel 是组合根的 kernel mux（Register 键 "METHOD /path"）。
	Kernel Registerer
	// RequireSession 与 chat 面同款登录态中间件（authDeps.RequireSession(true)）。
	RequireSession func(http.Handler) http.Handler
	// ScopeResolver 从请求解析数据范围（RequireSession 之后调用）。
	ScopeResolver ScopeResolver
	// Dispatch 是固定派发端口（cmd 层包装 chatGatewayExecutor）。
	Dispatch DispatchPort
	// Accounts/AccountLookup/TransportAccounts 是账户三端口（cmd 层包装
	// accounts.Store 与 runtime cache）。
	Accounts          AccountOptionsPort
	AccountLookup     AccountLookupPort
	TransportAccounts TransportAccountsPort
	// ProviderCatalog 是模型目录端口（cmd 层包装同一 runtime cache loader，
	// 与 chat 面读取链同源，custom 继承已生效）。generic_http 账户的
	// account_supported_models 目录回退端口随通用连接器退场删除。
	ProviderCatalog ProviderCatalogPort
	// FetchURL 是 fetch_url 工具执行端口（nil 时装配默认实现 fetchurl.go）。
	FetchURL FetchURLFunc
	// ArtifactsRoot 是产物暂存根 <数据根>/playground-artifacts（设计 §3.8，零
	// 新 env：组合根按 internal/datadir 数据根派生传入，无显式覆盖入口）。空 =
	// 暂存面未接线，产物端点显式降级 503（功能不设开关，非静默空行为）。
	ArtifactsRoot string
}

// Registerer 是演练场挂载所需的 kernel 子集（kernel.Kernel 同形）。
type Registerer interface {
	Register(pattern string, handler http.Handler)
}
