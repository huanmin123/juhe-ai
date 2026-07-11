package managementapikeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/apikeyschedule"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultListPageSize = 50
	maxListPageSize     = 200
	maxListWindowRows   = 1000
	maxQuotaAmount      = 9007199254740991
)

var ErrAPIKeyListInvalid = errors.New("management API Key list invalid")

type Service struct {
	store port.ManagementAPIKeyListReader
}

type ListInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	Page                 int
	PageSize             int
	PageSizeProvided     bool
	Keyword              string
	Status               string
	RouteStrategyID      string
}

type ListItem struct {
	ID                   string                             `json:"id"`
	SystemAccountID      string                             `json:"systemAccountId,omitempty"`
	SystemAccountName    string                             `json:"systemAccountName,omitempty"`
	Name                 string                             `json:"name"`
	Description          *string                            `json:"description,omitempty"`
	KeyPrefix            string                             `json:"keyPrefix"`
	KeySuffix            string                             `json:"keySuffix"`
	Status               string                             `json:"status"`
	IsDefault            bool                               `json:"isDefault"`
	RouteStrategyID      string                             `json:"routeStrategyId"`
	RouteStrategyName    string                             `json:"routeStrategyName,omitempty"`
	RouteStrategyMode    string                             `json:"routeStrategyMode,omitempty"`
	RouteStrategyStatus  string                             `json:"routeStrategyStatus,omitempty"`
	ExpiresAt            *time.Time                         `json:"expiresAt,omitempty"`
	QuotaLimits          port.ManagementRequestQuotaLimits  `json:"quotaLimits"`
	AvailabilitySchedule map[string]any                     `json:"availabilitySchedule,omitempty"`
	Usage                port.ManagementAccountUsageSummary `json:"usage"`
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

func NewService(store port.ManagementAPIKeyListReader) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management API Key list reader is required")
	}
	systemAccountID, includeOwner, err := listScope(input)
	if err != nil {
		return ListResult{}, err
	}
	pageSize := listPageSize(input.PageSize, input.PageSizeProvided)
	page := listPage(input.Page, pageSize)
	storedPage, err := s.store.ListManagementAPIKeys(ctx, port.ManagementAPIKeyListInput{
		SystemAccountID: systemAccountID,
		Keyword:         strings.TrimSpace(input.Keyword),
		Status:          listStatus(input.Status),
		RouteStrategyID: strings.TrimSpace(input.RouteStrategyID),
		Limit:           pageSize + 1,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	rows := storedPage.Rows
	hasMore := storedPage.HasMore || len(rows) > pageSize
	if len(rows) > pageSize {
		rows = rows[:pageSize]
	}
	if len(rows) == 0 {
		return listResult(nil, page, pageSize, hasMore), nil
	}

	usageScopes := make([]port.ManagementAPIKeyUsageScope, 0, len(rows))
	for _, row := range rows {
		usageScopes = append(usageScopes, port.ManagementAPIKeyUsageScope{
			SystemAccountID: row.SystemAccountID,
			APIKeyID:        row.ID,
		})
	}
	usageRows, err := s.store.ListManagementAPIKeyUsageTotals(ctx, usageScopes)
	if err != nil {
		return ListResult{}, err
	}
	usageByScope := make(map[port.ManagementAPIKeyUsageScope]port.ManagementAccountUsageSummary, len(usageRows))
	for _, row := range usageRows {
		usageByScope[port.ManagementAPIKeyUsageScope{
			SystemAccountID: row.SystemAccountID,
			APIKeyID:        row.APIKeyID,
		}] = row.Usage
	}

	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		item, err := listItem(row, usageByScope[port.ManagementAPIKeyUsageScope{
			SystemAccountID: row.SystemAccountID,
			APIKeyID:        row.ID,
		}], includeOwner)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, item)
	}
	return listResult(items, page, pageSize, hasMore), nil
}

func listScope(input ListInput) (string, bool, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	if actorSystemAccountID == "" {
		return "", false, ErrAPIKeyListInvalid
	}
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return actorSystemAccountID, false, nil
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return systemAccountID, true, nil
}

func isAdminRole(role string) bool {
	switch strings.TrimSpace(role) {
	case "admin", "super_admin":
		return true
	default:
		return false
	}
}

func listPageSize(value int, provided bool) int {
	if !provided {
		return defaultListPageSize
	}
	return min(max(value, 1), maxListPageSize)
}

func listPage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	return min(value, max(1, maxListWindowRows/max(1, pageSize)))
}

func listStatus(value string) string {
	switch strings.TrimSpace(value) {
	case "active":
		return "active"
	case "disabled":
		return "disabled"
	default:
		return ""
	}
}

