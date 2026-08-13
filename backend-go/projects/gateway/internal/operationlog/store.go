package operationlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

var ErrOwnerLeaseLost = errors.New("F4 operation-log owner lease lost")

const (
	maxListWindowRows = 1001
	maxPageSize       = 50

	postgresApplicationName         = "juhe-ai-gateway-f4-operationlog"
	postgresStatementTimeout        = "10s"
	postgresLockTimeout             = "1s"
	postgresIdleTransactionTimeout  = "10s"
	legacyMigrationDeadline         = 30 * time.Minute
	legacyMigrationStatementTimeout = "5min"
	legacyMigrationLockTimeout      = "30s"
	legacyMigrationIdleTimeout      = "10min"
)

type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}
type Store interface {
	EnsureSchema(context.Context) error
	AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error)
	RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error)
	ReleaseOwnerLease(context.Context, OwnerLease) error
	Persist(context.Context, OwnerLease, Input) (bool, error)
	List(context.Context, ListOptions) (ListResult, error)
	Detail(context.Context, string, string) (DetailSupplement, bool, error)
	CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error)
	RetentionDays(context.Context, int) (int, error)
	Close() error
}
type sqlStore struct {
	db          *sql.DB
	businessDB  *sql.DB
	mode        Mode
	writeMu     sync.Mutex
	schemaMu    sync.Mutex
	schemaReady bool
}

func OpenStore(cfg Config) (Store, error) {
	if cfg.Mode == ModeSQLite {
		if err := ensureDistinctSQLitePaths(cfg.DatabasePath, append([]string{cfg.BusinessSettingsPath}, cfg.SQLiteIsolationPaths...)...); err != nil {
			return nil, err
		}
		dsn, err := sqliteDSN(cfg.DatabasePath)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := configureSQLite(db); err != nil {
			_ = db.Close()
			return nil, err
		}
		businessDB, err := openSQLiteReadOnly(cfg.BusinessSettingsPath)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		return &sqlStore{db: db, businessDB: businessDB, mode: cfg.Mode}, nil
	}
	pgConfig, err := pgx.ParseConfig(cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("parse F4 PostgreSQL URL: %w", err)
	}
	if pgConfig.RuntimeParams == nil {
		pgConfig.RuntimeParams = map[string]string{}
	}
	pgConfig.RuntimeParams["application_name"] = postgresApplicationName
	db := stdlib.OpenDB(*pgConfig)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	return &sqlStore{db: db, mode: cfg.Mode}, nil
}

func (s *sqlStore) beginTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil || s.mode != ModePostgres {
		return tx, err
	}
	for _, setting := range []string{
		"SET LOCAL statement_timeout = '" + postgresStatementTimeout + "'",
		"SET LOCAL lock_timeout = '" + postgresLockTimeout + "'",
		"SET LOCAL idle_in_transaction_session_timeout = '" + postgresIdleTransactionTimeout + "'",
	} {
		if _, err = tx.ExecContext(ctx, setting); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("configure F4 PostgreSQL transaction: %w", err)
		}
	}
	return tx, nil
}

func (s *sqlStore) beginLegacyMigrationTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil || s.mode != ModePostgres {
		return tx, err
	}
	for _, setting := range []string{
		"SET LOCAL statement_timeout = '" + legacyMigrationStatementTimeout + "'",
		"SET LOCAL lock_timeout = '" + legacyMigrationLockTimeout + "'",
		"SET LOCAL idle_in_transaction_session_timeout = '" + legacyMigrationIdleTimeout + "'",
	} {
		if _, err = tx.ExecContext(ctx, setting); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("configure F4 PostgreSQL legacy migration transaction: %w", err)
		}
	}
	return tx, nil
}

func ensureDistinctSQLitePaths(operationPath string, otherPaths ...string) error {
	operationAbs, err := filepath.Abs(operationPath)
	if err != nil {
		return err
	}
	canonical := func(path string) string {
		abs, _ := filepath.Abs(path)
		if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(abs)
	}
	operationCanonical := canonical(operationAbs)
	operationInfo, operationStatErr := os.Stat(operationAbs)
	for _, otherPath := range otherPaths {
		if strings.TrimSpace(otherPath) == "" {
			continue
		}
		otherAbs, absErr := filepath.Abs(otherPath)
		if absErr != nil {
			return absErr
		}
		if operationCanonical == canonical(otherAbs) {
			return fmt.Errorf("F4 operation log SQLite database must be physically distinct from %s", otherPath)
		}
		if operationStatErr == nil {
			if otherInfo, statErr := os.Stat(otherAbs); statErr == nil && os.SameFile(operationInfo, otherInfo) {
				return fmt.Errorf("F4 operation log SQLite database must not share an inode with %s", otherPath)
			}
		}
	}
	return nil
}
func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: "_pragma=busy_timeout(5000)"}).String(), nil
}
func configureSQLite(db *sql.DB) error {
	ctx := context.Background()
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var timeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		return fmt.Errorf("F4 SQLite busy timeout invalid: %d: %w", timeout, err)
	}
	return nil
}
func (s *sqlStore) Close() error {
	if s.businessDB != nil {
		_ = s.businessDB.Close()
	}
	return s.db.Close()
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("read F4 business settings SQLite: %w", err)
	}
	filePath := filepath.ToSlash(abs)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	dsn := (&url.URL{Scheme: "file", Path: filePath, RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *sqlStore) RetentionDays(ctx context.Context, fallback int) (int, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	var value string
	var err error
	if s.mode == ModeSQLite {
		err = s.businessDB.QueryRowContext(ctx, "SELECT value_json FROM system_settings WHERE system_account_id=? AND key=?", "sys_admin", "operationLogRetentionDays").Scan(&value)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT value_json FROM juhe_business.system_settings WHERE system_account_id=$1 AND key=$2", "sys_admin", "operationLogRetentionDays").Scan(&value)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read operationLogRetentionDays: %w", err)
	}
	var number any
	if err := json.Unmarshal([]byte(value), &number); err != nil {
		return 0, fmt.Errorf("operationLogRetentionDays is not JSON: %w", err)
	}
	parsed, ok := number.(float64)
	if !ok || parsed != float64(int(parsed)) || parsed < 1 || parsed > 3650 {
		return 0, fmt.Errorf("operationLogRetentionDays must be integer 1..3650")
	}
	return int(parsed), nil
}
func (s *sqlStore) table(name string) string {
	if s.mode == ModePostgres {
		return "juhe_dataset." + name
	}
	return name
}
func (s *sqlStore) bind(q string) string {
	if s.mode != ModePostgres {
		return q
	}
	for n := 1; strings.Contains(q, "?"); n++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", n), 1)
	}
	return q
}
func (s *sqlStore) EnsureSchema(ctx context.Context) error {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		if _, err := s.db.ExecContext(ctx, sqliteSchema); err != nil {
			return fmt.Errorf("initialize F4 sqlite schema: %w", err)
		}
	} else {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(763847296)"); err != nil {
			return err
		}
		if err = applyPostgresSchema(ctx, tx); err != nil {
			return err
		}
		if err = validatePostgresSchema(ctx, postgresSQLCatalog{queryer: tx}); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	s.schemaReady = true
	return nil
}

