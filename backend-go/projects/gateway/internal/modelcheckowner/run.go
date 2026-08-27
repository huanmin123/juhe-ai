package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

type ItemStatus string

const (
	ItemPassed  ItemStatus = "passed"
	ItemWarning ItemStatus = "warning"
	ItemFailed  ItemStatus = "failed"
	ItemSkipped ItemStatus = "skipped"
)

type RunRecord struct {
	ID, SystemAccountID, ActorSystemAccountID, ProviderCode string
	TargetType, TargetID, Model, Profile, TriggerKind       string
	ProbeSetVersion                                         string
	ScheduleID, TraceID                                     string
	RequestSummary, PolicySnapshot                          json.RawMessage
	StartedAt                                               time.Time
}

type ItemRecord struct {
	ID, RunID, ItemKey, ItemType string
	Status                       ItemStatus
	Score, MaxScore              int
	DurationMS                   *int64
	TraceID, EvidenceSummary     string
	ErrorCode, ErrorMessage      string
}

type ObservationRecord struct {
	ID, RunID, SystemAccountID, AccountID, ProviderCode string
	RequestedModel, MappedUpstreamModel, ProbeFamily    string
	ObservationStatus, IdentityStatus, MappingStatus    string
	ProtocolStatus                                      string
	EvidenceCoverage                                    int
	CreatedAt                                           time.Time
}

type OutcomeProjection struct {
	RunID, Level, Message string
	Status                RunStatus
	Score, MaxScore       int
	FinishedAt            time.Time
	Items                 []ItemRecord
	ResultSummary         json.RawMessage
	QualityDecision       json.RawMessage
}

var ErrRunProjectionConflict = errors.New("J3b run projection conflicts with existing terminal state")

func (s *Store) CreateRun(ctx context.Context, run RunRecord) error {
	if s == nil || s.db == nil {
		return errors.New("J3b store is not open")
	}
	if err := validateRun(run); err != nil {
		return err
	}
	request := normalizeJSON(run.RequestSummary)
	policy := normalizeJSON(run.PolicySnapshot)
	now := run.StartedAt.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_runs")+` (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,schedule_id,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,trace_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), run.ID, run.SystemAccountID, run.ActorSystemAccountID, run.ProviderCode, run.TargetType, run.TargetID, run.Model, run.Profile, run.TriggerKind, nullable(run.ScheduleID), string(RunRunning), "unavailable", 0, 100, "", string(request), "{}", string(policy), "{}", run.ProbeSetVersion, now, nullable(run.TraceID), now, now)
	if err != nil {
		return fmt.Errorf("create J3b run: %w", err)
	}
	return nil
}

