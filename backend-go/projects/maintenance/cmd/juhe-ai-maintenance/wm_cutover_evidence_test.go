package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
)

// 本文件覆盖 --assemble-cutover-evidence 的 CLI 面：互斥枚举、必填 flag
// 预检（exit 2）、临时目录真实文件全流程（组装 → 网关契约链自验通过 →
// exit 0）、自验失败 exit 3 且文件已删、运行错误 exit 1，以及
// --j3b-readback-manifest-out 的接线预检。测试单进程：不 exec 子进程。

var wmCERequiredTables = []string{
	"account_quality_health_hourly",
	"model_check_items",
	"model_check_observations",
	"model_check_runs",
	"model_account_trust_results",
	"model_token_intercept_baseline_versions",
	"model_trust_aggregation_state",
	"model_trust_latest_dirty_accounts",
	"model_trust_observation_receipts",
}

// wmCEManifestBytes 构造自洽的 v2 readback manifest；dropTable 非空时去掉
// 该表（重算 ManifestHash 保持自洽，但证据校验必须拒绝）。
func wmCEManifestBytes(t *testing.T, verifiedAt time.Time, dropTable string) []byte {
	t.Helper()
	manifest := contracts.J3bReadbackManifest{
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		Producer:               "wm-cutover-evidence-test",
		SourceSnapshotIdentity: "wm-ce-snapshot-1",
		SourceSchema:           "juhe_dataset+juhe_stats",
		TargetSchema:           "juhe_j3b",
		ProjectionComplete:     true,
		VerifiedAt:             verifiedAt.UTC().Format(time.RFC3339),
	}
	for _, name := range wmCERequiredTables {
		if name == dropTable {
			continue
		}
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{Name: name, SourceRows: 1, TargetRows: 1, SourceDigest: strings.Repeat("b", 64), TargetDigest: strings.Repeat("b", 64)})
	}
	hash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// wmCEInputs 构造真实备份文件与合法清单文件。
