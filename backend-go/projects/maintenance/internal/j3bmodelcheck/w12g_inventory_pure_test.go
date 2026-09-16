package j3bmodelcheck

// w12g 波次：覆盖 inventory 证据链、readback manifest 构造与 PostgreSQL
// backfill 纯函数的全部错误/边界分支。全部为包内直调，无 I/O。

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errW12GPure = fmt.Errorf("w12g 纯函数探针")

func jsonUnmarshalW12G(data string, target any) error { return json.Unmarshal([]byte(data), target) }

func w12gWriteFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func w12gDigest(seed string) string {
	return hex.EncodeToString([]byte(fmt.Sprintf("%064s", seed)))[:64]
}

func TestW12GLoadLegacyJ3bFactEvidenceBranches(t *testing.T) {
	if _, err := LoadLegacyJ3bFactEvidence("   "); err == nil || !strings.Contains(err.Error(), "explicit file path") {
		t.Fatalf("空路径必须拒绝: %v", err)
	}
	if _, err := LoadLegacyJ3bFactEvidence("w12g/no-such-file.json"); err == nil || !strings.Contains(err.Error(), "read J3b inventory evidence file") {
		t.Fatalf("缺失文件必须报读失败: %v", err)
	}
	path := w12gWriteFile(t, "bad.json", "{not json")
	if _, err := LoadLegacyJ3bFactEvidence(path); err == nil || !strings.Contains(err.Error(), "decode J3b inventory evidence file") {
		t.Fatalf("坏 JSON 必须报解码失败: %v", err)
	}
	trailing := w12gWriteFile(t, "trailing.json", `{"facts":{}} {"facts":{}}`)
	if _, err := LoadLegacyJ3bFactEvidence(trailing); err == nil || !strings.Contains(err.Error(), "trailing JSON data") {
		t.Fatalf("尾随 JSON 必须拒绝: %v", err)
	}
	broken := w12gWriteFile(t, "broken.json", `{"facts":{"a":{"scope":"x"}}} {"oops"}`)
	if _, err := LoadLegacyJ3bFactEvidence(broken); err == nil || !strings.Contains(err.Error(), "decode J3b inventory evidence file") {
		t.Fatalf("尾随坏 JSON 必须报解码失败: %v", err)
	}
	noFacts := w12gWriteFile(t, "nofacts.json", `{}`)
	if _, err := LoadLegacyJ3bFactEvidence(noFacts); err == nil || !strings.Contains(err.Error(), "facts object is required") {
		t.Fatalf("缺失 facts 必须拒绝: %v", err)
	}
	ok := w12gWriteFile(t, "ok.json", `{"facts":{"a":{"scope":"","digest":""}}}`)
	facts, err := LoadLegacyJ3bFactEvidence(ok)
	if err != nil {
		t.Fatal(err)
	}
	fact, exists := facts["a"]
	if !exists || !fact.evidenceFromJSON || !fact.scopeSet {
		t.Fatalf("JSON 解码必须记录 scope 显式存在性: %+v", fact)
	}
}

func TestW12GEvidenceUnmarshalJSONScopePresence(t *testing.T) {
	// 直接解码：显式空 scope 与缺失 scope 必须区分。
	var explicit LegacyJ3bFactEvidence
	if err := jsonUnmarshalW12G(`{"scope":"","digest":""}`, &explicit); err != nil {
		t.Fatal(err)
	}
	if !explicit.evidenceFromJSON || !explicit.scopeSet {
		t.Fatalf("显式空 scope 必须被记录: %+v", explicit)
	}
	var omitted LegacyJ3bFactEvidence
	if err := jsonUnmarshalW12G(`{}`, &omitted); err != nil {
		t.Fatal(err)
	}
	if !omitted.evidenceFromJSON || omitted.scopeSet {
		t.Fatalf("缺失 scope 必须记为未设置: %+v", omitted)
	}
	if err := jsonUnmarshalW12G(`{"scope":123}`, &omitted); err == nil {
		t.Fatal("非法 scope 类型必须报错")
	}
}