func listResult(items []ListItem, page int, pageSize int, hasMore bool) ListResult {
	if items == nil {
		items = []ListItem{}
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return ListResult{
		Items:    items,
		Total:    total,
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}
}

func listItem(
	row port.ManagementAPIKeyListRow,
	usage port.ManagementAccountUsageSummary,
	includeOwner bool,
) (ListItem, error) {
	quotaLimits, err := parseQuotaLimits(row.QuotaLimitsJSON)
	if err != nil {
		return ListItem{}, fmt.Errorf("parse management API Key %q quota limits: %w", row.ID, err)
	}
	schedule, err := parseAvailabilitySchedule(row.AvailabilityScheduleJSON)
	if err != nil {
		return ListItem{}, fmt.Errorf("parse management API Key %q availability schedule: %w", row.ID, err)
	}
	item := ListItem{
		ID:                   row.ID,
		Name:                 row.Name,
		Description:          row.Description,
		KeyPrefix:            row.KeyPrefix,
		KeySuffix:            row.KeySuffix,
		Status:               row.Status,
		IsDefault:            row.IsDefault,
		RouteStrategyID:      row.RouteStrategyID,
		RouteStrategyName:    row.RouteStrategyName,
		RouteStrategyMode:    row.RouteStrategyMode,
		RouteStrategyStatus:  row.RouteStrategyStatus,
		ExpiresAt:            row.ExpiresAt,
		QuotaLimits:          quotaLimits,
		AvailabilitySchedule: schedule,
		Usage:                usage,
	}
	if includeOwner {
		item.SystemAccountID = row.SystemAccountID
		item.SystemAccountName = row.SystemAccountName
	}
	return item, nil
}

func parseQuotaLimits(raw *string) (port.ManagementRequestQuotaLimits, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" || strings.TrimSpace(*raw) == "null" {
		return port.ManagementRequestQuotaLimits{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(*raw), &values); err != nil {
		return port.ManagementRequestQuotaLimits{}, err
	}
	if values == nil {
		return port.ManagementRequestQuotaLimits{}, nil
	}
	result := port.ManagementRequestQuotaLimits{}
	for key, value := range values {
		switch key {
		case "hourly":
			limit, hours, err := parseQuotaLimit(value, true)
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, fmt.Errorf("%s: %w", key, err)
			}
			result.Hourly = &port.ManagementRequestHourlyQuotaLimit{
				Enabled: true,
				Hours:   hours,
				Limit:   limit,
			}
		case "daily", "weekly", "monthly", "total":
			limit, _, err := parseQuotaLimit(value, false)
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, fmt.Errorf("%s: %w", key, err)
			}
			item := &port.ManagementRequestQuotaLimit{Enabled: true, Limit: limit}
			switch key {
			case "daily":
				result.Daily = item
			case "weekly":
				result.Weekly = item
			case "monthly":
				result.Monthly = item
			case "total":
				result.Total = item
			}
		default:
			return port.ManagementRequestQuotaLimits{}, fmt.Errorf("unknown quota field %q", key)
		}
	}
	return result, nil
}

func parseQuotaLimit(raw json.RawMessage, hourly bool) (float64, int, error) {
	var item struct {
		Enabled bool        `json:"enabled"`
		Hours   *int        `json:"hours"`
		Limit   json.Number `json:"limit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&item); err != nil {
		return 0, 0, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, 0, fmt.Errorf("quota value must contain one object")
	}
	limit, err := strconv.ParseFloat(item.Limit.String(), 64)
	if err != nil ||
		!item.Enabled ||
		math.IsNaN(limit) ||
		math.IsInf(limit, 0) ||
		limit <= 0 ||
		limit > maxQuotaAmount {
		return 0, 0, fmt.Errorf("quota must be enabled with a positive limit")
	}
	if quotaDecimalPlaces(item.Limit.String()) > 6 {
		return 0, 0, fmt.Errorf("quota limit must have at most 6 decimal places")
	}
	if hourly {
		if item.Hours == nil || *item.Hours < 1 || *item.Hours > 720 {
			return 0, 0, fmt.Errorf("hourly hours must be in 1..720")
		}
		return limit, *item.Hours, nil
	}
	if item.Hours != nil {
		return 0, 0, fmt.Errorf("hours are only valid for hourly quota")
	}
	return limit, 0, nil
}

func quotaDecimalPlaces(text string) int {
	mantissa := text
	exponent := 0
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(mantissa[index+1:])
		if err != nil {
			return 7
		}
		exponent = parsed
		mantissa = mantissa[:index]
	}
	mantissa = strings.TrimPrefix(strings.TrimPrefix(mantissa, "+"), "-")
	decimalIndex := strings.IndexByte(mantissa, '.')
	fractionLength := 0
	digits := mantissa
	if decimalIndex >= 0 {
		fractionLength = len(mantissa) - decimalIndex - 1
		digits = mantissa[:decimalIndex] + mantissa[decimalIndex+1:]
	}
	scale := fractionLength - exponent
	for scale > 0 && strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		scale--
	}
	return max(0, scale)
}

func parseAvailabilitySchedule(raw *string) (map[string]any, error) {
	return apikeyschedule.ParseJSON(raw, time.Now().UTC(), "UTC")
}
