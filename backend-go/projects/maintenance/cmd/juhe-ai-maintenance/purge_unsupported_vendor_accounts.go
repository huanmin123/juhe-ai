// 九家真实厂商收口清退命令（purge-unsupported-vendor-accounts，2026-11-05）：
//   - 目标账户：vendor_code IS NULL、connection_driver_code='generic_http' 或
//     vendor/driver 不一致的账户（含其授权实例）。generic_http 连接器已随
//     openai/hybrid 一起退役，这些账户不再有运行时语义；
//   - 删除范围由用户 2026-11-05 裁决：账户配置 + 运行态 + 测试草稿 + media 任务
//     + 历史 usage/audit/stats 一并删除，不留存量；九家真实厂商账户与历史行
//     不受影响；
//   - dry-run 只读探测并写出 manifest（冻结 ID 清单）；正式执行必须带
//     --stop-write-confirmed 与 --backup-confirmed，且复用 dry-run 的 manifest，
//     执行前再核对当前目标集合未漂移；
//   - 分批删除、可重入；每批独立提交，失败后可用同一 manifest 续跑；
//   - stdout 单行 JSON 报告，stderr 进度；退出码 0 成功、1 运行失败、2 用法/门禁。
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const purgeUnsupportedVendorCommandName = "purge-unsupported-vendor-accounts"

// purgeUnsupportedVendorGate 是命令的用法与停写/备份门禁；validate 不触碰数据库。
type purgeUnsupportedVendorGate struct {
	DSN                string
	DryRun             bool
	StopWriteConfirmed bool
	BackupConfirmed    bool
	ManifestOut        string
	BatchSize          int
}

func (g purgeUnsupportedVendorGate) validate() error {
	dsn := strings.TrimSpace(g.DSN)
	if dsn == "" {
		return fmt.Errorf("%s 需要 --dsn 指向显式的 PostgreSQL URL", purgeUnsupportedVendorCommandName)
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return fmt.Errorf("%s 的 --dsn 必须是 postgres:// 或 postgresql:// URL", purgeUnsupportedVendorCommandName)
	}
	if strings.TrimSpace(g.ManifestOut) == "" {
		return fmt.Errorf("%s 需要 --manifest-out（dry-run 写出、apply 读取同一清单）", purgeUnsupportedVendorCommandName)
	}
	if g.DryRun {
		return nil
	}
	if !g.StopWriteConfirmed {
		return fmt.Errorf("%s 正式执行需要 --stop-write-confirmed（gateway/jobs 已停止）", purgeUnsupportedVendorCommandName)
	}
	if !g.BackupConfirmed {
		return fmt.Errorf("%s 正式执行需要 --backup-confirmed（可恢复备份已验证）", purgeUnsupportedVendorCommandName)
	}
	return nil
}

// purgeManifest 是 dry-run 冻结、apply 复核的账户 ID 清单。
type purgeManifest struct {
	Version      int      `json:"version"`
	GeneratedAt  string   `json:"generated_at"`
	Database     string   `json:"database"`
	Predicate    string   `json:"predicate"`
	AccountIDs   []string `json:"account_ids"`
	AccountCount int      `json:"account_count"`
	IDsSHA256    string   `json:"ids_sha256"`
}

// purgeTableCount 是单表探测/删除计数。
type purgeTableCount struct {
	Table   string `json:"table"`
	Count   int64  `json:"count"`
	Deleted int64  `json:"deleted,omitempty"`
}

// purgeUnsupportedVendorReport 是 stdout 单行 JSON 报告。
type purgeUnsupportedVendorReport struct {
	Mode              string            `json:"mode"`
	Database          string            `json:"database"`
	Manifest          string            `json:"manifest"`
	ManifestSHA256    string            `json:"manifest_ids_sha256"`
	AccountCount      int               `json:"account_count"`
	RealAccountCount  int64             `json:"real_account_count"`
	Tables            []purgeTableCount `json:"tables"`
	UsageDateRange    string            `json:"usage_date_range,omitempty"`
	AuditDateRange    string            `json:"audit_date_range,omitempty"`
	InvariantIssues   []string          `json:"invariant_issues,omitempty"`
	Rollback          string            `json:"rollback_note"`
	PostCheckAccounts int64             `json:"post_check_unsupported_accounts"`
}

