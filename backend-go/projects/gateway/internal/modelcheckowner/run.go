package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxSummaryStringLength = 500
	maxSummaryArrayLength  = 20
	maxSummaryObjectKeys   = 32
	maxSummaryDepth        = 4
)

var (
	sensitiveSummaryKeyPattern  = regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|set-cookie|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|rawbody|raw_body|fullresponse|full_response)`)
	apiKeySummaryPattern        = regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{6,}\b`)
	authorizationSummaryPattern = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	proxyURLSummaryPattern      = regexp.MustCompile(`(?i)(https?://[^/\s:@]+:)[^@\s/]+@`)
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
	TargetName, TargetOwnerSystemAccountID                  string
	AccountID, GroupID, APIKeyID                            string
	ProbeSetVersion                                         string
	ScheduleID, TraceID                                     string
	TrustedComparison, TrustedComparisonAvailable           bool
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
	RunID, Level, Message   string
	Status                  RunStatus
	Score, MaxScore         int
	FinishedAt              time.Time
	DurationMS              *int64
	ErrorCode, ErrorMessage string
	Items                   []ItemRecord
	ResultSummary           json.RawMessage
	QualityDecision         json.RawMessage
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
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_runs")+` (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,target_name,target_owner_system_account_id,account_id,group_id,api_key_id,model,profile,trigger_kind,schedule_id,trusted_comparison_enabled,trusted_comparison_available,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,trace_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), run.ID, run.SystemAccountID, run.ActorSystemAccountID, run.ProviderCode, run.TargetType, run.TargetID, nullable(run.TargetName), nullable(run.TargetOwnerSystemAccountID), nullable(run.AccountID), nullable(run.GroupID), nullable(run.APIKeyID), run.Model, run.Profile, run.TriggerKind, nullable(run.ScheduleID), boolToInt(run.TrustedComparison), boolToInt(run.TrustedComparisonAvailable), string(RunRunning), "unavailable", 0, 100, "", string(request), "{}", string(policy), "{}", run.ProbeSetVersion, now, nullable(run.TraceID), now, now)
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
	evidence := normalizeJSON([]byte(item.EvidenceSummary))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_items")+` (id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), item.ID, item.RunID, item.ItemKey, item.ItemType, string(item.Status), item.Score, item.MaxScore, item.DurationMS, nullable(item.TraceID), string(evidence), nullable(sanitizeSummaryString(item.ErrorCode)), nullable(sanitizeSummaryString(item.ErrorMessage)), now, now)
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
	if s == nil || s.db == nil || strings.TrimSpace(projection.RunID) == "" || !terminalStatus(projection.Status) || strings.TrimSpace(projection.Level) == "" || projection.Score < 0 || projection.MaxScore < projection.Score || projection.FinishedAt.IsZero() || len(projection.Items) == 0 || !validOptionalJSONObject(projection.ResultSummary) || !validOptionalJSONObject(projection.QualityDecision) {
		return errors.New("J3b outcome projection input is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin J3b outcome projection: %w", err)
	}
	defer tx.Rollback()
	var status, level, message, result, decision string
	var finished sql.NullString
	var duration sql.NullInt64
	var errorCode, errorMessage sql.NullString
	var score, maxScore int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT status,level,score,max_score,message,result_summary_json,quality_decision_json,finished_at,duration_ms,error_code,error_message FROM `+s.table("model_check_runs")+` WHERE id=?`+s.forUpdate()), projection.RunID).Scan(&status, &level, &score, &maxScore, &message, &result, &decision, &finished, &duration, &errorCode, &errorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("J3b run not found")
	}
	if err != nil {
		return fmt.Errorf("read J3b run for projection: %w", err)
	}
	projectedMessage := sanitizeSummaryString(projection.Message)
	if status != string(RunRunning) {
		itemsMatch, itemsErr := s.terminalItemsMatch(ctx, tx, projection.RunID, projection.Items)
		if itemsErr != nil {
			return itemsErr
		}
		if !itemsMatch || !finished.Valid || status != string(projection.Status) || level != projection.Level || score != projection.Score || maxScore != projection.MaxScore || message != projectedMessage || finished.String != projection.FinishedAt.UTC().Format(time.RFC3339Nano) || !sameNullableInt64(duration, projection.DurationMS) || !sameNullableString(errorCode, sanitizeNullableSummaryString(projection.ErrorCode)) || !sameNullableString(errorMessage, sanitizeNullableSummaryString(projection.ErrorMessage)) || !jsonEqual([]byte(result), normalizeJSON(projection.ResultSummary)) || !jsonEqual([]byte(decision), normalizeJSON(projection.QualityDecision)) {
			return ErrRunProjectionConflict
		}
		return tx.Commit()
	}
	when := projection.FinishedAt.UTC().Format(time.RFC3339Nano)
	for _, item := range projection.Items {
		if err := validateItem(item); err != nil || item.RunID != projection.RunID {
			return errors.New("J3b outcome item is invalid")
		}
		evidence := normalizeJSON([]byte(item.EvidenceSummary))
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_items")+` (id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), item.ID, item.RunID, item.ItemKey, item.ItemType, string(item.Status), item.Score, item.MaxScore, item.DurationMS, nullable(item.TraceID), string(evidence), nullable(sanitizeSummaryString(item.ErrorCode)), nullable(sanitizeSummaryString(item.ErrorMessage)), when, when); err != nil {
			return fmt.Errorf("append projected J3b item: %w", err)
		}
	}
	updateResult, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_runs")+` SET level=?,score=?,max_score=?,status=?,message=?,finished_at=?,duration_ms=?,error_code=?,error_message=?,result_summary_json=?,quality_decision_json=?,updated_at=? WHERE id=? AND status='running'`), projection.Level, projection.Score, projection.MaxScore, string(projection.Status), projectedMessage, when, projection.DurationMS, nullable(sanitizeNullableSummaryString(projection.ErrorCode)), nullable(sanitizeNullableSummaryString(projection.ErrorMessage)), string(normalizeJSON(projection.ResultSummary)), string(normalizeJSON(projection.QualityDecision)), when, projection.RunID)
	if err != nil {
		return fmt.Errorf("finish J3b run: %w", err)
	}
	if changed, err := updateResult.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return fmt.Errorf("finish J3b run affected rows: %w", err)
		}
		return errors.New("finish J3b run affected unexpected number of rows")
	}
	return tx.Commit()
}

