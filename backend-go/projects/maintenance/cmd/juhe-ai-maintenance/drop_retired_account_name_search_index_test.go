package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestDropRetiredAccountNameSearchIndexUsageGates 钉死用法/门禁契约：空 dsn、
// 非 postgres URL、正式执行缺停写或备份确认都必须 exit 2，且不得进入开库
// 分支（--dsn 指向不可达端口也不会产生连接错误输出）。
func TestDropRetiredAccountNameSearchIndexUsageGates(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"missing dsn", nil},
		{"blank dsn", []string{"--dsn", "   "}},
		{"non postgres dsn", []string{"--dsn", "mysql://user@127.0.0.1:1/none"}},
		{"apply missing both confirmations", []string{"--dsn", "postgres://user@127.0.0.1:1/none"}},
		{"apply missing stop", []string{"--dsn", "postgres://user@127.0.0.1:1/none", "--backup-confirmed"}},
		{"apply missing backup", []string{"--dsn", "postgres://user@127.0.0.1:1/none", "--stop-write-confirmed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			stdout, stderr := bdCaptureOutput(t, func() {
				code = runDropRetiredAccountNameSearchIndexCommand(tc.argv)
			})
			if code != 2 {
				t.Fatalf("exit = %d, want 2（stdout=%q stderr=%q）", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("门禁拒绝不得写 stdout 报告: %q", stdout)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Fatal("门禁拒绝必须在 stderr 说明原因")
			}
		})
	}
}

// TestDropRetiredAccountNameSearchIndexDryRunGate 证明 dry-run 不要求停写/备份
// 确认；只测门禁函数，不连接任何数据库。
func TestDropRetiredAccountNameSearchIndexDryRunGate(t *testing.T) {
	dryRun := accountNameSearchRetirementGate{DSN: "postgres://user@127.0.0.1:1/none", DryRun: true}
	if err := dryRun.validate(); err != nil {
		t.Fatalf("dry-run 不要求确认: %v", err)
	}
	apply := accountNameSearchRetirementGate{
		DSN:                "postgres://user@127.0.0.1:1/none",
		StopWriteConfirmed: true,
		BackupConfirmed:    true,
	}
	if err := apply.validate(); err != nil {
		t.Fatalf("完整确认的正式执行应通过门禁: %v", err)
	}
}

