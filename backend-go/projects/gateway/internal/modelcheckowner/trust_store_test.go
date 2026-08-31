package modelcheckowner

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

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
	if _, err := store.db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-a','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','protocol_basic','complete','verified','unmapped','passed',100,?)`, created); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_trust_latest_dirty_accounts(system_account_id,account_id,requested_model,dirty_reason,updated_at) VALUES ('sys','acct','gpt-5.6','baseline_changed',?)`, created); err != nil {
		t.Fatal(err)
	}
	projection := TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "verified", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, ReasonCodes: []string{"z", "a", "a"}}}
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
	if _, err := store.db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-z','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','usage_shape','complete','verified','unmapped','passed',100,?)`, created); err != nil {
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
