package main

// OAuth 刷新失败状态存储的运行态装配（P0 修复）：redis-state 部署注入
// oauthrefresh.RedisFailureStateStore（退避状态跨重启与多副本共享），否则
// 保持 refresh job 的内存版默认（sqlite / 无 Redis 运行态部署）。
// worker_assembly.go 的 wireOAuthFamily 经 oauthFailureStateStore 取装配分支。

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// oauthFailureRedisScripter 把 go-redis 客户端适配为 oauthrefresh.Scripter
// （Lua 失败状态记录 + GET 读取）。GET 的 redis.Nil（键不存在）折叠为空串，
// 由 store 层 raw == "" 分支承担“无状态”语义；其余错误原样上抛，refresh
// 任务对读失败的账户保守跳过（failurestate.go Read 契约）。
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

// oauthFailureStateStore 解析失败状态存储装配分支：JUHE_AI_REDIS_STATE_URL
// 已配置（redis-state 部署，与 circuit/probe 运行态族同一分支信号）→ Redis
// 版 + 客户端 closer；否则返回 (nil, nil, nil)，refresh job 保持内存版默认。
// URL 非法返回错误由装配 fail-fast（不静默退回内存版掩盖配置错误）。
func (a *workerAssembly) oauthFailureStateStore() (oauthrefresh.FailureStateStore, func() error, error) {
	if a.config.RedisStateURL == "" {
		return nil, nil, nil
	}
	options, err := redis.ParseURL(a.config.RedisStateURL)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 JUHE_AI_REDIS_STATE_URL 失败: %w", err)
	}
	client := redis.NewClient(options)
	return oauthrefresh.NewRedisFailureStateStore(oauthFailureRedisScripter{client: client}),
		func() error { return client.Close() },
		nil
}
