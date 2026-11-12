package timeinventory

import (
	"database/sql"
	"testing"
)

func precision(value int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(value), Valid: true}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		schema    string
		table     string
		column    string
		dataType  string
		precision sql.NullInt64
		wantType  string
		wantKind  string
		review    bool
	}{
		{"usage created_at", "juhe_usage", "usage_records", "created_at", "text", sql.NullInt64{}, "timestamptz(3)", "instant", false},
		{"usage watermark", "juhe_usage", "usage_records", "usage_stats_aggregated_at", "text", sql.NullInt64{}, "timestamptz(6)", "instant", false},
		{"stats date", "juhe_stats", "usage_stats_daily", "stat_date", "text", sql.NullInt64{}, "date", "calendar_date", false},
		{"stats week", "juhe_stats", "usage_stats_weekly", "stat_week", "text", sql.NullInt64{}, "date", "calendar_date", false},
		{"stats month", "juhe_stats", "usage_stats_monthly", "stat_month", "text", sql.NullInt64{}, "date", "calendar_date", false},
		{"stats hour", "juhe_stats", "usage_stats_hourly", "stat_hour", "text", sql.NullInt64{}, "timestamp(0) without time zone", "calendar_hour", false},
		{"stats minute", "juhe_stats", "usage_stats_minute", "stat_minute", "text", sql.NullInt64{}, "timestamp(0) without time zone", "calendar_hour", false},
		{"ledger converged", "juhe_business", "schema_state", "converged_at", "text", sql.NullInt64{}, "text", "exception", false},
		{"ledger started", "juhe_business", "schema_migrations", "started_at", "text", sql.NullInt64{}, "text", "exception", false},
		{"window start date", "juhe_stats", "usage_overview_summary_windows", "start_date", "text", sql.NullInt64{}, "text", "exception", false},
		{"release date", "juhe_business", "models", "release_date", "text", sql.NullInt64{}, "text", "exception", false},
		{"sort key", "juhe_business", "account_list_availability_projections", "created_at_sort_key", "text", sql.NullInt64{}, "text", "exception", false},
		{"cursor id", "juhe_stats", "stats_job_state", "cursor_id", "text", sql.NullInt64{}, "text", "exception", false},
		{"expires anchor enum", "juhe_business", "accounts", "expires_after_anchor", "text", sql.NullInt64{}, "text", "exception", false},
		{"already timestamptz", "juhe_business", "proxy_profiles", "created_at", "timestamp with time zone", precision(6), "timestamptz(6)", "instant", false},
		{"already timestamptz no typmod", "juhe_dataset", "audit_logs", "created_at", "timestamp with time zone", sql.NullInt64{}, "timestamptz", "instant", false},
		{"date type", "juhe_business", "models", "release_date", "date", sql.NullInt64{}, "date", "calendar_date", false},
		{"timestamp type", "juhe_stats", "usage_stats_hourly", "stat_hour", "timestamp without time zone", precision(0), "timestamp(0) without time zone", "calendar_hour", false},
		{"micros runtime table", "juhe_stats", "background_task_runs", "started_at", "text", sql.NullInt64{}, "timestamptz(6)", "instant", false},
		{"deleted business instant", "juhe_business", "accounts", "deleted_at", "text", sql.NullInt64{}, "timestamptz(3)", "instant", false},
		{"runtime lease instant", "juhe_business", "account_lock_states", "incident_started_at", "text", sql.NullInt64{}, "timestamptz(6)", "instant", false},
		{"cooldown fence instant", "juhe_business", "accounts", "cooldown_retest_last_at", "text", sql.NullInt64{}, "timestamptz(3)", "instant", false},
		{"schedule lease", "juhe_business", "model_quality_schedules", "lease_until", "text", sql.NullInt64{}, "timestamptz(6)", "instant", false},
		{"dirty min date", "juhe_stats", "usage_overview_dirty_scopes", "min_changed_date", "text", sql.NullInt64{}, "date", "calendar_date", false},
		{"retired shard table", "juhe_usage", "usage_record_shards", "last_write_at", "text", sql.NullInt64{}, "text", "exception", false},
		{"unknown instant", "juhe_business", "accounts", "mystery_at", "text", sql.NullInt64{}, "timestamptz(3)", "instant", true},
		{"unknown date", "juhe_business", "accounts", "mystery_date", "text", sql.NullInt64{}, "date", "calendar_date", true},
		{"epoch ms", "juhe_business", "account_circuit_incidents", "created_at_ms", "bigint", sql.NullInt64{}, "timestamptz(6)", "epoch_ms", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.schema, tc.table, tc.column, tc.dataType, tc.precision)
			if got.Type != tc.wantType || got.Kind != tc.wantKind || got.Review != tc.review {
				t.Fatalf("Classify = %+v, want type=%s kind=%s review=%v", got, tc.wantType, tc.wantKind, tc.review)
			}
		})
	}
}

