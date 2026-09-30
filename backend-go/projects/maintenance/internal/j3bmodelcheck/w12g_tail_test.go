package j3bmodelcheck

// w12g 波次：收尾覆盖——inventory 未知 disposition 的证据缺失分支、
// readback manifest 的共享契约校验失败分支。

import (
	"testing"
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
