package managementgroups

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOptionLimit = 50
	maxOptionLimit     = 50
)

type Service struct {
	store port.ManagementGroupOptionReader
}

type OptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	Limit                      int
	ManageableOnly             bool
	PreferDefault              bool
}

type ResourcePermissions struct {
	CanUse                 bool `json:"canUse"`
	CanEdit                bool `json:"canEdit"`
	CanDelete              bool `json:"canDelete"`
	CanReturnAuthorization bool `json:"canReturnAuthorization"`
	CanAuthorize           bool `json:"canAuthorize"`
	CanViewCredentials     bool `json:"canViewCredentials"`
	CanManageAccounts      bool `json:"canManageAccounts"`
	CanBindToAPIKey        bool `json:"canBindToApiKey"`
}

type Option struct {
	ID                     string              `json:"id"`
	SystemAccountID        string              `json:"systemAccountId,omitempty"`
	SystemAccountName      string              `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string              `json:"ownerSystemAccountId"`
	OwnerSystemAccountName string              `json:"ownerSystemAccountName,omitempty"`
	Name                   string              `json:"name"`
	ProviderCode           string              `json:"providerCode"`
	Enabled                bool                `json:"enabled"`
	IsDefault              bool                `json:"isDefault"`
	GroupType              string              `json:"groupType"`
	SchedulingPolicy       map[string]any      `json:"schedulingPolicy,omitempty"`
	AccessType             string              `json:"accessType"`
	Permissions            ResourcePermissions `json:"permissions"`
}

func NewService(store port.ManagementGroupOptionReader) *Service {
	return &Service{store: store}
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management group option store is required")
	}
	rows, err := s.store.ListManagementGroupOptions(ctx, port.ManagementGroupOptionListInput{
		SystemAccountID:            strings.TrimSpace(input.SystemAccountID),
		IncludeSystemAccountFields: input.IncludeSystemAccountFields,
		IDs:                        uniqueStrings(input.IDs, 50),
		Keyword:                    strings.TrimSpace(input.Keyword),
		ProviderCode:               strings.TrimSpace(input.ProviderCode),
		Limit:                      optionLimit(input.Limit),
		PreferDefault:              input.PreferDefault,
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:                     row.ID,
			SystemAccountID:        row.SystemAccountID,
			SystemAccountName:      row.SystemAccountName,
			OwnerSystemAccountID:   row.OwnerSystemAccountID,
			OwnerSystemAccountName: row.OwnerSystemAccountName,
			Name:                   row.Name,
			ProviderCode:           row.ProviderCode,
			Enabled:                row.Enabled,
			IsDefault:              row.IsDefault,
			GroupType:              row.GroupType,
			SchedulingPolicy:       row.SchedulingPolicy,
			AccessType:             "owner",
			Permissions:            ownerPermissions(),
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

func ownerPermissions() ResourcePermissions {
	return ResourcePermissions{
		CanUse:                 true,
		CanEdit:                true,
		CanDelete:              true,
		CanReturnAuthorization: false,
		CanAuthorize:           true,
		CanViewCredentials:     true,
		CanManageAccounts:      true,
		CanBindToAPIKey:        true,
	}
}