func TestW12GValidateLegacyJ3bFactCoverageAllGates(t *testing.T) {

	t.Run("unnamed duplicate and unknown entries", func(t *testing.T) {
		inventory := []LegacyJ3bFact{
			{Name: "  "},
			{Name: "model_check_runs", SourceSchema: "juhe_dataset", SourceTable: "model_check_runs", Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs"},
			{Name: "model_check_runs", SourceSchema: "juhe_dataset", SourceTable: "model_check_runs", Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs"},
			{Name: "w12g-unknown-fact", Disposition: LegacyFactRetain, RetentionWhy: "x"},
		}
		report := ValidateLegacyJ3bFactCoverage(inventory, map[string]LegacyJ3bFactEvidence{})
		if report.Ready || report.InventoryComplete {
			t.Fatal("异常条目必须保持未就绪")
		}
		if report.Facts["__unnamed__"] == "" || report.Facts["w12g-unknown-fact"] != "unknown inventory entry" {
			t.Fatalf("报告缺失关键 finding: %+v", report.Facts)
		}
	})

	t.Run("source drift target gap retention gap", func(t *testing.T) {
		inventory := []LegacyJ3bFact{
			{Name: "model_check_runs", SourceSchema: "w12g-wrong-schema", SourceTable: "model_check_runs", Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs"},
			{Name: "model_check_items", SourceSchema: "juhe_dataset", SourceTable: "model_check_items", Disposition: LegacyFactBackfill},
			{Name: "model_token_integrity_windows", SourceSchema: "juhe_stats", SourceTable: "model_token_integrity_windows", Disposition: LegacyFactRetain},
			{Name: "model_paired_similarity_windows", SourceSchema: "juhe_stats", SourceTable: "model_paired_similarity_windows", Disposition: "w12g-unknown-disposition"},
		}
		evidence := map[string]LegacyJ3bFactEvidence{
			// 未知 disposition 只有在身份校验通过后才会落到 default 分支。
			"model_paired_similarity_windows": {SourceSchema: "juhe_stats", SourceTable: "model_paired_similarity_windows", Digest: w12gDigest("x")},
			// evidence 与期望一致、inventory 条目本身漂移时才报 source mapping drift。
			"model_check_runs": {SourceSchema: "juhe_dataset", SourceTable: "model_check_runs", Digest: w12gDigest("x")},
		}
		report := ValidateLegacyJ3bFactCoverage(inventory, evidence)
		for _, want := range []string{"source mapping drift", "target mapping missing", "retention decision missing", "coverage disposition missing", "coverage decision missing"} {
			found := false
			for _, reason := range report.Facts {
				if reason == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("缺少 %q: %+v", want, report.Facts)
			}
		}
	})

	t.Run("evidence presence branches", func(t *testing.T) {
		// backfill 无证据 / retain 无证据 / 完整证据 / scope 漂移 / readback 未验证 / digest 不匹配。
		inventory := []LegacyJ3bFact{
			{Name: "model_check_runs", SourceSchema: "juhe_dataset", SourceTable: "model_check_runs", Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs"},
			{Name: "model_check_items", SourceSchema: "juhe_dataset", SourceTable: "model_check_items", Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_items"},
			{Name: "model_check_observations", SourceSchema: "juhe_dataset", SourceTable: "model_check_observations", Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_observations"},
			{Name: "model_token_integrity_windows", SourceSchema: "juhe_stats", SourceTable: "model_token_integrity_windows", Disposition: LegacyFactRetain, RetentionWhy: "retain"},
			{Name: "model_token_integrity_rounds", SourceSchema: "juhe_stats", SourceTable: "model_token_integrity_rounds", Disposition: LegacyFactRetain, RetentionWhy: "retain"},
			{Name: "model_trust_window_sources", SourceSchema: "juhe_stats", SourceTable: "model_trust_window_sources", Disposition: LegacyFactRetain, RetentionWhy: "retain"},
		}
		evidence := map[string]LegacyJ3bFactEvidence{
			// model_check_runs：无证据（backfill readback unverified）。
			"model_check_items":             {SourceSchema: "wrong", SourceTable: "wrong", Digest: w12gDigest("x"), BackfillReadbackVerified: true},
			"model_check_observations":      {SourceSchema: "juhe_dataset", SourceTable: "model_check_observations", Digest: "not-a-digest", BackfillReadbackVerified: true},
			"model_token_integrity_windows": {SourceSchema: "juhe_stats", SourceTable: "model_token_integrity_windows", Digest: w12gDigest("x")},
			"model_token_integrity_rounds":  {SourceSchema: "juhe_stats", SourceTable: "model_token_integrity_rounds", Digest: w12gDigest("x"), RetentionVerified: true, SourceDigest: "aa", TargetDigest: "bb"},
			"model_trust_window_sources":    {SourceSchema: "juhe_stats", SourceTable: "model_trust_window_sources", Digest: w12gDigest("x"), RetentionVerified: true},
		}
		report := ValidateLegacyJ3bFactCoverage(inventory, evidence)
		if report.Ready {
			t.Fatal("混合证据必须未就绪")
		}
		expected := map[string]string{
			"model_check_runs":                "backfill readback unverified",
			"model_check_items":               "source/scope evidence drift",
			"model_check_observations":        "source/scope/digest evidence missing",
			"model_token_integrity_windows":   "immutable retention unverified",
			"model_token_integrity_rounds":    "immutable retention digest mismatch",
			"model_trust_window_sources":      "immutable retention verified",
			"model_paired_similarity_windows": "coverage decision missing",
		}
		for name, want := range expected {
			if report.Facts[name] != want {
				t.Fatalf("%s = %q, want %q", name, report.Facts[name], want)
			}
		}
	})

	t.Run("backfill verified happy path", func(t *testing.T) {
		inventory := []LegacyJ3bFact{{
			Name: "model_check_runs", SourceSchema: "juhe_dataset", SourceTable: "model_check_runs",
			Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs",
		}}
		evidence := map[string]LegacyJ3bFactEvidence{
			"model_check_runs": {SourceSchema: "juhe_dataset", SourceTable: "model_check_runs", Digest: w12gDigest("ok"), BackfillReadbackVerified: true, evidenceFromJSON: true, scopeSet: true},
		}
		report := ValidateLegacyJ3bFactCoverage(inventory, evidence)
		if report.Facts["model_check_runs"] != "backfill readback verified" || report.Ready {
			t.Fatalf("单一事实就绪但整体应因清单不全而未就绪: %+v", report.Facts)
		}
	})

	t.Run("unknown evidence entry reported once", func(t *testing.T) {
		evidence := map[string]LegacyJ3bFactEvidence{
			"w12g-unknown-evidence": {SourceSchema: "s", SourceTable: "t", Digest: w12gDigest("x")},
			"model_check_runs":      {SourceSchema: "juhe_dataset", SourceTable: "model_check_runs", Digest: w12gDigest("x"), BackfillReadbackVerified: true},
		}
		report := ValidateLegacyJ3bFactCoverage(nil, evidence)
		if report.Facts["w12g-unknown-evidence"] != "unknown evidence entry" {
			t.Fatalf("未知证据必须报告: %+v", report.Facts)
		}
	})
}

func TestW12GLegacyEvidenceIdentityHelper(t *testing.T) {
	item := LegacyJ3bFact{SourceSchema: "s", SourceTable: "t"}
	if legacyJ3bFactEvidenceHasIdentity(item, LegacyJ3bFactEvidence{SourceSchema: "s", Digest: w12gDigest("x")}) {
		t.Fatal("缺失 sourceTable 不得通过")
	}
	if legacyJ3bFactEvidenceHasIdentity(item, LegacyJ3bFactEvidence{SourceSchema: "s", SourceTable: "t", evidenceFromJSON: true}) {
		t.Fatal("JSON 证据缺 scope 不得通过")
	}
	if legacyJ3bFactEvidenceHasIdentity(item, LegacyJ3bFactEvidence{SourceSchema: "s", SourceTable: "t"}) {
		t.Fatal("缺失 digest 不得通过")
	}
	if !legacyJ3bFactEvidenceHasIdentity(item, LegacyJ3bFactEvidence{SourceSchema: "s", SourceTable: "t", RetentionDigest: w12gDigest("x")}) {
		t.Fatal("retention digest 可作为身份摘要")
	}
	if !legacyJ3bFactEvidenceHasIdentity(item, LegacyJ3bFactEvidence{SourceSchema: "s", SourceTable: "t", Digest: "sha256:" + strings.ToUpper(w12gDigest("x"))}) {
		t.Fatal("sha256 前缀与大小写必须归一化")
	}
}

func TestW12GNormalizePostgresBackfillOptionsBounds(t *testing.T) {
	if got, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{}); err != nil || got.maxRows != DefaultPostgresBackfillMaxRows || got.maxBytes != DefaultPostgresBackfillMaxBytes {
		t.Fatalf("默认值: %+v err=%v", got, err)
	}
	if _, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{MaxRowsPerTable: -1}); err == nil {
		t.Fatal("负行数必须拒绝")
	}
	if _, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{MaxRowsPerTable: MaximumPostgresBackfillMaxRows + 1}); err == nil {
		t.Fatal("超限行数必须拒绝")
	}
	if _, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{MaxBytesPerTable: -1}); err == nil {
		t.Fatal("负字节必须拒绝")
	}
	if _, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{MaxBytesPerTable: MaximumPostgresBackfillMaxBytes + 1}); err == nil {
		t.Fatal("超限字节必须拒绝")
	}
	got, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{MaxRowsPerTable: 5, MaxBytesPerTable: 6})
	if err != nil || got.maxRows != 5 || got.maxBytes != 6 {
		t.Fatalf("显式值必须保留: %+v err=%v", got, err)
	}
}

