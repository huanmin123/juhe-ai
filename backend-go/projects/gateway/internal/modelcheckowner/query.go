package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type RunListResult struct {
	Items    []RunView `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type RunView struct {
	ID                         string  `json:"id"`
	SystemAccountID            string  `json:"systemAccountId"`
	ActorSystemAccountID       string  `json:"actorSystemAccountId"`
	ProviderCode               string  `json:"providerCode"`
	TargetType                 string  `json:"targetType"`
	TargetID                   string  `json:"targetId"`
	TargetName                 *string `json:"targetName,omitempty"`
	TargetOwnerSystemAccountID *string `json:"targetOwnerSystemAccountId,omitempty"`
	AccountID                  *string `json:"accountId,omitempty"`
	GroupID                    *string `json:"groupId,omitempty"`
	APIKeyID                   *string `json:"apiKeyId,omitempty"`
	Model                      string  `json:"model"`
	Profile                    string  `json:"profile"`
	TriggerKind                string  `json:"triggerKind"`
	ScheduleID                 *string `json:"scheduleId,omitempty"`
	TrustedComparison          bool    `json:"trustedComparison"`
	TrustedComparisonAvailable bool    `json:"trustedComparisonAvailable"`
	Status                     string  `json:"status"`
	Level                      string  `json:"level"`
	Message                    string  `json:"message"`
	Score                      int     `json:"score"`
	MaxScore                   int     `json:"maxScore"`
	ProbeSetVersion            string  `json:"probeSetVersion"`
	StartedAt                  string  `json:"startedAt"`
	TraceID                    *string `json:"traceId,omitempty"`
	FinishedAt                 *string `json:"finishedAt,omitempty"`
	DurationMS                 *int64  `json:"durationMs,omitempty"`
	ErrorCode                  *string `json:"errorCode,omitempty"`
	ErrorMessage               *string `json:"errorMessage,omitempty"`
	CreatedAt                  string  `json:"createdAt"`
	UpdatedAt                  string  `json:"updatedAt"`
}

// RunDetail is built only from the durable J3b run/item projection. It does
// not manufacture a report from an in-memory execution result: an unreadable
// JSON projection causes GetRun to fail so the HTTP layer remains closed.
type RunDetail struct {
	RunView
	RequestSummary  json.RawMessage `json:"requestSummary"`
	ResultSummary   json.RawMessage `json:"resultSummary"`
	PolicySnapshot  json.RawMessage `json:"policySnapshot"`
	QualityDecision json.RawMessage `json:"qualityDecision"`
	Checks          []RunCheck      `json:"checks"`
}

type RunCheck struct {
	ID              string          `json:"id"`
	RunID           string          `json:"runId"`
	ItemKey         string          `json:"itemKey"`
	ItemType        string          `json:"itemType"`
	Status          string          `json:"status"`
	Score           int             `json:"score"`
	MaxScore        int             `json:"maxScore"`
	DurationMS      *int64          `json:"durationMs,omitempty"`
	TraceID         *string         `json:"traceId,omitempty"`
	EvidenceSummary json.RawMessage `json:"evidenceSummary"`
	ErrorCode       *string         `json:"errorCode,omitempty"`
	ErrorMessage    *string         `json:"errorMessage,omitempty"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
}