// TestDropRetiredAccountNameSearchIndexDispatch 证明 main 的子命令分发在单层
// FlagSet 解析之前接住 argv[0]。
func TestDropRetiredAccountNameSearchIndexDispatch(t *testing.T) {
	var code int
	stdout, stderr := bdCaptureOutput(t, func() {
		code = runMaintenance([]string{"drop-retired-account-name-search-index"})
	})
	if code != 2 {
		t.Fatalf("缺 --dsn 应 exit 2, got %d", code)
	}
	if stdout != "" {
		t.Fatalf("分发路径不应写 stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "--dsn") {
		t.Fatalf("stderr 应说明缺 --dsn: %q", stderr)
	}
}

// TestDropRetiredAccountNameSearchIndexPostgres 覆盖真实 PG 全链路：dry-run
// 只读、apply 清退三表、一列与遗留触发器函数、重跑幂等。
func TestDropRetiredAccountNameSearchIndexPostgres(t *testing.T) {
	const prepareSchema = `
		CREATE SCHEMA juhe_business;
		CREATE TABLE juhe_business.account_name_search_terms (account_id text NOT NULL, term text NOT NULL);
		CREATE TABLE juhe_business.account_name_search_documents (account_id text PRIMARY KEY);
		CREATE TABLE juhe_business.account_list_availability_projection_search_terms (
			viewer_system_account_id text NOT NULL,
			account_id text NOT NULL,
			term text NOT NULL,
			PRIMARY KEY (viewer_system_account_id, account_id, term)
		);
		CREATE TABLE juhe_business.account_list_availability_projection_index (
			viewer_system_account_id text NOT NULL,
			account_id text NOT NULL,
			search_index_complete integer NOT NULL DEFAULT 0,
			PRIMARY KEY (viewer_system_account_id, account_id)
		);
		CREATE FUNCTION juhe_business.account_list_availability_name_search_dirty_trigger()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$ BEGIN RETURN NULL; END; $$;
		-- 复刻存量库依赖形态：函数仅被两张待删表上的触发器依赖，先删表后
		-- 删函数必须能以默认 RESTRICT 通过。
		CREATE TRIGGER account_list_availability_name_search_terms
		AFTER INSERT OR UPDATE OR DELETE ON juhe_business.account_name_search_terms
		FOR EACH ROW EXECUTE FUNCTION juhe_business.account_list_availability_name_search_dirty_trigger();
		CREATE TRIGGER account_list_availability_name_search_documents
		AFTER INSERT OR UPDATE OR DELETE ON juhe_business.account_name_search_documents
		FOR EACH ROW EXECUTE FUNCTION juhe_business.account_list_availability_name_search_dirty_trigger();
		INSERT INTO juhe_business.account_name_search_terms (account_id, term) VALUES ('acc-1', '账');
		INSERT INTO juhe_business.account_name_search_documents (account_id) VALUES ('acc-1');
		INSERT INTO juhe_business.account_list_availability_projection_search_terms
			(viewer_system_account_id, account_id, term) VALUES ('sys-1', 'acc-1', '账');
		INSERT INTO juhe_business.account_list_availability_projection_index
			(viewer_system_account_id, account_id, search_index_complete) VALUES ('sys-1', 'acc-1', 1);`
	tdb := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, prepareSchema)
		return err
	})

	run := func(t *testing.T, argv ...string) (int, string, string) {
		t.Helper()
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = runDropRetiredAccountNameSearchIndexCommand(argv)
		})
		return code, stdout, stderr
	}
	decode := func(t *testing.T, stdout string) accountNameSearchRetirementReport {
		t.Helper()
		if strings.Count(strings.TrimSpace(stdout), "\n") != 0 {
			t.Fatalf("stdout 必须是单行 JSON: %q", stdout)
		}
		var report accountNameSearchRetirementReport
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("stdout 必须是 JSON: %v（%q）", err, stdout)
		}
		return report
	}

	// dry-run：报告存在状态，不写库，不要求确认。
	code, stdout, stderr := run(t, "--dsn", tdb.DSN, "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run exit = %d, want 0（stderr=%q）", code, stderr)
	}
	report := decode(t, stdout)
	if report.Mode != "dry-run" {
		t.Fatalf("mode = %q, want dry-run", report.Mode)
	}
	if report.Database == "" {
		t.Fatal("dry-run 报告必须包含 database")
	}
	if len(report.Tables) != len(accountNameSearchRetiredTables) {
		t.Fatalf("dry-run 报告表数 = %d, want %d", len(report.Tables), len(accountNameSearchRetiredTables))
	}
	for _, table := range report.Tables {
		if !table.Existed || table.Dropped {
			t.Fatalf("dry-run 表 %s 应存在且未删除: %+v", table.Table, table)
		}
	}
	if !report.Column.Existed || report.Column.Dropped {
		t.Fatalf("dry-run 列应存在且未删除: %+v", report.Column)
	}
	if report.Function.Function != accountNameSearchDirtyTriggerFunction || !report.Function.Existed || report.Function.Dropped {
		t.Fatalf("dry-run 函数报告应为存在且未删除: %+v", report.Function)
	}
	if len(report.Actions) != len(accountNameSearchRetiredTables)+2 {
		t.Fatalf("dry-run 计划动作数 = %d, want %d: %v", len(report.Actions), len(accountNameSearchRetiredTables)+2, report.Actions)
	}
	for index, table := range accountNameSearchRetiredTables {
		want := "DROP TABLE IF EXISTS juhe_business." + table
		if report.Actions[index] != want {
			t.Fatalf("dry-run 计划动作[%d] = %q, want %q", index, report.Actions[index], want)
		}
	}
	wantColumnAction := "ALTER TABLE IF EXISTS juhe_business.account_list_availability_projection_index DROP COLUMN IF EXISTS search_index_complete"
	if report.Actions[len(accountNameSearchRetiredTables)] != wantColumnAction {
		t.Fatalf("dry-run 列动作 = %q, want %q", report.Actions[len(accountNameSearchRetiredTables)], wantColumnAction)
	}
	wantFunctionAction := "DROP FUNCTION IF EXISTS juhe_business.account_list_availability_name_search_dirty_trigger()"
	if report.Actions[len(report.Actions)-1] != wantFunctionAction {
		t.Fatalf("dry-run 函数动作 = %q, want %q", report.Actions[len(report.Actions)-1], wantFunctionAction)
	}
	assertFunctionMissing(t, tdb.DB, false, "dry-run 后")
	var rows int
	if err := tdb.DB.QueryRow(`SELECT count(*) FROM juhe_business.account_name_search_terms`).Scan(&rows); err != nil {
		t.Fatalf("dry-run 后词项表必须仍存在: %v", err)
	}
	if rows != 1 {
		t.Fatalf("dry-run 不得删除数据行: %d", rows)
	}
	for _, table := range accountNameSearchRetiredTables {
		var found sql.NullString
		if err := tdb.DB.QueryRow(`SELECT to_regclass($1)`, "juhe_business."+table).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found.Valid || found.String == "" {
			t.Fatalf("dry-run 后表 %s 必须仍存在", table)
		}
	}
	var columnCount int
	if err := tdb.DB.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'juhe_business' AND table_name = 'account_list_availability_projection_index'
		AND column_name = 'search_index_complete'`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 1 {
		t.Fatalf("dry-run 后列 search_index_complete 必须仍存在: %d", columnCount)
	}

	// apply 缺确认：exit 2 且不写库。
	code, stdout, _ = run(t, "--dsn", tdb.DSN, "--stop-write-confirmed")
	if code != 2 || stdout != "" {
		t.Fatalf("apply 缺备份确认应 exit 2 且无 stdout: exit=%d stdout=%q", code, stdout)
	}
	if err := tdb.DB.QueryRow(`SELECT count(*) FROM juhe_business.account_name_search_terms`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("缺确认的 apply 不得写库: rows=%d err=%v", rows, err)
	}

	// apply：清退三表、一列与遗留触发器函数。
	code, stdout, stderr = run(t, "--dsn", tdb.DSN, "--stop-write-confirmed", "--backup-confirmed")
	if code != 0 {
		t.Fatalf("apply exit = %d, want 0（stderr=%q）", code, stderr)
	}
	report = decode(t, stdout)
	if report.Mode != "apply" {
		t.Fatalf("mode = %q, want apply", report.Mode)
	}
	for _, table := range report.Tables {
		if !table.Existed || !table.Dropped {
			t.Fatalf("apply 表 %s 应报告删除: %+v", table.Table, table)
		}
	}
	if !report.Column.Existed || !report.Column.Dropped {
		t.Fatalf("apply 列应报告删除: %+v", report.Column)
	}
	if report.Function.Function != accountNameSearchDirtyTriggerFunction || !report.Function.Existed || !report.Function.Dropped {
		t.Fatalf("apply 函数报告应为存在且已删除: %+v", report.Function)
	}
	for _, table := range accountNameSearchRetiredTables {
		var found sql.NullString
		if err := tdb.DB.QueryRow(`SELECT to_regclass($1)`, "juhe_business."+table).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found.Valid && found.String != "" {
			t.Fatalf("apply 后表 %s 必须不存在: %s", table, found.String)
		}
	}
	if err := tdb.DB.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'juhe_business' AND table_name = 'account_list_availability_projection_index'
		AND column_name = 'search_index_complete'`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 0 {
		t.Fatalf("apply 后列 search_index_complete 必须不存在: %d", columnCount)
	}
	assertFunctionMissing(t, tdb.DB, true, "apply 后")

	// 重跑幂等：全部已缺席，exit 0，不重复报告删除。
	code, stdout, stderr = run(t, "--dsn", tdb.DSN, "--stop-write-confirmed", "--backup-confirmed")
	if code != 0 {
		t.Fatalf("重跑 exit = %d, want 0（stderr=%q）", code, stderr)
	}
	report = decode(t, stdout)
	if len(report.Actions) != len(accountNameSearchRetiredTables)+2 {
		t.Fatalf("重跑计划动作数 = %d, want %d", len(report.Actions), len(accountNameSearchRetiredTables)+2)
	}
	for _, table := range report.Tables {
		if table.Existed || table.Dropped {
			t.Fatalf("重跑表 %s 应报告已缺席: %+v", table.Table, table)
		}
	}
	if report.Column.Existed || report.Column.Dropped {
		t.Fatalf("重跑列应报告已缺席: %+v", report.Column)
	}
	if report.Function.Existed || report.Function.Dropped {
		t.Fatalf("重跑函数应报告已缺席: %+v", report.Function)
	}
	assertFunctionMissing(t, tdb.DB, true, "重跑后")
}

// assertFunctionMissing 断言名称搜索 dirty 触发器函数的存在状态：
// wantMissing=true 时要求 to_regprocedure(...) IS NULL。
func assertFunctionMissing(t *testing.T, db *sql.DB, wantMissing bool, when string) {
	t.Helper()
	var missing bool
	if err := db.QueryRow(`SELECT to_regprocedure('juhe_business.account_list_availability_name_search_dirty_trigger()') IS NULL`).Scan(&missing); err != nil {
		t.Fatalf("%s探测触发器函数: %v", when, err)
	}
	if missing != wantMissing {
		t.Fatalf("%s触发器函数缺失状态 = %t, want %t", when, missing, wantMissing)
	}
}
