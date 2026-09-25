package main

// 统计链路排查修复的组合根回归：共享 pgpool.Registry（B④）、溢出 spool
// 回放装配与 ingestgate 多源水位接线（B①）、writer 观测面（B②/B③）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagespooldrain"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// TestAcquirePoolSharesRegistryEntry 锁定 B④：同 URL 的任务族必须命中同
// 一个共享池条目（refs 引用计数），不再每族一池。
func TestAcquirePoolSharesRegistryEntry(t *testing.T) {
	config := workerConfig{Driver: "postgres", PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 1}
	assembly := newWorkerAssembly(config, nil)
	url := "postgres://wb:secret@127.0.0.1:1/db?sslmode=disable"
	_, err := assembly.acquirePool(url, "task-runs")
	if err != nil {
		t.Fatalf("acquirePool A: %v", err)
	}
	if _, err := assembly.acquirePool(url, "stats-agg"); err != nil {
		t.Fatalf("acquirePool B: %v", err)
	}
	snapshots := assembly.poolRegistry.Stats()
	if len(snapshots) != 1 {
		t.Fatalf("同 URL 必须共享一个池条目，实际 %d 个: %+v", len(snapshots), snapshots)
	}
	if snapshots[0].Refs != 2 {
		t.Fatalf("共享池引用计数 = %d, want 2", snapshots[0].Refs)
	}
	if snapshots[0].Role != workerPoolRole {
		t.Fatalf("共享池 role = %q, want %q", snapshots[0].Role, workerPoolRole)
	}
	if len(assembly.pools) != 2 {
		t.Fatalf("句柄必须逐个登记: %d", len(assembly.pools))
	}
	// 引用计数关闭：单句柄 Close 不关库（另一句柄仍持有）。
	firstHandle := assembly.pools[0]
	if err := firstHandle.Close(); err != nil {
		t.Fatal(err)
	}
	if remaining := assembly.poolRegistry.Stats(); len(remaining) != 1 || remaining[0].Refs != 1 {
		t.Fatalf("单句柄关闭不得关库: %+v", remaining)
	}
	assembly.closeStores()
	if remaining := assembly.poolRegistry.Stats(); len(remaining) != 0 {
		t.Fatalf("closeStores 后共享池必须关闭: %+v", remaining)
	}
}

// TestAcquirePoolDistinctURLsDistinctEntries 保留去重键语义：不同 URL 各自
// 独立条目。
func TestAcquirePoolDistinctURLsDistinctEntries(t *testing.T) {
	config := workerConfig{Driver: "postgres", PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 1}
	assembly := newWorkerAssembly(config, nil)
	t.Cleanup(assembly.closeStores)
	for index, url := range []string{
		"postgres://wb:secret@127.0.0.1:1/db-a?sslmode=disable",
		"postgres://wb:secret@127.0.0.1:1/db-b?sslmode=disable",
	} {
		if _, err := assembly.acquirePool(url, "label"); err != nil {
			t.Fatalf("acquirePool %d: %v", index, err)
		}
	}
	if snapshots := assembly.poolRegistry.Stats(); len(snapshots) != 2 {
		t.Fatalf("不同 URL 必须独立条目，实际 %d 个", len(snapshots))
	}
}

// TestWorkerUsageOverflowSpoolDirectory 锁定溢出 spool 目录派生：stats 库
// 目录下的固定名。
func TestWorkerUsageOverflowSpoolDirectory(t *testing.T) {
	config := workerConfig{StatsSQLitePath: filepath.Join("data", "stats.sqlite3")}
	got := workerUsageOverflowSpoolDirectory(config)
	if want := filepath.Join("data", "usage-record-overflow-spool"); got != want {
		t.Fatalf("溢出 spool 目录 = %q, want %q", got, want)
	}
}

