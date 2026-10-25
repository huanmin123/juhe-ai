package recordmaintenance

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// mockRunner 记录 RunOnce 调用并按 job.ID 注入失败（Mock 边界：drain 循环
// 的正常/异常路径均可回放、结果稳定）。
type mockRunner struct {
	mu      sync.Mutex
	ran     []retention.RecordMaintenanceJob
	failFor map[string]error
}

func (m *mockRunner) RunOnce(ctx context.Context, job retention.RecordMaintenanceJob) (map[string]any, error) {
	m.mu.Lock()
	m.ran = append(m.ran, job)
	_, failed := m.failFor[job.ID]
	m.mu.Unlock()
	if failed {
		return nil, m.failFor[job.ID]
	}
	return map[string]any{"deletedRows": int64(1)}, nil
}

func (m *mockRunner) ranIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.ran))
	for _, job := range m.ran {
		out = append(out, job.ID)
	}
	return out
}

func (m *mockRunner) setFailure(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failFor == nil {
		m.failFor = map[string]error{}
	}
	if err == nil {
		delete(m.failFor, id)
		return
	}
	m.failFor[id] = err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func pendingCount(t *testing.T, store *Store) int {
	t.Helper()
	jobs, err := store.Dequeue(context.Background(), 1000)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	return len(jobs)
}

