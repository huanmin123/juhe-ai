package usagespooldrain

// BUG-0227 回归：属主/权限不符的不可读队头文件不得阻塞消费链、不得静默
// （节流 Error + Run 失败轮 Warn），待删水位不得被其永久卡死。

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// logCapture 线程安全收集 slog 文本行（Run 循环在独立 goroutine 写日志）。
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, string(p))
	return len(p), nil
}

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(c, nil))
}

// countEvent 统计含指定 event 的日志行数。
func (c *logCapture) countEvent(event string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, line := range c.lines {
		if strings.Contains(line, "event="+event) {
			count++
		}
	}
	return count
}

// countEventValue 统计含指定 event 且指定键匹配值的日志行数；值匹配兼容
// TextHandler 的引号转义形态（Windows 路径含反斜杠时值被 strconv.Quote）。
func (c *logCapture) countEventValue(event, key, value string) int {
	quoted := strconv.Quote(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, line := range c.lines {
		if !strings.Contains(line, "event="+event) {
			continue
		}
		if strings.Contains(line, key+"="+value) || strings.Contains(line, key+"="+quoted) {
			count++
		}
	}
	return count
}

func (c *logCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

// TestW0227DrainOnceSkipsUnreadableHeadAndRecovers 覆盖契约 1：队头不可读
// 文件记一条 Error（event=usage_record_spool_file_read_failed）后跳过、不
// 隔离、不删除，后续文件照常消费；60s 节流窗口内同文件不重复记录；句柄
// 释放（模拟运维修复属主）后文件自然消费。
func TestW0227DrainOnceSkipsUnreadableHeadAndRecovers(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("独占句柄共享冲突语义仅在本机 Windows 验证")
	}
	directory := t.TempDir()
	locked := gatewaySpoolFile(t, directory, "0001-locked", baseRecordWithCreatedAt("id-locked", "2026-01-02T03:04:05.000Z"))
	gatewaySpoolFile(t, directory, "0002-good", baseRecordWithCreatedAt("id-good", "2026-01-02T03:04:06.000Z"))
	enqueuer := &recordingEnqueuer{}
	logs := &logCapture{}
	drainer := &Drainer{Directory: directory, Enqueuer: enqueuer, Logger: logs.logger()}

	closeHandle := holdFileHandle(t, locked, 0)
	defer func() {
		if closeHandle != nil {
			closeHandle()
			closeHandle = nil
		}
	}()

	// 第一轮：不可读队头跳过，后续文件正常消费；错误日志一条。
	processed, err := drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("第一轮 err = %v, want nil（不可读文件跳过本轮不报错）", err)
	}
	if processed != 1 {
		t.Fatalf("第一轮 processed = %d, want 1（队头不可读不阻塞后续文件）", processed)
	}
	if _, statErr := os.Stat(locked); statErr != nil {
		t.Fatalf("不可读文件必须保留原位: %v", statErr)
	}
	if _, statErr := os.Stat(locked + ".corrupt"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("不可读文件不得隔离为 .corrupt: %v", statErr)
	}
	if got := logs.countEventValue("usage_record_spool_file_read_failed", "file", locked); got != 1 {
		t.Fatalf("read_failed 错误日志 = %d, want 1", got)
	}
	if len(enqueuer.inputs) != 1 || enqueuer.inputs[0].ID != "id-good" {
		t.Fatalf("enqueued = %+v", enqueuer.inputs)
	}

	// 第二轮（60s 节流窗口内）：同文件同错误不重复记录。
	processed, err = drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮 err = %v", err)
	}
	if processed != 0 {
		t.Fatalf("第二轮 processed = %d, want 0（仅剩不可读文件）", processed)
	}
	if got := logs.countEventValue("usage_record_spool_file_read_failed", "file", locked); got != 1 {
		t.Fatalf("节流窗口内重复记录 read_failed: %d", got)
	}

	// 句柄释放（模拟运维修复属主）后自然消费，积压清空。
	closeHandle()
	processed, err = drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("修复后 err = %v", err)
	}
	if processed != 1 {
		t.Fatalf("修复后 processed = %d, want 1", processed)
	}
	if _, statErr := os.Stat(locked); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("修复后文件未消费: %v", statErr)
	}
	if len(enqueuer.inputs) != 2 {
		t.Fatalf("enqueued = %d, want 2", len(enqueuer.inputs))
	}
}

