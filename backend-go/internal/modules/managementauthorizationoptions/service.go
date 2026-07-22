package managementauthorizationoptions

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPrincipalOptionLimit = 50
	maxPrincipalOptionLimit     = 50
	maxPrincipalOptionItems     = 50
)

type Service struct {
	store port.ManagementAuthorizationOptionReader
}

type PrincipalOptionListInput struct {
	IDs     []string
	Keyword string
	Limit   int
}

type GranteeGroupOptionListInput struct {
	GranteeSystemAccountID string
	IDs                    []string
	Keyword                string
	ProviderCode           string
	Limit                  int
	PreferDefault          bool
}

type GranteeAccountOption struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

type GranteeTeamOption struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type GranteeGroupOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func NewService(store port.ManagementAuthorizationOptionReader) *Service {
	return &Service{store: store}
}

func (s *Service) GranteeAccounts(ctx context.Context, input PrincipalOptionListInput) ([]GranteeAccountOption, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management authorization option store is required")
	}
	rows, err := s.store.ListManagementAuthorizationGranteeAccounts(ctx, port.ManagementAuthorizationPrincipalOptionListInput{
		IDs:     uniqueStrings(input.IDs, maxPrincipalOptionItems),
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   principalOptionLimit(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]GranteeAccountOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, GranteeAccountOption{
			ID:          row.ID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Status:      row.Status,
		})
	}
	return items, nil
}

func (s *Service) GranteeTeams(ctx context.Context, input PrincipalOptionListInput) ([]GranteeTeamOption, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management authorization option store is required")
	}
	rows, err := s.store.ListManagementAuthorizationGranteeTeams(ctx, port.ManagementAuthorizationPrincipalOptionListInput{
		IDs:     uniqueStrings(input.IDs, maxPrincipalOptionItems),
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   principalOptionLimit(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]GranteeTeamOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, GranteeTeamOption{
			ID:     row.ID,
			Name:   row.Name,
			Status: row.Status,
		})
	}
	return items, nil
}

func (s *Service) GranteeGroups(ctx context.Context, input GranteeGroupOptionListInput) ([]GranteeGroupOption, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management authorization option store is required")
	}
	rows, err := s.store.ListManagementAuthorizationGranteeGroups(ctx, port.ManagementAuthorizationGranteeGroupOptionListInput{
		GranteeSystemAccountID: strings.TrimSpace(input.GranteeSystemAccountID),
		IDs:                    uniqueStrings(input.IDs, maxPrincipalOptionItems),
		Keyword:                strings.TrimSpace(input.Keyword),
		ProviderCode:           strings.TrimSpace(input.ProviderCode),
		Limit:                  principalOptionLimit(input.Limit),
		PreferDefault:          input.PreferDefault,
	})
	if err != nil {
		return nil, err
	}
	items := make([]GranteeGroupOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, GranteeGroupOption{
			ID:   row.ID,
			Name: row.Name,
		})
	}
	return items, nil
}

func principalOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultPrincipalOptionLimit
	}
	return min(limit, maxPrincipalOptionLimit)
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
