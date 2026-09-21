package businessdataset

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func filepathJoinTemp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "dataset")
}

// TestBDRowsIterationFailures 覆盖行迭代中途故障（rows.Err 分支）在导出与
// 导入各目录/读回查询上的 fail-closed 行为。
func TestBDRowsIterationFailures(t *testing.T) {
	cases := []struct {
		name     string
		fragment string
		side     string
	}{
		{"export columns", "information_schema.columns", "export"},
		{"export primary key", "indisprimary", "export"},
		{"export rows", "FROM juhe_business", "export"},
		{"import foreign keys", "pg_constraint", "import"},
		{"import attributes", "pg_attribute", "import"},
		{"import readback", "FROM juhe_business", "import"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			catalog := newBDPGCatalog("juhe_source_db")
			bdBuildWhitelistCatalog(catalog)
			bdSeedSourceRows(catalog)
			catalog.rowsFailFragment = item.fragment
			db := openBDFakePG(catalog)
			defer db.Close()
			if item.side == "export" {
				if _, err := ExportBusinessDataset(context.Background(), db, t.TempDir(), ExportOptions{}); err == nil {
					t.Fatal("行迭代故障必须报错")
				}
				return
			}
			source := openBDFakePG(bdNewSource(t))
			outDir := filepathJoinTemp(t)
			exportReport, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{})
			source.Close()
			if err != nil {
				t.Fatalf("对照导出不应失败: %v", err)
			}
			_ = exportReport
			if _, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{}); err == nil {
				t.Fatal("行迭代故障必须报错")
			}
		})
	}
}

func TestBDNormalizeValueValuerErrorInScan(t *testing.T) {
	catalog := bdNewSource(t)
	catalog.seedRow("groups", map[string]driver.Value{"id": "valuer-err", "name": bdFailingValuer{}, "created_at": "t"})
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ExportBusinessDataset(context.Background(), db, t.TempDir(), ExportOptions{}); err == nil {
		t.Fatal("Valuer 失败必须让导出报错")
	}
}

func TestBDValueKeyUnmarshalableFallback(t *testing.T) {
	if !strings.HasPrefix(bdValueKey(map[string]any{"bad": make(chan int)}), "x:") {
		t.Fatalf("不可序列化嵌套值必须走 %%v 兜底")
	}
}

func TestBDImportMaxQueryFailure(t *testing.T) {
	source := openBDFakePG(bdNewSource(t))
	outDir := filepathJoinTemp(t)
	if _, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{}); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	source.Close()
	catalog := bdNewTarget(t)
	catalog.injectedFails = []string{"SELECT MAX("}
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{}); err == nil {
		t.Fatal("MAX 查询故障必须报运行错误")
	}
}

func TestBDCommitFailureRollsBack(t *testing.T) {
	source := openBDFakePG(bdNewSource(t))
	outDir := filepathJoinTemp(t)
	if _, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{}); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	source.Close()
	catalog := bdNewTarget(t)
	catalog.commitFails = true
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{}); err == nil {
		t.Fatal("提交失败必须报运行错误")
	}
}

func TestBDResolveAttnumsDirect(t *testing.T) {
	attnums := map[string]map[int64]string{"t": {1: "a", 2: "b"}}
	columns, err := bdResolveAttnums(attnums, "t", []int16{2, 1})
	if err != nil || len(columns) != 2 || columns[0] != "b" || columns[1] != "a" {
		t.Fatalf("解析错误: %v %v", columns, err)
	}
	if _, err := bdResolveAttnums(attnums, "t", []any{"junk"}); err == nil {
		t.Fatal("非法列序号必须报错")
	}
	if _, err := bdResolveAttnums(attnums, "t", []int16{9}); err == nil {
		t.Fatal("未知列序号必须报错")
	}
	if _, err := bdResolveAttnums(attnums, "missing", []int16{1}); err == nil {
		t.Fatal("未知表必须报错")
	}
}

