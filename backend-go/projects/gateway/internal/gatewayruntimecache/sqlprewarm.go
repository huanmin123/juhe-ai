// sqlprewarm.go 移植 Node prewarmGatewayApiKeyValidationCacheAsync 的数据面
// （storage/gateway-api-key.repository.ts:300-336，verdict-ap TRULY_MISSING 补齐）：
// 启动期枚举全部满足校验条件的活跃 API Key（status='active' 未过期 + 活跃
// system_accounts 归属 + 活跃 route_strategies 绑定，与
// loadGatewayAPIKeyByKeyHash 的 JOIN 条件逐列一致），供 Service 预热校验缓存。
package gatewayruntimecache

import (
	"context"
)

// GatewayAPIKeyPrewarmer is the narrow loader seam behind
// PrewarmGatewayAPIKeyValidationCache. *SQLReadModels implements it; other
// ReadModels implementations (tests) may leave it unsupported — the prewarm
// degrades to a no-op like the Node control-replica skip (server.ts:180).
type GatewayAPIKeyPrewarmer interface {
	// ListActiveGatewayAPIKeyHashes mirrors the Node prewarm row query, trimmed
	// to the cache key projection (api_keys.key_hash of every warmable row).
	ListActiveGatewayAPIKeyHashes(ctx context.Context) ([]string, error)
	// ReadGatewayRuntimeByKeyHash resolves one warmable row by its hash.
	ReadGatewayRuntimeByKeyHash(ctx context.Context, keyHash string) (GatewayRuntime, error)
}

// gatewayAPIKeyPrewarmChunkSize mirrors the Node prewarm batch width (rows
// sliced 8 at a time); the Go loader walks hashes chunk-wise to bound the
// IN-lists of any downstream reads.
const gatewayAPIKeyPrewarmChunkSize = 8

// ListActiveGatewayAPIKeyHashes implements GatewayAPIKeyPrewarmer: the same
// join contract as the Node prewarm query (api_keys ⋈ system_accounts ⋈
// route_strategies, active + unexpired), projecting only the key hash.
func (m *SQLReadModels) ListActiveGatewayAPIKeyHashes(ctx context.Context) ([]string, error) {
	ctx = ensureModelCtx(ctx)
	nowISO := m.now().UTC().Format("2006-01-02T15:04:05.000") + "Z"
	rows, err := m.db.QueryContext(ctx, m.bind(`
		SELECT api_keys.key_hash
		FROM `+m.table("api_keys")+` api_keys
		INNER JOIN `+m.table("system_accounts")+` system_accounts
			ON system_accounts.id = api_keys.system_account_id
			AND system_accounts.status = 'active'
		INNER JOIN `+m.table("route_strategies")+` route_strategies
			ON route_strategies.id = api_keys.route_strategy_id
			AND route_strategies.system_account_id = api_keys.system_account_id
			AND route_strategies.status = 'active'
		WHERE api_keys.status = 'active'
			AND (api_keys.expires_at IS NULL OR api_keys.expires_at > ?)
	`), nowISO)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hashes := []string{}
	for rows.Next() {
		var keyHash string
		if err := rows.Scan(&keyHash); err != nil {
			return nil, err
		}
		if keyHash == "" {
			continue
		}
		hashes = append(hashes, keyHash)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hashes, nil
}

// ReadGatewayRuntimeByKeyHash（GatewayAPIKeyPrewarmer 的第二个方法）实现在
// sqlruntime.go：与 ReadGatewayRuntime 共享 readGatewayRuntimeForAPIKey 尾段。
