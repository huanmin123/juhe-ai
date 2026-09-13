package schema

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

// 本文件驱动 seedPostgresDefaults 的“对象已存在”分支（admin 默认分组/路由
// 策略/API Key、chat API Key、外部集成测试 token），锁定幂等跳过语义。

func TestWMSeedPostgresDefaultsWrapperRunsOnInjectedDB(t *testing.T) {
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	result, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{
		Now:    func() time.Time { return time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC) },
		Secret: "wm-secret",
	})
	if err != nil {
		t.Fatalf("SeedPostgresDefaults: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("StatementCount 必须为正: %d", result.StatementCount)
	}
}

func TestWMSeedPostgresDefaultsSkipsExistingAdminObjects(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	// 每个默认分组：分组存在 → 路由策略存在 → 默认 API Key 存在。
	for _, group := range pgSeedGroups {
		rec.script("FROM \"juhe_business\".\"groups\"", []string{"c0", "c1"}, [][]driver.Value{{group.ID, group.Name}})
		rec.script("route_strategies", []string{"c0"}, [][]driver.Value{{defaultRouteStrategyIDForGroup(group.ID)}})
		rec.script("WHERE route_strategy_id = $1 AND is_default = 1", []string{"c0"}, [][]driver.Value{{defaultAPIKeyIDForRouteStrategy(defaultRouteStrategyIDForGroup(group.ID))}})
	}
	// chat API Key 已存在：整段创建路径跳过。
	rec.script("WHERE system_account_id = 'sys_admin' AND purpose = 'chat'", []string{"c0"}, [][]driver.Value{{"key_chat_existing"}})
	// 外部集成测试 token 已存在：只走受保护 UPDATE。
	rec.script("external_integration_source_tokens", []string{"c0"}, [][]driver.Value{{externalIntegrationTestTokenID}})

	client := &wmSeedCaptureClient{db: db, rec: rec}
	result, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }, Secret: sqliteSeedTestSecret})
	if err != nil {
		t.Fatalf("seedPostgresDefaults（对象已存在分支）: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("语句计数必须为正: %d", result.StatementCount)
	}
	// 已存在的默认 API Key / chat Key 不得再产生 INSERT；路由策略与其分组
	// 绑定是 ON CONFLICT DO NOTHING 的无条件 upsert，不受存在性分支控制。
	joined := rec.execQueries()
	for _, statement := range joined {
		trimmed := strings.TrimSpace(statement)
		if strings.HasPrefix(trimmed, `INSERT INTO "juhe_business"."api_keys"`) {
			t.Fatalf("默认 API Key/chat Key 已存在时不应再插入: %q", statement)
		}
	}
	// 外部集成测试 token 的存在路径必须仍执行受保护 UPDATE。
	foundUpdate := false
	for _, statement := range joined {
		if strings.HasPrefix(strings.TrimSpace(statement), "UPDATE \"juhe_business\".\"external_integration_source_tokens\"") {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Fatal("token 存在路径必须执行受保护 UPDATE")
	}
}
