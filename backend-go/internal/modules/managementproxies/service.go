package managementproxies

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultListPageSize = 20
	maxListPageSize     = 200
	defaultListWindow   = 1001
)

type Service struct {
	store port.ManagementProxyReader
}

type ListInput struct {
	Page     int
	PageSize int
	Keyword  string
}

type OptionListInput struct {
	Keyword string
	Limit   int
}

type Summary struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     *string    `json:"description,omitempty"`
	Type            string     `json:"type"`
	Host            string     `json:"host"`
	Port            int        `json:"port"`
	Username        *string    `json:"username,omitempty"`
	Enabled         bool       `json:"enabled"`
	TestStatus      string     `json:"testStatus"`
	LatencyMs       *int       `json:"latencyMs,omitempty"`
	OutboundIP      *string    `json:"outboundIp,omitempty"`
	OutboundRegion  *string    `json:"outboundRegion,omitempty"`
	LastTestMessage *string    `json:"lastTestMessage,omitempty"`
	LastTestedAt    *time.Time `json:"lastTestedAt,omitempty"`
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Option struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

func NewService(store port.ManagementProxyReader) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management proxy store is required")
	}
	pageSize := normalizeListPageSize(input.PageSize)
	page := normalizeListPage(input.Page, pageSize)
	result, err := s.store.ListManagementProxies(ctx, port.ManagementProxyListInput{
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   pageSize + 1,
		Offset:  (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, min(len(result.Items), pageSize))
	for index, row := range result.Items {
		if index >= pageSize {
			break
		}
		items = append(items, Summary{
			ID:              row.ID,
			Name:            row.Name,
			Description:     row.Description,
			Type:            row.Type,
			Host:            row.Host,
			Port:            row.Port,
			Username:        row.Username,
			Enabled:         row.Enabled,
			TestStatus:      row.TestStatus,
			LatencyMs:       row.LatencyMs,
			OutboundIP:      row.OutboundIP,
			OutboundRegion:  row.OutboundRegion,
			LastTestMessage: row.LastTestMessage,
			LastTestedAt:    row.LastTestedAt,
		})
	}
	hasMore := result.HasMore || len(result.Items) > pageSize
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), hasMore),
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management proxy option store is required")
	}
	rows, err := s.store.ListManagementProxyOptions(ctx, port.ManagementProxyOptionListInput{
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   normalizeOptionLimit(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:      row.ID,
			Name:    row.Name,
			Type:    row.Type,
			Enabled: row.Enabled,
		})
	}
	return items, nil
}

func normalizeListPageSize(value int) int {
	if value <= 0 {
		return defaultListPageSize
	}
	if value > maxListPageSize {
		return maxListPageSize
	}
	return value
}

func normalizeListPage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	return min(value, pageUpperBoundForWindow(pageSize))
}

func normalizeOptionLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 50 {
		return 50
	}
	return value
}

func pagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	total := (max(1, page) - 1) * max(0, pageSize)
	total += max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}

func pageUpperBoundForWindow(pageSize int) int {
	return max(1, (defaultListWindow-1)/max(1, pageSize))
}
