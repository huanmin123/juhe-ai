package gatewayusage

// DD-D7 回归：spool 损坏隔离文件 .corrupt 的保留 TTL。
//
// 事实链：gateway RunReplayOnce / jobs drain 把无法解析的 spool 文件改名
// 隔离为 <原名>.corrupt（改名非复制，原文件消失），gateway scanCapacity
// 把 .corrupt 计入 MaxItems/MaxBytes 容量配额（w13d 断言钉住的有意行为）；
// 修复前全仓无任何 .corrupt 删除点，长期运行中损坏文件慢性侵占配额直至
// Persist 报容量上限、溢出补偿失败丢 usage。修复：.corrupt 排障现场保留
// 7 天（spoolCorruptFileRetention），到期由 scanCapacity 同一清理周期
// 顺带删除；保留期内原样保留、照常计入配额；清理失败只诊断不阻断 scan
// （失败文件仍在磁盘，照常计入配额）。.tmp 既有 1h 清理回归由
// TestW13DSpoolScanCapacityEntries 继续钉住（过期 stale.tmp 清理、新鲜
// b.tmp 计数）。
//
// 清理失败臂的失败注入用打开中的句柄占住目标文件（Windows 语义）；POSIX
// 上打开中的文件不影响 Remove、临时目录内清理失败无法稳定注入（与 w13d
// 登记的「过期 .tmp 清理失败分支不可达」同一先例），非 Windows 跳过。

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDDDSpoolCorruptWithinRetentionKeptAndCounted(t *testing.T) {
	directory := t.TempDir()
	instance := filepath.Join(directory, "inst")
	if err := os.MkdirAll(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(instance, "fresh.corrupt")
	if err := os.WriteFile(fresh, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	aging := filepath.Join(instance, "aging.corrupt")
	if err := os.WriteFile(aging, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 6 天前：仍在 7 天保留期内。
	past := time.UnixMilli(1700000000000 - 6*24*60*60_000)
	if err := os.Chtimes(aging, past, past); err != nil {
		t.Fatal(err)
	}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: directory, InstanceID: "inst", MaxItems: 100, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	capacity, err := spool.scanCapacity()
	if err != nil {
		t.Fatal(err)
	}
	// 新鲜与保留期内的 .corrupt 原样保留，照常计入配额（配额计量不变）。
	if capacity.items != 2 || capacity.bytes != 4 {
		t.Fatalf("items=%d bytes=%d", capacity.items, capacity.bytes)
	}
	for _, path := range []string{fresh, aging} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("保留期内 .corrupt 必须原样保留 %s: %v", path, err)
		}
	}
}

func TestDDDSpoolCorruptExpiredCleanedAndQuotaReclaimed(t *testing.T) {
	directory := t.TempDir()
	instance := filepath.Join(directory, "inst")
	if err := os.MkdirAll(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := filepath.Join(instance, "expired.corrupt")
	if err := os.WriteFile(expired, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 8 天前：超过 7 天保留期。
	past := time.UnixMilli(1700000000000 - 8*24*60*60_000)
	if err := os.Chtimes(expired, past, past); err != nil {
		t.Fatal(err)
	}
	// 对照：新鲜 .json 保留并计数，证明配额只回收过期 .corrupt。
	control := filepath.Join(instance, "control.json")
	if err := os.WriteFile(control, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: directory, InstanceID: "inst", MaxItems: 100, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	capacity, err := spool.scanCapacity()
	if err != nil {
		t.Fatal(err)
	}
	if capacity.items != 1 || capacity.bytes != 2 {
		t.Fatalf("过期 .corrupt 配额未回收: items=%d bytes=%d", capacity.items, capacity.bytes)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("过期 .corrupt 必须被清理: %v", err)
	}
	if _, err := os.Stat(control); err != nil {
		t.Fatalf("对照 .json 必须保留: %v", err)
	}
}

func TestDDDSpoolCorruptCleanupFailureDoesNotBlockScan(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("POSIX 上打开中的文件不影响 Remove，清理失败无法稳定注入（对齐 w13d 不可达语句登记先例）")
	}
	directory := t.TempDir()
	instance := filepath.Join(directory, "inst")
	if err := os.MkdirAll(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := filepath.Join(instance, "expired.corrupt")
	if err := os.WriteFile(expired, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.UnixMilli(1700000000000 - 8*24*60*60_000)
	if err := os.Chtimes(expired, past, past); err != nil {
		t.Fatal(err)
	}
	// 占住句柄让 os.Remove 稳定失败（Windows：文件被另一进程占用）。
	handle, err := os.Open(expired)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	logger := &captureLogger{}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: directory, InstanceID: "inst", MaxItems: 100, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, logger)
	capacity, err := spool.scanCapacity()
	if err != nil {
		t.Fatalf("清理失败不得阻断 scan: %v", err)
	}
	// 失败文件仍在磁盘 → 照常计入配额（配额计量以磁盘事实为准）。
	if capacity.items != 1 || capacity.bytes != 2 {
		t.Fatalf("items=%d bytes=%d", capacity.items, capacity.bytes)
	}
	if _, err := os.Stat(expired); err != nil {
		t.Fatalf("清理失败的 .corrupt 必须保留现场: %v", err)
	}
	if len(logger.warns) != 1 {
		t.Fatalf("清理失败必须记诊断 Warn，warns=%d", len(logger.warns))
	}
}
