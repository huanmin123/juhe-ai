package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestParseRunListQueryForwardsNodeCompatibleFilters(t *testing.T) {
	request := httptest.NewRequest("GET", "/runs?page=2&pageSize=25&targetId=acct-1&model=gpt-5.6&level=failure&status=failed&triggerKind=quality_recovery&startAt=2026-08-29T10%3A00%3A00Z&endAt=2026-08-29T11%3A00%3A00Z", nil)
	query, err := parseRunListQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if query.Page != 2 || query.PageSize != 25 || query.TargetID != "acct-1" || query.Model != "gpt-5.6" || query.Level != "failure" || query.Status != "failed" || query.TriggerKind != "quality_recovery" || query.StartAt != "2026-08-29T10:00:00Z" || query.EndAt != "2026-08-29T11:00:00Z" {
		t.Fatalf("query=%+v", query)
	}
}

func TestRuntimeListRunsFiltersAndUsesBoundedPagination(t *testing.T) {
	runtime, db := queryRuntimeFixture(t)
	defer db.Close()
	seedQueryRuns(t, db)

	filtered, err := runtime.ListRuns(context.Background(), RunListQuery{
		SystemAccountID: "sys-1",
		Page:            1,
		PageSize:        1,
		TargetID:        "acct-1",
		Model:           "gpt-5.6",
		Level:           "failure",
		Status:          "failed",
		TriggerKind:     "quality_recovery",
		StartAt:         "2026-08-29T10:00:00Z",
		EndAt:           "2026-08-29T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := filtered.(RunListResult)
	if !ok {
		t.Fatalf("result type=%T", filtered)
	}
	if result.Page != 1 || result.PageSize != 1 || result.Total != 1 || result.HasMore || len(result.Items) != 1 {
		t.Fatalf("filtered result=%+v", result)
	}
	item := result.Items[0]
	if item.ID != "run-filter" || item.ProviderCode != "openai" || item.TriggerKind != "quality_recovery" || item.CreatedAt != "2026-08-29T10:00:00Z" {
		t.Fatalf("filtered item=%+v", item)
	}

	first, err := runtime.ListRuns(context.Background(), RunListQuery{SystemAccountID: "sys-1", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	firstPage := first.(RunListResult)
	if !firstPage.HasMore || firstPage.Total != 3 || firstPage.Page != 1 || firstPage.PageSize != 2 || len(firstPage.Items) != 2 || firstPage.Items[0].ID != "run-latest" || firstPage.Items[1].ID != "run-middle" {
		t.Fatalf("first page=%+v", firstPage)
	}

	second, err := runtime.ListRuns(context.Background(), RunListQuery{SystemAccountID: "sys-1", Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	secondPage := second.(RunListResult)
	if secondPage.HasMore || secondPage.Total != 4 || len(secondPage.Items) != 2 || secondPage.Items[0].ID != "run-filter" || secondPage.Items[1].ID != "run-earliest" {
		t.Fatalf("second page=%+v", secondPage)
	}
	for _, page := range []RunListResult{firstPage, secondPage} {
		for _, view := range page.Items {
			if view.SystemAccountID != "sys-1" {
				t.Fatalf("out-of-scope item returned: %+v", view)
			}
		}
	}
}

func TestRuntimeGetRunUsesFoundSignalForMissingRun(t *testing.T) {
	runtime, db := queryRuntimeFixture(t)
	defer db.Close()
	seedQueryRuns(t, db)

	result, found, err := runtime.GetRun(context.Background(), "missing")
	if err != nil || found || result != nil {
		t.Fatalf("missing result=%#v found=%v err=%v", result, found, err)
	}
	result, found, err = runtime.GetRun(context.Background(), "run-filter")
	if err != nil || !found {
		t.Fatalf("existing result=%#v found=%v err=%v", result, found, err)
	}
	detail, ok := result.(RunDetail)
	if !ok || detail.ID != "run-filter" || detail.TriggerKind != "quality_recovery" || detail.CreatedAt != "2026-08-29T10:00:00Z" {
		t.Fatalf("existing detail=%#v", result)
	}
	if string(detail.RequestSummary) != `{"targetId":"acct-1"}` || string(detail.ResultSummary) != `{"score":50}` || len(detail.Checks) != 1 || detail.Checks[0].ItemKey != "response" {
		t.Fatalf("durable detail=%+v", detail)
	}
}

func TestRuntimeGetRunRejectsInvalidDurableJSON(t *testing.T) {
	runtime, db := queryRuntimeFixture(t)
	defer db.Close()
	seedQueryRuns(t, db)
	if _, err := db.Exec(`UPDATE model_check_runs SET result_summary_json='not-json' WHERE id='run-filter'`); err != nil {
		t.Fatal(err)
	}
	if result, found, err := runtime.GetRun(context.Background(), "run-filter"); err == nil || found || result != nil {
		t.Fatalf("result=%#v found=%v err=%v", result, found, err)
	}
}

func queryRuntimeFixture(t *testing.T) (*Runtime, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "query.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE model_check_runs (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		model TEXT NOT NULL,
		profile TEXT NOT NULL,
		trigger_kind TEXT NOT NULL,
		status TEXT NOT NULL,
		level TEXT NOT NULL,
		message TEXT NOT NULL,
		score INTEGER NOT NULL,
		max_score INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		request_summary_json TEXT NOT NULL,
		result_summary_json TEXT NOT NULL
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE model_check_items (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		item_key TEXT NOT NULL,
		item_type TEXT NOT NULL,
		status TEXT NOT NULL,
		score INTEGER NOT NULL,
		max_score INTEGER NOT NULL,
		duration_ms INTEGER,
		trace_id TEXT,
		evidence_summary_json TEXT NOT NULL,
		error_code TEXT,
		error_message TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return &Runtime{Store: &Store{db: db, mode: "sqlite"}}, db
}

func seedQueryRuns(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, row := range []struct {
		id, systemAccountID, targetID, model, triggerKind, status, level, createdAt string
	}{
		{"run-latest", "sys-1", "acct-1", "gpt-5.6", "manual", "completed", "success", "2026-08-29T12:00:00Z"},
		{"run-middle", "sys-1", "acct-2", "gpt-5.6", "scheduled", "completed", "success", "2026-08-29T11:00:00Z"},
		{"run-filter", "sys-1", "acct-1", "gpt-5.6", "quality_recovery", "failed", "failure", "2026-08-29T10:00:00Z"},
		{"run-earliest", "sys-1", "acct-1", "gpt-4.1", "manual", "completed", "success", "2026-08-29T09:00:00Z"},
		{"run-other-scope", "sys-2", "acct-1", "gpt-5.6", "quality_recovery", "failed", "failure", "2026-08-29T13:00:00Z"},
	} {
		if _, err := db.Exec(`INSERT INTO model_check_runs(id,system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,level,message,score,max_score,created_at,request_summary_json,result_summary_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.id, row.systemAccountID, "openai", "account", row.targetID, row.model, "full", row.triggerKind, row.status, row.level, "message", 50, 100, row.createdAt, `{"targetId":"`+row.targetID+`"}`, `{"score":50}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_items(id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES ('item-filter','run-filter','response','upstream','passed',50,100,NULL,NULL,?,NULL,NULL,'2026-08-29T10:00:00Z','2026-08-29T10:00:00Z')`, string(json.RawMessage(`{"response":"OK"}`))); err != nil {
		t.Fatal(err)
	}
}
