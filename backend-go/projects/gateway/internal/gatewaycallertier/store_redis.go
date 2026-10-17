package gatewaycallertier

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore 是 performance 模式的跨进程实现。依架构总览「易失运行态边界」：
// Redis 不可用时错误向上返回，由调用方按保守缺省处理（该请求按 T2 参与
// 调度并计指标）——不做运行时回退 memory；memory 是启动时的部署形态选择。
//
// 原子性经 WATCH 乐观重试：mutator 在事务外基于 GET 结果计算，TxPipelined
// 提交前 key 未变才生效；冲突重试上限内失败则返回 Redis 错误（保守缺省兜底）。
// 状态为单 key JSON blob，滑动 TTL（StateTTL）。
type RedisStore struct {
	client    *redis.Client
	keyPrefix string
	maxRetries int
	now       func() time.Time
}

// NewRedisStore 构造 Redis 存储。keyPrefix 形如 "juhe-ai:caller-tiers:"，
// 最终 key = prefix + apiKeyID。
func NewRedisStore(client *redis.Client, keyPrefix string, now func() time.Time) *RedisStore {
	if now == nil {
		now = time.Now
	}
	return &RedisStore{client: client, keyPrefix: keyPrefix, maxRetries: 5, now: now}
}

// Mutate 实现乐观锁读改写。
func (r *RedisStore) Mutate(ctx context.Context, apiKeyID string, mutator func([]byte) ([]byte, error)) error {
	key := r.keyPrefix + apiKeyID
	var lastErr error
	for attempt := 0; attempt < r.maxRetries; attempt++ {
		err := r.client.Watch(ctx, func(tx *redis.Tx) error {
			current, err := tx.Get(ctx, key).Bytes()
			if err != nil && err != redis.Nil {
				return err
			}
			updated, err := mutator(current)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, updated, StateTTL)
				return nil
			})
			return err
		}, key)
		if err == nil {
			return nil
		}
		if err == redis.TxFailedErr {
			lastErr = err
			continue
		}
		return err
	}
	return lastErr
}

// GetRaw 读取原始状态 blob（观测/测试用；redis.Nil 返回 nil, nil）。
func (r *RedisStore) GetRaw(ctx context.Context, apiKeyID string) ([]byte, error) {
	blob, err := r.client.Get(ctx, r.keyPrefix+apiKeyID).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return blob, nil
}
