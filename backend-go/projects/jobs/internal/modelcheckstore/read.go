package modelcheckstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type RunListOptions struct {
	SystemAccountID string
	TargetType      string
	TargetID        string
	Model           string
	Level           string
	Status          string
	TriggerKind     string
	StartAt         string
	EndAt           string
	Page            int
	PageSize        int
}

type RunListItem struct {
	ID                string `json:"id"`
	SystemAccountID   string `json:"systemAccountId,omitempty"`
	ProviderCode      string `json:"providerCode"`
	TargetType        string `json:"targetType"`
	TargetID          string `json:"targetId"`
	TargetName        string `json:"targetName,omitempty"`
	Model             string `json:"model"`
	Profile           string `json:"profile"`
	TriggerKind       string `json:"triggerKind"`
	Level             string `json:"level"`
	Status            string `json:"status"`
	Message           string `json:"message"`
	TrustedComparison bool   `json:"trustedComparison"`
	Score             int    `json:"score"`
	MaxScore          int    `json:"maxScore"`
	DurationMS        *int64 `json:"durationMs,omitempty"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
	CreatedAt         string `json:"createdAt"`
}

type RunListResult struct {
	Items    []RunListItem `json:"items"`
	Total    int           `json:"total"`
	HasMore  bool          `json:"hasMore"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}

type RunDetail struct {
	RunListItem
	ActorSystemAccountID       string         `json:"actorSystemAccountId,omitempty"`
	TargetOwnerSystemAccountID string         `json:"targetOwnerSystemAccountId,omitempty"`
	AccountID                  string         `json:"accountId,omitempty"`
	GroupID                    string         `json:"groupId,omitempty"`
	APIKeyID                   string         `json:"apiKeyId,omitempty"`
	TrustedComparisonAvailable bool           `json:"trustedComparisonAvailable"`
	TraceID                    string         `json:"traceId,omitempty"`
	ProbeSetVersion            string         `json:"probeSetVersion"`
	StartedAt                  string         `json:"startedAt"`
	FinishedAt                 string         `json:"finishedAt,omitempty"`
	UpdatedAt                  string         `json:"updatedAt"`
	RequestSummary             map[string]any `json:"requestSummary"`
	ResultSummary              map[string]any `json:"resultSummary"`
	PolicySnapshot             map[string]any `json:"policySnapshot"`
	QualityDecision            map[string]any `json:"qualityDecision,omitempty"`
	Checks                     []ItemView     `json:"checks"`
}

type ItemView struct {
	ID              string         `json:"id"`
	RunID           string         `json:"runId"`
	ItemKey         string         `json:"itemKey"`
	ItemType        string         `json:"itemType"`
	Status          string         `json:"status"`
	Score           int            `json:"score"`
	MaxScore        int            `json:"maxScore"`
	DurationMS      *int64         `json:"durationMs,omitempty"`
	TraceID         string         `json:"traceId,omitempty"`
	EvidenceSummary map[string]any `json:"evidenceSummary"`
	ErrorCode       string         `json:"errorCode,omitempty"`
	ErrorMessage    string         `json:"errorMessage,omitempty"`
	CreatedAt       string         `json:"createdAt"`
	UpdatedAt       string         `json:"updatedAt"`
}