func TestW12GPostgresProjectionHelpers(t *testing.T) {
	textCol := func(nullable bool) postgresBackfillColumn {
		return postgresBackfillColumn{DataType: "text", UdtName: "text", Nullable: nullable}
	}
	source := map[string]postgresBackfillColumn{"a": textCol(false), "b": textCol(true)}
	target := map[string]postgresBackfillColumn{"a": textCol(false), "b": textCol(true), "c": textCol(true)}

	projection, err := postgresBackfillProjection(source, target, "t")
	if err != nil || len(projection) != 2 {
		t.Fatalf("合法投影: %v err=%v", projection, err)
	}

	if _, err := postgresBackfillProjection(map[string]postgresBackfillColumn{"x": textCol(false)}, target, "t"); err == nil || !strings.Contains(err.Error(), "unmapped legacy source column x") {
		t.Fatalf("仅源列必须拒绝: %v", err)
	}
	drift := map[string]postgresBackfillColumn{"a": {DataType: "integer", UdtName: "int4"}}
	if _, err := postgresBackfillProjection(drift, target, "t"); err == nil || !strings.Contains(err.Error(), "type/nullability mismatch") {
		t.Fatalf("类型漂移必须拒绝: %v", err)
	}
	targetOnlyRequired := map[string]postgresBackfillColumn{"a": textCol(false), "c": textCol(false)}
	if _, err := postgresBackfillProjection(map[string]postgresBackfillColumn{"a": textCol(false)}, targetOnlyRequired, "t"); err == nil || !strings.Contains(err.Error(), "required but absent") {
		t.Fatalf("必需目标列缺失必须拒绝: %v", err)
	}
	if _, err := postgresBackfillProjection(map[string]postgresBackfillColumn{}, map[string]postgresBackfillColumn{}, "t"); err == nil || !strings.Contains(err.Error(), "no public projection") {
		t.Fatalf("空投影必须拒绝: %v", err)
	}

	if _, err := postgresReadbackProjection([]string{"a", "ghost"}, []string{"a"}, "t"); err == nil || !strings.Contains(err.Error(), "unmapped legacy source column ghost") {
		t.Fatalf("readback 仅源列必须拒绝: %v", err)
	}
	if got, err := postgresReadbackProjection([]string{"b", "a"}, []string{"a", "b", "c"}, "t"); err != nil || len(got) != 2 {
		t.Fatalf("readback 投影: %v err=%v", got, err)
	}
	if _, err := postgresReadbackProjection([]string{}, []string{}, "t"); err == nil || !strings.Contains(err.Error(), "no public projection") {
		t.Fatalf("readback 空投影必须拒绝: %v", err)
	}
}

