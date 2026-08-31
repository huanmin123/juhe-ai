package modelcheckowner

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresGatewayTrustRuntimeSmoke is opt-in because it writes only to a
// disposable development database supplied by the operator. It exercises the
// actual Gateway path through PgBouncer: durable run, observations, receipts,
// cursor and latest trust result.
func TestPostgresGatewayTrustRuntimeSmoke(t *testing.T) {
	if os.Getenv("J3B_GATEWAY_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3B_GATEWAY_POSTGRES_SMOKE=1 to run the isolated J3b Gateway PostgreSQL smoke")
	}
	dsn := strings.TrimSpace(os.Getenv("JUHE_AI_J3B_POSTGRES_SMOKE_URL"))
	if dsn == "" {
		t.Fatal("JUHE_AI_J3B_POSTGRES_SMOKE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	defer db.Close()
	store := &Store{db: db, mode: "postgres", schema: "juhe_j3b"}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("J3b PostgreSQL schema must be ready before runtime smoke: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "record_model_check"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "status"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`))
		default:
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
		}
	}))
	defer server.Close()
	now := time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC)
	runtime := &Runtime{Store: store, OwnerID: "gateway-pg-smoke", Now: func() time.Time { return now }, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 3}, nil
	}}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "j3b-pg-smoke-system", ActorSystemAccountID: "j3b-pg-smoke-actor", TargetType: "account", TargetID: "j3b-pg-smoke-account", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "pg-smoke-config", PolicyRevision: "pg-smoke-policy", ManualEnforcementEnabled: true, OwnPhysicalAccount: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPostgresGatewayTrustSmoke(t, db, result.RunID)
	var receipts, consumed, latest int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_j3b.model_trust_observation_receipts WHERE observation_id LIKE $1`, result.RunID+"-observation-%").Scan(&receipts); err != nil || receipts == 0 {
		t.Fatalf("trust receipts=%d err=%v", receipts, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_j3b.model_check_observations WHERE run_id=$1 AND aggregation_completed_at IS NOT NULL`, result.RunID).Scan(&consumed); err != nil || consumed != receipts {
		t.Fatalf("consumed=%d receipts=%d err=%v", consumed, receipts, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_j3b.model_account_trust_results WHERE system_account_id='j3b-pg-smoke-system' AND account_id='j3b-pg-smoke-account' AND requested_model='gpt-5.6-sol'`).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("latest=%d err=%v", latest, err)
	}
}

func cleanupPostgresGatewayTrustSmoke(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM juhe_j3b.model_trust_observation_receipts WHERE observation_id LIKE $1`, []any{runID + "-observation-%"}},
		{query: `DELETE FROM juhe_j3b.model_account_trust_results WHERE system_account_id='j3b-pg-smoke-system' AND account_id='j3b-pg-smoke-account' AND requested_model='gpt-5.6-sol'`},
		{query: `DELETE FROM juhe_j3b.model_trust_aggregation_state WHERE scope_key='model-trust-observation-aggregation'`},
		{query: `DELETE FROM juhe_j3b.model_check_execution_claims WHERE input_id IN (SELECT input_id FROM juhe_j3b.model_check_inputs WHERE target_id='j3b-pg-smoke-account')`},
		{query: `DELETE FROM juhe_j3b.model_check_outcomes WHERE input_id IN (SELECT input_id FROM juhe_j3b.model_check_inputs WHERE target_id='j3b-pg-smoke-account')`},
		{`DELETE FROM juhe_j3b.model_check_runs WHERE id=$1`, []any{runID}},
		{query: `DELETE FROM juhe_j3b.model_check_inputs WHERE target_id='j3b-pg-smoke-account'`},
		{query: `DELETE FROM juhe_j3b.model_check_input_versions`},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Errorf("cleanup J3b PostgreSQL runtime smoke: %v", err)
		}
	}
}
