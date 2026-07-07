package managementproxies

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

type Service struct {
	store port.ManagementProxyOptionReader
}

type OptionListInput struct {
	Keyword string
	Limit   int
}

type Option struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

func NewService(store port.ManagementProxyOptionReader) *Service {
	return &Service{store: store}
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

func normalizeOptionLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 50 {
		return 50
	}
	return value
}
