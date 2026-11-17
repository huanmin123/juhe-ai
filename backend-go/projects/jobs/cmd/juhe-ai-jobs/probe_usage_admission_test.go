package main

// 2026-11-17 生产事故回归（统计聚合停摆 6.5h+）：
//
//	probeUsageWriter.resolveScope 在账户查询出错/账户已删时静默返回，探针
//	用量记录以空 SystemAccountId 入队；PG 写入端（usagewriter/rows.go）对
//	缺失 systemAccountId 的记录整批拒绝 → 队头永久阻塞 → ingestgate 粘性
//	门控使 usage-stats/client-ip-stats/account-quality 全部跳轮。
//
// 修复后语义（本文件断言）：
//   - 账户已删除（sql.ErrNoRows）→ 跳过补记：返回 nil、不产生使用记录；
//   - 查询失败（瞬时）→ 返回错误、不产生使用记录（上游 warn 留痕）；
//   - 作用域解析出空 systemAccountId（防御）→ 返回错误、不产生使用记录；
//   - 正常解析 → 记录以非空 SystemAccountID 入队并最终写入 store。
//
// 空 owner 记录绝不再进入 writer 队列——这是毒批次无法再楔死统计链的前提。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

const probeAdmissionAccountsColumns = 6

var probeAdmissionRegisterOnce sync.Once

func registerProbeAdmissionDriver() {
	probeAdmissionRegisterOnce.Do(func() { sql.Register("probe-admission-scripted", probeAdmissionDriver{}) })
}

// probeAdmissionDriver 按脚本状态返回账户查询结果；分组查询恒 0 行（组作用
// 域可空，与生产语义一致）。
type probeAdmissionDriver struct{}

func (probeAdmissionDriver) Open(string) (driver.Conn, error) { return probeAdmissionConn{}, nil }

type probeAdmissionConn struct{}

func (probeAdmissionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (probeAdmissionConn) Close() error              { return nil }
func (probeAdmissionConn) Begin() (driver.Tx, error) { return nil, errors.New("tx not supported") }

func (probeAdmissionConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "group_accounts") {
		return &probeAdmissionRows{cols: []string{"group_id", "system_account_id", "id", "effective_source_team_id"}}, nil
	}
	switch probeAdmissionMode() {
	case "query-error":
		return nil, errors.New("db unavailable")
	case "account-gone":
		return &probeAdmissionRows{cols: probeAdmissionAccountCols()}, nil // 0 行 → ErrNoRows
	case "empty-owner":
		return &probeAdmissionRows{cols: probeAdmissionAccountCols(), rows: [][]driver.Value{{"", nil, nil, nil, nil, nil}}}, nil
	default: // owner
		return &probeAdmissionRows{cols: probeAdmissionAccountCols(), rows: [][]driver.Value{{"", "sys-probe-owner", nil, nil, "", ""}}}, nil
	}
}

// probeAdmissionState 携带当前用例的脚本状态（同一驱动类型注册一次，测试
// 串行执行、逐用例设置/清理）。
var probeAdmissionState struct {
	sync.Mutex
	mode string
}

func setProbeAdmissionMode(t *testing.T, mode string) {
	t.Helper()
	probeAdmissionState.Lock()
	probeAdmissionState.mode = mode
	probeAdmissionState.Unlock()
	t.Cleanup(func() {
		probeAdmissionState.Lock()
		probeAdmissionState.mode = ""
		probeAdmissionState.Unlock()
	})
}

func probeAdmissionMode() string {
	probeAdmissionState.Lock()
	defer probeAdmissionState.Unlock()
	if probeAdmissionState.mode == "" {
		return "owner"
	}
	return probeAdmissionState.mode
}

func probeAdmissionAccountCols() []string { return make([]string, probeAdmissionAccountsColumns) }

type probeAdmissionRows struct {
	cols []string
	rows [][]driver.Value
	at   int
}

func (r *probeAdmissionRows) Columns() []string { return r.cols }
func (r *probeAdmissionRows) Close() error      { return nil }
func (r *probeAdmissionRows) Next(dest []driver.Value) error {
	if r.at >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.at])
	r.at++
	return nil
}

// probeAdmissionStore 记录成功落库的 flush 批次行数（不落 PG）。
type probeAdmissionStore struct {
	mu    sync.Mutex
	rows  int
	flush int
}

func (s *probeAdmissionStore) WriteBatch(_ usagewriter.Ctx, plan usagewriter.WritePlan) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, shard := range plan.RowsByShard {
		s.rows += len(shard.Rows)
	}
	s.flush++
	return len(plan.RowsByShard), nil
}

