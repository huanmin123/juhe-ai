package managementaccounts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOptionLimit      = 50
	maxOptionLimit          = 50
	defaultOptionPage       = 1
	optionWindowRows        = 1001
	maxOptionFilterItemSize = 50
)

type Service struct {
	store port.ManagementAccountOptionReader
}

type OptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	GroupID                    string
	TagIDs                     []string
	Type                       string
	Status                     string
	Schedulable                string
	Page                       int
	Limit                      int
}

type TagListInput struct {
	SystemAccountID string
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
	ID                        string              `json:"id"`
	SystemAccountID           string              `json:"systemAccountId,omitempty"`
	SystemAccountName         string              `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID      string              `json:"ownerSystemAccountId"`
	OwnerSystemAccountName    string              `json:"ownerSystemAccountName,omitempty"`
	ProviderCode              string              `json:"providerCode"`
	ProviderProtocolProfileID string              `json:"providerProtocolProfileId"`
	ProtocolCode              string              `json:"protocolCode"`
	ProtocolVersion           string              `json:"protocolVersion"`
	Name                      string              `json:"name"`
	Type                      string              `json:"type"`
	Status                    string              `json:"status"`
	AccessType                string              `json:"accessType"`
	AccountExpiresAt          string              `json:"accountExpiresAt,omitempty"`
	Permissions               ResourcePermissions `json:"permissions"`
}

type Tag struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AccountCount int    `json:"accountCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

func NewService(store port.ManagementAccountOptionReader) *Service {
	return &Service{store: store}
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management account option store is required")
	}
	tagIDs := uniqueStrings(input.TagIDs, 100)
	limit := optionLimit(input.Limit)
	page := optionPage(input.Page, limit)
	rows, err := s.store.ListManagementAccountOptions(ctx, port.ManagementAccountOptionListInput{
		SystemAccountID:            strings.TrimSpace(input.SystemAccountID),
		IncludeSystemAccountFields: input.IncludeSystemAccountFields,
		IDs:                        uniqueStrings(input.IDs, maxOptionFilterItemSize),
		Keyword:                    strings.TrimSpace(input.Keyword),
		ProviderCode:               optionFilterText(input.ProviderCode),
		GroupID:                    strings.TrimSpace(input.GroupID),
		TagIDs:                     tagIDs,
		Type:                       optionFilterText(input.Type),
		Statuses:                   statusValues(input.Status),
		Schedulable:                schedulableFilter(input.Schedulable),
		Limit:                      limit,
		Offset:                     (page - 1) * limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:                        row.ID,
			SystemAccountID:           row.SystemAccountID,
			SystemAccountName:         row.SystemAccountName,
			OwnerSystemAccountID:      row.OwnerSystemAccountID,
			OwnerSystemAccountName:    row.OwnerSystemAccountName,
			ProviderCode:              row.ProviderCode,
			ProviderProtocolProfileID: row.ProviderProtocolProfileID,
			ProtocolCode:              row.ProtocolCode,
			ProtocolVersion:           row.ProtocolVersion,
			Name:                      row.Name,
			Type:                      row.Type,
			Status:                    row.Status,
			AccessType:                "owner",
			AccountExpiresAt:          formatOptionalTime(row.AccountExpiresAt),
			Permissions:               ownerPermissions(),
		})
	}
	return items, nil
}

func (s *Service) Tags(ctx context.Context, input TagListInput) ([]Tag, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management account tag store is required")
	}
	rows, err := s.store.ListManagementAccountTags(ctx, port.ManagementAccountTagListInput{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil {
		return nil, err
	}
	items := make([]Tag, 0, len(rows))
	for _, row := range rows {
		accountCount := row.AccountCount
		if accountCount < 0 {
			accountCount = 0
		}
		items = append(items, Tag{
			ID:           row.ID,
			Name:         row.Name,
			AccountCount: accountCount,
			CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:    row.UpdatedAt.UTC().Format(time.RFC3339Nano),
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

func optionPage(page int, pageSize int) int {
	if page < 1 {
		return defaultOptionPage
	}
	maxPage := max(1, (optionWindowRows-1)/max(1, pageSize))
	return min(page, maxPage)
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

func optionFilterText(value string) string {
	text := strings.TrimSpace(value)
	if text == "all" {
		return ""
	}
	return text
}

func statusValues(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		text := optionFilterText(part)
		if text == "" {
			continue
		}
		values = append(values, text)
	}
	return uniqueStrings(values, 20)
}

func schedulableFilter(value string) string {
	switch strings.TrimSpace(value) {
	case "enabled", "disabled", "cooling":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
