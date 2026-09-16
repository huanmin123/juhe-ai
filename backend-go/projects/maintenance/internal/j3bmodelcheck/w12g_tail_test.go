package j3bmodelcheck

// w12g 波次：收尾覆盖——inventory 未知 disposition 的证据缺失分支、
// readback manifest 的共享契约校验失败分支。

import (
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func TestW12GUnknownDispositionWithoutEvidence(t *testing.T) {
	inventory := []LegacyJ3bFact{{
		Name: "model_paired_similarity_windows", SourceSchema: "juhe_stats", SourceTable: "model_paired_similarity_windows",
		Disposition: "w12g-unknown-disposition",
	}}
	report := ValidateLegacyJ3bFactCoverage(inventory, map[string]LegacyJ3bFactEvidence{})
	if report.Facts["model_paired_similarity_windows"] != "coverage evidence missing" {
		t.Fatalf("未知 disposition 且无证据必须报告 coverage evidence missing: %+v", report.Facts)
	}
}

func TestW12GNewJ3bReadbackManifestContractValidation(t *testing.T) {
	verifiedAt := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	statuses := map[string]string{}
	sourceRows, targetRows := map[string]int64{}, map[string]int64{}
	sourceDigest, targetDigest := map[string]string{}, map[string]string{}
	for _, name := range j3bReadbackRequiredTables() {
		statuses[name] = "match"
		sourceRows[name], targetRows[name] = 1, 1
		sourceDigest[name] = w12gDigest(name)
		targetDigest[name] = w12gDigest(name)
	}
	// 不受支持的 schema 组合在共享契约校验处失败闭环。
	_, err := newJ3bReadbackManifest("p", "unsupported-source", "unsupported-target", statuses, sourceRows, targetRows, sourceDigest, targetDigest,
		J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt})
	if err == nil || !stringsContains(err.Error(), "is invalid") {
		t.Fatalf("不受支持 schema 必须失败闭环: %v", err)
	}

	sqliteReport := BackfillVerificationReport{Ready: true, ProjectionComplete: false, Complete: false}
	if _, err := NewSQLiteJ3bReadbackManifest(sqliteReport, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt}); err == nil || !stringsContains(err.Error(), "not complete") {
		t.Fatalf("SQLite 报告未完成必须拒绝: %v", err)
	}
	pgReport := PostgresBackfillVerificationReport{Ready: true, TransactionReadOnly: false}
	if _, err := NewPostgresJ3bReadbackManifest(pgReport, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt}); err == nil || !stringsContains(err.Error(), "not complete") {
		t.Fatalf("PG 报告非只读必须拒绝: %v", err)
	}
	pgReportReady := PostgresBackfillVerificationReport{Ready: true, TransactionReadOnly: true}
	if _, err := NewPostgresJ3bReadbackManifest(pgReportReady, J3bReadbackManifestOptions{SourceSnapshotIdentity: "snap", VerifiedAt: verifiedAt}); err == nil {
		t.Fatal("PG 证据不完整必须拒绝")
	}
	_ = contracts.J3bReadbackManifestFormatVersion
}

func stringsContains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestW12GBackfillDigestMismatchReported(t *testing.T) {
	inventory := []LegacyJ3bFact{{
		Name: "model_check_runs", SourceSchema: "juhe_dataset", SourceTable: "model_check_runs",
		Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs",
	}}
	evidence := map[string]LegacyJ3bFactEvidence{
		"model_check_runs": {
			SourceSchema: "juhe_dataset", SourceTable: "model_check_runs",
			Digest: w12gDigest("ok"), BackfillReadbackVerified: true,
			SourceDigest: "aaaa", TargetDigest: "bbbb",
		},
	}
	report := ValidateLegacyJ3bFactCoverage(inventory, evidence)
	if report.Facts["model_check_runs"] != "backfill digest mismatch" {
		t.Fatalf("digest 不一致必须报告: %+v", report.Facts)
	}
}

func TestW12GBackfillReadbackUnverifiedBranch(t *testing.T) {
	inventory := []LegacyJ3bFact{{
		Name: "model_check_runs", SourceSchema: "juhe_dataset", SourceTable: "model_check_runs",
		Disposition: LegacyFactBackfill, TargetSchema: SchemaName, TargetTable: "model_check_runs",
	}}
	evidence := map[string]LegacyJ3bFactEvidence{
		"model_check_runs": {
			SourceSchema: "juhe_dataset", SourceTable: "model_check_runs",
			Digest: w12gDigest("ok"), BackfillReadbackVerified: false,
		},
	}
	report := ValidateLegacyJ3bFactCoverage(inventory, evidence)
	if report.Facts["model_check_runs"] != "backfill readback unverified" {
		t.Fatalf("readback 未验证必须报告: %+v", report.Facts)
	}
}
