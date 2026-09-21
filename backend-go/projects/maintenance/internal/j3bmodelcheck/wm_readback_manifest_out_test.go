package j3bmodelcheck

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// TestWMWriteJ3bReadbackManifestFile 覆盖 --j3b-readback-manifest-out 的写盘
// 函数：verify 成功报告 → v2 manifest JSON 落盘且可被 Decode 对称读回；未就绪
// 报告与不可写路径失败闭环。
func TestWMWriteJ3bReadbackManifestFile(t *testing.T) {
	tables, rows, digests := completeReadbackEvidence()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	report := PostgresBackfillVerificationReport{
		Ready: true, TransactionReadOnly: true,
		Tables: tables, SourceRows: rows, TargetRows: rows,
		SourceDigest: digests, TargetDigest: digests,
		SourceExceededRowLimit: map[string]bool{}, TargetExceededRowLimit: map[string]bool{},
	}
	options := J3bReadbackManifestOptions{SourceSnapshotIdentity: "wm-out-1", VerifiedAt: now}

	out := filepath.Join(t.TempDir(), "readback-manifest.json")
	written, err := WriteJ3bReadbackManifestFile(out, report, options)
	if err != nil {
		t.Fatal(err)
	}
	if written != out {
		t.Fatalf("写出路径不一致: %q", written)
	}
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := contracts.DecodeJ3bReadbackManifest(data)
	if err != nil {
		t.Fatalf("写出的 manifest 必须可被 Decode 对称读回: %v", err)
	}
	if manifest.ManifestHash == "" || manifest.FormatVersion != contracts.J3bReadbackManifestFormatVersion {
		t.Fatalf("manifest 头部字段缺失: %+v", manifest)
	}
	if len(manifest.Tables) != len(j3bReadbackRequiredTables()) {
		t.Fatalf("manifest 表数 %d != 9", len(manifest.Tables))
	}
	if errors := contracts.ValidateJ3bReadbackManifest(manifest, now, 1); len(errors) != 0 {
		t.Fatalf("写盘前自校验必须通过: %v", errors)
	}

	// 未就绪报告（缺少各表证据）必须拒绝转换。
	if _, err := WriteJ3bReadbackManifestFile(filepath.Join(t.TempDir(), "x.json"), PostgresBackfillVerificationReport{Ready: true}, options); err == nil {
		t.Fatal("未就绪报告必须拒绝写盘")
	}
	// 写出目录缺失必须报错。
	if _, err := WriteJ3bReadbackManifestFile(filepath.Join(t.TempDir(), "missing", "x.json"), report, options); err == nil {
		t.Fatal("目录缺失必须报错")
	}
}
