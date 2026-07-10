package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultOperationLogMode            = "self"
	defaultOperationLogDetailLevel     = "full"
	defaultOperationLogVisibilityScope = "targeted"
	defaultOperationLogTargetRelation  = "primary"
	operationLogActorSelfViewerReason  = "actor_self"
	operationLogResourceOwnerReason    = "resource_owner"
	operationLogAdminManagedReason     = "admin_managed_my_resource"
	operationLogSearchMinTermRunes     = 2
	operationLogSearchMaxTermRunes     = 128
	maxOperationLogSearchTerms         = 1500
	maxOperationLogSearchTermRunes     = 256
	maxOperationLogSearchGramRunes     = 128
	maxOperationLogReadRows            = 1001
	maxOperationLogNameLookupIDs       = 900
)

func (s *Store) InsertOperationLog(ctx context.Context, input port.OperationLogInput) error {
	normalized, err := normalizeOperationLogInput(input)
	if err != nil {
		return err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin operation log tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	q := s.queries().WithTx(tx)
	insertedID, err := q.InsertOperationLog(ctx, insertOperationLogParams(normalized))
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit duplicate operation log tx: %w", err)
		}
		committed = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("insert operation log: %w", err)
	}

	for _, target := range operationLogTargets(normalized) {
		if err := q.InsertOperationLogTarget(ctx, postgresqueries.InsertOperationLogTargetParams{
			ID:                         "oplogtgt_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			OperationLogID:             insertedID,
			TargetType:                 strings.TrimSpace(target.TargetType),
			TargetID:                   pgText(strings.TrimSpace(target.TargetID)),
			TargetName:                 pgText(strings.TrimSpace(target.TargetName)),
			TargetOwnerSystemAccountID: pgText(strings.TrimSpace(target.TargetOwnerSystemAccountID)),
			Relation:                   defaultText(target.Relation, defaultOperationLogTargetRelation),
			CreatedAt:                  pgTimestamptz(normalized.CreatedAt),
		}); err != nil {
			return fmt.Errorf("insert operation log target: %w", err)
		}
	}

	for _, viewer := range operationLogViewers(normalized) {
		if err := q.InsertOperationLogViewer(ctx, postgresqueries.InsertOperationLogViewerParams{
			OperationLogID:   insertedID,
			SystemAccountID:  strings.TrimSpace(viewer.SystemAccountID),
			VisibilityReason: defaultText(viewer.VisibilityReason, operationLogResourceOwnerReason),
			DetailLevel:      defaultText(viewer.DetailLevel, normalized.DetailLevel),
			CreatedAt:        pgTimestamptz(normalized.CreatedAt),
		}); err != nil {
			return fmt.Errorf("insert operation log viewer: %w", err)
		}
	}

	if terms := operationLogSummarySearchTerms(normalized.Summary); len(terms) > 0 {
		if err := q.InsertOperationLogSearchTerms(ctx, postgresqueries.InsertOperationLogSearchTermsParams{
			OperationLogID: insertedID,
			CreatedAt:      pgTimestamptz(normalized.CreatedAt),
			Terms:          terms,
		}); err != nil {
			return fmt.Errorf("insert operation log search terms: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("commit operation log tx rolled back: %w", err)
		}
		return fmt.Errorf("commit operation log tx: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) ListOperationLogs(ctx context.Context, input port.OperationLogListInput) (port.OperationLogListResult, error) {
	limit := operationLogReadLimit(input.Limit)
	searchTerm, hasSearch, invalidSearch := operationLogSearchTermFromKeyword(input.SummaryKeyword)
	if invalidSearch {
		return port.OperationLogListResult{}, nil
	}

	q := s.queries()
	params := operationLogListParams(input, limit, input.Offset)
	var (
		rows []postgresqueries.JuheDatasetOperationLog
		err  error
	)
	if hasSearch {
		rows, err = q.ListOperationLogsBySummarySearch(ctx, postgresqueries.ListOperationLogsBySummarySearchParams{
			SearchTerm:                    searchTerm,
			TraceID:                       params.TraceID,
			TraceIDUpper:                  params.TraceIDUpper,
			Module:                        params.Module,
			Action:                        params.Action,
			ResourceType:                  params.ResourceType,
			ResourceID:                    params.ResourceID,
			ActorSystemAccountID:          params.ActorSystemAccountID,
			OperationScopeSystemAccountID: params.OperationScopeSystemAccountID,
			AffectedSystemAccountID:       params.AffectedSystemAccountID,
			StartAt:                       params.StartAt,
			EndAt:                         params.EndAt,
			RowOffset:                     params.RowOffset,
			RowLimit:                      params.RowLimit,
		})
	} else {
		rows, err = q.ListOperationLogs(ctx, params)
	}
	if err != nil {
		return port.OperationLogListResult{}, fmt.Errorf("list operation logs: %w", err)
	}
	return s.operationLogListResult(ctx, rows, limit, "")
}

func (s *Store) ListVisibleOperationLogs(ctx context.Context, input port.OperationLogVisibleListInput) (port.OperationLogListResult, error) {
	viewerSystemAccountID := strings.TrimSpace(input.ViewerSystemAccountID)
	if viewerSystemAccountID == "" {
		return port.OperationLogListResult{}, nil
	}
	limit := operationLogReadLimit(input.List.Limit)
	rowWindowLimit := operationLogReadLimit(input.List.Offset + limit)
	searchTerm, hasSearch, invalidSearch := operationLogSearchTermFromKeyword(input.List.SummaryKeyword)
	if invalidSearch {
		return port.OperationLogListResult{}, nil
	}

	q := s.queries()
	visibleParams := operationLogVisibleListParams(input.List, rowWindowLimit)
	var (
		targetedRows []postgresqueries.JuheDatasetOperationLog
		allUsersRows []postgresqueries.JuheDatasetOperationLog
		err          error
	)
	if hasSearch {
		targetedRows, err = q.ListVisibleTargetedOperationLogsBySummarySearch(ctx, postgresqueries.ListVisibleTargetedOperationLogsBySummarySearchParams{
			SystemAccountID: viewerSystemAccountID,
			SearchTerm:      searchTerm,
			TraceID:         visibleParams.TraceID,
			TraceIDUpper:    visibleParams.TraceIDUpper,
			Module:          visibleParams.Module,
			Action:          visibleParams.Action,
			ResourceType:    visibleParams.ResourceType,
			ResourceID:      visibleParams.ResourceID,
			StartAt:         visibleParams.StartAt,
			EndAt:           visibleParams.EndAt,
			RowLimit:        visibleParams.RowLimit,
		})
		if err != nil {
			return port.OperationLogListResult{}, fmt.Errorf("list visible targeted operation logs: %w", err)
		}
		allUsersRows, err = q.ListVisibleAllUsersOperationLogsBySummarySearch(ctx, postgresqueries.ListVisibleAllUsersOperationLogsBySummarySearchParams{
			SearchTerm:   searchTerm,
			TraceID:      visibleParams.TraceID,
			TraceIDUpper: visibleParams.TraceIDUpper,
			Module:       visibleParams.Module,
			Action:       visibleParams.Action,
			ResourceType: visibleParams.ResourceType,
			ResourceID:   visibleParams.ResourceID,
			StartAt:      visibleParams.StartAt,
			EndAt:        visibleParams.EndAt,
			RowLimit:     visibleParams.RowLimit,
		})
	} else {
		targetedRows, err = q.ListVisibleTargetedOperationLogs(ctx, postgresqueries.ListVisibleTargetedOperationLogsParams{
			SystemAccountID: viewerSystemAccountID,
			TraceID:         visibleParams.TraceID,
			TraceIDUpper:    visibleParams.TraceIDUpper,
			Module:          visibleParams.Module,
			Action:          visibleParams.Action,
			ResourceType:    visibleParams.ResourceType,
			ResourceID:      visibleParams.ResourceID,
			StartAt:         visibleParams.StartAt,
			EndAt:           visibleParams.EndAt,
			RowLimit:        visibleParams.RowLimit,
		})
		if err != nil {
			return port.OperationLogListResult{}, fmt.Errorf("list visible targeted operation logs: %w", err)
		}
		allUsersRows, err = q.ListVisibleAllUsersOperationLogs(ctx, visibleParams)
	}
	if err != nil {
		return port.OperationLogListResult{}, fmt.Errorf("list visible all-users operation logs: %w", err)
	}

	rows := mergeOperationLogRowsByCreatedAt(targetedRows, allUsersRows, rowWindowLimit)
	offset := max(0, input.List.Offset)
	if offset > len(rows) {
		rows = nil
	} else {
		rows = rows[offset:]
	}
	return s.operationLogListResult(ctx, rows, limit, viewerSystemAccountID)
}

func (s *Store) GetOperationLogDetail(ctx context.Context, input port.OperationLogDetailInput) (port.OperationLogDetail, bool, error) {
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return port.OperationLogDetail{}, false, nil
	}
	viewerSystemAccountID := strings.TrimSpace(input.ViewerSystemAccountID)
	q := s.queries()
	var (
		row postgresqueries.JuheDatasetOperationLog
		err error
	)
	if viewerSystemAccountID == "" {
		row, err = q.GetOperationLogDetail(ctx, id)
	} else {
		row, err = q.GetVisibleOperationLogDetail(ctx, postgresqueries.GetVisibleOperationLogDetailParams{
			ID:              id,
			SystemAccountID: viewerSystemAccountID,
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return port.OperationLogDetail{}, false, nil
	}
	if err != nil {
		return port.OperationLogDetail{}, false, fmt.Errorf("get operation log detail: %w", err)
	}

	targetRows, err := q.ListOperationLogTargets(ctx, row.ID)
	if err != nil {
		return port.OperationLogDetail{}, false, fmt.Errorf("list operation log targets: %w", err)
	}
	viewerRows, err := q.ListOperationLogViewers(ctx, row.ID)
	if err != nil {
		return port.OperationLogDetail{}, false, fmt.Errorf("list operation log viewers: %w", err)
	}

	nameIDs := operationLogNameIDsFromDetail(row, targetRows, viewerRows)
	names, err := s.operationLogSystemAccountNames(ctx, nameIDs)
	if err != nil {
		return port.OperationLogDetail{}, false, err
	}
	summary, err := operationLogSummaryFromRow(row, names, true)
	if err != nil {
		return port.OperationLogDetail{}, false, err
	}
	if viewerSystemAccountID != "" {
		detailLevel, err := q.GetOperationLogViewerDetailLevel(ctx, postgresqueries.GetOperationLogViewerDetailLevelParams{
			OperationLogID:  row.ID,
			SystemAccountID: viewerSystemAccountID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			detailLevel = ""
		} else if err != nil {
			return port.OperationLogDetail{}, false, fmt.Errorf("get operation log viewer detail level: %w", err)
		}
		summary.ViewerDetailLevel = detailLevel
	}

	return port.OperationLogDetail{
		Summary: summary,
		Targets: operationLogTargetsFromRows(targetRows, names),
		Viewers: operationLogViewersFromRows(viewerRows, names),
	}, true, nil
}

func (s *Store) GetOperationLogRetentionDays(ctx context.Context) (int, bool, error) {
	raw, err := s.queries().GetOperationLogRetentionDays(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("读取操作日志保留天数失败: %w", err)
	}
	var value int
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, false, fmt.Errorf("operationLogRetentionDays JSON 无效: %w", err)
	}
	return value, true, nil
}

func (s *Store) OperationLogMaxChangesPerRecord(ctx context.Context) (int, error) {
	raw, err := s.queries().GetOperationLogMaxChangesPerRecord(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("系统设置缺少字段：operationLogMaxChangesPerRecord")
	}
	if err != nil {
		return 0, fmt.Errorf("读取操作日志最大变更数失败: %w", err)
	}
	return parseOperationLogMaxChangesPerRecord(raw)
}

func parseOperationLogMaxChangesPerRecord(raw string) (int, error) {
	var value int
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, fmt.Errorf("operationLogMaxChangesPerRecord JSON 无效: %w", err)
	}
	if value < 1 || value > 500 {
		return 0, fmt.Errorf("系统设置 operationLogMaxChangesPerRecord 必须在 1 到 500 之间")
	}
	return value, nil
}

func (s *Store) CleanupOperationLogsBefore(ctx context.Context, input port.OperationLogCleanupInput) (int64, error) {
	cutoff := input.CutoffCreatedAt
	if cutoff.IsZero() {
		return 0, fmt.Errorf("操作日志保留清理 cutoff_created_at 不能为空")
	}
	limit := input.Limit
	if limit <= 0 {
		return 0, fmt.Errorf("操作日志保留清理 limit 必须大于 0")
	}
	deleted, err := s.queries().CleanupOperationLogsBefore(ctx, postgresqueries.CleanupOperationLogsBeforeParams{
		CutoffCreatedAt: pgTimestamptz(cutoff.UTC()),
		RowLimit:        int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("按保留期清理操作日志失败: %w", err)
	}
	return deleted, nil
}

func normalizeOperationLogInput(input port.OperationLogInput) (port.OperationLogInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.TraceID = strings.TrimSpace(input.TraceID)
	input.ActorSystemAccountID = strings.TrimSpace(input.ActorSystemAccountID)
	input.ActorUsername = strings.TrimSpace(input.ActorUsername)
	input.ActorDisplayName = strings.TrimSpace(input.ActorDisplayName)
	input.ActorRole = strings.TrimSpace(input.ActorRole)
	input.OperationScopeSystemAccountID = strings.TrimSpace(input.OperationScopeSystemAccountID)
	input.Mode = defaultText(input.Mode, defaultOperationLogMode)
	input.Module = strings.TrimSpace(input.Module)
	input.Action = strings.TrimSpace(input.Action)
	input.OperationKey = strings.TrimSpace(input.OperationKey)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.ResourceName = strings.TrimSpace(input.ResourceName)
	input.Summary = strings.TrimSpace(input.Summary)
	input.DetailLevel = defaultText(input.DetailLevel, defaultOperationLogDetailLevel)
	input.VisibilityScope = defaultText(input.VisibilityScope, defaultOperationLogVisibilityScope)
	input.Method = strings.TrimSpace(input.Method)
	input.Path = strings.TrimSpace(input.Path)
	input.ClientIP = strings.TrimSpace(input.ClientIP)
	input.UserAgent = strings.TrimSpace(input.UserAgent)
	input.CreatedAt = input.CreatedAt.UTC()

	if input.ID == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log id is required")
	}
	if input.ActorSystemAccountID == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log actor_system_account_id is required")
	}
	if input.ActorRole == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log actor_role is required")
	}
	if input.Module == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log module is required")
	}
	if input.Action == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log action is required")
	}
	if input.OperationKey == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log operation_key is required")
	}
	if input.ResourceType == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log resource_type is required")
	}
	if input.Summary == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log summary is required")
	}
	if input.CreatedAt.IsZero() {
		return port.OperationLogInput{}, fmt.Errorf("operation log created_at is required")
	}
	return input, nil
}