const purgeUnsupportedVendorRollbackNote = "清退与后续 accounts CHECK 均不可逆；回滚 = 用 --backup-confirmed 验证过的整库备份恢复，" +
	"旧二进制因 SchemaRevision 17 门禁无法直接启动。历史 usage/audit/stats 已随账户删除。"

// runPurgeUnsupportedVendorAccountsCommand 是子命令入口，返回进程退出码。
func runPurgeUnsupportedVendorAccountsCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance "+purgeUnsupportedVendorCommandName, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL of the business database")
	dryRun := fs.Bool("dry-run", false, "read-only probe and manifest write; no account/usage/audit rows are deleted")
	stopWriteConfirmed := fs.Bool("stop-write-confirmed", false, "confirm gateway/jobs are stopped for the purge")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable full backup was verified")
	manifestOut := fs.String("manifest-out", "", "manifest JSON path (dry-run writes it; apply reads the same file)")
	batchSize := fs.Int("batch-size", 1000, "delete batch size (clamped to 1..50000)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	gate := purgeUnsupportedVendorGate{
		DSN:                *dsn,
		DryRun:             *dryRun,
		StopWriteConfirmed: *stopWriteConfirmed,
		BackupConfirmed:    *backupConfirmed,
		ManifestOut:        *manifestOut,
		BatchSize:          *batchSize,
	}
	if err := gate.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	db, err := openPostgresBootstrap(strings.TrimSpace(gate.DSN))
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开 PostgreSQL 连接失败: %v\n", err)
		return 1
	}
	defer db.Close()

	report, runErr := runPurgeUnsupportedVendorAccounts(context.Background(), db, gate, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "["+purgeUnsupportedVendorCommandName+"] "+format+"\n", args...)
	})
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "九家真实厂商收口清退失败: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode %s report: %v\n", purgeUnsupportedVendorCommandName, err)
		return 1
	}
	return 0
}

