package main

// OAuth 刷新跨进程状态装配（计划
// docs/plans/计划-20261106T024618000Z-网关与jobs跨进程共享状态改造.md §B1/§B2）：
// redis-state 部署为 refresh/keepalive 两个 job 注入共享 Redis 锁，并在 Store
// 的凭据写事务提交后递增账户凭据版本（gateway 运行态账号缓存据此提前重载）。
//
// 键空间固定为 <RedisNamespace>:oauth（与既有失败状态存储同域，键前缀由
// crossprocessstate 的实体名区分），不新增环境变量、不改变无 Redis 部署的
// 既有内存语义（测试夹具与本地最小装配不受影响；生产 gateway/jobs 的 Redis
// 是必填运行态）。

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-platform/crossprocessstate"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// oauthCrossProcessLock 把共享契约适配为 oauthrefresh.RefreshLock（刷新锁
// 实体固定，调用方只传账户 ID）。
type oauthCrossProcessLock struct {
	client *crossprocessstate.Client
}

var _ oauthrefresh.RefreshLock = oauthCrossProcessLock{}

// TryLock implements oauthrefresh.RefreshLock.
func (l oauthCrossProcessLock) TryLock(ctx context.Context, accountID string, ttl time.Duration) (string, bool, error) {
	return l.client.TryLock(ctx, crossprocessstate.EntityOAuthRefreshLock, accountID, ttl)
}

// Unlock implements oauthrefresh.RefreshLock.
func (l oauthCrossProcessLock) Unlock(ctx context.Context, accountID, token string) error {
	return l.client.Unlock(ctx, crossprocessstate.EntityOAuthRefreshLock, accountID, token)
}

// oauthCredentialVersionBumper 把共享契约适配为
// oauthrefresh.CredentialVersionBumper。
type oauthCredentialVersionBumper struct {
	client *crossprocessstate.Client
}

var _ oauthrefresh.CredentialVersionBumper = oauthCredentialVersionBumper{}

// Bump implements oauthrefresh.CredentialVersionBumper.
func (b oauthCredentialVersionBumper) Bump(ctx context.Context, accountID string) error {
	_, err := b.client.BumpVersion(ctx, crossprocessstate.EntityAccountCredentialsVersion, accountID)
	return err
}

// oauthCrossProcessState 构造 OAuth 域的共享状态客户端；未配置 Redis 时返回
// (nil, nil, nil)，调用方保持既有内存/进程内语义。Redis URL 非法时 fail-fast，
// 不静默退回掩盖配置错误。
func (a *workerAssembly) oauthCrossProcessState() (*crossprocessstate.Client, redis.UniversalClient, error) {
	if a.config.RedisURL == "" {
		return nil, nil, nil
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: a.config.RedisURL})
	if err != nil {
		return nil, nil, fmt.Errorf("创建 OAuth 跨进程状态 Redis 客户端失败: %w", err)
	}
	state, err := crossprocessstate.NewRedis(client, rediscfg.DomainNamespace(a.config.RedisNamespace, rediscfg.DomainOAuth))
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return state, client, nil
}
