// Package upstreamlimit 提供按上游 base_url origin 分组的并发桶：同一上游
// 的在途探针请求不超过配置上限，不同上游之间并行（2026-11-22 用户裁决：
// 探测总并发不压，但同一家上游必须限流防止触发限频/风控；对话探针是秒级
// 长请求，同源 60 条在途是持续负载而非突发风暴）。
//
// jobs 进程构造一个 Limiter 实例，由 accountprobe.Service（探针族槽位层）
// 与 accounthealth J1 IO worker（对话模型探针）共享，使「同一上游 ≤ limit
// 并发」在这两族通道间成为进程级事实，而不是各自计数叠加。连接测试通道
// （connectivityhealth）不共用该实例：它在 runner 层每轮自建同 limit 桶、
// 在探测预算之外排队（等桶不占单账户硬顶），同 origin 的瞬时在途上限为
// 两层各自的 limit（详见各通道装配处注释）。
package upstreamlimit

import (
	"net/url"
	"strings"
	"sync"
)

// Limiter 按上游 origin 提供同源并发桶（get-or-create，mutex 保护）。
// 零值不可用，必须经 NewLimiter 构造；nil *Limiter 由调用方按「不限流」
// 处理（gateway 侧构造与测试形态）。
type Limiter struct {
	mu    sync.Mutex
	limit int
	sems  map[string]chan struct{}
}

// NewLimiter 构建同源并发桶集合；limit 必须 > 0（装配错误直接 panic，
// 不允许静默退化为零容量桶）。
func NewLimiter(limit int) *Limiter {
	if limit <= 0 {
		panic("upstreamlimit: limit 必须 > 0")
	}
	return &Limiter{limit: limit, sems: make(map[string]chan struct{})}
}

// Bucket 返回 origin 的并发桶；同一 origin 恒返回同一 chan。
func (l *Limiter) Bucket(origin string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	sem, ok := l.sems[origin]
	if !ok {
		sem = make(chan struct{}, l.limit)
		l.sems[origin] = sem
	}
	return sem
}

// Origin 返回同上游限流的分组键：base_url 的 scheme://host:port。
// 解析失败回落原始字符串（同一 base_url 恒映射同一键）；空值共享一个
// 限流桶，按保守处理不放大瞬时压力。
func Origin(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return trimmed
	}
	return parsed.Scheme + "://" + parsed.Host
}
