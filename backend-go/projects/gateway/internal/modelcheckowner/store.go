package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
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
	db             *sql.DB
	mode           string
	schema         string
	HealthStatHour HealthStatHourFunc
}

func (s *Store) schedulerTaskTable() string {
	if s.mode == "postgres" {
		return s.schema + ".model_check_scheduler_tasks"
	}
	return "model_check_scheduler_tasks"
}

var requiredTables = []string{
	"model_check_input_versions", "model_check_inputs", "model_check_execution_claims", "model_check_outcomes",
	"model_check_runs", "model_check_items", "model_check_observations",
	"account_quality_health_hourly",
	"model_check_scheduler_tasks", "model_token_intercept_baseline_versions",
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
		"id", "system_account_id", "actor_system_account_id", "provider_code", "target_type", "target_id", "account_id",
		"model", "profile", "trigger_kind", "schedule_id", "status", "level", "score", "max_score", "message", "request_summary_json", "result_summary_json",
		"policy_snapshot_json", "quality_decision_json", "probe_set_version", "started_at", "trace_id", "quality_health_sync_status", "created_at", "updated_at", "finished_at",
	},
	"model_check_items": {
		// Runtime.GetRun reads the complete durable item projection. Keep the
		// detail columns in the readiness contract so an older projection cannot
		// open successfully and fail only when a detail request arrives.
		"id", "run_id", "item_key", "item_type", "status", "score", "max_score", "duration_ms", "trace_id", "evidence_summary_json", "error_code", "error_message", "created_at", "updated_at",
	},
	"model_check_observations": {
		"id", "run_id", "system_account_id", "account_id", "provider_code", "requested_model",
		"mapped_upstream_model", "probe_family", "observation_status", "identity_status", "mapping_status",
		"protocol_status", "evidence_coverage", "created_at",
	},
	"account_quality_health_hourly": {
		"account_id", "system_account_id", "provider_code", "stat_hour", "observed_at", "model_check_run_id",
		"model", "profile", "score", "threshold", "level", "error_code", "error_message", "updated_at",
	},
	"model_check_scheduler_tasks": {
		"id", "kind", "due_at", "claim_owner", "claim_until", "fence_token", "state", "last_error", "completed_at", "payload", "updated_at",
	},
	// The Gateway-owned baseline table deliberately keeps only the durable
	// calibration and activation facts needed by J3b. Source aggregation is an
	// offline/worker concern; this table is never populated from a Node bridge.
	"model_token_intercept_baseline_versions": {
		"cohort_key_hmac", "requested_model", "tokenizer_version", "probe_set_version", "baseline_version",
		"version_status", "evidence_status", "independent_source_count", "q90_intercept",
		"strong_threshold_intercept", "strong_gate_enabled", "calibration_note", "updated_at",
	},
}

// ErrTokenInterceptBaselineUnavailable means the dedicated J3b baseline
// storage is not readable (for example, the table or database is unavailable).
// HTTP management maps it to 503 rather than pretending activation succeeded.
var ErrTokenInterceptBaselineUnavailable = errors.New("J3b token intercept baseline storage unavailable")

// ErrTokenInterceptBaselineConflict is returned for stale, missing, or
// ineligible calibration versions. Activation is intentionally a CAS and the
// caller must refresh the candidate before retrying.
var ErrTokenInterceptBaselineConflict = errors.New("J3b token intercept baseline activation conflict")

// TokenInterceptBaselineActivation is the complete, versioned input accepted
// by the Gateway management endpoint. It mirrors the Node contract without
// exposing database handles or cross-process adapters.
type TokenInterceptBaselineActivation struct {
	CohortKeyHMAC            string  `json:"cohortKeyHmac"`
	RequestedModel           string  `json:"requestedModel"`
	TokenizerVersion         string  `json:"tokenizerVersion"`
	ProbeSetVersion          string  `json:"probeSetVersion"`
	BaselineVersion          int     `json:"baselineVersion"`
	StrongThresholdIntercept float64 `json:"strongThresholdIntercept"`
	CalibrationNote          string  `json:"calibrationNote"`
}

