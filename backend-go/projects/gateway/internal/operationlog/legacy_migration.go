package operationlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"

	_ "modernc.org/sqlite"
)

// LegacyMigrationOptions is the explicit stop-and-copy gate for F4 history.
// It is never called by normal sidecar startup. Source data is read only and
// is never deleted or modified by the SQLite migration.
type LegacyMigrationOptions struct {
	SourceDatabasePath string
	NodeStopped        bool
	GoStopped          bool
	BackupConfirmed    bool
}

// LegacyMigrationResult is emitted by the offline command for the runbook.
// Search terms are intentionally rebuilt by the current Go tokenizer, so the
// source and target search-term counts can legitimately differ.
type LegacyMigrationResult struct {
	Mode                  string           `json:"mode"`
	NoOp                  bool             `json:"noOp"`
	SourceCounts          map[string]int64 `json:"sourceCounts"`
	TargetCounts          map[string]int64 `json:"targetCounts"`
	SearchTermsRebuilt    bool             `json:"searchTermsRebuilt"`
	MigratedOperationLogs int64            `json:"migratedOperationLogs"`
}

type legacyLeaseRenewer struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func startLegacyLeaseRenewer(ctx context.Context, store Store, lease OwnerLease, duration time.Duration) *legacyLeaseRenewer {
	renewCtx, cancel := context.WithCancel(ctx)
	r := &legacyLeaseRenewer{cancel: cancel, done: make(chan struct{})}
	interval := duration / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	go func() {
		defer safego.Recover("operationlog.legacy_migration.lease_renew")
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				ok, err := store.RenewOwnerLease(renewCtx, lease, duration)
				if renewCtx.Err() != nil {
					// Stop 取消上下文时在途续租会带着 context.Canceled 返回，
					// 那是主动停止不是租约失败，不能记成 Err 让迁移误判租约丢失。
					return
				}
				if err == nil && !ok {
					err = ErrOwnerLeaseLost
				}
				if err == nil {
					continue
				}
				r.mu.Lock()
				r.err = err
				r.mu.Unlock()
				return
			}
		}
	}()
	return r
}

func (r *legacyLeaseRenewer) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *legacyLeaseRenewer) Stop() error {
	r.cancel()
	<-r.done
	return r.Err()
}

func requireLegacyMigrationGates(options LegacyMigrationOptions) error {
	if !options.NodeStopped || !options.GoStopped {
		return errors.New("F4 操作日志离线迁移要求停机：必须同时确认 Node 和 Go 已停止（--node-stopped --go-stopped）")
	}
	if !options.BackupConfirmed {
		return errors.New("F4 操作日志离线迁移要求已完成可恢复备份确认（--backup-confirmed）")
	}
	return nil
}

// MigrateLegacySQLite copies the old Node dataset operation-log tables into
// the dedicated F4 SQLite store. It keeps the stable operation-log IDs and
// rebuilds only the derived search-term index with the current Go algorithm.

