package gatewaysession

import (
	"sync"
	"time"
)

// 内容指纹谱系判别器（docs/functions/内容指纹会话身份设计.md §4.3）：确认档
// 亲和键的产出门控。判别"本请求的会话上下文是否处于演化中"——恒定长度重放
// （批量脚本、冒烟测试）不建立、不消费亲和；真实会话自第二请求起重发上下文
// 长度必然变化（客户端全量重发、append-only），判别器对增长中会话零拦截。
//
// 进程内 LRU：键 = confirmedHex（已含 systemAccountID + apiKeyID +
// clientProfile 隔离），值 = {lastMessageCount, everGrown, lastSeen}；满员
// 逐出 lastSeen 最旧者，TTL 过期视同首见。判定与更新在互斥锁内单临界区原子
// 完成，并发同键首请求串行化后恰一个产出。本服务是进程本地状态：当前生产
// 形态为国内单机单实例（判别精确）；多实例下判别退化为每实例 best-effort
// （同键 CAS 收敛，无正确性影响，设计 §7.5）。

const (
	// DefaultContentFingerprintLineageCapacity 默认容量（设计 §4.3：10000 条）。
	DefaultContentFingerprintLineageCapacity = 10000
	// DefaultContentFingerprintLineageTTL 默认 TTL（设计 §4.3：24h，与账户
	// 亲和 memory TTL 同量级）。
	DefaultContentFingerprintLineageTTL = 24 * time.Hour
)

// ContentFingerprintLineageConfig 谱系判别器配置；零值字段取默认值。
type ContentFingerprintLineageConfig struct {
	// Capacity 是 LRU 最大记录数；<= 0 取 DefaultContentFingerprintLineageCapacity。
	Capacity int
	// TTL 是记录存活时长；<= 0 取 DefaultContentFingerprintLineageTTL。
	TTL time.Duration
	// Now 注入时钟（测试）；nil 用 time.Now。
	Now func() time.Time
}

type contentFingerprintLineageEntry struct {
	lastMessageCount int
	everGrown        bool
	lastSeen         time.Time
}

// ContentFingerprintLineageService 是内容指纹确认档的谱系判别器。
type ContentFingerprintLineageService struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	entries  map[string]*contentFingerprintLineageEntry
}

// NewContentFingerprintLineageService 构造谱系判别器。
func NewContentFingerprintLineageService(config ContentFingerprintLineageConfig) *ContentFingerprintLineageService {
	capacity := config.Capacity
	if capacity <= 0 {
		capacity = DefaultContentFingerprintLineageCapacity
	}
	ttl := config.TTL
	if ttl <= 0 {
		ttl = DefaultContentFingerprintLineageTTL
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &ContentFingerprintLineageService{
		capacity: capacity,
		ttl:      ttl,
		now:      now,
		entries:  make(map[string]*contentFingerprintLineageEntry),
	}
}

// Record 对确认档指纹执行一次谱系判定与更新（单临界区原子完成，设计 §4.3
// 产出规则），返回本请求是否产出确认档 ConversationKey：
//
//   - 首见（含 TTL 过期后再次出现）→ 产出，建立记录；
//   - 消息数 ≠ 记录值（增长或收缩均为谱系演化证据）→ 产出并更新记录，
//     增长置 everGrown（只置不清）；
//   - 等长且 everGrown（已增长谱系的同长重放：轮内失败重试、重新生成、
//     并发同轮）→ 产出，记录不变；
//   - 等长且未增长 → 不产出且不更新记录（lastSeen 也不刷新：恒定长度重放
//     在记录生命周期内至多首见产出一次）。
//
// confirmedHex 必须已含账户/画像隔离（指纹公式保证）；messageCount 是本请求
// messages 数组元素总数。每次身份解析至多调用一次（调用方经请求级记忆化保
// 证）。
func (s *ContentFingerprintLineageService) Record(confirmedHex string, messageCount int) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[confirmedHex]
	if ok && now.Sub(entry.lastSeen) > s.ttl {
		delete(s.entries, confirmedHex)
		ok = false
	}
	if !ok {
		if len(s.entries) >= s.capacity {
			s.evictOldestLocked()
		}
		s.entries[confirmedHex] = &contentFingerprintLineageEntry{
			lastMessageCount: messageCount,
			everGrown:        false,
			lastSeen:         now,
		}
		return true
	}
	if entry.lastMessageCount != messageCount {
		if messageCount > entry.lastMessageCount {
			entry.everGrown = true
		}
		entry.lastMessageCount = messageCount
		entry.lastSeen = now
		return true
	}
	if entry.everGrown {
		entry.lastSeen = now
		return true
	}
	return false
}

// evictOldestLocked 逐出 lastSeen 最旧的记录。容量 10000 量级下线性扫描的
// 摊还成本可忽略：每次插入至多触发一次逐出。
func (s *ContentFingerprintLineageService) evictOldestLocked() {
	oldestKey := ""
	var oldestSeen time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastSeen.Before(oldestSeen) {
			oldestKey = key
			oldestSeen = entry.lastSeen
			found = true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}
