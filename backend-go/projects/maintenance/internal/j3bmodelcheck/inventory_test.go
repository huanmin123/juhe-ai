package j3bmodelcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyJ3bFactInventoryCoversNodeDatasetAndStatsFacts(t *testing.T) {
	want := map[string]string{
		"model_check_runs":                                                           "juhe_dataset",
		"model_check_items":                                                          "juhe_dataset",
		"model_check_observations":                                                   "juhe_dataset",
		"account_quality_health_hourly":                                              "juhe_stats",
		"model_token_integrity_windows":                                              "juhe_stats",
		"model_token_integrity_rounds":                                               "juhe_stats",
		"model_token_intercept_baseline_versions":                                    "juhe_stats",
		"model_trust_window_sources":                                                 "juhe_stats",
		"model_identity_source_features":                                             "juhe_stats",
		"model_identity_baseline_versions":                                           "juhe_stats",
		"model_paired_similarity_windows":                                            "juhe_stats",
		"model_account_trust_results":                                                "juhe_stats",
		"model_trust_latest_dirty_accounts":                                          "juhe_stats",
		"model_trust_observation_receipts":                                           "juhe_stats",
		"stats_job_state:model-trust-observation-aggregation":                        "juhe_stats",
		"background_job_leases:scheduled:model-trust-observation-aggregation:global": "juhe_stats",
	}
	if len(LegacyJ3bFactInventory) != len(want) {
		t.Fatalf("inventory count=%d want=%d", len(LegacyJ3bFactInventory), len(want))
	}
	for _, item := range LegacyJ3bFactInventory {
		if schema, ok := want[item.Name]; !ok || schema != item.SourceSchema {
			t.Fatalf("unexpected inventory entry: %+v", item)
		}
		if item.Disposition != LegacyFactBackfill && item.Disposition != LegacyFactRetain {
			t.Fatalf("entry %s has no valid disposition", item.Name)
		}
	}
}

func TestLegacyJ3bFactCoverageFailsClosedWithoutEvidence(t *testing.T) {
	report := ValidateLegacyJ3bFactCoverage(LegacyJ3bFactInventory, nil)
	if !report.InventoryComplete {
		t.Fatalf("declared inventory must be structurally complete: %+v", report)
	}
	if report.Ready {
		t.Fatalf("declarations are not migration evidence: %+v", report)
	}
	if got := report.Facts["model_check_runs"]; got != "backfill readback unverified" {
		t.Fatalf("backfill status=%q", got)
	}
	if got := report.Facts["model_token_integrity_windows"]; got != "immutable retention unverified" {
		t.Fatalf("retention status=%q", got)
	}
}

func TestLegacyJ3bFactCoverageRejectsMissingTargetAndRetentionDecisions(t *testing.T) {
	broken := append([]LegacyJ3bFact(nil), LegacyJ3bFactInventory...)
	for index := range broken {
		switch broken[index].Name {
		case "model_check_runs":
			broken[index].TargetTable = ""
		case "model_token_integrity_windows":
			broken[index].RetentionWhy = ""
		}
	}
	report := ValidateLegacyJ3bFactCoverage(broken, nil)
	if report.InventoryComplete || report.Ready {
		t.Fatalf("missing mapping/decision must fail closed: %+v", report)
	}
	if got := report.Facts["model_check_runs"]; got != "target mapping missing" {
		t.Fatalf("target status=%q", got)
	}
	if got := report.Facts["model_token_integrity_windows"]; got != "retention decision missing" {
		t.Fatalf("retention status=%q", got)
	}
}

func TestLegacyJ3bFactCoverageNeedsEveryKnownSourceFact(t *testing.T) {
	partial := append([]LegacyJ3bFact(nil), LegacyJ3bFactInventory[:len(LegacyJ3bFactInventory)-1]...)
	report := ValidateLegacyJ3bFactCoverage(partial, nil)
	if report.InventoryComplete || report.Ready {
		t.Fatalf("omitted legacy fact must fail closed: %+v", report)
	}
	if got := report.Facts["background_job_leases:scheduled:model-trust-observation-aggregation:global"]; got != "coverage decision missing" {
		t.Fatalf("omission status=%q", got)
	}
}

