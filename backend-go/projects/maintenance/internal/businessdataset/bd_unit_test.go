package businessdataset

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func TestBDOpenRejectsNonPostgresURLs(t *testing.T) {
	db, err := Open("sqlite:///x.db")
	if err == nil {
		db.Close()
		t.Fatal("SQLite URL 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "postgres/postgresql") {
		t.Fatalf("错误必须说明方言要求: %v", err)
	}
	if _, err := Open("postgres://u:p@127.0.0.1:5432/db?application_name=x"); err != nil {
		t.Fatalf("postgres URL 必须接受: %v", err)
	}
	if _, err := Open("   "); err == nil {
		t.Fatal("空白 URL 必须拒绝")
	}
}

func TestBDCanonicalRowJSONSortsKeys(t *testing.T) {
	line, err := bdCanonicalRowJSON(map[string]any{"zeta": "1", "alpha": "2", "mid": nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != `{"alpha":"2","mid":null,"zeta":"1"}` {
		t.Fatalf("键必须字典序输出: %s", line)
	}
}

func TestBDNormalizeValueMatrix(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "null"},
		{"string", "x", `"x"`},
		{"int64", int64(7), "7"},
		{"float64", 1.5, "1.5"},
		{"bool", true, "true"},
		{"bytes", []byte("hi"), `"aGk="`},
		{"fallback", struct{ A int }{A: 1}, `{1}`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			normalized, err := bdNormalizeValue(item.value)
			if err != nil {
				t.Fatal(err)
			}
			line, err := bdCanonicalRowJSON(map[string]any{"v": normalized})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(line), item.want) {
				t.Fatalf("值 %v 规范化输出应包含 %s: %s", item.value, item.want, line)
			}
		})
	}
	if _, err := bdNormalizeValue(bdFailingValuer{}); err == nil {
		t.Fatal("Valuer 失败必须传播")
	}
	// time.Time 直接透传（两侧同为 RFC3339 JSON 渲染）。
	normalized, err := bdNormalizeValue(bdSampleTime())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := normalized.(time.Time); !ok {
		t.Fatalf("time.Time 必须原样透传: %T", normalized)
	}
}

func bdSampleTime() time.Time {
	return time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
}

type bdFailingValuer struct{}

func (bdFailingValuer) Value() (driver.Value, error) { return nil, errBDInjected }

var errBDInjected = errors.New("injected")

func TestBDParamValueMatrix(t *testing.T) {
	intValue, err := bdParamValue(json.Number("42"))
	if err != nil || intValue != int64(42) {
		t.Fatalf("整数 JSON 数字必须无损转 int64: %v %v", intValue, err)
	}
	floatValue, err := bdParamValue(json.Number("1.25"))
	if err != nil || floatValue != 1.25 {
		t.Fatalf("小数 JSON 数字必须转 float64: %v %v", floatValue, err)
	}
	if _, err := bdParamValue(json.Number("abc")); err == nil {
		t.Fatal("非法 JSON 数字必须报错")
	}
	nested, err := bdParamValue(map[string]any{"b": 1, "a": 2})
	if err != nil || nested != `{"a":2,"b":1}` {
		t.Fatalf("嵌套对象必须 canonical 化为字符串: %v %v", nested, err)
	}
	if value, err := bdParamValue(nil); err != nil || value != nil {
		t.Fatalf("nil 必须保持 nil: %v %v", value, err)
	}
	if value, err := bdParamValue("x"); err != nil || value != "x" {
		t.Fatalf("字符串必须透传: %v %v", value, err)
	}
	if _, err := bdParamValue(complex(1, 2)); err == nil {
		t.Fatal("未知类型必须报错")
	}
}

