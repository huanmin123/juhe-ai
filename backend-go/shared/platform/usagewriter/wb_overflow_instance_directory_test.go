package usagewriter

// BUG-0363 终审 M1（gateway 装配溢出 spool）回归：FileOverflowSpool 支持
// 实例子目录定制（gateway 装配设 "gateway"，与 jobs 默认 "jobs" 同根隔离，
// drain 消费面扫描全部实例子目录互不混淆）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileOverflowSpoolInstanceDirectoryOverride(t *testing.T) {
	directory := t.TempDir()
	spool := NewFileOverflowSpool(directory)
	spool.InstanceDirectory = "gateway"
	input := gatewayInput("instance-override")
	input.ID = "usage_20260102_s01_1704164645000_instanceoverride"
	normalized, err := NormalizeUsageRecordInput(input, fixedClock("2026-01-02T03:04:05.000Z"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Persist(context.Background(), normalized); err != nil {
		t.Fatalf("溢出落盘失败: %v", err)
	}
	instanceDirectory := filepath.Join(directory, "gateway")
	entries, err := os.ReadDir(instanceDirectory)
	if err != nil {
		t.Fatalf("溢出文件必须落在定制实例子目录 %s: %v", instanceDirectory, err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			jsonFiles++
		}
	}
	if jsonFiles != 1 {
		t.Fatalf("定制实例目录溢出文件数 = %d, want 1", jsonFiles)
	}
	if _, err := os.Stat(filepath.Join(directory, "jobs")); !os.IsNotExist(err) {
		t.Fatalf("定制实例目录时不得创建默认 jobs/ 目录: %v", err)
	}
}
