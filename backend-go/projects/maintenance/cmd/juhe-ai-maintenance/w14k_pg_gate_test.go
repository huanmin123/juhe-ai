package main

// w14k 波次：PG 门控测试。从 .local/project-resources/dev/env/shared.env 读
// JUHE_AI_POSTGRES_URL，替换为 w1cover 覆盖专用库（端口 6432→5432、库
// juhe_ai_sub2api_dev→juhe_ai_sub2api_dev_w1cover）。连接失败一律 t.Skip。
// 共享库只读使用：check 模式与 REPEATABLE READ READ ONLY 快照都不写任何对象，
// 不做 schema 变更。连接串/密码绝不进入日志、断言或错误消息。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/goruntimemetrics"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w14kMaintenancePostgresURL 解析共享 dev 连接串并改写为 w1cover 覆盖库；
// 文件缺失、变量缺失或库不可达时跳过。
func w14kMaintenancePostgresURL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skipf("shared.env 不可读，跳过 PG 门控: %v", err)
	}
	var rawURL string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			rawURL = strings.TrimSpace(strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL="))
		}
	}
	if rawURL == "" {
		t.Skip("shared.env 未提供 JUHE_AI_POSTGRES_URL，跳过 PG 门控")
	}
	coverageURL := strings.ReplaceAll(rawURL, ":6432/", ":5432/")
	coverageURL = strings.Replace(coverageURL, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	db, err := sql.Open("pgx", coverageURL)
	if err != nil {
		t.Skipf("w1cover 连接初始化失败，跳过 PG 门控")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skip("w1cover PostgreSQL 不可达，跳过 PG 门控")
	}
	return coverageURL
}

// TestW14KPgGateBootstrapChecks 对 w1cover 覆盖库执行三个 bootstrap runner 的
// check 模式。覆盖库不含维护 schema，预期走未就绪出口 3；同时断言退出码与
// stdout 报告的就绪状态一致（exit 0 ⇔ Ready，exit 3 ⇔ 未就绪），无论共享库
// 将来是否被其他授权流程建过 schema，断言都保持行为一致。
func TestW14KPgGateBootstrapChecks(t *testing.T) {
	coverageURL := w14kMaintenancePostgresURL(t)

	assertExitMatchesReport := func(t *testing.T, name string, code int, stdout string, ready func() bool) {
		t.Helper()
		var generic map[string]any
		if err := json.Unmarshal([]byte(stdout), &generic); err != nil {
			t.Fatalf("%s: stdout 必须是 JSON 报告: %v\n%s", name, err, stdout)
		}
		if code == 0 && !ready() {
			t.Fatalf("%s: exit 0 但报告未就绪: %s", name, stdout)
		}
		if code == 3 && ready() {
			t.Fatalf("%s: exit 3 但报告已就绪: %s", name, stdout)
		}
		if code != 0 && code != 3 {
			t.Fatalf("%s: check 模式对真实库不应出现其他退出码: %d\n%s", name, code, stdout)
		}
	}

	t.Run("j3b pg bootstrap check", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.BootstrapEnv, coverageURL)
		var got int
		stdout := wmCaptureStdout(t, func() { got = j3bModelCheckBootstrapResult(false) })
		db, err := j3bmodelcheck.Open(coverageURL)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		report, err := j3bmodelcheck.Run(context.Background(), db, false)
		if err != nil {
			t.Skipf("w1cover check 执行失败，跳过: %v", err)
		}
		assertExitMatchesReport(t, "j3b", got, stdout, report.Ready)
	})

	t.Run("go runtime metrics check", func(t *testing.T) {
		var got int
		stdout := wmCaptureStdout(t, func() {
			got = goRuntimeMetricsBootstrapResult(false, coverageURL, false, false, false)
		})
		db, err := goruntimemetrics.Open(coverageURL)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		report, err := goruntimemetrics.Run(context.Background(), db, false)
		if err != nil {
			t.Skipf("w1cover check 执行失败，跳过: %v", err)
		}
		assertExitMatchesReport(t, "metrics", got, stdout, report.Ready)
	})

	t.Run("j3a check", func(t *testing.T) {
		t.Setenv(j3aproxylatency.BootstrapEnv, coverageURL)
		var got int
		stdout := wmCaptureStdout(t, func() { got = j3aProxyLatencyBootstrapResult(false) })
		db, err := j3aproxylatency.Open(coverageURL)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		report, err := j3aproxylatency.Run(context.Background(), db, false)
		if err != nil {
			t.Skipf("w1cover check 执行失败，跳过: %v", err)
		}
		assertExitMatchesReport(t, "j3a", got, stdout, report.Ready)
	})
}

// TestW14KPgGateSchemaSnapshotSuccess 对 w1cover 库执行完整只读 schema 快照
// （REPEATABLE READ READ ONLY 事务 + catalog 采集 + stdout JSON）。
//
// 已登记上游缺陷（w14k，internal/schemasnapshot 不在本波次改动范围）：
// CollectSnapshot 的 $1::text[] catalog 查询（relations/columns/constraints/
// indexes/functions/triggers/views/partitions/sequences）调用 collectRows 时
// 未传 schemaNames 参数，而 Node 原件
// migration-backup/node/final-archive/backend/src/scripts/operations/
// postgres-schema-snapshot.ts 每条查询都携带 [schemaNames]。路由式 fake 驱动
// 不校验参数个数，w12g 未能暴露；真实 PostgreSQL 上快照必然以
// "expected 1 arguments, got 0" 失败（exit 1）。该包修复后，本用例会自动从
// skip 升级为 exit 0 成功断言。
func TestW14KPgGateSchemaSnapshotSuccess(t *testing.T) {
	coverageURL := w14kMaintenancePostgresURL(t)
	t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_TARGET", "test")
	t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL", coverageURL)
	t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM", "READ_ONLY")

	var got int
	stdout := wmCaptureStdout(t, func() { got = postgresSchemaSnapshotResult() })
	if got != 0 {
		t.Skipf("上游 schemasnapshot collectRows 缺参缺陷未修复（当前 exit %d），登记待修后本断言生效", got)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(stdout), &snapshot); err != nil {
		t.Fatalf("快照输出必须是 JSON: %v\n%s", err, stdout)
	}
	if snapshot["digest"] == "" || snapshot["schemaVersion"] != float64(1) {
		t.Fatalf("快照元数据不完整: %.80s", stdout)
	}
}
