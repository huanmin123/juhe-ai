package main

// OAuth 刷新失败状态存储的运行态装配（P0 修复 + T65-③ keepalive 对齐）：
// redis-state 部署注入 oauthrefresh.RedisFailureStateStore（退避状态跨重启与
// 多副本共享），否则保持两个 job 各自的内存版默认（sqlite / 无 Redis 运行态
// 部署）。refresh（OpenAI 族）与 keepalive（anthropic/gemini/grok 三族）共用
// 同一 Redis 客户端，key 前缀按族隔离：OpenAI 族保持既有
// openai-oauth-refresh-failure 前缀逐字节不变（存量记录继续可读），keepalive
// 族使用独立 oauth-keepalive-failure 前缀。
// worker_assembly.go 的 wireOAuthFamily 经 oauthFailureStateStores 取装配分支。

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// oauthFailureRedisScripter 把 go-redis 客户端适配为 oauthrefresh.Scripter
// （Lua 失败状态记录 + GET 读取）。GET 的 redis.Nil（键不存在）折叠为空串，
// 由 store 层 raw == "" 分支承担“无状态”语义；其余错误原样上抛，refresh /
// keepalive 任务对读失败的账户保守跳过（failurestate.go Read 契约）。
type oauthFailureRedisScripter struct {
	client *redis.Client
}

// Eval implements oauthrefresh.Scripter.
func (s oauthFailureRedisScripter) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return s.client.Eval(ctx, script, keys, args...).Result()
}

// Get implements oauthrefresh.Scripter.
func (s oauthFailureRedisScripter) Get(ctx context.Context, key string) (string, error) {
	value, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

// oauthFailureStateStores 解析失败状态存储装配分支：JUHE_AI_REDIS_STATE_URL
// 已配置（redis-state 部署，与 circuit/probe 运行态族同一分支信号）→ 共用
// 同一 Redis 客户端的两个族隔离存储（refresh = OpenAI 族既有 key 前缀，
// keepalive = 独立 oauth-keepalive-failure 前缀）+ 客户端 closer；否则返回
// (nil, nil, nil, nil)，refresh / keepalive job 保持各自内存版默认。URL 非法
// 返回错误由装配 fail-fast（不静默退回内存版掩盖配置错误）。
func (a *workerAssembly) oauthFailureStateStores() (refreshStore, keepaliveStore oauthrefresh.FailureStateStore, closer func() error, err error) {
	if a.config.RedisStateURL == "" {
		return nil, nil, nil, nil
	}
	options, err := redis.ParseURL(a.config.RedisStateURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("解析 JUHE_AI_REDIS_STATE_URL 失败: %w", err)
	}
	client := redis.NewClient(options)
	scripter := oauthFailureRedisScripter{client: client}
	return oauthrefresh.NewRedisFailureStateStore(scripter),
		oauthrefresh.NewRedisFailureStateStoreWithKeyPrefix(scripter, oauthrefresh.KeepaliveFailureStateKeyPrefix),
		func() error { return client.Close() },
		nil
}
