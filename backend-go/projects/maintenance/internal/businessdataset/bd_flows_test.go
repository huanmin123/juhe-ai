package businessdataset

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// bdNewSource 构造带种子数据的源库 fake；bdNewTarget 构造空目标库 fake。
func bdNewSource(t *testing.T) *bdPGCatalog {
	t.Helper()
	catalog := newBDPGCatalog("juhe_source_db")
	bdBuildWhitelistCatalog(catalog)
	bdSeedSourceRows(catalog)
	return catalog
}

func bdNewTarget(t *testing.T) *bdPGCatalog {
	t.Helper()
	catalog := newBDPGCatalog("juhe_target_db")
	bdBuildWhitelistCatalog(catalog)
	return catalog
}

func bdExportForTest(t *testing.T) (string, contracts.BusinessDatasetManifest) {
	t.Helper()
	source := openBDFakePG(bdNewSource(t))
	defer source.Close()
	outDir := filepath.Join(t.TempDir(), "dataset")
	report, err := ExportBusinessDataset(context.Background(), source, outDir, ExportOptions{TargetIdentity: "juhe_target_db", Now: bdTimeOf("2026-09-21T08:00:00Z")})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("导出报告未就绪: %+v", report)
	}
	data, err := os.ReadFile(report.ManifestFile)
	if err != nil {
		t.Fatalf("读取 manifest: %v", err)
	}
	manifest, err := contracts.DecodeBusinessDatasetManifest(data)
	if err != nil {
		t.Fatalf("解码 manifest: %v", err)
	}
	return outDir, manifest
}

