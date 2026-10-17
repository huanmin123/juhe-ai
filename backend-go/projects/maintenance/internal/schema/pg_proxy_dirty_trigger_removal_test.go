package schema

// pg_proxy_dirty_trigger_removal_test.go 锁定 PLAN-20261017T035500000Z 契约：
// proxy_profiles 的 dirty 触发器扇出为结构性纯 churn（投影管线构建/写入/读链
// 全链路零 proxy 消费，取证见计划），从 schema 移除定义并对存量库幂等清理。
// 文本断言 SQLite 可跑；真实 PG 门禁验证重放后触发器/函数已卸载，且 J3a 回写
// 形态（无值变化 UPDATE proxy_profiles）不再产生 dirty 标脏。门禁模式沿用
// pg_dirty_trigger_bounded_wait_test.go（JUHE_AI_WPLAN_PG_URL 优先，否则读
// shared.env；PG 不可达 t.Skip）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestProxyDirtyTriggerRemovalContract 文本契约：语句块不含 proxy 触发器与
// 两函数定义，且含存量库幂等 DROP 清理语句。
func TestProxyDirtyTriggerRemovalContract(t *testing.T) {
	sqlText, _ := wPlanDirtyTriggerStatement(t)
	for _, removed := range []string{
		"CREATE TRIGGER account_list_availability_proxies",
		"CREATE OR REPLACE FUNCTION account_list_availability_proxy_dirty_trigger",
		"CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_proxy",
	} {
		if strings.Contains(sqlText, removed) {
			t.Fatalf("语句块不应再包含 %s（PLAN-20261017T035500000Z 已移除）", removed)
		}
	}
	for _, cleanup := range []string{
		"DROP TRIGGER IF EXISTS account_list_availability_proxies ON proxy_profiles;",
		"DROP FUNCTION IF EXISTS juhe_business.account_list_availability_proxy_dirty_trigger();",
		"DROP FUNCTION IF EXISTS juhe_business.account_list_availability_mark_dirty_proxy(text, text);",
	} {
		if !strings.Contains(sqlText, cleanup) {
			t.Fatalf("缺少存量库清理语句 %q", cleanup)
		}
	}
}

// TestProxyDirtyTriggerRemovalOnPostgres 真实 PG 门禁：同构重放语句块后，
// 触发器与两函数确认卸载；存在 proxy 行时对既有行做无值变化 UPDATE（J3a 回写
// 形态），其绑定账户的 dirty 行数不变。
func TestProxyDirtyTriggerRemovalOnPostgres(t *testing.T) {
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
	statementSQL, statementSchemaName := wPlanDirtyTriggerStatement(t)
	installSQL := "SET search_path TO " + statementSchemaName + ", public;\n" + statementSQL
	if _, err := db.ExecContext(ctx, installSQL); err != nil {
		t.Fatalf("重放语句块失败: %v", err)
	}

	var triggerCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_trigger
		WHERE tgrelid = to_regclass('juhe_business.proxy_profiles')
		  AND tgname = 'account_list_availability_proxies' AND NOT tgisinternal`).Scan(&triggerCount); err != nil {
		t.Fatalf("查询触发器失败: %v", err)
	}
	if triggerCount != 0 {
		t.Fatalf("proxy dirty 触发器应已卸载，残留 %d 个", triggerCount)
	}
	for name, probe := range map[string]string{
		"proxy_dirty_trigger": `SELECT to_regprocedure('juhe_business.account_list_availability_proxy_dirty_trigger()')`,
		"mark_dirty_proxy":    `SELECT to_regprocedure('juhe_business.account_list_availability_mark_dirty_proxy(text, text)')`,
	} {
		var reg interface{}
		if err := db.QueryRowContext(ctx, probe).Scan(&reg); err != nil {
			t.Fatalf("查询函数 %s 失败: %v", name, err)
		}
		if reg != nil {
			t.Fatalf("函数 %s 应已卸载", name)
		}
	}

	var proxyTable interface{}
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('juhe_business.proxy_profiles')`).Scan(&proxyTable); err != nil || proxyTable == nil {
		t.Skipf("PG gated: dev 库无 proxy_profiles 表（err=%v）", err)
	}
	var proxyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_business.proxy_profiles`).Scan(&proxyCount); err != nil {
		t.Skipf("PG gated: proxy_profiles 不可读 %v", err)
	}
	if proxyCount == 0 {
		t.Log("dev 库无 proxy 行，跳过回写行为断言")
		return
	}
	var proxyID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM juhe_business.proxy_profiles ORDER BY id LIMIT 1`).Scan(&proxyID); err != nil {
		t.Fatal(err)
	}
	dirtyCountForProxy := func() int {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_business.account_list_availability_dirty
			WHERE account_id IN (SELECT id FROM juhe_business.accounts WHERE proxy_profile_id = $1)`, proxyID).Scan(&count); err != nil {
			t.Fatalf("查询 dirty 计数失败: %v", err)
		}
		return count
	}
	before := dirtyCountForProxy()
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles SET last_tested_at = last_tested_at WHERE id = $1`, proxyID); err != nil {
		t.Fatalf("无值变化 UPDATE 失败: %v", err)
	}
	if after := dirtyCountForProxy(); after != before {
		t.Fatalf("J3a 回写不应再标脏：dirty 行数 %d -> %d", before, after)
	}
}