func TestLegacyJ3bFactCoverageReadyOnlyWithMatchingIndependentEvidence(t *testing.T) {
	evidence := make(map[string]LegacyJ3bFactEvidence, len(LegacyJ3bFactInventory))
	for _, item := range LegacyJ3bFactInventory {
		evidence[item.Name] = LegacyJ3bFactEvidence{
			SourceSchema:             item.SourceSchema,
			SourceTable:              item.SourceTable,
			Scope:                    item.Scope,
			Digest:                   "sha256:" + strings.Repeat("a", 64),
			BackfillReadbackVerified: item.Disposition == LegacyFactBackfill,
			RetentionVerified:        item.Disposition == LegacyFactRetain,
		}
	}
	report := ValidateLegacyJ3bFactCoverage(LegacyJ3bFactInventory, evidence)
	if !report.InventoryComplete || !report.Ready {
		t.Fatalf("complete independently evidenced coverage must be ready: %+v", report)
	}
	if message := FormatLegacyJ3bFactCoverageFailure(report); message != "" {
		t.Fatalf("ready report error=%q", message)
	}

	evidence["unknown"] = LegacyJ3bFactEvidence{BackfillReadbackVerified: true}
	// Evidence for unknown names cannot silently satisfy an omitted inventory
	// fact, and an extra inventory entry is itself rejected below.
	withUnknown := append(append([]LegacyJ3bFact(nil), LegacyJ3bFactInventory...), LegacyJ3bFact{Name: "unknown", SourceSchema: "juhe_stats", SourceTable: "unknown", Disposition: LegacyFactRetain, RetentionWhy: "not allowed"})
	report = ValidateLegacyJ3bFactCoverage(withUnknown, evidence)
	if report.Ready || !strings.Contains(FormatLegacyJ3bFactCoverageFailure(report), "unknown: unknown inventory entry") {
		t.Fatalf("unknown fact must be visible and fail closed: %+v", report)
	}
}

func TestLegacyJ3bFactCoverageRejectsEvidenceWithoutSourceScopeOrDigest(t *testing.T) {
	evidence := map[string]LegacyJ3bFactEvidence{
		"model_check_runs": {BackfillReadbackVerified: true},
	}
	report := ValidateLegacyJ3bFactCoverage(LegacyJ3bFactInventory, evidence)
	if report.Ready {
		t.Fatalf("incomplete evidence must fail closed: %+v", report)
	}
	if got := report.Facts["model_check_runs"]; got != "source/scope/digest evidence missing" {
		t.Fatalf("missing evidence status=%q", got)
	}
}

func TestLegacyJ3bFactCoverageRejectsNonHashDigest(t *testing.T) {
	evidence := map[string]LegacyJ3bFactEvidence{
		"model_check_runs": {
			SourceSchema:             "juhe_dataset",
			SourceTable:              "model_check_runs",
			Scope:                    "",
			Digest:                   "sha256:not-a-digest",
			BackfillReadbackVerified: true,
		},
	}
	report := ValidateLegacyJ3bFactCoverage(LegacyJ3bFactInventory, evidence)
	if report.Ready || report.Facts["model_check_runs"] != "source/scope/digest evidence missing" {
		t.Fatalf("non-hash digest must fail closed: %+v", report)
	}
}

func TestLoadLegacyJ3bFactEvidenceRequiresExplicitScopeAndDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	document := LegacyJ3bFactEvidenceDocument{Facts: map[string]LegacyJ3bFactEvidence{
		"model_check_runs": {
			SourceSchema:             "juhe_dataset",
			SourceTable:              "model_check_runs",
			BackfillReadbackVerified: true,
		},
	}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := LoadLegacyJ3bFactEvidence(path)
	if err != nil {
		t.Fatal(err)
	}
	report := ValidateLegacyJ3bFactCoverage(LegacyJ3bFactInventory, evidence)
	if report.Ready || report.Facts["model_check_runs"] != "source/scope/digest evidence missing" {
		t.Fatalf("omitted scope/digest must remain blocked: %+v", report)
	}
}

func TestLegacyJ3bFactCoverageRejectsMismatchedReadbackDigests(t *testing.T) {
	evidence := make(map[string]LegacyJ3bFactEvidence, len(LegacyJ3bFactInventory))
	for _, item := range LegacyJ3bFactInventory {
		evidence[item.Name] = LegacyJ3bFactEvidence{
			SourceSchema:             item.SourceSchema,
			SourceTable:              item.SourceTable,
			Scope:                    item.Scope,
			Digest:                   "sha256:" + strings.Repeat("a", 64),
			BackfillReadbackVerified: item.Disposition == LegacyFactBackfill,
			RetentionVerified:        item.Disposition == LegacyFactRetain,
		}
	}
	evidence["model_check_runs"] = LegacyJ3bFactEvidence{
		SourceSchema:             "juhe_dataset",
		SourceTable:              "model_check_runs",
		Scope:                    "",
		SourceDigest:             "sha256:" + strings.Repeat("a", 64),
		TargetDigest:             "sha256:" + strings.Repeat("b", 64),
		BackfillReadbackVerified: true,
	}
	report := ValidateLegacyJ3bFactCoverage(LegacyJ3bFactInventory, evidence)
	if report.Ready || report.Facts["model_check_runs"] != "backfill digest mismatch" {
		t.Fatalf("digest mismatch must remain blocked: %+v", report)
	}
}

func TestLegacyJ3bFactNamesAreSorted(t *testing.T) {
	names := LegacyJ3bFactNames()
	if len(names) != len(LegacyJ3bFactInventory) {
		t.Fatalf("names=%d inventory=%d", len(names), len(LegacyJ3bFactInventory))
	}
	for index := 1; index < len(names); index++ {
		if names[index-1] > names[index] {
			t.Fatalf("names are not sorted: %#v", names)
		}
	}
}