func insertOperationLogParams(input port.OperationLogInput) postgresqueries.InsertOperationLogParams {
	return postgresqueries.InsertOperationLogParams{
		ID:                            input.ID,
		TraceID:                       pgText(input.TraceID),
		ActorSystemAccountID:          input.ActorSystemAccountID,
		ActorUsername:                 pgText(input.ActorUsername),
		ActorDisplayName:              pgText(input.ActorDisplayName),
		ActorRole:                     input.ActorRole,
		OperationScopeSystemAccountID: pgText(input.OperationScopeSystemAccountID),
		Mode:                          input.Mode,
		Module:                        input.Module,
		Action:                        input.Action,
		OperationKey:                  input.OperationKey,
		ResourceType:                  input.ResourceType,
		ResourceID:                    pgText(input.ResourceID),
		ResourceName:                  pgText(input.ResourceName),
		Summary:                       input.Summary,
		DetailLevel:                   input.DetailLevel,
		VisibilityScope:               input.VisibilityScope,
		ChangesJson:                   safeOperationLogChangesJSONString(input.Changes),
		MetadataJson:                  safeJSONObjectString(input.Metadata),
		Method:                        pgText(input.Method),
		Path:                          pgText(input.Path),
		StatusCode:                    pgInt4Ptr(input.StatusCode),
		ClientIp:                      pgText(input.ClientIP),
		UserAgent:                     pgText(input.UserAgent),
		CreatedAt:                     pgTimestamptz(input.CreatedAt),
	}
}

