package managementclientipstats

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

var ErrIPNotFound = errors.New("management client IP stats IP not found")

type DetailInput struct {
	IPHash    string
	Page      int
	PageSize  int
	StartDate string
	EndDate   string
	SortField string
	SortOrder string
}

type AccountUsageItem struct {
	AccountID                     string       `json:"accountId"`
	AccountName                   *string      `json:"accountName,omitempty"`
	AccountOwnerSystemAccountID   *string      `json:"accountOwnerSystemAccountId,omitempty"`
	AccountOwnerSystemAccountName *string      `json:"accountOwnerSystemAccountName,omitempty"`
	RangeUsage                    UsageSummary `json:"rangeUsage"`
}

type DetailResult struct {
	IPHash         string             `json:"ipHash"`
	AggregateIPKey string             `json:"aggregateIpKey"`
	LastSeenAt     *string            `json:"lastSeenAt,omitempty"`
	Items          []AccountUsageItem `json:"items"`
	PageUpperBound int                `json:"pageUpperBound"`
	HasMore        bool               `json:"hasMore"`
	Page           int                `json:"page"`
	PageSize       int                `json:"pageSize"`
	Range          UsageRange         `json:"range"`
	RangeReady     bool               `json:"rangeReady"`
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (DetailResult, error) {
	if s.detailReader == nil {
		return DetailResult{}, fmt.Errorf("management client IP stats detail reader is required")
	}
	ipHash, valid := normalizeIPHash(input.IPHash)
	if !valid {
		return DetailResult{}, ErrIPNotFound
	}
	location, err := s.usageStatsLocation(ctx)
	if err != nil {
		return DetailResult{}, err
	}
	now := s.now()
	rangeValue := normalizeUsageRange(input.StartDate, input.EndDate, now, location)
	pageSize := normalizeDetailPageSize(input.PageSize)
	page := normalizePage(input.Page, pageSize)
	sortField, sortOrder := normalizeDetailSort(input.SortField, input.SortOrder)

	pageValue, err := s.detailReader.GetManagementClientIPStatsDetail(
		ctx,
		port.ManagementClientIPStatsDetailInput{
			IPHash:    ipHash,
			StartDate: rangeValue.StartDate,
			EndDate:   rangeValue.EndDate,
			SortField: sortField,
			SortOrder: sortOrder,
			Limit:     pageSize + 1,
			Offset:    (page - 1) * pageSize,
		},
	)
	if err != nil {
		return DetailResult{}, err
	}
	if !pageValue.Found {
		return DetailResult{}, ErrIPNotFound
	}

	result := DetailResult{
		IPHash:         pageValue.IPHash,
		AggregateIPKey: pageValue.AggregateIPKey,
		LastSeenAt:     stringPointer(pageValue.LastSeenAt),
		Items:          []AccountUsageItem{},
		Page:           page,
		PageSize:       pageSize,
		Range:          rangeValue,
		RangeReady:     pageValue.RangeReady,
	}
	if !pageValue.RangeReady {
		return result, nil
	}

	result.Items = make([]AccountUsageItem, 0, len(pageValue.Rows))
	for _, row := range pageValue.Rows {
		result.Items = append(result.Items, AccountUsageItem{
			AccountID:                     row.AccountID,
			AccountName:                   cloneString(row.AccountName),
			AccountOwnerSystemAccountID:   cloneString(row.AccountOwnerSystemAccountID),
			AccountOwnerSystemAccountName: cloneString(row.AccountOwnerSystemAccountName),
			RangeUsage:                    usageSummary(row.RangeUsage),
		})
	}
	result.PageUpperBound = (page-1)*pageSize + len(result.Items)
	result.HasMore = pageValue.HasMore
	if result.HasMore {
		result.PageUpperBound++
	}
	return result, nil
}

func normalizeIPHash(value string) (string, bool) {
	value = trimECMAScriptWhitespace(value)
	if len(value) != 64 {
		return "", false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'f' ||
			character >= 'A' && character <= 'F' {
			continue
		}
		return "", false
	}
	return strings.ToLower(value), true
}

func normalizeDetailPageSize(value int) int {
	if value <= 0 {
		return defaultListPageSize
	}
	return min(value, 100)
}

func normalizeDetailSort(
	field string,
	order string,
) (port.ManagementClientIPStatsSortField, port.ManagementClientIPStatsSortOrder) {
	sortField := port.ManagementClientIPStatsSortField(field)
	if field == "" {
		sortField = port.ManagementClientIPStatsSortRequestCount
	}
	if order == "asc" {
		return sortField, port.ManagementClientIPStatsSortAscending
	}
	return sortField, port.ManagementClientIPStatsSortDescending
}