func (s *Store) ListRuns(ctx context.Context, options RunListOptions) (RunListResult, error) {
	if s == nil || s.db == nil {
		return RunListResult{}, errors.New("model check store is not initialized")
	}
	page, pageSize := options.Page, options.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		return RunListResult{}, errors.New("model check run page size is invalid")
	}
	clauses := []string{}
	args := []any{}
	add := func(column, value string) {
		if value = strings.TrimSpace(value); value != "" {
			clauses = append(clauses, column+"=?")
			args = append(args, value)
		}
	}
	add("system_account_id", options.SystemAccountID)
	if strings.TrimSpace(options.TargetType) == "account" {
		add("target_type", "account")
	}
	add("target_id", options.TargetID)
	if model := strings.TrimSpace(options.Model); supportedModel(model) {
		add("model", model)
	}
	if level := strings.TrimSpace(options.Level); validLevel(level) {
		add("level", level)
	}
	if status := strings.TrimSpace(options.Status); validStatus(status) {
		add("status", status)
	}
	if trigger := strings.TrimSpace(options.TriggerKind); validTrigger(trigger) {
		add("trigger_kind", trigger)
	}
	if value := strings.TrimSpace(options.StartAt); value != "" {
		clauses = append(clauses, "created_at>=?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(options.EndAt); value != "" {
		clauses = append(clauses, "created_at<=?")
		args = append(args, value)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	query := "SELECT id,system_account_id,provider_code,target_type,target_id,target_name,model,profile,trigger_kind,trusted_comparison_enabled,level,score,max_score,status,message,duration_ms,error_message,created_at FROM model_check_runs" + where + " ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return RunListResult{}, fmt.Errorf("list model check runs: %w", err)
	}
	defer rows.Close()
	items := make([]RunListItem, 0, pageSize)
	for rows.Next() {
		var item RunListItem
		var targetName, errorMessage sql.NullString
		var trusted int
		var duration sql.NullInt64
		var created any
		if err := rows.Scan(&item.ID, &item.SystemAccountID, &item.ProviderCode, &item.TargetType, &item.TargetID, &targetName, &item.Model, &item.Profile, &item.TriggerKind, &trusted, &item.Level, &item.Score, &item.MaxScore, &item.Status, &item.Message, &duration, &errorMessage, &created); err != nil {
			return RunListResult{}, err
		}
		item.TargetName = targetName.String
		item.TrustedComparison = trusted != 0
		if duration.Valid {
			item.DurationMS = &duration.Int64
		}
		item.ErrorMessage = errorMessage.String
		item.CreatedAt, err = s.readTimestamp(created)
		if err != nil {
			return RunListResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RunListResult{}, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	// Node 的 pagedTotalUpperBound 只返回当前页可证明的上界，不执行 COUNT(*)。
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return RunListResult{Items: items, Total: total, HasMore: hasMore, Page: page, PageSize: pageSize}, nil
}

func (s *Store) GetRun(ctx context.Context, runID, systemAccountID string) (RunDetail, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(runID) == "" {
		return RunDetail{}, false, errors.New("model check run ID is required")
	}
	where := "id=?"
	args := []any{strings.TrimSpace(runID)}
	if strings.TrimSpace(systemAccountID) != "" {
		where += " AND system_account_id=?"
		args = append(args, strings.TrimSpace(systemAccountID))
	}
	query := "SELECT id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,target_name,target_owner_system_account_id,account_id,group_id,api_key_id,model,profile,trigger_kind,trusted_comparison_enabled,trusted_comparison_available,level,score,max_score,status,message,trace_id,probe_set_version,started_at,finished_at,duration_ms,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,error_message,created_at,updated_at FROM model_check_runs WHERE " + where + " LIMIT 1"
	var d RunDetail
	var targetName, targetOwner, accountID, groupID, apiKeyID, traceID, finished, errMessage sql.NullString
	var trusted, trustedAvailable int
	var duration sql.NullInt64
	var started, created, updated any
	var requestJSON, resultJSON, policyJSON, decisionJSON []byte
	if err := s.db.QueryRowContext(ctx, s.bind(query), args...).Scan(&d.ID, &d.SystemAccountID, &d.ActorSystemAccountID, &d.ProviderCode, &d.TargetType, &d.TargetID, &targetName, &targetOwner, &accountID, &groupID, &apiKeyID, &d.Model, &d.Profile, &d.TriggerKind, &trusted, &trustedAvailable, &d.Level, &d.Score, &d.MaxScore, &d.Status, &d.Message, &traceID, &d.ProbeSetVersion, &started, &finished, &duration, &requestJSON, &resultJSON, &policyJSON, &decisionJSON, &errMessage, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunDetail{}, false, nil
		}
		return RunDetail{}, false, fmt.Errorf("get model check run: %w", err)
	}
	d.TargetName = targetName.String
	d.TargetOwnerSystemAccountID = targetOwner.String
	d.AccountID = accountID.String
	d.GroupID = groupID.String
	d.APIKeyID = apiKeyID.String
	d.TrustedComparison = trusted != 0
	d.TrustedComparisonAvailable = trustedAvailable != 0
	d.TraceID = traceID.String
	d.FinishedAt = finished.String
	d.ErrorMessage = errMessage.String
	if duration.Valid {
		d.DurationMS = &duration.Int64
	}
	var err error
	d.StartedAt, err = s.readTimestamp(started)
	if err != nil {
		return RunDetail{}, false, err
	}
	d.CreatedAt, err = s.readTimestamp(created)
	if err != nil {
		return RunDetail{}, false, err
	}
	d.UpdatedAt, err = s.readTimestamp(updated)
	if err != nil {
		return RunDetail{}, false, err
	}
	if d.RequestSummary, err = decodeObject(requestJSON); err != nil {
		return RunDetail{}, false, err
	}
	if d.ResultSummary, err = decodeObject(resultJSON); err != nil {
		return RunDetail{}, false, err
	}
	if d.PolicySnapshot, err = decodeObject(policyJSON); err != nil {
		return RunDetail{}, false, err
	}
	if d.QualityDecision, err = decodeObject(decisionJSON); err != nil {
		return RunDetail{}, false, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind("SELECT id,run_id,item_key,item_type,status,score,max_score,duration_ms,trace_id,evidence_summary_json,error_code,error_message,created_at,updated_at FROM model_check_items WHERE run_id=? ORDER BY created_at ASC,id ASC"), runID)
	if err != nil {
		return RunDetail{}, false, fmt.Errorf("list model check items: %w", err)
	}
	defer rows.Close()
	d.Checks = []ItemView{}
	for rows.Next() {
		var item ItemView
		var trace, errorCode, errorMsg sql.NullString
		var duration sql.NullInt64
		var evidence []byte
		var createdAt, updatedAt any
		if err := rows.Scan(&item.ID, &item.RunID, &item.ItemKey, &item.ItemType, &item.Status, &item.Score, &item.MaxScore, &duration, &trace, &evidence, &errorCode, &errorMsg, &createdAt, &updatedAt); err != nil {
			return RunDetail{}, false, err
		}
		item.TraceID = trace.String
		item.ErrorCode = errorCode.String
		item.ErrorMessage = errorMsg.String
		if duration.Valid {
			item.DurationMS = &duration.Int64
		}
		if item.EvidenceSummary, err = decodeObject(evidence); err != nil {
			return RunDetail{}, false, err
		}
		item.CreatedAt, err = s.readTimestamp(createdAt)
		if err != nil {
			return RunDetail{}, false, err
		}
		item.UpdatedAt, err = s.readTimestamp(updatedAt)
		if err != nil {
			return RunDetail{}, false, err
		}
		d.Checks = append(d.Checks, item)
	}
	if err := rows.Err(); err != nil {
		return RunDetail{}, false, err
	}
	return d, true, nil
}

func (s *Store) readTimestamp(raw any) (string, error) {
	switch value := raw.(type) {
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano), nil
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", errors.New("invalid model check timestamp")
	}
}
func decodeObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode model check JSON: %w", err)
	}
	if value == nil {
		return map[string]any{}, nil
	}
	return value, nil
}

func supportedModel(value string) bool {
	for _, candidate := range modelcheckprofile.SupportedModels() {
		if candidate == value {
			return true
		}
	}
	return false
}

func validLevel(value string) bool {
	switch value {
	case "high_confidence", "likely", "uncertain", "suspicious", "unavailable":
		return true
	default:
		return false
	}
}

func validStatus(value string) bool {
	switch value {
	case "running", "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

func validTrigger(value string) bool {
	switch value {
	case "manual", "scheduled", "quality_recovery":
		return true
	default:
		return false
	}
}
