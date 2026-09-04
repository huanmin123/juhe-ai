// Package taskruns 是 J-INF 基建：jobs 运行记录（background_task_runs）与
// 共享租约（background_job_leases）的 Go 实现。状态机、CAS 谓词和对账 SQL
// 与 Node backend/src/storage/background-task-runs.repository.ts、
// scheduled-job-lease.repository.ts 逐字段对齐；SQLite 用于测试闭环，
// PostgreSQL 为生产语义（juhe_stats schema，text 时间戳与 Node 冻结 DDL 一致）。
package taskruns

import (
	"fmt"
	"sync"
	"time"
)

// Clock 注入时间源；测试用 FakeClock 保证可回放。
type Clock interface {
	Now() time.Time
}

// SystemClock 使用真实时间。
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// FakeClock 是测试用的手动推进时钟（并发安全：心跳协程与主 goroutine
// 会同时读/推进）。
type FakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{current: start.UTC()}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// RFC3339 毫秒 UTC 文本，等价 Node `new Date().toISOString()`。
func FormatInstant(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// NowIso 返回当前时刻的 RFC3339 毫秒 UTC 文本。
func NowIso(clock Clock) string { return FormatInstant(clock.Now()) }

// ParseInstant 解析 RFC3339 文本；等价 Node requiredRfc3339Instant 的
// “必须带 Z 或数值 offset”约束（time.RFC3339 解析强制带时区）。
func ParseInstant(value, field string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", field, value)
	}
	return t, nil
}

// OptionalInstant 允许空值；非空时必须可解析。
func OptionalInstant(value *string, field string) (*time.Time, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	t, err := ParseInstant(*value, field)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