func runPurgeUnsupportedVendorAccounts(ctx context.Context, db *sql.DB, gate purgeUnsupportedVendorGate, logf func(string, ...any)) (purgeUnsupportedVendorReport, error) {
	if db == nil {
		return purgeUnsupportedVendorReport{}, errors.New("清退数据库未初始化")
	}
	if err := db.PingContext(ctx); err != nil {
		return purgeUnsupportedVendorReport{}, fmt.Errorf("目标 PostgreSQL 不可达: %w", err)
	}
	var database string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return purgeUnsupportedVendorReport{}, fmt.Errorf("读取目标数据库名: %w", err)
	}
	mode := "apply"
	if gate.DryRun {
		mode = "dry-run"
	}
	report := purgeUnsupportedVendorReport{
		Mode:     mode,
		Database: database,
		Manifest: strings.TrimSpace(gate.ManifestOut),
		Rollback: purgeUnsupportedVendorRollbackNote,
	}
	batch := gate.BatchSize
	if batch < 1 {
		batch = 1
	}
	if batch > 50000 {
		batch = 50000
	}

	liveIDs, err := purgeTargetAccountIDs(ctx, db)
	if err != nil {
		return report, err
	}
	report.RealAccountCount, err = purgeRealAccountCount(ctx, db)
	if err != nil {
		return report, err
	}
	report.InvariantIssues, err = purgeInvariantIssues(ctx, db)
	if err != nil {
		return report, err
	}
	report.UsageDateRange, err = purgeDateRange(ctx, db, "juhe_usage.usage_records", "created_at")
	if err != nil {
		return report, err
	}
	report.AuditDateRange, err = purgeDateRange(ctx, db, "juhe_dataset.audit_logs", "started_at")
	if err != nil {
		return report, err
	}

	var ids []string
	if gate.DryRun {
		ids = liveIDs
		if len(ids) == 0 {
			logf("dry-run：目标库 %s 没有待清退账户；仍写出空 manifest 以便复核", database)
		} else {
			logf("dry-run：目标库 %s 待清退账户 %d 个（含授权实例）；只读探测并写 manifest %s", database, len(ids), report.Manifest)
		}
	} else {
		manifest, readErr := readPurgeManifest(report.Manifest)
		if readErr != nil {
			return report, readErr
		}
		if manifest.Database != database {
			return report, fmt.Errorf("manifest 属于数据库 %q，当前库为 %q", manifest.Database, database)
		}
		ids = append([]string(nil), manifest.AccountIDs...)
		if hash := purgeIDsHash(ids); hash != manifest.IDsSHA256 {
			return report, fmt.Errorf("manifest ID 清单哈希不匹配（文件损坏或手工修改）")
		}
		if len(liveIDs) == 0 {
			// 幂等重跑：上一轮已清退完毕，按 manifest 继续做残留表的 no-op 删除。
			logf("apply：目标库 %s 已无待清退账户（上一轮已完成），按 manifest 做幂等复查", database)
		} else if liveHash := purgeIDsHash(liveIDs); liveHash != manifest.IDsSHA256 {
			return report, fmt.Errorf("manifest 已漂移：当前目标集合哈希 %s != manifest %s；请重新 dry-run 生成", liveHash, manifest.IDsSHA256)
		}
		report.ManifestSHA256 = manifest.IDsSHA256
		logf("apply：目标库 %s，manifest 账户 %d 个；停写/备份门禁已确认", database, len(ids))
	}
	report.AccountCount = len(ids)
	if gate.DryRun {
		manifest := purgeManifest{
			Version:      1,
			GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
			Database:     database,
			Predicate:    purgeTargetPredicate,
			AccountIDs:   ids,
			AccountCount: len(ids),
			IDsSHA256:    purgeIDsHash(ids),
		}
		if err := writePurgeManifest(report.Manifest, manifest); err != nil {
			return report, err
		}
		report.ManifestSHA256 = manifest.IDsSHA256
	}

	report.Tables, err = purgeProbeCounts(ctx, db, ids)
	if err != nil {
		return report, err
	}
	if gate.DryRun {
		logf("dry-run：探测完成，未删除任何行；明细见 stdout JSON")
		return report, nil
	}
	if len(ids) == 0 {
		logf("apply：manifest 为空，无需删除；只检查残留不变量")
		report.PostCheckAccounts, err = purgeUnsupportedAccountCount(ctx, db)
		return report, err
	}

	if err := purgeDeleteRows(ctx, db, ids, batch, &report, logf); err != nil {
		return report, err
	}
	report.PostCheckAccounts, err = purgeUnsupportedAccountCount(ctx, db)
	if err != nil {
		return report, err
	}
	if report.PostCheckAccounts != 0 {
		return report, fmt.Errorf("清退后仍有 %d 个不支持账户，请检查是否在 manifest 之外新建", report.PostCheckAccounts)
	}
	logf("apply：完成；不支持账户计数 0，真实厂商账户保持不变")
	return report, nil
}

const purgeTargetPredicate = `vendor_code IS NULL OR connection_driver_code = 'generic_http' OR vendor_code IS DISTINCT FROM connection_driver_code`

// purgeTargetAccountIDs 返回目标账户 + 其授权实例账户（实例即使 vendor 已复制
// 成真实厂商也随来源账户一起删除）。
func purgeTargetAccountIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		WITH base AS (
			SELECT id FROM juhe_business.accounts WHERE `+purgeTargetPredicate+`
		)
		SELECT id FROM base
		UNION
		SELECT a.id FROM juhe_business.accounts AS a JOIN base ON a.authorization_instance_source_account_id = base.id
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取待清退账户: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描待清退账户: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func purgeUnsupportedAccountCount(ctx context.Context, db *sql.DB) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM juhe_business.accounts WHERE `+purgeTargetPredicate).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计不支持账户: %w", err)
	}
	return count, nil
}

func purgeRealAccountCount(ctx context.Context, db *sql.DB) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM juhe_business.accounts WHERE vendor_code IS NOT NULL AND connection_driver_code = vendor_code`).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计真实厂商账户: %w", err)
	}
	return count, nil
}

