package main

// 演练场路由族组装（docs/functions/演练场设计.md §3.1）：cmd 适配层把派发执
// 行器（chatGatewayExecutor.WithChatDispatchAccount 固定派发视图 + 内部身份
// 通道）、accounts.Store 账户查询与 runtime cache 目录 loader 包装成 playground
// 包的自有端口，挂载 ${systemApiPrefix}/playground。
//
// 原 my-chat 会话面随设计 §8 退役后，调度覆盖两个进程级装配槽
// （setChainChatDispatchAccountGroups / setChainChatDispatchPinnedFetch）的
// 唯一置位点收敛到 composePlaygroundFamily：演练场固定派发（pinned 候选窗
// 口、无跨分组回退）依赖这两个槽在 /v1 链消费侧生效。

import (
	"context"
	"errors"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/playground"
)

// composePlaygroundFamily 装配演练场 Deps 并注册路由族。依赖与派发执行器同
// 源（chain / runtime cache / accounts.Store），缺一 fail fast。
func composePlaygroundFamily(composed *composition, services *chainRuntimeServices, chain *gatewayChain, accountStore *accounts.Store) error {
	if composed == nil {
		return errors.New("演练场组合缺少 composition")
	}
	if services == nil || services.Cache == nil {
		return errors.New("演练场组合缺少网关链 runtime cache")
	}
	if chain == nil {
		return errors.New("演练场组合缺少网关链 runtime")
	}
	if accountStore == nil {
		return errors.New("演练场组合缺少 accounts.Store")
	}
	catalog := chatModelCatalog{cache: services.Cache}
	deps := &playground.Deps{
		Kernel:         composed.kernel,
		RequireSession: composed.authDeps.RequireSession(true),
		ScopeResolver:  newPlaygroundScopeResolver(),
		// 固定派发端口：包装 chatGatewayExecutor.WithChatDispatchAccount（
		// pinned 候选窗口、无跨分组回退、内部身份免 Key、SuppressUsage 由被
		// 包装执行器承担，设计 §3.1/§3.7）。
		Dispatch:          playgroundDispatchAdapter{executor: newChatGatewayExecutor(chain)},
		Accounts:          playgroundAccountOptionsAdapter{store: accountStore},
		AccountLookup:     playgroundAccountLookupAdapter{store: accountStore},
		TransportAccounts: playgroundTransportAccountsAdapter{catalog: catalog},
		ProviderCatalog:   playgroundProviderCatalogAdapter{catalog: catalog},
		AccountCatalog:    playgroundAccountCatalogAdapter{catalog: catalog},
	}
	deps.Register(composed.kernel, systemAPIPrefix+"/playground")
	// 演练场调度覆盖通道（设计 §3.7 固定派发）：account 模式承载分组解析端口，
	// 与 /v1 消费侧同口径（FindChatAccount 的 EnabledGroupIDs，enabled=1 分组
	// 绑定）。生产组合根装配期置位一次，此后只读（进程级槽，chain_obs_wiring.go
	// 同模式）。
	setChainChatDispatchAccountGroups(func(accountID string) ([]string, bool) {
		// 进程级系统上下文（无请求用户），绑定授权已在派发端口内收敛；按
		// admin 范围（全量号池口径）解析账户启用分组。
		ref, err := accountStore.FindChatAccount(accounts.AccountScope{IsAdmin: true}, accountID)
		if err != nil || ref == nil {
			return nil, false
		}
		return ref.EnabledGroupIDs, true
	})
	// 演练场调度覆盖通道的 pinned 直取端口：经 runtime cache 专用路径按（分
	// 组, 账户 ID）直取运行时快照，含禁用/冷却/过期/禁调度账户；装配期置位
	// 一次，此后只读。
	setChainChatDispatchPinnedFetch(services.Cache.ListFreshOpenAIAccountsForChatPinnedAsync)
	return nil
}

// newPlaygroundScopeResolver 从登录态解析数据范围（AuthContext.SystemAccountID
// + IsAdminRole 判定；RequireSession 中间件已挡未登录，此处兜底错误不再透出
// 身份细节）。
func newPlaygroundScopeResolver() playground.ScopeResolver {
	return func(r *http.Request) (playground.Scope, error) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			return playground.Scope{}, errors.New("请先登录")
		}
		return playground.Scope{ViewerID: auth.SystemAccountID, IsAdmin: authsys.IsAdminRole(auth.Role)}, nil
	}
}

// playgroundDispatchAdapter 把 playground.DispatchPort 适配到派发执行器：每次
// 派发按（目标账户, 请求者）解析固定派发视图（WithChatDispatchAccount 每调用
// 返回新视图实例）。
type playgroundDispatchAdapter struct {
	executor *chatGatewayExecutor
}

