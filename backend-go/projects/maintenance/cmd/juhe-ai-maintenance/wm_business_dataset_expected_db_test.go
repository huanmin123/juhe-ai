package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businessdataset"
)

// 本文件覆盖 --business-dataset-expected-target-db /
// --business-dataset-expected-source-db 的 CLI 接线：任一提供即启用对应
// fail-closed 门（不匹配或不可核实 → exit 2），两者都未提供时行为与既有
// 导入路径完全一致。数据库全链路正/反例由 internal/businessdataset 包内
// fake 覆盖；cmd 层用最小 manifest 与不可达地址验证预检与出口码
//（测试单进程纪律：不 exec 子进程、不依赖真实 PostgreSQL）。

// wmBDDatasetDir 写出一个可解码的最小 manifest（字段名与契约一致）。
func wmBDDatasetDir(t *testing.T, sourceIdentity string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := `{"formatVersion":"business-dataset-manifest/v1","sourceIdentity":"` + sourceIdentity + `","targetIdentity":"authoritative-business-postgres"}`
	if err := os.WriteFile(filepath.Join(dir, "business-dataset-manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const wmBDUnreachableURL = "postgres://bd@127.0.0.1:1/none"

func TestBDBusinessDatasetImportExpectedDBFlags(t *testing.T) {
	t.Run("neither flag provided keeps legacy behavior", func(t *testing.T) {
		dir := wmBDDatasetDir(t, "prod-source-db")
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = businessDatasetImportResultWithExpectedDB(wmBDUnreachableURL, dir, nil, "", "")
		})
		if code != 3 {
			t.Fatalf("未提供 flag 必须走既有 blocker 路径（exit 3）: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stdout, `"blockers"`) {
			t.Fatalf("stdout 必须输出 blocker 明细:\n%s", stdout)
		}
	})
	t.Run("expected source matching manifest passes preflight", func(t *testing.T) {
		dir := wmBDDatasetDir(t, "prod-source-db")
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = businessDatasetImportResultWithExpectedDB(wmBDUnreachableURL, dir, nil, "", "prod-source-db")
		})
		if code != 3 {
			t.Fatalf("源库标识一致必须放行进入导入路径（exit 3 而非 2）: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stdout, `"blockers"`) {
			t.Fatalf("stdout 必须输出 blocker 明细:\n%s", stdout)
		}
	})
	t.Run("expected source mismatch exits two", func(t *testing.T) {
		dir := wmBDDatasetDir(t, "prod-source-db")
		var code int
		_, stderr := bdCaptureOutput(t, func() {
			code = businessDatasetImportResultWithExpectedDB(wmBDUnreachableURL, dir, nil, "", "other-source-db")
		})
		if code != 2 {
			t.Fatalf("源库标识不匹配必须 exit 2: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "sourceIdentity mismatch") {
			t.Fatalf("stderr 必须说明源库标识不匹配:\n%s", stderr)
		}
	})
	t.Run("placeholder source identity exits two even when matching", func(t *testing.T) {
		dir := wmBDDatasetDir(t, businessdataset.DefaultTargetIdentity)
		var code int
		_, stderr := bdCaptureOutput(t, func() {
			code = businessDatasetImportResultWithExpectedDB(wmBDUnreachableURL, dir, nil, "", businessdataset.DefaultTargetIdentity)
		})
		if code != 2 {
			t.Fatalf("占位源标识必须 exit 2: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "placeholder") {
			t.Fatalf("stderr 必须说明占位值:\n%s", stderr)
		}
	})
	t.Run("expected target with unreachable database fails closed", func(t *testing.T) {
		dir := wmBDDatasetDir(t, "prod-source-db")
		var code int
		_, stderr := bdCaptureOutput(t, func() {
			code = businessDatasetImportResultWithExpectedDB(wmBDUnreachableURL, dir, nil, "authoritative-target-db", "")
		})
		if code != 2 {
			t.Fatalf("无法核实目标库标识必须 fail-closed exit 2: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "could not verify the target database identity") {
			t.Fatalf("stderr 必须说明核实失败:\n%s", stderr)
		}
	})
	t.Run("runMaintenance dispatch wires both flags", func(t *testing.T) {
		dir := wmBDDatasetDir(t, "prod-source-db")
		code, _, stderr := wmRunMaintenanceCapture(t,
			"-import-business-dataset",
			"-business-dataset-url", wmBDUnreachableURL,
			"-business-dataset-dir", dir,
			"-business-dataset-expected-source-db", "other-source-db",
			"-business-dataset-expected-target-db", "authoritative-target-db")
		if code != 2 {
			t.Fatalf("runMaintenance 分发后源库不匹配必须 exit 2: code=%d stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "sourceIdentity mismatch") {
			t.Fatalf("stderr 必须说明源库标识不匹配:\n%s", stderr)
		}
	})
}
