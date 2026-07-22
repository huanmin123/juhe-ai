package managementpublicapilogs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize           = 50
	maxPageSize               = 100
	maxListWindowRows         = 1001
	javaScriptISOStringLayout = "2006-01-02T15:04:05.000Z"
)

type Service struct {
	store port.ManagementPublicAPILogReader
}

type ListInput struct {
	TraceID          string
	SourceRefID      string
	Path             string
	Result           string
	StatusCode       int
	ClientIP         string
	StartAt          time.Time
	EndAt            time.Time
	Page             int
	PageSize         int
	PageSizeProvided bool
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type ListItem struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"createdAt"`
	SourceName string `json:"sourceName,omitempty"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Success    bool   `json:"success"`
	StatusCode *int   `json:"statusCode,omitempty"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	ClientIP   string `json:"clientIp,omitempty"`
	TraceID    string `json:"traceId,omitempty"`
}

type Summary struct {
	ID                    string                         `json:"id"`
	TraceID               string                         `json:"traceId,omitempty"`
	SourceRefID           string                         `json:"sourceRefId,omitempty"`
	SourceName            string                         `json:"sourceName,omitempty"`
	TokenID               string                         `json:"tokenId,omitempty"`
	TokenName             string                         `json:"tokenName,omitempty"`
	TokenPrefix           string                         `json:"tokenPrefix,omitempty"`
	IsTestToken           bool                           `json:"isTestToken"`
	Method                string                         `json:"method"`
	Path                  string                         `json:"path"`
	QueryString           string                         `json:"queryString,omitempty"`
	ClientIP              string                         `json:"clientIp,omitempty"`
	UserAgent             string                         `json:"userAgent,omitempty"`
	StatusCode            *int                           `json:"statusCode,omitempty"`
	Success               bool                           `json:"success"`
	DurationMs            *int64                         `json:"durationMs,omitempty"`
	RequestSizeBytes      int64                          `json:"requestSizeBytes"`
	ResponseSizeBytes     int64                          `json:"responseSizeBytes"`
	RequestCaptureStatus  port.PublicAPILogCaptureStatus `json:"requestCaptureStatus"`
	ResponseCaptureStatus port.PublicAPILogCaptureStatus `json:"responseCaptureStatus"`
	ErrorCode             string                         `json:"errorCode,omitempty"`
	ErrorMessage          string                         `json:"errorMessage,omitempty"`
	StartedAt             string                         `json:"startedAt"`
	EndedAt               string                         `json:"endedAt"`
	CreatedAt             string                         `json:"createdAt"`
}

type Detail struct {
	Summary
	RequestData  map[string]any `json:"requestData"`
	ResponseData map[string]any `json:"responseData"`
}

func NewService(store port.ManagementPublicAPILogReader) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management public API log reader is required")
	}
	pageSize := normalizePageSize(input.PageSize, input.PageSizeProvided)
	page := normalizePage(input.Page, pageSize)
	result, err := s.store.ListManagementPublicAPILogs(ctx, port.ManagementPublicAPILogListInput{
		TraceID:     trimECMAScriptWhitespace(input.TraceID),
		SourceRefID: normalizeSourceRefID(input.SourceRefID),
		Path:        normalizePath(input.Path),
		Result:      normalizeResult(input.Result),
		StatusCode:  normalizeStatusCode(input.StatusCode),
		ClientIP:    trimECMAScriptWhitespace(input.ClientIP),
		StartAt:     input.StartAt,
		EndAt:       input.EndAt,
		Limit:       pageSize,
		Offset:      (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}

	rows := result.Items
	hasMore := result.HasMore || len(rows) > pageSize
	if len(rows) > pageSize {
		rows = rows[:pageSize]
	}
	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, listItemFromStore(row))
	}
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), hasMore),
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Detail(ctx context.Context, id string) (Detail, bool, error) {
	if s.store == nil {
		return Detail{}, false, fmt.Errorf("management public API log reader is required")
	}
	detail, found, err := s.store.GetManagementPublicAPILog(ctx, id)
	if err != nil || !found {
		return Detail{}, found, err
	}
	return Detail{
		Summary:      summaryFromStore(detail.ManagementPublicAPILogSummary),
		RequestData:  parseJSONObject(detail.RequestDataJSON),
		ResponseData: parseJSONObject(detail.ResponseDataJSON),
	}, true, nil
}

func listItemFromStore(item port.ManagementPublicAPILogListItem) ListItem {
	return ListItem{
		ID:         item.ID,
		CreatedAt:  formatTime(item.CreatedAt),
		SourceName: optionalText(item.SourceName),
		Method:     item.Method,
		Path:       item.Path,
		Success:    item.Success,
		StatusCode: cloneInt(item.StatusCode),
		DurationMs: cloneInt64(item.DurationMs),
		ClientIP:   optionalText(item.ClientIP),
		TraceID:    optionalText(item.TraceID),
	}
}

func summaryFromStore(item port.ManagementPublicAPILogSummary) Summary {
	return Summary{
		ID:                    item.ID,
		TraceID:               optionalText(item.TraceID),
		SourceRefID:           optionalText(item.SourceRefID),
		SourceName:            optionalText(item.SourceName),
		TokenID:               optionalText(item.TokenID),
		TokenName:             optionalText(item.TokenName),
		TokenPrefix:           optionalText(item.TokenPrefix),
		IsTestToken:           item.IsTestToken,
		Method:                item.Method,
		Path:                  item.Path,
		QueryString:           optionalText(item.QueryString),
		ClientIP:              optionalText(item.ClientIP),
		UserAgent:             optionalText(item.UserAgent),
		StatusCode:            cloneInt(item.StatusCode),
		Success:               item.Success,
		DurationMs:            cloneInt64(item.DurationMs),
		RequestSizeBytes:      max(0, item.RequestSizeBytes),
		ResponseSizeBytes:     max(0, item.ResponseSizeBytes),
		RequestCaptureStatus:  normalizeCaptureStatus(item.RequestCaptureStatus),
		ResponseCaptureStatus: normalizeCaptureStatus(item.ResponseCaptureStatus),
		ErrorCode:             optionalText(item.ErrorCode),
		ErrorMessage:          optionalText(item.ErrorMessage),
		StartedAt:             formatTime(item.StartedAt),
		EndedAt:               formatTime(item.EndedAt),
		CreatedAt:             formatTime(item.CreatedAt),
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
	total := (max(1, page)-1)*max(0, pageSize) + max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}

func normalizeSourceRefID(value string) string {
	text := trimECMAScriptWhitespace(value)
	if text == "all" {
		return ""
	}
	return text
}

func normalizePath(value string) string {
	text := trimECMAScriptWhitespace(value)
	if text == "" || text == "all" {
		return ""
	}
	text = stripHTTPMethod(text)
	if queryIndex := strings.IndexByte(text, '?'); queryIndex >= 0 {
		text = text[:queryIndex]
	}
	return trimECMAScriptWhitespace(text)
}

func stripHTTPMethod(value string) string {
	for _, method := range [...]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if len(value) <= len(method) || !strings.EqualFold(value[:len(method)], method) {
			continue
		}
		suffix := value[len(method):]
		first, _ := utf8.DecodeRuneInString(suffix)
		if !isECMAScriptWhitespace(first) {
			continue
		}
		return strings.TrimLeftFunc(suffix, isECMAScriptWhitespace)
	}
	return value
}

func normalizeResult(value string) port.ManagementPublicAPILogResultFilter {
	switch trimECMAScriptWhitespace(value) {
	case string(port.ManagementPublicAPILogResultSuccess):
		return port.ManagementPublicAPILogResultSuccess
	case string(port.ManagementPublicAPILogResultFailed):
		return port.ManagementPublicAPILogResultFailed
	default:
		return port.ManagementPublicAPILogResultAll
	}
}

func normalizeStatusCode(value int) *int {
	if value < 100 || value > 599 {
		return nil
	}
	result := value
	return &result
}

func normalizeCaptureStatus(value port.PublicAPILogCaptureStatus) port.PublicAPILogCaptureStatus {
	switch value {
	case port.PublicAPILogCaptureComplete,
		port.PublicAPILogCaptureTruncated,
		port.PublicAPILogCaptureEmpty,
		port.PublicAPILogCaptureDropped:
		return value
	default:
		return port.PublicAPILogCaptureEmpty
	}
}

func parseJSONObject(value string) map[string]any {
	var result map[string]any
	if err := json.Unmarshal([]byte(value), &result); err != nil || result == nil {
		return map[string]any{}
	}
	return result
}

func optionalText(value *string) string {
	if value == nil || *value == "" {
		return ""
	}
	return *value
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Millisecond).Format(javaScriptISOStringLayout)
}

func trimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, isECMAScriptWhitespace)
}

func isECMAScriptWhitespace(character rune) bool {
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
}
