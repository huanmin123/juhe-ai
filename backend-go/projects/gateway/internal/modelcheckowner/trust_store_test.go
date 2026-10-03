package modelcheckowner

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCompareTrustCursorUsesParsedInstants(t *testing.T) {
	for _, test := range []struct {
		name, left, right string
		want              int
	}{
		{name: "whole second before fractional", left: "2026-08-31T10:00:00Z", right: "2026-08-31T10:00:00.1Z", want: -1},
		{name: "shorter fractional before longer", left: "2026-08-31T10:00:00.12345678Z", right: "2026-08-31T10:00:00.123456789Z", want: -1},
		{name: "offsets compare by instant", left: "2026-08-31T11:00:00+01:00", right: "2026-08-31T10:00:00.1Z", want: -1},
		{name: "same instant uses ID", left: "2026-08-31T10:00:00Z", right: "2026-08-31T11:00:00+01:00", want: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := compareTrustCursor(test.left, "id-a", test.right, "id-b"); got != test.want {
				t.Fatalf("compareTrustCursor(%q,%q)=%d, want %d", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestProjectTrustReceiptsCursorAndLatestAreReplaySafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range runtimeTestDDL() {
		if _, err := seed.Exec(ddl); err != nil {
			seed.Close()
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const created = "2026-08-31T10:00:00Z"
	if _, err := store.db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-a','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','protocol_basic','complete','consistent','unknown','consistent',100,?)`, created); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_trust_latest_dirty_accounts(system_account_id,account_id,requested_model,dirty_reason,updated_at) VALUES ('sys','acct','gpt-5.6','baseline_changed',?)`, created); err != nil {
		t.Fatal(err)
	}
	projection := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "insufficient_evidence", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100, ReasonCodes: []string{"z", "a", "a"}}}
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	var receipts, consumed, dirty int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_trust_observation_receipts`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE aggregation_completed_at IS NOT NULL`).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("consumed=%d err=%v", consumed, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_trust_latest_dirty_accounts`).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("dirty=%d err=%v", dirty, err)
	}
	var lastID, reasons string
	if err := store.db.QueryRow(`SELECT last_observed_id,reason_codes_json FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6'`).Scan(&lastID, &reasons); err != nil || lastID != "obs-a" || reasons != `["a","z"]` {
		t.Fatalf("latest lastID=%q reasons=%q err=%v", lastID, reasons, err)
	}
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatalf("identical trust projection must replay: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-z','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','usage_shape','complete','consistent','unknown','consistent',100,?)`, created); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatalf("later ID at the same cursor timestamp must advance: %v", err)
	}
	if err := store.db.QueryRow(`SELECT last_observed_id FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6'`).Scan(&lastID); err != nil || lastID != "obs-z" {
		t.Fatalf("latest lastID=%q err=%v", lastID, err)
	}
	conflict := projection
	conflict.Report.IdentityStatus = "suspected_downgrade"
	if err := store.ProjectTrust(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("same cursor with different trust facts must fail closed, err=%v", err)
	}
}

func TestTrustProtocolStatusPreservesWarning(t *testing.T) {
	if got := trustProtocolStatus([]trustObservation{{protocolStatus: "consistent"}, {protocolStatus: "warning"}}); got != "warning" {
		t.Fatalf("protocol status=%q, want warning", got)
	}
	if got := trustProtocolStatus([]trustObservation{{protocolStatus: "consistent"}, {protocolStatus: "failed"}}); got != "failed" {
		t.Fatalf("protocol status=%q, want failed", got)
	}
}

func TestTrustMappingStatusMixedFailsClosed(t *testing.T) {
	if got := trustMappingStatus([]trustObservation{{mappingStatus: "direct"}, {mappingStatus: "configured_mapping"}}); got != "mixed" {
		t.Fatalf("mapping status=%q, want mixed before persistence normalization", got)
	}
	path := filepath.Join(t.TempDir(), "mixed-trust.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range runtimeTestDDL() {
		if _, err := seed.Exec(ddl); err != nil {
			seed.Close()
			t.Fatal(err)
		}
	}
	created := "2026-08-31T10:00:00Z"
	if _, err := seed.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-a','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','protocol_basic','complete','consistent','direct','consistent',100,?),('obs-b','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','usage_shape','complete','consistent','configured_mapping','consistent',100,?)`, created, created); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projection := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "unknown", UsageIntegrityStatus: "insufficient_evidence", ProtocolStatus: "consistent", EvidenceStatus: "stable", TrustScore: 1, EvidenceCoverage: 100}}
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	var mapping, reasons string
	if err := store.db.QueryRow(`SELECT mapping_status,reason_codes_json FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6'`).Scan(&mapping, &reasons); err != nil {
		t.Fatal(err)
	}
	if mapping != "unknown" || !strings.Contains(reasons, "mapping_status_conflict") {
		t.Fatalf("mapping=%q reasons=%q, mixed mapping must persist unknown with conflict reason", mapping, reasons)
	}
}

func TestTrustMappingStatusInvalidFailsClosed(t *testing.T) {
	if got := trustMappingStatus([]trustObservation{{mappingStatus: "legacy_unmapped"}}); got != "legacy_unmapped" {
		t.Fatalf("mapping status=%q, helper should expose invalid source for persistence normalization", got)
	}
	// The durable path shares the same fail-closed normalization as mixed data;
	// validTrustReport does not permit arbitrary historical vocabularies.
	if validTrustReport(TrustReport{IdentityStatus: "consistent", MappingStatus: "legacy_unmapped", UsageIntegrityStatus: "insufficient_evidence", ProtocolStatus: "consistent", EvidenceStatus: "stable", TrustScore: 1}) {
		t.Fatal("invalid mapping vocabulary must not be accepted as a trust report")
	}
}

func openTrustProjectionStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trust-projection.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range runtimeTestDDL() {
		if _, err := seed.Exec(ddl); err != nil {
			seed.Close()
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func insertTrustProjectionObservation(t *testing.T, store *Store, id, runID, createdAt string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES (?,?,'sys','acct','openai','gpt-5.6','gpt-5.6','protocol_basic','complete','consistent','direct','consistent',100,?)`, id, runID, createdAt); err != nil {
		t.Fatal(err)
	}
}

func trustLatestUsageStatus(t *testing.T, store *Store) string {
	t.Helper()
	var usage string
	if err := store.db.QueryRow(`SELECT usage_integrity_status FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6'`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestProjectTrustPersistsUsageIntegrityTruth(t *testing.T) {
	store := openTrustProjectionStore(t)
	insertTrustProjectionObservation(t, store, "obs-a", "run-1", "2026-08-31T10:00:00Z")
	projection := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "suspected_padding", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100}}
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if usage := trustLatestUsageStatus(t, store); usage != "suspected_padding" {
		t.Fatalf("usage=%q, projection truth must reach the latest row instead of the pinned constant", usage)
	}
	// The usage column participates in the same-cursor comparison, so an
	// identical projection must still replay cleanly.
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatalf("identical projection must replay with the usage column in the comparison: %v", err)
	}
}

func TestProjectTrustKeepsConclusiveUsageWithoutNewEvidence(t *testing.T) {
	store := openTrustProjectionStore(t)
	insertTrustProjectionObservation(t, store, "obs-a", "run-1", "2026-08-31T10:00:00Z")
	conclusive := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "consistent", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100}}
	if err := store.ProjectTrust(context.Background(), conclusive); err != nil {
		t.Fatal(err)
	}
	insertTrustProjectionObservation(t, store, "obs-b", "run-2", "2026-08-31T10:01:00Z")
	noEvidence := TrustProjection{RunID: "run-2", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "insufficient_evidence", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100}}
	if err := store.ProjectTrust(context.Background(), noEvidence); err != nil {
		t.Fatal(err)
	}
	if usage := trustLatestUsageStatus(t, store); usage != "consistent" {
		t.Fatalf("usage=%q, an insufficient_evidence projection must keep the stored conclusive state", usage)
	}
}

func TestProjectTrustReplayConflictsOnUsageStatusChange(t *testing.T) {
	store := openTrustProjectionStore(t)
	insertTrustProjectionObservation(t, store, "obs-a", "run-1", "2026-08-31T10:00:00Z")
	projection := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "consistent", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100}}
	if err := store.ProjectTrust(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	conflict := projection
	conflict.Report.UsageIntegrityStatus = "suspected_padding"
	if err := store.ProjectTrust(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("same cursor with a different usage status must fail closed, err=%v", err)
	}
}