func applyPostgresSchema(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range strings.Split(postgresSchema, ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("initialize F4 postgres schema: %w", err)
			}
		}
	}
	return nil
}

type postgresSchemaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type postgresSchemaCatalog interface {
	String(context.Context, string, ...any) (string, error)
	Bool(context.Context, string, ...any) (bool, error)
}

type postgresSQLCatalog struct{ queryer postgresSchemaQueryer }

func (c postgresSQLCatalog) String(ctx context.Context, query string, args ...any) (string, error) {
	var value string
	err := c.queryer.QueryRowContext(ctx, query, args...).Scan(&value)
	return value, err
}

func (c postgresSQLCatalog) Bool(ctx context.Context, query string, args ...any) (bool, error) {
	var value bool
	err := c.queryer.QueryRowContext(ctx, query, args...).Scan(&value)
	return value, err
}

type postgresColumn struct {
	name     string
	typeName string
}

var postgresSchemaColumns = map[string][]postgresColumn{
	"operation_log_owner_leases": {
		{"lease_key", "text"}, {"owner_id", "text"}, {"fence_token", "bigint"}, {"lease_until", "timestamp with time zone"}, {"updated_at", "timestamp with time zone"},
	},
	"operation_logs": {
		{"id", "text"}, {"trace_id", "text"}, {"actor_system_account_id", "text"}, {"actor_username", "text"}, {"actor_display_name", "text"}, {"actor_role", "text"}, {"operation_scope_system_account_id", "text"}, {"mode", "text"}, {"module", "text"}, {"action", "text"}, {"operation_key", "text"}, {"resource_type", "text"}, {"resource_id", "text"}, {"resource_name", "text"}, {"summary", "text"}, {"detail_level", "text"}, {"visibility_scope", "text"}, {"changes_json", "jsonb"}, {"metadata_json", "jsonb"}, {"method", "text"}, {"path", "text"}, {"status_code", "integer"}, {"client_ip", "text"}, {"user_agent", "text"}, {"created_at", "timestamp with time zone"},
	},
	"operation_log_targets": {
		{"id", "text"}, {"operation_log_id", "text"}, {"target_type", "text"}, {"target_id", "text"}, {"target_name", "text"}, {"target_owner_system_account_id", "text"}, {"relation", "text"}, {"created_at", "timestamp with time zone"},
	},
	"operation_log_viewers": {
		{"operation_log_id", "text"}, {"system_account_id", "text"}, {"visibility_reason", "text"}, {"detail_level", "text"}, {"created_at", "timestamp with time zone"},
	},
	"operation_log_summary_search_terms": {
		{"operation_log_id", "text"}, {"term", "text"}, {"created_at", "timestamp with time zone"},
	},
}

var postgresPrimaryKeys = map[string]string{
	"operation_log_owner_leases":         "lease_key",
	"operation_logs":                     "id",
	"operation_log_targets":              "id",
	"operation_log_viewers":              "operation_log_id,system_account_id,visibility_reason,detail_level",
	"operation_log_summary_search_terms": "term,operation_log_id",
}

var postgresForeignKeyTables = []string{
	"operation_log_targets",
	"operation_log_viewers",
	"operation_log_summary_search_terms",
}

var postgresRequiredNotNull = map[string][]string{
	"operation_log_owner_leases": {"lease_key", "owner_id", "fence_token", "lease_until", "updated_at"},
	"operation_logs": {
		"id", "actor_system_account_id", "actor_role", "mode", "module", "action", "operation_key", "resource_type", "summary", "detail_level", "visibility_scope", "changes_json", "metadata_json", "created_at",
	},
	"operation_log_targets":              {"id", "operation_log_id", "target_type", "relation", "created_at"},
	"operation_log_viewers":              {"operation_log_id", "system_account_id", "visibility_reason", "detail_level", "created_at"},
	"operation_log_summary_search_terms": {"operation_log_id", "term", "created_at"},
}

