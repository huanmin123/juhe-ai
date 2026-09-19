package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/ownermanifest"
)

// 本文件原以 go build + 运行编译产物的方式验证 CLI 契约；按“测试单进程纪律”
// （docs/develop/后端测试分层规则.md “测试进程纪律（硬性）”章节）改写为进程内
// 直调 runMaintenance（wmRunMaintenanceCapture 捕获退出码与 stdout/stderr），
// 退出码与输出断言语义不变。“编译产物 + 真实启动”的验证走 acceptance/ 冒烟
// 体系，不进 go test。

func TestMaintenanceCommandDefaultsUseFinalArchive(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	// 归档裁剪后 db-service 契约源与 system-api-app.ts 缺席（对照
	// ownermanifest 包内同款跳过边界）：CLI 默认指向 final-archive 的
	// 校验前提不存在时跳过。
	if _, err := os.Stat(filepath.Join(root, "migration-backup", "node", "final-archive",
		"backend", "src", "modules", "db-service", "db-service-types.ts")); err != nil {
		t.Skip("Node archive db-service contract sources absent (trimmed archive)")
	}

	code, output, stderr := wmRunMaintenanceCapture(t, "-verify-business-owner-manifest")
	if code != 0 {
		t.Fatalf("business owner manifest exit=%d output=%s stderr=%s", code, output, stderr)
	}
	var ownerReport ownermanifest.Report
	if err := json.Unmarshal([]byte(output), &ownerReport); err != nil || ownerReport.Operations == 0 {
		t.Fatalf("business owner manifest report=%s err=%v", output, err)
	}

	code, output, stderr = wmRunMaintenanceCapture(t, "-verify-gateway-route-owner-manifest")
	if code != 3 {
		t.Fatalf("gateway owner manifest exit=%d output=%s stderr=%s", code, output, stderr)
	}
	var routeReport ownermanifest.GatewayRouteOwnerReport
	if err := json.Unmarshal([]byte(output), &routeReport); err != nil || len(routeReport.PendingFamilies) == 0 {
		t.Fatalf("gateway owner manifest report=%s err=%v", output, err)
	}

	code, output, stderr = wmRunMaintenanceCapture(t, "-scan-node-j3b-active-path")
	if code != 0 {
		t.Fatalf("node active path scan exit=%d output=%s stderr=%s", code, output, stderr)
	}
	var activeReport ownermanifest.ActivePathReport
	if err := json.Unmarshal([]byte(output), &activeReport); err != nil || activeReport.ScannedFiles != 0 || activeReport.BlockedFindings != 0 {
		t.Fatalf("node active path report=%s err=%v", output, err)
	}
}

func TestJ3bCutoverEvidenceExitCodeKeepsUnreadyGateClosed(t *testing.T) {
	if got := j3bCutoverEvidenceExitCode(businesshandoff.J3bCutoverEvidenceReport{}); got != 3 {
		t.Fatalf("unready cutover evidence exit code=%d, want 3", got)
	}
	if got := j3bCutoverEvidenceExitCode(businesshandoff.J3bCutoverEvidenceReport{Ready: true}); got != 0 {
		t.Fatalf("ready cutover evidence exit code=%d, want 0", got)
	}
}

func TestJ3bInventoryExitCodeKeepsUnreadyGateClosed(t *testing.T) {
	if got := j3bInventoryExitCode(j3bmodelcheck.LegacyJ3bFactCoverageReport{}); got != 3 {
		t.Fatalf("unready inventory exit code=%d, want 3", got)
	}
	if got := j3bInventoryExitCode(j3bmodelcheck.LegacyJ3bFactCoverageReport{Ready: true}); got != 0 {
		t.Fatalf("ready inventory exit code=%d, want 0", got)
	}
}

