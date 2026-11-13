package main

// BUG-0363 终审 M1（gateway 主生产用量 writer 装配溢出 spool）装配行为回
// 归：队满 + spool 可写时 Enqueue 不丢（Persist 成功路径），溢出文件落
// gateway 实例子目录（<DATA_DIR>/usage-record-overflow-spool/gateway/），
// 内容携带稳定 id/createdAt（jobs usagespooldrain.parseSpoolRecord 前置校
// 验兼容）。测试形态对齐 jobs 侧 t207_probe_usage_durable_test.go 与共享包
// wb_overflow_pgfastfail_test.go。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

// usageWriterSpoolTestStore 是 ShardStore 最小替身：writer 不 Start 时写臂
// 不会被调用。
type usageWriterSpoolTestStore struct{}

func (usageWriterSpoolTestStore) WriteBatch(_ usagewriter.Ctx, _ usagewriter.WritePlan) (int, error) {
	return 0, nil
}

// TestGatewayUsageOverflowSpoolDirectoryDerivation 锁定目录派生契约：DATA_DIR
// 下固定名（与 jobs 侧 workerUsageOverflowSpoolDirectory 同根同名），实例子
// 目录为 gateway（与 jobs 的 jobs/ 隔离，drain 全量扫描互不混淆）。
func TestGatewayUsageOverflowSpoolDirectoryDerivation(t *testing.T) {
	got := gatewayUsageOverflowSpoolDirectory(filepath.Join("srv", "data"))
	want := filepath.Join("srv", "data", "usage-record-overflow-spool")
	if got != want {
		t.Fatalf("溢出 spool 根目录派生 = %q, want %q（与 jobs 侧同根同名）", got, want)
	}
	if usageOverflowSpoolInstanceDirectory != "gateway" {
		t.Fatalf("gateway 实例子目录 = %q, want gateway", usageOverflowSpoolInstanceDirectory)
	}
}

// TestGatewayUsageWriterOverflowPersistsToGatewayInstanceDirectory 锁定生产
// 装配同型行为：队满 + spool 可写时记录溢出落盘而不是终态丢弃，文件落
// gateway 实例子目录，格式可被 jobs Drainer 解析回放。
func TestGatewayUsageWriterOverflowPersistsToGatewayInstanceDirectory(t *testing.T) {
	dataDir := t.TempDir()
	// 生产装配同型（chain_usage_writer.go newUsageWriterDeliverer）：
	// NewFileOverflowSpool(DATA_DIR 派生根) + InstanceDirectory=gateway。
	spool := usagewriter.NewFileOverflowSpool(gatewayUsageOverflowSpoolDirectory(dataDir))
	spool.InstanceDirectory = usageOverflowSpoolInstanceDirectory
	writer := usagewriter.NewWriter(usagewriter.Config{
		QueueMaxItems:   1,
		QueueMaxBytes:   1 << 20,
		BatchSize:       100,
		FlushIntervalMs: 60_000,
	}, usageWriterSpoolTestStore{}, usagewriter.SystemClock{},
		usagewriter.WithOverflowSpool(spool))
	// 不 Start：无 flush 消费，队列保持满（溢出路径状态确定）。
	first := usagewriter.UsageRecordInput{
		SystemAccountID: "sys1",
		TraceID:         "gateway-spool-first",
		TrafficSource:   usagewriter.TrafficSourceGateway,
		Success:         true,
	}
	if err := writer.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := usagewriter.UsageRecordInput{
		SystemAccountID: "sys1",
		TraceID:         "gateway-spool-second",
		TrafficSource:   usagewriter.TrafficSourceGateway,
		Success:         true,
	}
	if err := writer.Enqueue(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	runtime := writer.Runtime()
	if runtime.DroppedOverflowCount != 0 || runtime.DroppedDispatchCount != 0 {
		t.Fatalf("队满 + spool 可写时记录必须落盘而不是丢弃: %+v", runtime)
	}
	if runtime.QueueLength != 1 {
		t.Fatalf("队列长度 = %d, want 1（首条入队，次条溢出落盘）", runtime.QueueLength)
	}
	instanceDirectory := filepath.Join(dataDir, "usage-record-overflow-spool", "gateway")
	entries, err := os.ReadDir(instanceDirectory)
	if err != nil {
		t.Fatalf("溢出文件必须落在 gateway 实例子目录 %s: %v", instanceDirectory, err)
	}
	jsonCount := 0
	var overflowFile string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("溢出落盘不得残留 .tmp 临时文件: %s", entry.Name())
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			jsonCount++
			overflowFile = filepath.Join(instanceDirectory, entry.Name())
		}
	}
	if jsonCount != 1 || overflowFile == "" {
		t.Fatalf("gateway 实例子目录溢出文件数 = %d, want 1", jsonCount)
	}
	// 文件内容与 jobs Drainer 解析语义兼容：落盘记录是 Enqueue 内 normalize
	// 后的（稳定 id/createdAt 必非空），parseSpoolRecord 前置校验恒通过。
	content, err := os.ReadFile(overflowFile)
	if err != nil {
		t.Fatal(err)
	}
	var replay usagewriter.UsageRecordInput
	if err := json.Unmarshal(content, &replay); err != nil {
		t.Fatalf("溢出文件必须可 json.Unmarshal（drain 解析语义）: %v", err)
	}
	if replay.ID == "" || replay.CreatedAt == "" {
		t.Fatalf("溢出文件必须携带稳定 id/createdAt（drain 前置校验）: %+v", replay)
	}
	if replay.TraceID != second.TraceID {
		t.Fatalf("溢出文件必须保留原记录: got traceId=%q, want %q", replay.TraceID, second.TraceID)
	}
}