// bdTimeOf 返回确定的 UTC 时刻，保持导出输出可断言。
func bdTimeOf(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestBDRoundTripHappyPath(t *testing.T) {
	ctx := context.Background()
	source := openBDFakePG(bdNewSource(t))
	outDir := filepath.Join(t.TempDir(), "dataset")
	exportReport, err := ExportBusinessDataset(ctx, source, outDir, ExportOptions{Producer: "bd-test", TargetIdentity: "juhe_target_db", Now: bdTimeOf("2026-09-21T08:00:00Z")})
	source.Close()
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !exportReport.Ready() {
		t.Fatalf("导出报告未就绪: %+v", exportReport)
	}
	if !exportReport.ReadOnlyTransaction {
		t.Fatal("导出必须声明只读事务")
	}
	if exportReport.SourceIdentity != "juhe_source_db" || exportReport.TargetIdentity != "juhe_target_db" {
		t.Fatalf("源/目标标识错误: %+v", exportReport)
	}
	if exportReport.CapturedAt != "2026-09-21T08:00:00Z" {
		t.Fatalf("CapturedAt 必须是固定 UTC RFC3339: %q", exportReport.CapturedAt)
	}
	if len(exportReport.Tables) != len(contracts.BusinessDatasetTables) {
		t.Fatalf("导出表数 %d != 40", len(exportReport.Tables))
	}

	data, err := os.ReadFile(exportReport.ManifestFile)
	if err != nil {
		t.Fatalf("读取 manifest: %v", err)
	}
	manifest, err := contracts.DecodeBusinessDatasetManifest(data)
	if err != nil {
		t.Fatalf("解码 manifest: %v", err)
	}
	if errors := contracts.ValidateBusinessDatasetManifest(manifest); len(errors) > 0 {
		t.Fatalf("manifest 自校验失败: %s", strings.Join(errors, "; "))
	}
	structuralSeen := 0
	for _, entry := range manifest.Tables {
		if contracts.IsBusinessDatasetStructuralOnlyTable(entry.Name) {
			structuralSeen++
			if entry.Rows != 0 || entry.Sha256 != contracts.BusinessDatasetEmptyContentSha256 {
				t.Fatalf("结构-only 表 %s 必须零行且空内容摘要: %+v", entry.Name, entry)
			}
			if _, err := os.Stat(filepath.Join(outDir, entry.File)); !os.IsNotExist(err) {
				t.Fatalf("结构-only 表 %s 不得生成数据文件: %v", entry.Name, err)
			}
			continue
		}
		if entry.Name == "providers" && entry.Rows != 2 {
			t.Fatalf("providers 应导出 2 行: %+v", entry)
		}
		if entry.Name == "system_accounts" && entry.Rows != 1 {
			t.Fatalf("system_accounts 应导出 1 行: %+v", entry)
		}
		if _, err := os.Stat(filepath.Join(outDir, entry.File)); err != nil {
			t.Fatalf("数据文件应存在: %v", err)
		}
	}
	if structuralSeen != len(contracts.BusinessDatasetStructuralOnlyTables) {
		t.Fatalf("结构-only 表条目数 %d != 3", structuralSeen)
	}

	targetCatalog := bdNewTarget(t)
	var insertLog *[]bdFakeInsert
	targetCatalog.onInsert = func(table string, row map[string]driver.Value) { _ = insertLog }
	target := openBDFakePG(targetCatalog)
	defer target.Close()
	importReport, err := ImportBusinessDataset(ctx, target, outDir, ImportOptions{ExpectedTargetIdentity: "juhe_target_db"})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if !importReport.Ready() {
		t.Fatalf("导入报告未就绪: %+v", importReport)
	}
	if !importReport.Committed || importReport.Verification != "match" || !importReport.StructuralEmptyVerified || !importReport.FilesVerified {
		t.Fatalf("导入完成态错误: %+v", importReport)
	}
	if importReport.TargetIdentity != "juhe_target_db" || !importReport.TargetIdentityMatch {
		t.Fatalf("目标标识不匹配: %+v", importReport)
	}
	if importReport.ForeignKeysInspected == 0 {
		t.Fatal("目标库必须发现外键边")
	}
	// 拓扑序：每条非自引用边的被引用表必须先于引用表。
	position := map[string]int{}
	for index, name := range importReport.TableOrder {
		position[name] = index
	}
	for _, edge := range []struct{ parent, child string }{
		{"system_accounts", "accounts"},
		{"providers", "accounts"},
		{"protocols", "provider_protocol_profiles"},
		{"providers", "provider_protocol_profiles"},
		{"provider_protocol_profiles", "provider_protocol_profile_families"},
		{"groups", "group_accounts"},
		{"accounts", "group_accounts"},
		{"resource_authorizations", "resource_authorization_sources"},
	} {
		if position[edge.parent] >= position[edge.child] {
			t.Fatalf("拓扑序错误: %s(%d) 必须先于 %s(%d)", edge.parent, position[edge.parent], edge.child, position[edge.child])
		}
	}
	// providers 自引用父行必须先插入。
	parentIndex, childIndex := -1, -1
	for index, insert := range targetCatalog.insertLog {
		if insert.Table != "providers" {
			continue
		}
		switch insert.Row["id"] {
		case "z-parent":
			parentIndex = index
		case "a-child":
			childIndex = index
		}
	}
	if parentIndex < 0 || childIndex < 0 {
		t.Fatalf("providers 两行都应被插入: %+v", targetCatalog.insertLog)
	}
	if parentIndex > childIndex {
		t.Fatalf("providers 父行必须先于子行插入: parent=%d child=%d", parentIndex, childIndex)
	}
	// 序列推进到 max(id)。
	if seq := targetCatalog.sequences["juhe_business.api_keys_id_seq"]; seq.value != 41 || !seq.isCalled {
		t.Fatalf("api_keys 序列应 setval 到导入后的 max(id)=41: %+v", seq)
	}
	// 复合主键表必须跳过序列推进。
	if stat := bdImportStat(t, importReport, "provider_protocol_profile_families"); stat.SequenceAdvanced || stat.SequenceName != "" {
		t.Fatalf("复合主键表必须跳过序列推进: %+v", stat)
	}
	if stat := bdImportStat(t, importReport, "api_keys"); !stat.SequenceAdvanced || stat.SequenceName != "juhe_business.api_keys_id_seq" {
		t.Fatalf("api_keys 必须推进序列: %+v", stat)
	}
	if stat := bdImportStat(t, importReport, "system_accounts"); stat.SequenceAdvanced {
		t.Fatalf("无序列表必须跳过: %+v", stat)
	}
	// providers 顺序重排后读回 digest 仍与 manifest 一致。
	if stat := bdImportStat(t, importReport, "providers"); !stat.DigestMatch || stat.Status != "match" {
		t.Fatalf("providers 读回必须 match: %+v", stat)
	}
	if targetCatalog.commits != 1 || targetCatalog.rollbacks != 0 {
		t.Fatalf("成功路径必须恰好一次提交且无回滚: commits=%d rollbacks=%d", targetCatalog.commits, targetCatalog.rollbacks)
	}
}

func bdImportStat(t *testing.T, report ImportReport, table string) *ImportTableStat {
	t.Helper()
	for _, stat := range report.Tables {
		if stat.Name == table {
			return stat
		}
	}
	t.Fatalf("报告缺少表 %s", table)
	return nil
}

func TestBDExportRejectsWritableTransaction(t *testing.T) {
	catalog := bdNewSource(t)
	catalog.ignoreReadOnly = true
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ExportBusinessDataset(context.Background(), db, filepath.Join(t.TempDir(), "dataset"), ExportOptions{})
	if err == nil {
		t.Fatalf("非只读事务必须报错: %+v", report)
	}
	if !strings.Contains(err.Error(), "READ ONLY") {
		t.Fatalf("错误必须说明只读要求: %v", err)
	}
}

func TestBDExportStructuralTableNonEmpty(t *testing.T) {
	catalog := bdNewSource(t)
	catalog.seedRow("group_account_stats_dirty", map[string]driver.Value{"id": "dirty-1", "name": "x", "created_at": "t"})
	db := openBDFakePG(catalog)
	defer db.Close()
	outDir := filepath.Join(t.TempDir(), "dataset")
	report, err := ExportBusinessDataset(context.Background(), db, outDir, ExportOptions{})
	if err != nil {
		t.Fatalf("结构表非空属于 blocker 而非运行错误: %v", err)
	}
	if report.Ready() {
		t.Fatalf("结构表非空必须阻断: %+v", report)
	}
	if len(report.Blockers) != 1 || !strings.Contains(report.Blockers[0], "group_account_stats_dirty") {
		t.Fatalf("blocker 必须指明结构表: %+v", report.Blockers)
	}
	if _, err := os.Stat(filepath.Join(outDir, ManifestFileName)); !os.IsNotExist(err) {
		t.Fatal("存在 blocker 时不得写出 manifest")
	}
}

func TestBDExportMissingWhitelistTable(t *testing.T) {
	catalog := bdNewSource(t)
	delete(catalog.tables, "groups")
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ExportBusinessDataset(context.Background(), db, filepath.Join(t.TempDir(), "dataset"), ExportOptions{})
	if err == nil {
		t.Fatalf("源库缺少 whitelist 表必须报错: %+v", report)
	}
}

func TestBDExportBadOutputDir(t *testing.T) {
	db := openBDFakePG(bdNewSource(t))
	defer db.Close()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportBusinessDataset(context.Background(), db, blocker, ExportOptions{}); err == nil {
		t.Fatal("导出目录是文件必须报错")
	}
	db2 := openBDFakePG(bdNewSource(t))
	defer db2.Close()
	if _, err := ExportBusinessDataset(context.Background(), db2, "  ", ExportOptions{}); err == nil {
		t.Fatal("空导出目录必须报错")
	}
	db3 := openBDFakePG(bdNewSource(t))
	defer db3.Close()
	if _, err := ExportBusinessDataset(context.Background(), nil, "x", ExportOptions{}); err == nil {
		t.Fatal("nil 数据库必须报错")
	}
}

func TestBDExportTableQueryFailure(t *testing.T) {
	catalog := bdNewSource(t)
	catalog.injectedFails = []string{"FROM juhe_business"}
	db := openBDFakePG(catalog)
	defer db.Close()
	if _, err := ExportBusinessDataset(context.Background(), db, filepath.Join(t.TempDir(), "dataset"), ExportOptions{}); err == nil {
		t.Fatal("行查询故障必须报错")
	}
}

func TestBDExportValueNormalizationBranches(t *testing.T) {
	catalog := bdNewSource(t)
	// []byte（bytea 语义）导出为 base64 字符串；未知类型走 %v 兜底；Valuer
	// 类型走 Value() 展开。
	catalog.seedRow("global_settings", map[string]driver.Value{"id": "bytes-1", "name": []byte("binary"), "created_at": "t"})
	catalog.seedRow("system_settings", map[string]driver.Value{"id": "valuer-1", "name": bdValuerValue{"wrapped"}, "created_at": "t"})
	catalog.seedRow("proxy_profiles", map[string]driver.Value{"id": "exotic-1", "name": struct{ A int }{A: 3}, "created_at": "t"})
	db := openBDFakePG(catalog)
	defer db.Close()
	outDir := filepath.Join(t.TempDir(), "dataset")
	report, err := ExportBusinessDataset(context.Background(), db, outDir, ExportOptions{})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("导出应成功: %+v", report)
	}
	bytesLine := bdReadLine(t, filepath.Join(outDir, "global_settings.jsonl"), "bytes-1")
	if !strings.Contains(bytesLine, "YmluYXJ5") {
		t.Fatalf("[]byte 必须以 base64 字符串导出: %s", bytesLine)
	}
	valuerLine := bdReadLine(t, filepath.Join(outDir, "system_settings.jsonl"), "valuer-1")
	if !strings.Contains(valuerLine, "wrapped") {
		t.Fatalf("Valuer 值必须展开: %s", valuerLine)
	}
	exoticLine := bdReadLine(t, filepath.Join(outDir, "proxy_profiles.jsonl"), "exotic-1")
	if !strings.Contains(exoticLine, "{3}") {
		t.Fatalf("未知类型必须以 %%v 确定性兜底: %s", exoticLine)
	}
}