// MigrateLegacyPostgres upgrades the historical Node viewer primary key in
// place. PostgreSQL uses the same juhe_dataset table names for F4, so copying
// into another live owner is unsafe; this command instead performs the one
// catalog-checked DDL transformation under an advisory transaction lock.
func MigrateLegacyPostgres(ctx context.Context, cfg Config, options LegacyMigrationOptions) (LegacyMigrationResult, error) {
	if err := requireLegacyMigrationGates(options); err != nil {
		return LegacyMigrationResult{}, err
	}
	store, err := OpenStore(cfg)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("打开 F4 PostgreSQL 目标失败: %w", err)
	}
	defer store.Close()
	migrationCtx, cancel := context.WithTimeout(ctx, legacyMigrationDeadline)
	defer cancel()
	concrete := store.(*sqlStore)
	tx, err := concrete.beginLegacyMigrationTx(migrationCtx)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(migrationCtx, "SELECT pg_advisory_xact_lock(763847296)"); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("获取 F4 PostgreSQL 历史迁移锁失败: %w", err)
	}
	counts, err := operationLogCounts(migrationCtx, tx, "juhe_dataset.")
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	samples, err := snapshotPostgresLegacySamples(migrationCtx, tx)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	primaryKey, err := postgresPrimaryKey(migrationCtx, tx, "juhe_dataset.operation_log_viewers")
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	if primaryKey == postgresPrimaryKeys["operation_log_viewers"] {
		if err := validatePostgresSchema(migrationCtx, postgresSQLCatalog{queryer: tx}); err != nil {
			return LegacyMigrationResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return LegacyMigrationResult{}, err
		}
		return LegacyMigrationResult{Mode: "postgres-in-place", NoOp: true, SourceCounts: counts, TargetCounts: counts}, nil
	}
	const legacyViewerPrimaryKey = "operation_log_id,system_account_id,visibility_reason"
	if primaryKey != legacyViewerPrimaryKey {
		return LegacyMigrationResult{}, fmt.Errorf("F4 PostgreSQL 历史迁移拒绝未知 operation_log_viewers 主键 %q", primaryKey)
	}
	constraint, err := postgresPrimaryKeyConstraint(migrationCtx, tx, "juhe_dataset.operation_log_viewers")
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	if _, err = tx.ExecContext(migrationCtx, `ALTER TABLE juhe_dataset.operation_log_viewers DROP CONSTRAINT `+quotePostgresIdentifier(constraint)); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("升级旧 Node operation_log_viewers 主键失败: %w", err)
	}
	if _, err = tx.ExecContext(migrationCtx, `ALTER TABLE juhe_dataset.operation_log_viewers ADD CONSTRAINT operation_log_viewers_pkey PRIMARY KEY (operation_log_id,system_account_id,visibility_reason,detail_level)`); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("创建 F4 operation_log_viewers 主键失败: %w", err)
	}
	if err = rebuildPostgresOperationLogIndexes(migrationCtx, tx); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err = rebuildPostgresOperationLogSearchTerms(migrationCtx, tx); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err = applyPostgresSchema(migrationCtx, tx); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err = validatePostgresSchema(migrationCtx, postgresSQLCatalog{queryer: tx}); err != nil {
		return LegacyMigrationResult{}, err
	}
	targetCounts, err := operationLogCounts(migrationCtx, tx, "juhe_dataset.")
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	for _, table := range []string{"operation_logs", "operation_log_targets", "operation_log_viewers"} {
		if counts[table] != targetCounts[table] {
			return LegacyMigrationResult{}, fmt.Errorf("F4 PostgreSQL 历史迁移 %s 行数变化: before=%d after=%d", table, counts[table], targetCounts[table])
		}
	}
	if err = verifyPostgresLegacySamples(migrationCtx, tx, samples); err != nil {
		return LegacyMigrationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("提交 F4 PostgreSQL 历史迁移失败: %w", err)
	}
	return LegacyMigrationResult{Mode: "postgres-in-place", SourceCounts: counts, TargetCounts: targetCounts, SearchTermsRebuilt: true, MigratedOperationLogs: counts["operation_logs"]}, nil
}

