package mockdata

// autofill 纯函数的单测：跳过清单、行数确定性、CHECK 解析与值生成。需要真实
// PostgreSQL 的集成行为（空表补行、FK 父表缺失、coverage Ready）见
// autofill_pg_test.go。

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAutofillRowCountIsDeterministicAndBounded(t *testing.T) {
	for _, table := range []string{"mockdata_autofill_probe", "usage_records", "x"} {
		first := autofillRowCount(table)
		if first < 1 || first > 3 {
			t.Fatalf("行数 %d 超出 1..3: table=%s", first, table)
		}
		if again := autofillRowCount(table); again != first {
			t.Fatalf("行数不确定: table=%s first=%d again=%d", table, first, again)
		}
	}
}

func TestAutofillSkipReason(t *testing.T) {
	cases := []struct {
		table    string
		contains string
	}{
		{table: "background_job_leases", contains: "owner 租约族"},
		{table: "stats_job_state", contains: "owner 状态族"},
		{table: "backfill_progress", contains: "一次性回填"},
		{table: "usage_range_window_requests", contains: "瞬时队列"},
		{table: "codex_context_storage_cleanup_queue", contains: "瞬时清理队列"},
		{table: "usage_stats_totals", contains: "usage_stats_"},
		{table: "account_quality_dirty_accounts", contains: "account_quality_"},
		{table: "usage_quota_hourly_windows", contains: "usage_quota_hourly_"},
		{table: "account_balance_projection_cursors", contains: "_projection_cursors"},
		{table: "mockdata_autofill_parent_owner_leases", contains: "_owner_leases"},
		{table: "groups", contains: "seedBusiness"},
		{table: "accounts", contains: "seedBusiness"},
		{table: "media_jobs", contains: "seedBusiness"},
		{table: "super_group_members", contains: "seedBusiness"},
		{table: "chat_image_generations", contains: "域业务表"},
		{table: "request_quota_hourly_window_scope_bindings", contains: "seedBusiness"},
		{table: "account_list_availability_dirty", contains: "运行时脏队列"},
		{table: "account_list_availability_projection_viewer_health", contains: "查看者健康"},
		{table: "proxy_latency_projection_receipts", contains: "运行时回执族"},
		{table: "model_check_question_tests", contains: "运行时诊断记录"},
		{table: "chat_user_tool_preferences", contains: "列均可空"},
		{table: "audit_payload_blobs", contains: "seedObservability"},
		{table: "operation_log_viewers", contains: "seedObservability"},
		{table: "runtime_log_file_cursors", contains: "owner 游标族"},
		{table: "account_health_probe_request_outbox", contains: "运行时 outbox"},
		{table: "usage_records", contains: "seedUsage"},
		{table: "model_check_runs", contains: "J3b"},
	}
	for _, item := range cases {
		reason, skip := autofillSkipReason(item.table)
		if !skip {
			t.Fatalf("%s 应命中跳过清单", item.table)
		}
		if !strings.Contains(reason, item.contains) {
			t.Fatalf("%s 跳过原因 %q 不含 %q", item.table, reason, item.contains)
		}
	}
	for _, table := range []string{"vendors", "global_settings", "system_settings", "scheduling_preferences", "mockdata_autofill_probe", "protocols"} {
		if _, skip := autofillSkipReason(table); skip {
			t.Fatalf("%s 不应命中跳过清单（结构占位可补）", table)
		}
	}
}

func TestParseAutofillCheckValues(t *testing.T) {
	values := parseAutofillCheckValues([]string{
		// pg_get_constraintdef 对 CHECK (status IN ('alpha','beta')) 的实际形态。
		`CHECK (status = ANY (ARRAY['alpha'::text, 'beta'::text]))`,
		// 等值约束与强制转型形态。
		`CHECK (((kind)::text = 'video'::text))`,
		// 数组字面量整体再转型。
		`CHECK ((scope)::text = ANY ((ARRAY['global'::text, 'account'::text])::text[]))`,
		// 转义引号。
		`CHECK (remark = 'it''s'::text)`,
		// 数值比较：无数值字面量，不产生条目。
		`CHECK (level >= 0)`,
		// 多列约束：无法静态映射到单列，不产生条目。
		`CHECK (start_at < end_at)`,
		// 长度约束：无数值字面量，不产生条目。
		`CHECK (length(name) > 0)`,
	})
	if got := values["status"]; len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("status 允许值解析不符: %v", got)
	}
	if got := values["kind"]; len(got) != 1 || got[0] != "video" {
		t.Fatalf("kind 允许值解析不符: %v", got)
	}
	if got := values["scope"]; len(got) != 2 || got[0] != "global" || got[1] != "account" {
		t.Fatalf("scope 允许值解析不符: %v", got)
	}
	if got := values["remark"]; len(got) != 1 || got[0] != "it's" {
		t.Fatalf("remark 允许值解析不符: %v", got)
	}
	for _, column := range []string{"level", "name"} {
		if got, ok := values[column]; ok {
			t.Fatalf("%s 不应解析出允许值: %v", column, got)
		}
	}
	if _, ok := values["start_at"]; ok {
		t.Fatal("多列约束不应解析出允许值")
	}
}

