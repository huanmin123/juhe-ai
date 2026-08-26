// Package modelcheckstore owns the durable J3b run/item/observation facts.
// It is intentionally independent from the Node runtime and talks directly to
// the SQLite or PostgreSQL dataset store selected by the jobs process.
package modelcheckstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type StoreMode string

const (
	StoreSQLite   StoreMode = "sqlite"
	StorePostgres StoreMode = "postgres"
)

type RunStatus string
type ItemStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"

	ItemPassed  ItemStatus = "passed"
	ItemWarning ItemStatus = "warning"
	ItemFailed  ItemStatus = "failed"
	ItemSkipped ItemStatus = "skipped"
)

type Trigger string

const (
	TriggerManual          Trigger = "manual"
	TriggerScheduled       Trigger = "scheduled"
	TriggerQualityRecovery Trigger = "quality_recovery"
)

type RunInput struct {
	ID                         string
	SystemAccountID            string
	ActorSystemAccountID       string
	ProviderCode               string
	TargetType                 string
	TargetID                   string
	TargetName                 string
	TargetOwnerSystemAccountID string
	AccountID                  string
	GroupID                    string
	APIKeyID                   string
	Model                      string
	Profile                    string
	Trigger                    Trigger
	ScheduleID                 string
	TrustedComparisonEnabled   bool
	TrustedComparisonAvailable bool
	TraceID                    string
	ProbeSetVersion            string
	StartedAt                  time.Time
	RequestSummary             json.RawMessage
	PolicySnapshot             json.RawMessage
}

type ItemInput struct {
	ID              string
	RunID           string
	ItemKey         string
	ItemType        string
	Status          ItemStatus
	Score           int
	MaxScore        int
	DurationMS      *int64
	TraceID         string
	EvidenceSummary json.RawMessage
	ErrorCode       string
	ErrorMessage    string
}

type ObservationInput struct {
	ID                        string
	RunID                     string
	SystemAccountID           string
	AccountID                 string
	ProviderCode              string
	ProviderProtocolProfileID string
	EndpointFamily            string
	RequestedModel            string
	MappedUpstreamModel       string
	ObservedModel             string
	MappingApplied            bool
	UpstreamBucketHMAC        string
	CohortKeyHMAC             string
	PopulationKeyHMAC         string
	ProbeKeyHMAC              string
	SystemFingerprintHMAC     string
	ProbeFamily               string
	ProbeSetVersion           string
	TokenizerVersion          string
	FeatureVersion            string
	RoundIndex                int
	PaddingTokens             int
	LocalInputTokens          int
	ReportedInputTokens       *int
	CachedInputTokens         *int
	ConstraintPassed          *bool
	Features                  [8]*float64
	ObservationStatus         string
	IdentityStatus            string
	MappingStatus             string
	ProtocolStatus            string
	EvidenceCoverage          int
	TraceID                   string
	CreatedAt                 time.Time
}

// OutcomeProjection is the atomic dataset projection of one completed Go
// model-check execution. The run must already exist in running state; items
// and the terminal run update are committed together so a crash cannot expose
// a terminal run with only a prefix of its evidence.
type OutcomeProjection struct {
	RunID           string
	Items           []ItemInput
	Status          RunStatus
	Level           string
	Score           int
	MaxScore        int
	Message         string
	FinishedAt      time.Time
	ResultSummary   json.RawMessage
	QualityDecision json.RawMessage
}

var ErrProjectionConflict = errors.New("model check outcome projection conflicts with terminal run")

type Store struct {
	db   *sql.DB
	mode StoreMode
}

func OpenSQLite(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("model check SQLite path is required")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open model check SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Store{db: db, mode: StoreSQLite}, nil
}

// OpenPostgres opens the jobs-owned dataset connection. PostgreSQL schema is
// provisioned by the explicit Node-compatible maintenance path; jobs only
// verifies it and never executes DDL at runtime.
func OpenPostgres(dsn string, maxOpen, maxIdle int) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("model check PostgreSQL URL is required")
	}
	if maxOpen <= 0 {
		maxOpen = 1000
	}
	if maxIdle <= 0 {
		maxIdle = maxOpen
	}
	if maxIdle > maxOpen {
		return nil, errors.New("model check PostgreSQL idle pool cannot exceed open pool")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open model check PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	return &Store{db: db, mode: StorePostgres}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	return NewStoreWithMode(db, StoreSQLite)
}

