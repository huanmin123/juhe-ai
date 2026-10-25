// accounts.capability_domain 启动只读形状校验（AI 账户能力域拆分 M1）测试
// （PostgreSQL 唯一存储形态，PLAN-20261024T051925945Z）：pgtestkit 隔离库 +
// maintenance bootstrap.EnsurePostgres fresh DDL 锁同形（防启动校验与权威
// DDL 漂移）；scratch schema 矩阵覆盖通过/缺列/类型错/可空/默认值错/缺
// CHECK/收窄 CHECK；M2 阶段记录门禁覆盖记录缺失/仅 m1_complete/ruleVersion
// 不符/global_settings 表缺失三个失败臂与已封存放行臂。（原 SQLite 形状矩阵
// 与 SQLite ensure 同形锁随 sqlite 存储形态退役删除，D9。）
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// capabilityDomainCorrectColumn 是与 maintenance DDL 同构的正确列声明。
const capabilityDomainCorrectColumn = `capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))`

// openCapabilityDomainPGDatabase 创建 pgtestkit 隔离测试库并应用
// maintenance bootstrap.EnsurePostgres 生产全量建表（与 accounts
// newFreshPGDatabase 同款 fresh-DDL 契约设施）。
func openCapabilityDomainPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := bootstrap.EnsurePostgres(ctx, db)
		return err
	})
}

// TestCapabilityDomainSchemaMatchesMaintenanceBootstrap 锁定启动校验与
// maintenance bootstrap ensure 的建表形状一致：同一次 EnsurePostgres 产出的
// 业务库必须通过校验（防两侧 DDL 漂移）。
func TestCapabilityDomainSchemaMatchesMaintenanceBootstrap(t *testing.T) {
	db := openCapabilityDomainPGDatabase(t)
	if err := checkAccountsCapabilityDomainSchema(context.Background(), db); err != nil {
		t.Fatalf("bootstrap ensure 产出的业务库必须通过启动校验: %v", err)
	}
}