// ActivateTokenInterceptBaseline performs the only mutable operation exposed
// for fixed-intercept calibration. The candidate is locked and changed from
// calibration_pending to active in one transaction; an existing active
// version is retired in the same transaction. This keeps readers from seeing
// two active versions or a partially activated candidate.
func (s *Store) ActivateTokenInterceptBaseline(ctx context.Context, input TokenInterceptBaselineActivation) error {
	if s == nil || s.db == nil {
		return ErrTokenInterceptBaselineUnavailable
	}
	input = normalizeTokenInterceptBaselineActivation(input)
	if err := validateTokenInterceptBaselineActivation(input); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin activation transaction: %v", ErrTokenInterceptBaselineUnavailable, err)
	}
	rollback := func(e error) error {
		_ = tx.Rollback()
		return e
	}
	table := s.tokenInterceptBaselineTable()
	query := `SELECT version_status,evidence_status,independent_source_count,q90_intercept
		FROM ` + table + ` WHERE cohort_key_hmac=? AND requested_model=? AND tokenizer_version=?
		AND probe_set_version=? AND baseline_version=? LIMIT 1`
	if s.mode == "postgres" {
		query += " FOR UPDATE"
	}
	var status, evidence string
	var independent int
	var q90 sql.NullFloat64
	err = tx.QueryRowContext(ctx, s.bind(query), input.CohortKeyHMAC, input.RequestedModel, input.TokenizerVersion, input.ProbeSetVersion, input.BaselineVersion).Scan(&status, &evidence, &independent, &q90)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(fmt.Errorf("%w: candidate version does not exist", ErrTokenInterceptBaselineConflict))
	}
	if err != nil {
		return rollback(fmt.Errorf("%w: read candidate version: %v", ErrTokenInterceptBaselineUnavailable, err))
	}
	if status != "calibration_pending" || evidence != "stable" || independent < 10 || !q90.Valid || input.StrongThresholdIntercept < q90.Float64 {
		return rollback(fmt.Errorf("%w: candidate is not eligible for activation", ErrTokenInterceptBaselineConflict))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+table+` SET version_status='retired',strong_gate_enabled=0,updated_at=? WHERE cohort_key_hmac=? AND requested_model=? AND tokenizer_version=? AND probe_set_version=? AND version_status='active'`), now, input.CohortKeyHMAC, input.RequestedModel, input.TokenizerVersion, input.ProbeSetVersion); err != nil {
		return rollback(fmt.Errorf("%w: retire active version: %v", ErrTokenInterceptBaselineUnavailable, err))
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+table+` SET version_status='active',strong_threshold_intercept=?,strong_gate_enabled=1,calibration_note=?,updated_at=? WHERE cohort_key_hmac=? AND requested_model=? AND tokenizer_version=? AND probe_set_version=? AND baseline_version=? AND version_status='calibration_pending'`), input.StrongThresholdIntercept, input.CalibrationNote, now, input.CohortKeyHMAC, input.RequestedModel, input.TokenizerVersion, input.ProbeSetVersion, input.BaselineVersion)
	if err != nil {
		return rollback(fmt.Errorf("%w: activate candidate version: %v", ErrTokenInterceptBaselineUnavailable, err))
	}
	if changed, err := result.RowsAffected(); err != nil {
		return rollback(fmt.Errorf("%w: read activation result: %v", ErrTokenInterceptBaselineUnavailable, err))
	} else if changed != 1 {
		return rollback(fmt.Errorf("%w: candidate changed before activation", ErrTokenInterceptBaselineConflict))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit activation transaction: %v", ErrTokenInterceptBaselineUnavailable, err)
	}
	return nil
}

func normalizeTokenInterceptBaselineActivation(input TokenInterceptBaselineActivation) TokenInterceptBaselineActivation {
	input.CohortKeyHMAC = strings.TrimSpace(input.CohortKeyHMAC)
	input.RequestedModel = strings.TrimSpace(input.RequestedModel)
	input.TokenizerVersion = strings.TrimSpace(input.TokenizerVersion)
	input.ProbeSetVersion = strings.TrimSpace(input.ProbeSetVersion)
	input.CalibrationNote = strings.TrimSpace(input.CalibrationNote)
	return input
}

func (s *Store) tokenInterceptBaselineTable() string {
	if s.mode == "postgres" {
		return s.schema + ".model_token_intercept_baseline_versions"
	}
	return "model_token_intercept_baseline_versions"
}

func validateTokenInterceptBaselineActivation(input TokenInterceptBaselineActivation) error {
	if !strings.HasPrefix(strings.ToLower(input.CohortKeyHMAC), "hmac-sha256-v1:") || len(input.CohortKeyHMAC) != len("hmac-sha256-v1:")+64 {
		return errors.New("cohort key 格式无效")
	}
	for _, char := range input.CohortKeyHMAC[len("hmac-sha256-v1:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return errors.New("cohort key 格式无效")
		}
	}
	if strings.TrimSpace(input.RequestedModel) == "" || len(input.RequestedModel) > 200 || strings.TrimSpace(input.TokenizerVersion) == "" || len(input.TokenizerVersion) > 200 || strings.TrimSpace(input.ProbeSetVersion) == "" || len(input.ProbeSetVersion) > 200 {
		return errors.New("固定截距基线作用域无效")
	}
	if input.BaselineVersion <= 0 || !isFiniteNonNegative(input.StrongThresholdIntercept) {
		return errors.New("固定截距基线版本或阈值无效")
	}
	if note := strings.TrimSpace(input.CalibrationNote); note == "" || len([]rune(note)) > 500 {
		return errors.New("固定截距校准记录必须为 1 到 500 个字符")
	}
	return nil
}