func TestBDAttnumListVariants(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want []int64
		bad  bool
	}{
		{"int16", []int16{1, 3}, []int64{1, 3}, false},
		{"int32", []int32{2}, []int64{2}, false},
		{"int64", []int64{4}, []int64{4}, false},
		{"any", []any{int64(5)}, []int64{5}, false},
		{"bytes", []byte("1 2"), []int64{1, 2}, false},
		{"string", "3 4", []int64{3, 4}, false},
		{"nil", nil, nil, false},
		{"junk", []any{"x"}, nil, true},
		{"junktext", "x y", nil, true},
		{"type", complex(1, 2), nil, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := bdAttnumList(item.raw)
			if item.bad {
				if err == nil {
					t.Fatalf("必须失败: %v", item.raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(item.want) {
				t.Fatalf("%v != %v", got, item.want)
			}
			for i := range got {
				if got[i] != item.want[i] {
					t.Fatalf("%v != %v", got, item.want)
				}
			}
		})
	}
}

func TestBDTopologicalOrderUnit(t *testing.T) {
	// 无边时输出 canonical 序且不判环。
	order, cycle := bdTopologicalOrder(nil)
	if cycle || len(order) != len(contracts.BusinessDatasetTables) || order[0] != contracts.BusinessDatasetTables[0] {
		t.Fatalf("无边时必须输出 canonical 序: %v cycle=%v", order, cycle)
	}
	// 自引用边在表级拓扑中会成环——这正是调用方（Import）必须先剔除自引用、
	// 交由行级父先于子排序处理的契约。
	_, cycle = bdTopologicalOrder([][2]int{{bdCanonicalIndex("providers"), bdCanonicalIndex("providers")}})
	if !cycle {
		t.Fatal("自引用边直接进入表级拓扑必须判环（调用方契约）")
	}
	// 双向边成环。
	_, cycle = bdTopologicalOrder([][2]int{
		{bdCanonicalIndex("resource_authorizations"), bdCanonicalIndex("resource_authorization_sources")},
		{bdCanonicalIndex("resource_authorization_sources"), bdCanonicalIndex("resource_authorizations")},
	})
	if !cycle {
		t.Fatal("双向边必须判为环")
	}
}

func TestBDReadDatasetFileColumnConsistency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1,\"b\":\"x\"}\n{\"a\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := bdReadDatasetFile(path); err == nil {
		t.Fatal("列集合漂移必须报错")
	}
	if err := os.WriteFile(path, []byte("{\"a\":1}\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := bdReadDatasetFile(path); err == nil {
		t.Fatal("坏行必须报错")
	}
	if _, _, _, _, err := bdReadDatasetFile(filepath.Join(dir, "missing.jsonl")); err == nil {
		t.Fatal("缺文件必须报错")
	}
	if err := os.WriteFile(path, []byte("{\"b\":\"x\",\"a\":1}\n\n{\"a\":2,\"b\":\"y\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, columns, rows, count, err := bdReadDatasetFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(columns) != 2 || columns[0] != "a" || columns[1] != "b" || len(rows) != 2 {
		t.Fatalf("解析结果错误: %d %v %v", count, columns, rows)
	}
	if len(digest) != 64 {
		t.Fatalf("digest 必须是 sha256 hex: %q", digest)
	}
}

func TestBDValueKeysStable(t *testing.T) {
	if bdValueKey(nil) != "\x00null" {
		t.Fatal("nil 键必须稳定")
	}
	if bdValueKey(json.Number("3")) != "n:3" {
		t.Fatal("数字键必须带类型前缀")
	}
	if bdValueKey("3") != "s:3" {
		t.Fatal("字符串键必须带类型前缀")
	}
	if bdValueKey(true) != "b:true" {
		t.Fatal("布尔键必须带类型前缀")
	}
	if bdValueKeys([]any{"a", nil}) != "s:a\x1f\x00null" {
		t.Fatalf("多列键必须拼接: %q", bdValueKeys([]any{"a", nil}))
	}
}

func TestBDQuoteIdentRejectsUnsafe(t *testing.T) {
	if _, err := quoteIdent(`bad"name`); err == nil {
		t.Fatal("内嵌引号必须拒绝")
	}
	if _, err := quoteIdent(""); err == nil {
		t.Fatal("空标识符必须拒绝")
	}
	quoted, err := quoteIdent("ok")
	if err != nil || quoted != `"ok"` {
		t.Fatalf("合法标识符必须加引号: %q %v", quoted, err)
	}
	if _, err := bdSelectAll("t", []string{"c"}, nil); err == nil {
		t.Fatal("空排序键必须拒绝")
	}
	if _, err := bdInsert("t", nil); err == nil {
		t.Fatal("空列 INSERT 必须拒绝（调用方保证不发生）")
	}
}

func TestBDManifestDecodeRejectsUnknownAndTrailing(t *testing.T) {
	base := contracts.BusinessDatasetManifest{
		FormatVersion:  contracts.BusinessDatasetManifestFormatVersion,
		Scope:          contracts.BusinessDatasetManifestScope,
		Producer:       "t",
		SourceIdentity: "s",
		TargetIdentity: "d",
		CapturedAt:     "2026-09-21T00:00:00Z",
	}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contracts.DecodeBusinessDatasetManifest(data); err != nil {
		t.Fatalf("合法 manifest 必须可解码: %v", err)
	}
	if _, err := contracts.DecodeBusinessDatasetManifest(append(append([]byte{}, data...), []byte(" {}")...)); err == nil {
		t.Fatal("尾部 JSON 必须拒绝")
	}
	if _, err := contracts.DecodeBusinessDatasetManifest([]byte(`{"unknown":1}`)); err == nil {
		t.Fatal("未知字段必须拒绝")
	}
}

func TestBDManifestValidationErrors(t *testing.T) {
	valid := contracts.BusinessDatasetManifest{
		FormatVersion:  contracts.BusinessDatasetManifestFormatVersion,
		Scope:          contracts.BusinessDatasetManifestScope,
		Producer:       "t",
		SourceIdentity: "s",
		TargetIdentity: "d",
		CapturedAt:     "2026-09-21T00:00:00Z",
	}
	for _, name := range contracts.BusinessDatasetTables {
		entry := contracts.BusinessDatasetTableEntry{Name: name, File: name + ".jsonl", Rows: 0, Sha256: strings.Repeat("a", 64)}
		if contracts.IsBusinessDatasetStructuralOnlyTable(name) {
			entry.File = name + ".jsonl"
			entry.Sha256 = contracts.BusinessDatasetEmptyContentSha256
		}
		valid.Tables = append(valid.Tables, entry)
	}
	hash, err := contracts.ComputeBusinessDatasetManifestHash(valid)
	if err != nil {
		t.Fatal(err)
	}
	valid.ManifestHash = hash
	if errors := contracts.ValidateBusinessDatasetManifest(valid); len(errors) != 0 {
		t.Fatalf("合法 manifest 必须通过: %s", strings.Join(errors, "; "))
	}

	broken := valid
	broken.FormatVersion = "x"
	broken.Scope = "y"
	broken.Producer = " "
	broken.SourceIdentity = ""
	broken.TargetIdentity = ""
	broken.CapturedAt = "not-a-time"
	broken.ManifestHash = "nothex"
	if errors := contracts.ValidateBusinessDatasetManifest(broken); len(errors) < 6 {
		t.Fatalf("基础字段错误必须全部上报: %v", errors)
	}

	dup := valid
	dup.Tables = append([]contracts.BusinessDatasetTableEntry(nil), valid.Tables...)
	dup.Tables[1] = dup.Tables[0]
	dup.Tables = dup.Tables[:len(dup.Tables)-1]
	if errors := contracts.ValidateBusinessDatasetManifest(dup); len(errors) < 2 {
		t.Fatalf("重复表与缺失表必须上报: %v", errors)
	}

	extra := valid
	extra.Tables = append(append([]contracts.BusinessDatasetTableEntry(nil), valid.Tables...), contracts.BusinessDatasetTableEntry{Name: "outside", File: "x", Sha256: strings.Repeat("b", 64)})
	if errors := contracts.ValidateBusinessDatasetManifest(extra); len(errors) < 1 {
		t.Fatalf("白名单外表必须上报: %v", errors)
	}

	structural := valid
	structural.Tables = append([]contracts.BusinessDatasetTableEntry(nil), valid.Tables...)
	for index := range structural.Tables {
		if structural.Tables[index].Name == "api_key_schedule_status_events" {
			structural.Tables[index].Rows = 5
			structural.Tables[index].Sha256 = strings.Repeat("c", 64)
		}
	}
	if errors := contracts.ValidateBusinessDatasetManifest(structural); len(errors) < 2 {
		t.Fatalf("结构表非零行/错摘要必须上报: %v", errors)
	}

	dataRows := valid
	dataRows.Tables = append([]contracts.BusinessDatasetTableEntry(nil), valid.Tables...)
	for index := range dataRows.Tables {
		if dataRows.Tables[index].Name == "groups" {
			dataRows.Tables[index].File = ""
			dataRows.Tables[index].Sha256 = "z"
			dataRows.Tables[index].Rows = -1
		}
	}
	if errors := contracts.ValidateBusinessDatasetManifest(dataRows); len(errors) < 3 {
		t.Fatalf("数据表缺失文件/坏摘要/负行数必须上报: %v", errors)
	}

	hashMismatch := valid
	hashMismatch.ManifestHash = strings.Repeat("d", 64)
	if errors := contracts.ValidateBusinessDatasetManifest(hashMismatch); len(errors) != 1 || !strings.Contains(errors[0], "canonical") {
		t.Fatalf("哈希不自洽必须上报: %v", errors)
	}
	if errors := contracts.ValidateBusinessDatasetManifest(contracts.BusinessDatasetManifest{}); len(errors) < 5 {
		t.Fatalf("空 manifest 必须全面报错: %v", errors)
	}
}

func TestBDManifestHashIgnoresTableOrder(t *testing.T) {
	build := func(reverse bool) contracts.BusinessDatasetManifest {
		manifest := contracts.BusinessDatasetManifest{
			FormatVersion:  contracts.BusinessDatasetManifestFormatVersion,
			Scope:          contracts.BusinessDatasetManifestScope,
			Producer:       "t",
			SourceIdentity: "s",
			TargetIdentity: "d",
			CapturedAt:     "2026-09-21T00:00:00Z",
		}
		for _, name := range contracts.BusinessDatasetTables {
			manifest.Tables = append(manifest.Tables, contracts.BusinessDatasetTableEntry{Name: name, File: name + ".jsonl", Sha256: contracts.BusinessDatasetEmptyContentSha256})
		}
		if reverse {
			for i, j := 0, len(manifest.Tables)-1; i < j; i, j = i+1, j-1 {
				manifest.Tables[i], manifest.Tables[j] = manifest.Tables[j], manifest.Tables[i]
			}
		}
		return manifest
	}
	left, err := contracts.ComputeBusinessDatasetManifestHash(build(false))
	if err != nil {
		t.Fatal(err)
	}
	right, err := contracts.ComputeBusinessDatasetManifestHash(build(true))
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatal("canonical 哈希必须与表顺序无关")
	}
}

