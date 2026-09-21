package businessdataset

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 本文件覆盖期望库标识两个 fail-closed 门（ExpectedTargetIdentity 对实际
// current_database()、ExpectedSourceIdentity 对 manifest.SourceIdentity 含
// 占位值拒绝）的正/反/未提供路径；CLI 层接线由
// cmd/juhe-ai-maintenance 的 TestBDBusinessDatasetImportExpectedDBFlags 覆盖。

// bdRewriteManifestSourceIdentity 把已导出数据集 manifest 的源库标识改写为
// 指定值并重算 ManifestHash，保持 manifest 其余自洽性不变。
func bdRewriteManifestSourceIdentity(t *testing.T, dir, sourceIdentity string) {
	t.Helper()
	path := filepath.Join(dir, ManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := contracts.DecodeBusinessDatasetManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceIdentity = sourceIdentity
	hash, err := contracts.ComputeBusinessDatasetManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func bdImportIntoFreshTarget(t *testing.T, dir string, opts ImportOptions) (ImportReport, int, error) {
	t.Helper()
	catalog := bdNewTarget(t)
	db := openBDFakePG(catalog)
	defer db.Close()
	report, err := ImportBusinessDataset(context.Background(), db, dir, opts)
	return report, catalog.commits, err
}

func TestBDImportExpectedIdentityGates(t *testing.T) {
	t.Run("neither expectation provided stays green", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{})
		if err != nil || !report.Ready() || commits != 1 {
			t.Fatalf("未提供期望不得启用任何门: err=%v ready=%v commits=%d", err, report.Ready(), commits)
		}
	})
	t.Run("target expectation matching actual database passes", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{ExpectedTargetIdentity: "juhe_target_db"})
		if err != nil || !report.Ready() || commits != 1 {
			t.Fatalf("与实际目标库一致必须放行: err=%v ready=%v commits=%d", err, report.Ready(), commits)
		}
		if !report.TargetIdentityMatch || report.TargetIdentity != "juhe_target_db" {
			t.Fatalf("目标标识记录错误: %+v", report)
		}
	})
	t.Run("target expectation mismatch is a blocker", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{ExpectedTargetIdentity: "another-db"})
		if err != nil {
			t.Fatalf("目标库不匹配属于 blocker: %v", err)
		}
		if report.Ready() || commits != 0 {
			t.Fatalf("导错目标库必须整体阻断: ready=%v commits=%d", report.Ready(), commits)
		}
		if !strings.Contains(strings.Join(report.Blockers, "; "), "目标库标识不匹配") {
			t.Fatalf("blocker 必须说明目标库标识不匹配: %+v", report.Blockers)
		}
	})
	t.Run("source expectation matching manifest identity passes", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{ExpectedSourceIdentity: "juhe_source_db"})
		if err != nil || !report.Ready() || commits != 1 {
			t.Fatalf("与 manifest 源库标识一致必须放行: err=%v ready=%v commits=%d", err, report.Ready(), commits)
		}
	})
	t.Run("source expectation mismatch is a blocker", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{ExpectedSourceIdentity: "another-source-db"})
		if err != nil {
			t.Fatalf("源库不匹配属于 blocker: %v", err)
		}
		if report.Ready() || commits != 0 {
			t.Fatalf("源库标识不匹配必须整体阻断: ready=%v commits=%d", report.Ready(), commits)
		}
		if !strings.Contains(strings.Join(report.Blockers, "; "), "manifest 源库标识不匹配") {
			t.Fatalf("blocker 必须说明源库标识不匹配: %+v", report.Blockers)
		}
	})
	t.Run("placeholder source identity is refused even when it matches", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		bdRewriteManifestSourceIdentity(t, outDir, DefaultTargetIdentity)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{ExpectedSourceIdentity: DefaultTargetIdentity})
		if err != nil {
			t.Fatalf("占位源标识属于 blocker: %v", err)
		}
		if report.Ready() || commits != 0 {
			t.Fatalf("占位源标识必须整体阻断: ready=%v commits=%d", report.Ready(), commits)
		}
		if !strings.Contains(strings.Join(report.Blockers, "; "), "占位值") {
			t.Fatalf("blocker 必须说明占位值: %+v", report.Blockers)
		}
	})
	t.Run("placeholder source identity without expectation stays green", func(t *testing.T) {
		outDir, _ := bdExportForTest(t)
		bdRewriteManifestSourceIdentity(t, outDir, DefaultTargetIdentity)
		report, commits, err := bdImportIntoFreshTarget(t, outDir, ImportOptions{})
		if err != nil || !report.Ready() || commits != 1 {
			t.Fatalf("未提供期望时占位源标识不得阻断: err=%v ready=%v commits=%d", err, report.Ready(), commits)
		}
	})
	t.Run("IsPlaceholderIdentity matrix", func(t *testing.T) {
		for value, want := range map[string]bool{
			"":                                  true,
			"   ":                               true,
			DefaultTargetIdentity:               true,
			" authoritative-business-postgres ": true,
			"juhe_source_db":                    false,
		} {
			if got := IsPlaceholderIdentity(value); got != want {
				t.Fatalf("IsPlaceholderIdentity(%q)=%v want %v", value, got, want)
			}
		}
	})
}
