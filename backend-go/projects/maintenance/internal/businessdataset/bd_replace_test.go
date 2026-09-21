package businessdataset

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// bdSeedTargetLikeSeededDatabase 把目标库 fake 置为“已 seed 的权威库”状态：
// 一部分种子行与导入数据同主键（内容不同），一部分是种子独有行（导入数据中
// 不存在），复现生产流程“权威建库 + seed 之后导入 40 表数据”的起点。
func bdSeedTargetLikeSeededDatabase(t *testing.T, c *bdPGCatalog) {
	t.Helper()
	c.seedRow("system_accounts", map[string]driver.Value{"id": "sa-1", "name": "seed-root", "created_at": "2025-12-31T00:00:00Z"})
	c.seedRow("providers", map[string]driver.Value{"id": "a-child", "name": "seed-child", "created_at": "2025-12-31T00:00:00Z", "code": "prov-child", "parent_code": "prov-a"})
	c.seedRow("providers", map[string]driver.Value{"id": "z-parent", "name": "seed-parent", "created_at": "2025-12-31T00:00:00Z", "code": "prov-a", "parent_code": nil})
	c.seedRow("groups", map[string]driver.Value{"id": "g-1", "name": "seed-group", "created_at": "2025-12-31T00:00:00Z"})
	c.seedRow("api_keys", map[string]driver.Value{"id": int64(41), "name": "seed-key", "created_at": "2025-12-31T00:00:00Z"})
	// 种子独有行：replace 后必须消失。
	c.seedRow("groups", map[string]driver.Value{"id": "seed-only-group", "name": "seed-only", "created_at": "2025-12-31T00:00:00Z"})
	c.seedRow("system_settings", map[string]driver.Value{"id": "seed-only-setting", "name": "seed-only", "created_at": "2025-12-31T00:00:00Z"})
}

func bdFindRowByID(t *testing.T, c *bdPGCatalog, table, id string) map[string]driver.Value {
	t.Helper()
	for _, row := range c.tables[table].rows {
		if bdRenderCell(row["id"]) == id {
			return row
		}
	}
	t.Fatalf("表 %s 缺少 id=%s 的行", table, id)
	return nil
}

// TestBDImportReplaceExistingRebuildsAllRows 固化 replace=true 的生产切流场景：
// 目标库已 seed（含同主键与种子独有行），导入后行集与源完全一致、读回 digest
// 全部 match、恰好一次提交。
func TestBDImportReplaceExistingRebuildsAllRows(t *testing.T) {
	outDir, manifest := bdExportForTest(t)
	targetCatalog := bdNewTarget(t)
	bdSeedTargetLikeSeededDatabase(t, targetCatalog)
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	report, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{ExpectedTargetIdentity: "juhe_target_db", ReplaceExisting: true})
	if err != nil {
		t.Fatalf("replace 导入失败: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("replace 导入报告未就绪: %+v", report)
	}
	if report.Verification != "match" || !report.Committed || !report.StructuralEmptyVerified {
		t.Fatalf("replace 完成态错误: %+v", report)
	}
	if targetCatalog.commits != 1 || targetCatalog.rollbacks != 0 {
		t.Fatalf("replace 成功必须恰好一次提交且无回滚: commits=%d rollbacks=%d", targetCatalog.commits, targetCatalog.rollbacks)
	}
	manifestRows := map[string]int64{}
	for _, entry := range manifest.Tables {
		manifestRows[entry.Name] = entry.Rows
	}
	for name, table := range targetCatalog.tables {
		if int64(len(table.rows)) != manifestRows[name] {
			t.Fatalf("表 %s 导入后行数 %d != manifest %d", name, len(table.rows), manifestRows[name])
		}
	}
	// 同主键行的内容以导入数据为准，不再是种子内容。
	if got := bdFindRowByID(t, targetCatalog, "system_accounts", "sa-1")["name"]; got != "root" {
		t.Fatalf("system_accounts sa-1 必须以导入数据为准: %v", got)
	}
	if got := bdFindRowByID(t, targetCatalog, "groups", "g-1")["name"]; got != "group" {
		t.Fatalf("groups g-1 必须以导入数据为准: %v", got)
	}
	if got := bdFindRowByID(t, targetCatalog, "api_keys", "41")["name"]; got != "key" {
		t.Fatalf("api_keys 41 必须以导入数据为准: %v", got)
	}
	// 种子独有行必须被整体替换删除。
	for _, table := range []string{"groups", "system_settings"} {
		for _, row := range targetCatalog.tables[table].rows {
			if strings.HasPrefix(bdRenderCell(row["id"]), "seed-only") {
				t.Fatalf("表 %s 的种子独有行 %v 必须被替换删除", table, row["id"])
			}
		}
	}
	for _, stat := range report.Tables {
		if !stat.DigestMatch || stat.Status != "match" {
			t.Fatalf("表 %s 读回必须 match: %+v", stat.Name, stat)
		}
	}
}