func NewStoreWithMode(db *sql.DB, mode StoreMode) (*Store, error) {
	if db == nil {
		return nil, errors.New("model check database is required")
	}
	if mode != StoreSQLite && mode != StorePostgres {
		return nil, errors.New("model check store mode is invalid")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Store{db: db, mode: mode}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("model check store is not initialized")
	}
	if s.mode == StorePostgres {
		return s.CheckSchema(ctx)
	}
	_, err := s.db.ExecContext(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("initialize model check schema: %w", err)
	}
	return nil
}

// CheckSchema verifies the three J3b tables through the same direct database
// connection used by jobs. It intentionally has no repair or fallback path.
func (s *Store) CheckSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("model check store is not initialized")
	}
	for table, columns := range map[string][]string{
		"model_check_runs":         {"id", "status", "request_summary_json", "quality_decision_json"},
		"model_check_items":        {"id", "run_id", "item_key", "evidence_summary_json"},
		"model_check_observations": {"id", "run_id", "probe_key_hmac", "aggregation_completed_at"},
	} {
		query := "SELECT " + strings.Join(columns, ",") + " FROM " + table + " LIMIT 0"
		if _, err := s.db.ExecContext(ctx, s.bind(query)); err != nil {
			return fmt.Errorf("verify model check schema %s: %w", table, err)
		}
	}
	if err := s.checkIndexes(ctx); err != nil {
		return err
	}
	return nil
}

var requiredModelCheckIndexes = map[string][]string{
	"model_check_runs": {
		"idx_model_check_runs_created", "idx_model_check_runs_system_account_created", "idx_model_check_runs_actor_created",
		"idx_model_check_runs_model_created", "idx_model_check_runs_level_created", "idx_model_check_runs_status_created",
		"idx_model_check_runs_target_created", "idx_model_check_runs_account_created", "idx_model_check_runs_trigger_created",
		"idx_model_check_runs_quality_health_sync_retry", "idx_model_check_runs_system_account_model_created",
		"idx_model_check_runs_system_account_level_created", "idx_model_check_runs_system_account_status_created",
		"idx_model_check_runs_system_account_target_created",
	},
	"model_check_items": {
		"idx_model_check_items_run_order", "idx_model_check_items_run_key", "idx_model_check_items_run_status",
	},
	"model_check_observations": {
		"idx_model_check_observations_cursor", "idx_model_check_observations_pending_aggregation",
		"idx_model_check_observations_account_model", "idx_model_check_observations_cohort", "idx_model_check_observations_population",
	},
}

func (s *Store) checkIndexes(ctx context.Context) error {
	for table, required := range requiredModelCheckIndexes {
		found := make(map[string]struct{}, len(required))
		if s.mode == StorePostgres {
			rows, err := s.db.QueryContext(ctx, `SELECT indexname FROM pg_indexes WHERE schemaname='juhe_dataset' AND tablename=$1`, table)
			if err != nil {
				return fmt.Errorf("verify model check indexes %s: %w", table, err)
			}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					return fmt.Errorf("read model check indexes %s: %w", table, err)
				}
				found[name] = struct{}{}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return fmt.Errorf("iterate model check indexes %s: %w", table, err)
			}
			rows.Close()
		} else {
			rows, err := s.db.QueryContext(ctx, "PRAGMA index_list("+table+")")
			if err != nil {
				return fmt.Errorf("verify model check indexes %s: %w", table, err)
			}
			for rows.Next() {
				var seq int
				var name, unique, origin, partial any
				if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
					rows.Close()
					return fmt.Errorf("read model check indexes %s: %w", table, err)
				}
				if indexName, ok := name.(string); ok {
					found[indexName] = struct{}{}
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return fmt.Errorf("iterate model check indexes %s: %w", table, err)
			}
			rows.Close()
		}
		for _, index := range required {
			if _, ok := found[index]; !ok {
				return fmt.Errorf("verify model check schema %s: missing index %s", table, index)
			}
		}
	}
	return nil
}