func (s *Store) AppendItem(ctx context.Context, item ItemRecord) error {
	if err := validateItem(item); err != nil {
		return err
	}
	tx, err := s.beginRunning(ctx, item.RunID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	evidence := item.EvidenceSummary
	if evidence == "" {
		evidence = "{}"
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_items")+` (id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), item.ID, item.RunID, item.ItemKey, item.ItemType, string(item.Status), item.Score, item.MaxScore, item.DurationMS, nullable(item.TraceID), evidence, nullable(item.ErrorCode), nullable(item.ErrorMessage), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("append J3b item: %w", err)
	}
	return tx.Commit()
}

func (s *Store) AppendObservation(ctx context.Context, observation ObservationRecord) error {
	if err := validateObservation(observation); err != nil {
		return err
	}
	tx, err := s.beginRunning(ctx, observation.RunID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	created := observation.CreatedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_observations")+` (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), observation.ID, observation.RunID, observation.SystemAccountID, observation.AccountID, observation.ProviderCode, observation.RequestedModel, observation.MappedUpstreamModel, observation.ProbeFamily, observation.ObservationStatus, observation.IdentityStatus, observation.MappingStatus, observation.ProtocolStatus, observation.EvidenceCoverage, created)
	if err != nil {
		return fmt.Errorf("append J3b observation: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ProjectOutcome(ctx context.Context, projection OutcomeProjection) error {
	if s == nil || s.db == nil || strings.TrimSpace(projection.RunID) == "" || !terminalStatus(projection.Status) || strings.TrimSpace(projection.Level) == "" || projection.Score < 0 || projection.MaxScore < projection.Score || projection.FinishedAt.IsZero() || len(projection.Items) == 0 || !validOptionalJSON(projection.ResultSummary) || !validOptionalJSON(projection.QualityDecision) {
		return errors.New("J3b outcome projection input is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin J3b outcome projection: %w", err)
	}
	defer tx.Rollback()
	var status, level, message, result, decision string
	var finished sql.NullString
	var score, maxScore int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT status,level,score,max_score,message,result_summary_json,quality_decision_json,finished_at FROM `+s.table("model_check_runs")+` WHERE id=?`), projection.RunID).Scan(&status, &level, &score, &maxScore, &message, &result, &decision, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("J3b run not found")
	}
	if err != nil {
		return fmt.Errorf("read J3b run for projection: %w", err)
	}
	if status != string(RunRunning) {
		if !finished.Valid || status != string(projection.Status) || level != projection.Level || score != projection.Score || maxScore != projection.MaxScore || message != projection.Message || finished.String != projection.FinishedAt.UTC().Format(time.RFC3339Nano) || !jsonEqual([]byte(result), normalizeJSON(projection.ResultSummary)) || !jsonEqual([]byte(decision), normalizeJSON(projection.QualityDecision)) {
			return ErrRunProjectionConflict
		}
		return tx.Commit()
	}
	when := projection.FinishedAt.UTC().Format(time.RFC3339Nano)
	for _, item := range projection.Items {
		if err := validateItem(item); err != nil || item.RunID != projection.RunID {
			return errors.New("J3b outcome item is invalid")
		}
		evidence := item.EvidenceSummary
		if evidence == "" {
			evidence = "{}"
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_items")+` (id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), item.ID, item.RunID, item.ItemKey, item.ItemType, string(item.Status), item.Score, item.MaxScore, item.DurationMS, nullable(item.TraceID), evidence, nullable(item.ErrorCode), nullable(item.ErrorMessage), when, when); err != nil {
			return fmt.Errorf("append projected J3b item: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_runs")+` SET level=?,score=?,max_score=?,status=?,message=?,finished_at=?,result_summary_json=?,quality_decision_json=?,updated_at=? WHERE id=? AND status='running'`), projection.Level, projection.Score, projection.MaxScore, string(projection.Status), projection.Message, when, string(normalizeJSON(projection.ResultSummary)), string(normalizeJSON(projection.QualityDecision)), when, projection.RunID)
	if err != nil {
		return fmt.Errorf("finish J3b run: %w", err)
	}
	return tx.Commit()
}

func (s *Store) beginRunning(ctx context.Context, runID string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin J3b append: %w", err)
	}
	var status string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("model_check_runs")+` WHERE id=?`), runID).Scan(&status); err != nil {
		tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("J3b run not found")
		}
		return nil, err
	}
	if status != string(RunRunning) {
		tx.Rollback()
		return nil, fmt.Errorf("J3b run is not running: %s", status)
	}
	return tx, nil
}

func validateRun(run RunRecord) error {
	for name, value := range map[string]string{"id": run.ID, "system_account_id": run.SystemAccountID, "actor_system_account_id": run.ActorSystemAccountID, "provider_code": run.ProviderCode, "target_type": run.TargetType, "target_id": run.TargetID, "model": run.Model, "profile": run.Profile, "trigger_kind": run.TriggerKind, "probe_set_version": run.ProbeSetVersion} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("J3b run %s is required", name)
		}
	}
	if run.StartedAt.IsZero() || !validOptionalJSON(run.RequestSummary) || !validOptionalJSON(run.PolicySnapshot) {
		return errors.New("J3b run time or JSON is invalid")
	}
	return nil
}

func validOptionalJSON(value []byte) bool {
	return len(value) == 0 || json.Valid(value)
}

func validateItem(item ItemRecord) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.RunID) == "" || strings.TrimSpace(item.ItemKey) == "" || strings.TrimSpace(item.ItemType) == "" || (item.Status != ItemPassed && item.Status != ItemWarning && item.Status != ItemFailed && item.Status != ItemSkipped) || item.Score < 0 || item.MaxScore < item.Score {
		return errors.New("J3b item is invalid")
	}
	if item.EvidenceSummary != "" && !json.Valid([]byte(item.EvidenceSummary)) {
		return errors.New("J3b item evidence must be valid JSON")
	}
	return nil
}

func validateObservation(observation ObservationRecord) error {
	for name, value := range map[string]string{"id": observation.ID, "run_id": observation.RunID, "system_account_id": observation.SystemAccountID, "account_id": observation.AccountID, "provider_code": observation.ProviderCode, "requested_model": observation.RequestedModel, "mapped_upstream_model": observation.MappedUpstreamModel, "probe_family": observation.ProbeFamily, "observation_status": observation.ObservationStatus, "identity_status": observation.IdentityStatus, "mapping_status": observation.MappingStatus, "protocol_status": observation.ProtocolStatus} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("J3b observation %s is required", name)
		}
	}
	if observation.EvidenceCoverage < 0 || observation.CreatedAt.IsZero() {
		return errors.New("J3b observation evidence or time is invalid")
	}
	return nil
}

func terminalStatus(status RunStatus) bool {
	return status == RunCompleted || status == RunFailed || status == RunCanceled
}

func normalizeJSON(value []byte) []byte {
	if len(value) == 0 || !json.Valid(value) {
		return []byte("{}")
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return []byte("{}")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