func TestReferencesIdentifierUsesWordBoundary(t *testing.T) {
	if !referencesIdentifier(`PRIMARY KEY (created_at, id)`, "created_at") {
		t.Fatal("应命中 created_at")
	}
	if referencesIdentifier(`PRIMARY KEY (updated_at, id)`, "created_at") {
		t.Fatal("created_at 不应命中 updated_at")
	}
	if referencesIdentifier(`CREATE INDEX idx ON t (id)`, "created") {
		t.Fatal("部分词不应命中")
	}
}

func TestApplyDependencies(t *testing.T) {
	column := Column{Schema: "juhe_usage", Table: "usage_records", Column: "created_at"}
	deps := map[string][]relationDependency{
		"juhe_usage.usage_records": {
			{name: "usage_records_pkey", kind: "constraint:p", def: "PRIMARY KEY (created_at, id)"},
			{name: "idx_usage_records_stats_cursor", kind: "index", def: "CREATE INDEX idx ON juhe_usage.usage_records (created_at, id)"},
			{name: "trg_usage_records", kind: "trigger", def: "CREATE TRIGGER trg AFTER INSERT ON usage_records ... NEW.created_at ..."},
			{name: "partition_key", kind: "partition", def: "RANGE (created_at)"},
			{name: "idx_updated", kind: "index", def: "CREATE INDEX idx_updated ON juhe_usage.usage_records (updated_at)"},
			{name: "juhe_business.child.fk_usage", kind: "referenced_by_fk", def: "created_at"},
			{name: "juhe_business.usage_view", kind: "view", def: "created_at"},
		},
	}
	applyDependencies(&column, deps)
	if !column.Dependencies.PrimaryKey || !column.Dependencies.PartitionKey {
		t.Fatalf("依赖标记缺失: %+v", column.Dependencies)
	}
	if len(column.Dependencies.Indexes) != 1 || column.Dependencies.Indexes[0] != "idx_usage_records_stats_cursor" {
		t.Fatalf("索引依赖错误: %+v", column.Dependencies.Indexes)
	}
	if len(column.Dependencies.Constraints) != 1 || column.Dependencies.Constraints[0] != "usage_records_pkey" {
		t.Fatalf("约束依赖错误: %+v", column.Dependencies.Constraints)
	}
	if len(column.Dependencies.Triggers) != 1 || column.Dependencies.Triggers[0] != "trg_usage_records" {
		t.Fatalf("触发器依赖错误: %+v", column.Dependencies.Triggers)
	}
	if len(column.Dependencies.ReferencedByFKs) != 1 || column.Dependencies.ReferencedByFKs[0] != "juhe_business.child.fk_usage" {
		t.Fatalf("外键引用依赖错误: %+v", column.Dependencies.ReferencedByFKs)
	}
	if len(column.Dependencies.Views) != 1 || column.Dependencies.Views[0] != "juhe_business.usage_view" {
		t.Fatalf("视图依赖错误: %+v", column.Dependencies.Views)
	}
}

func TestSummary(t *testing.T) {
	summary := Summary{}
	summary.add(Column{DataType: "text", Target: Target{Kind: "instant", Review: false}})
	summary.add(Column{DataType: "timestamp with time zone", Target: Target{Kind: "instant"}})
	summary.add(Column{DataType: "text", Target: Target{Kind: "calendar_date"}})
	summary.add(Column{DataType: "text", Target: Target{Kind: "calendar_hour"}})
	summary.add(Column{DataType: "text", Target: Target{Kind: "exception"}})
	summary.add(Column{DataType: "bigint", Target: Target{Kind: "epoch_ms", Review: true}})
	if summary.InstantText != 1 || summary.AlreadyTimestamptz != 1 || summary.CalendarDate != 1 ||
		summary.CalendarHour != 1 || summary.KeepText != 1 || summary.EpochMS != 1 || summary.Review != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}