func operationLogTargets(input port.OperationLogInput) []port.OperationLogTargetInput {
	targets := make([]port.OperationLogTargetInput, 0, len(input.Targets)+1)
	hasPrimary := false
	for _, target := range input.Targets {
		target.TargetType = strings.TrimSpace(target.TargetType)
		target.Relation = defaultText(target.Relation, defaultOperationLogTargetRelation)
		if target.TargetType == "" {
			continue
		}
		if target.Relation == defaultOperationLogTargetRelation {
			hasPrimary = true
		}
		targets = append(targets, target)
	}
	if !hasPrimary {
		targets = append(targets, port.OperationLogTargetInput{
			TargetType:                 input.ResourceType,
			TargetID:                   input.ResourceID,
			TargetName:                 input.ResourceName,
			TargetOwnerSystemAccountID: input.OperationScopeSystemAccountID,
			Relation:                   defaultOperationLogTargetRelation,
		})
	}
	return targets
}

func operationLogViewers(input port.OperationLogInput) []port.OperationLogViewerInput {
	if input.VisibilityScope == "admin_only" || input.VisibilityScope == "all_users" {
		return nil
	}
	viewers := make([]port.OperationLogViewerInput, 0, len(input.Viewers)+2)
	hasSystemAccount := map[string]bool{}
	seen := map[string]struct{}{}
	add := func(viewer port.OperationLogViewerInput) {
		viewer.SystemAccountID = strings.TrimSpace(viewer.SystemAccountID)
		viewer.VisibilityReason = defaultText(viewer.VisibilityReason, operationLogResourceOwnerReason)
		viewer.DetailLevel = defaultText(viewer.DetailLevel, input.DetailLevel)
		if viewer.SystemAccountID == "" {
			return
		}
		key := viewer.SystemAccountID + "\x00" + viewer.VisibilityReason + "\x00" + viewer.DetailLevel
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		hasSystemAccount[viewer.SystemAccountID] = true
		viewers = append(viewers, viewer)
	}
	for _, viewer := range input.Viewers {
		add(viewer)
	}
	add(port.OperationLogViewerInput{
		SystemAccountID:  input.ActorSystemAccountID,
		VisibilityReason: operationLogActorSelfViewerReason,
		DetailLevel:      input.DetailLevel,
	})
	if input.OperationScopeSystemAccountID != "" &&
		input.OperationScopeSystemAccountID != input.ActorSystemAccountID &&
		!hasSystemAccount[input.OperationScopeSystemAccountID] {
		reason := operationLogResourceOwnerReason
		if isOperationLogAdminRole(input.ActorRole) {
			reason = operationLogAdminManagedReason
		}
		add(port.OperationLogViewerInput{
			SystemAccountID:  input.OperationScopeSystemAccountID,
			VisibilityReason: reason,
			DetailLevel:      input.DetailLevel,
		})
	}
	return viewers
}