func (s *Store) CreateRun(ctx context.Context, input RunInput) error {
	if err := validateRunInput(input); err != nil {
		return err
	}
	requestSummary := normalizeJSON(input.RequestSummary)
	policySnapshot := normalizeJSON(input.PolicySnapshot)
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,target_name,target_owner_system_account_id,account_id,group_id,api_key_id,model,profile,trigger_kind,schedule_id,trusted_comparison_enabled,trusted_comparison_available,level,score,max_score,status,message,trace_id,probe_set_version,started_at,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), input.ID, input.SystemAccountID, input.ActorSystemAccountID, input.ProviderCode, input.TargetType, input.TargetID, input.TargetName, input.TargetOwnerSystemAccountID, input.AccountID, input.GroupID, input.APIKeyID, input.Model, input.Profile, string(input.Trigger), nullable(input.ScheduleID), boolInt(input.TrustedComparisonEnabled), boolInt(input.TrustedComparisonAvailable), "unavailable", 0, 100, "running", "", nullable(input.TraceID), input.ProbeSetVersion, input.StartedAt.UTC().Format(time.RFC3339Nano), string(requestSummary), "{}", string(policySnapshot), "{}", input.StartedAt.UTC().Format(time.RFC3339Nano), input.StartedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create model check run: %w", err)
	}
	return nil
}

func (s *Store) AppendItem(ctx context.Context, input ItemInput) error {
	if err := validateItemInput(input); err != nil {
		return err
	}
	tx, err := s.beginRunningRunTx(ctx, input.RunID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	evidence := normalizeJSON(input.EvidenceSummary)
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO model_check_items (id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), input.ID, input.RunID, input.ItemKey, input.ItemType, string(input.Status), input.Score, input.MaxScore, input.DurationMS, nullable(input.TraceID), string(evidence), nullable(input.ErrorCode), nullable(input.ErrorMessage), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("append model check item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model check item: %w", err)
	}
	return nil
}

func (s *Store) AppendObservation(ctx context.Context, input ObservationInput) error {
	if err := validateObservationInput(input); err != nil {
		return err
	}
	tx, err := s.beginRunningRunTx(ctx, input.RunID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	features := make([]any, 8)
	for i, value := range input.Features {
		if value != nil {
			features[i] = *value
		}
	}
	args := []any{input.ID, input.RunID, input.SystemAccountID, input.AccountID, input.ProviderCode, input.ProviderProtocolProfileID, input.EndpointFamily, input.RequestedModel, input.MappedUpstreamModel, nullable(input.ObservedModel), boolInt(input.MappingApplied), input.UpstreamBucketHMAC, input.CohortKeyHMAC, input.PopulationKeyHMAC, input.ProbeKeyHMAC, nullable(input.SystemFingerprintHMAC), input.ProbeFamily, input.ProbeSetVersion, input.TokenizerVersion, input.FeatureVersion, input.RoundIndex, input.PaddingTokens, input.LocalInputTokens, input.ReportedInputTokens, input.CachedInputTokens, boolPtrInt(input.ConstraintPassed)}
	args = append(args, features...)
	args = append(args, input.ObservationStatus, input.IdentityStatus, input.MappingStatus, input.ProtocolStatus, input.EvidenceCoverage, nullable(input.TraceID), input.CreatedAt.UTC().Format(time.RFC3339Nano))
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO model_check_observations (id,run_id,system_account_id,account_id,provider_code,provider_protocol_profile_id,endpoint_family,requested_model,mapped_upstream_model,observed_model,mapping_applied,upstream_bucket_hmac,cohort_key_hmac,population_key_hmac,probe_key_hmac,system_fingerprint_hmac,probe_family,probe_set_version,tokenizer_version,feature_version,round_index,padding_tokens,local_input_tokens,reported_input_tokens,cached_input_tokens,constraint_passed,feature_1,feature_2,feature_3,feature_4,feature_5,feature_6,feature_7,feature_8,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,trace_id,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), args...)
	if err != nil {
		return fmt.Errorf("append model check observation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model check observation: %w", err)
	}
	return nil
}

