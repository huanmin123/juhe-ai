package main

// W33：--rebuild-runtime-log-facets CLI 契约测试（真实 PG，pgtestkit 隔离库）。
// 覆盖：与 --run-jobs-once 的用法互斥（exit 2）；空库首跑建立 schema 并输出
// 零计数 JSON；插入 runtime_logs 后重建输出与直接事实一致的 JSON 结果。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestW33CmdRebuildRuntimeLogFacetsMutuallyExclusiveWithRunJobsOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-rebuild-runtime-log-facets", "-run-jobs-once", "smoke"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--rebuild-runtime-log-facets") {
		t.Fatalf("stderr 必须披露互斥用法错误: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("用法错误不得写 stdout: %q", stdout.String())
	}
}

func TestW33CmdRebuildRuntimeLogFacetsOnPostgres(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, key)
)`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES ('sys_admin', 'runtimeLogIndexRetentionDays', '14', '2026-08-08T00:00:00.000Z')`)
		return err
	})
	t.Setenv("JUHE_AI_RUNTIME_LOG_POSTGRES_URL", testDB.DSN)
	t.Setenv("JUHE_AI_LOG_DIR", t.TempDir())

	// 空库首跑：EnsureSchema 建立 F1 表，重建输出零计数 JSON。
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-rebuild-runtime-log-facets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("空库重建 exit = %d, want 0, stderr=%s", code, stderr.String())
	}
	var first rebuildRuntimeLogFacetsResult
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil {
		t.Fatalf("解析首跑结果 JSON 失败: %v, stdout=%q", err, stdout.String())
	}
	if first.Bucket != "current" || first.TotalCount != 0 || first.EarliestTime != "" || first.LatestTime != "" {
		t.Fatalf("空库首跑结果 = %+v, want bucket=current total=0 时间字段为空", first)
	}

	// 直接向 runtime_logs 造数（schema 已由首跑建立），二跑重建输出与事实一致。
	now := time.Now().UTC()
	insert := `INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at)
VALUES ($1, 'w33-cmd.log', 1, 1, $2, 'warn', NULL, 'w33-cmd-event', '', NULL, '{}', $2)`
	ctx := context.Background()
	if _, err := testDB.DB.ExecContext(ctx, insert, "w33-cmd-older", now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.DB.ExecContext(ctx, insert, "w33-cmd-newer", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-rebuild-runtime-log-facets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("造数后重建 exit = %d, want 0, stderr=%s", code, stderr.String())
	}
	var second rebuildRuntimeLogFacetsResult
	if err := json.Unmarshal(stdout.Bytes(), &second); err != nil {
		t.Fatalf("解析重建结果 JSON 失败: %v, stdout=%q", err, stdout.String())
	}
	earliest := now.Add(-2 * time.Hour).Truncate(time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z")
	latest := now.Add(-time.Hour).Truncate(time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z")
	if second.Bucket != "current" || second.TotalCount != 2 || second.EarliestTime != earliest || second.LatestTime != latest {
		t.Fatalf("重建结果 = %+v, want bucket=current total=2 earliest=%q latest=%q", second, earliest, latest)
	}
}
