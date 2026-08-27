package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store is a J3b-owned connection. It never creates or mutates schema; schema
// migration and data backfill belong to the offline maintenance command.
type Store struct {
	db     *sql.DB
	mode   string
	schema string
}

var requiredTables = []string{
	"model_check_input_versions", "model_check_inputs", "model_check_execution_claims", "model_check_outcomes",
	"model_check_runs", "model_check_items", "model_check_observations",
	"account_quality_health_hourly",
}

// requiredColumns is intentionally a small, stable contract rather than a
// copy of every historical column. These columns are the identity, fence,
// replay and health-ordering fields that make a J3b store safe to open. A
// migration that omits any of them must fail closed before a writer starts.
var requiredColumns = map[string][]string{
	"model_check_input_versions": {
		"identity_key", "next_version", "updated_at",
	},
	"model_check_inputs": {
		"input_id", "identity_key", "input_version", "input_digest", "target_id",
		"config_revision", "policy_revision", "trigger", "issued_at", "expires_at", "payload",
	},
	"model_check_execution_claims": {
		"input_id", "claim_token", "outcome_id", "owner_id", "fence_token", "claim_until", "updated_at",
	},
	"model_check_outcomes": {
		"outcome_id", "input_id", "input_digest", "fence_token", "observed_at", "stored_at", "payload", "payload_digest", "committed",
	},
	"model_check_runs": {
		"id", "system_account_id", "actor_system_account_id", "provider_code", "target_type", "target_id",
		"model", "profile", "trigger_kind", "status", "request_summary_json", "result_summary_json",
		"policy_snapshot_json", "quality_decision_json", "created_at", "updated_at",
	},
	"model_check_items": {
		"id", "run_id", "item_key", "item_type", "status", "evidence_summary_json", "created_at", "updated_at",
	},
	"model_check_observations": {
		"id", "run_id", "system_account_id", "account_id", "provider_code", "requested_model",
		"mapped_upstream_model", "probe_family", "observation_status", "identity_status", "mapping_status",
		"protocol_status", "evidence_coverage", "created_at",
	},
	"account_quality_health_hourly": {
		"account_id", "system_account_id", "provider_code", "stat_hour", "observed_at", "model_check_run_id",
		"model", "profile", "score", "threshold", "level", "updated_at",
	},
}

func OpenStore(cfg Config) (*Store, error) {
	if !cfg.Enabled {
		return nil, errors.New("J3b owner config is disabled")
	}
	if !cfg.BusinessHandoffConfirmed || !cfg.SchemaReady || !cfg.HealthBoundaryReady || !cfg.RuntimeReady {
		return nil, errors.New("J3b owner readiness gates are incomplete")
	}
	var driver, dsn, schema string
	switch cfg.StoreMode {
	case "sqlite":
		driver, dsn = "sqlite", "file:"+cfg.DatabasePath+"?mode=rw&_pragma=busy_timeout(5000)"
	case "postgres":
		driver, dsn, schema = "pgx", cfg.PostgresURL, "juhe_j3b"
	default:
		return nil, fmt.Errorf("unsupported J3b store mode %q", cfg.StoreMode)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open J3b store: %w", err)
	}
	if cfg.StoreMode == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	return &Store{db: db, mode: cfg.StoreMode, schema: schema}, nil
}

