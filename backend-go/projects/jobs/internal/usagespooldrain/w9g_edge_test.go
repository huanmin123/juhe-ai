package usagespooldrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// TestW9GConfigDefaults 覆盖零值回落默认与显式值直通两条分支。
func TestW9GConfigDefaults(t *testing.T) {
	zero := &Drainer{}
	if got := zero.batchSize(); got != DefaultBatchSize {
		t.Fatalf("zero batchSize = %d, want %d", got, DefaultBatchSize)
	}
	if got := zero.flushInterval(); got != DefaultFlushIntervalMs*time.Millisecond {
		t.Fatalf("zero flushInterval = %v", got)
	}
	if got := zero.retryDelay(); got != DefaultRetryDelay {
		t.Fatalf("zero retryDelay = %v", got)
	}
	if got := zero.shutdownFlushMaxBatches(); got != DefaultShutdownFlushMaxBatches {
		t.Fatalf("zero shutdownFlushMaxBatches = %d", got)
	}
	if zero.logger() == nil {
		t.Fatal("zero logger() = nil, want slog.Default()")
	}

	explicit := &Drainer{
		BatchSize:               7,
		FlushInterval:           3 * time.Second,
		RetryDelay:              9 * time.Minute,
		ShutdownFlushMaxBatches: 5,
		Logger:                  silentLogger(),
	}
	if got := explicit.batchSize(); got != 7 {
		t.Fatalf("explicit batchSize = %d", got)
	}
	if got := explicit.flushInterval(); got != 3*time.Second {
		t.Fatalf("explicit flushInterval = %v", got)
	}
	if got := explicit.retryDelay(); got != 9*time.Minute {
		t.Fatalf("explicit retryDelay = %v", got)
	}
	if got := explicit.shutdownFlushMaxBatches(); got != 5 {
		t.Fatalf("explicit shutdownFlushMaxBatches = %d", got)
	}
	if explicit.logger() != explicit.Logger {
		t.Fatal("explicit logger 未直通")
	}
}

// TestW9GListSpoolFilesRootError：spool 根路径不可枚举且错误不属于
// ErrNotExist（超长文件名组件触发 ERROR_INVALID_NAME）时必须原样上抛。
func TestW9GListSpoolFilesRootError(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("x", 300))
	enqueuer := &recordingEnqueuer{}
	drainer := newTestDrainer(root, enqueuer)
	_, err := drainer.listSpoolFiles(10)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want 非 ErrNotExist 枚举错误", err)
	}
	_, err = drainer.DrainOnce(context.Background())
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DrainOnce err = %v, want 非 ErrNotExist 枚举错误", err)
	}
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("enqueued = %d", len(enqueuer.inputs))
	}
}

