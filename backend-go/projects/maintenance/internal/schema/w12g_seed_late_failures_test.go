package schema

// w12g 波次：对 SeedPostgresDefaults / SeedSQLiteDefaults 的语句流做逐条
// failExecAfter 注入，覆盖此前只扫 EnsurePostgresSeeds 入口（语句流前段被
// schema 语句占据）而漏掉的 seed 段 exec 错误分支。
// 另覆盖 seedEncryptJSON 的不可序列化输入分支（seedJSONStringify 错误路径）。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// w12gScriptedSeedRec 构造带查询脚本链的 recorder：让 seed 流程中的存在性
// 检查走到"已存在/需修复"分支，使语句流与真实执行一致。
func w12gScriptedSeedRec(isSQLite bool) *wmSchemaRecorder {
	rec := &wmSchemaRecorder{}
	if isSQLite {
		rec.script(sqSeedGeneratedModelRowsSelect, nil, [][]driver.Value{
			{"provider_model_stale", "glm", "removed-model"},
		})
		w9aScriptRowsFor(rec, sqSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
		rec.script(sqSeedDefaultGroupsSelect, nil, [][]driver.Value{{"grp_gpt_default", "gpt默认分组"}})
		rec.script(sqSeedRouteStrategyExistsSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default"}})
		rec.script(sqSeedChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
		rec.script(sqSeedChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default", "gpt默认路由"}})
		return rec
	}
	w9aScriptRowsFor(rec, pgSeedProfileAccountTypesSelect, len(pgSeedProfiles), []driver.Value{"[]"})
	for _, groupSeed := range pgSeedGroups {
		if groupSeed.SystemAccountID != "sys_admin" || groupSeed.ProviderCode == hybridProviderCode {
			continue
		}
		rec.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{{"grp_" + groupSeed.ProviderCode + "_default", groupSeed.Name + "分组"}})
		rec.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{defaultRouteStrategyIDForGroup("grp_" + groupSeed.ProviderCode + "_default")}})
	}
	rec.script(pgSeedAdminChatKeyDefaultGroupSelect, nil, [][]driver.Value{{"grp_gpt_default"}})
	rec.script(pgSeedAdminChatKeyRouteSelect, nil, [][]driver.Value{{"route_strategy_grp_gpt_default", "gpt默认路由"}})
	return rec
}

func TestW12GSeedPostgresDefaultsExecInjectFailures(t *testing.T) {
	// PG seed 语句流实测 167 条；扫到 200 覆盖全部尾部分支。
	for failAfter := 1; failAfter <= 200; failAfter++ {
		rec := w12gScriptedSeedRec(false)
		rec.failExecAfter = failAfter
		db := openWMSchemaFakeDB(rec)
		_, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{})
		db.Close()
		if err == nil {
			// 注入点已越过会失败的语句（流末尾的 off-by-one 或耗尽），结束扫描。
			return
		}
		if !strings.Contains(err.Error(), "postgres seed statement") && !strings.Contains(err.Error(), errW9AExecFailed.Error()) {
			t.Fatalf("failAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

func TestW12GSeedSQLiteDefaultsExecInjectFailures(t *testing.T) {
	// SQLite seed 语句流实测 270 条；此前只扫 200，尾段分支未覆盖。
	for failAfter := 1; failAfter <= 300; failAfter++ {
		rec := w12gScriptedSeedRec(true)
		rec.failExecAfter = failAfter
		db := openWMSchemaFakeDB(rec)
		_, err := SeedSQLiteDefaults(context.Background(), db, SeedOptions{})
		db.Close()
		if err == nil {
			// 注入点已越过会失败的语句（流末尾的 off-by-one 或耗尽），结束扫描。
			return
		}
		if !strings.Contains(err.Error(), "sqlite seed statement") && !strings.Contains(err.Error(), errW9AExecFailed.Error()) {
			t.Fatalf("failAfter=%d 错误异常: %v", failAfter, err)
		}
	}
}

func TestW12GSeedEncryptJSONRejectsUnserializableValue(t *testing.T) {
	_, err := seedEncryptJSON("w12g-secret", map[string]any{"bad": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "seed encode json") {
		t.Fatalf("不可序列化值必须报错: %v", err)
	}
}

// TestW12GSeedSQLiteRowsIterateErrors 用 fake 驱动的迭代错误注入覆盖
// seedSQLiteDisableStaleGeneratedModels / seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys
// 的 rows.Err() 分支。
func TestW12GSeedSQLiteRowsIterateErrors(t *testing.T) {
	ctx := context.Background()

	// stale generated models 迭代中断。
	recStale := &wmSchemaRecorder{}
	recStale.scriptNextError(sqSeedGeneratedModelRowsSelect, [][]driver.Value{
		{"provider_model_w12g", "glm", "w12g-model"},
	})
	dbStale := openWMSchemaFakeDB(recStale)
	defer dbStale.Close()
	err := seedSQLiteDisableStaleGeneratedModels(ctx, dbStale, func(string, ...any) error { return nil },
		"2026-09-17T00:00:00.000Z", map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "sqlite seed iterate generated model rows") {
		t.Fatalf("stale 模型迭代错误必须被包装: %v", err)
	}

	// 默认分组列表迭代中断。
	recGroups := &wmSchemaRecorder{}
	recGroups.scriptNextError(sqSeedDefaultGroupsSelect, [][]driver.Value{
		{"grp_gpt_default", "gpt默认分组"},
	})
	dbGroups := openWMSchemaFakeDB(recGroups)
	defer dbGroups.Close()
	err = seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, dbGroups, func(string, ...any) error { return nil },
		SeedOptions{}, "2026-09-17T00:00:00.000Z")
	if err == nil || !strings.Contains(err.Error(), "sqlite seed iterate default groups") {
		t.Fatalf("默认分组迭代错误必须被包装: %v", err)
	}
}
