package managementaccountlist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxWindowRows   = 1000
)

var allowedStatuses = map[string]struct{}{
	"active": {}, "pending_test": {}, "disabled": {}, "error": {},
	"rate_limited": {}, "temporary_unavailable": {},
}

var allowedSortFields = map[string]struct{}{
	"priority": {}, "superPriority": {}, "fallback": {}, "qualityScore": {}, "name": {}, "type": {},
	"providerCode": {}, "systemAccount": {}, "concurrency": {}, "status": {},
	"accountExpiresAt": {}, "lastUsedAt": {},
}

type Sort struct {
	Field string
	Order string
}

type Input struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	Page                 int
	PageSize             int
	PageSizeProvided     bool
	Keyword              string
	ProviderCode         string
	GroupID              string
	Type                 string
	Statuses             []string
	TagIDs               []string
	Schedulable          string
	Sorts                []Sort
}

type Usage struct {
	RequestCount int64   `json:"requestCount"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	TotalTokens  int64   `json:"totalTokens"`
	TotalCost    float64 `json:"totalCost"`
}

type Permissions struct {
	CanUse    bool `json:"canUse"`
	CanEdit   bool `json:"canEdit"`
	CanDelete bool `json:"canDelete"`
}

type Item struct {
	ID                     string      `json:"id"`
	SystemAccountID        string      `json:"systemAccountId,omitempty"`
	SystemAccountName      string      `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string      `json:"ownerSystemAccountId"`
	OwnerSystemAccountName string      `json:"ownerSystemAccountName,omitempty"`
	Name                   string      `json:"name"`
	ProviderCode           string      `json:"providerCode"`
	Type                   string      `json:"type"`
	Status                 string      `json:"status"`
	Schedulable            bool        `json:"schedulable"`
	ConcurrencyLimit       int         `json:"concurrencyLimit"`
	Priority               int         `json:"priority"`
	SuperPriorityEnabled   bool        `json:"superPriorityEnabled"`
	FallbackEnabled        bool        `json:"fallbackEnabled"`
	HealthCheckModel       string      `json:"healthCheckModel"`
	HealthCheckEndpointMode string     `json:"healthCheckEndpointMode"`
	AccountExpiresAt       *time.Time  `json:"accountExpiresAt,omitempty"`
	LastUsedAt             *time.Time  `json:"lastUsedAt,omitempty"`
	AccessType             string      `json:"accessType"`
	AccountAuthorizationID string      `json:"accountAuthorizationId,omitempty"`
	AuthorizationStatus    string      `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt *time.Time  `json:"authorizationExpiresAt,omitempty"`
	Usage                  Usage       `json:"usage"`
	QualityScore           *int64      `json:"qualityScore,omitempty"`
	Permissions            Permissions `json:"permissions"`
}

type Result struct {
	Items    []Item `json:"items"`
	Total    int    `json:"total"`
	HasMore  bool   `json:"hasMore"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

type Service struct {
	reader port.ManagementAccountListReader
}

func NewService(reader port.ManagementAccountListReader) *Service { return &Service{reader: reader} }

func (s *Service) List(ctx context.Context, input Input) (Result, error) {
	if s == nil || s.reader == nil {
		return Result{}, fmt.Errorf("management account list reader is required")
	}
	actorID := strings.TrimSpace(input.ActorSystemAccountID)
	if actorID == "" {
		return Result{}, fmt.Errorf("management account list actor is required")
	}
	admin := input.ActorRole == "admin" || input.ActorRole == "super_admin"
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if input.SelfOnly || !admin {
		systemAccountID = actorID
	}
	pageSize := defaultPageSize
	if input.PageSizeProvided || input.PageSize != 0 {
		pageSize = min(max(input.PageSize, 1), maxPageSize)
	}
	page := max(input.Page, 1)
	page = min(page, max(1, maxWindowRows/pageSize))
	statuses := normalizeStatuses(input.Statuses)
	schedulable := normalizeSchedulable(input.Schedulable)
	sorts := normalizeSorts(input.Sorts)
	portSorts := make([]port.ManagementAccountListSort, len(sorts))
	for i, sort := range sorts {
		portSorts[i] = port.ManagementAccountListSort{Field: sort.Field, Order: sort.Order}
	}
	stored, err := s.reader.ListManagementAccounts(ctx, port.ManagementAccountListInput{
		SystemAccountID: systemAccountID,
		Keyword:         boundedText(input.Keyword, 128),
		ProviderCode:    boundedText(input.ProviderCode, 64),
		GroupID:         boundedText(input.GroupID, 128),
		Type:            boundedText(input.Type, 32),
		Statuses:        statuses,
		TagIDs:          normalizeTagIDs(input.TagIDs),
		Schedulable:     schedulable,
		Sorts:           portSorts,
		Limit:           pageSize + 1,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return Result{}, err
	}
	rows := stored.Rows
	hasMore := stored.HasMore || len(rows) > pageSize
	if len(rows) > pageSize {
		rows = rows[:pageSize]
	}
	items := make([]Item, 0, len(rows))
	includeSystemAccount := admin && !input.SelfOnly
	for _, row := range rows {
		owner := row.AccessType != "authorized"
		item := Item{
			ID: row.ID, OwnerSystemAccountID: row.SystemAccountID, OwnerSystemAccountName: row.SystemAccountName,
			Name: row.Name, ProviderCode: row.ProviderCode, Type: row.Type, Status: row.Status,
			Schedulable: row.Schedulable, ConcurrencyLimit: row.ConcurrencyLimit, Priority: row.Priority,
			SuperPriorityEnabled: row.SuperPriorityEnabled, FallbackEnabled: row.FallbackEnabled,
			HealthCheckModel: row.HealthCheckModel, HealthCheckEndpointMode: row.HealthCheckEndpointMode,
			AccountExpiresAt: row.AccountExpiresAt, LastUsedAt: row.LastUsedAt, AccessType: row.AccessType,
			AccountAuthorizationID: row.AccountAuthorizationID, AuthorizationStatus: row.AuthorizationStatus,
			AuthorizationExpiresAt: row.AuthorizationExpiresAt,
			Usage:                  Usage{RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.InputTokens + row.OutputTokens, TotalCost: row.TotalCost},
			QualityScore:           row.QualityScore,
			Permissions:            Permissions{CanUse: true, CanEdit: owner, CanDelete: owner},
		}
		if includeSystemAccount {
			item.SystemAccountID = row.SystemAccountID
			item.SystemAccountName = row.SystemAccountName
		}
		items = append(items, item)
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return Result{Items: items, Total: total, HasMore: hasMore, Page: page, PageSize: pageSize}, nil
}

func normalizeStatuses(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := allowedStatuses[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeSchedulable(value string) string {
	switch strings.TrimSpace(value) {
	case "enabled", "disabled", "cooling":
		return strings.TrimSpace(value)
	default:
		return "all"
	}
}

func normalizeSorts(values []Sort) []Sort {
	for _, value := range values {
		if _, ok := allowedSortFields[value.Field]; ok && (value.Order == "asc" || value.Order == "desc") {
			return []Sort{value}
		}
	}
	return nil
}

func normalizeTagIDs(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, min(len(values), 100))
	for _, value := range values {
		value = boundedText(value, 128)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == 100 {
			break
		}
	}
	return result
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
