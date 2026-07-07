package managementoperationlogs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize = 100
	maxPageSize     = 100
	maxWindowRows   = 1001
)

type Service struct {
	store port.OperationLogReader
}

type ListInput struct {
	ViewerSystemAccountID         string
	SummaryKeyword                string
	Module                        string
	Action                        string
	ResourceType                  string
	ResourceID                    string
	ActorSystemAccountID          string
	AffectedSystemAccountID       string
	OperationScopeSystemAccountID string
	TraceID                       string
	StartAt                       time.Time
	EndAt                         time.Time
	Page                          int
	PageSize                      int
}

type DetailInput struct {
	ID                    string
	ViewerSystemAccountID string
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Summary struct {
	ID                              string `json:"id"`
	TraceID                         string `json:"traceId,omitempty"`
	ActorSystemAccountID            string `json:"actorSystemAccountId"`
	ActorUsername                   string `json:"actorUsername,omitempty"`
	ActorDisplayName                string `json:"actorDisplayName,omitempty"`
	ActorSystemAccountName          string `json:"actorSystemAccountName,omitempty"`
	ActorRole                       string `json:"actorRole"`
	OperationScopeSystemAccountID   string `json:"operationScopeSystemAccountId,omitempty"`
	OperationScopeSystemAccountName string `json:"operationScopeSystemAccountName,omitempty"`
	Mode                            string `json:"mode"`
	Module                          string `json:"module"`
	Action                          string `json:"action"`
	OperationKey                    string `json:"operationKey"`
	ResourceType                    string `json:"resourceType"`
	ResourceID                      string `json:"resourceId,omitempty"`
	ResourceName                    string `json:"resourceName,omitempty"`
	Summary                         string `json:"summary"`
	DetailLevel                     string `json:"detailLevel"`
	VisibilityScope                 string `json:"visibilityScope"`
	Method                          string `json:"method,omitempty"`
	Path                            string `json:"path,omitempty"`
	StatusCode                      *int   `json:"statusCode,omitempty"`
	ClientIP                        string `json:"clientIp,omitempty"`
	CreatedAt                       string `json:"createdAt"`
}

type TargetSummary struct {
	ID                           string `json:"id"`
	TargetType                   string `json:"targetType"`
	TargetID                     string `json:"targetId,omitempty"`
	TargetName                   string `json:"targetName,omitempty"`
	TargetOwnerSystemAccountID   string `json:"targetOwnerSystemAccountId,omitempty"`
	TargetOwnerSystemAccountName string `json:"targetOwnerSystemAccountName,omitempty"`
	Relation                     string `json:"relation"`
	CreatedAt                    string `json:"createdAt"`
}

type ViewerSummary struct {
	SystemAccountID   string `json:"systemAccountId"`
	SystemAccountName string `json:"systemAccountName,omitempty"`
	VisibilityReason  string `json:"visibilityReason"`
	DetailLevel       string `json:"detailLevel"`
	CreatedAt         string `json:"createdAt"`
}

type Detail struct {
	Summary
	Changes   []port.OperationLogChange `json:"changes"`
	Metadata  map[string]any            `json:"metadata"`
	Targets   []TargetSummary           `json:"targets"`
	Viewers   []ViewerSummary           `json:"viewers"`
	UserAgent string                    `json:"userAgent,omitempty"`
}

func NewService(store port.OperationLogReader) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("operation log reader is required")
	}
	pageSize := normalizePageSize(input.PageSize)
	page := normalizePage(input.Page, pageSize)
	storeInput := port.OperationLogListInput{
		SummaryKeyword:                strings.TrimSpace(input.SummaryKeyword),
		Module:                        strings.TrimSpace(input.Module),
		Action:                        strings.TrimSpace(input.Action),
		ResourceType:                  strings.TrimSpace(input.ResourceType),
		ResourceID:                    strings.TrimSpace(input.ResourceID),
		ActorSystemAccountID:          strings.TrimSpace(input.ActorSystemAccountID),
		AffectedSystemAccountID:       strings.TrimSpace(input.AffectedSystemAccountID),
		OperationScopeSystemAccountID: strings.TrimSpace(input.OperationScopeSystemAccountID),
		TraceID:                       strings.TrimSpace(input.TraceID),
		StartAt:                       input.StartAt,
		EndAt:                         input.EndAt,
		Limit:                         pageSize + 1,
		Offset:                        (page - 1) * pageSize,
	}
	viewerSystemAccountID := strings.TrimSpace(input.ViewerSystemAccountID)
	var (
		pageResult port.OperationLogListResult
		err        error
	)
	if viewerSystemAccountID == "" {
		pageResult, err = s.store.ListOperationLogs(ctx, storeInput)
	} else {
		pageResult, err = s.store.ListVisibleOperationLogs(ctx, port.OperationLogVisibleListInput{
			ViewerSystemAccountID: viewerSystemAccountID,
			List:                  storeInput,
		})
	}
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(pageResult.Items))
	for _, item := range pageResult.Items {
		summary := apiSummary(item)
		if viewerSystemAccountID != "" {
			summary = sanitizeSummaryForViewer(summary, item)
		}
		items = append(items, summary)
	}
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), pageResult.HasMore),
		HasMore:  pageResult.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (Detail, bool, error) {
	if s.store == nil {
		return Detail{}, false, fmt.Errorf("operation log reader is required")
	}
	viewerSystemAccountID := strings.TrimSpace(input.ViewerSystemAccountID)
	detail, found, err := s.store.GetOperationLogDetail(ctx, port.OperationLogDetailInput{
		ID:                    strings.TrimSpace(input.ID),
		ViewerSystemAccountID: viewerSystemAccountID,
	})
	if err != nil || !found {
		return Detail{}, found, err
	}
	output := apiDetail(detail)
	if viewerSystemAccountID != "" {
		output = sanitizeDetailForViewer(output, detail.Summary)
	}
	return output, true, nil
}