func (s *Store) CheckSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("J3b store is not open")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping J3b store: %w", err)
	}
	for _, table := range requiredTables {
		var found string
		var err error
		if s.mode == "postgres" {
			err = s.db.QueryRowContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2`, s.schema, table).Scan(&found)
		} else {
			err = s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
		}
		if err != nil {
			return fmt.Errorf("J3b schema missing table %q: %w", table, err)
		}
		if strings.TrimSpace(found) != table {
			return fmt.Errorf("J3b schema returned unexpected table %q", found)
		}
		if err := s.checkColumns(ctx, table, requiredColumns[table]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) checkColumns(ctx context.Context, table string, required []string) error {
	if len(required) == 0 {
		return nil
	}
	found := make(map[string]struct{}, len(required))
	if s.mode == "postgres" {
		rows, err := s.db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2`, s.schema, table)
		if err != nil {
			return fmt.Errorf("read J3b schema columns %q: %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("scan J3b schema columns %q: %w", table, err)
			}
			found[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate J3b schema columns %q: %w", table, err)
		}
	} else {
		rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return fmt.Errorf("read J3b SQLite schema columns %q: %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				return fmt.Errorf("scan J3b SQLite schema columns %q: %w", table, err)
			}
			found[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate J3b SQLite schema columns %q: %w", table, err)
		}
	}
	missing := make([]string, 0)
	for _, column := range required {
		if _, ok := found[column]; !ok {
			missing = append(missing, column)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("J3b schema missing columns %q: %s", table, strings.Join(missing, ", "))
	}
	return nil
}

// ApplyHealthFact performs the Node-compatible latest-wins projection. It is
// intentionally the only health writer exposed by this package; J3c has no
// write access to the J3b store.
func (s *Store) ApplyHealthFact(ctx context.Context, fact HealthFact) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("J3b store is not open")
	}
	if fact.AccountID == "" || fact.SystemAccountID == "" || fact.StatHour == "" || fact.RunID == "" || fact.ObservedAt.IsZero() {
		return false, errors.New("health fact identity is incomplete")
	}
	if fact.ErrorMessage != "" && len([]rune(fact.ErrorMessage)) > 1000 {
		fact.ErrorMessage = string([]rune(fact.ErrorMessage)[:1000])
	}
	observed := fact.ObservedAt.UTC().Format(time.RFC3339Nano)
	table := s.healthTable()
	query := fmt.Sprintf(`INSERT INTO %s (account_id,system_account_id,provider_code,stat_hour,observed_at,model_check_run_id,model,profile,score,threshold,level,error_code,error_message,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,stat_hour) DO UPDATE SET system_account_id=excluded.system_account_id,provider_code=excluded.provider_code,observed_at=excluded.observed_at,model_check_run_id=excluded.model_check_run_id,model=excluded.model,profile=excluded.profile,score=excluded.score,threshold=excluded.threshold,level=excluded.level,error_code=excluded.error_code,error_message=excluded.error_message,updated_at=excluded.updated_at WHERE excluded.observed_at > account_quality_health_hourly.observed_at OR (excluded.observed_at = account_quality_health_hourly.observed_at AND excluded.model_check_run_id > account_quality_health_hourly.model_check_run_id)`, table)
	result, err := s.db.ExecContext(ctx, s.bind(query), fact.AccountID, fact.SystemAccountID, fact.ProviderCode, fact.StatHour, observed, fact.RunID, fact.Model, fact.Profile, fact.Score, fact.Threshold, fact.Level, nullable(fact.ErrorCode), nullable(fact.ErrorMessage), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("upsert J3b health fact: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read J3b health upsert result: %w", err)
	}
	return changed > 0, nil
}

// ReadHealthFact is the read-only boundary exposed to J3c consumers. It
// accepts an explicit account and hour scope so callers cannot accidentally
// turn a broad stats scan into an implicit J3b dependency.
func (s *Store) ReadHealthFact(ctx context.Context, accountID, statHour string) (HealthFact, bool, error) {
	if s == nil || s.db == nil {
		return HealthFact{}, false, errors.New("J3b store is not open")
	}
	accountID = strings.TrimSpace(accountID)
	statHour = strings.TrimSpace(statHour)
	if accountID == "" || statHour == "" {
		return HealthFact{}, false, errors.New("health read scope is incomplete")
	}
	query := fmt.Sprintf(`SELECT account_id,system_account_id,stat_hour,model_check_run_id,provider_code,model,profile,observed_at,score,threshold,level,error_code,error_message FROM %s WHERE account_id=? AND stat_hour=?`, s.healthTable())
	var fact HealthFact
	var observed string
	var errorCode, errorMessage sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(query), accountID, statHour).Scan(
		&fact.AccountID, &fact.SystemAccountID, &fact.StatHour, &fact.RunID, &fact.ProviderCode,
		&fact.Model, &fact.Profile, &observed, &fact.Score, &fact.Threshold, &fact.Level,
		&errorCode, &errorMessage,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return HealthFact{}, false, nil
	}
	if err != nil {
		return HealthFact{}, false, fmt.Errorf("read J3b health fact: %w", err)
	}
	fact.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	if err != nil {
		return HealthFact{}, false, fmt.Errorf("parse J3b health observed_at: %w", err)
	}
	fact.ErrorCode, fact.ErrorMessage = errorCode.String, errorMessage.String
	return fact, true, nil
}

func (s *Store) healthTable() string {
	if s.mode == "postgres" {
		return s.schema + ".account_quality_health_hourly"
	}
	return "account_quality_health_hourly"
}

func (s *Store) bind(query string) string {
	if s.mode != "postgres" {
		return query
	}
	var b strings.Builder
	index := 0
	for _, char := range query {
		if char == '?' {
			index++
			fmt.Fprintf(&b, "$%d", index)
		} else {
			b.WriteRune(char)
		}
	}
	return b.String()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
