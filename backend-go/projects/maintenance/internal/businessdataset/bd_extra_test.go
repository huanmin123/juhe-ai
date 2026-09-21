package businessdataset

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func mathNaN() float64 { return math.NaN() }

func TestBDValueKeyComplexFallback(t *testing.T) {
	if !strings.HasPrefix(bdValueKey(map[string]any{"a": 1}), "j:") {
		t.Fatal("嵌套对象必须走 canonical JSON 兜底")
	}
	if !strings.HasPrefix(bdValueKey([]any{json.Number("1")}), "j:") {
		t.Fatal("嵌套数组必须走 canonical JSON 兜底")
	}
}

func TestBDCanonicalRowJSONRejectsUnmarshalable(t *testing.T) {
	if _, err := bdCanonicalRowJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("不可序列化值必须报错")
	}
	digest := newBDDigest()
	if err := digest.writeRow(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("writeRow 必须传播渲染错误")
	}
}

func TestBDWriteManifestFileFailures(t *testing.T) {
	invalid := contracts.BusinessDatasetManifest{FormatVersion: "bad"}
	if _, err := bdWriteManifestFile(t.TempDir(), invalid); err == nil {
		t.Fatal("无效 manifest 必须拒绝写出")
	}
	valid := contracts.BusinessDatasetManifest{
		FormatVersion:  contracts.BusinessDatasetManifestFormatVersion,
		Scope:          contracts.BusinessDatasetManifestScope,
		Producer:       "t",
		SourceIdentity: "s",
		TargetIdentity: "d",
		CapturedAt:     "2026-09-21T00:00:00Z",
	}
	// 目录不存在时写出失败。
	if _, err := bdWriteManifestFile(filepath.Join(t.TempDir(), "missing"), valid); err == nil {
		t.Fatal("目录缺失必须报错")
	}
}

func TestBDProjectedRowsEqualMatrix(t *testing.T) {
	public := []string{"id", "name"}
	source := []map[string]any{
		{"id": "a", "name": "x", "extra": "dropped"},
		{"id": "b", "name": "y", "extra": "dropped"},
	}
	readback := []map[string]any{
		{"id": "b", "name": "y"},
		{"id": "a", "name": "x"},
	}
	if !bdProjectedRowsEqual([]string{"id"}, public, source, readback) {
		t.Fatal("同构投影必须相等（与顺序无关）")
	}
	if bdProjectedRowsEqual(nil, public, source, readback) {
		t.Fatal("无主键必须返回 false")
	}
	if bdProjectedRowsEqual([]string{"id"}, []string{"name"}, source, readback) {
		t.Fatal("主键不在公共列必须返回 false")
	}
	duplicate := append(append([]map[string]any(nil), source...), map[string]any{"id": "a", "name": "dup"})
	if bdProjectedRowsEqual([]string{"id"}, public, duplicate, readback) {
		t.Fatal("重复主键必须返回 false")
	}
	missing := append(append([]map[string]any(nil), readback...), map[string]any{"id": "c", "name": "z"})
	if bdProjectedRowsEqual([]string{"id"}, public, source, missing) {
		t.Fatal("读回多行必须返回 false")
	}
	drifted := []map[string]any{{"id": "b", "name": "y"}, {"id": "a", "name": "DRIFT"}}
	if bdProjectedRowsEqual([]string{"id"}, public, source, drifted) {
		t.Fatal("字段漂移必须返回 false")
	}
	orphan := []map[string]any{{"id": "b", "name": "y"}, {"id": "zz", "name": "x"}}
	if bdProjectedRowsEqual([]string{"id"}, public, source, orphan) {
		t.Fatal("读回出现未知主键必须返回 false")
	}
}

func TestBDCanonicalIndexAndTopoGuards(t *testing.T) {
	if bdCanonicalIndex("not_a_table") != len(contracts.BusinessDatasetTables) {
		t.Fatal("未知表必须返回越界哨兵")
	}
	order, cycle := bdTopologicalOrder([][2]int{{99, 0}, {-1, 0}, {0, 999}})
	if cycle || len(order) != len(contracts.BusinessDatasetTables) {
		t.Fatalf("越界边必须被忽略: %v cycle=%v", cycle, order)
	}
}

func TestBDSelectAllRejectsUnsafeTable(t *testing.T) {
	if _, err := bdSelectAll(`bad"name`, []string{"c"}, []string{"c"}); err == nil {
		t.Fatal("非法表名必须拒绝")
	}
	if _, err := bdInsert(`bad"name`, []string{"c"}); err == nil {
		t.Fatal("非法表名 INSERT 必须拒绝")
	}
	if _, err := bdSelectAll("t", []string{`bad"col`}, []string{"c"}); err == nil {
		t.Fatal("非法列名必须拒绝")
	}
	if _, err := bdSelectAll("t", []string{"c"}, []string{`bad"col`}); err == nil {
		t.Fatal("非法排序列必须拒绝")
	}
	if _, err := bdInsert("t", []string{`bad"col`}); err == nil {
		t.Fatal("非法 INSERT 列必须拒绝")
	}
}

