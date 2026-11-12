// D13 非对话账户 cutover 清理命令（purge-non-chat-accounts）：
//   - 目标账户：capability_domain <> 'chat'（video/audio/image/embedding/
//     rerank/moderation/realtime），以及这些账户的授权实例；
//   - 用户 2026-11-12 裁决：非对话账户当前没有实际使用，上线统一删除，只保留
//     对话账户；不做向后兼容、不保留混合账户迁移；
//   - dry-run 只读探测并写 manifest；apply 必须带 --stop-write-confirmed 与
//     --backup-confirmed，复用 dry-run manifest，执行前复核目标集合未漂移；
//   - 复用 purge 命令的分批删除、usage/audit/stats 清理与幂等重跑设施。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const purgeNonChatCommandName = "purge-non-chat-accounts"

const purgeNonChatPredicate = `capability_domain <> 'chat'`

const purgeNonChatRollbackNote = "非对话账户删除不可逆；回滚 = 用 --backup-confirmed 验证过的整库备份恢复。" +
	"cutover 后旧二进制不再受支持，数据库恢复后按新版本重建。"

type purgeNonChatGate struct {
	DSN                string
	DryRun             bool
	StopWriteConfirmed bool
	BackupConfirmed    bool
	ManifestOut        string
	BatchSize          int
}

func (g purgeNonChatGate) validate() error {
	dsn := strings.TrimSpace(g.DSN)
	if dsn == "" {
		return fmt.Errorf("%s 需要 --dsn 指向显式的 PostgreSQL URL", purgeNonChatCommandName)
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return fmt.Errorf("%s 的 --dsn 必须是 postgres:// 或 postgresql:// URL", purgeNonChatCommandName)
	}
	if strings.TrimSpace(g.ManifestOut) == "" {
		return fmt.Errorf("%s 需要 --manifest-out（dry-run 写出、apply 读取同一清单）", purgeNonChatCommandName)
	}
	if g.DryRun {
		return nil
	}
	if !g.StopWriteConfirmed {
		return fmt.Errorf("%s 正式执行需要 --stop-write-confirmed（gateway/jobs 已停止）", purgeNonChatCommandName)
	}
	if !g.BackupConfirmed {
		return fmt.Errorf("%s 正式执行需要 --backup-confirmed（可恢复备份已验证）", purgeNonChatCommandName)
	}
	return nil
}

func runPurgeNonChatAccountsCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance "+purgeNonChatCommandName, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL of the business database")
	dryRun := fs.Bool("dry-run", false, "read-only probe and manifest write; no rows are deleted")
	stopWriteConfirmed := fs.Bool("stop-write-confirmed", false, "confirm gateway/jobs are stopped for the cutover")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable full backup was verified")
	manifestOut := fs.String("manifest-out", "", "manifest JSON path (dry-run writes it; apply reads the same file)")
	batchSize := fs.Int("batch-size", 1000, "delete batch size (clamped to 1..50000)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	gate := purgeNonChatGate{
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
	report, runErr := runPurgeNonChatAccounts(context.Background(), db, gate, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "["+purgeNonChatCommandName+"] "+format+"\n", args...)
	})
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "非对话账户 cutover 清理失败: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode %s report: %v\n", purgeNonChatCommandName, err)
		return 1
	}
	return 0
}

func runPurgeNonChatAccounts(ctx context.Context, db *sql.DB, gate purgeNonChatGate, logf func(string, ...any)) (purgeUnsupportedVendorReport, error) {
	if db == nil {
		return purgeUnsupportedVendorReport{}, errors.New("cutover 数据库未初始化")
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
		Rollback: purgeNonChatRollbackNote,
	}
	batch := gate.BatchSize
	if batch < 1 {
		batch = 1
	}
	if batch > 50000 {
		batch = 50000
	}

	liveIDs, err := purgeNonChatTargetAccountIDs(ctx, db)
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
			logf("dry-run：目标库 %s 没有非对话账户；仍写出空 manifest 以便复核", database)
		} else {
			logf("dry-run：目标库 %s 待删除非对话账户 %d 个（含授权实例）；只读探测并写 manifest %s", database, len(ids), report.Manifest)
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
			if err := purgeValidateStaleManifest(ctx, db, ids); err != nil {
				return report, err
			}
			logf("apply：目标库 %s 已无非对话账户（上一轮已完成），按 manifest 做幂等复查", database)
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
			Predicate:    purgeNonChatPredicate,
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
		report.PostCheckAccounts, err = purgeNonChatAccountCount(ctx, db)
		return report, err
	}
	if pending, err := purgePendingSpoolFiles(); err != nil {
		return report, err
	} else if pending > 0 {
		return report, fmt.Errorf("usage spool 仍有 %d 个待消费文件；请先启动 jobs 排空（或确认停写后清理）再执行 purge", pending)
	}

	if err := purgeDeleteRows(ctx, db, ids, batch, &report, logf); err != nil {
		return report, err
	}
	report.PostCheckAccounts, err = purgeNonChatAccountCount(ctx, db)
	if err != nil {
		return report, err
	}
	if report.PostCheckAccounts != 0 {
		return report, fmt.Errorf("删除后仍有 %d 个非对话账户，请检查是否在 manifest 之外新建", report.PostCheckAccounts)
	}
	logf("apply：完成；非对话账户计数 0，对话账户保持不变")
	return report, nil
}

func purgeNonChatTargetAccountIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		WITH base AS (
			SELECT id FROM juhe_business.accounts WHERE `+purgeNonChatPredicate+`
		)
		SELECT id FROM base
		UNION
		SELECT a.id FROM juhe_business.accounts AS a JOIN base ON a.authorization_instance_source_account_id = base.id
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取待删除非对话账户: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描待删除非对话账户: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func purgeNonChatAccountCount(ctx context.Context, db *sql.DB) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.accounts WHERE `+purgeNonChatPredicate).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计非对话账户: %w", err)
	}
	return count, nil
}
