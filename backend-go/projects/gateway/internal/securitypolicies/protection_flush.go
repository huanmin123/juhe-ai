// 防护次数刷写任务（写后缓存合并面，设计 §4.3.1）：gateway 进程内定时把
// Redis 计数键的未落库增量合并进 juhe_business.security_policies.protection_
// count 列。单轮流程：
//
//	SCAN（有界 COUNT、单轮键数上限）匹配本 namespace 全部计数键
//	  → 逐键 GETDEL 原子取走增量（不丢不重：GETDEL 之后本进程不再读同一增量，
//	    多实例并发刷写只是各自取走各自 SCAN 到的键）
//	  → 增量 > 0 时 UPDATE protection_count = protection_count + delta,
//	    updated_at = updated_at（不碰其他列、不推进 CAS 版本；行不存在丢弃）
//
// 失败语义：SCAN 失败返回错误由调用方记 WARN（下轮整轮重试）；单键 GETDEL /
// UPDATE 失败记 WARN 继续下一个键（GETDEL 已取走的增量随失败丢失——展示用
// 途可接受，绝不倒灌回 Redis 造成重复计数）。任务不挂 owner lease 也安全：
// GETDEL 原子 + UPDATE 求和幂等。
package securitypolicies

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	redis "github.com/redis/go-redis/v9"
)

// protectionFlushScanCount 是 SCAN 单次游标的 COUNT 提示（每次往返建议扫描
// 的键空间槽位数，有界；实际返回键数由 Redis 决定）。
const protectionFlushScanCount = 100

// protectionFlushMaxKeysPerRound 是单轮刷写的键数上限：超过即停止本轮（剩余
// 键带增量留在 Redis，下一轮继续取走——有界防单轮占用过长，不丢增量）。
const protectionFlushMaxKeysPerRound = 500

// pgProtectionCountFlushSQL 是刷写 UPDATE（PG 方言 $n 占位符，flusher 只服
// 务恒 PG 的组合根，与 Store 同一 schema 限定）：只累加 protection_count、
// 显式 updated_at=updated_at 保持版本不推进（策略 CAS 不因计数合并触发
// 409）；行不存在（RowsAffected=0）时增量丢弃——策略已删，计数无落点。
const pgProtectionCountFlushSQL = `UPDATE juhe_business.security_policies
	SET protection_count = protection_count + $1, updated_at = updated_at
	WHERE id = $2`

// protectionFlushDB 是刷写任务所需的 SQL 窄口（*sql.DB 天然满足；测试用
// fake 记录 UPDATE 调用）。
type protectionFlushDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ProtectionFlusher 周期把 Redis 防护计数增量合并进 PG 列。client / db 任一
// 为 nil 时 NewProtectionFlusher 返回 nil（未装配形态，组合根不启动任务）。
type ProtectionFlusher struct {
	client    ProtectionRedisClient
	namespace string
	db        protectionFlushDB
}

// NewProtectionFlusher 构建刷写任务。
func NewProtectionFlusher(client ProtectionRedisClient, namespace string, db protectionFlushDB) *ProtectionFlusher {
	if client == nil || db == nil {
		return nil
	}
	return &ProtectionFlusher{client: client, namespace: namespace, db: db}
}

// FlushOnce 执行单轮刷写，返回本轮成功合并的键数。整体 SCAN 失败返回错误；
// 单键失败仅 WARN 并继续（不计入返回值）。
func (f *ProtectionFlusher) FlushOnce(ctx context.Context) (int, error) {
	if f == nil {
		return 0, nil
	}
	prefix := protectionCountKeyPrefix(f.namespace)
	cursor := uint64(0)
	flushed := 0
	for {
		keys, nextCursor, err := f.client.Scan(ctx, cursor, prefix+"*", protectionFlushScanCount).Result()
		if err != nil {
			return flushed, err
		}
		for _, key := range keys {
			if flushed >= protectionFlushMaxKeysPerRound {
				return flushed, nil
			}
			if f.flushKey(ctx, prefix, key) {
				flushed++
			}
		}
		if nextCursor == 0 {
			return flushed, nil
		}
		cursor = nextCursor
	}
}

// flushKey 取走并合并单个键的增量；返回是否成功合并（键值缺失 / 非 >0 / 行
// 不存在均不算）。GETDEL 原子：取走后即使 UPDATE 失败也不回灌，增量丢失可
// 接受（展示用途）。
func (f *ProtectionFlusher) flushKey(ctx context.Context, prefix, key string) bool {
	policyID := strings.TrimPrefix(key, prefix)
	if policyID == "" || strings.Contains(policyID, ":") {
		// 非本任务键空间形态（无 policyID 段或多段）：不可反解，原样留在
		// Redis（MATCH 前缀内理论上不出现，防御性跳过不刷写）。
		return false
	}
	raw, err := f.client.GetDel(ctx, key).Result()
	if err != nil {
		// redis.Nil = 键不存在（本进程或并发实例已取走）：正常竞态，不算失败。
		if !errors.Is(err, redis.Nil) {
			slog.Warn("安全策略防护次数 GETDEL 失败，增量留待下轮",
				"event", "gateway_security_policy_protection_flush_failed",
				"policyId", policyID,
				"error", err)
		}
		return false
	}
	delta, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if parseErr != nil || delta <= 0 {
		// 损坏值 / 零增量：GETDEL 已清键，直接丢弃（不为展示计数让任务卡住）。
		return false
	}
	result, err := f.db.ExecContext(ctx, pgProtectionCountFlushSQL, delta, policyID)
	if err != nil {
		slog.Warn("安全策略防护次数合并 UPDATE 失败，本轮增量丢弃",
			"event", "gateway_security_policy_protection_flush_failed",
			"policyId", policyID,
			"delta", delta,
			"error", err)
		return false
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		slog.Debug("安全策略防护次数合并目标行不存在，增量丢弃",
			"event", "gateway_security_policy_protection_flush_missing_row",
			"policyId", policyID,
			"delta", delta)
	}
	return true
}