// TestW0227WatermarkSkipsUnreadableHead 覆盖契约 3：队头不可读时
// OldestPendingCreatedAt 取第一个可读待消费文件的 created_at；可读文件全
// 部消费、仅剩不可读文件时保留最后水位（不清空、不前进）；不可读文件修
// 复消费完后积压清空、水位复位空串。
func TestW0227WatermarkSkipsUnreadableHead(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("独占句柄共享冲突语义仅在本机 Windows 验证")
	}
	directory := t.TempDir()
	locked := gatewaySpoolFile(t, directory, "0001-locked", baseRecordWithCreatedAt("id-locked", "2026-01-02T03:04:05.000Z"))
	gatewaySpoolFile(t, directory, "0002-good", baseRecordWithCreatedAt("id-good", "2026-01-02T03:04:06.000Z"))
	// 入队失败保留文件，使可读文件在轮末仍是待消费状态（水位可观察）。
	enqueuer := &recordingEnqueuer{failOn: map[string]error{"id-good": errors.New("usage writer 已停止，拒绝写入使用记录")}}
	drainer := newTestDrainer(directory, enqueuer)

	closeHandle := holdFileHandle(t, locked, 0)
	defer func() {
		if closeHandle != nil {
			closeHandle()
			closeHandle = nil
		}
	}()

	// 第一轮：不可读队头跳过；水位跳过它取第一个可读待消费文件。
	if _, err := drainer.DrainOnce(context.Background()); err == nil {
		t.Fatal("入队失败必须报错")
	}
	if got := drainer.OldestPendingCreatedAt(); got != "2026-01-02T03:04:06.000Z" {
		t.Fatalf("水位必须跳过不可读队头取第一个可读文件: %q", got)
	}

	// 第二轮：可读文件消费完、仅剩不可读文件，水位保留最后值。
	delete(enqueuer.failOn, "id-good")
	if processed, err := drainer.DrainOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("第二轮 processed=%d err=%v, want 1/nil", processed, err)
	}
	if got := drainer.OldestPendingCreatedAt(); got != "2026-01-02T03:04:06.000Z" {
		t.Fatalf("仅剩不可读文件时水位必须保留最后值: %q", got)
	}

	// 修复属主后消费完不可读文件：积压清空，水位复位。
	closeHandle()
	if processed, err := drainer.DrainOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("修复后 processed=%d err=%v, want 1/nil", processed, err)
	}
	if got := drainer.OldestPendingCreatedAt(); got != "" {
		t.Fatalf("积压清空后水位必须复位: %q", got)
	}
}

// TestW0227RunLogsWarnOnDrainRoundFailure 覆盖契约 2：DrainOnce 返回非
// nil 错误时 Run 打 Warn（event=usage_record_spool_drain_round_failed），
// 不再静默退避；退避后照常重试，文件保留。
func TestW0227RunLogsWarnOnDrainRoundFailure(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &failingEnqueuer{}
	logs := &logCapture{}
	drainer := &Drainer{
		Directory:     directory,
		Enqueuer:      enqueuer,
		Logger:        logs.logger(),
		FlushInterval: 5 * time.Millisecond,
		RetryDelay:    10 * time.Millisecond,
	}
	gatewaySpoolFile(t, directory, "0001-fail", baseRecord("id-fail"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		drainer.Run(ctx)
		close(done)
	}()
	waitForCalls(t, enqueuer, 2)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if got := logs.countEvent("usage_record_spool_drain_round_failed"); got < 1 {
		t.Fatalf("失败轮必须打 Warn（usage_record_spool_drain_round_failed），lines=%v", logs.snapshot())
	}
	if _, statErr := os.Stat(filepath.Join(directory, "gateway-chain", "0001-fail.json")); statErr != nil {
		t.Fatalf("投递失败的文件必须保留: %v", statErr)
	}
}
