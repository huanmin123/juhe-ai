package main

// compose_account_reads.go — 账户管理面读伴随切片的组合根移交装配（审计
// verdict-ak / verdict-ap / verdict-aa 三个 TRULY_MISSING / 风险注记补齐）：
//
//	AuthorizationStatsSource → authz.Store（internal/authz/stats_loader.go，
//	                           Node loadResourceAuthorizationStatsByResourceIdsAsync）
//	APIKeyRuntimeDetailsReader → accountkeystates.Store（internal/accountkeystates/
//	                           details.go，Node loadAccountApiKeyRuntimeDetailsByAccountIdsAsync）
//
// 两个端口都容忍 nil（账户切片保持零值/空 items 降级），装配失败按组合根约定
// fail-fast：accountkeystates Store 校验业务句柄与运行密钥。

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// authorizationStatsSourceAdapter bridges the authz stats loader to the
// accounts port (both slices own their DTO shapes; the composition root does
// the field-wise translation — the same role the loader's own projection plays
// in Node).
type authorizationStatsSourceAdapter struct{ store *authz.Store }

func (a authorizationStatsSourceAdapter) ResourceAuthorizationStatsByResourceIds(ctx context.Context, resourceType string, resourceIDs []string) (map[string]accounts.AuthorizationStats, error) {
	loaded, err := a.store.ResourceAuthorizationStatsByResourceIds(ctx, resourceType, resourceIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]accounts.AuthorizationStats, len(loaded))
	for id, stats := range loaded {
		out[id] = accounts.AuthorizationStats{
			AuthorizationCount:     stats.AuthorizationCount,
			AuthorizationTeamCount: stats.AuthorizationTeamCount,
		}
	}
	return out, nil
}

// wireAccountReadCompanions wires the two account read companions. keyStates
// is a dedicated accountkeystates Store instance (stateless over the shared
// business handle; the runtime-reset bridge builds its own — the stores share
// the same tables and CAS fences by design).
func wireAccountReadCompanions(composed *composition, accountStore *accounts.Store, authzStore *authz.Store, secret string) error {
	// 授权统计 loader：accounts 列表三字段投影（Node
	// account-summary.repository.ts:1608-1610）。
	accountStore.SetAuthorizationStatsSource(authorizationStatsSourceAdapter{store: authzStore})
	// API Key 运行明细读取器：/accounts/{id}/api-key-runtime 的 items 数据源
	// （此前组装根从未注入，响应 items 恒为 []）。
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB:       composed.db,
		Postgres: composed.pgDialect,
		Secret:   secret,
		Now:      time.Now,
	})
	if err != nil {
		return err
	}
	accountStore.SetAPIKeyRuntimeDetailsReader(keyStates)
	return nil
}
