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
	ID              string `json:"id"`
	SystemAccountID string `json:"systemAccountId"`
	ProviderCode    string `json:"providerCode"`
	TargetType      string `json:"targetType"`
	TargetID        string `json:"targetId"`
	Model           string `json:"model"`
	Profile         string `json:"profile"`
	TriggerKind     string `json:"triggerKind"`
	Status          string `json:"status"`
	Level           string `json:"level"`
	Message         string `json:"message"`
	Score           int    `json:"score"`
	MaxScore        int    `json:"maxScore"`
	CreatedAt       string `json:"createdAt"`
}

// RunDetail is built only from the durable J3b run/item projection. It does
// not manufacture a report from an in-memory execution result: an unreadable
// JSON projection causes GetRun to fail so the HTTP layer remains closed.
type RunDetail struct {
	RunView
	RequestSummary json.RawMessage `json:"requestSummary"`
	ResultSummary  json.RawMessage `json:"resultSummary"`
	Checks         []RunCheck      `json:"checks"`
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
	if s == nil || s.Store == nil || s.Store.db == nil || strings.TrimSpace(query.SystemAccountID) == "" {
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
	clauses := []string{"system_account_id=?"}
	args := []any{strings.TrimSpace(query.SystemAccountID)}
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
	statement := `SELECT id,system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,level,message,score,max_score,created_at FROM ` + s.Store.table("model_check_runs") + ` WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`
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
	var requestSummary, resultSummary string
	err := scanRunViewWithSummaries(s.Store.db.QueryRowContext(ctx, s.Store.bind(`SELECT id,system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,level,message,score,max_score,created_at,request_summary_json,result_summary_json FROM `+s.Store.table("model_check_runs")+` WHERE id=?`), strings.TrimSpace(runID)), &detail.RunView, &requestSummary, &resultSummary)
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
	checks, err := s.readRunChecks(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, false, err
	}
	detail.Checks = checks
	return detail, true, nil
}

type runViewScanner interface {
	Scan(...any) error
}

func scanRunView(scanner runViewScanner, view *RunView) error {
	return scanner.Scan(
		&view.ID,
		&view.SystemAccountID,
		&view.ProviderCode,
		&view.TargetType,
		&view.TargetID,
		&view.Model,
		&view.Profile,
		&view.TriggerKind,
		&view.Status,
		&view.Level,
		&view.Message,
		&view.Score,
		&view.MaxScore,
		&view.CreatedAt,
	)
}

func scanRunViewWithSummaries(scanner runViewScanner, view *RunView, requestSummary, resultSummary *string) error {
	return scanner.Scan(
		&view.ID,
		&view.SystemAccountID,
		&view.ProviderCode,
		&view.TargetType,
		&view.TargetID,
		&view.Model,
		&view.Profile,
		&view.TriggerKind,
		&view.Status,
		&view.Level,
		&view.Message,
		&view.Score,
		&view.MaxScore,
		&view.CreatedAt,
		requestSummary,
		resultSummary,
	)
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