// ProjectOutcome atomically appends all outcome items and transitions the run
// to its terminal state. A terminal replay is accepted only when its complete
// run summary and item set match the stored projection exactly.
func (s *Store) ProjectOutcome(ctx context.Context, projection OutcomeProjection) error {
	if err := validateOutcomeProjection(projection); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model check outcome projection: %w", err)
	}
	defer tx.Rollback()
	var currentStatus, currentLevel, currentMessage, currentResult, currentDecision, startedAt string
	var currentFinishedAt sql.NullString
	var currentScore, currentMaxScore int
	row := tx.QueryRowContext(ctx, s.lockRunQuery(`SELECT status,level,score,max_score,message,started_at,finished_at,result_summary_json,quality_decision_json FROM model_check_runs WHERE id=?`), projection.RunID)
	if err := row.Scan(&currentStatus, &currentLevel, &currentScore, &currentMaxScore, &currentMessage, &startedAt, &currentFinishedAt, &currentResult, &currentDecision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("model check run not found")
		}
		return fmt.Errorf("read model check run for projection: %w", err)
	}
	if currentStatus != string(RunRunning) {
		if err := s.verifyTerminalProjection(ctx, tx, projection, currentStatus, currentLevel, currentScore, currentMaxScore, currentMessage, currentFinishedAt, currentResult, currentDecision); err != nil {
			return err
		}
		return tx.Commit()
	}
	createdAt := projection.FinishedAt.UTC().Format(time.RFC3339Nano)
	for _, item := range projection.Items {
		evidence := normalizeJSON(item.EvidenceSummary)
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO model_check_items (id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), item.ID, projection.RunID, item.ItemKey, item.ItemType, string(item.Status), item.Score, item.MaxScore, item.DurationMS, nullable(item.TraceID), string(evidence), nullable(item.ErrorCode), nullable(item.ErrorMessage), createdAt, createdAt); err != nil {
			return fmt.Errorf("append model check projected item %s: %w", item.ID, err)
		}
	}
	duration := int64(0)
	if parsed, parseErr := time.Parse(time.RFC3339Nano, startedAt); parseErr == nil {
		duration = projection.FinishedAt.Sub(parsed).Milliseconds()
		if duration < 0 {
			duration = 0
		}
	}
	result := normalizeJSON(projection.ResultSummary)
	decision := normalizeJSON(projection.QualityDecision)
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE model_check_runs SET level=?,score=?,max_score=?,status=?,message=?,finished_at=?,duration_ms=?,result_summary_json=?,quality_decision_json=?,updated_at=? WHERE id=? AND status='running'`), projection.Level, projection.Score, projection.MaxScore, string(projection.Status), projection.Message, projection.FinishedAt.UTC().Format(time.RFC3339Nano), duration, string(result), string(decision), createdAt, projection.RunID); err != nil {
		return fmt.Errorf("finish projected model check run: %w", err)
	}
	return tx.Commit()
}

func (s *Store) verifyTerminalProjection(ctx context.Context, tx *sql.Tx, projection OutcomeProjection, status, level string, score, maxScore int, message string, finishedAt sql.NullString, result, decision string) error {
	if !finishedAt.Valid || status != string(projection.Status) || level != projection.Level || score != projection.Score || maxScore != projection.MaxScore || message != projection.Message || finishedAt.String != projection.FinishedAt.UTC().Format(time.RFC3339Nano) || !jsonEqual([]byte(result), projection.ResultSummary) || !jsonEqual([]byte(decision), projection.QualityDecision) {
		return ErrProjectionConflict
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message FROM model_check_items WHERE run_id=? ORDER BY id`), projection.RunID)
	if err != nil {
		return fmt.Errorf("read projected model check items: %w", err)
	}
	defer rows.Close()
	expected := make(map[string]ItemInput, len(projection.Items))
	for _, item := range projection.Items {
		expected[item.ID] = item
	}
	seen := 0
	for rows.Next() {
		var id, key, itemType, itemStatus, traceID, evidence, errorCode, errorMessage sql.NullString
		var itemScore, itemMax int
		var duration sql.NullInt64
		if err := rows.Scan(&id, &key, &itemType, &itemStatus, &itemScore, &itemMax, &duration, &traceID, &evidence, &errorCode, &errorMessage); err != nil {
			return fmt.Errorf("scan projected model check item: %w", err)
		}
		item, ok := expected[id.String]
		if !ok || item.ItemKey != key.String || item.ItemType != itemType.String || string(item.Status) != itemStatus.String || item.Score != itemScore || item.MaxScore != itemMax || !nullableIntEqual(item.DurationMS, duration) || !nullableStringMatches(item.TraceID, traceID) || !jsonEqual([]byte(evidence.String), normalizeJSON(item.EvidenceSummary)) || !nullableStringMatches(item.ErrorCode, errorCode) || !nullableStringMatches(item.ErrorMessage, errorMessage) {
			return ErrProjectionConflict
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate projected model check items: %w", err)
	}
	if seen != len(expected) {
		return ErrProjectionConflict
	}
	return nil
}

