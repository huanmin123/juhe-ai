package main

// S13：gateway 401 自动刷新读取 jobs 写入的 OAuth 刷新失败退避。键形状与
// jobs oauthrefresh RedisFailureStateStore 同源（shared/platform/oauthfailure），
// 只读且不清理 jobs 记录；读取失败按进程内节流回退，不阻断刷新。

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/oauthfailure"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// gatewayOAuthRefreshFailureState 适配 chainOAuthRefreshFailureStateReader。
type gatewayOAuthRefreshFailureState struct {
	client    redis.UniversalClient
	namespace string
}

// newGatewayOAuthRefreshFailureState 在未配置 Redis 时返回 nil（保持既有
// 进程内节流）；命名空间与 gateway 其他 OAuth 共享状态同域。
func newGatewayOAuthRefreshFailureState(client redis.UniversalClient, baseNamespace string) chainOAuthRefreshFailureStateReader {
	if client == nil {
		return nil
	}
	return &gatewayOAuthRefreshFailureState{
		client:    client,
		namespace: rediscfg.DomainNamespace(baseNamespace, rediscfg.DomainOAuth),
	}
}

// ActiveBackoff implements chainOAuthRefreshFailureStateReader.
func (r *gatewayOAuthRefreshFailureState) ActiveBackoff(ctx context.Context, accountID, vendor string, nowMillis, configRevision int64) (int64, bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	raw, err := r.client.Get(readCtx, oauthfailure.KeyForVendor(vendor, r.namespace, accountID)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	until, active := oauthfailure.ActiveBackoff(raw, nowMillis, configRevision)
	return until, active, nil
}