func wmCEInputs(t *testing.T, manifestData []byte) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "migration-backup.tar")
	if err := os.WriteFile(backupPath, []byte("immutable backup payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "readback-manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, backupPath, manifestPath
}

func wmCEAssembleArgs(backupPath, manifestPath, out string) []string {
	return []string{
		"-assemble-cutover-evidence",
		"-cutover-backup-artifact", backupPath,
		"-cutover-readback-manifest", manifestPath,
		"-cutover-owner-epoch", "epoch-1",
		"-cutover-replay-cursor", "batch-20260921-000042",
		"-cutover-evidence-out", out,
	}
}

func TestWMCutoverEvidenceMutexBranches(t *testing.T) {
	branches := []struct {
		name    string
		args    []string
		wantErr string
	}{
		// 组装分支排在 version/check、inventory 与 J3 系列 flag 之后：与这些
		// 命令组合时由组装分支先拦截；排在它之前的命令（import/snapshot/
		// bootstrap/metrics/cutover verify）先拦截并输出各自的消息。
		{"assemble with version", []string{"-assemble-cutover-evidence", "-version"}, "cutover evidence assembly flag is mutually exclusive"},
		{"assemble with check", []string{"-assemble-cutover-evidence", "-check-boundary"}, "cutover evidence assembly flag is mutually exclusive"},
		{"assemble with import", []string{"-assemble-cutover-evidence", "-import-business-dataset"}, "business dataset export/import flags are mutually exclusive"},
		{"assemble with snapshot", []string{"-assemble-cutover-evidence", "-postgres-schema-snapshot"}, "PostgreSQL schema snapshot flag is mutually exclusive"},
		{"assemble with ensure-schema", []string{"-assemble-cutover-evidence", "-ensure-schema"}, "storage bootstrap flags are mutually exclusive"},
		{"assemble with metrics", []string{"-assemble-cutover-evidence", "-check-go-runtime-metrics"}, "Go runtime metrics flags are mutually exclusive"},
		{"assemble with cutover verify", []string{"-assemble-cutover-evidence", "-verify-j3b-cutover-evidence=x"}, "J3b cutover evidence verification is mutually exclusive"},
		{"assemble with inventory", []string{"-assemble-cutover-evidence", "-verify-j3b-model-check-inventory"}, "cutover evidence assembly flag is mutually exclusive"},
		{"assemble with pg readback", []string{"-assemble-cutover-evidence", "-verify-j3b-model-check-postgres-backfill"}, "cutover evidence assembly flag is mutually exclusive"},
		{"assemble with sqlite backfill", []string{"-assemble-cutover-evidence", "-backfill-j3b-model-check-sqlite"}, "cutover evidence assembly flag is mutually exclusive"},
		{"import rejects assemble", []string{"-import-business-dataset", "-assemble-cutover-evidence"}, "business dataset export/import flags are mutually exclusive"},
		{"snapshot rejects assemble", []string{"-postgres-schema-snapshot", "-assemble-cutover-evidence"}, "PostgreSQL schema snapshot flag is mutually exclusive"},
		{"metrics reject assemble", []string{"-check-go-runtime-metrics", "-assemble-cutover-evidence"}, "Go runtime metrics flags are mutually exclusive"},
		{"cutover verify rejects assemble", []string{"-verify-j3b-cutover-evidence=x", "-assemble-cutover-evidence"}, "J3b cutover evidence verification is mutually exclusive"},
		{"inventory rejects assemble", []string{"-verify-j3b-model-check-inventory", "-assemble-cutover-evidence"}, "cutover evidence assembly flag is mutually exclusive"},
		{"pg readback rejects assemble", []string{"-verify-j3b-model-check-postgres-backfill", "-assemble-cutover-evidence"}, "cutover evidence assembly flag is mutually exclusive"},
	}
	for _, item := range branches {
		item := item
		t.Run(item.name, func(t *testing.T) {
			code, _, stderr := wmRunMaintenanceCapture(t, item.args...)
			if code != 2 {
				t.Fatalf("exit=%d want 2\n%s", code, stderr)
			}
			if !strings.Contains(stderr, item.wantErr) {
				t.Fatalf("输出缺少 %q:\n%s", item.wantErr, stderr)
			}
		})
	}
}

func TestWMCutoverEvidenceAssembleFlagPreflight(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"missing owner epoch", []string{"-assemble-cutover-evidence"}, "owner epoch is required"},
		{"missing replay cursor", []string{"-assemble-cutover-evidence", "-cutover-owner-epoch", "epoch-1"}, "replay cursor is required"},
		{"missing backup artifact", []string{"-assemble-cutover-evidence", "-cutover-owner-epoch", "epoch-1", "-cutover-replay-cursor", "cursor-1"}, "backup artifact path is required"},
		{"missing readback manifest", []string{"-assemble-cutover-evidence", "-cutover-owner-epoch", "epoch-1", "-cutover-replay-cursor", "cursor-1", "-cutover-backup-artifact", "x.tar"}, "readback manifest path is required"},
		{"missing evidence out", []string{"-assemble-cutover-evidence", "-cutover-owner-epoch", "epoch-1", "-cutover-replay-cursor", "cursor-1", "-cutover-backup-artifact", "x.tar", "-cutover-readback-manifest", "m.json"}, "evidence output path is required"},
		{"non positive max age", []string{"-assemble-cutover-evidence", "-cutover-owner-epoch", "epoch-1", "-cutover-replay-cursor", "cursor-1", "-cutover-backup-artifact", "x.tar", "-cutover-readback-manifest", "m.json", "-cutover-evidence-out", "e.json", "-cutover-max-age-seconds", "0"}, "max age"},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			code, _, stderr := wmRunMaintenanceCapture(t, item.args...)
			if code != 2 {
				t.Fatalf("exit=%d want 2\n%s", code, stderr)
			}
			if !strings.Contains(stderr, item.wantErr) {
				t.Fatalf("输出缺少 %q:\n%s", item.wantErr, stderr)
			}
		})
	}
	t.Run("nonexistent backup artifact", func(t *testing.T) {
		_, backupPath, manifestPath := wmCEInputs(t, wmCEManifestBytes(t, time.Now(), ""))
		code, _, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(filepath.Join(filepath.Dir(backupPath), "nope.tar"), manifestPath, filepath.Join(t.TempDir(), "evidence.json"))...)
		if code != 2 {
			t.Fatalf("缺失备份必须 exit 2: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "read backup artifact") {
			t.Fatalf("stderr 必须说明备份不可读:\n%s", stderr)
		}
	})
	t.Run("malformed readback manifest", func(t *testing.T) {
		dir, backupPath, _ := wmCEInputs(t, wmCEManifestBytes(t, time.Now(), ""))
		badManifest := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(badManifest, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(backupPath, badManifest, filepath.Join(t.TempDir(), "evidence.json"))...)
		if code != 2 {
			t.Fatalf("畸形清单必须 exit 2: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "decode readback manifest") {
			t.Fatalf("stderr 必须说明清单解码失败:\n%s", stderr)
		}
	})
}