func (s *Runtime) ListRuns(ctx context.Context, query RunListQuery) (any, error) {
	if s == nil || s.Store == nil || s.Store.db == nil {
		return nil, errors.New("J3b runtime list scope is incomplete")
	}
	pageSize := query.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)
	if query.AllSystemAccounts {
		// Administrator global history is a read-only scope. Runtime callers
		// must never combine it with a target tenant value.
		if strings.TrimSpace(query.SystemAccountID) != "" {
			return nil, errors.New("J3b runtime global scope cannot include systemAccountId")
		}
		clauses = append(clauses, "1=1")
	} else {
		if strings.TrimSpace(query.SystemAccountID) == "" {
			return nil, errors.New("J3b runtime list scope is incomplete")
		}
		clauses = append(clauses, "system_account_id=?")
		args = append(args, strings.TrimSpace(query.SystemAccountID))
	}
	for _, filter := range []struct {
		column string
		value  string
	}{
		{"target_id", query.TargetID},
		{"model", query.Model},
		{"level", query.Level},
		{"status", query.Status},
		{"trigger_kind", query.TriggerKind},
	} {
		if value := strings.TrimSpace(filter.value); value != "" {
			clauses = append(clauses, filter.column+"=?")
			args = append(args, value)
		}
	}
	if value := strings.TrimSpace(query.StartAt); value != "" {
		clauses = append(clauses, "created_at>=?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.EndAt); value != "" {
		clauses = append(clauses, "created_at<=?")
		args = append(args, value)
	}
	args = append(args, pageSize+1, offset)
	statement := `SELECT ` + runViewColumns + ` FROM ` + s.Store.table("model_check_runs") + ` WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`
	rows, err := s.Store.db.QueryContext(ctx, s.Store.bind(statement), args...)
	if err != nil {
		return nil, fmt.Errorf("list J3b runs: %w", err)
	}
	defer rows.Close()
	result := RunListResult{Items: make([]RunView, 0), Page: page, PageSize: pageSize}
	for rows.Next() {
		var view RunView
		if err := scanRunView(rows, &view); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result.Items) > pageSize {
		result.HasMore = true
		result.Items = result.Items[:pageSize]
	}
	result.Total = (page-1)*pageSize + len(result.Items)
	if result.HasMore {
		// Node uses an upper-bound total to avoid a second COUNT query.
		result.Total++
	}
	return result, nil
}

func (s *Runtime) GetRun(ctx context.Context, runID string) (any, bool, error) {
	if s == nil || s.Store == nil || s.Store.db == nil || strings.TrimSpace(runID) == "" {
		return nil, false, errors.New("J3b runtime run ID is required")
	}
	var detail RunDetail
	var requestSummary, resultSummary, policySnapshot, qualityDecision string
	err := scanRunViewWithSummaries(s.Store.db.QueryRowContext(ctx, s.Store.bind(`SELECT `+runViewColumns+`,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json FROM `+s.Store.table("model_check_runs")+` WHERE id=?`), strings.TrimSpace(runID)), &detail.RunView, &requestSummary, &resultSummary, &policySnapshot, &qualityDecision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read J3b run: %w", err)
	}
	if detail.RequestSummary, err = requiredJSONObject("J3b request summary", requestSummary); err != nil {
		return nil, false, err
	}
	if detail.ResultSummary, err = requiredJSONObject("J3b result summary", resultSummary); err != nil {
		return nil, false, err
	}
	if detail.PolicySnapshot, err = requiredJSONObject("J3b policy snapshot", policySnapshot); err != nil {
		return nil, false, err
	}
	if detail.QualityDecision, err = requiredJSONObject("J3b quality decision", qualityDecision); err != nil {
		return nil, false, err
	}
	checks, err := s.readRunChecks(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, false, err
	}
	detail.Checks = checks
	// Node refreshes the full-detail trust report from the latest durable
	// projection. A missing or temporarily unreadable latest row must not make
	// an otherwise valid historical run disappear; the stored run summary is
	// the fallback, just as it is in the Node reader.
	s.mergeLatestTrustReport(ctx, &detail)
	return detail, true, nil
}