var postgresRequiredIndexDefinitions = map[string]string{
	"idx_operation_logs_created":                          "CREATE INDEX idx_operation_logs_created ON juhe_dataset.operation_logs USING btree (created_at, id)",
	"idx_operation_logs_actor_created":                    "CREATE INDEX idx_operation_logs_actor_created ON juhe_dataset.operation_logs USING btree (actor_system_account_id, created_at, id)",
	"idx_operation_logs_scope_created":                    "CREATE INDEX idx_operation_logs_scope_created ON juhe_dataset.operation_logs USING btree (operation_scope_system_account_id, created_at, id)",
	"idx_operation_logs_module_action_created":            "CREATE INDEX idx_operation_logs_module_action_created ON juhe_dataset.operation_logs USING btree (module, action, created_at, id)",
	"idx_operation_logs_resource_created":                 "CREATE INDEX idx_operation_logs_resource_created ON juhe_dataset.operation_logs USING btree (resource_type, resource_id, created_at, id)",
	"idx_operation_logs_resource_id_created":              "CREATE INDEX idx_operation_logs_resource_id_created ON juhe_dataset.operation_logs USING btree (resource_id, created_at, id)",
	"idx_operation_logs_visibility_created":               "CREATE INDEX idx_operation_logs_visibility_created ON juhe_dataset.operation_logs USING btree (visibility_scope, created_at, id)",
	"idx_operation_logs_trace_created":                    "CREATE INDEX idx_operation_logs_trace_created ON juhe_dataset.operation_logs USING btree (trace_id, created_at, id)",
	"idx_operation_logs_trace_c_created":                  "CREATE INDEX idx_operation_logs_trace_c_created ON juhe_dataset.operation_logs USING btree (trace_id COLLATE \"C\", created_at, id)",
	"idx_operation_log_targets_log":                       "CREATE INDEX idx_operation_log_targets_log ON juhe_dataset.operation_log_targets USING btree (operation_log_id)",
	"idx_operation_log_targets_target":                    "CREATE INDEX idx_operation_log_targets_target ON juhe_dataset.operation_log_targets USING btree (target_type, target_id, created_at, id)",
	"idx_operation_log_viewers_account_created":           "CREATE INDEX idx_operation_log_viewers_account_created ON juhe_dataset.operation_log_viewers USING btree (system_account_id, created_at, operation_log_id)",
	"idx_operation_log_terms_log":                         "CREATE INDEX idx_operation_log_terms_log ON juhe_dataset.operation_log_summary_search_terms USING btree (operation_log_id)",
	"idx_operation_log_summary_search_terms_term_created": "CREATE INDEX idx_operation_log_summary_search_terms_term_created ON juhe_dataset.operation_log_summary_search_terms USING btree (term, created_at, operation_log_id)",
}

// validatePostgresSchema rejects legacy Node F4 tables instead of silently treating them as Go-compatible.
func validatePostgresSchema(ctx context.Context, catalog postgresSchemaCatalog) error {
	for table, columns := range postgresSchemaColumns {
		qualified := "juhe_dataset." + table
		for _, column := range columns {
			var actual string
			actual, err := catalog.String(ctx, `SELECT format_type(a.atttypid,a.atttypmod) FROM pg_attribute a WHERE a.attrelid=$1::regclass AND a.attname=$2 AND a.attnum>0 AND NOT a.attisdropped`, qualified, column.name)
			if err != nil {
				return fmt.Errorf("F4 PostgreSQL schema incompatible: %s.%s is missing: %w", table, column.name, err)
			}
			if actual != column.typeName {
				return fmt.Errorf("F4 PostgreSQL schema incompatible: %s.%s type=%s want=%s; migrate historical operation logs offline before cutover", table, column.name, actual, column.typeName)
			}
		}
		primaryKey, err := catalog.String(ctx, `SELECT string_agg(a.attname,',' ORDER BY key.ordinality) FROM pg_constraint c CROSS JOIN unnest(c.conkey) WITH ORDINALITY AS key(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.attnum WHERE c.conrelid=$1::regclass AND c.contype='p'`, qualified)
		if err != nil {
			return fmt.Errorf("F4 PostgreSQL schema incompatible: read primary key for %s: %w", table, err)
		}
		if primaryKey != postgresPrimaryKeys[table] {
			return fmt.Errorf("F4 PostgreSQL schema incompatible: %s primary key=%q want=%q; migrate historical operation logs offline before cutover", table, primaryKey, postgresPrimaryKeys[table])
		}
		for _, column := range postgresRequiredNotNull[table] {
			notNull, err := catalog.Bool(ctx, `SELECT a.attnotnull FROM pg_attribute a WHERE a.attrelid=$1::regclass AND a.attname=$2 AND a.attnum>0 AND NOT a.attisdropped`, qualified, column)
			if err != nil {
				return fmt.Errorf("F4 PostgreSQL schema incompatible: read nullability for %s.%s: %w", table, column, err)
			}
			if !notNull {
				return fmt.Errorf("F4 PostgreSQL schema incompatible: %s.%s must be NOT NULL; migrate historical operation logs offline before cutover", table, column)
			}
		}
	}
	for _, table := range postgresForeignKeyTables {
		present, err := catalog.Bool(ctx, `SELECT EXISTS(
			SELECT 1
			FROM pg_constraint c
			JOIN unnest(c.conkey) WITH ORDINALITY AS local_key(attnum,ordinality) ON true
			JOIN unnest(c.confkey) WITH ORDINALITY AS remote_key(attnum,ordinality) ON remote_key.ordinality=local_key.ordinality
			JOIN pg_attribute local_column ON local_column.attrelid=c.conrelid AND local_column.attnum=local_key.attnum
			JOIN pg_attribute remote_column ON remote_column.attrelid=c.confrelid AND remote_column.attnum=remote_key.attnum
			WHERE c.conrelid=$1::regclass
			  AND c.contype='f'
			  AND c.confrelid='juhe_dataset.operation_logs'::regclass
			  AND c.confdeltype='c'
			  AND cardinality(c.conkey)=1
			  AND cardinality(c.confkey)=1
			  AND local_column.attname='operation_log_id'
			  AND remote_column.attname='id'
		)`, "juhe_dataset."+table)
		if err != nil {
			return fmt.Errorf("F4 PostgreSQL schema incompatible: read foreign key for %s: %w", table, err)
		}
		if !present {
			return fmt.Errorf("F4 PostgreSQL schema incompatible: %s must have an exact single-column operation_log_id->operation_logs.id cascade foreign key; migrate historical operation logs offline before cutover", table)
		}
	}
	for index, expectedDefinition := range postgresRequiredIndexDefinitions {
		definition, err := catalog.String(ctx, `SELECT CASE WHEN i.indisvalid AND i.indisready THEN pg_get_indexdef(i.indexrelid) ELSE '' END FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='juhe_dataset' AND c.relname=$1`, index)
		if err != nil {
			return fmt.Errorf("F4 PostgreSQL schema incompatible: read index %s: %w", index, err)
		}
		if normalizeSQLDefinition(definition) != normalizeSQLDefinition(expectedDefinition) {
			return fmt.Errorf("F4 PostgreSQL schema incompatible: index %s definition is missing, invalid, or incompatible; migrate historical operation logs offline before cutover", index)
		}
	}
	return nil
}

