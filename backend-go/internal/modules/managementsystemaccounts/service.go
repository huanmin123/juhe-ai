package managementsystemaccounts

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOptionLimit      = 50
	maxOptionLimit          = 50
	maxOptionFilterItemSize = 50
)

type Service struct {
	store port.ManagementSystemAccountOptionReader
}

type OptionListInput struct {
	IDs     []string
	Keyword string
	Limit   int
}

type Option struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

func NewService(store port.ManagementSystemAccountOptionReader) *Service {
	return &Service{store: store}
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management system account option store is required")
	}
	rows, err := s.store.ListManagementSystemAccountOptions(ctx, port.ManagementSystemAccountOptionListInput{
		IDs:     uniqueStrings(input.IDs, maxOptionFilterItemSize),
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   optionLimit(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:          row.ID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Status:      row.Status,
		})
	}
	return items, nil
}

func optionLimit(limit int) int {
	if limit <= 0 {
		return defaultOptionLimit
	}
	return min(limit, maxOptionLimit)
}

func uniqueStrings(values []string, maxItems int) []string {
	seen := make(map[string]struct{}, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		output = append(output, text)
		if len(output) >= maxItems {
			break
		}
	}
	return output
}