func rebuildPostgresOperationLogIndexes(ctx context.Context, tx *sql.Tx) error {
	for index := range postgresRequiredIndexDefinitions {
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS juhe_dataset.`+quotePostgresIdentifier(index)); err != nil {
			return fmt.Errorf("删除旧 Node F4 索引 %s 失败: %w", index, err)
		}
	}
	return nil
}

func rebuildPostgresOperationLogSearchTerms(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM juhe_dataset.operation_log_summary_search_terms`); err != nil {
		return fmt.Errorf("清理旧 Node F4 search terms 失败: %w", err)
	}
	var lastCreated *time.Time
	var lastID string
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,summary,created_at FROM juhe_dataset.operation_logs WHERE ($1::timestamptz IS NULL OR (created_at,id) > ($1::timestamptz,$2)) ORDER BY created_at,id LIMIT 200`, lastCreated, lastID)
		if err != nil {
			return fmt.Errorf("读取 F4 PostgreSQL search term 重建批次失败: %w", err)
		}
		type row struct {
			id        string
			summary   string
			createdAt time.Time
		}
		batch := make([]row, 0, 200)
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.summary, &item.createdAt); err != nil {
				_ = rows.Close()
				return fmt.Errorf("读取 F4 PostgreSQL search term 重建行失败: %w", err)
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("遍历 F4 PostgreSQL search term 重建批次失败: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("关闭 F4 PostgreSQL search term 重建批次失败: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		logIDs, terms, created := make([]string, 0), make([]string, 0), make([]string, 0)
		for _, item := range batch {
			for _, term := range searchTerms(item.summary) {
				logIDs = append(logIDs, item.id)
				terms = append(terms, term)
				created = append(created, storageTime(item.createdAt))
			}
		}
		if len(terms) > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_dataset.operation_log_summary_search_terms (operation_log_id,term,created_at) SELECT operation_log_id,term,created_at::timestamptz FROM unnest($1::text[],$2::text[],$3::text[]) AS s(operation_log_id,term,created_at) ON CONFLICT DO NOTHING`, logIDs, terms, created); err != nil {
				return fmt.Errorf("写入 F4 PostgreSQL 重建 search terms 失败: %w", err)
			}
		}
		latest := batch[len(batch)-1]
		lastCreated, lastID = &latest.createdAt, latest.id
	}
}

const postgresLegacyOperationLogProjection = `SELECT id,COALESCE(trace_id,''),actor_system_account_id,COALESCE(actor_username,''),COALESCE(actor_display_name,''),actor_role,COALESCE(operation_scope_system_account_id,''),mode,module,action,operation_key,resource_type,COALESCE(resource_id,''),COALESCE(resource_name,''),summary,detail_level,visibility_scope,changes_json::text,metadata_json::text,COALESCE(method,''),COALESCE(path,''),status_code,COALESCE(client_ip,''),COALESCE(user_agent,''),created_at FROM juhe_dataset.operation_logs`

func snapshotPostgresLegacySamples(ctx context.Context, tx *sql.Tx) ([]legacyOperationLog, error) {
	queries := []string{
		postgresLegacyOperationLogProjection + ` ORDER BY created_at,id LIMIT 1`,
		postgresLegacyOperationLogProjection + ` ORDER BY created_at DESC,id DESC LIMIT 1`,
	}
	samples := make([]legacyOperationLog, 0, 2)
	seen := map[string]bool{}
	for _, query := range queries {
		record, found, err := readPostgresLegacyRecord(ctx, tx, query, nil)
		if err != nil {
			return nil, err
		}
		if found && !seen[record.Input.ID] {
			seen[record.Input.ID] = true
			samples = append(samples, record)
		}
	}
	return samples, nil
}

func verifyPostgresLegacySamples(ctx context.Context, tx *sql.Tx, samples []legacyOperationLog) error {
	for _, expected := range samples {
		actual, found, err := readPostgresLegacyRecord(ctx, tx, postgresLegacyOperationLogProjection+` WHERE id=$1`, []any{expected.Input.ID})
		if err != nil {
			return err
		}
		if !found || !reflect.DeepEqual(actual.Input, expected.Input) || actual.RawChangesJSON != expected.RawChangesJSON || actual.RawMetadataJSON != expected.RawMetadataJSON || !reflect.DeepEqual(actual.Targets, expected.Targets) || !reflect.DeepEqual(actual.Viewers, expected.Viewers) {
			return fmt.Errorf("F4 PostgreSQL 历史迁移抽样 %s 的业务事实不一致", expected.Input.ID)
		}
	}
	return nil
}

