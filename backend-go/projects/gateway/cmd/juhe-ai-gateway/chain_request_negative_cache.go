package main

// L2 请求负面缓存的进程内运行态（请求级提前耗尽与负面缓存设计 §5/§7）：
// 缓存键 = clientSourceKey + requestFingerprint（会话级最小作用域），值为
// L1 已确认的提前耗尽事实（命中即进入既有统一耗尽出口）。与 IP 级错误熔断
// 运行态同风格：进程内有界 TTL map、单机不共享、零 schema；TTL 固定不续期，
// 容量上限内拒绝新写入并递增 Prometheus 计数。

import (
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

const (
	// requestNegativeCacheTTLms 是 L2 条目的固定 TTL（10 分钟，设计 §7 初值）：
	// 上游行为漂移（模型升级、政策调整）的自愈窗口；命中不续期。
	requestNegativeCacheTTLms = int64(10 * 60 * 1000)
	// requestNegativeCacheMaxEntries 是进程内容量上限（设计 §7：最坏
	// 10000 × ~200B ≈ 2MB 内存）；达上限拒绝新写入并递增容量拒绝计数。
	requestNegativeCacheMaxEntries = 10_000
)

// chainRequestNegativeCache 是有界 TTL 集合：条目只有键与到期时刻（命中语义
// 由调用方解释），到期读删、插入序淘汰不适用——容量语义是"拒绝新写入"而非
// 淘汰旧条目（设计 §7）。
type chainRequestNegativeCache struct {
	clock func() int64

	mu      sync.Mutex
	entries map[string]int64
	order   []string
}

func newChainRequestNegativeCache(nowMs func() int64) *chainRequestNegativeCache {
	return &chainRequestNegativeCache{
		clock:   nowMs,
		entries: map[string]int64{},
	}
}

// get 报告键是否在 TTL 内；到期条目读删（TTL 固定，不续期）。
func (c *chainRequestNegativeCache) get(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiresAt, ok := c.entries[key]
	if !ok {
		return false
	}
	if expiresAt <= c.clock() {
		c.removeLocked(key)
		return false
	}
	return true
}

// set 写入一条 TTL 内的负面缓存事实；返回 false 表示容量已满、写入被拒绝
// （并递增 Prometheus 容量拒绝计数）。已存在的键刷新到期时刻（同键重复触发
// 属同一坏请求事实，续写不放大占用）。
func (c *chainRequestNegativeCache) set(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.entries[key]
	if !exists && len(c.entries) >= requestNegativeCacheMaxEntries {
		gatewayusage.RecordGatewayRequestNegativeCacheCapacityRejectedMetric()
		return false
	}
	if !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = c.clock() + requestNegativeCacheTTLms
	return true
}

// lenForTest 返回当前条目数（测试断言容量行为）。
func (c *chainRequestNegativeCache) lenForTest() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *chainRequestNegativeCache) removeLocked(key string) {
	delete(c.entries, key)
	for index, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:index], c.order[index+1:]...)
			return
		}
	}
}