func TestWMCutoverEvidenceAssembleHappyPath(t *testing.T) {
	_, backupPath, manifestPath := wmCEInputs(t, wmCEManifestBytes(t, time.Now(), ""))
	out := filepath.Join(t.TempDir(), "evidence.json")
	code, stdout, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(backupPath, manifestPath, out)...)
	if code != 0 {
		t.Fatalf("exit=%d want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"ready":true`) {
		t.Fatalf("stdout 必须输出就绪报告:\n%s", stdout)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("自验通过后证据文件必须保留: %v", err)
	}
	// 与 go-gateway 相同的契约链对写盘产物复验。
	verifyReport, err := businesshandoff.VerifyJ3bCutoverEvidence(out, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !verifyReport.Ready {
		t.Fatalf("网关契约链必须接受组装产物: %+v", verifyReport.Errors)
	}
}

func TestWMCutoverEvidenceAssembleSelfVerifyExit3(t *testing.T) {
	assertExit3AndRemoved := func(t *testing.T, code int, stdout, stderr, out, fragment string) {
		t.Helper()
		if code != 3 {
			t.Fatalf("exit=%d want 3\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "failed self-verification and was removed") || !strings.Contains(stderr, fragment) {
			t.Fatalf("stderr 必须说明自验失败且文件已删（%s）:\n%s", fragment, stderr)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("未就绪证据必须已删除: %v", err)
		}
	}
	t.Run("expired readback manifest", func(t *testing.T) {
		_, backupPath, manifestPath := wmCEInputs(t, wmCEManifestBytes(t, time.Now().Add(-91*24*time.Hour), ""))
		out := filepath.Join(t.TempDir(), "evidence.json")
		code, _, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(backupPath, manifestPath, out)...)
		assertExit3AndRemoved(t, code, "", stderr, out, "expired")
	})
	t.Run("manifest missing required table", func(t *testing.T) {
		_, backupPath, manifestPath := wmCEInputs(t, wmCEManifestBytes(t, time.Now(), "model_check_runs"))
		out := filepath.Join(t.TempDir(), "evidence.json")
		code, _, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(backupPath, manifestPath, out)...)
		assertExit3AndRemoved(t, code, "", stderr, out, "model_check_runs")
	})
	t.Run("evidence output overwriting backup fails hash gate", func(t *testing.T) {
		_, backupPath, manifestPath := wmCEInputs(t, wmCEManifestBytes(t, time.Now(), ""))
		// 证据写到备份文件自身：组装覆写备份后，自验重读哈希必然不符。
		code, _, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(backupPath, manifestPath, backupPath)...)
		assertExit3AndRemoved(t, code, "", stderr, backupPath, "backupArtifact")
	})
}

func TestWMCutoverEvidenceAssembleRuntimeFailure(t *testing.T) {
	_, backupPath, manifestPath := wmCEInputs(t, wmCEManifestBytes(t, time.Now(), ""))
	out := filepath.Join(filepath.Dir(backupPath), "missing-dir", "evidence.json")
	code, _, stderr := wmRunMaintenanceCapture(t, wmCEAssembleArgs(backupPath, manifestPath, out)...)
	if code != 1 {
		t.Fatalf("exit=%d want 1\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "cutover evidence assembly failed") {
		t.Fatalf("stderr 必须说明组装失败:\n%s", stderr)
	}
}

func TestWMJ3bReadbackManifestOutFlagWiring(t *testing.T) {
	t.Run("missing url still exits two", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := j3bModelCheckPostgresReadbackResultWithManifestOut("", 1, "out.json", "snap-1"); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "requires --j3b-postgres-readback-url") {
			t.Fatalf("stderr 缺少 URL 要求:\n%s", stderr)
		}
	})
	t.Run("manifest out without source snapshot exits two", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := j3bModelCheckPostgresReadbackResultWithManifestOut("postgres://bd@127.0.0.1:1/none", 1, "out.json", ""); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "requires --j3b-readback-source-snapshot") {
			t.Fatalf("stderr 缺少快照标识要求:\n%s", stderr)
		}
	})
	t.Run("runtime failure skips manifest write", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out.json")
		code, _, stderr := wmRunMaintenanceCapture(t,
			"-verify-j3b-model-check-postgres-backfill",
			"-j3b-postgres-readback-url", "postgres://bd@127.0.0.1:1/none",
			"-j3b-postgres-readback-max-rows", "1",
			"-j3b-readback-manifest-out", out,
			"-j3b-readback-source-snapshot", "snap-1")
		if code != 1 {
			t.Fatalf("exit=%d want 1\nstderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "J3b PostgreSQL backfill readback failed") {
			t.Fatalf("stderr 缺少读回失败原因:\n%s", stderr)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("verify 未成功不得写盘: %v", err)
		}
	})
}