func isFiniteNonNegative(value float64) bool {
	return value >= 0 && value < 1.7976931348623157e+308
}

func OpenStore(cfg Config) (*Store, error) {
	if !cfg.Enabled {
		return nil, errors.New("J3b owner config is disabled")
	}
	if !cfg.BusinessHandoffConfirmed || !cfg.NodeWriterStopped || !cfg.SchemaReady || !cfg.HealthBoundaryReady || !cfg.RuntimeReady {
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
	if strings.TrimSpace(fact.AccountID) == "" || strings.TrimSpace(fact.SystemAccountID) == "" || strings.TrimSpace(fact.ProviderCode) == "" || strings.TrimSpace(fact.Model) == "" || strings.TrimSpace(fact.Profile) == "" || strings.TrimSpace(fact.Level) == "" || strings.TrimSpace(fact.StatHour) == "" || strings.TrimSpace(fact.RunID) == "" || fact.ObservedAt.IsZero() || fact.Threshold < 40 || fact.Threshold > 100 || fact.Score < 0 || fact.Score > 100 {
		return false, errors.New("health fact identity is incomplete")
	}
	if !validHealthStatHour(fact.StatHour) {
		return false, errors.New("health fact stat hour is invalid")
	}
	if fact.ErrorMessage != "" && len([]rune(fact.ErrorMessage)) > 1000 {
		fact.ErrorMessage = string([]rune(fact.ErrorMessage)[:1000])
	}
	observed := fact.ObservedAt.UTC().Format(time.RFC3339Nano)
	table := s.healthTable()
	query := fmt.Sprintf(`INSERT INTO %s AS target (account_id,system_account_id,provider_code,stat_hour,observed_at,model_check_run_id,model,profile,score,threshold,level,error_code,error_message,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,stat_hour) DO UPDATE SET system_account_id=excluded.system_account_id,provider_code=excluded.provider_code,observed_at=excluded.observed_at,model_check_run_id=excluded.model_check_run_id,model=excluded.model,profile=excluded.profile,score=excluded.score,threshold=excluded.threshold,level=excluded.level,error_code=excluded.error_code,error_message=excluded.error_message,updated_at=excluded.updated_at WHERE excluded.observed_at > target.observed_at OR (excluded.observed_at = target.observed_at AND excluded.model_check_run_id > target.model_check_run_id)`, table)
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

// validHealthStatHour accepts both the Gateway's canonical UTC RFC3339 hour
// and the legacy Business stats bucket key (YYYY-MM-DDTHH). The latter has no
// timezone by design and must remain readable during the owner handoff.
func validHealthStatHour(value string) bool {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.Equal(parsed.UTC().Truncate(time.Hour))
	}
	_, err := time.Parse("2006-01-02T15", value)
	return err == nil
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

// MarkHealthSync records the health publication outcome without changing the
// terminal run status. It is fenced by the run ID and is idempotent for the
// same state.
func (s *Store) MarkHealthSync(ctx context.Context, runID, state string) error {
	if s == nil || s.db == nil || strings.TrimSpace(runID) == "" {
		return errors.New("J3b health sync identity is incomplete")
	}
	if state != "applied" && state != "failed" {
		return errors.New("J3b health sync state is invalid")
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_runs")+` SET quality_health_sync_status=?,updated_at=? WHERE id=?`), state, time.Now().UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return fmt.Errorf("mark J3b health sync %s: %w", state, err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("J3b run not found for health sync")
	}
	return nil
}

type HealthSyncRetry struct {
	RunID, AccountID, SystemAccountID, ProviderCode, Model, Profile, Level, StatHour, ScheduleID string
	PolicyRevision, AccountConfigRevision, PenaltyAction                                         string
	Score, Threshold, RecoveryIntervalMinutes                                                    int
	ObservedAt                                                                                   time.Time
	EvidenceFormed, TrustFormed                                                                  bool
	EnforcementAllowed                                                                           bool
}

// ListHealthSyncRetries re-discovers Node-equivalent failed health
// publications from durable completed run facts. Invalid rows intentionally
// remain in their durable failed state, but are skipped so one malformed row
// cannot prevent a later valid retry from being scheduled.
func (s *Store) ListHealthSyncRetries(ctx context.Context, limit int) ([]HealthSyncRetry, error) {
	if s == nil || s.db == nil || limit <= 0 || limit > 10000 {
		return nil, errors.New("J3b health retry scan input is invalid")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id,account_id,system_account_id,provider_code,model,profile,level,score,schedule_id,policy_snapshot_json,quality_decision_json,request_summary_json,finished_at FROM `+s.table("model_check_runs")+` WHERE status='completed' AND quality_health_sync_status='failed' AND account_id IS NOT NULL AND finished_at IS NOT NULL ORDER BY updated_at ASC,id ASC`))
	if err != nil {
		return nil, fmt.Errorf("list J3b health sync retries: %w", err)
	}
	defer rows.Close()
	result := make([]HealthSyncRetry, 0)
	for rows.Next() {
		var retry HealthSyncRetry
		var policy, decision, requestSummary, finished string
		var schedule sql.NullString
		if err := rows.Scan(&retry.RunID, &retry.AccountID, &retry.SystemAccountID, &retry.ProviderCode, &retry.Model, &retry.Profile, &retry.Level, &retry.Score, &schedule, &policy, &decision, &requestSummary, &finished); err != nil {
			return nil, fmt.Errorf("scan J3b health sync retry: %w", err)
		}
		if retry.AccountID == "" || retry.SystemAccountID == "" || retry.ProviderCode == "" || retry.Model == "" || retry.Profile == "" {
			continue
		}
		var policyFields map[string]any
		if err := json.Unmarshal([]byte(policy), &policyFields); err != nil {
			continue
		}
		threshold, ok := policyFields["threshold"].(float64)
		if !ok || threshold < 40 || threshold > 100 {
			continue
		}
		var decisionFields struct {
			EvidenceFormed     bool  `json:"evidenceFormed"`
			TrustFormed        bool  `json:"trustFormed"`
			EnforcementAllowed *bool `json:"enforcementAllowed"`
		}
		if err := json.Unmarshal([]byte(decision), &decisionFields); err != nil || !decisionFields.EvidenceFormed || !decisionFields.TrustFormed {
			continue
		}
		retry.EvidenceFormed, retry.TrustFormed = true, true
		retry.Threshold = int(threshold)
		var policySnapshot struct {
			Revision                  string `json:"revision"`
			Action                    string `json:"action"`
			RecoveryIntervalMinutes   int    `json:"recoveryIntervalMinutes"`
			ManualEnforcementEligible *bool  `json:"manualEnforcementEligible"`
		}
		if err := json.Unmarshal([]byte(policy), &policySnapshot); err != nil || strings.TrimSpace(policySnapshot.Revision) == "" {
			continue
		}
		retry.PolicyRevision = policySnapshot.Revision
		retry.PenaltyAction = policySnapshot.Action
		if retry.PenaltyAction == "" || policySnapshot.RecoveryIntervalMinutes < 10 || policySnapshot.RecoveryIntervalMinutes > 10080 {
			continue
		}
		retry.RecoveryIntervalMinutes = policySnapshot.RecoveryIntervalMinutes
		// The finished decision records the exact gate that applied to this
		// run. For pre-field runs, a policy snapshot is the only available
		// evidence. Missing both must fail closed for enforcement while keeping
		// the health fact retryable.
		if decisionFields.EnforcementAllowed != nil {
			retry.EnforcementAllowed = *decisionFields.EnforcementAllowed
		} else if policySnapshot.ManualEnforcementEligible != nil && retry.Level != "unavailable" {
			retry.EnforcementAllowed = *policySnapshot.ManualEnforcementEligible
		}
		if schedule.Valid {
			retry.ScheduleID = schedule.String
		}
		var requestSnapshot struct {
			ConfigRevision string `json:"configRevision"`
		}
		if err := json.Unmarshal([]byte(requestSummary), &requestSnapshot); err != nil || strings.TrimSpace(requestSnapshot.ConfigRevision) == "" {
			continue
		}
		retry.AccountConfigRevision = requestSnapshot.ConfigRevision
		retry.ObservedAt, err = time.Parse(time.RFC3339Nano, finished)
		if err != nil {
			continue
		}
		retry.StatHour, err = s.formatHealthStatHour(retry.ObservedAt)
		if err != nil {
			continue
		}
		if retry.Score >= retry.Threshold && retry.Level != "unavailable" {
			continue
		}
		result = append(result, retry)
		if len(result) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b health sync retries: %w", err)
	}
	return result, nil
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