func TestW12GPostgresValueHelpers(t *testing.T) {
	now := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	if postgresBackfillRowBytes([]any{nil, []byte("ab"), "cd", now, int64(7)}) == 0 {
		t.Fatal("字节计不得为 0")
	}
	if !postgresBackfillValuesEqual(nil, nil) || postgresBackfillValuesEqual(nil, "x") {
		t.Fatal("nil 相等语义")
	}
	if !postgresBackfillValuesEqual(now, now.UTC()) || postgresBackfillValuesEqual(now, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("时间相等语义")
	}
	if postgresBackfillValuesEqual(now, "text") {
		t.Fatal("时间与文本不得相等")
	}
	if !postgresBackfillValuesEqual([]byte("a"), "a") {
		t.Fatal("字节与文本按归一化相等")
	}
	if postgresPlaceholders(3) != "$1,$2,$3" {
		t.Fatalf("占位符: %s", postgresPlaceholders(3))
	}
	if postgresPrimaryKeyPredicate([]string{"a", "b"}) != `"a"=$1 AND "b"=$2` {
		t.Fatalf("谓词: %s", postgresPrimaryKeyPredicate([]string{"a", "b"}))
	}
}

func TestW12GNewJ3bReadbackManifestRejections(t *testing.T) {
	verifiedAt := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	base := func() (map[string]string, map[string]int64, map[string]int64, map[string]string, map[string]string) {
		statuses := map[string]string{}
		sourceRows, targetRows := map[string]int64{}, map[string]int64{}
		sdigest, tdigest := map[string]string{}, map[string]string{}
		for _, name := range j3bReadbackRequiredTables() {
			statuses[name] = "match"
			sourceRows[name], targetRows[name] = 1, 1
			sdigest[name], tdigest[name] = w12gDigest(name), w12gDigest(name)
		}
		return statuses, sourceRows, targetRows, sdigest, tdigest
	}
	statuses, srows, trows, sdigest, tdigest := base()
	manifest, err := newJ3bReadbackManifest("p", "legacy-sqlite-dataset+stats", "juhe-j3b-sqlite", statuses, srows, trows, sdigest, tdigest, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables) != len(j3bReadbackRequiredTables()) || manifest.ManifestHash == "" {
		t.Fatalf("完整 manifest: %+v", manifest)
	}

	if _, err := newJ3bReadbackManifest("p", "s", "t", statuses, srows, trows, sdigest, tdigest, J3bReadbackManifestOptions{VerifiedAt: verifiedAt}); err == nil {
		t.Fatal("缺 snapshot identity 必须拒绝")
	}
	if _, err := newJ3bReadbackManifest("p", "s", "t", statuses, srows, trows, sdigest, tdigest, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap"}); err == nil {
		t.Fatal("缺 verified time 必须拒绝")
	}

	badStatus, badSRows, badTRows, badSDigest, badTDigest := base()
	badStatus["model_check_runs"] = "drift"
	if _, err := newJ3bReadbackManifest("p", "s", "t", badStatus, badSRows, badTRows, badSDigest, badTDigest, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt}); err == nil || !strings.Contains(err.Error(), "is not a match") {
		t.Fatalf("非 match 表必须拒绝: %v", err)
	}

	missingEvidence, mSRows, mTRows, mSDigest, mTDigest := base()
	delete(mSDigest, "model_check_runs")
	if _, err := newJ3bReadbackManifest("p", "s", "t", missingEvidence, mSRows, mTRows, mSDigest, mTDigest, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt}); err == nil || !strings.Contains(err.Error(), "incomplete evidence") {
		t.Fatalf("证据不完整必须拒绝: %v", err)
	}
}
