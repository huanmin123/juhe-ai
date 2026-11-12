package instantmigration

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/timeinventory"
)

func TestBuildPlanSelectsTextInstantColumns(t *testing.T) {
	report := timeinventory.Report{Columns: []timeinventory.Column{
		{
			Schema: "juhe_business", Table: "accounts", Column: "created_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
		},
		{
			Schema: "juhe_usage", Table: "usage_records", Column: "created_at", DataType: "text",
			Target:       timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
			Dependencies: timeinventory.Dependencies{PartitionKey: true},
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "generated_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"}, IsGenerated: true,
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "mystery_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant", Review: true},
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "updated_at", DataType: "timestamp with time zone",
			Target: timeinventory.Target{Type: "timestamptz(6)", Kind: "instant"},
		},
	}}
	plan := BuildPlan(report, false, nil)
	if len(plan.Actions) != 1 {
		t.Fatalf("actions = %+v", plan.Actions)
	}
	if plan.Actions[0].Table != "accounts" || plan.Actions[0].Column != "created_at" {
		t.Fatalf("action = %+v", plan.Actions[0])
	}
	if len(plan.Skipped) != 3 {
		t.Fatalf("skipped = %+v", plan.Skipped)
	}
	withReview := BuildPlan(report, true, nil)
	if len(withReview.Actions) != 2 {
		t.Fatalf("includeReview actions = %+v", withReview.Actions)
	}
}

func TestBuildPlanPhase2ScopeExcludesStatsAndUsage(t *testing.T) {
	report := timeinventory.Report{Columns: []timeinventory.Column{
		{
			Schema: "juhe_stats", Table: "usage_stats_daily", Column: "updated_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
		},
		{
			Schema: "juhe_stats", Table: "background_task_runs", Column: "started_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(6)", Kind: "instant"},
		},
		{
			Schema: "juhe_usage", Table: "usage_records", Column: "updated_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
		},
	}}
	plan := BuildPlan(report, false, Phase2Scope)
	if len(plan.Actions) != 1 || plan.Actions[0].Table != "background_task_runs" {
		t.Fatalf("phase2 actions = %+v", plan.Actions)
	}
	if len(plan.Skipped) != 2 {
		t.Fatalf("phase2 skipped = %+v", plan.Skipped)
	}
	all := BuildPlan(report, false, AllScope)
	if len(all.Actions) != 3 {
		t.Fatalf("all actions = %+v", all.Actions)
	}
}

func TestBuildPlanWarnings(t *testing.T) {
	report := timeinventory.Report{Columns: []timeinventory.Column{{
		Schema: "juhe_business", Table: "accounts", Column: "created_at", DataType: "text",
		Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
		Dependencies: timeinventory.Dependencies{
			PrimaryKey:  true,
			Indexes:     []string{"idx_a", "idx_b"},
			Constraints: []string{"accounts_pkey"},
			Triggers:    []string{"trg_a"},
		},
	}}}
	plan := BuildPlan(report, false, nil)
	if len(plan.Actions) != 1 || len(plan.Actions[0].Warnings) < 4 {
		t.Fatalf("warnings = %+v", plan.Actions)
	}
}

func TestBuildPlanSkipsDefaultFKAndView(t *testing.T) {
	report := timeinventory.Report{Columns: []timeinventory.Column{
		{
			Schema: "juhe_business", Table: "accounts", Column: "with_default_at", DataType: "text",
			Target:  timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
			Default: "''::text",
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "fk_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
			Dependencies: timeinventory.Dependencies{
				ReferencedByFKs: []string{"juhe_business.child.fk_account"},
			},
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "view_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
			Dependencies: timeinventory.Dependencies{
				Views: []string{"juhe_business.account_view"},
			},
		},
		{
			Schema: "juhe_business", Table: "accounts", Column: "plain_at", DataType: "text",
			Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
		},
	}}
	plan := BuildPlan(report, false, nil)
	if len(plan.Actions) != 1 || plan.Actions[0].Column != "plain_at" {
		t.Fatalf("actions = %+v", plan.Actions)
	}
	if len(plan.Skipped) != 3 {
		t.Fatalf("skipped = %+v", plan.Skipped)
	}
	reasons := map[string]string{}
	for _, skipped := range plan.Skipped {
		reasons[skipped.Column] = skipped.Reason
	}
	if reasons["with_default_at"] == "" || reasons["fk_at"] == "" || reasons["view_at"] == "" {
		t.Fatalf("skip reasons = %+v", reasons)
	}
}

func TestBatchOf(t *testing.T) {
	cases := []struct {
		schema, table, want string
	}{
		{"juhe_business", "system_accounts", BatchSmallTables},
		{"juhe_business", "api_keys", BatchCRUD},
		{"juhe_business", "resource_authorization_grants", BatchCRUD},
		{"juhe_business", "accounts", BatchAccounts},
		{"juhe_business", "account_list_availability_projections", BatchAccounts},
		{"juhe_business", "proxy_profiles", BatchAccounts},
		{"juhe_dataset", "public_api_logs", BatchDatasetJobs},
		{"juhe_j3b", "model_check_runs", BatchDatasetJobs},
		{"juhe_stats", "background_task_runs", BatchDatasetJobs},
	}
	for _, tc := range cases {
		if got := BatchOf(tc.schema, tc.table); got != tc.want {
			t.Fatalf("BatchOf(%s,%s) = %s, want %s", tc.schema, tc.table, got, tc.want)
		}
	}
}

func TestAlterSQLQuotesIdentifiers(t *testing.T) {
	column := timeinventory.Column{
		Schema: "juhe_business", Table: "accounts", Column: "created_at",
		Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
	}
	got := AlterSQL(column)
	want := `ALTER TABLE "juhe_business"."accounts" ALTER COLUMN "created_at" TYPE timestamptz(3) USING "created_at"::timestamptz`
	if got != want {
		t.Fatalf("AlterSQL = %s", got)
	}
}

func TestTextToInstantAction(t *testing.T) {
	action := TextToInstantAction("juhe_business", "system_accounts", "last_login_at", "timestamptz(3)")
	want := `ALTER TABLE "juhe_business"."system_accounts" ALTER COLUMN "last_login_at" TYPE timestamptz(3) USING "last_login_at"::timestamptz`
	if action.SQL != want {
		t.Fatalf("SQL = %s", action.SQL)
	}
	if action.Batch != BatchSmallTables || action.CurrentType != "text" {
		t.Fatalf("action = %+v", action)
	}
}

func TestPrecisionFromType(t *testing.T) {
	cases := map[string]struct {
		value int
		ok    bool
	}{
		"timestamptz(3)": {3, true},
		"timestamptz(6)": {6, true},
		"timestamptz":    {0, false},
		"date":           {0, false},
	}
	for input, want := range cases {
		value, ok := precisionFromType(input)
		if value != want.value || ok != want.ok {
			t.Fatalf("precisionFromType(%q) = %d,%v want %d,%v", input, value, ok, want.value, want.ok)
		}
	}
}