func TestBDParamValueAndAttnumTail(t *testing.T) {
	value, err := bdParamValue([]any{json.Number("1"), "x"})
	if err != nil || value != `[1,"x"]` {
		t.Fatalf("嵌套数组必须 canonical 化: %v %v", value, err)
	}
	if got, err := bdAttnumList([]byte("junk")); err == nil || got != nil {
		t.Fatal("非法文本列序号必须报错")
	}
	if bdValueKey(false) != "b:false" {
		t.Fatal("false 键必须稳定")
	}
}

func TestBDBeginTxFailure(t *testing.T) {
	catalog := bdNewSource(t)
	catalog.beginFails = true
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ExportBusinessDataset(context.Background(), db, t.TempDir(), ExportOptions{}); err == nil {
		t.Fatal("开启事务失败必须报错")
	}
	if _, err := ImportBusinessDataset(context.Background(), db, t.TempDir(), ImportOptions{}); err == nil {
		t.Fatal("开启事务失败必须报错")
	}
}

func TestBDExportPreflightQueryFailures(t *testing.T) {
	// QueryRow 路径：故障 Rows 立即报错，覆盖 Row.Scan 错误分支。
	for _, fragment := range []string{"SHOW transaction_read_only", "SELECT current_database()"} {
		t.Run(fragment, func(t *testing.T) {
			catalog := bdNewSource(t)
			catalog.rowsFailFragment = fragment
			catalog.rowsFailImmediate = true
			db := openBDFakePG(catalog)
			defer db.Close()
			if _, err := ExportBusinessDataset(context.Background(), db, t.TempDir(), ExportOptions{}); err == nil {
				t.Fatalf("导出前置查询故障必须报错: %s", fragment)
			}
		})
	}
	catalog := bdNewSource(t)
	catalog.commitFails = true
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ExportBusinessDataset(context.Background(), db, filepathJoinTemp(t), ExportOptions{}); err == nil {
		t.Fatal("提交失败必须报错")
	}
}

// TestBDNoPrimaryKeyRoundTrip 覆盖无主键表按全列排序的导出与读回路径。
func TestBDNoPrimaryKeyRoundTrip(t *testing.T) {
	sourceCatalog := bdNewSource(t)
	sourceCatalog.tables["system_settings"].primaryKey = nil
	source := openBDFakePG(sourceCatalog)
	outDir := filepathJoinTemp(t)
	exportReport, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{})
	source.Close()
	if err != nil || !exportReport.Ready() {
		t.Fatalf("导出失败: %v %+v", err, exportReport)
	}
	targetCatalog := bdNewTarget(t)
	targetCatalog.tables["system_settings"].primaryKey = nil
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	importReport, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{})
	if err != nil || !importReport.Ready() {
		t.Fatalf("导入失败: %v %+v", err, importReport)
	}
}

// TestBDImportFKFromOutsideWhitelist 覆盖白名单外表发起的外键边（忽略）。
func TestBDImportFKFromOutsideWhitelist(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.addTable("external_integration_sources", []bdFakeColumn{{name: "id", dataType: "text", nullable: false}}, []string{"id"})
	catalog.addForeignKey("external_integration_sources", "external_fk", []string{"id"}, "resource_authorizations", []string{"id"})
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("白名单外表发起的外键必须被忽略: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("白名单外表发起的外键不得阻断: %+v", report)
	}
}

// TestBDImportAllColumnsDropped 覆盖全部源列被放行（无公共列可插）的路径。
func TestBDImportAllColumnsDropped(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.tables["system_teams"].columns = []bdFakeColumn{{name: "future_col", dataType: "text", nullable: true}}
	catalog.tables["system_teams"].primaryKey = nil
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{
		AllowedMissingColumns: []string{"system_teams.id", "system_teams.name", "system_teams.created_at"},
	})
	if err != nil {
		t.Fatalf("全列放行导入属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("有行但无公共列必须在校验阶段阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "无公共列") {
		t.Fatalf("blocker 必须说明无公共列: %+v", report.Blockers)
	}
}