func validateOutcomeProjection(projection OutcomeProjection) error {
	if strings.TrimSpace(projection.RunID) == "" || len(projection.Items) == 0 || (projection.Status != RunCompleted && projection.Status != RunFailed && projection.Status != RunCanceled) || strings.TrimSpace(projection.Level) == "" || projection.Score < 0 || projection.MaxScore < 0 || projection.Score > projection.MaxScore || projection.FinishedAt.IsZero() {
		return errors.New("model check outcome projection input is invalid")
	}
	seen := make(map[string]struct{}, len(projection.Items))
	for _, item := range projection.Items {
		if err := validateItemInput(item); err != nil || item.RunID != projection.RunID {
			return errors.New("model check outcome projection item is invalid")
		}
		if _, ok := seen[item.ID]; ok {
			return errors.New("model check outcome projection contains duplicate item ID")
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if !json.Valid(left) || !json.Valid(right) || json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func nullableStringMatches(expected string, actual sql.NullString) bool {
	if strings.TrimSpace(expected) == "" {
		return !actual.Valid
	}
	return actual.Valid && actual.String == expected
}

func nullableIntEqual(expected *int64, actual sql.NullInt64) bool {
	if expected == nil {
		return !actual.Valid
	}
	return actual.Valid && actual.Int64 == *expected
}

func (s *Store) FinishRun(ctx context.Context, runID string, status RunStatus, level string, score, maxScore int, message string, finishedAt time.Time, resultSummary, qualityDecision json.RawMessage) error {
	if strings.TrimSpace(runID) == "" || (status != RunCompleted && status != RunFailed && status != RunCanceled) || strings.TrimSpace(level) == "" || score < 0 || maxScore < 0 || score > maxScore || finishedAt.IsZero() {
		return errors.New("finish model check run input is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finish model check run: %w", err)
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx, s.lockRunQuery(`SELECT status FROM model_check_runs WHERE id=?`), runID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("model check run not found")
		}
		return err
	}
	if current != string(RunRunning) {
		return fmt.Errorf("model check run is already terminal: %s", current)
	}
	result := normalizeJSON(resultSummary)
	decision := normalizeJSON(qualityDecision)
	var startedAt string
	if err := tx.QueryRowContext(ctx, s.lockRunQuery(`SELECT started_at FROM model_check_runs WHERE id=?`), runID).Scan(&startedAt); err != nil {
		return fmt.Errorf("read model check run start time: %w", err)
	}
	duration := int64(0)
	if parsed, parseErr := time.Parse(time.RFC3339Nano, startedAt); parseErr == nil {
		duration = finishedAt.Sub(parsed).Milliseconds()
		if duration < 0 {
			duration = 0
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE model_check_runs SET level=?,score=?,max_score=?,status=?,message=?,finished_at=?,duration_ms=?,result_summary_json=?,quality_decision_json=?,updated_at=? WHERE id=? AND status='running'`), level, score, maxScore, string(status), message, finishedAt.UTC().Format(time.RFC3339Nano), duration, string(result), string(decision), finishedAt.UTC().Format(time.RFC3339Nano), runID); err != nil {
		return fmt.Errorf("finish model check run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finished model check run: %w", err)
	}
	return nil
}

func (s *Store) beginRunningRunTx(ctx context.Context, runID string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model check run append: %w", err)
	}
	var status string
	err = tx.QueryRowContext(ctx, s.lockRunQuery(`SELECT status FROM model_check_runs WHERE id=?`), runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil, errors.New("model check run not found")
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("read model check run status: %w", err)
	}
	if status != string(RunRunning) {
		_ = tx.Rollback()
		return nil, fmt.Errorf("model check run is not running: %s", status)
	}
	return tx, nil
}

func validateRunInput(input RunInput) error {
	for name, value := range map[string]string{"id": input.ID, "systemAccountId": input.SystemAccountID, "actorSystemAccountId": input.ActorSystemAccountID, "providerCode": input.ProviderCode, "targetType": input.TargetType, "targetId": input.TargetID, "model": input.Model, "profile": input.Profile, "probeSetVersion": input.ProbeSetVersion} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("model check run %s is required", name)
		}
	}
	if input.Trigger != TriggerManual && input.Trigger != TriggerScheduled && input.Trigger != TriggerQualityRecovery {
		return errors.New("model check run trigger is invalid")
	}
	if input.Profile != "quick" && input.Profile != "full" {
		return errors.New("model check run profile is invalid")
	}
	if input.StartedAt.IsZero() {
		return errors.New("model check run startedAt is required")
	}
	return nil
}
func validateItemInput(input ItemInput) error {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.ItemKey) == "" || strings.TrimSpace(input.ItemType) == "" || input.Score < 0 || input.MaxScore < 0 || input.Score > input.MaxScore {
		return errors.New("model check item input is invalid")
	}
	switch input.Status {
	case ItemPassed, ItemWarning, ItemFailed, ItemSkipped:
	default:
		return errors.New("model check item status is invalid")
	}
	return nil
}
func validateObservationInput(input ObservationInput) error {
	for name, value := range map[string]string{"id": input.ID, "runId": input.RunID, "systemAccountId": input.SystemAccountID, "accountId": input.AccountID, "providerCode": input.ProviderCode, "providerProtocolProfileId": input.ProviderProtocolProfileID, "endpointFamily": input.EndpointFamily, "requestedModel": input.RequestedModel, "mappedUpstreamModel": input.MappedUpstreamModel, "upstreamBucketHmac": input.UpstreamBucketHMAC, "cohortKeyHmac": input.CohortKeyHMAC, "populationKeyHmac": input.PopulationKeyHMAC, "probeKeyHmac": input.ProbeKeyHMAC, "probeFamily": input.ProbeFamily, "probeSetVersion": input.ProbeSetVersion, "tokenizerVersion": input.TokenizerVersion, "observationStatus": input.ObservationStatus, "identityStatus": input.IdentityStatus, "mappingStatus": input.MappingStatus, "protocolStatus": input.ProtocolStatus} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("model check observation %s is required", name)
		}
	}
	if input.CreatedAt.IsZero() || input.RoundIndex < 0 || input.PaddingTokens < 0 || input.LocalInputTokens < 0 || input.EvidenceCoverage < 0 || input.EvidenceCoverage > 100 {
		return errors.New("model check observation input is invalid")
	}
	return nil
}
func normalizeJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage("{}")
	}
	return value
}
func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func boolPtrInt(value *bool) any {
	if value == nil {
		return nil
	}
	return boolInt(*value)
}

func (s *Store) bind(query string) string {
	if s.mode != StorePostgres {
		return query
	}
	for _, table := range []string{"model_check_runs", "model_check_items", "model_check_observations"} {
		query = strings.ReplaceAll(query, table, "juhe_dataset."+table)
	}
	for index := 1; strings.Contains(query, "?"); index++ {
		query = strings.Replace(query, "?", fmt.Sprintf("$%d", index), 1)
	}
	return query
}

// lockRunQuery serializes a PostgreSQL append or finish operation on one run.
// SQLite already serializes its single-writer transaction path.
func (s *Store) lockRunQuery(query string) string {
	if s.mode == StorePostgres {
		query += " FOR UPDATE"
	}
	return s.bind(query)
}

const schemaSQL = `PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS model_check_runs (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,actor_system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,target_name TEXT,target_owner_system_account_id TEXT,account_id TEXT,group_id TEXT,api_key_id TEXT,model TEXT NOT NULL,profile TEXT NOT NULL DEFAULT 'quick',trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('manual','scheduled','quality_recovery')),schedule_id TEXT,trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0 CHECK(trusted_comparison_enabled IN (0,1)),trusted_comparison_available INTEGER NOT NULL DEFAULT 0 CHECK(trusted_comparison_available IN (0,1)),level TEXT NOT NULL DEFAULT 'unavailable',score INTEGER NOT NULL DEFAULT 0,max_score INTEGER NOT NULL DEFAULT 100,status TEXT NOT NULL CHECK(status IN ('running','completed','failed','canceled')),message TEXT NOT NULL DEFAULT '',trace_id TEXT,probe_set_version TEXT NOT NULL,started_at TEXT NOT NULL,finished_at TEXT,duration_ms INTEGER,request_summary_json TEXT NOT NULL DEFAULT '{}',result_summary_json TEXT NOT NULL DEFAULT '{}',policy_snapshot_json TEXT NOT NULL DEFAULT '{}',quality_decision_json TEXT NOT NULL DEFAULT '{}',quality_health_sync_status TEXT CHECK(quality_health_sync_status IS NULL OR quality_health_sync_status IN ('applied','pending_retry','failed')),error_code TEXT,error_message TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS model_check_items (id TEXT PRIMARY KEY,run_id TEXT NOT NULL,item_key TEXT NOT NULL,item_type TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('passed','warning','failed','skipped')),score INTEGER NOT NULL DEFAULT 0,max_score INTEGER NOT NULL DEFAULT 0,duration_ms INTEGER,trace_id TEXT,evidence_summary_json TEXT NOT NULL DEFAULT '{}',error_code TEXT,error_message TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,FOREIGN KEY(run_id) REFERENCES model_check_runs(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS model_check_observations (id TEXT PRIMARY KEY,run_id TEXT NOT NULL,system_account_id TEXT NOT NULL,account_id TEXT NOT NULL,provider_code TEXT NOT NULL,provider_protocol_profile_id TEXT NOT NULL,endpoint_family TEXT NOT NULL,requested_model TEXT NOT NULL,mapped_upstream_model TEXT NOT NULL,observed_model TEXT,mapping_applied INTEGER NOT NULL DEFAULT 0,upstream_bucket_hmac TEXT NOT NULL,cohort_key_hmac TEXT NOT NULL,population_key_hmac TEXT NOT NULL,probe_key_hmac TEXT NOT NULL,system_fingerprint_hmac TEXT,probe_family TEXT NOT NULL,probe_set_version TEXT NOT NULL,tokenizer_version TEXT NOT NULL,feature_version TEXT NOT NULL DEFAULT 'none',round_index INTEGER NOT NULL,padding_tokens INTEGER NOT NULL,local_input_tokens INTEGER NOT NULL,reported_input_tokens INTEGER,cached_input_tokens INTEGER,constraint_passed INTEGER,feature_1 REAL,feature_2 REAL,feature_3 REAL,feature_4 REAL,feature_5 REAL,feature_6 REAL,feature_7 REAL,feature_8 REAL,observation_status TEXT NOT NULL,identity_status TEXT NOT NULL,mapping_status TEXT NOT NULL,protocol_status TEXT NOT NULL,evidence_coverage INTEGER NOT NULL DEFAULT 0,trace_id TEXT,created_at TEXT NOT NULL,aggregation_completed_at TEXT,FOREIGN KEY(run_id) REFERENCES model_check_runs(id) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_created ON model_check_runs(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_created ON model_check_runs(system_account_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_actor_created ON model_check_runs(actor_system_account_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_model_created ON model_check_runs(model,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_level_created ON model_check_runs(level,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_status_created ON model_check_runs(status,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_target_created ON model_check_runs(target_type,target_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_account_created ON model_check_runs(account_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_trigger_created ON model_check_runs(trigger_kind,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_retry ON model_check_runs(quality_health_sync_status,updated_at,id) WHERE quality_health_sync_status='failed';
CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_model_created ON model_check_runs(system_account_id,model,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_level_created ON model_check_runs(system_account_id,level,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_status_created ON model_check_runs(system_account_id,status,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_target_created ON model_check_runs(system_account_id,target_type,target_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_items_run_order ON model_check_items(run_id,created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_items_run_key ON model_check_items(run_id,item_key,id);
CREATE INDEX IF NOT EXISTS idx_model_check_items_run_status ON model_check_items(run_id,status,created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_observations_cursor ON model_check_observations(created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation ON model_check_observations(created_at,id) WHERE aggregation_completed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_model_check_observations_account_model ON model_check_observations(system_account_id,account_id,requested_model,created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_observations_cohort ON model_check_observations(cohort_key_hmac,mapped_upstream_model,created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_observations_population ON model_check_observations(population_key_hmac,requested_model,probe_family,created_at,id);`