func purgeInvariantIssues(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT 'vendor_driver_mismatch:' || id
		  FROM juhe_business.accounts
		 WHERE vendor_code IS NOT NULL AND connection_driver_code IS DISTINCT FROM vendor_code
		UNION ALL
		SELECT 'generic_preset_on_real_account:' || c.account_id || ':' || c.connection_preset_code
		  FROM juhe_business.account_connections AS c
		  JOIN juhe_business.accounts AS a ON a.id = c.account_id
		 WHERE c.connection_preset_code IN ('generic_openai_v1','generic_anthropic_v1','generic_gemini_v1beta')
		   AND a.vendor_code IS NOT NULL
		 ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("检查 vendor/driver 不变量: %w", err)
	}
	defer rows.Close()
	issues := []string{}
	for rows.Next() {
		var issue string
		if err := rows.Scan(&issue); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

func purgeDateRange(ctx context.Context, db *sql.DB, table, column string) (string, error) {
	var minValue, maxValue sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT min(`+column+`), max(`+column+`) FROM `+table).Scan(&minValue, &maxValue); err != nil {
		return "", fmt.Errorf("读取 %s 日期范围: %w", table, err)
	}
	if !minValue.Valid && !maxValue.Valid {
		return "", nil
	}
	return minValue.String + " .. " + maxValue.String, nil
}

func purgeIDsHash(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

func writePurgeManifest(path string, manifest purgeManifest) error {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return fmt.Errorf("创建 manifest 目录: %w", err)
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 manifest: %w", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入 manifest: %w", err)
	}
	return nil
}

func readPurgeManifest(path string) (purgeManifest, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return purgeManifest{}, fmt.Errorf("读取 manifest（先跑 --dry-run 生成）: %w", err)
	}
	var manifest purgeManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return purgeManifest{}, fmt.Errorf("解析 manifest: %w", err)
	}
	if manifest.Version != 1 {
		return purgeManifest{}, fmt.Errorf("manifest version=%d，期望 1", manifest.Version)
	}
	return manifest, nil
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `/\`)
	if index <= 0 {
		return "."
	}
	return path[:index]
}

// purgeTableSpec 是一类删除目标；countSQL/deleteSQL 的 WHERE 条件必须一致。
type purgeTableSpec struct {
	label     string
	countSQL  string
	deleteSQL string
}

// purgeTableSpecs 集中定义账户维度的删除目标；顺序即 FK/清理顺序。
func purgeTableSpecs() []purgeTableSpec {
	return []purgeTableSpec{
		{"juhe_business.account_health_jobs_input_versions", `SELECT count(*) FROM juhe_business.account_health_jobs_input_versions WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_business.account_health_jobs_input_versions WHERE account_id = ANY($1::text[])`},
		{"juhe_business.account_health_jobs_input_outbox", `SELECT count(*) FROM juhe_business.account_health_jobs_input_outbox WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_business.account_health_jobs_input_outbox WHERE account_id = ANY($1::text[])`},
		{"juhe_business.account_test_tasks", `SELECT count(*) FROM juhe_business.account_test_tasks WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_business.account_test_tasks WHERE account_id = ANY($1::text[])`},
		{"juhe_business.media_jobs", `SELECT count(*) FROM juhe_business.media_jobs WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_business.media_jobs WHERE account_id = ANY($1::text[])`},
		{"juhe_business.account_schedule_status_events", `SELECT count(*) FROM juhe_business.account_schedule_status_events WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_business.account_schedule_status_events WHERE account_id = ANY($1::text[])`},
		{"juhe_business.model_check_question_tests", `SELECT count(*) FROM juhe_business.model_check_question_tests WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_business.model_check_question_tests WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_health_outcomes", `SELECT count(*) FROM juhe_jobs.account_health_outcomes WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_health_outcomes WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_health_current_state", `SELECT count(*) FROM juhe_jobs.account_health_current_state WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_health_current_state WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_health_direct_input_suppressions", `SELECT count(*) FROM juhe_jobs.account_health_direct_input_suppressions WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_health_direct_input_suppressions WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_health_key_cursors", `SELECT count(*) FROM juhe_jobs.account_health_key_cursors WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_health_key_cursors WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_balance_account_leases", `SELECT count(*) FROM juhe_jobs.account_balance_account_leases WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_balance_account_leases WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_balance_snapshots", `SELECT count(*) FROM juhe_jobs.account_balance_snapshots WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_balance_snapshots WHERE account_id = ANY($1::text[])`},
		{"juhe_jobs.account_balance_outcomes", `SELECT count(*) FROM juhe_jobs.account_balance_outcomes WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_jobs.account_balance_outcomes WHERE account_id = ANY($1::text[])`},
		{"juhe_stats.account_quality_minute_stats", `SELECT count(*) FROM juhe_stats.account_quality_minute_stats WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_stats.account_quality_minute_stats WHERE account_id = ANY($1::text[])`},
		{"juhe_stats.account_health_hourly", `SELECT count(*) FROM juhe_stats.account_health_hourly WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_stats.account_health_hourly WHERE account_id = ANY($1::text[])`},
		{"juhe_stats.account_quality_scores", `SELECT count(*) FROM juhe_stats.account_quality_scores WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_stats.account_quality_scores WHERE account_id = ANY($1::text[])`},
		{"juhe_stats.account_quality_dirty_accounts", `SELECT count(*) FROM juhe_stats.account_quality_dirty_accounts WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_stats.account_quality_dirty_accounts WHERE account_id = ANY($1::text[])`},
		{"juhe_stats.account_usage_snapshots", `SELECT count(*) FROM juhe_stats.account_usage_snapshots WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_stats.account_usage_snapshots WHERE account_id = ANY($1::text[])`},
		{"juhe_usage.usage_records", `SELECT count(*) FROM juhe_usage.usage_records WHERE account_id = ANY($1::text[]) OR (account_id IS NULL AND provider_code IN ('openai','hybrid'))`, `DELETE FROM juhe_usage.usage_records WHERE account_id = ANY($1::text[]) OR (account_id IS NULL AND provider_code IN ('openai','hybrid'))`},
		{"juhe_usage.usage_record_shard_entries", `SELECT count(*) FROM juhe_usage.usage_record_shard_entries WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_usage.usage_record_shard_entries WHERE account_id = ANY($1::text[])`},
		{"juhe_usage.usage_record_account_shards", `SELECT count(*) FROM juhe_usage.usage_record_account_shards WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_usage.usage_record_account_shards WHERE account_id = ANY($1::text[])`},
		{"juhe_dataset.audit_logs", `SELECT count(*) FROM juhe_dataset.audit_logs WHERE account_id = ANY($1::text[]) OR (account_id IS NULL AND provider_code IN ('openai','hybrid'))`, `DELETE FROM juhe_dataset.audit_logs WHERE account_id = ANY($1::text[]) OR (account_id IS NULL AND provider_code IN ('openai','hybrid'))`},
		{"juhe_dataset.audit_log_attempts", `SELECT count(*) FROM juhe_dataset.audit_log_attempts WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_dataset.audit_log_attempts WHERE account_id = ANY($1::text[])`},
		{"juhe_dataset.audit_error_groups", `SELECT count(*) FROM juhe_dataset.audit_error_groups WHERE account_id = ANY($1::text[]) OR (account_id IS NULL AND provider_code IN ('openai','hybrid'))`, `DELETE FROM juhe_dataset.audit_error_groups WHERE account_id = ANY($1::text[]) OR (account_id IS NULL AND provider_code IN ('openai','hybrid'))`},
		{"juhe_dataset.account_record_cleanup_targets", `SELECT count(*) FROM juhe_dataset.account_record_cleanup_targets WHERE account_id = ANY($1::text[])`, `DELETE FROM juhe_dataset.account_record_cleanup_targets WHERE account_id = ANY($1::text[])`},
	}
}

// purgeProbeCounts 统计每张目标表命中的行数（dry-run 报告 + apply 删除前基线）。
func purgeProbeCounts(ctx context.Context, db *sql.DB, ids []string) ([]purgeTableCount, error) {
	counts := []purgeTableCount{}
	for _, spec := range purgeTableSpecs() {
		count, err := purgeQueryCount(ctx, db, spec.countSQL, ids)
		if err != nil {
			return nil, err
		}
		counts = append(counts, purgeTableCount{Table: spec.label, Count: count})
	}
	return counts, nil
}

func purgeQueryCount(ctx context.Context, db *sql.DB, countSQL string, ids []string) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, countSQL, ids).Scan(&count); err != nil {
		if purgeIsUndefinedTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("统计清退目标 %q: %w", countSQL, err)
	}
	return count, nil
}

func purgeIsUndefinedTable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "does not exist") || strings.Contains(message, "42p01")
}

// purgeExecBatched 按 ID 分批执行 DELETE；每批独立提交，失败后可用同一
// manifest 续跑。
func purgeExecBatched(ctx context.Context, db *sql.DB, deleteSQL string, ids []string, batchSize int) (int64, error) {
	total := int64(0)
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		result, err := db.ExecContext(ctx, deleteSQL, ids[start:end])
		if err != nil {
			if purgeIsUndefinedTable(err) {
				return total, nil
			}
			return total, fmt.Errorf("执行清退 %q: %w", deleteSQL, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
	}
	return total, nil
}

func purgeDeleteRows(ctx context.Context, db *sql.DB, ids []string, batchSize int, report *purgeUnsupportedVendorReport, logf func(string, ...any)) error {
	// 授权链先于账户删除（group_accounts.account_authorization_id 为 NO ACTION）。
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.request_quota_hourly_window_scope_bindings
		WHERE source_type = 'resource_authorization_grant'
		  AND source_id IN (SELECT id FROM juhe_business.resource_authorization_grants
		                    WHERE resource_type = 'account' AND resource_id = ANY($1::text[]))`, ids); err != nil {
		return fmt.Errorf("删除授权 quota 绑定: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.resource_authorization_grants
		WHERE resource_type = 'account' AND resource_id = ANY($1::text[])`, ids); err != nil {
		return fmt.Errorf("删除授权 grants: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.resource_authorization_sources
		WHERE authorization_id IN (SELECT id FROM juhe_business.resource_authorizations
		                           WHERE resource_type = 'account' AND resource_id = ANY($1::text[]))`, ids); err != nil {
		return fmt.Errorf("删除授权 sources: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.resource_authorizations
		WHERE resource_type = 'account' AND resource_id = ANY($1::text[])`, ids); err != nil {
		return fmt.Errorf("删除授权 rows: %w", err)
	}
	// 统计表按 scope 维度删除（与 recordcleanup 的账户 scope 口径一致）。
	statsTables := []string{
		"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily",
		"usage_stats_weekly", "usage_stats_monthly", "usage_latency_minute", "usage_latency_hourly",
		"usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly",
		"usage_quota_hourly_windows", "usage_scope_range_windows",
	}
	for _, table := range statsTables {
		stmt := fmt.Sprintf(`DELETE FROM juhe_stats.%s WHERE scope_type IN ('account','caller_account') AND scope_id = ANY($1::text[])`, table)
		if _, err := db.ExecContext(ctx, stmt, ids); err != nil {
			return fmt.Errorf("删除 %s: %w", table, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_stats.usage_rank_snapshots
		WHERE scope_type IN ('account','caller_account') AND scope_id = ANY($1::text[])`, ids); err != nil {
		return fmt.Errorf("删除 usage_rank_snapshots: %w", err)
	}
	for index, spec := range purgeTableSpecs() {
		deleted, err := purgeExecBatched(ctx, db, spec.deleteSQL, ids, batchSize)
		if err != nil {
			return err
		}
		if index < len(report.Tables) {
			report.Tables[index].Deleted = deleted
		}
		logf("已删除 %s: %d 行", spec.label, deleted)
	}
	// 账户本体：先删授权实例，再删来源，避免自引用 FK 逐行检查失败。
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.accounts
		WHERE id = ANY($1::text[]) AND authorization_instance_source_account_id IS NOT NULL`, ids); err != nil {
		return fmt.Errorf("删除授权实例账户: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])`, ids); err != nil {
		return fmt.Errorf("删除账户: %w", err)
	}
	// 非九家 models/vendors 残留：先删模型，再删厂商；models.vendor_code NULL
	// 的手工定价模型保留。
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.models
		WHERE vendor_code IS NOT NULL AND vendor_code NOT IN ('gpt','xai','deepseek','anthropic','gemini','glm','minimax','volcengine','qwen')
		  AND NOT EXISTS (SELECT 1 FROM juhe_business.account_supported_models AS s WHERE s.model_id = models.id)`); err != nil {
		return fmt.Errorf("删除非九家模型行: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.vendors
		WHERE code NOT IN ('gpt','xai','deepseek','anthropic','gemini','glm','minimax','volcengine','qwen')
		  AND NOT EXISTS (SELECT 1 FROM juhe_business.accounts AS a WHERE a.vendor_code = vendors.code)
		  AND NOT EXISTS (SELECT 1 FROM juhe_business.models AS m WHERE m.vendor_code = vendors.code)`); err != nil {
		return fmt.Errorf("删除非九家供应商行: %w", err)
	}
	return nil
}