// TestBDImportReplaceDeleteOrderReverseTopological 断言 replace=true 的 DELETE
// 严格按 FK 拓扑序的逆序执行（子先父后），且结构-only 3 表不被 DELETE。
func TestBDImportReplaceDeleteOrderReverseTopological(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	targetCatalog := bdNewTarget(t)
	bdSeedTargetLikeSeededDatabase(t, targetCatalog)
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	report, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{ReplaceExisting: true})
	if err != nil {
		t.Fatalf("replace 导入失败: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("replace 导入报告未就绪: %+v", report)
	}
	expected := make([]string, 0, len(report.TableOrder))
	for index := len(report.TableOrder) - 1; index >= 0; index-- {
		name := report.TableOrder[index]
		if contracts.IsBusinessDatasetStructuralOnlyTable(name) {
			continue
		}
		expected = append(expected, name)
	}
	wantDataTables := len(contracts.BusinessDatasetTables) - len(contracts.BusinessDatasetStructuralOnlyTables)
	if len(expected) != wantDataTables {
		t.Fatalf("期望 DELETE 表数 %d != 数据表数 %d", len(expected), wantDataTables)
	}
	if strings.Join(targetCatalog.deleteLog, ",") != strings.Join(expected, ",") {
		t.Fatalf("DELETE 必须按拓扑逆序（子先父后）执行:\n got=%v\nwant=%v", targetCatalog.deleteLog, expected)
	}
	position := map[string]int{}
	for index, name := range targetCatalog.deleteLog {
		position[name] = index
	}
	for _, edge := range []struct{ child, parent string }{
		{"accounts", "system_accounts"},
		{"accounts", "providers"},
		{"provider_protocol_profiles", "protocols"},
		{"provider_protocol_profile_families", "provider_protocol_profiles"},
		{"group_accounts", "groups"},
		{"resource_authorization_sources", "resource_authorizations"},
	} {
		if position[edge.child] >= position[edge.parent] {
			t.Fatalf("DELETE 顺序必须子先父后: %s(%d) 必须先于 %s(%d)", edge.child, position[edge.child], edge.parent, position[edge.parent])
		}
	}
	for _, name := range contracts.BusinessDatasetStructuralOnlyTables {
		for _, deleted := range targetCatalog.deleteLog {
			if deleted == name {
				t.Fatalf("结构-only 表 %s 不得被 DELETE", name)
			}
		}
	}
}

// TestBDImportWithoutReplaceConflictsRollBack 固化 replace=false 的 fail-closed
// 默认：目标已有同主键现存行时 INSERT 冲突报错并整体回滚，绝不静默覆盖。
func TestBDImportWithoutReplaceConflictsRollBack(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	targetCatalog := bdNewTarget(t)
	targetCatalog.seedRow("system_accounts", map[string]driver.Value{"id": "sa-1", "name": "seed-root", "created_at": "2025-12-31T00:00:00Z"})
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	report, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{})
	if err == nil {
		t.Fatalf("同主键现存行必须让导入报错: %+v", report)
	}
	if !strings.Contains(err.Error(), "system_accounts") || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("错误必须说明 system_accounts 主键冲突: %v", err)
	}
	if targetCatalog.commits != 0 {
		t.Fatalf("冲突必须整体回滚: commits=%d", targetCatalog.commits)
	}
	if targetCatalog.rollbacks != 1 {
		t.Fatalf("冲突必须恰好回滚一次: rollbacks=%d", targetCatalog.rollbacks)
	}
}

// TestBDImportReplaceDeleteFailureRollsBack 覆盖 replace=true 的 DELETE 故障
// 分支：任一清空失败即运行错误并整体回滚。
func TestBDImportReplaceDeleteFailureRollsBack(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	targetCatalog := bdNewTarget(t)
	targetCatalog.injectedFails = []string{"DELETE FROM juhe_business"}
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	report, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{ReplaceExisting: true})
	if err == nil {
		t.Fatalf("DELETE 故障必须返回运行错误: %+v", report)
	}
	if !strings.Contains(err.Error(), "清空表") {
		t.Fatalf("错误必须说明清空失败: %v", err)
	}
	if targetCatalog.commits != 0 {
		t.Fatalf("DELETE 故障必须整体回滚: commits=%d", targetCatalog.commits)
	}
}