// TestW9GListSpoolFilesFilterAndLimit：根级普通文件跳过、实例目录内
// 子目录/.tmp/.corrupt/.json 的取舍、排序与批次截断。
func TestW9GListSpoolFilesFilterAndLimit(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "loose.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	instanceDirectory := filepath.Join(directory, "inst")
	if err := os.MkdirAll(instanceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(instanceDirectory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0003-c.json", "0001-a.json", "0002-b.tmp", "0000-x.corrupt"} {
		if err := os.WriteFile(filepath.Join(instanceDirectory, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	drainer := newTestDrainer(directory, &recordingEnqueuer{})
	files, err := drainer.listSpoolFiles(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || filepath.Base(files[0]) != "0001-a.json" || filepath.Base(files[1]) != "0003-c.json" {
		t.Fatalf("files = %v, want 仅实例目录内两个 .json 且按名排序", files)
	}

	limited, err := drainer.listSpoolFiles(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || filepath.Base(limited[0]) != "0001-a.json" {
		t.Fatalf("limited = %v, want 截断为排序首文件", limited)
	}
}

// TestW9GDrainOnceContextCancelled：列出文件后逐文件检查 ctx，取消即刻
// 返回 (processed, ctx.Err())。
func TestW9GDrainOnceContextCancelled(t *testing.T) {
	directory := t.TempDir()
	gatewaySpoolFile(t, directory, "0001-pending", baseRecord("id-pending"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	processed, err := newTestDrainer(directory, &recordingEnqueuer{}).DrainOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "gateway-chain", "0001-pending.json")); statErr != nil {
		t.Fatalf("取消时文件被改动: %v", statErr)
	}
}

// TestW9GDrainOnceReadFileError：文件被无任何共享位的句柄占用（共享冲突，
// 非 ErrNotExist）时 DrainOnce 终止本轮并上抛错误。
func TestW9GDrainOnceReadFileError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("独占句柄共享冲突语义仅在本机 Windows 验证")
	}
	directory := t.TempDir()
	path := gatewaySpoolFile(t, directory, "0001-locked", baseRecord("id-locked"))

	closeHandle := holdFileHandle(t, path, 0)
	defer closeHandle()

	processed, err := newTestDrainer(directory, &recordingEnqueuer{}).DrainOnce(context.Background())
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want 非 ErrNotExist 读取错误", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
}

// TestW9GDrainOnceRemoveError：文件可读（FILE_SHARE_READ）但无共享删除位，
// 入队成功后 os.Remove 被共享冲突拒绝 → 保留文件、终止本轮。
func TestW9GDrainOnceRemoveError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("共享冲突删除拒绝仅在本机 Windows 验证")
	}
	directory := t.TempDir()
	path := gatewaySpoolFile(t, directory, "0001-undeletable", baseRecord("id-undeletable"))

	closeHandle := holdFileHandle(t, path, windows.FILE_SHARE_READ)
	defer closeHandle()

	processed, err := newTestDrainer(directory, &recordingEnqueuer{}).DrainOnce(context.Background())
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want 非 ErrNotExist 删除错误", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("删除失败后文件必须保留: %v", statErr)
	}
}

// TestW9GDrainOnceConcurrentDeleteSkipped：排在前面的文件入队时并发删除了
// 后续文件（gateway 容量扫描语义），ErrNotExist 跳过不终止本轮。
func TestW9GDrainOnceConcurrentDeleteSkipped(t *testing.T) {
	directory := t.TempDir()
	removedPath := filepath.Join(directory, "gateway-chain", "0002-vanished.json")
	enqueuer := &hookEnqueuer{
		onEnqueue: func(input usagewriter.UsageRecordInput) {
			if input.ID == "id-first" {
				_ = os.Remove(removedPath)
			}
		},
	}
	gatewaySpoolFile(t, directory, "0001-first", baseRecord("id-first"))
	if err := os.WriteFile(removedPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	processed, err := newTestDrainer(directory, enqueuer).DrainOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1（并发删除文件跳过）", processed)
	}
	if len(enqueuer.inputs) != 1 || enqueuer.inputs[0].ID != "id-first" {
		t.Fatalf("enqueued = %+v", enqueuer.inputs)
	}
}

// hookEnqueuer 记录入队调用并在入队时执行钩子副作用。
type hookEnqueuer struct {
	inputs    []usagewriter.UsageRecordInput
	onEnqueue func(input usagewriter.UsageRecordInput)
}

func (e *hookEnqueuer) Enqueue(_ usagewriter.Ctx, input usagewriter.UsageRecordInput) error {
	if e.onEnqueue != nil {
		e.onEnqueue(input)
	}
	e.inputs = append(e.inputs, input)
	return nil
}

// TestW9GDrainShutdownConsumesAllBatches：连续多批都有文件时消费完全部
// 批次配额后收尾返回总数。
func TestW9GDrainShutdownConsumesAllBatches(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &hookEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)
	drainer.ShutdownFlushMaxBatches = 2
	drainer.BatchSize = 1
	gatewaySpoolFile(t, directory, "0001-a", baseRecord("id-a"))
	gatewaySpoolFile(t, directory, "0002-b", baseRecord("id-b"))

	processed := drainer.DrainShutdown()
	if processed != 2 {
		t.Fatalf("processed = %d, want 2（两批各消费一个文件）", processed)
	}
	if len(enqueuer.inputs) != 2 {
		t.Fatalf("enqueued = %d, want 2", len(enqueuer.inputs))
	}
}

// holdFileHandle 以指定共享模式保持文件句柄打开（dwShareMode=0 阻断
// ReadFile；仅 FILE_SHARE_READ 时可读但 DeleteFile 因缺共享删除位失败）。
func holdFileHandle(t *testing.T, path string, shareMode uint32) func() {
	t.Helper()
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ|windows.GENERIC_WRITE, shareMode, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Skipf("打开文件句柄失败（环境限制）: %v", err)
	}
	return func() { _ = windows.CloseHandle(handle) }
}