func readPostgresLegacyRecord(ctx context.Context, tx *sql.Tx, query string, args []any) (legacyOperationLog, bool, error) {
	row := tx.QueryRowContext(ctx, query, args...)
	record, err := scanLegacyOperationLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return legacyOperationLog{}, false, nil
	}
	if err != nil {
		return legacyOperationLog{}, false, err
	}
	if record.Targets, err = readPostgresLegacyTargets(ctx, tx, record.Input.ID); err != nil {
		return legacyOperationLog{}, false, err
	}
	if record.Viewers, err = readPostgresLegacyViewers(ctx, tx, record.Input.ID); err != nil {
		return legacyOperationLog{}, false, err
	}
	return record, true, nil
}

func readPostgresLegacyTargets(ctx context.Context, tx *sql.Tx, operationLogID string) ([]legacyTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,target_type,COALESCE(target_id,''),COALESCE(target_name,''),COALESCE(target_owner_system_account_id,''),relation,created_at FROM juhe_dataset.operation_log_targets WHERE operation_log_id=$1 ORDER BY created_at,id`, operationLogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]legacyTarget, 0)
	for rows.Next() {
		var target legacyTarget
		var createdAt storageTimestamp
		if err := rows.Scan(&target.ID, &target.Target.TargetType, &target.Target.TargetID, &target.Target.TargetName, &target.Target.TargetOwnerSystemAccountID, &target.Target.Relation, &createdAt); err != nil {
			return nil, err
		}
		target.CreatedAt = string(createdAt)
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func readPostgresLegacyViewers(ctx context.Context, tx *sql.Tx, operationLogID string) ([]legacyViewer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT system_account_id,visibility_reason,detail_level,created_at FROM juhe_dataset.operation_log_viewers WHERE operation_log_id=$1 ORDER BY system_account_id,visibility_reason,detail_level`, operationLogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	viewers := make([]legacyViewer, 0)
	for rows.Next() {
		var viewer legacyViewer
		var createdAt storageTimestamp
		if err := rows.Scan(&viewer.Viewer.SystemAccountID, &viewer.Viewer.VisibilityReason, &viewer.Viewer.DetailLevel, &createdAt); err != nil {
			return nil, err
		}
		viewer.CreatedAt = string(createdAt)
		viewers = append(viewers, viewer)
	}
	return viewers, rows.Err()
}





var legacyOperationLogTables = []string{
	"operation_logs",
	"operation_log_targets",
	"operation_log_viewers",
	"operation_log_summary_search_terms",
}

type operationLogCounter interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func operationLogCounts(ctx context.Context, db operationLogCounter, prefix string) (map[string]int64, error) {
	counts := make(map[string]int64, len(legacyOperationLogTables))
	for _, table := range legacyOperationLogTables {
		var count int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+prefix+table).Scan(&count); err != nil {
			return nil, fmt.Errorf("读取操作日志表 %s 行数失败: %w", table, err)
		}
		counts[table] = count
	}
	return counts, nil
}

type legacyOperationLogScanner interface {
	Scan(...any) error
}

type legacyOperationLog struct {
	Input           Input
	RawChangesJSON  string
	RawMetadataJSON string
	Targets         []legacyTarget
	Viewers         []legacyViewer
}

type legacyTarget struct {
	ID        string
	Target    Target
	CreatedAt string
}

type legacyViewer struct {
	Viewer    Viewer
	CreatedAt string
}