// TestWireUsageWriterFamilyWiresOverflowReplay 锁定 B② 装配：writer 家族
// 接线后溢出回放 drain 就绪、supervisor 组件与 /health 观测键齐备。
func TestWireUsageWriterFamilyWiresOverflowReplay(t *testing.T) {
	root := t.TempDir()
	config := workerConfig{
		Driver:                 "sqlite",
		InstanceID:             "wb-usage",
		Secret:                 "wb-secret",
		BusinessSQLitePath:     filepath.Join(root, "business.sqlite3"),
		StatsSQLitePath:        filepath.Join(root, "stats.sqlite3"),
		UsageCatalogSQLitePath: filepath.Join(root, "usage-catalog.sqlite3"),
		UsageShardRoot:         filepath.Join(root, "usage-shards"),
		UsageShardCount:        4,
		UsageSpoolDirectory:    filepath.Join(root, "usage-record-spool"),
	}
	assembly := newWorkerAssembly(config, nil)
	t.Cleanup(assembly.closeStores)
	if err := assembly.wireUsageWriterFamily(context.Background()); err != nil {
		t.Fatalf("装配 usage-writer 家族失败: %v", err)
	}
	if assembly.writer == nil {
		t.Fatal("writer 必须装配")
	}
	if assembly.usageOverflowReplay == nil {
		t.Fatal("溢出回放 drain 必须接线")
	}
	if assembly.usageOverflowReplay.Directory != workerUsageOverflowSpoolDirectory(config) {
		t.Fatalf("回放目录 = %q, want %q", assembly.usageOverflowReplay.Directory, workerUsageOverflowSpoolDirectory(config))
	}
	componentNames := map[string]bool{}
	for _, component := range assembly.components() {
		componentNames[component.Name] = true
	}
	if !componentNames["usage-record overflow replay"] {
		t.Fatalf("溢出回放必须注册 supervisor 组件: %v", componentNames)
	}
	payload := assembly.statusPayload()
	if payload["workerUsageOverflowReplay"] != true {
		t.Fatalf("statusPayload 必须报告溢出回放: %v", payload["workerUsageOverflowReplay"])
	}
	if _, ok := payload["workerUsageWriterRuntime"].(usagewriter.Runtime); !ok {
		t.Fatalf("statusPayload 必须携带 writer 运行态快照: %T", payload["workerUsageWriterRuntime"])
	}
}

// wbFailingEnqueuer 恒失败入队器：让 drain 保留文件，制造待删水位。
type wbFailingEnqueuer struct{}

func (wbFailingEnqueuer) Enqueue(ctx usagewriter.Ctx, input usagewriter.UsageRecordInput) error {
	return context.Canceled
}

// TestIngestDrainProbeAggregatesSpoolWatermark 锁定 B① 接线：probe 的
// PendingUsageRecordsOldestCreatedAt 取 drain 未确认文件的队头 created_at，
// 门控据此回退统计游标（记录滞留超过安全水位仍可聚合）。
func TestIngestDrainProbeAggregatesSpoolWatermark(t *testing.T) {
	root := t.TempDir()
	config := workerConfig{
		Driver:                 "sqlite",
		InstanceID:             "wb-probe",
		Secret:                 "wb-secret",
		BusinessSQLitePath:     filepath.Join(root, "business.sqlite3"),
		StatsSQLitePath:        filepath.Join(root, "stats.sqlite3"),
		UsageCatalogSQLitePath: filepath.Join(root, "usage-catalog.sqlite3"),
		UsageShardRoot:         filepath.Join(root, "usage-shards"),
		UsageShardCount:        4,
		UsageSpoolDirectory:    filepath.Join(root, "usage-record-spool"),
	}
	assembly := newWorkerAssembly(config, nil)
	t.Cleanup(assembly.closeStores)
	if err := assembly.wireUsageWriterFamily(context.Background()); err != nil {
		t.Fatalf("装配 usage-writer 家族失败: %v", err)
	}
	// gateway 交接 drain 指向含一个滞留文件的 spool 目录；入队恒失败 →
	// 文件保留，水位 = 文件内记录 created_at。
	instanceDirectory := filepath.Join(config.UsageSpoolDirectory, "gateway-chain")
	if err := os.MkdirAll(instanceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	record := `{"id":"usage_wb_1","traceId":"wb","trafficSource":"gateway","systemAccountId":"sys1","success":true,"createdAt":"2026-01-02T03:04:05.000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(instanceDirectory, "0001-a.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	assembly.usageSpoolDrain = &usagespooldrain.Drainer{
		Directory: config.UsageSpoolDirectory,
		Enqueuer:  wbFailingEnqueuer{},
	}
	if _, err := assembly.usageSpoolDrain.DrainOnce(context.Background()); err == nil {
		t.Fatal("入队失败必须报错（文件保留）")
	}

	probe := assembly.ingestDrainProbe()
	status, err := probe(context.Background())
	if err != nil || status == nil {
		t.Fatalf("probe 必须可用: %v %v", status, err)
	}
	if status.PendingUsageRecordsOldestCreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("probe 水位必须并入 drain 待删文件队头: %q", status.PendingUsageRecordsOldestCreatedAt)
	}
	// 门控端到端：滞留记录（created_at 早于 now-15s）必须把安全截止时间
	// 回退到最旧滞留记录前 1ms，游标不得越过。
	safety, err := assembly.ingestDrainSafety(context.Background())
	if err != nil {
		t.Fatalf("门控必须成功: %v", err)
	}
	if safety.SafeCreatedBefore != "2026-01-02T03:04:04.999Z" {
		t.Fatalf("安全截止时间必须回退到滞留记录之前 1ms: %q", safety.SafeCreatedBefore)
	}
}