func TestBDScanRowsFailureBranches(t *testing.T) {
	db := openBDFakePG(bdNewSource(t))
	defer db.Close()
	ctx := context.Background()
	if _, err := bdLoadColumns(ctx, db, "groups"); err != nil {
		t.Fatalf("目录查询走 database/sql 池必须成功: %v", err)
	}
	catalog := bdNewSource(t)
	catalog.injectedFails = []string{"information_schema.columns"}
	broken := openBDFakePG(catalog)
	defer broken.Close()
	if _, err := bdLoadColumns(ctx, broken, "groups"); err == nil {
		t.Fatal("列目录故障必须报错")
	}
	catalog2 := bdNewSource(t)
	catalog2.injectedFails = []string{"indisprimary"}
	broken2 := openBDFakePG(catalog2)
	defer broken2.Close()
	if _, err := bdLoadPrimaryKey(ctx, broken2, "groups"); err == nil {
		t.Fatal("主键目录故障必须报错")
	}
	catalog3 := bdNewSource(t)
	catalog3.injectedFails = []string{"pg_constraint"}
	broken3 := openBDFakePG(catalog3)
	defer broken3.Close()
	if _, err := bdLoadForeignKeys(ctx, broken3); err == nil {
		t.Fatal("外键目录故障必须报错")
	}
	catalog4 := bdNewSource(t)
	catalog4.injectedFails = []string{"pg_attribute"}
	broken4 := openBDFakePG(catalog4)
	defer broken4.Close()
	if _, err := bdLoadForeignKeys(ctx, broken4); err == nil {
		t.Fatal("列序号目录故障必须报错")
	}
}

