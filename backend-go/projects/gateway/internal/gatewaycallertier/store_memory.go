package gatewaycallertier

import (
	"context"
	"sync"
	"time"
)

// Store 是层级状态的原子读改写端口。mutator 收到当前状态（可能为零值/损坏
// 已保守归一）并返回新状态；实现必须保证同 Key 的读-改-写原子性（memory=
// 互斥锁；Redis=WATCH 乐观重试）。blob 为空表示该 Key 无状态（新 Key）。
type Store interface {
	Mutate(ctx context.Context, apiKeyID string, mutator func(current []byte) (updated []byte, err error)) error
}

// MemoryStore 是进程内实现（standalone 单机形态 / 测试）。带惰性过期清扫：
// 读写遇到的过期条目按删除处理（回 T2 保守默认态）。
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
	now     func() time.Time
}

type memoryEntry struct {
	blob       []byte
	expiresAt  time.Time
}

// NewMemoryStore 构造进程内存储。
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{entries: map[string]memoryEntry{}, now: now}
}

// Mutate 实现原子读改写。
func (m *MemoryStore) Mutate(_ context.Context, apiKeyID string, mutator func([]byte) ([]byte, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[apiKeyID]
	if ok && !entry.expiresAt.After(m.now()) {
		delete(m.entries, apiKeyID)
		ok = false
	}
	var current []byte
	if ok {
		current = entry.blob
	}
	updated, err := mutator(current)
	if err != nil {
		return err
	}
	m.entries[apiKeyID] = memoryEntry{blob: updated, expiresAt: m.now().Add(StateTTL)}
	return nil
}

// GetRaw 读取原始状态 blob（TierOf 只读路径；过期/不存在返回 nil, nil）。
func (m *MemoryStore) GetRaw(_ context.Context, apiKeyID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[apiKeyID]
	if !ok || !entry.expiresAt.After(m.now()) {
		if ok {
			delete(m.entries, apiKeyID)
		}
		return nil, nil
	}
	return entry.blob, nil
}

// Snapshot 导出全部未过期状态（观测/测试用，非请求路径）。
func (m *MemoryStore) Snapshot() map[string]State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]State{}
	now := m.now()
	for key, entry := range m.entries {
		if !entry.expiresAt.After(now) {
			continue
		}
		out[key] = unmarshalState(entry.blob, now)
	}
	return out
}