func apiSummary(item port.OperationLogSummary) Summary {
	return Summary{
		ID:                              item.ID,
		TraceID:                         item.TraceID,
		ActorSystemAccountID:            item.ActorSystemAccountID,
		ActorUsername:                   item.ActorUsername,
		ActorDisplayName:                item.ActorDisplayName,
		ActorSystemAccountName:          item.ActorSystemAccountName,
		ActorRole:                       item.ActorRole,
		OperationScopeSystemAccountID:   item.OperationScopeSystemAccountID,
		OperationScopeSystemAccountName: item.OperationScopeSystemAccountName,
		Mode:                            item.Mode,
		Module:                          item.Module,
		Action:                          item.Action,
		OperationKey:                    item.OperationKey,
		ResourceType:                    item.ResourceType,
		ResourceID:                      item.ResourceID,
		ResourceName:                    item.ResourceName,
		Summary:                         item.Summary,
		DetailLevel:                     item.DetailLevel,
		VisibilityScope:                 item.VisibilityScope,
		Method:                          item.Method,
		Path:                            item.Path,
		StatusCode:                      item.StatusCode,
		ClientIP:                        item.ClientIP,
		CreatedAt:                       formatTime(item.CreatedAt),
	}
}

func apiDetail(detail port.OperationLogDetail) Detail {
	targets := make([]TargetSummary, 0, len(detail.Targets))
	for _, target := range detail.Targets {
		targets = append(targets, TargetSummary{
			ID:                           target.ID,
			TargetType:                   target.TargetType,
			TargetID:                     target.TargetID,
			TargetName:                   target.TargetName,
			TargetOwnerSystemAccountID:   target.TargetOwnerSystemAccountID,
			TargetOwnerSystemAccountName: target.TargetOwnerSystemAccountName,
			Relation:                     target.Relation,
			CreatedAt:                    formatTime(target.CreatedAt),
		})
	}
	viewers := make([]ViewerSummary, 0, len(detail.Viewers))
	for _, viewer := range detail.Viewers {
		viewers = append(viewers, ViewerSummary{
			SystemAccountID:   viewer.SystemAccountID,
			SystemAccountName: viewer.SystemAccountName,
			VisibilityReason:  viewer.VisibilityReason,
			DetailLevel:       viewer.DetailLevel,
			CreatedAt:         formatTime(viewer.CreatedAt),
		})
	}
	changes := detail.Summary.Changes
	if changes == nil {
		changes = []port.OperationLogChange{}
	}
	metadata := detail.Summary.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return Detail{
		Summary:   apiSummary(detail.Summary),
		Changes:   changes,
		Metadata:  metadata,
		Targets:   targets,
		Viewers:   viewers,
		UserAgent: detail.Summary.UserAgent,
	}
}

func sanitizeSummaryForViewer(summary Summary, source port.OperationLogSummary) Summary {
	summary.ClientIP = ""
	if effectiveViewerDetailLevel(source) == "full" {
		return summary
	}
	summary.DetailLevel = "summary"
	summary.Method = ""
	summary.Path = ""
	summary.StatusCode = nil
	return summary
}

func sanitizeDetailForViewer(detail Detail, source port.OperationLogSummary) Detail {
	detail.Summary = sanitizeSummaryForViewer(detail.Summary, source)
	detail.UserAgent = ""
	level := effectiveViewerDetailLevel(source)
	if level == "summary" {
		detail.Changes = []port.OperationLogChange{}
		detail.Metadata = map[string]any{}
		detail.Targets = []TargetSummary{}
		detail.Viewers = []ViewerSummary{}
		return detail
	}
	detail.Viewers = []ViewerSummary{}
	return detail
}

func effectiveViewerDetailLevel(source port.OperationLogSummary) string {
	if source.VisibilityScope == "all_users" {
		return "summary"
	}
	if source.DetailLevel == "summary" || source.ViewerDetailLevel == "summary" {
		return "summary"
	}
	return "full"
}

func normalizePageSize(value int) int {
	if value <= 0 {
		return defaultPageSize
	}
	return min(value, maxPageSize)
}

func normalizePage(value int, pageSize int) int {
	maxPage := max(1, (maxWindowRows-1)/max(1, pageSize))
	if value <= 0 {
		return 1
	}
	return min(maxPage, value)
}

func pagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	total := (max(1, page) - 1) * max(0, pageSize)
	total += max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