// TestW9GDrainOnceRenameError：损坏文件隔离目标 .corrupt 已被目录占用时，
// 隔离失败终止本轮。
func TestW9GDrainOnceRenameError(t *testing.T) {
	directory := t.TempDir()
	gatewaySpoolFile(t, directory, "0001-bad", baseRecord("id-bad"))
	// gatewaySpoolFile 写的是合法记录；这里改写为损坏内容。
	corrupt := filepath.Join(directory, "gateway-chain", "0001-bad.json")
	if err := os.WriteFile(corrupt, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(corrupt+".corrupt", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(corrupt + ".corrupt") })

	processed, err := newTestDrainer(directory, &recordingEnqueuer{}).DrainOnce(context.Background())
	if err == nil {
		t.Fatal(".corrupt 被目录占用时期望隔离失败报错")
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, statErr := os.Stat(corrupt); statErr != nil {
		t.Fatalf("隔离失败后原文件必须保留: %v", statErr)
	}
}

// failingEnqueuer 恒定失败并计数，驱动 Run 的固定退避分支。
type failingEnqueuer struct {
	calls int
}

func (e *failingEnqueuer) Enqueue(_ usagewriter.Ctx, _ usagewriter.UsageRecordInput) error {
	e.calls++
	return errors.New("usage writer 已停止，拒绝写入使用记录")
}

func waitForCalls(t *testing.T, enqueuer *failingEnqueuer, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for enqueuer.calls < want {
		select {
		case <-deadline:
			t.Fatalf("等待 %d 次入队尝试超时，当前 %d", want, enqueuer.calls)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestW9GRunRetryThenCancelDuringBackoff：失败后进入固定退避；在退避
// 等待中取消 ctx，Run 走 timer 分支内的停机排空并返回。
func TestW9GRunRetryThenCancelDuringBackoff(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &failingEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)
	drainer.FlushInterval = 5 * time.Millisecond
	drainer.RetryDelay = 10 * time.Second
	gatewaySpoolFile(t, directory, "0001-fail", baseRecord("id-fail"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		drainer.Run(ctx)
		close(done)
	}()
	waitForCalls(t, enqueuer, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("退避中取消后 Run 未返回")
	}
	if _, statErr := os.Stat(filepath.Join(directory, "gateway-chain", "0001-fail.json")); statErr != nil {
		t.Fatalf("投递失败的文件必须保留: %v", statErr)
	}
}

// TestW9GRunRetryTimerFires：退避到期后进入下一轮继续失败，取消后正常
// 停机返回。
func TestW9GRunRetryTimerFires(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &failingEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)
	drainer.FlushInterval = 10 * time.Millisecond
	drainer.RetryDelay = 20 * time.Millisecond
	gatewaySpoolFile(t, directory, "0001-fail", baseRecord("id-fail"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		drainer.Run(ctx)
		close(done)
	}()
	waitForCalls(t, enqueuer, 3)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if enqueuer.calls < 3 {
		t.Fatalf("calls = %d, want >= 3（退避到期后重试）", enqueuer.calls)
	}
}

// TestW9GDrainShutdownAbortsOnError：停机排空首轮即失败时立刻中止并返回
// 已接受数量。
func TestW9GDrainShutdownAbortsOnError(t *testing.T) {
	directory := t.TempDir()
	gatewaySpoolFile(t, directory, "0001-fail", baseRecord("id-fail"))
	processed := newTestDrainer(directory, &failingEnqueuer{}).DrainShutdown()
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "gateway-chain", "0001-fail.json")); statErr != nil {
		t.Fatalf("停机排空失败文件必须保留: %v", statErr)
	}
}

// TestW9GDrainShutdownEmptyBatchTerminates：多批次配额下第二轮为空批次时
// 提前返回，不再空转。
func TestW9GDrainShutdownEmptyBatchTerminates(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &recordingEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)
	drainer.ShutdownFlushMaxBatches = 4
	gatewaySpoolFile(t, directory, "0001-one", baseRecord("id-one"))

	processed := drainer.DrainShutdown()
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if len(enqueuer.inputs) != 1 || enqueuer.inputs[0].ID != "id-one" {
		t.Fatalf("enqueued = %+v", enqueuer.inputs)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "gateway-chain", "0001-one.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("已消费文件未删除: %v", statErr)
	}
}
