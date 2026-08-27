package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type RunListResult struct {
	Items []RunView `json:"items"`
}

type RunView struct {
	ID, SystemAccountID, TargetType, TargetID, Model, Profile, Status, Level, Message string
	Score, MaxScore                                                                   int
}

func (s *Runtime) ListRuns(ctx context.Context, query RunListQuery) (any, error) {
	if s == nil || s.Store == nil || strings.TrimSpace(query.SystemAccountID) == "" {
		return nil, errors.New("J3b runtime list scope is incomplete")
	}
	pageSize := query.PageSize
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 50
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	rows, err := s.Store.db.QueryContext(ctx, s.Store.bind(`SELECT id,system_account_id,target_type,target_id,model,profile,status,level,message,score,max_score FROM `+s.Store.table("model_check_runs")+` WHERE system_account_id=? ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`), query.SystemAccountID, pageSize, offset)
	if err != nil {
		return nil, fmt.Errorf("list J3b runs: %w", err)
	}
	defer rows.Close()
	result := RunListResult{Items: make([]RunView, 0)}
	for rows.Next() {
		var view RunView
		if err := rows.Scan(&view.ID, &view.SystemAccountID, &view.TargetType, &view.TargetID, &view.Model, &view.Profile, &view.Status, &view.Level, &view.Message, &view.Score, &view.MaxScore); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, view)
	}
	return result, rows.Err()
}

func (s *Runtime) GetRun(ctx context.Context, runID string) (any, bool, error) {
	if s == nil || s.Store == nil || strings.TrimSpace(runID) == "" {
		return nil, false, errors.New("J3b runtime run ID is required")
	}
	var view RunView
	err := s.Store.db.QueryRowContext(ctx, s.Store.bind(`SELECT id,system_account_id,target_type,target_id,model,profile,status,level,message,score,max_score FROM `+s.Store.table("model_check_runs")+` WHERE id=?`), runID).Scan(&view.ID, &view.SystemAccountID, &view.TargetType, &view.TargetID, &view.Model, &view.Profile, &view.Status, &view.Level, &view.Message, &view.Score, &view.MaxScore)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read J3b run: %w", err)
	}
	return view, true, nil
}

var _ RunService = (*Runtime)(nil)
