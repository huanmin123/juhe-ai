// accounts.capability_domain 启动只读形状校验（AI 账户能力域拆分 M1）jobs
// 侧测试：PostgreSQL 方言在 wgCmdKitDB 隔离 PG 测试库的独立
// scratch schema 上验证真实 PG 判定（含默认值 `'chat'::text` 渲染），不触碰
// juhe_business 数据。原 SQLite 方言形状矩阵与 AtStartup 包装臂已随 sqlite
// 存储形态退役删除（生产校验恒走 PG）。
package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// capabilityDomainCorrectColumn 是与 maintenance DDL 同构的正确列声明。
const capabilityDomainCorrectColumn = `capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))`

// TestCapabilityDomainSchemaPostgresArms 在 w12c 覆盖库上以独立 scratch schema
// 验证 PG 方言判定；覆盖库不可用时跳过。
func TestCapabilityDomainSchemaPostgresArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 PG")
	}
	db, pgURL := wgCmdKitDB(t)
	_ = pgURL

	schema := "w12c_capdom_probe"
	// defer db.Close() 先于 t.Cleanup 执行，清理必须走测试体内的显式 DROP。
	defer func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
			t.Errorf("清理 scratch schema 失败: %v", err)
		}
	}()

	scenarios := []struct {
		name    string
		column  string
		wantErr string
	}{
		{"pass", capabilityDomainCorrectColumn, ""},
		{"missing-column", "", "缺列"},
		{"wrong-type", `capability_domain bigint NOT NULL DEFAULT 0`, "期望 text"},
		{"nullable", `capability_domain text`, "期望 NOT NULL"},
		{"wrong-default", `capability_domain text NOT NULL DEFAULT 'video'`, "期望前缀 'chat'"},
		{"missing-check", `capability_domain text NOT NULL DEFAULT 'chat'`, "CHECK"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
				t.Fatalf("重置 scratch schema: %v", err)
			}
			columnSQL := ""
			if scenario.column != "" {
				columnSQL = ",\n\t" + scenario.column
			}
			if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %s; CREATE TABLE %s.accounts (id text PRIMARY KEY%s)`, schema, schema, columnSQL)); err != nil {
				t.Fatalf("建 scratch accounts: %v", err)
			}
			err := checkAccountsCapabilityDomainSchemaPostgres(context.Background(), db, schema)
			if scenario.wantErr == "" {
				if err != nil {
					t.Fatalf("标准形状必须通过: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("形状不符必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErr) {
				t.Fatalf("错误应包含 %q，got: %v", scenario.wantErr, err)
			}
		})
	}
}