// Dispatch implements playground.DispatchPort.
func (a playgroundDispatchAdapter) Dispatch(ctx context.Context, req playground.DispatchRequest) (*playground.DispatchResponse, error) {
	executor := a.executor.WithChatDispatchAccount(req.AccountID, req.OwnerID)
	response, err := executor.Dispatch(ctx, GenerationDispatchRequest{
		Path:    req.Path,
		Method:  req.Method,
		Headers: req.Headers,
		Body:    req.Body,
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, nil
	}
	return &playground.DispatchResponse{
		Status: response.Status,
		Header: response.Header,
		Body:   response.Body,
	}, nil
}

// playgroundAccountOptionsAdapter 适配 accounts.Store.ListChatAccountOptions
// （GET /__aisys__/api/playground/accounts 的账户列表端口；数据范围换算成
// accounts.AccountScope 后透传，投影为 playground 最小条目）。
type playgroundAccountOptionsAdapter struct {
	store *accounts.Store
}

// ListAccountOptions implements playground.AccountOptionsPort.
func (a playgroundAccountOptionsAdapter) ListAccountOptions(ctx context.Context, scope playground.Scope) ([]playground.AccountOption, error) {
	options, err := a.store.ListChatAccountOptions(ctx, accounts.AccountScope{ViewerID: scope.ViewerID, IsAdmin: scope.IsAdmin})
	if err != nil {
		return nil, err
	}
	out := make([]playground.AccountOption, 0, len(options))
	for _, option := range options {
		out = append(out, playground.AccountOption{
			ID:           option.ID,
			Name:         option.Name,
			ProviderCode: option.ProviderCode,
			Status:       option.Status,
		})
	}
	return out, nil
}

// playgroundAccountLookupAdapter 适配 accounts.Store.FindChatAccount（数据范围
// 内账户事实：名称/provider/生效投影/启用分组绑定）。
type playgroundAccountLookupAdapter struct {
	store *accounts.Store
}

// FindAccount implements playground.AccountLookupPort. 范围外/不存在 (nil,
// nil) 语义原样透传。
func (a playgroundAccountLookupAdapter) FindAccount(ctx context.Context, scope playground.Scope, accountID string) (*playground.AccountRef, error) {
	ref, err := a.store.FindChatAccount(accounts.AccountScope{ViewerID: scope.ViewerID, IsAdmin: scope.IsAdmin}, accountID)
	if err != nil || ref == nil {
		return nil, err
	}
	return &playground.AccountRef{
		ID:              ref.ID,
		Name:            ref.Name,
		ProviderCode:    ref.ProviderCode,
		Enabled:         ref.Enabled,
		EnabledGroupIDs: ref.EnabledGroupIDs,
	}, nil
}

// playgroundTransportAccountsAdapter 适配 runtime cache 的 chat-pinned 直取视
// 口（绑定作用域运行时快照，含非 active/冷却/过期账户），投影为 playground
// 传输视图（媒体白名单「账户可路由」判定消费端点模式与模型清单）。
type playgroundTransportAccountsAdapter struct {
	catalog chatModelCatalog
}

// ListPinnedAccountsForGroup implements playground.TransportAccountsPort.
func (a playgroundTransportAccountsAdapter) ListPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []playground.TransportAccount {
	transportAccounts := a.catalog.ListChatPinnedAccountsForGroup(groupID, systemAccountID, accountID)
	out := make([]playground.TransportAccount, 0, len(transportAccounts))
	for _, account := range transportAccounts {
		mappings := make([]playground.TransportModelMapping, 0, len(account.ModelMappings))
		for _, mapping := range account.ModelMappings {
			enabled := mapping.Enabled
			mappings = append(mappings, playground.TransportModelMapping{
				Enabled:                enabled,
				SourceModel:            mapping.SourceModel,
				UpstreamModel:          mapping.UpstreamModel,
				SourceEndpointFamily:   mapping.SourceEndpointFamily,
				UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			})
		}
		out = append(out, playground.TransportAccount{
			ID:                     account.ID,
			Type:                   account.Type,
			ProviderCode:           account.ProviderCode,
			SupportedEndpointModes: account.SupportedEndpointModes,
			SupportedModels:        account.SupportedModels,
			ModelMappings:          mappings,
		})
	}
	return out
}

// playgroundProviderCatalogAdapter 适配 runtime cache 供应商目录 loader
// （IncludeUnpriced 口径与目录读取链一致，custom 继承在 loader 内已生效）。
type playgroundProviderCatalogAdapter struct {
	catalog chatModelCatalog
}

// ListProviderCatalog implements playground.ProviderCatalogPort.
func (a playgroundProviderCatalogAdapter) ListProviderCatalog(providerCode, systemAccountID string) []playground.CatalogItem {
	return playgroundCatalogItemsProjection(a.catalog.ListProviderCatalog(providerCode, systemAccountID))
}

// playgroundAccountCatalogAdapter 适配 runtime cache 的账户显式绑定模型目录
// （generic_http 账户按 account_supported_models 补齐）。
type playgroundAccountCatalogAdapter struct {
	catalog chatModelCatalog
}

// ListAccountSupportedModels implements playground.AccountCatalogPort.
func (a playgroundAccountCatalogAdapter) ListAccountSupportedModels(accountID, systemAccountID string) []playground.CatalogItem {
	return playgroundCatalogItemsProjection(a.catalog.ListAccountSupportedModels(accountID, systemAccountID))
}

// playgroundCatalogItemsProjection 是目录条目到 playground 最小投影的字段复
// 制（Model + 协议清单 + 协议×工具矩阵；纯 JSON 数据的字段搬运，无行为）。
func playgroundCatalogItemsProjection(items []ProviderModelCatalogItem) []playground.CatalogItem {
	out := make([]playground.CatalogItem, 0, len(items))
	for _, item := range items {
		out = append(out, playground.CatalogItem{
			Model:                    item.Model,
			SupportedAPIProtocols:    item.SupportedAPIProtocols,
			SupportedToolsByProtocol: item.SupportedToolsByProtocol,
		})
	}
	return out
}