// TestCapabilityDomainSchemaPostgresArms 在 pgtestkit 隔离库的独立 scratch
// schema 上验证 PG 四值形状判定矩阵（含真实 PG 的默认值渲染 `'chat'::text`
// 与 IN 归一化为 = ANY(ARRAY[...]) 的约束文本）；收窄 CHECK 臂锁定值集形状
// 判定拒绝子集词表。不触碰 juhe_business 数据。
func TestCapabilityDomainSchemaPostgresArms(t *testing.T) {
	db := openCapabilityDomainPGDatabase(t)

	schema := "capdom_probe"

	scenarios := []struct {
		name    string
		column  string
		wantErr string
	}{
		{"pass", capabilityDomainCorrectColumn, ""},
		{"missing-column", "", "缺列"},
		{"missing-table", "", "缺列"},
		{"wrong-type", `capability_domain bigint NOT NULL DEFAULT 0`, "期望 text"},
		{"nullable", `capability_domain text`, "期望 NOT NULL"},
		{"wrong-default", `capability_domain text NOT NULL DEFAULT 'video'`, "期望前缀 'chat'"},
		{"missing-check", `capability_domain text NOT NULL DEFAULT 'chat'`, "CHECK"},
		{"narrowed-check", `capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video'))`, "四值域集"},
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

// TestCapabilityDomainStageRecordGateArms 覆盖 M2 启动阶段门禁三失败臂
// （AI 账户能力域拆分 v7）：记录缺失 / 仅 m1_complete / ruleVersion 不符
// （含 global_settings 表缺失的读取失败臂）都必须 fail-fast，且错误信息携带
// 迁移命令指引；已封存 m2_finalized 放行。
func TestCapabilityDomainStageRecordGateArms(t *testing.T) {
	const migrationCommandNeedle = "--migrate-account-capability-domain"
	seedStageRecord := func(t *testing.T, db *sql.DB, record string) {
		t.Helper()
		if record == "" {
			return
		}
		if _, err := db.Exec(`INSERT INTO juhe_business.global_settings (key, value_json, updated_at) VALUES ($1, $2, '2026-10-12T08:00:00Z')`,
			capdomain.MigrationSettingKey, record); err != nil {
			t.Fatalf("seed stage record: %v", err)
		}
	}
	scenarios := []struct {
		name        string
		record      string
		wantErrText string
	}{
		{"record-missing", "", "不存在"},
		{"m1-complete", `{"stage":"m1_complete","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`, "m1_complete"},
		{"rule-version-mismatch", `{"stage":"m2_finalized","ruleVersion":2,"updatedAt":"2026-10-12T08:00:00Z"}`, "ruleVersion"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			db := openCapabilityDomainPGDatabase(t)
			seedStageRecord(t, db, scenario.record)
			err := checkAccountCapabilityDomainStageRecord(context.Background(), db)
			if err == nil {
				t.Fatalf("阶段门禁失败臂必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErrText) || !strings.Contains(err.Error(), migrationCommandNeedle) {
				t.Fatalf("错误应包含 %q 与迁移命令指引 %q，got: %v", scenario.wantErrText, migrationCommandNeedle, err)
			}
		})
	}
	t.Run("global-settings-table-missing", func(t *testing.T) {
		db := openCapabilityDomainPGDatabase(t)
		if _, err := db.Exec(`DROP TABLE juhe_business.global_settings`); err != nil {
			t.Fatalf("drop global_settings: %v", err)
		}
		err := checkAccountCapabilityDomainStageRecord(context.Background(), db)
		if err == nil || !strings.Contains(err.Error(), "读取失败") || !strings.Contains(err.Error(), migrationCommandNeedle) {
			t.Fatalf("global_settings 缺失必须报读取失败且携带迁移命令指引，got: %v", err)
		}
	})
	t.Run("m2-finalized-passes", func(t *testing.T) {
		db := openCapabilityDomainPGDatabase(t)
		seedStageRecord(t, db, `{"stage":"m2_finalized","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`)
		if err := checkAccountCapabilityDomainStageRecord(context.Background(), db); err != nil {
			t.Fatalf("已封存记录必须放行: %v", err)
		}
	})
}

// TestComposeSystemAPISealsCapabilityDomainStageRecord 验证 compose 夹具路径
// 确实经过全新空库自动封存（AI 账户能力域拆分 v7）：pgtestkit fresh 测试库
// 走 maintenance bootstrap.SeedPostgres，fresh 业务库在组合根内获得
// m2_finalized 阶段记录，启动阶段门禁因此放行（gateway 启动本身不是迁移
// 执行者，封存发生在 bootstrap 种子路径）。
func TestComposeSystemAPISealsCapabilityDomainStageRecord(t *testing.T) {
	tdb := composeTestPostgres(t)
	cfg := composeTestConfigForDSN(t, tdb.DSN)
	store := openComposeOperationStore(t, tdb.DSN)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), mustComposeAuditProducer(t, tdb.DSN), composeTestAuditConfig(t, tdb.DSN), composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	recordJSON, err := capabilityDomainStageRecordJSON(context.Background(), composed.DB)
	if err != nil {
		t.Fatalf("read stage record: %v", err)
	}
	record, err := capdomain.ParseRecord(recordJSON)
	if err != nil {
		t.Fatalf("parse stage record: %v", err)
	}
	if record.Stage != capdomain.StageM2Finalized || record.RuleVersion != capdomain.RuleVersion {
		t.Fatalf("compose 夹具库阶段记录应已封存 m2_finalized，got %+v", record)
	}
}

// capabilityDomainStageRecordJSON 读业务库阶段记录原始 JSON（无记录时报错）。
func capabilityDomainStageRecordJSON(ctx context.Context, db *sql.DB) (string, error) {
	var valueJSON string
	err := db.QueryRowContext(ctx, `SELECT value_json FROM juhe_business.global_settings WHERE key = $1`, capdomain.MigrationSettingKey).Scan(&valueJSON)
	if err != nil {
		return "", fmt.Errorf("read capability domain stage record: %w", err)
	}
	return valueJSON, nil
}
