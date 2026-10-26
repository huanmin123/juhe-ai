package main

// w14k 波次：PG 门控测试。pgtestkit 每测试独立库（PLAN-20261024T051925945Z
// 测试基建唯一存储设施）：prepare 经本模块 bootstrap.EnsurePostgres+
// SeedPostgres 落生产全量 schema+seed，schema snapshot 的只读采集落在真实
// 生产 DDL 上，不再依赖共享覆盖库。配置缺失或 PG 不可达一律 t.Fatalf（存储
// 测试迁移 PG 后即核心回归面，静默跳过等于假绿）。连接串由 pgtestkit 自
// JUHE_AI_TEST_PG_ADMIN_URL 或 .local dev env 解析，内容不进入任何文件、
// 日志或断言。

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestW14KPgGateBootstrapChecks 历史上覆盖 go-runtime-metrics bootstrap 的
// check 模式与退出码/报告一致性；该命令族已随 internal/goruntimemetrics 包
// 删除（退役监控表清退改由 --drop-retired-monitoring-tables 承担，存储链路
// 由 monitoringretirement 包测试覆盖），本用例仅保留 schema snapshot 的 PG
// 门控（见下）。

// TestW14KPgGateSchemaSnapshotSuccess 对 bootstrap.EnsurePostgres+SeedPostgres
// 建好的独立库执行完整只读 schema 快照（REPEATABLE READ READ ONLY 事务 +
// catalog 采集 + stdout JSON）。
//
// 上游缺陷已修复：CollectSnapshot 的 catalog 查询（relations/columns/
// constraints/indexes/functions/triggers/views/partitions/sequences）现全部
// 携带 schemaNames 参数（Node 原件 scripts/operations/
// postgres-schema-snapshot.ts 同形），本用例按原定契约断言 exit 0 与快照
// 元数据，不再 skip。
func TestW14KPgGateSchemaSnapshotSuccess(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := bootstrap.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		if _, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: time.Now}); err != nil {
			return err
		}
		return nil
	})
	t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_TARGET", "test")
	t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL", testDB.DSN)
	t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM", "READ_ONLY")

	var got int
	stdout := wmCaptureStdout(t, func() { got = postgresSchemaSnapshotResult() })
	if got != 0 {
		t.Fatalf("schema snapshot 必须 exit 0: exit %d, stdout %.200s", got, stdout)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(stdout), &snapshot); err != nil {
		t.Fatalf("快照输出必须是 JSON: %v\n%s", err, stdout)
	}
	if snapshot["digest"] == "" || snapshot["schemaVersion"] != float64(1) {
		t.Fatalf("快照元数据不完整: %.80s", stdout)
	}
}
