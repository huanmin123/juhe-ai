package schema

// w12g 波次：对 SeedPostgresDefaults 的语句流做逐条 failExecAfter 注入，
// 覆盖此前只扫 EnsurePostgresSeeds 入口（语句流前段被 schema 语句占据）而
// 漏掉的 seed 段 exec 错误分支；另覆盖 seedEncryptJSON 的不可序列化输入分支
// （seedJSONStringify 错误路径）。SQLite seed 注入臂随 SQLite 存储形态退役
// 删除（PLAN-20261024T051925945Z B4）。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// w12gScriptedSeedRec 构造带查询脚本链的 recorder：让 seed 流程中的存在性
// 检查走到"已存在/需修复"分支，使语句流与真实执行一致。
func w12gScriptedSeedRec() *wmSchemaRecorder {
	rec := &wmSchemaRecorder{}
	for _, groupSeed := range pgSeedGroups {
		if groupSeed.SystemAccountID != "sys_admin" {
			continue
		}
		rec.script(pgSeedAdminDefaultGroupSelect, nil, [][]driver.Value{{groupSeed.ID, groupSeed.Name}})
		rec.script(pgSeedAdminRouteStrategySelect, nil, [][]driver.Value{{defaultRouteStrategyIDForGroup(groupSeed.ID)}})
	}
	return rec
}

func TestW12GSeedPostgresDefaultsExecInjectFailures(t *testing.T) {
	// PG seed 语句流实测 167 条；扫到 200 覆盖全部尾部分支。
	for failAfter := 1; failAfter <= 200; failAfter++ {
		rec := w12gScriptedSeedRec()
		rec.failExecAfter = failAfter
		db := openWMSchemaFakeDB(rec)
		_, err := SeedPostgresDefaults(context.Background(), db, SeedOptions{})
		db.Close()
		if err == nil {
			// 注入点已越过会失败的语句（流末尾的 off-by-one 或耗尽），结束扫描。
			return
		}
		if !strings.Contains(err.Error(), "postgres seed statement") && !strings.Contains(err.Error(), errW9AExecFailed.Error()) && !strings.Contains(err.Error(), "postgres seed seal scheduling preferences fresh install") {
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