func (s *Runtime) mergeLatestTrustReport(ctx context.Context, detail *RunDetail) {
	if detail == nil || detail.Profile == "quick" || detail.AccountID == nil || strings.TrimSpace(*detail.AccountID) == "" {
		return
	}
	var unverified bool
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(detail.ResultSummary, &summary); err != nil || summary == nil {
		return
	}
	if raw, ok := summary["modelCheckUnverified"]; ok {
		_ = json.Unmarshal(raw, &unverified)
	}
	if unverified {
		return
	}
	var currentTrust map[string]json.RawMessage
	if raw, ok := summary["trustReport"]; ok {
		if err := json.Unmarshal(raw, &currentTrust); err != nil {
			currentTrust = nil
		}
	}
	if hasTrustReason(currentTrust, "model_response_evidence_unavailable") {
		return
	}
	if detail.Level == "unavailable" && !hasTrustText(currentTrust, "observedModel") {
		return
	}
	var row struct {
		IdentityStatus   string
		MappingStatus    string
		UsageIntegrity   string
		ProtocolStatus   string
		EvidenceStatus   string
		EvidenceCoverage int
		ObservationCount int
		ReasonCodesJSON  string
		LastObservedID   sql.NullString
		LastObservedAt   sql.NullString
	}
	query := `SELECT identity_status,mapping_status,usage_integrity_status,protocol_status,evidence_status,evidence_coverage,observation_count,reason_codes_json,last_observed_id,last_observed_at FROM ` + s.Store.table("model_account_trust_results") + ` WHERE system_account_id=? AND account_id=? AND requested_model=? LIMIT 1`
	err := s.Store.db.QueryRowContext(ctx, s.Store.bind(query), detail.SystemAccountID, strings.TrimSpace(*detail.AccountID), detail.Model).Scan(
		&row.IdentityStatus, &row.MappingStatus, &row.UsageIntegrity, &row.ProtocolStatus, &row.EvidenceStatus,
		&row.EvidenceCoverage, &row.ObservationCount, &row.ReasonCodesJSON, &row.LastObservedID, &row.LastObservedAt,
	)
	if err != nil {
		return
	}
	trust := map[string]any{
		"requestedModel":       detail.Model,
		"identityStatus":       row.IdentityStatus,
		"mappingStatus":        row.MappingStatus,
		"usageIntegrityStatus": row.UsageIntegrity,
		"protocolStatus":       row.ProtocolStatus,
		"evidenceStatus":       row.EvidenceStatus,
		"evidenceCoverage":     row.EvidenceCoverage,
		"observationCount":     row.ObservationCount,
	}
	if reasons := parseTrustReasonCodes(row.ReasonCodesJSON); reasons != nil {
		trust["reasonCodes"] = reasons
	}
	if row.LastObservedID.Valid {
		trust["lastObservedId"] = row.LastObservedID.String
	}
	if row.LastObservedAt.Valid {
		trust["lastObservedAt"] = row.LastObservedAt.String
	}
	current := map[string]any{}
	if currentTrust != nil {
		for key, raw := range currentTrust {
			var value any
			if json.Unmarshal(raw, &value) == nil {
				current[key] = value
			}
		}
	}
	for key, value := range trust {
		current[key] = value
	}
	summary["trustReport"] = mustMarshalJSONRaw(current)
	merged, err := json.Marshal(summary)
	if err == nil {
		detail.ResultSummary = merged
	}
}

func hasTrustReason(trust map[string]json.RawMessage, expected string) bool {
	if trust == nil {
		return false
	}
	raw, ok := trust["reasonCodes"]
	if !ok {
		return false
	}
	var reasons []string
	if json.Unmarshal(raw, &reasons) != nil {
		return false
	}
	for _, reason := range reasons {
		if reason == expected {
			return true
		}
	}
	return false
}

func hasTrustText(trust map[string]json.RawMessage, field string) bool {
	if trust == nil {
		return false
	}
	raw, ok := trust[field]
	if !ok {
		return false
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != ""
}

func parseTrustReasonCodes(value string) []string {
	var reasons []string
	if err := json.Unmarshal([]byte(value), &reasons); err != nil {
		return nil
	}
	result := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			result = append(result, reason)
		}
	}
	return result
}

func mustMarshalJSONRaw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

type runViewScanner interface {
	Scan(...any) error
}

const runViewColumns = `id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,target_name,target_owner_system_account_id,account_id,group_id,api_key_id,model,profile,trigger_kind,schedule_id,trusted_comparison_enabled,trusted_comparison_available,status,level,message,score,max_score,probe_set_version,started_at,trace_id,finished_at,duration_ms,error_code,error_message,created_at,updated_at`