func safeOperationLogChangesJSONString(value []port.OperationLogChange) string {
	if value == nil {
		return "[]"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func operationLogSummarySearchTerms(summary string) []string {
	normalized := strings.ToLower(strings.TrimSpace(norm.NFKC.String(summary)))
	if normalized == "" {
		return nil
	}
	parts := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	compact := strings.Join(parts, "")
	terms := make([]string, 0, min(maxOperationLogSearchTerms, 128))
	seen := map[string]struct{}{}
	add := func(value string) bool {
		term := truncateRunes(strings.TrimSpace(value), maxOperationLogSearchTermRunes)
		if term == "" {
			return len(terms) >= maxOperationLogSearchTerms
		}
		if _, exists := seen[term]; exists {
			return len(terms) >= maxOperationLogSearchTerms
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		return len(terms) >= maxOperationLogSearchTerms
	}
	if add(normalized) || add(compact) {
		return terms
	}
	for _, part := range parts {
		if add(part) {
			return terms
		}
	}
	for _, source := range append([]string{compact}, parts...) {
		runes := []rune(source)
		maxGramLength := min(maxOperationLogSearchGramRunes, len(runes))
		for size := 2; size <= maxGramLength; size++ {
			for start := 0; start+size <= len(runes); start++ {
				if add(string(runes[start : start+size])) {
					return terms
				}
			}
		}
	}
	return terms
}

func operationLogSearchTermFromKeyword(keyword string) (string, bool, bool) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "", false, false
	}
	for _, term := range operationLogSummarySearchTerms(keyword) {
		runeCount := len([]rune(term))
		if runeCount >= operationLogSearchMinTermRunes && runeCount <= operationLogSearchMaxTermRunes {
			return term, true, false
		}
	}
	return "", false, true
}

func operationLogReadLimit(limit int) int {
	if limit <= 1 {
		return 101
	}
	return min(limit, maxOperationLogReadRows)
}

func operationLogListParams(input port.OperationLogListInput, limit int, offset int) postgresqueries.ListOperationLogsParams {
	startAt, endAt := operationLogTimeRange(input.StartAt, input.EndAt)
	traceID := strings.TrimSpace(input.TraceID)
	return postgresqueries.ListOperationLogsParams{
		TraceID:                       traceID,
		TraceIDUpper:                  operationLogTraceUpper(traceID),
		Module:                        operationLogFilterText(input.Module),
		Action:                        operationLogFilterText(input.Action),
		ResourceType:                  operationLogFilterText(input.ResourceType),
		ResourceID:                    operationLogFilterText(input.ResourceID),
		ActorSystemAccountID:          operationLogFilterText(input.ActorSystemAccountID),
		OperationScopeSystemAccountID: operationLogFilterText(input.OperationScopeSystemAccountID),
		AffectedSystemAccountID:       operationLogFilterText(input.AffectedSystemAccountID),
		StartAt:                       pgTimestamptz(startAt),
		EndAt:                         pgTimestamptz(endAt),
		RowOffset:                     int32(max(0, offset)),
		RowLimit:                      int32(limit),
	}
}

func operationLogVisibleListParams(input port.OperationLogListInput, limit int) postgresqueries.ListVisibleAllUsersOperationLogsParams {
	startAt, endAt := operationLogTimeRange(input.StartAt, input.EndAt)
	traceID := strings.TrimSpace(input.TraceID)
	return postgresqueries.ListVisibleAllUsersOperationLogsParams{
		TraceID:      traceID,
		TraceIDUpper: operationLogTraceUpper(traceID),
		Module:       operationLogFilterText(input.Module),
		Action:       operationLogFilterText(input.Action),
		ResourceType: operationLogFilterText(input.ResourceType),
		ResourceID:   operationLogFilterText(input.ResourceID),
		StartAt:      pgTimestamptz(startAt),
		EndAt:        pgTimestamptz(endAt),
		RowLimit:     int32(limit),
	}
}

func operationLogTimeRange(startAt time.Time, endAt time.Time) (time.Time, time.Time) {
	if !startAt.IsZero() {
		startAt = startAt.UTC()
	}
	if !endAt.IsZero() {
		endAt = endAt.UTC()
	}
	if !startAt.IsZero() && !endAt.IsZero() && startAt.After(endAt) {
		return endAt, startAt
	}
	return startAt, endAt
}

func operationLogTraceUpper(traceID string) string {
	if traceID == "" {
		return ""
	}
	return textPrefixUpperBound(traceID)
}

func operationLogFilterText(value string) string {
	text := strings.TrimSpace(value)
	if text == "all" {
		return ""
	}
	return text
}

func (s *Store) operationLogListResult(ctx context.Context, rows []postgresqueries.JuheDatasetOperationLog, limit int, viewerSystemAccountID string) (port.OperationLogListResult, error) {
	pageSize := max(0, limit-1)
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items, err := s.operationLogSummariesFromRows(ctx, rows, viewerSystemAccountID)
	if err != nil {
		return port.OperationLogListResult{}, err
	}
	return port.OperationLogListResult{Items: items, HasMore: hasMore}, nil
}

func (s *Store) operationLogSummariesFromRows(ctx context.Context, rows []postgresqueries.JuheDatasetOperationLog, viewerSystemAccountID string) ([]port.OperationLogSummary, error) {
	if len(rows) == 0 {
		return []port.OperationLogSummary{}, nil
	}
	names, err := s.operationLogSystemAccountNames(ctx, operationLogNameIDsFromRows(rows))
	if err != nil {
		return nil, err
	}
	viewerLevels := map[string]string{}
	if strings.TrimSpace(viewerSystemAccountID) != "" {
		viewerLevels, err = s.operationLogViewerDetailLevels(ctx, rows, viewerSystemAccountID)
		if err != nil {
			return nil, err
		}
	}
	items := make([]port.OperationLogSummary, 0, len(rows))
	for _, row := range rows {
		item, err := operationLogSummaryFromRow(row, names, false)
		if err != nil {
			return nil, err
		}
		item.ViewerDetailLevel = viewerLevels[item.ID]
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) operationLogViewerDetailLevels(ctx context.Context, rows []postgresqueries.JuheDatasetOperationLog, viewerSystemAccountID string) (map[string]string, error) {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	ids = uniqueStrings(ids, maxOperationLogNameLookupIDs)
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	levelRows, err := s.queries().ListOperationLogViewerDetailLevels(ctx, postgresqueries.ListOperationLogViewerDetailLevelsParams{
		SystemAccountID: strings.TrimSpace(viewerSystemAccountID),
		OperationLogIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("list operation log viewer detail levels: %w", err)
	}
	levels := make(map[string]string, len(levelRows))
	for _, row := range levelRows {
		current := levels[row.OperationLogID]
		if current == "full" {
			continue
		}
		if row.DetailLevel == "summary" {
			levels[row.OperationLogID] = "summary"
			continue
		}
		levels[row.OperationLogID] = "full"
	}
	return levels, nil
}

func (s *Store) operationLogSystemAccountNames(ctx context.Context, ids []string) (map[string]string, error) {
	ids = uniqueStrings(ids, maxOperationLogNameLookupIDs)
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	rows, err := s.queries().ListManagementSystemAccountOptions(ctx, postgresqueries.ListManagementSystemAccountOptionsParams{
		HasIds:   true,
		Ids:      ids,
		RowLimit: int32(len(ids)),
	})
	if err != nil {
		return nil, fmt.Errorf("list operation log system account names: %w", err)
	}
	names := make(map[string]string, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.DisplayName)
		if name == "" {
			name = strings.TrimSpace(row.Username)
		}
		if name == "" {
			name = row.ID
		}
		names[row.ID] = name
	}
	return names, nil
}

func operationLogSummaryFromRow(row postgresqueries.JuheDatasetOperationLog, names map[string]string, includePayload bool) (port.OperationLogSummary, error) {
	changes := []port.OperationLogChange{}
	metadata := map[string]any{}
	var err error
	if includePayload {
		changes, err = parseOperationLogChangesJSON(row.ChangesJson)
		if err != nil {
			return port.OperationLogSummary{}, err
		}
		metadata, err = parseOperationLogMetadataJSON(row.MetadataJson)
		if err != nil {
			return port.OperationLogSummary{}, err
		}
	}
	operationScopeSystemAccountID := providerTextValue(row.OperationScopeSystemAccountID)
	return port.OperationLogSummary{
		ID:                              row.ID,
		TraceID:                         providerTextValue(row.TraceID),
		ActorSystemAccountID:            row.ActorSystemAccountID,
		ActorUsername:                   providerTextValue(row.ActorUsername),
		ActorDisplayName:                providerTextValue(row.ActorDisplayName),
		ActorSystemAccountName:          names[row.ActorSystemAccountID],
		ActorRole:                       row.ActorRole,
		OperationScopeSystemAccountID:   operationScopeSystemAccountID,
		OperationScopeSystemAccountName: names[operationScopeSystemAccountID],
		Mode:                            row.Mode,
		Module:                          row.Module,
		Action:                          row.Action,
		OperationKey:                    row.OperationKey,
		ResourceType:                    row.ResourceType,
		ResourceID:                      providerTextValue(row.ResourceID),
		ResourceName:                    providerTextValue(row.ResourceName),
		Summary:                         row.Summary,
		DetailLevel:                     row.DetailLevel,
		VisibilityScope:                 row.VisibilityScope,
		Changes:                         changes,
		Metadata:                        metadata,
		Method:                          providerTextValue(row.Method),
		Path:                            providerTextValue(row.Path),
		StatusCode:                      int4Ptr(row.StatusCode),
		ClientIP:                        providerTextValue(row.ClientIp),
		UserAgent:                       providerTextValue(row.UserAgent),
		CreatedAt:                       timestamptzValue(row.CreatedAt),
	}, nil
}

func operationLogTargetsFromRows(rows []postgresqueries.JuheDatasetOperationLogTarget, names map[string]string) []port.OperationLogTargetSummary {
	if len(rows) == 0 {
		return []port.OperationLogTargetSummary{}
	}
	items := make([]port.OperationLogTargetSummary, 0, len(rows))
	for _, row := range rows {
		ownerID := providerTextValue(row.TargetOwnerSystemAccountID)
		items = append(items, port.OperationLogTargetSummary{
			ID:                           row.ID,
			TargetType:                   row.TargetType,
			TargetID:                     providerTextValue(row.TargetID),
			TargetName:                   providerTextValue(row.TargetName),
			TargetOwnerSystemAccountID:   ownerID,
			TargetOwnerSystemAccountName: names[ownerID],
			Relation:                     row.Relation,
			CreatedAt:                    timestamptzValue(row.CreatedAt),
		})
	}
	return items
}

func operationLogViewersFromRows(rows []postgresqueries.JuheDatasetOperationLogViewer, names map[string]string) []port.OperationLogViewerSummary {
	if len(rows) == 0 {
		return []port.OperationLogViewerSummary{}
	}
	items := make([]port.OperationLogViewerSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.OperationLogViewerSummary{
			SystemAccountID:   row.SystemAccountID,
			SystemAccountName: names[row.SystemAccountID],
			VisibilityReason:  row.VisibilityReason,
			DetailLevel:       row.DetailLevel,
			CreatedAt:         timestamptzValue(row.CreatedAt),
		})
	}
	return items
}

func parseOperationLogChangesJSON(raw string) ([]port.OperationLogChange, error) {
	if strings.TrimSpace(raw) == "" {
		return []port.OperationLogChange{}, nil
	}
	var changes []port.OperationLogChange
	if err := json.Unmarshal([]byte(raw), &changes); err != nil {
		return nil, fmt.Errorf("parse operation log changes_json: %w", err)
	}
	if changes == nil {
		return []port.OperationLogChange{}, nil
	}
	return changes, nil
}

func parseOperationLogMetadataJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil, fmt.Errorf("parse operation log metadata_json: %w", err)
	}
	if metadata == nil {
		return map[string]any{}, nil
	}
	return metadata, nil
}

