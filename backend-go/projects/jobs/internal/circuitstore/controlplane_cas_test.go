package circuitstore

import (
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// casTestInput 构造最小合法 SUSPECT CAS 输入（account scope）。
func casTestInput(scopeKey, accountID, state string, generation, dispatchRevision, updatedAtMS int64) opsjobs.IncidentCASInput {
	return opsjobs.IncidentCASInput{Incident: opsjobs.IncidentCASRow{
		CircuitScopeKey:                 scopeKey,
		AccountID:                       accountID,
		AccountRuntimeKey:               accountID,
		ScopeKind:                       "account",
		IncidentID:                      "incident-" + scopeKey,
		ChildIncidentIDs:                []string{},
		State:                           state,
		Generation:                      generation,
		DispatchRevision:                dispatchRevision,
		TransitionID:                    "tr-" + scopeKey,
		ConfirmationFailuresRequired:    2,
		ConfirmationFailureEvidenceKeys: []string{},
		UpstreamAttemptObserved:         true,
		UpdatedAtMS:                     updatedAtMS,
	}}
}

func mustAccount(t *testing.T, db *sql.DB, id string, dispatch int64, deleted bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, dispatch_revision) VALUES ($1, $2)`, id, dispatch); err != nil {
		t.Fatal(err)
	}
	if deleted {
		if _, err := db.Exec(`UPDATE juhe_business.accounts SET deleted_at = '2020-01-01T00:00:00Z' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