func TestAutofillGeneratedValueByTypeAndName(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	textColumn := func(name string) autofillColumn {
		return autofillColumn{name: name, dataType: "text"}
	}
	cases := []struct {
		name    string
		column  autofillColumn
		pk      bool
		checks  []string
		enums   []string
		shape   string
		row     int
		want    any
		wantErr bool
	}{
		{name: "text pk", column: autofillColumn{name: "id", dataType: "text"}, pk: true, row: 2,
			want: CleanupIDPrefix + "autofill_probe_2"},
		{name: "bigint", column: autofillColumn{name: "total", dataType: "bigint"}, row: 1, want: int(2)},
		{name: "smallint", column: autofillColumn{name: "level", dataType: "smallint"}, row: 0, want: int(1)},
		{name: "numeric", column: autofillColumn{name: "amount", dataType: "numeric"}, row: 0, want: float64(1)},
		{name: "boolean", column: autofillColumn{name: "enabled", dataType: "boolean"}, row: 0, want: true},
		{name: "jsonb default object", column: autofillColumn{name: "payload", dataType: "jsonb"}, row: 0, want: "{}"},
		{name: "json array shape", column: autofillColumn{name: "items", dataType: "json"}, shape: "array", row: 0, want: "[]"},
		{name: "text json array shape", column: textColumn("supported_api_protocols_json"), shape: "array", row: 0, want: "[]"},
		{name: "text json no shape", column: textColumn("ext_json"), row: 0, want: "{}"},
		{name: "text created_at", column: textColumn("created_at"), row: 1,
			want: now.Add(-time.Minute).UTC().Format(isoMillisLayout)},
		{name: "text updated_time", column: textColumn("updated_time"), row: 0,
			want: now.UTC().Format(isoMillisLayout)},
		{name: "text email", column: textColumn("contact_email"), row: 0,
			want: CleanupIDPrefix + "autofill@example.invalid"},
		{name: "text ip", column: textColumn("client_ip"), row: 0, want: "10.10.0.1"},
		{name: "text sha256", column: textColumn("payload_sha256"), row: 0, want: nil},
		{name: "text url", column: textColumn("callback_url"), row: 0,
			want: "https://example.invalid/" + CleanupTracePrefix + "autofill"},
		{name: "text marker", column: textColumn("label"), row: 1,
			want: CleanupIDPrefix + "autofill_probe_label_1"},
		{name: "check first value", column: textColumn("status"), checks: []string{"beta", "alpha"}, row: 0, want: "beta"},
		{name: "enum first label", column: autofillColumn{name: "stage", dataType: "USER-DEFINED", udtName: "stage_enum"},
			enums: []string{"pending", "done"}, row: 0, want: "pending"},
		{name: "bytea", column: autofillColumn{name: "blob", dataType: "bytea"}, row: 0,
			want: []byte(CleanupTracePrefix + "autofill")},
		{name: "text array", column: autofillColumn{name: "tags", dataType: "ARRAY", udtName: "_text"}, row: 0,
			want: []string{CleanupTracePrefix + "autofill"}},
		{name: "int array", column: autofillColumn{name: "buckets", dataType: "ARRAY", udtName: "_int4"}, row: 2,
			want: []int32{3}},
		{name: "uuid", column: autofillColumn{name: "external_id", dataType: "uuid"}, row: 0, want: nil},
		{name: "timestamptz", column: autofillColumn{name: "occurred_at", dataType: "timestamp with time zone"}, row: 0,
			want: now.UTC()},
		{name: "unknown type", column: autofillColumn{name: "money", dataType: "money"}, row: 0, wantErr: true},
		{name: "unknown array element", column: autofillColumn{name: "weird", dataType: "ARRAY", udtName: "_money"}, row: 0, wantErr: true},
	}
	for _, item := range cases {
		got, err := autofillGeneratedValue("juhe_stats", "probe", item.column, item.pk, item.checks, item.enums, item.shape, item.row, now)
		if item.wantErr {
			if err == nil {
				t.Fatalf("%s: 应返回错误，实际 %v", item.name, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		switch item.want.(type) {
		case nil:
			// sha256 / uuid 等派生值单独断言。
			switch item.column.dataType {
			case "uuid":
				if !isValidAutofillUUID(got.(string)) {
					t.Fatalf("%s: uuid 形态不符: %v", item.name, got)
				}
				again, _ := autofillGeneratedValue("juhe_stats", "probe", item.column, item.pk, item.checks, item.enums, item.shape, item.row, now)
				if again != got {
					t.Fatalf("%s: uuid 不确定: %v vs %v", item.name, got, again)
				}
			default:
				if len(got.(string)) != 64 {
					t.Fatalf("%s: sha256 hex 长度不符: %v", item.name, got)
				}
			}
		case time.Time:
			if !got.(time.Time).Equal(item.want.(time.Time)) {
				t.Fatalf("%s: 时间不符: %v want %v", item.name, got, item.want)
			}
		case []string, []int32, []byte:
			assertAutofillDeepEqual(t, item.name, got, item.want)
		default:
			if got != item.want {
				t.Fatalf("%s: 值不符: got %v want %v", item.name, got, item.want)
			}
		}
	}
}

func assertAutofillDeepEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("%s: 值不符: got %s want %s", name, gotJSON, wantJSON)
	}
}

// isValidAutofillUUID 校验 v4 形态 UUID（版本位 4、变体位 8/9/a/b）。
func isValidAutofillUUID(value string) bool {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	return pattern.MatchString(value)
}

func TestAutofillTruncate(t *testing.T) {
	if got := autofillTruncate("abcdef", 3); got != "abc" {
		t.Fatalf("truncate = %q", got)
	}
	if got := autofillTruncate("abc", 0); got != "abc" {
		t.Fatalf("无限制时不应截断: %q", got)
	}
	if got := autofillTruncate("abc", 5); got != "abc" {
		t.Fatalf("短值不应截断: %q", got)
	}
}

func TestAutofillColumnOmitRules(t *testing.T) {
	nextval := "nextval('mockdata_autofill_probe_id_seq'::regclass)"
	literal := "'active'::text"
	cases := []struct {
		name   string
		column autofillColumn
		omit   bool
	}{
		{name: "identity always", column: autofillColumn{name: "id", dataType: "bigint", identity: true, identityGeneration: "ALWAYS"}, omit: true},
		{name: "identity by default", column: autofillColumn{name: "id", dataType: "bigint", identity: true, identityGeneration: "BY DEFAULT"}, omit: false},
		{name: "sequence default", column: autofillColumn{name: "id", dataType: "bigint", defaultValue: &nextval}, omit: true},
		{name: "literal default", column: autofillColumn{name: "status", dataType: "text", defaultValue: &literal}, omit: false},
		{name: "plain not null", column: autofillColumn{name: "label", dataType: "text"}, omit: false},
	}
	for _, item := range cases {
		omit := item.column.identityAlways() || item.column.hasSequenceDefault()
		if omit != item.omit {
			t.Fatalf("%s: omit = %v, want %v", item.name, omit, item.omit)
		}
	}
}

func TestAutofillTextValueMaxLengthIntegration(t *testing.T) {
	// varchar 长度上限与确定性标记组合：截断后长度不越界，且等于未截断标记
	// 的同长前缀（截断可能切掉清理标识尾部，但值仍确定性可复现）。
	column := autofillColumn{name: "code", dataType: "character varying", maxLength: 8}
	value, err := autofillGeneratedValue("juhe_business", "probe", column, false, nil, nil, "", 0, time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(value.(string)) != 8 {
		t.Fatalf("截断后长度不符: %q (%d)", value, len(value.(string)))
	}
	want := (CleanupIDPrefix + "autofill_probe_code_0")[:8]
	if value.(string) != want {
		t.Fatalf("截断值应是确定性标记的前缀: got %q want %q", value, want)
	}
}
