package schema

// pg_dirty_trigger_bounded_wait_test.go 锁定 PLAN-20261017T030349000Z 契约：
// 库端 dirty 触发器唯一汇聚函数 account_list_availability_mark_dirty_accounts
// 的 7001001 advisory 取锁有界 2s（问题-0313 护航队治理批次 1-3）——包裹式
// （保存/恢复事务级 lock_timeout，宿主事务其余锁等待零外溢）、无 EXCEPTION
// 捕获（触发变更与 dirty 标脏同生共死，55P03 整体回滚自愈）。文本断言
// SQLite 可跑；真实 PG 门禁验证函数可安装、可执行且锁被占用时等待确实有界
// （~2s 后 55P03 而非无界排队）。门禁模式沿用 w184 系列：JUHE_AI_WPLAN_PG_URL
// 优先，否则读 shared.env 的 JUHE_AI_POSTGRES_URL；PG 不可达时 t.Skip。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func wPlanDirtyTriggerStatement(t *testing.T) (statementSQL string, statementSchemaName string) {
	t.Helper()
	for _, statement := range postgresSchemaStatements {
		if statement.Source == "account-list-projection-pg-dirty-triggers" {
			return statement.SQL, statement.SchemaName
		}
	}
	t.Fatal("schema 语句清单缺少 account-list-projection-pg-dirty-triggers")
	return "", ""
}

// TestDirtyTriggerConvergenceBoundedWaitContract 文本契约：限 2s → 取锁 →
// 恢复 顺序成立、取锁前保存会话级 lock_timeout、函数体无 EXCEPTION。
func TestDirtyTriggerConvergenceBoundedWaitContract(t *testing.T) {
	sqlText, _ := wPlanDirtyTriggerStatement(t)
	const (
		saveStmt    = "v_prev_lock_timeout text := current_setting('lock_timeout', true)"
		capStmt     = "PERFORM set_config('lock_timeout', '2s', true);"
		lockStmt    = "PERFORM pg_advisory_xact_lock(7001001);"
		restoreStmt = "PERFORM set_config('lock_timeout', COALESCE(v_prev_lock_timeout, '0'), true);"
	)
	saveIndex := strings.Index(sqlText, saveStmt)
	capIndex := strings.Index(sqlText, capStmt)
	lockIndex := strings.Index(sqlText, lockStmt)
	restoreIndex := strings.Index(sqlText, restoreStmt)
	if saveIndex < 0 || capIndex < 0 || lockIndex < 0 || restoreIndex < 0 {
		t.Fatalf("包裹语句缺失: save=%d cap=%d lock=%d restore=%d", saveIndex, capIndex, lockIndex, restoreIndex)
	}
	if !(saveIndex < capIndex && capIndex < lockIndex && lockIndex < restoreIndex) {
		t.Fatalf("包裹顺序必须为 保存 -> 限2s -> 取锁 -> 恢复: save=%d cap=%d lock=%d restore=%d", saveIndex, capIndex, lockIndex, restoreIndex)
	}
	if strings.Contains(sqlText, "EXCEPTION") {
		t.Fatal("汇聚块不得捕获异常：55P03 必须中止宿主事务（变更与标脏同生共死）")
	}
}

func wPlanDirtyTriggerGateDSN(t *testing.T) string {
	t.Helper()
	pgtestkit.SkipUnlessExternalAuthorized(t)
	if url := os.Getenv("JUHE_AI_WPLAN_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("PG gated: shared.env 不可读")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			return strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	t.Skip("PG gated: 缺少 JUHE_AI_POSTGRES_URL")
	return ""
}

// TestDirtyTriggerConvergenceBoundedWaitOnPostgres 真实 PG 门禁：幂等安装汇聚
// 函数（CREATE OR REPLACE，与 ensure-schema 同文同路径）→ 函数体可执行（不
// 存在的探测账户 0 行标脏，无数据变更）→ tx1 占住 7001001 后 tx2 经汇聚函数
// 取锁须 ~2s 内以 55P03 失败（旧无界形态会撞 8s ctx 上限以 DeadlineExceeded
// 失败，本测试据此区分有界/无界）。
func TestDirtyTriggerConvergenceBoundedWaitOnPostgres(t *testing.T) {
	dsn := wPlanDirtyTriggerGateDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("PG gated: 驱动打开失败 %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("PG gated: ping 失败 %v", err)
	}
	// 与 EnsurePostgres 同构执行：SET search_path 前缀在同一 simple 协议消息内
	// 对后续语句生效（无参 ExecContext 走 pgx simple 协议，多语句合法）。
	statementSQL, statementSchemaName := wPlanDirtyTriggerStatement(t)
	installSQL := "SET search_path TO " + statementSchemaName + ", public;\n" + statementSQL
	if _, err := db.ExecContext(ctx, installSQL); err != nil {
		t.Fatalf("安装汇聚函数失败: %v", err)
	}
	probe := `SELECT juhe_business.account_list_availability_mark_dirty_accounts(ARRAY['wplan-probe-nonexistent'], 'plan-20261017-bounded-wait-probe')`
	if _, err := db.ExecContext(ctx, probe); err != nil {
		t.Fatalf("汇聚函数执行失败: %v", err)
	}

	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback() }()
	if _, err := tx1.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7001001)`); err != nil {
		t.Fatalf("tx1 占锁失败: %v", err)
	}

	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	started := time.Now()
	_, err = tx2.ExecContext(waitCtx, probe)
	elapsed := time.Since(started)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("占用锁下汇聚函数应以 55P03 快失败，实际 err=%v elapsed=%s", err, elapsed)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("等待应受 2s lock_timeout 约束，实际 %s", elapsed)
	}
}