func TestBDImportPreflightQueryFailures(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	cases := []struct {
		name     string
		fragment string
	}{
		{"show", "SHOW transaction_read_only"},
		{"columns", "information_schema.columns"},
		{"fk", "pg_constraint"},
		{"sequence", "pg_get_serial_sequence"},
		{"setval", "setval("},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			catalog := bdNewTarget(t)
			catalog.injectedFails = []string{item.fragment}
			db := openBDFakePG(catalog)
			defer db.Close()
			if _, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{}); err == nil {
				t.Fatalf("目录/事务查询故障必须返回运行错误: %s", item.name)
			}
		})
	}
	catalog := bdNewTarget(t)
	catalog.injectedFails = []string{"SELECT current_database()"}
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{}); err == nil {
		t.Fatal("目标标识查询故障必须返回运行错误")
	}
	catalog2 := bdNewTarget(t)
	catalog2.injectedFails = []string{"indisprimary"}
	db2 := openBDFakePG(catalog2)
	defer db2.Close()
	if _, err := ImportBusinessDataset(context.Background(), db2, outDir, ImportOptions{}); err == nil {
		t.Fatal("主键目录故障必须返回运行错误")
	}
}

func TestBDSequenceEmptyTableResetsToStart(t *testing.T) {
	// 空表 + 有序列：setval(seq, 1, false)。用仅含 api_keys 空数据集的目录做
	// 导出（其余表也有行——单独清空 api_keys 即可）。
	catalog := bdNewSource(t)
	catalog.tables["api_keys"].rows = nil
	db := openBDFakePG(catalog)
	defer db.Close()
	outDir := filepath.Join(t.TempDir(), "dataset")
	report, err := ExportBusinessDataset(context.Background(), db, outDir, ExportOptions{})
	if err != nil || !report.Ready() {
		t.Fatalf("导出失败: %v %+v", err, report)
	}
	targetCatalog := bdNewTarget(t)
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	importReport, err := ImportBusinessDataset(context.Background(), target, outDir, ImportOptions{})
	if err != nil || !importReport.Ready() {
		t.Fatalf("导入失败: %v %+v", err, importReport)
	}
	seq := targetCatalog.sequences["juhe_business.api_keys_id_seq"]
	if seq.value != 1 || seq.isCalled {
		t.Fatalf("空表序列必须 setval(seq,1,false): %+v", seq)
	}
}

func TestBDReportReadySemantics(t *testing.T) {
	if (ExportReport{}).Ready() {
		t.Fatal("空导出报告不得就绪")
	}
	if (ImportReport{}).Ready() {
		t.Fatal("空导入报告不得就绪")
	}
	if (ImportReport{Blockers: []string{"x"}}).Ready() {
		t.Fatal("有 blocker 不得就绪")
	}
}

func TestBDLoadBusinessDatasetManifest(t *testing.T) {
	if _, _, err := LoadBusinessDatasetManifest(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("目录缺失必须报错")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, path, err := LoadBusinessDatasetManifest(dir)
	if err != nil || path != filepath.Join(dir, ManifestFileName) || manifest.FormatVersion != "" {
		t.Fatalf("解码成功路径语义错误: %v %s %+v", err, path, manifest)
	}
}