func normalizeSQLDefinition(definition string) string {
	return strings.Join(strings.Fields(definition), " ")
}
func (s *sqlStore) AcquireOwnerLease(ctx context.Context, owner string, d time.Duration) (OwnerLease, bool, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	if err := s.EnsureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	if s.mode == ModePostgres {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return OwnerLease{}, false, err
		}
		defer tx.Rollback()
		q := `INSERT INTO juhe_dataset.operation_log_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at) VALUES ('f4-operation-log-persistence',?,1,clock_timestamp()+(? * INTERVAL '1 millisecond'),clock_timestamp()) ON CONFLICT(lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id,fence_token=juhe_dataset.operation_log_owner_leases.fence_token+1,lease_until=EXCLUDED.lease_until,updated_at=clock_timestamp() WHERE juhe_dataset.operation_log_owner_leases.lease_until<=clock_timestamp() RETURNING fence_token`
		var token int64
		err = tx.QueryRowContext(ctx, s.bind(q), owner, d.Milliseconds()).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerLease{}, false, tx.Commit()
		}
		if err != nil {
			return OwnerLease{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return OwnerLease{}, false, err
		}
		return OwnerLease{owner, token}, true, nil
	}
	now := time.Now()
	q := `INSERT INTO operation_log_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at) VALUES ('f4-operation-log-persistence',?,1,?,?) ON CONFLICT(lease_key) DO UPDATE SET owner_id=excluded.owner_id,fence_token=operation_log_owner_leases.fence_token+1,lease_until=excluded.lease_until,updated_at=excluded.updated_at WHERE operation_log_owner_leases.lease_until<=? RETURNING fence_token`
	var token int64
	err := s.db.QueryRowContext(ctx, q, owner, storageTime(now.Add(d)), storageTime(now), storageTime(now)).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	return OwnerLease{owner, token}, err == nil, err
}
func (s *sqlStore) RenewOwnerLease(ctx context.Context, l OwnerLease, d time.Duration) (bool, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	q := `UPDATE ` + s.table("operation_log_owner_leases") + ` SET lease_until=?,updated_at=? WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>?`
	now := time.Now()
	args := []any{storageTime(now.Add(d)), storageTime(now), l.OwnerID, l.FenceToken, storageTime(now)}
	if s.mode == ModePostgres {
		q = `UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()+(? * INTERVAL '1 millisecond'),updated_at=clock_timestamp() WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>clock_timestamp()`
		args = []any{d.Milliseconds(), l.OwnerID, l.FenceToken}
	}
	if s.mode == ModePostgres {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		r, err := tx.ExecContext(ctx, s.bind(q), args...)
		if err != nil {
			return false, err
		}
		n, err := r.RowsAffected()
		if err != nil || n != 1 {
			return n == 1, err
		}
		return true, tx.Commit()
	}
	r, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s *sqlStore) ReleaseOwnerLease(ctx context.Context, l OwnerLease) error {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	q := `UPDATE ` + s.table("operation_log_owner_leases") + ` SET lease_until=?,updated_at=? WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=?`
	args := []any{storageTime(time.Unix(0, 0)), storageTime(time.Now()), l.OwnerID, l.FenceToken}
	if s.mode == ModePostgres {
		q = `UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=to_timestamp(0),updated_at=clock_timestamp() WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=?`
		args = []any{l.OwnerID, l.FenceToken}
	}
	if s.mode == ModePostgres {
		tx, err := s.beginTx(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		r, err := tx.ExecContext(ctx, s.bind(q), args...)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrOwnerLeaseLost
		}
		return tx.Commit()
	}
	r, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}