func scanLegacyOperationLog(row legacyOperationLogScanner) (legacyOperationLog, error) {
	var record legacyOperationLog
	input := &record.Input
	var changes, metadata string
	var createdAt storageTimestamp
	if err := row.Scan(&input.ID, &input.TraceID, &input.ActorSystemAccountID, &input.ActorUsername, &input.ActorDisplayName, &input.ActorRole, &input.OperationScopeSystemAccountID, &input.Mode, &input.Module, &input.Action, &input.OperationKey, &input.ResourceType, &input.ResourceID, &input.ResourceName, &input.Summary, &input.DetailLevel, &input.VisibilityScope, &changes, &metadata, &input.Method, &input.Path, &input.StatusCode, &input.ClientIP, &input.UserAgent, &createdAt); err != nil {
		return legacyOperationLog{}, fmt.Errorf("读取旧 Node 操作日志行失败: %w", err)
	}
	input.CreatedAt = string(createdAt)
	if !json.Valid([]byte(changes)) {
		return legacyOperationLog{}, fmt.Errorf("旧 Node 操作日志 %s changes_json 无效", input.ID)
	}
	if err := json.Unmarshal([]byte(changes), &input.Changes); err != nil {
		return legacyOperationLog{}, fmt.Errorf("旧 Node 操作日志 %s changes_json 无效: %w", input.ID, err)
	}
	if !json.Valid([]byte(metadata)) {
		return legacyOperationLog{}, fmt.Errorf("旧 Node 操作日志 %s metadata_json 无效", input.ID)
	}
	input.Metadata = json.RawMessage(metadata)
	if err := normalizeLegacyOperationLogInput(*input); err != nil {
		return legacyOperationLog{}, fmt.Errorf("旧 Node 操作日志 %s 不兼容: %w", input.ID, err)
	}
	input.CreatedAt, _ = parseStorageTime(input.CreatedAt)
	record.RawChangesJSON, record.RawMetadataJSON = changes, metadata
	return record, nil
}



func normalizeLegacyOperationLogInput(input Input) error {
	for name, value := range map[string]string{"id": input.ID, "actorSystemAccountId": input.ActorSystemAccountID, "actorRole": input.ActorRole, "mode": input.Mode, "module": input.Module, "action": input.Action, "operationKey": input.OperationKey, "resourceType": input.ResourceType, "summary": input.Summary, "detailLevel": input.DetailLevel, "visibilityScope": input.VisibilityScope, "createdAt": input.CreatedAt} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("缺少 %s", name)
		}
	}
	if _, err := parseStorageTime(input.CreatedAt); err != nil {
		return fmt.Errorf("created_at 无效: %w", err)
	}
	if !known(input.Mode, "self", "admin") || !known(input.DetailLevel, "full", "summary") || !known(input.VisibilityScope, "targeted", "all_users", "admin_only") {
		return errors.New("枚举值无效")
	}
	if !json.Valid(input.Metadata) {
		return errors.New("metadata_json 无效")
	}
	return nil
}

// persistLegacySQLiteOperationLog intentionally bypasses normalizeInput. The
// ordinary write path derives viewers/targets for new data; a history copy must
// retain exactly the source facts, including original target row IDs.

type legacyRecordQueryer interface {
	legacyQueryer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}


type legacyQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}






func postgresPrimaryKey(ctx context.Context, db operationLogCounter, qualifiedTable string) (string, error) {
	var primaryKey sql.NullString
	err := db.QueryRowContext(ctx, `SELECT string_agg(a.attname,',' ORDER BY key.ordinality) FROM pg_constraint c CROSS JOIN unnest(c.conkey) WITH ORDINALITY AS key(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.attnum WHERE c.conrelid=$1::regclass AND c.contype='p'`, qualifiedTable).Scan(&primaryKey)
	if err != nil {
		return "", fmt.Errorf("读取 %s 主键失败: %w", qualifiedTable, err)
	}
	return primaryKey.String, nil
}

func postgresPrimaryKeyConstraint(ctx context.Context, db operationLogCounter, qualifiedTable string) (string, error) {
	var name string
	err := db.QueryRowContext(ctx, `SELECT conname FROM pg_constraint WHERE conrelid=$1::regclass AND contype='p'`, qualifiedTable).Scan(&name)
	if err != nil {
		return "", fmt.Errorf("读取 %s 主键约束失败: %w", qualifiedTable, err)
	}
	return name, nil
}

func quotePostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
