// 防护次数计数（写后缓存，设计 §4.3.1）：请求终态把该请求的原始命中总数
// （TotalHits，不去重）INCRBY 进每个安全策略一个 STRING 计数键（fire-and-
// forget，失败仅 debug 日志，绝不影响响应）；gateway 进程内刷写任务
// （protection_flush.go）每 5 分钟 SCAN + GETDEL 取走增量，合并进
// juhe_business.security_policies.protection_count 列；列表/详情/options 读
// 侧直接投影 PG 列，不再读 Redis（Redis 不可用只延迟入库，不影响管理面读
// 路径）。计数是展示用途，不承诺一致性与准确。
//
// 键布局：`juhe-ai:{<namespace>}:state:security-policy-protection:v1:<policyID>`
// ——namespace 拼装与既有 runtime-state 先例同源（rediscfg.NamespacedKey，
// Cluster hash tag 让同 namespace 键固定同一 slot）；`state:` 段与
// gatewayproxyhealth.RedisRuntimeStateStore 的 `state:<name>:<key>` 布局一致。
//
// Store 保持纯 SQL：Redis 绑定只存在于本文件的 ProtectionCountStore 与
// protection_flush.go 的 ProtectionFlusher（共用 ProtectionRedisClient 接口
// 与键拼装），经 Deps.Protection 注入 routes 层（删除清键）、组合根注入
// gateway 终态写侧与刷写任务；nil 时全部侧面静默降级（INCRBY 无操作、删除
// 不清键、刷写不启动），管理面读路径不受 Redis 可用性影响。
package securitypolicies

import (
	"context"
	"log/slog"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// protectionStateKeyName 是防护计数键的 `state:` 段（版本后缀 v1；计数口径
// 或键布局变化时升版本新键空间，旧键自然废弃——不做在线迁移）。
const protectionStateKeyName = "state:security-policy-protection:v1"

// protectionIncrTimeout 是终态 INCRBY 的硬预算（fire-and-forget 写侧；调用
// 方在独立 goroutine 执行，本预算只约束单次 Redis 往返，超时即放弃本次计
// 数——防护计数是展示用途的运行态增量，丢一次 INCRBY 可接受，绝不影响请求
// 响应）。
const protectionIncrTimeout = 50 * time.Millisecond

// protectionSideOpTimeout 是删除清键等管理面侧面操作的单操作预算（对齐
// runtime-state 3s 操作先例；失败仅日志，不影响业务响应）。
const protectionSideOpTimeout = 3 * time.Second

// ProtectionRedisClient 是防护计数所需的 Redis 命令子集（*redis.Client /
// *redis.UniversalClient 天然满足；测试用 fake）。INCRBY/Del 是写侧与删除
// 清键，Scan/GetDel 是刷写任务的取增量原语（protection_flush.go）。
type ProtectionRedisClient interface {
	IncrBy(ctx context.Context, key string, value int64) *redis.IntCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
	GetDel(ctx context.Context, key string) *redis.StringCmd
}

// ProtectionCountKey 返回策略的防护计数键
// `juhe-ai:{<namespace>}:state:security-policy-protection:v1:<policyID>`。
// policyID 经 SanitizeRedisName 清洗（合法输入逐字节不变，防御性约束键字符
// 集，对齐 runtime-state 键段清洗先例）；namespace 走既有 NamespacedKey 拼
// 装（根前缀不重复、Cluster hash tag 同源）。
func ProtectionCountKey(namespace, policyID string) string {
	return rediscfg.NamespacedKey(protectionStateKeyName+":"+rediscfg.SanitizeRedisName(strings.TrimSpace(policyID)), namespace)
}

// protectionCountKeyPrefix 返回该 namespace 下全部防护计数键的公共前缀（刷
// 写任务 SCAN MATCH 与键→policyID 反解共用同一来源，保证匹配空间与读回空间
// 一致）。
func protectionCountKeyPrefix(namespace string) string {
	return rediscfg.NamespacedKey(protectionStateKeyName+":", namespace)
}

// ProtectionCountStore 是防护计数的 Redis 写侧面（写后缓存）。零值语义：
// nil *ProtectionCountStore（未装配 Redis）全部方法安全返回——Increment 无
// 操作、DeleteKey 无操作。
type ProtectionCountStore struct {
	client    ProtectionRedisClient
	namespace string
}

// NewProtectionCountStore 构建防护计数 Redis 写侧面；client 为 nil（无 Redis
// 客户端，组合测试 / 非 redis 驱动形态）返回 nil，消费方按未装配语义降级。
func NewProtectionCountStore(client ProtectionRedisClient, namespace string) *ProtectionCountStore {
	if client == nil {
		return nil
	}
	return &ProtectionCountStore{client: client, namespace: namespace}
}

// Increment 对策略键 INCRBY total（调用方已保证 total > 0）。失败返回原始错
// 误；调用方（gateway 终态写侧）按事件 gateway_security_policy_hit_incr_failed
// 记录——本方法已带 traceId 日志参数，错误日志在此单点发出（含 traceId 与
// policyId，不含任何内容）。展示用途的运行态增量，失败仅 debug。
func (s *ProtectionCountStore) Increment(ctx context.Context, policyID string, total int64, traceID string) error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, protectionIncrTimeout)
	defer cancel()
	if err := s.client.IncrBy(ctx, ProtectionCountKey(s.namespace, policyID), total).Err(); err != nil {
		slog.Debug("安全策略防护次数 INCRBY 失败（展示用途运行态增量，丢弃本次计数）",
			"event", "gateway_security_policy_hit_incr_failed",
			"traceId", traceID,
			"policyId", policyID,
			"error", err)
		return err
	}
	return nil
}

// DeleteKey 删除策略的防护计数键（Delete 成功后的侧面清理；失败仅日志，
// 残键无 TTL 但策略行已删、刷写 UPDATE 找不到行自然丢弃——语义等同废弃）。
// 返回原始错误供调用方记日志。
func (s *ProtectionCountStore) DeleteKey(ctx context.Context, policyID string) error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, protectionSideOpTimeout)
	defer cancel()
	if err := s.client.Del(ctx, ProtectionCountKey(s.namespace, policyID)).Err(); err != nil {
		return err
	}
	return nil
}