func scanRunView(scanner runViewScanner, view *RunView) error {
	var trustedComparison, trustedComparisonAvailable int
	if err := scanner.Scan(
		&view.ID,
		&view.SystemAccountID,
		&view.ActorSystemAccountID,
		&view.ProviderCode,
		&view.TargetType,
		&view.TargetID,
		&view.TargetName,
		&view.TargetOwnerSystemAccountID,
		&view.AccountID,
		&view.GroupID,
		&view.APIKeyID,
		&view.Model,
		&view.Profile,
		&view.TriggerKind,
		&view.ScheduleID,
		&trustedComparison,
		&trustedComparisonAvailable,
		&view.Status,
		&view.Level,
		&view.Message,
		&view.Score,
		&view.MaxScore,
		&view.ProbeSetVersion,
		&view.StartedAt,
		&view.TraceID,
		&view.FinishedAt,
		&view.DurationMS,
		&view.ErrorCode,
		&view.ErrorMessage,
		&view.CreatedAt,
		&view.UpdatedAt,
	); err != nil {
		return err
	}
	view.TrustedComparison = trustedComparison != 0
	view.TrustedComparisonAvailable = trustedComparisonAvailable != 0
	return nil
}

func scanRunViewWithSummaries(scanner runViewScanner, view *RunView, requestSummary, resultSummary, policySnapshot, qualityDecision *string) error {
	var trustedComparison, trustedComparisonAvailable int
	if err := scanner.Scan(
		&view.ID, &view.SystemAccountID, &view.ActorSystemAccountID, &view.ProviderCode, &view.TargetType, &view.TargetID,
		&view.TargetName, &view.TargetOwnerSystemAccountID, &view.AccountID, &view.GroupID, &view.APIKeyID,
		&view.Model, &view.Profile, &view.TriggerKind, &view.ScheduleID, &trustedComparison, &trustedComparisonAvailable,
		&view.Status, &view.Level, &view.Message, &view.Score, &view.MaxScore, &view.ProbeSetVersion, &view.StartedAt,
		&view.TraceID, &view.FinishedAt, &view.DurationMS, &view.ErrorCode, &view.ErrorMessage, &view.CreatedAt, &view.UpdatedAt,
		requestSummary, resultSummary, policySnapshot, qualityDecision,
	); err != nil {
		return err
	}
	view.TrustedComparison = trustedComparison != 0
	view.TrustedComparisonAvailable = trustedComparisonAvailable != 0
	return nil
}

func (s *Runtime) readRunChecks(ctx context.Context, runID string) ([]RunCheck, error) {
	rows, err := s.Store.db.QueryContext(ctx, s.Store.bind(`SELECT id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at FROM `+s.Store.table("model_check_items")+` WHERE run_id=? ORDER BY created_at ASC,id ASC`), runID)
	if err != nil {
		return nil, fmt.Errorf("read J3b run checks: %w", err)
	}
	defer rows.Close()
	checks := make([]RunCheck, 0)
	for rows.Next() {
		var check RunCheck
		var evidence string
		if err := rows.Scan(&check.ID, &check.RunID, &check.ItemKey, &check.ItemType, &check.Status, &check.Score, &check.MaxScore, &check.DurationMS, &check.TraceID, &evidence, &check.ErrorCode, &check.ErrorMessage, &check.CreatedAt, &check.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan J3b run check: %w", err)
		}
		var err error
		if check.EvidenceSummary, err = requiredJSONObject("J3b check evidence", evidence); err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b run checks: %w", err)
	}
	return checks, nil
}

func requiredJSONObject(label, value string) (json.RawMessage, error) {
	parsed := json.RawMessage(strings.TrimSpace(value))
	if len(parsed) == 0 || !json.Valid(parsed) {
		return nil, fmt.Errorf("%s is invalid", label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parsed, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	return parsed, nil
}

var _ RunService = (*Runtime)(nil)