func (s *sqlStore) verifyLease(ctx context.Context, tx *sql.Tx, l OwnerLease) error {
	q := `SELECT 1 FROM ` + s.table("operation_log_owner_leases") + ` WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>?`
	args := []any{l.OwnerID, l.FenceToken, storageTime(time.Now())}
	if s.mode == ModePostgres {
		q = `SELECT 1 FROM juhe_dataset.operation_log_owner_leases WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>clock_timestamp() FOR UPDATE`
		args = []any{l.OwnerID, l.FenceToken}
	}
	var one int
	if err := tx.QueryRowContext(ctx, s.bind(q), args...).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerLeaseLost
		}
		return fmt.Errorf("verify F4 operation-log owner lease: %w", err)
	}
	return nil
}
func (s *sqlStore) Persist(ctx context.Context, l OwnerLease, input Input) (bool, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	input, err := normalizeInput(input)
	if err != nil {
		return false, err
	}
	if err = s.EnsureSchema(ctx); err != nil {
		return false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return false, err
	}
	changes, _ := json.Marshal(input.Changes)
	q := `INSERT INTO ` + s.table("operation_logs") + ` (id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,operation_scope_system_account_id,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,client_ip,user_agent,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`
	if s.mode == ModePostgres {
		q = `INSERT INTO juhe_dataset.operation_logs (id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,operation_scope_system_account_id,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,client_ip,user_agent,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?::jsonb,?::jsonb,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`
	}
	r, err := tx.ExecContext(ctx, s.bind(q), input.ID, nilIf(input.TraceID), input.ActorSystemAccountID, nilIf(input.ActorUsername), nilIf(input.ActorDisplayName), input.ActorRole, nilIf(input.OperationScopeSystemAccountID), input.Mode, input.Module, input.Action, input.OperationKey, input.ResourceType, nilIf(input.ResourceID), nilIf(input.ResourceName), input.Summary, input.DetailLevel, input.VisibilityScope, string(changes), string(input.Metadata), nilIf(input.Method), nilIf(input.Path), input.StatusCode, nilIf(input.ClientIP), nilIf(input.UserAgent), input.CreatedAt)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return true, tx.Commit()
	}
	if s.mode == ModePostgres {
		if err = persistPostgresChildren(ctx, tx, input); err != nil {
			return false, err
		}
	} else if err = persistSQLiteChildren(ctx, tx, s, input); err != nil {
		return false, err
	}
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func persistSQLiteChildren(ctx context.Context, tx *sql.Tx, store *sqlStore, input Input) error {
	for i, target := range input.Targets {
		if _, err := tx.ExecContext(ctx, store.bind(`INSERT INTO `+store.table("operation_log_targets")+` (id,operation_log_id,target_type,target_id,target_name,target_owner_system_account_id,relation,created_at) VALUES (?,?,?,?,?,?,?,?)`), fmt.Sprintf("optgt_%s_%d", input.ID, i), input.ID, target.TargetType, nilIf(target.TargetID), nilIf(target.TargetName), nilIf(target.TargetOwnerSystemAccountID), target.Relation, input.CreatedAt); err != nil {
			return err
		}
	}
	for _, viewer := range input.Viewers {
		if _, err := tx.ExecContext(ctx, store.bind(`INSERT INTO `+store.table("operation_log_viewers")+` (operation_log_id,system_account_id,visibility_reason,detail_level,created_at) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`), input.ID, viewer.SystemAccountID, viewer.VisibilityReason, viewer.DetailLevel, input.CreatedAt); err != nil {
			return err
		}
	}
	for _, term := range searchTerms(input.Summary) {
		if _, err := tx.ExecContext(ctx, store.bind(`INSERT INTO `+store.table("operation_log_summary_search_terms")+` (operation_log_id,term,created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`), input.ID, term, input.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func persistPostgresChildren(ctx context.Context, tx *sql.Tx, input Input) error {
	if len(input.Targets) > 0 {
		ids, logIDs, types, targetIDs, names, owners, relations, created := make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets)), make([]string, 0, len(input.Targets))
		for index, target := range input.Targets {
			ids, logIDs, types = append(ids, fmt.Sprintf("optgt_%s_%d", input.ID, index)), append(logIDs, input.ID), append(types, target.TargetType)
			targetIDs, names, owners, relations, created = append(targetIDs, target.TargetID), append(names, target.TargetName), append(owners, target.TargetOwnerSystemAccountID), append(relations, target.Relation), append(created, input.CreatedAt)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_dataset.operation_log_targets (id,operation_log_id,target_type,target_id,target_name,target_owner_system_account_id,relation,created_at) SELECT id,operation_log_id,target_type,NULLIF(target_id,''),NULLIF(target_name,''),NULLIF(owner_id,''),relation,created_at::timestamptz FROM unnest($1::text[],$2::text[],$3::text[],$4::text[],$5::text[],$6::text[],$7::text[],$8::text[]) AS t(id,operation_log_id,target_type,target_id,target_name,owner_id,relation,created_at)`, ids, logIDs, types, targetIDs, names, owners, relations, created); err != nil {
			return err
		}
	}
	if len(input.Viewers) > 0 {
		logIDs, ids, reasons, levels, created := make([]string, 0, len(input.Viewers)), make([]string, 0, len(input.Viewers)), make([]string, 0, len(input.Viewers)), make([]string, 0, len(input.Viewers)), make([]string, 0, len(input.Viewers))
		for _, viewer := range input.Viewers {
			logIDs, ids, reasons, levels, created = append(logIDs, input.ID), append(ids, viewer.SystemAccountID), append(reasons, viewer.VisibilityReason), append(levels, viewer.DetailLevel), append(created, input.CreatedAt)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_dataset.operation_log_viewers (operation_log_id,system_account_id,visibility_reason,detail_level,created_at) SELECT operation_log_id,system_account_id,visibility_reason,detail_level,created_at::timestamptz FROM unnest($1::text[],$2::text[],$3::text[],$4::text[],$5::text[]) AS v(operation_log_id,system_account_id,visibility_reason,detail_level,created_at) ON CONFLICT DO NOTHING`, logIDs, ids, reasons, levels, created); err != nil {
			return err
		}
	}
	terms := searchTerms(input.Summary)
	if len(terms) > 0 {
		logIDs, created := make([]string, len(terms)), make([]string, len(terms))
		for index := range terms {
			logIDs[index], created[index] = input.ID, input.CreatedAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_dataset.operation_log_summary_search_terms (operation_log_id,term,created_at) SELECT operation_log_id,term,created_at::timestamptz FROM unnest($1::text[],$2::text[],$3::text[]) AS s(operation_log_id,term,created_at) ON CONFLICT DO NOTHING`, logIDs, terms, created); err != nil {
			return err
		}
	}
	return nil
}
func nilIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func searchTerms(value string) []string {
	value = normalizeSearchText(value)
	if value == "" {
		return nil
	}
	compact := strings.ReplaceAll(value, " ", "")
	parts := strings.Fields(value)
	set := map[string]bool{}
	add := func(term string) {
		if length := len([]rune(term)); length >= 1 && length <= 128 {
			set[term] = true
		}
	}
	add(value)
	add(compact)
	for _, part := range parts {
		add(part)
	}
	for _, candidate := range append([]string{value, compact}, parts...) {
		chars := []rune(candidate)
		if len(chars) > 256 {
			chars = chars[:256]
		}
		for length := 1; length <= 128 && length <= len(chars); length++ {
			for start := 0; start+length <= len(chars) && len(set) < 1500; start++ {
				add(string(chars[start : start+length]))
			}
			if len(set) >= 1500 {
				break
			}
		}
		if len(set) >= 1500 {
			break
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func normalizeSearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
	var b strings.Builder
	needsSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			needsSpace = false
		} else if b.Len() > 0 && !needsSpace {
			b.WriteByte(' ')
			needsSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *sqlStore) List(ctx context.Context, options ListOptions) (ListResult, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	page := options.Page
	if page < 1 {
		page = 1
	}
	size := options.PageSize
	if size < 1 {
		size = 20
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	maxPage := max(1, (maxListWindowRows-1)/size)
	if page > maxPage {
		page = maxPage
	}
	where := []string{}
	args := []any{}
	add := func(column, value string) {
		value = strings.TrimSpace(value)
		if value != "" && value != "all" {
			where = append(where, column+"=?")
			args = append(args, value)
		}
	}
	add("ol.module", options.Module)
	add("ol.action", options.Action)
	add("ol.resource_type", options.ResourceType)
	add("ol.resource_id", options.ResourceID)
	add("ol.actor_system_account_id", options.ActorSystemAccountID)
	add("ol.operation_scope_system_account_id", options.OperationScopeSystemAccountID)
	if traceID := strings.TrimSpace(options.TraceID); traceID != "" {
		traceColumn := "ol.trace_id"
		if s.mode == ModePostgres {
			traceColumn += ` COLLATE "C"`
		}
		where = append(where, traceColumn+">=? AND "+traceColumn+"<?")
		args = append(args, traceID, textPrefixUpperBound(traceID))
	}
	startAt, startOK := parseStorageTime(options.StartAt)
	endAt, endOK := parseStorageTime(options.EndAt)
	if startOK == nil && endOK == nil && startAt > endAt {
		startAt, endAt = endAt, startAt
	}
	if startOK == nil {
		where = append(where, "ol.created_at>=?")
		args = append(args, startAt)
	}
	if endOK == nil {
		where = append(where, "ol.created_at<=?")
		args = append(args, endAt)
	}
	if affected := strings.TrimSpace(options.AffectedSystemAccountID); affected != "" && affected != "all" {
		where = append(where, "(ol.visibility_scope='all_users' OR EXISTS (SELECT 1 FROM "+s.table("operation_log_viewers")+" av WHERE av.operation_log_id=ol.id AND av.system_account_id=?))")
		args = append(args, affected)
	}
	if options.SummaryKeyword != "" {
		term := normalizeSearchText(options.SummaryKeyword)
		if len([]rune(term)) > 128 {
			term = strings.ReplaceAll(term, " ", "")
		}
		if len([]rune(term)) <= 128 && term != "" {
			where = append(where, "EXISTS (SELECT 1 FROM "+s.table("operation_log_summary_search_terms")+" st WHERE st.operation_log_id=ol.id AND st.term=?)")
			args = append(args, term)
		} else {
			where = append(where, "1=0")
		}
	}
	queryItems := func(from string, queryWhere []string, queryArgs []any, limit, offset int) ([]ListItem, error) {
		queryClause := ""
		if len(queryWhere) > 0 {
			queryClause = " WHERE " + strings.Join(queryWhere, " AND ")
		}
		q := `SELECT ol.id,COALESCE(ol.trace_id,''),ol.actor_system_account_id,COALESCE(ol.actor_display_name,''),COALESCE(ol.operation_scope_system_account_id,''),ol.module,ol.action,ol.summary,ol.created_at FROM ` + from + queryClause + ` ORDER BY ol.created_at DESC,ol.id DESC LIMIT ? OFFSET ?`
		boundArgs := append(append([]any{}, queryArgs...), limit, offset)
		rows, err := s.db.QueryContext(ctx, s.bind(q), boundArgs...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []ListItem{}
		for rows.Next() {
			var item ListItem
			var createdAt storageTimestamp
			if scanErr := rows.Scan(&item.ID, &item.TraceID, &item.ActorSystemAccountID, &item.ActorDisplayName, &item.OperationScopeSystemAccountID, &item.Module, &item.Action, &item.Summary, &createdAt); scanErr != nil {
				return nil, scanErr
			}
			item.CreatedAt = string(createdAt)
			items = append(items, item)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return nil, rowsErr
		}
		return items, nil
	}
	start := (page - 1) * size
	items := []ListItem{}
	var err error
	if options.ViewerID == "" {
		items, err = queryItems(s.table("operation_logs")+" ol", where, args, size+1, start)
		if err != nil {
			return ListResult{}, err
		}
	} else {
		// Keep personal history index-driven: targeted viewer rows and all-user summaries
		// are independent bounded streams, merged only after both SQL reads complete.
		baseWhere := append([]string{}, where...)
		baseArgs := append([]any{}, args...)
		targetedWhere := append(baseWhere, "ol.visibility_scope='targeted'", "visible.system_account_id=?", "NOT EXISTS (SELECT 1 FROM "+s.table("operation_log_viewers")+" previous WHERE previous.operation_log_id=visible.operation_log_id AND previous.system_account_id=visible.system_account_id AND (previous.visibility_reason < visible.visibility_reason OR (previous.visibility_reason=visible.visibility_reason AND previous.detail_level < visible.detail_level)))")
		targetedArgs := append(baseArgs, options.ViewerID)
		allUsersWhere := append(baseWhere, "ol.visibility_scope='all_users'")
		targetedFrom := s.table("operation_log_viewers") + " visible JOIN " + s.table("operation_logs") + " ol ON ol.id=visible.operation_log_id"
		targeted, err := queryItems(targetedFrom, targetedWhere, targetedArgs, maxListWindowRows, 0)
		if err != nil {
			return ListResult{}, err
		}
		allUsers, err := queryItems(s.table("operation_logs")+" ol", allUsersWhere, baseArgs, maxListWindowRows, 0)
		if err != nil {
			return ListResult{}, err
		}
		items = append(targeted, allUsers...)
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt == items[j].CreatedAt {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt > items[j].CreatedAt
		})
		if len(items) > start+size+1 {
			items = items[:start+size+1]
		}
		if start >= len(items) {
			items = []ListItem{}
		} else {
			items = items[start:]
		}
	}
	names, err := s.accountNames(ctx, listAccountIDs(items))
	if err != nil {
		return ListResult{}, err
	}
	for index := range items {
		items[index].ActorSystemAccountName = names[items[index].ActorSystemAccountID]
		items[index].OperationScopeSystemAccountName = names[items[index].OperationScopeSystemAccountID]
	}
	more := len(items) > size
	if more {
		items = items[:size]
	}
	total := (page-1)*size + len(items)
	if more {
		total++
	}
	return ListResult{Items: items, Total: total, HasMore: more, Page: page, PageSize: size}, nil
}

func textPrefixUpperBound(value string) string {
	bytes := []byte(value)
	for index := len(bytes) - 1; index >= 0; index-- {
		if bytes[index] < 0xff {
			return string(append(bytes[:index], bytes[index]+1))
		}
	}
	return value + "\x00"
}

func (s *sqlStore) Detail(ctx context.Context, id, viewerID string) (DetailSupplement, bool, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	where := "ol.id=?"
	args := []any{id}
	if viewerID != "" {
		where += ` AND (ol.visibility_scope='all_users' OR (ol.visibility_scope='targeted' AND EXISTS (SELECT 1 FROM ` + s.table("operation_log_viewers") + ` auth WHERE auth.operation_log_id=ol.id AND auth.system_account_id=?)))`
		args = append(args, viewerID)
	}
	q := `SELECT ol.operation_key,ol.resource_type,COALESCE(ol.resource_id,''),COALESCE(ol.resource_name,''),ol.visibility_scope,ol.detail_level FROM ` + s.table("operation_logs") + ` ol WHERE ` + where + ` LIMIT 1`
	var detail DetailSupplement
	var logLevel string
	err := s.db.QueryRowContext(ctx, s.bind(q), args...).Scan(&detail.OperationKey, &detail.ResourceType, &detail.ResourceID, &detail.ResourceName, &detail.VisibilityScope, &logLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return DetailSupplement{}, false, nil
	}
	if err != nil {
		return DetailSupplement{}, false, err
	}
	full := viewerID == ""
	if viewerID != "" {
		err = s.db.QueryRowContext(ctx, s.bind(`SELECT EXISTS(SELECT 1 FROM `+s.table("operation_log_viewers")+` WHERE operation_log_id=? AND system_account_id=? AND detail_level='full')`), id, viewerID).Scan(&full)
		if err != nil {
			return DetailSupplement{}, false, err
		}
		if !full || logLevel != "full" {
			detail.Changes = []Change{}
			detail.Targets = []DetailTarget{}
			detail.Viewers = []DetailViewer{}
			return detail, true, nil
		}
	}
	var changes string
	q = `SELECT changes_json,COALESCE(method,''),COALESCE(path,''),COALESCE(client_ip,'') FROM ` + s.table("operation_logs") + ` WHERE id=? LIMIT 1`
	if err = s.db.QueryRowContext(ctx, s.bind(q), id).Scan(&changes, &detail.Method, &detail.Path, &detail.ClientIP); err != nil {
		return DetailSupplement{}, false, err
	}
	_ = json.Unmarshal([]byte(changes), &detail.Changes)
	if detail.Changes == nil {
		detail.Changes = []Change{}
	}
	if viewerID != "" {
		detail.ClientIP = ""
	}
	targetRows, err := s.db.QueryContext(ctx, s.bind(`SELECT id,target_type,COALESCE(target_id,''),COALESCE(target_name,''),COALESCE(target_owner_system_account_id,''),relation FROM `+s.table("operation_log_targets")+` WHERE operation_log_id=? ORDER BY created_at,id`), id)
	if err != nil {
		return DetailSupplement{}, false, err
	}
	defer targetRows.Close()
	targetOwnerIDs := make([]string, 0)
	for targetRows.Next() {
		var t DetailTarget
		var ownerID string
		if err = targetRows.Scan(&t.ID, &t.TargetType, &t.TargetID, &t.TargetName, &ownerID, &t.Relation); err != nil {
			return DetailSupplement{}, false, err
		}
		targetOwnerIDs = append(targetOwnerIDs, ownerID)
		detail.Targets = append(detail.Targets, t)
	}
	if err = targetRows.Err(); err != nil {
		return DetailSupplement{}, false, err
	}
	if viewerID == "" {
		viewerRows, err := s.db.QueryContext(ctx, s.bind(`SELECT system_account_id,visibility_reason,detail_level FROM `+s.table("operation_log_viewers")+` WHERE operation_log_id=? ORDER BY created_at,system_account_id`), id)
		if err != nil {
			return DetailSupplement{}, false, err
		}
		defer viewerRows.Close()
		for viewerRows.Next() {
			var v DetailViewer
			if err = viewerRows.Scan(&v.SystemAccountID, &v.VisibilityReason, &v.DetailLevel); err != nil {
				return DetailSupplement{}, false, err
			}
			detail.Viewers = append(detail.Viewers, v)
		}
		if err = viewerRows.Err(); err != nil {
			return DetailSupplement{}, false, err
		}
	}
	ids := make([]string, 0, len(targetOwnerIDs)+len(detail.Viewers))
	ids = append(ids, targetOwnerIDs...)
	for _, viewer := range detail.Viewers {
		ids = append(ids, viewer.SystemAccountID)
	}
	names, err := s.accountNames(ctx, ids)
	if err != nil {
		return DetailSupplement{}, false, err
	}
	for index := range detail.Targets {
		detail.Targets[index].TargetOwnerSystemAccountName = names[targetOwnerIDs[index]]
	}
	for index := range detail.Viewers {
		detail.Viewers[index].SystemAccountName = names[detail.Viewers[index].SystemAccountID]
	}
	if detail.Targets == nil {
		detail.Targets = []DetailTarget{}
	}
	if detail.Viewers == nil {
		detail.Viewers = []DetailViewer{}
	}
	return detail, true, nil
}

func listAccountIDs(items []ListItem) []string {
	ids := make([]string, 0, len(items)*2)
	for _, item := range items {
		ids = append(ids, item.ActorSystemAccountID, item.OperationScopeSystemAccountID)
	}
	return ids
}

func (s *sqlStore) accountNames(ctx context.Context, ids []string) (map[string]string, error) {
	result := map[string]string{}
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || result[id] != "" {
			continue
		}
		result[id] = "\x00"
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return result, nil
	}
	var (
		rows *sql.Rows
		err  error
	)
	if s.mode == ModeSQLite {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(unique)), ",")
		args := make([]any, len(unique))
		for index := range unique {
			args[index] = unique[index]
		}
		rows, err = s.businessDB.QueryContext(ctx, "SELECT id,COALESCE(NULLIF(display_name,''),NULLIF(username,''),id) FROM system_accounts WHERE id IN ("+placeholders+")", args...)
	} else {
		rows, err = s.db.QueryContext(ctx, "SELECT id,COALESCE(NULLIF(display_name,''),NULLIF(username,''),id) FROM juhe_business.system_accounts WHERE id=ANY($1::text[])", unique)
	}
	if err != nil {
		return nil, fmt.Errorf("read F4 system account names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan F4 system account name: %w", err)
		}
		result[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate F4 system account names: %w", err)
	}
	for _, id := range unique {
		if result[id] == "\x00" {
			delete(result, id)
		}
	}
	return result, nil
}

func (s *sqlStore) CleanupRetention(ctx context.Context, l OwnerLease, cutoff time.Time, limit int) (int64, error) {
	ctx, cancel := storeContext(ctx)
	defer cancel()
	if limit < 1 {
		limit = 1
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return 0, err
	}
	q := `SELECT id FROM ` + s.table("operation_logs") + ` WHERE created_at<? ORDER BY created_at,id LIMIT ?`
	rows, err := tx.QueryContext(ctx, s.bind(q), storageTime(cutoff), limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) > 0 {
		if s.mode == ModePostgres {
			if _, err = tx.ExecContext(ctx, `DELETE FROM juhe_dataset.operation_logs WHERE id=ANY($1::text[])`, ids); err != nil {
				return 0, err
			}
		} else {
			for _, id := range ids {
				if _, err = tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("operation_logs")+` WHERE id=?`), id); err != nil {
					return 0, err
				}
			}
		}
	}
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}