func operationLogNameIDsFromRows(rows []postgresqueries.JuheDatasetOperationLog) []string {
	ids := make([]string, 0, len(rows)*2)
	for _, row := range rows {
		ids = append(ids, row.ActorSystemAccountID)
		if scopeID := providerTextValue(row.OperationScopeSystemAccountID); scopeID != "" {
			ids = append(ids, scopeID)
		}
	}
	return ids
}

func operationLogNameIDsFromDetail(row postgresqueries.JuheDatasetOperationLog, targets []postgresqueries.JuheDatasetOperationLogTarget, viewers []postgresqueries.JuheDatasetOperationLogViewer) []string {
	ids := operationLogNameIDsFromRows([]postgresqueries.JuheDatasetOperationLog{row})
	for _, target := range targets {
		if ownerID := providerTextValue(target.TargetOwnerSystemAccountID); ownerID != "" {
			ids = append(ids, ownerID)
		}
	}
	for _, viewer := range viewers {
		ids = append(ids, viewer.SystemAccountID)
	}
	return ids
}

func mergeOperationLogRowsByCreatedAt(leftRows []postgresqueries.JuheDatasetOperationLog, rightRows []postgresqueries.JuheDatasetOperationLog, limit int) []postgresqueries.JuheDatasetOperationLog {
	output := make([]postgresqueries.JuheDatasetOperationLog, 0, min(limit, len(leftRows)+len(rightRows)))
	leftIndex := 0
	rightIndex := 0
	for len(output) < limit && (leftIndex < len(leftRows) || rightIndex < len(rightRows)) {
		var left *postgresqueries.JuheDatasetOperationLog
		if leftIndex < len(leftRows) {
			left = &leftRows[leftIndex]
		}
		var right *postgresqueries.JuheDatasetOperationLog
		if rightIndex < len(rightRows) {
			right = &rightRows[rightIndex]
		}
		if right == nil || (left != nil && compareOperationLogRowsByCreatedAt(*left, *right) <= 0) {
			output = append(output, *left)
			leftIndex++
			continue
		}
		output = append(output, *right)
		rightIndex++
	}
	return output
}

func compareOperationLogRowsByCreatedAt(left postgresqueries.JuheDatasetOperationLog, right postgresqueries.JuheDatasetOperationLog) int {
	leftCreatedAt := timestamptzValue(left.CreatedAt)
	rightCreatedAt := timestamptzValue(right.CreatedAt)
	if !leftCreatedAt.Equal(rightCreatedAt) {
		if leftCreatedAt.After(rightCreatedAt) {
			return -1
		}
		return 1
	}
	if left.ID == right.ID {
		return 0
	}
	if left.ID > right.ID {
		return -1
	}
	return 1
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func defaultText(value string, fallback string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return fallback
	}
	return text
}

func isOperationLogAdminRole(role string) bool {
	role = strings.TrimSpace(role)
	return role == "admin" || role == "super_admin"
}

var _ port.OperationLogStore = (*Store)(nil)
var _ port.OperationLogReader = (*Store)(nil)
var _ port.OperationLogRetentionCleaner = (*Store)(nil)
