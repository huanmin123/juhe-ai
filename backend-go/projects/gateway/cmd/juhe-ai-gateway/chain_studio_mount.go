package main

// 媒体工作室路由族装配（媒体工作室设计 §4，挂载 ${systemApiPrefix}/my-studio）：
// 资产仓库落业务库（studio_* 三表 + 配额账本，juhe_business schema 限定）、
// 对象存储复用 chat 资产同款本地对象存储（chat_assets.storage_key 同源，
// §3.1 要点"复用既有存储端口，不新造存储栈"；chat 清理按行键回收，不波及
// 同根 studio 键）、账户候选/域模型目录端口与 purpose=studio Key 供应器，
// 登录中间件与 chat 面同一套（composed.authDeps.RequireSession）。

import (
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/studio"
)

// composeStudioFamily builds the studio Deps over the business database and
// the assembled /v1 chain runtime services, and registers the my-studio route
// family on the kernel. chain 是装配好的 /v1 网关链（generations 受理执行器
// 的进程内直驱目标，§4.5.1 不经回环 HTTP）。
func composeStudioFamily(composed *composition, cfg runtimeConfig, services *chainRuntimeServices, accountStore *accounts.Store, chain *gatewayChain) (*studio.Deps, error) {
	if composed == nil {
		return nil, fmt.Errorf("my-studio 组合缺少 composition")
	}
	if composed.db == nil {
		return nil, fmt.Errorf("my-studio 组合缺少业务数据库句柄")
	}
	if services == nil || services.Cache == nil {
		return nil, fmt.Errorf("my-studio 组合缺少网关链 runtime cache")
	}
	if accountStore == nil {
		return nil, fmt.Errorf("my-studio 组合缺少账户存储")
	}
	if chain == nil {
		return nil, fmt.Errorf("my-studio 组合缺少网关链 runtime")
	}
	objectStore, err := newChatAssetObjectStore(cfg.ChatAssetsRoot)
	if err != nil {
		return nil, err
	}
	deps := &studio.Deps{
		Store:          studio.NewStore(composed.db, studio.Dialect{Postgres: composed.pgDialect, Schema: businessSchema}),
		RequireSession: composed.authDeps.RequireSession(true),
		ObjectStore:    objectStore,
		AccountOptions: accountStore,
		Models:         studioModelCatalog{cache: services.Cache, accounts: accountStore},
		Keys:           newStudioAPIKeyProvider(composed.db, composed.pgDialect, cfg.Secret),
		Executor:       newStudioGatewayExecutor(chain),
	}
	deps.Register(composed.kernel, systemAPIPrefix+"/my-studio")
	return deps, nil
}