func TestMaintenanceCommandRejectsJ3bInventoryWithoutEvidencePath(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-verify-j3b-model-check-inventory")
	if code != 2 {
		t.Fatalf("missing inventory evidence exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "requires --j3b-inventory-evidence") {
		t.Fatalf("missing inventory evidence stderr=%q", stderr)
	}
}

func TestMaintenanceCommandAcceptsCompleteJ3bInventoryEvidence(t *testing.T) {
	evidence := make(map[string]j3bmodelcheck.LegacyJ3bFactEvidence, len(j3bmodelcheck.LegacyJ3bFactInventory))
	for _, item := range j3bmodelcheck.LegacyJ3bFactInventory {
		evidence[item.Name] = j3bmodelcheck.LegacyJ3bFactEvidence{
			SourceSchema:             item.SourceSchema,
			SourceTable:              item.SourceTable,
			Scope:                    item.Scope,
			Digest:                   "sha256:" + strings.Repeat("a", 64),
			BackfillReadbackVerified: item.Disposition == j3bmodelcheck.LegacyFactBackfill,
			RetentionVerified:        item.Disposition == j3bmodelcheck.LegacyFactRetain,
		}
	}
	path := filepath.Join(t.TempDir(), "inventory-evidence.json")
	data, err := json.Marshal(j3bmodelcheck.LegacyJ3bFactEvidenceDocument{Facts: evidence})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, output, stderr := wmRunMaintenanceCapture(t, "-verify-j3b-model-check-inventory", "-j3b-inventory-evidence", path)
	if code != 0 {
		t.Fatalf("complete inventory evidence failed: exit=%d\noutput=%s\nstderr=%s", code, output, stderr)
	}
	var report j3bmodelcheck.LegacyJ3bFactCoverageReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode inventory report: %v\n%s", err, output)
	}
	if !report.Ready || !report.InventoryComplete {
		t.Fatalf("complete inventory evidence report=%+v", report)
	}
}

func TestJ3bPostgresReadbackExitCodeKeepsUnreadyGateClosed(t *testing.T) {
	if got := j3bPostgresReadbackExitCode(j3bmodelcheck.PostgresBackfillVerificationReport{}); got != 3 {
		t.Fatalf("unready readback exit code=%d, want 3", got)
	}
	if got := j3bPostgresReadbackExitCode(j3bmodelcheck.PostgresBackfillVerificationReport{Ready: true}); got != 0 {
		t.Fatalf("ready readback exit code=%d, want 0", got)
	}
}

func TestJ3bSQLiteReadbackExitCodeRequiresCompleteProjection(t *testing.T) {
	if got := j3bSQLiteReadbackExitCode(j3bmodelcheck.BackfillVerificationReport{Ready: true}); got != 3 {
		t.Fatalf("ready-only readback exit code=%d, want 3", got)
	}
	if got := j3bSQLiteReadbackExitCode(j3bmodelcheck.BackfillVerificationReport{Ready: true, ProjectionComplete: false, Complete: false}); got != 3 {
		t.Fatalf("lossy readback exit code=%d, want 3", got)
	}
	if got := j3bSQLiteReadbackExitCode(j3bmodelcheck.BackfillVerificationReport{Ready: true, ProjectionComplete: true, Complete: true}); got != 0 {
		t.Fatalf("complete readback exit code=%d, want 0", got)
	}
}

func TestJ3bPostgresReadbackRequiresExplicitURL(t *testing.T) {
	if got := j3bPostgresReadbackURLRequiredExitCode(""); got != 2 {
		t.Fatalf("missing URL exit code=%d, want 2", got)
	}
	if got := j3bPostgresReadbackURLRequiredExitCode("postgres://reader@db.example.invalid:5432/juhe"); got != 0 {
		t.Fatalf("explicit URL exit code=%d, want 0", got)
	}
}

func TestJ3bPostgresBackfillPreflightRequiresURLAndAllConfirmations(t *testing.T) {
	validURL := "postgres://reader@db.example.invalid:5432/juhe"
	tests := []struct {
		name                                   string
		url                                    string
		nodeStopped, goStopped, backupVerified bool
		want                                   int
	}{
		{name: "missing url", url: "", nodeStopped: true, goStopped: true, backupVerified: true, want: 2},
		{name: "node running", url: validURL, nodeStopped: false, goStopped: true, backupVerified: true, want: 2},
		{name: "go running", url: validURL, nodeStopped: true, goStopped: false, backupVerified: true, want: 2},
		{name: "backup unverified", url: validURL, nodeStopped: true, goStopped: true, backupVerified: false, want: 2},
		{name: "all confirmed", url: validURL, nodeStopped: true, goStopped: true, backupVerified: true, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := j3bPostgresBackfillPreflightExitCode(test.url, test.nodeStopped, test.goStopped, test.backupVerified); got != test.want {
				t.Fatalf("preflight exit code=%d, want %d", got, test.want)
			}
		})
	}
}