type bdValuerValue struct{ inner string }

func (v bdValuerValue) Value() (driver.Value, error) { return v.inner, nil }

func bdReadLine(t *testing.T, path, id string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.Contains(line, id) {
			return line
		}
	}
	t.Fatalf("文件 %s 缺少 id %s 的行", path, id)
	return ""
}

func TestBDImportRejectsReadOnlyTransaction(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.forceReadOnly = true
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("只读事务属于 blocker 而非运行错误: %v", err)
	}
	if report.Ready() || report.WritableTransaction {
		t.Fatalf("只读事务必须阻断: %+v", report)
	}
	if len(report.Blockers) != 1 || !strings.Contains(report.Blockers[0], "只读") {
		t.Fatalf("blocker 必须说明只读: %+v", report.Blockers)
	}
	if catalog.commits != 0 {
		t.Fatal("只读事务不得提交")
	}
}

func TestBDImportHashTamperDetection(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	manifestPath := filepath.Join(outDir, ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest contracts.BusinessDatasetManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(outDir, manifest.Tables[0].File)
	original, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	// 追加一条形状一致的重复行：文件仍可正常解码，但哈希与行数同时漂移。
	if err := os.WriteFile(targetPath, append(original, original[:strings.IndexByte(string(original), '\n')+1]...), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openBDFakePG(bdNewTarget(t))
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("哈希篡改属于 blocker: %v", err)
	}
	if report.Ready() || report.FilesVerified {
		t.Fatalf("哈希篡改必须阻断: %+v", report)
	}
	if len(report.Blockers) == 0 {
		t.Fatalf("哈希/行数篡改必须产生 blocker: %+v", report)
	}
}

func TestBDImportMissingFile(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	if err := os.Remove(filepath.Join(outDir, "api_keys.jsonl")); err != nil {
		t.Fatal(err)
	}
	db := openBDFakePG(bdNewTarget(t))
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("缺文件属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("缺文件必须阻断: %+v", report)
	}
}

func TestBDImportMissingTargetTable(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	delete(catalog.tables, "system_teams")
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("目标缺表属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("目标缺表必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "system_teams") {
		t.Fatalf("blocker 必须指明缺失表: %+v", report.Blockers)
	}
	if catalog.commits != 0 {
		t.Fatal("blocker 路径不得提交")
	}
}

func TestBDImportFKCycle(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	// resource_authorization_sources→resource_authorizations 已存在；补一条反向边成环。
	catalog.addForeignKey("resource_authorizations", "ras_reverse_fk", []string{"id"}, "resource_authorization_sources", []string{"id"})
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("外键环属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("外键环必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "环") {
		t.Fatalf("blocker 必须说明环: %+v", report.Blockers)
	}
	if catalog.commits != 0 {
		t.Fatal("环阻断不得提交")
	}
}

func TestBDImportFKOutsideWhitelist(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.addTable("external_integration_sources", []bdFakeColumn{{name: "id", dataType: "text", nullable: false}}, []string{"id"})
	catalog.addColumn("api_keys", bdFakeColumn{name: "source_ref_id", dataType: "text", nullable: true})
	catalog.addForeignKey("api_keys", "api_keys_source_fk", []string{"source_ref_id"}, "external_integration_sources", []string{"id"})
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("白名单外 FK 属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("白名单外 FK 必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "白名单之外") {
		t.Fatalf("blocker 必须说明白名单外引用: %+v", report.Blockers)
	}
}

func TestBDImportStructuralTableNonEmpty(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.seedRow("account_schedule_status_events", map[string]driver.Value{"id": "evt-1", "name": "x", "created_at": "t"})
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("结构表非空属于 blocker: %v", err)
	}
	if report.Ready() || report.StructuralEmptyVerified {
		t.Fatalf("结构表非空必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "account_schedule_status_events") {
		t.Fatalf("blocker 必须指明结构表: %+v", report.Blockers)
	}
}

func TestBDImportTargetOnlyColumnOmitted(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.addColumn("groups", bdFakeColumn{name: "target_future_column", dataType: "text", nullable: true})
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("目标独有列允许省略: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("目标独有列不得阻断: %+v", report)
	}
	stat := bdImportStat(t, report, "groups")
	found := false
	for _, omitted := range stat.OmittedTargetOnlyColumns {
		if omitted == "target_future_column" {
			found = true
		}
	}
	if !found {
		t.Fatalf("目标独有列必须记录在案: %+v", stat)
	}
	// 公共投影与源列一致时 digest 仍直接对 manifest 校验。
	if !stat.DigestMatch {
		t.Fatalf("digest 必须与 manifest 一致: %+v", stat)
	}
}

func TestBDImportDroppedSourceColumn(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	// 目标库 providers 少一列 source_col：默认 blocker。
	for index, column := range catalog.tables["providers"].columns {
		if column.name == "name" {
			catalog.tables["providers"].columns = append(catalog.tables["providers"].columns[:index], catalog.tables["providers"].columns[index+1:]...)
			break
		}
	}
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("源独有列属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("未登记的源独有列必须阻断: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "providers 源列 name") {
		t.Fatalf("blocker 必须指明缺失列: %+v", report.Blockers)
	}

	// 登记放行后按主键逐行对比公共投影。
	catalog2 := bdNewTarget(t)
	for index, column := range catalog2.tables["providers"].columns {
		if column.name == "name" {
			catalog2.tables["providers"].columns = append(catalog2.tables["providers"].columns[:index], catalog2.tables["providers"].columns[index+1:]...)
			break
		}
	}
	db2 := openBDFakePG(catalog2)
	defer db2.Close()
	report2, err := ImportBusinessDataset(context.Background(), db2, outDir, ImportOptions{AllowedMissingColumns: []string{"providers.name"}})
	if err != nil {
		t.Fatalf("登记放行导入失败: %v", err)
	}
	if !report2.Ready() {
		t.Fatalf("登记放行必须成功: %+v", report2)
	}
	stat := bdImportStat(t, report2, "providers")
	if len(stat.DroppedSourceColumns) != 1 || stat.DroppedSourceColumns[0] != "name" {
		t.Fatalf("放行列必须记录: %+v", stat)
	}
	if !stat.DigestMatch {
		t.Fatalf("投影逐行校验必须通过: %+v", stat)
	}
}

func TestBDImportDigestDriftDetected(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	// 每行插入后篡改 name，制造读回 digest 漂移。
	catalog.onInsert = func(table string, row map[string]driver.Value) {
		if table == "groups" {
			row["name"] = "drifted"
		}
	}
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err != nil {
		t.Fatalf("digest 漂移属于 blocker: %v", err)
	}
	if report.Ready() {
		t.Fatalf("digest 漂移必须阻断: %+v", report)
	}
	if catalog.commits != 0 {
		t.Fatal("digest 漂移必须整体回滚（all-or-nothing）")
	}
	if !strings.Contains(strings.Join(report.Blockers, "; "), "读回校验不匹配") {
		t.Fatalf("blocker 必须说明读回不匹配: %+v", report.Blockers)
	}
}

func TestBDImportInsertFailureRollsBack(t *testing.T) {
	outDir, _ := bdExportForTest(t)
	catalog := bdNewTarget(t)
	catalog.injectedFails = []string{"INSERT INTO juhe_business.\"groups\""}
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, outDir, ImportOptions{})
	if err == nil {
		t.Fatalf("插入故障必须返回运行错误: %+v", report)
	}
	if catalog.commits != 0 {
		t.Fatal("插入故障必须回滚")
	}
}

func TestBDImportBadInputs(t *testing.T) {
	db := openBDFakePG(bdNewTarget(t))
	defer db.Close()
	if _, err := ImportBusinessDataset(context.Background(), nil, "x", ImportOptions{}); err == nil {
		t.Fatal("nil 数据库必须报错")
	}
	if _, err := ImportBusinessDataset(context.Background(), db, "", ImportOptions{}); err == nil {
		t.Fatal("空目录必须报错")
	}
	if _, err := ImportBusinessDataset(context.Background(), db, filepath.Join(t.TempDir(), "missing"), ImportOptions{}); err == nil {
		t.Fatal("目录不存在必须报错")
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ManifestFileName), []byte("{\"formatVersion\":1}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBusinessDataset(context.Background(), db, broken, ImportOptions{}); err == nil {
		t.Fatal("解码失败必须报错")
	}
	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, ManifestFileName), []byte("{\"formatVersion\":\"business-dataset-manifest/v1\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ImportBusinessDataset(context.Background(), db, invalid, ImportOptions{})
	if err != nil {
		t.Fatalf("自洽性失败属于 blocker: %v", err)
	}
	if report.Ready() || len(report.Blockers) == 0 {
		t.Fatalf("自洽性失败必须阻断: %+v", report)
	}
}
