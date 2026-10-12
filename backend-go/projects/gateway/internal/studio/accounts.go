package studio

import "context"

// 账户候选与域模型目录端口（媒体工作室设计 §4.1）。账户候选口径同
// /my-chat/accounts 两档（admin/super_admin 全量号池，普通用户自有 + 被授
// 权；全状态可见，不按启用状态过滤），由 internal/accounts 的 Store 实现；
// 域模型目录由组合根在 runtime cache 目录读链上按域过滤实现，工作室不新
// 造目录。

// StudioAccountOption 是 GET /my-studio/accounts 的行投影（§4.1 响应表）。
type StudioAccountOption struct {
	AccountID    string `json:"accountId"`
	Name         string `json:"name"`
	ProviderCode string `json:"providerCode"`
	Domain       string `json:"domain"`
	Status       string `json:"status"`
}

// AccountOptionsLookup 列出登录用户授权范围内的账户候选。viewerID 是当前
// 登录用户；adminScope 为 true 时全量号池（admin/super_admin）；domain 为
// 空，返回全部能力域，否则按 accounts.capability_domain 精确过滤。Port 由
// internal/accounts 在组合根装配；nil 让端点返回显式错误。
type AccountOptionsLookup interface {
	ListStudioAccountOptions(ctx context.Context, viewerID string, adminScope bool, domain string) ([]StudioAccountOption, error)
}

// StudioModel 是 GET /my-studio/models 的行投影（§4.1 响应表）：模型 id、
// 供应商、域、端点模式词表与能力载荷（能力接口驱动表单，未声明的键前端
// 不渲染；P0 目录读链无能力载荷来源，恒为空对象，不编造参数项）。
type StudioModel struct {
	Model         string         `json:"model"`
	ProviderCode  string         `json:"providerCode"`
	Domain        string         `json:"domain"`
	EndpointModes []string       `json:"endpointModes"`
	Capabilities  map[string]any `json:"capabilities"`
}

// ModelCatalog 是域模型目录端口：按域返回当前用户范围内可派发模型的最小
// 集（目录来源 = provider_model_catalog 静态快照 + custom_provider_models
// 既有目录读链，按 mode/端点模式过滤）。范围两档与 AccountOptionsLookup 一
// 致：adminScope 为 true 时按全量号池的 provider 集合聚合。Port 由组合根实
// 现；nil 让端点返回显式错误。
type ModelCatalog interface {
	ListDomainModels(ctx context.Context, viewerID string, adminScope bool, domain string) ([]StudioModel, error)
}

// 域词表（§4.1）：accounts 的 domain 查询参数允许 chat 四域全集；models
// 的 domain 必填且仅收 images/audio/video 三个工作台域。
var AccountDomainValues = map[string]bool{"chat": true, "image": true, "audio": true, "video": true}

var WorkbenchDomainValues = map[string]bool{"image": true, "audio": true, "video": true}