func (s *Store) terminalItemsMatch(ctx context.Context, tx *sql.Tx, runID string, expected []ItemRecord) (bool, error) {
	wantItems := append([]ItemRecord(nil), expected...)
	sort.Slice(wantItems, func(i, j int) bool { return wantItems[i].ID < wantItems[j].ID })
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message FROM `+s.table("model_check_items")+` WHERE run_id=? ORDER BY id`), runID)
	if err != nil {
		return false, fmt.Errorf("read J3b terminal items: %w", err)
	}
	defer rows.Close()
	actual := make([]ItemRecord, 0, len(expected))
	for rows.Next() {
		var item ItemRecord
		var status string
		var trace, evidence, errorCode, errorMessage sql.NullString
		var duration sql.NullInt64
		if err := rows.Scan(&item.ID, &item.RunID, &item.ItemKey, &item.ItemType, &status, &item.Score, &item.MaxScore, &duration, &trace, &evidence, &errorCode, &errorMessage); err != nil {
			return false, fmt.Errorf("scan J3b terminal item: %w", err)
		}
		item.Status = ItemStatus(status)
		if duration.Valid {
			value := duration.Int64
			item.DurationMS = &value
		}
		if trace.Valid {
			item.TraceID = trace.String
		}
		if evidence.Valid {
			item.EvidenceSummary = evidence.String
		}
		if errorCode.Valid {
			item.ErrorCode = errorCode.String
		}
		if errorMessage.Valid {
			item.ErrorMessage = errorMessage.String
		}
		actual = append(actual, item)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate J3b terminal items: %w", err)
	}
	if len(actual) != len(expected) {
		return false, nil
	}
	for i := range wantItems {
		want := wantItems[i]
		if err := validateItem(want); err != nil || want.RunID != runID {
			return false, nil
		}
		got := actual[i]
		if got.ID != want.ID || got.RunID != want.RunID || got.ItemKey != want.ItemKey || got.ItemType != want.ItemType || got.Status != want.Status || got.Score != want.Score || got.MaxScore != want.MaxScore || !sameNullableInt64Value(got.DurationMS, want.DurationMS) || got.TraceID != want.TraceID || !jsonEqual([]byte(got.EvidenceSummary), normalizeJSON([]byte(want.EvidenceSummary))) || sanitizeNullableSummaryString(got.ErrorCode) != sanitizeNullableSummaryString(want.ErrorCode) || sanitizeNullableSummaryString(got.ErrorMessage) != sanitizeNullableSummaryString(want.ErrorMessage) {
			return false, nil
		}
	}
	return true, nil
}

func sameNullableInt64Value(actual, expected *int64) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return *actual == *expected
}

func (s *Store) beginRunning(ctx context.Context, runID string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin J3b append: %w", err)
	}
	var status string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("model_check_runs")+` WHERE id=?`+s.forUpdate()), runID).Scan(&status); err != nil {
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
	if run.StartedAt.IsZero() || !validOptionalJSONObject(run.RequestSummary) || !validOptionalJSONObject(run.PolicySnapshot) {
		return errors.New("J3b run time or JSON is invalid")
	}
	return nil
}

func validOptionalJSONObject(value []byte) bool {
	if len(value) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validateItem(item ItemRecord) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.RunID) == "" || strings.TrimSpace(item.ItemKey) == "" || strings.TrimSpace(item.ItemType) == "" || (item.Status != ItemPassed && item.Status != ItemWarning && item.Status != ItemFailed && item.Status != ItemSkipped) || item.Score < 0 || item.MaxScore < item.Score {
		return errors.New("J3b item is invalid")
	}
	if item.EvidenceSummary != "" && !validOptionalJSONObject([]byte(item.EvidenceSummary)) {
		return errors.New("J3b item evidence must be a JSON object")
	}
	return nil
}

func validateObservation(observation ObservationRecord) error {
	for name, value := range map[string]string{"id": observation.ID, "run_id": observation.RunID, "system_account_id": observation.SystemAccountID, "account_id": observation.AccountID, "provider_code": observation.ProviderCode, "requested_model": observation.RequestedModel, "mapped_upstream_model": observation.MappedUpstreamModel, "probe_family": observation.ProbeFamily, "observation_status": observation.ObservationStatus, "identity_status": observation.IdentityStatus, "mapping_status": observation.MappingStatus, "protocol_status": observation.ProtocolStatus} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("J3b observation %s is required", name)
		}
	}
	if observation.EvidenceCoverage < 0 || observation.EvidenceCoverage > 100 || observation.CreatedAt.IsZero() {
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
	if _, ok := decoded.(map[string]any); !ok {
		return []byte("{}")
	}
	encoded, err := json.Marshal(sanitizeSummaryValue(decoded, 0))
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// sanitizeSummaryValue aligns the Gateway's durable run/item projections with
// the Node contract: they retain bounded diagnostics but never credentials,
// raw bodies, or unbounded upstream output.
func sanitizeSummaryValue(value any, depth int) any {
	if depth >= maxSummaryDepth {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case nil, bool, float64:
		return typed
	case string:
		return sanitizeSummaryString(typed)
	case []any:
		limit := len(typed)
		if limit > maxSummaryArrayLength {
			limit = maxSummaryArrayLength
		}
		result := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			result = append(result, sanitizeSummaryValue(item, depth+1))
		}
		return result
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > maxSummaryObjectKeys {
			keys = keys[:maxSummaryObjectKeys]
		}
		result := make(map[string]any, len(keys))
		for _, key := range keys {
			if sensitiveSummaryKeyPattern.MatchString(key) {
				result[key] = "[redacted]"
				continue
			}
			result[key] = sanitizeSummaryValue(typed[key], depth+1)
		}
		return result
	default:
		return fmt.Sprint(typed)
	}
}

func sanitizeSummaryString(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxSummaryStringLength {
		value = string(runes[:maxSummaryStringLength]) + "..."
	}
	value = apiKeySummaryPattern.ReplaceAllString(value, "[redacted]")
	value = authorizationSummaryPattern.ReplaceAllString(value, "$1 [redacted]")
	return proxyURLSummaryPattern.ReplaceAllString(value, "$1[redacted]@")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sanitizeNullableSummaryString(value string) string {
	return strings.TrimSpace(sanitizeSummaryString(value))
}

func sameNullableString(actual sql.NullString, expected string) bool {
	return actual.Valid == (expected != "") && (!actual.Valid || actual.String == expected)
}

func sameNullableInt64(actual sql.NullInt64, expected *int64) bool {
	return actual.Valid == (expected != nil) && (!actual.Valid || actual.Int64 == *expected)
}
