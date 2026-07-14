package managementruntimelogs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize           = 100
	maxPageSize               = 100
	maxListWindowRows         = 1001
	keywordDefaultWindow      = 6 * time.Hour
	javaScriptISOStringLayout = "2006-01-02T15:04:05.000Z"
)

type Service struct {
	store port.ManagementRuntimeLogReader
	now   func() time.Time
}

type ServiceOptions struct {
	Store port.ManagementRuntimeLogReader
	Now   func() time.Time
}

type ListInput struct {
	TraceID          string
	Level            string
	Event            string
	Keyword          string
	StartAt          time.Time
	EndAt            time.Time
	Page             int
	PageSize         int
	PageSizeProvided bool
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Summary struct {
	ID           string `json:"id"`
	Time         string `json:"time"`
	Level        string `json:"level"`
	TraceID      string `json:"traceId,omitempty"`
	Event        string `json:"event,omitempty"`
	Message      string `json:"message,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type Detail struct {
	Summary
	RawJSON string `json:"rawJson"`
}

func NewService(store port.ManagementRuntimeLogReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, now: now}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management runtime log reader is required")
	}
	pageSize := normalizePageSize(input.PageSize, input.PageSizeProvided)
	page := normalizePage(input.Page, pageSize)
	keyword := trimECMAScriptWhitespace(input.Keyword)
	startAt := formatQueryTime(input.StartAt)
	endAt := formatQueryTime(input.EndAt)
	if keyword != "" && startAt == "" && endAt == "" {
		startAt = formatQueryTime(s.now().Add(-keywordDefaultWindow))
	}
	result, err := s.store.ListManagementRuntimeLogs(ctx, port.ManagementRuntimeLogListInput{
		TraceID: trimECMAScriptWhitespace(input.TraceID),
		Level:   normalizeLevel(input.Level),
		Event:   trimECMAScriptWhitespace(input.Event),
		Keyword: keyword,
		StartAt: startAt,
		EndAt:   endAt,
		Limit:   pageSize,
		Offset:  (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, summaryFromStore(item))
	}
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), result.HasMore),
		HasMore:  result.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Detail(ctx context.Context, id string) (Detail, bool, error) {
	if s.store == nil {
		return Detail{}, false, fmt.Errorf("management runtime log reader is required")
	}
	detail, found, err := s.store.GetManagementRuntimeLog(ctx, trimECMAScriptWhitespace(id))
	if err != nil || !found {
		return Detail{}, found, err
	}
	return Detail{
		Summary: summaryFromStore(detail.ManagementRuntimeLogSummary),
		RawJSON: detail.RawJSON,
	}, true, nil
}

func summaryFromStore(item port.ManagementRuntimeLogSummary) Summary {
	return Summary{
		ID:           item.ID,
		Time:         item.Time,
		Level:        item.Level,
		TraceID:      optionalText(item.TraceID),
		Event:        optionalText(item.Event),
		Message:      optionalText(item.Message),
		ErrorMessage: optionalText(item.ErrorMessage),
		CreatedAt:    item.CreatedAt,
	}
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeLevel(value string) string {
	level := strings.ToLower(trimECMAScriptWhitespace(value))
	switch level {
	case "trace", "debug", "info", "warn", "error", "fatal":
		return level
	default:
		return ""
	}
}

func normalizePageSize(value int, provided bool) int {
	if !provided {
		return defaultPageSize
	}
	return min(max(value, 1), maxPageSize)
}

func normalizePage(value int, pageSize int) int {
	maxPage := max(1, (maxListWindowRows-1)/max(1, pageSize))
	if value <= 0 {
		return 1
	}
	return min(value, maxPage)
}

func pagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	total := (max(1, page) - 1) * max(0, pageSize)
	total += max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}

func formatQueryTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Millisecond).Format(javaScriptISOStringLayout)
}

func trimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
			'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
			'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
			'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
			'\u2029':
			return true
		default:
			return false
		}
	})
}