func TestBDImportIdentityMismatch(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	// manifest.TargetIdentity 是导出默认值；期望另一个名字 → blocker。
	catalog := bdNewTarget(t)
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{ExpectedTargetIdentity: "another-db"})
	if err != nil {
		t.Fatalf("标识不匹配属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("期望标识不匹配必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "目标库标识不匹配") {
		t.Fatalf("blocker 必须说明标识不匹配: %+v", report.Blockers)
	}
	// 期望与目标库实际标识一致 → 放行。
	target2 := openBDFakePG(bdNewTarget(t))
	defer target2.Close()
	report2, err := ImportBusinessDataset(context.Background(), target2, outDir, ImportOptions{ExpectedTargetIdentity: "juhe_target_db"})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if !report2.Ready() || !report2.TargetIdentityMatch {
		t.Fatalf("标识一致必须放行: %+v", report2)
	}
}

func TestBDImportSelfReferenceRowCycle(t *testing.T) {
	catalog := newBDPGCatalog("juhe_source_db")
	bdBuildWhitelistCatalog(catalog)
	bdSeedSourceRows(catalog)
	// 制造 providers 行级环：prov-a 的父是 prov-b，prov-b 的父是 prov-a。
	catalog.tables["providers"].rows = []map[string]driver.Value{
		{"id": "p1", "name": "a", "created_at": "t", "code": "prov-a", "parent_code": "prov-b"},
		{"id": "p2", "name": "b", "created_at": "t", "code": "prov-b", "parent_code": "prov-a"},
	}
	source := openBDFakePG(catalog)
	outDir := filepath.Join(t.TempDir(), "dataset")
	exportReport, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{})
	source.Close()
	if err != nil || !exportReport.Ready() {
		t.Fatalf("导出失败: %v %+v", err, exportReport)
	}
	targetCatalog := bdNewTarget(t)
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	report, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("行级环属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("行级环必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "自引用行存在环") {
		t.Fatalf("blocker 必须说明行级环: %+v", report.Blockers)
	}
	if targetCatalog.commits != 0 {
		t.Fatal("行级环不得提交")
	}
}

func TestBDImportNonIntegerSequenceMax(t *testing.T) {
	// 序列表主键值不是整数：MAX 读回后必须 fail-closed 报运行错误。
	catalog := newBDPGCatalog("juhe_source_db")
	bdBuildWhitelistCatalog(catalog)
	bdSeedSourceRows(catalog)
	catalog.tables["api_keys"].rows = []map[string]driver.Value{
		{"id": "not-an-int", "name": "key", "created_at": "t"},
	}
	source := openBDFakePG(catalog)
	outDir := filepath.Join(t.TempDir(), "dataset")
	exportReport, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{})
	source.Close()
	if err != nil || !exportReport.Ready() {
		t.Fatalf("导出失败: %v %+v", err, exportReport)
	}
	target := openBDFakePG(bdNewTarget(t))
	defer target.Close()
	if _, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{}); err == nil {
		t.Fatal("非整数序列主键必须报运行错误")
	}
}

func TestBDExportCreateFileFails(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "dataset")
	if err := os.MkdirAll(filepath.Join(outDir, "providers.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	db := openBDFakePG(bdNewSource(t))
	defer db.Close()
	if _, err := ExportBusinessDataset(context.Background(), db, outDir, ExportOptions{}); err == nil {
		t.Fatal("数据文件被目录占用必须报错")
	}
}

func TestBDScanRowsEmitErrorPropagates(t *testing.T) {
	db := openBDFakePG(bdNewSource(t))
	defer db.Close()
	query, err := bdSelectAll("groups", []string{"id", "name", "created_at"}, []string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	injected := errBDInjected
	if err := bdScanRows(context.Background(), db, "groups", query, []string{"id", "name", "created_at"}, func(map[string]any) error {
		return injected
	}); err == nil {
		t.Fatal("emit 错误必须传播")
	}
}

func TestBDReadDatasetFileAcceptsCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\r\n{\"a\":2}\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, rows, count, err := bdReadDatasetFile(path)
	if err != nil || count != 2 || len(rows) != 2 {
		t.Fatalf("CRLF 行必须可解析: %v %d", err, count)
	}
}

func TestBDQueryContextOutsideTxRejected(t *testing.T) {
	// fake 的防御：生产实现不会在事务外写入。
	catalog := bdNewTarget(t)
	db := openBDFakePG(catalog)
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("fake 应拒绝无法解析的写入")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestBDImportUnresolvableForeignKeyColumns(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.addForeignKey("groups", "ghost_fk", []string{"id"}, "ghost_table", []string{"id"})
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{}); err == nil {
		t.Fatal("外键列无法解析必须报运行错误")
	}
}

func TestBDExportUnmarshalableCellFailsClosed(t *testing.T) {
	catalog := bdNewSource(t)
	catalog.seedRow("groups", map[string]driver.Value{"id": "nan", "name": mathNaN(), "created_at": "t"})
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ExportBusinessDataset(context.Background(), db, t.TempDir(), ExportOptions{}); err == nil {
		t.Fatal("NaN 单元格必须让导出 fail-closed")
	}
}

func TestBDAttnumListScalarTypes(t *testing.T) {
	for value, want := range map[any]int64{int(3): 3, int32(4): 4, int64(5): 5} {
		got, err := bdAttnumList(value)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Fatalf("标量列序号 %v 解析错误: %v %v", value, got, err)
		}
	}
}