func (s *probeAdmissionStore) snapshot() (rows, flushes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows, s.flush
}

// probeAdmissionProbe 组装探针写入器：脚本驱动业务库 + recording store 的
// 真实 writer（Start 后异步 flush，poll 断言落库行数）。
func probeAdmissionProbe(t *testing.T) (*probeUsageWriter, *usagewriter.Writer, *probeAdmissionStore) {
	t.Helper()
	registerProbeAdmissionDriver()
	db, err := sql.Open("probe-admission-scripted", "probe-admission")
	if err != nil {
		t.Fatal(err)
	}
	store := &probeAdmissionStore{}
	writer := usagewriter.NewWriter(usagewriter.Config{
		QueueMaxItems:   100,
		QueueMaxBytes:   1 << 20,
		BatchSize:       1,
		FlushIntervalMs: 10,
	}, store, usagewriter.SystemClock{})
	writer.Start()
	t.Cleanup(func() {
		writer.Close(context.Background())
		_ = db.Close()
	})
	return &probeUsageWriter{
		business: &businessDB{db: db},
		writer:   writer,
		clock:    usagewriter.SystemClock{},
	}, writer, store
}

func probeAdmissionObservation() accounthealth.ProbeUsageObservation {
	return accounthealth.ProbeUsageObservation{
		AccountID:     "acc_probe_1",
		TrafficSource: accounthealth.ProbeTrafficSourceAccountHealthCheck,
		Endpoint:      "chat_completions",
		Model:         "gpt-5",
		Success:       true,
		ObservedAt:    time.Now().UTC(),
	}
}

// waitStoreRows 轮询等待 store 落库行数达到 want（超时即失败）。
func waitStoreRows(t *testing.T, store *probeAdmissionStore, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rows, _ := store.snapshot(); rows >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, flushes := store.snapshot()
	t.Fatalf("store rows = %d (flushes %d), want >= %d", rows, flushes, want)
}

// TestProbeUsageAccountGoneSkipsWithoutEnqueue：账户已删除 → 跳过补记，
// 不报错、不产生使用记录。
func TestProbeUsageAccountGoneSkipsWithoutEnqueue(t *testing.T) {
	probe, _, store := probeAdmissionProbe(t)
	setProbeAdmissionMode(t, "account-gone")
	if err := probe.RecordProbeUsage(context.Background(), probeAdmissionObservation()); err != nil {
		t.Fatalf("account gone must skip silently, got %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if rows, _ := store.snapshot(); rows != 0 {
		t.Fatalf("account gone must not enqueue usage record, store rows=%d", rows)
	}
}

// TestProbeUsageScopeQueryErrorRejectsWithoutEnqueue：瞬时查询错误 → 返回
// 错误（上游 warn），记录不入队（绝不写入空 owner 毒记录）。
func TestProbeUsageScopeQueryErrorRejectsWithoutEnqueue(t *testing.T) {
	probe, _, store := probeAdmissionProbe(t)
	setProbeAdmissionMode(t, "query-error")
	err := probe.RecordProbeUsage(context.Background(), probeAdmissionObservation())
	if err == nil || !strings.Contains(err.Error(), "解析探针用量账户作用域失败") {
		t.Fatalf("scope query error must surface, got %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if rows, _ := store.snapshot(); rows != 0 {
		t.Fatalf("scope failure must not enqueue, store rows=%d", rows)
	}
}

// TestProbeUsageEmptyOwnerRejectsWithoutEnqueue：解析到空 systemAccountId
// （防御口径）→ 返回错误，记录不入队。
func TestProbeUsageEmptyOwnerRejectsWithoutEnqueue(t *testing.T) {
	probe, _, store := probeAdmissionProbe(t)
	setProbeAdmissionMode(t, "empty-owner")
	err := probe.RecordProbeUsage(context.Background(), probeAdmissionObservation())
	if err == nil || !strings.Contains(err.Error(), "缺少 systemAccountId") {
		t.Fatalf("empty owner must be rejected, got %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if rows, _ := store.snapshot(); rows != 0 {
		t.Fatalf("empty owner must not enqueue, store rows=%d", rows)
	}
}

// TestProbeUsageValidScopeEnqueuesRecord：正常作用域 → 记录入队并落库。
func TestProbeUsageValidScopeEnqueuesRecord(t *testing.T) {
	probe, _, store := probeAdmissionProbe(t)
	setProbeAdmissionMode(t, "owner")
	if err := probe.RecordProbeUsage(context.Background(), probeAdmissionObservation()); err != nil {
		t.Fatalf("valid scope must enqueue, got %v", err)
	}
	waitStoreRows(t, store, 1)
}
