package modelcheckstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestReadRunsRespectsScopeAndReturnsTypedDetail(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, input := range []RunInput{
		{ID: "run-1", SystemAccountID: "scope-a", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct-a", TargetName: "Account A", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: now, RequestSummary: []byte(`{"inputId":"one"}`), PolicySnapshot: []byte(`{"revision":"1"}`)},
		{ID: "run-2", SystemAccountID: "scope-b", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct-b", TargetName: "Account B", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: now.Add(time.Second)},
	} {
		if err := store.CreateRun(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	duration := int64(7)
	if err := store.ProjectOutcome(ctx, OutcomeProjection{RunID: "run-1", Items: []ItemInput{{ID: "item-1", RunID: "run-1", ItemKey: "responses_basic", ItemType: "probe", Status: ItemPassed, Score: 100, MaxScore: 100, DurationMS: &duration, EvidenceSummary: []byte(`{"model":"gpt-5.6-sol"}`)}}, Status: RunCompleted, Level: "passed", Score: 100, MaxScore: 100, Message: "ok", FinishedAt: now.Add(time.Minute), ResultSummary: []byte(`{"score":100}`), QualityDecision: []byte(`{"result":"not_triggered"}`)}); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListRuns(ctx, RunListOptions{SystemAccountID: "scope-a", Page: 1, PageSize: 20})
	if err != nil || list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != "run-1" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if _, found, err := store.GetRun(ctx, "run-1", "scope-b"); err != nil || found {
		t.Fatalf("foreign scope found=%v err=%v", found, err)
	}
	detail, found, err := store.GetRun(ctx, "run-1", "scope-a")
	if err != nil || !found || detail.TargetName != "Account A" || detail.RequestSummary["inputId"] != "one" || detail.ResultSummary["score"].(float64) != 100 || len(detail.Checks) != 1 || detail.Checks[0].EvidenceSummary["model"] != "gpt-5.6-sol" {
		t.Fatalf("detail=%+v found=%v err=%v", detail, found, err)
	}
}

func TestReadRunsRejectsExcessivePageSize(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewStore(db)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRuns(context.Background(), RunListOptions{Page: 1, PageSize: 101}); err == nil {
		t.Fatal("expected page size error")
	}
}

func TestReadRunsMatchesNodeFiltersAndPagedTotalUpperBound(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	for _, input := range []RunInput{
		{ID: "old", SystemAccountID: "scope", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "one", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: now},
		{ID: "new", SystemAccountID: "scope", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "two", Model: "gpt-5.6-terra", Profile: "quick", Trigger: TriggerScheduled, ProbeSetVersion: "v1", StartedAt: now.Add(time.Second)},
	} {
		if err := store.CreateRun(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.ListRuns(ctx, RunListOptions{SystemAccountID: "scope", TargetType: "account", Model: "gpt-5.6-sol", Page: 1, PageSize: 1})
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != "old" || result.Total != 1 || result.HasMore {
		t.Fatalf("filtered result=%+v err=%v", result, err)
	}
	result, err = store.ListRuns(ctx, RunListOptions{SystemAccountID: "scope", TargetType: "account", Page: 1, PageSize: 1})
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != "new" || result.Total != 2 || !result.HasMore {
		t.Fatalf("paged result=%+v err=%v", result, err)
	}
	result, err = store.ListRuns(ctx, RunListOptions{SystemAccountID: "scope", Model: "not-supported", Level: "not-a-level", Status: "bad", TriggerKind: "bad", Page: 1, PageSize: 20})
	if err != nil || len(result.Items) != 2 {
		t.Fatalf("invalid Node filters must be ignored: result=%+v err=%v", result, err)
	}
}