func TestGoRuntimeMetricsApplyPreflightRequiresURLAndAllConfirmations(t *testing.T) {
	validURL := "postgres://metrics@db.example.invalid:5432/juhe"
	tests := []struct {
		name                                   string
		url                                    string
		nodeStopped, goStopped, backupVerified bool
		want                                   int
	}{
		{name: "missing url", url: "", nodeStopped: true, goStopped: true, backupVerified: true, want: 2},
		{name: "node running", url: validURL, nodeStopped: false, goStopped: true, backupVerified: true, want: 2},
		{name: "go running", url: validURL, nodeStopped: true, goStopped: false, backupVerified: true, want: 2},
		{name: "backup unverified", url: validURL, nodeStopped: true, goStopped: true, backupVerified: false, want: 2},
		{name: "all confirmed", url: validURL, nodeStopped: true, goStopped: true, backupVerified: true, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := goRuntimeMetricsApplyPreflightExitCode(test.url, test.nodeStopped, test.goStopped, test.backupVerified); got != test.want {
				t.Fatalf("preflight exit code=%d, want %d", got, test.want)
			}
		})
	}
}

func TestMaintenanceCommandRejectsGoRuntimeMetricsCheckWithoutURL(t *testing.T) {
	t.Setenv("JUHE_AI_MAINTENANCE_GO_RUNTIME_METRICS_POSTGRES_URL", "")
	code, _, stderr := wmRunMaintenanceCapture(t, "-check-go-runtime-metrics")
	if code != 2 {
		t.Fatalf("missing Go runtime metrics URL exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "requires --go-runtime-metrics-postgres-url") {
		t.Fatalf("missing Go runtime metrics URL stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsGoRuntimeMetricsApplyWithoutConfirmations(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-apply-go-runtime-metrics", "-go-runtime-metrics-postgres-url", "postgres://metrics@db.example.invalid:5432/juhe")
	if code != 2 {
		t.Fatalf("missing confirmation exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("missing confirmation stderr=%q", stderr)
	}
}

func TestJ3bBackfillEvidencePreflightUsesCutoverValidatorExitCodes(t *testing.T) {
	if _, got, err := j3bBackfillEvidencePreflight(""); got != 2 || err == nil {
		t.Fatalf("missing evidence preflight=(exit %d, err %v), want exit 2 with error", got, err)
	}
	if _, got, err := j3bBackfillEvidencePreflight(filepath.Join(t.TempDir(), "missing.json")); got != 2 || err == nil {
		t.Fatalf("unreadable evidence preflight=(exit %d, err %v), want exit 2 with error", got, err)
	}
	malformed := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(malformed, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report, got, err := j3bBackfillEvidencePreflight(malformed); got != 3 || err != nil || report.Ready {
		t.Fatalf("malformed evidence preflight=(report %+v, exit %d, err %v), want unready exit 3", report, got, err)
	}
}

func TestJ3bBackfillEvidencePreflightRejectsLegacyScalarDigestEvidence(t *testing.T) {
	backupPath := filepath.Join(t.TempDir(), "backup.bin")
	backupData := []byte("backup")
	if err := os.WriteFile(backupPath, backupData, 0o600); err != nil {
		t.Fatal(err)
	}
	backupDigest := sha256.Sum256(backupData)
	now := time.Now().UTC()
	evidence := businesshandoff.J3bCutoverEvidence{
		OldOwner:             "node",
		NewOwner:             contracts.J3bGatewayCutoverOwner,
		OwnerEpoch:           "epoch-1",
		DrainCompleted:       true,
		InFlight:             0,
		ActivePathZero:       true,
		BackupArtifact:       businesshandoff.J3bBackupArtifact{Path: backupPath, Hash: hex.EncodeToString(backupDigest[:])},
		RollbackReplayCursor: "cursor-1",
		SourceDigest:         strings.Repeat("a", 64),
		TargetDigest:         strings.Repeat("a", 64),
		BlockedFindings:      0,
		Freshness:            businesshandoff.J3bEvidenceFreshness{CapturedAt: now.Format(time.RFC3339), MaxAgeSeconds: 60},
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, got, err := j3bBackfillEvidencePreflight(path)
	if err != nil || got != 3 || report.Ready {
		t.Fatalf("legacy scalar evidence preflight=(report %+v, exit %d, err %v), want unready exit 3", report, got, err)
	}
}

func TestJ3bBackfillEvidencePreflightRejectsLegacyEvidenceWithoutTargetDigest(t *testing.T) {
	evidencePath := writeCompleteJ3bCutoverEvidence(t)
	data, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence businesshandoff.J3bCutoverEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	evidence.TargetDigest = ""
	data, err = json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, got, err := j3bBackfillEvidencePreflight(evidencePath)
	if err != nil || got != 3 || report.Ready {
		t.Fatalf("legacy pre-backfill evidence=(report %+v, exit %d, err %v), want unready exit 3", report, got, err)
	}
}

func TestMaintenanceCommandRejectsJ3bPostgresReadbackWithoutURL(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-verify-j3b-model-check-postgres-backfill")
	if code != 2 {
		t.Fatalf("missing explicit readback URL exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "requires --j3b-postgres-readback-url") {
		t.Fatalf("missing explicit readback URL stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsMalformedJ3bPostgresReadbackURL(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-verify-j3b-model-check-postgres-backfill", "-j3b-postgres-readback-url", "sqlite:///legacy.db")
	if code != 2 {
		t.Fatalf("malformed readback URL exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "必须提供包含主机、数据库和显式角色") {
		t.Fatalf("malformed readback URL stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsJ3bPostgresBackfillWithoutConfirmations(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-backfill-j3b-model-check-postgres", "-j3b-postgres-backfill-url", "postgres://reader@db.example.invalid:5432/juhe")
	if code != 2 {
		t.Fatalf("missing confirmation exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("missing confirmation stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsJ3bPostgresBackfillWithoutCutoverEvidence(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-backfill-j3b-model-check-postgres", "-j3b-postgres-backfill-url", "postgres://reader@db.example.invalid:5432/juhe", "-node-stopped", "-go-stopped", "-backup-confirmed")
	if code != 2 {
		t.Fatalf("missing cutover evidence exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "requires --j3b-backfill-evidence") {
		t.Fatalf("missing cutover evidence stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsMixedBackfillAndCutoverEvidenceModes(t *testing.T) {
	evidence := writeCompleteJ3bCutoverEvidence(t)
	code, _, stderr := wmRunMaintenanceCapture(t, "-verify-j3b-cutover-evidence", evidence, "-j3b-backfill-evidence", evidence)
	if code != 2 {
		t.Fatalf("mixed evidence modes exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("mixed evidence modes stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsJ3bPostgresBackfillWithValidEvidenceButMissingConfirmations(t *testing.T) {
	evidence := writeCompleteJ3bCutoverEvidence(t)
	code, _, stderr := wmRunMaintenanceCapture(t, "-backfill-j3b-model-check-postgres", "-j3b-postgres-backfill-url", "postgres://reader@db.example.invalid:5432/juhe", "-j3b-backfill-evidence", evidence)
	if code != 2 {
		t.Fatalf("missing confirmation exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("missing confirmation stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsJ3bSQLiteBackfillWithoutCutoverEvidence(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-backfill-j3b-model-check-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed")
	if code != 2 {
		t.Fatalf("missing cutover evidence exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "requires --j3b-backfill-evidence") {
		t.Fatalf("missing cutover evidence stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsMalformedJ3bPostgresBackfillEvidence(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(evidence, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := wmRunMaintenanceCapture(t, "-backfill-j3b-model-check-postgres", "-j3b-postgres-backfill-url", "postgres://reader@db.example.invalid:5432/juhe", "-node-stopped", "-go-stopped", "-backup-confirmed", "-j3b-backfill-evidence", evidence)
	if code != 3 {
		t.Fatalf("malformed cutover evidence exit=%d, want exit status 3; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "decode J3b cutover evidence") {
		t.Fatalf("malformed cutover evidence stderr=%q", stderr)
	}
}

func writeCompleteJ3bCutoverEvidence(t *testing.T) string {
	t.Helper()
	backupPath := filepath.Join(t.TempDir(), "backup.bin")
	backupData := []byte("backup")
	if err := os.WriteFile(backupPath, backupData, 0o600); err != nil {
		t.Fatal(err)
	}
	backupDigest := sha256.Sum256(backupData)
	evidence := businesshandoff.J3bCutoverEvidence{
		OldOwner: "node", NewOwner: contracts.J3bGatewayCutoverOwner, OwnerEpoch: "epoch-1", DrainCompleted: true,
		ActivePathZero:       true,
		BackupArtifact:       businesshandoff.J3bBackupArtifact{Path: backupPath, Hash: hex.EncodeToString(backupDigest[:])},
		RollbackReplayCursor: "cursor-1", SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64),
		Freshness: businesshandoff.J3bEvidenceFreshness{CapturedAt: time.Now().UTC().Format(time.RFC3339), MaxAgeSeconds: 60},
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
