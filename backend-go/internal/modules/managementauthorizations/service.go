package managementauthorizations

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	maxRemarkRunes                            = 200
	defaultAuthorizationListPageSize          = 50
	maxAuthorizationListPageSize              = 500
	maxAuthorizationListWindowRows            = 1001
	maxRequestQuotaHourlyWindowHours          = 24 * 30
	maxRequestQuotaAmountUSD                  = 9_007_199_254_740_991
	quotaAmountPrecision                int64 = 1_000_000
	ResourceAuthorizationCreatedReason        = "resource_authorization_created"
	ResourceAuthorizationUpdatedReason        = "resource_authorization_updated"
	ResourceAuthorizationReturnedReason       = "resource_authorization_returned"
	ResourceAuthorizationRevokedReason        = "resource_authorization_revoked"
)

var (
	ErrAuthorizationListInvalid   = errors.New("management authorization list invalid")
	ErrAuthorizationCreateInvalid = errors.New("management authorization create invalid")
	ErrAuthorizationUpdateInvalid = errors.New("management authorization update invalid")
	ErrAuthorizationReturnInvalid = errors.New("management authorization return invalid")
	ErrAuthorizationRevokeInvalid = errors.New("management authorization revoke invalid")
)

type Service struct {
	listStore                port.ManagementResourceAuthorizationLister
	getStore                 port.ManagementResourceAuthorizationGetter
	createStore              port.ManagementResourceAuthorizationCreator
	updateStore              port.ManagementResourceAuthorizationUpdater
	returnStore              port.ManagementResourceAuthorizationReturner
	revokeStore              port.ManagementResourceAuthorizationRevoker
	now                      func() time.Time
	secret                   string
	authorizationInvalidator AuthorizationInvalidator
}

type AuthorizationInvalidator interface {
	InvalidateAuthorizationChanged(ctx context.Context, reason string) error
}

type ServiceOptions struct {
	ListStore                port.ManagementResourceAuthorizationLister
	GetStore                 port.ManagementResourceAuthorizationGetter
	Store                    port.ManagementResourceAuthorizationCreator
	UpdateStore              port.ManagementResourceAuthorizationUpdater
	ReturnStore              port.ManagementResourceAuthorizationReturner
	RevokeStore              port.ManagementResourceAuthorizationRevoker
	Now                      func() time.Time
	Secret                   string
	AuthorizationInvalidator AuthorizationInvalidator
}

type ListInput struct {
	ActorSystemAccountID         string
	ActorRole                    string
	ScopedSystemAccountID        string
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	TeamID                       string
	Status                       string
	Direction                    string
	SourceType                   string
	Keyword                      string
	Page                         int
	PageSize                     int
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type Detail struct {
	Summary
	Permissions Permissions `json:"permissions"`
}

type ListItem struct {
	ID                             string        `json:"id"`
	ResourceType                   string        `json:"resourceType"`
	ResourceID                     string        `json:"resourceId"`
	ResourceName                   string        `json:"resourceName,omitempty"`
	ResourceOwnerSystemAccountID   string        `json:"resourceOwnerSystemAccountId"`
	ResourceOwnerSystemAccountName string        `json:"resourceOwnerSystemAccountName,omitempty"`
	GranteeType                    string        `json:"granteeType,omitempty"`
	GranteeSystemAccountID         string        `json:"granteeSystemAccountId,omitempty"`
	GranteeSystemAccountName       string        `json:"granteeSystemAccountName,omitempty"`
	GranteeUsername                string        `json:"granteeUsername,omitempty"`
	GranteeTeamID                  string        `json:"granteeTeamId,omitempty"`
	GranteeTeamName                string        `json:"granteeTeamName,omitempty"`
	Scope                          string        `json:"scope"`
	Status                         string        `json:"status"`
	Remark                         string        `json:"remark,omitempty"`
	ExpiresAt                      *time.Time    `json:"expiresAt,omitempty"`
	EffectiveSourceType            string        `json:"effectiveSourceType,omitempty"`
	EffectiveSourceTeamID          string        `json:"effectiveSourceTeamId,omitempty"`
	EffectiveSourceTeamName        string        `json:"effectiveSourceTeamName,omitempty"`
	ActivatedAt                    *time.Time    `json:"activatedAt,omitempty"`
	LastSourceChangedAt            *time.Time    `json:"lastSourceChangedAt,omitempty"`
	LastUsedAt                     *time.Time    `json:"lastUsedAt,omitempty"`
	CreatedBy                      string        `json:"createdBy"`
	CreatedAt                      time.Time     `json:"createdAt"`
	RevokedBy                      string        `json:"revokedBy,omitempty"`
	RevokedAt                      *time.Time    `json:"revokedAt,omitempty"`
	RevokedReason                  string        `json:"revokedReason,omitempty"`
	UpdatedAt                      time.Time     `json:"updatedAt"`
	Permissions                    Permissions   `json:"permissions"`
	SourceSummary                  SourceSummary `json:"sourceSummary"`
}

type Permissions struct {
	CanEdit      bool `json:"canEdit"`
	CanAuthorize bool `json:"canAuthorize"`
}

type SourceSummary struct {
	ActiveSourceCount int              `json:"activeSourceCount"`
	HasManual         bool             `json:"hasManual"`
	HasTeam           bool             `json:"hasTeam"`
	TeamSources       []TeamSourceItem `json:"teamSources"`
}

type TeamSourceItem struct {
	SourceTeamID   string `json:"sourceTeamId"`
	SourceTeamName string `json:"sourceTeamName,omitempty"`
}

type CreateInput struct {
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeType                  string
	GranteeID                    string
	TargetGroupID                string
	Remark                       string
	HasRemark                    bool
	ExpiresAt                    string
	HasExpiresAt                 bool
	Limits                       map[string]any
	HasLimits                    bool
	ActorSystemAccountID         string
}

type ReturnInput struct {
	AuthorizationID        string
	GranteeSystemAccountID string
	ActorSystemAccountID   string
}

type UpdateInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	ActorRole             string
	ScopedSystemAccountID string
	HasStatus             bool
	Status                string
	HasExpiresAt          bool
	ExpiresAt             *string
	HasLimits             bool
	Limits                map[string]any
	LimitsIsNull          bool
}

type RevokeInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	ActorRole             string
	ScopedSystemAccountID string
}

type GetInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	ActorRole             string
	ScopedSystemAccountID string
}

type Summary = port.ManagementResourceAuthorizationSummary

func NewService(store port.ManagementResourceAuthorizationCreator) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	returnStore := opts.ReturnStore
	if returnStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationReturner); ok {
			returnStore = candidate
		}
	}
	listStore := opts.ListStore
	if listStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationLister); ok {
			listStore = candidate
		}
	}
	getStore := opts.GetStore
	if getStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationGetter); ok {
			getStore = candidate
		}
	}
	updateStore := opts.UpdateStore
	if updateStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationUpdater); ok {
			updateStore = candidate
		}
	}
	revokeStore := opts.RevokeStore
	if revokeStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationRevoker); ok {
			revokeStore = candidate
		}
	}
	return &Service{
		listStore:                listStore,
		getStore:                 getStore,
		createStore:              opts.Store,
		updateStore:              updateStore,
		returnStore:              returnStore,
		revokeStore:              revokeStore,
		now:                      now,
		secret:                   opts.Secret,
		authorizationInvalidator: opts.AuthorizationInvalidator,
	}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.listStore == nil {
		return ListResult{}, fmt.Errorf("management resource authorization lister is required")
	}
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if actor == "" {
		return ListResult{}, ErrAuthorizationListInvalid
	}
	resourceType := normalizeResourceType(input.ResourceType)
	if strings.TrimSpace(input.ResourceType) != "" && resourceType == "" {
		return ListResult{}, ErrAuthorizationListInvalid
	}
	status := normalizeAuthorizationStatus(input.Status)
	if strings.TrimSpace(input.Status) != "" && strings.TrimSpace(input.Status) != "all" && status == "" {
		return ListResult{}, ErrAuthorizationListInvalid
	}
	direction := normalizeAuthorizationDirection(input.Direction)
	if strings.TrimSpace(input.Direction) != "" && strings.TrimSpace(input.Direction) != "all" && direction == "" {
		return ListResult{}, ErrAuthorizationListInvalid
	}
	sourceType := normalizeAuthorizationSourceType(input.SourceType)
	if strings.TrimSpace(input.SourceType) != "" && strings.TrimSpace(input.SourceType) != "all" && sourceType == "" {
		return ListResult{}, ErrAuthorizationListInvalid
	}
	keyword := strings.TrimSpace(input.Keyword)
	if utf8.RuneCountInString(keyword) > 120 {
		return ListResult{}, ErrAuthorizationListInvalid
	}
	pageSize := authorizationListPageSize(input.PageSize)
	page := authorizationListPage(input.Page, pageSize)
	canAccessAll := isAdminRole(input.ActorRole)
	scopedSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if !canAccessAll {
		scopedSystemAccountID = actor
	}
	result, err := s.listStore.ListManagementResourceAuthorizations(ctx, port.ManagementResourceAuthorizationListInput{
		ActorSystemAccountID:         actor,
		CanAccessAll:                 canAccessAll,
		ScopedSystemAccountID:        scopedSystemAccountID,
		ResourceType:                 resourceType,
		ResourceID:                   strings.TrimSpace(input.ResourceID),
		ResourceOwnerSystemAccountID: strings.TrimSpace(input.ResourceOwnerSystemAccountID),
		GranteeSystemAccountID:       strings.TrimSpace(input.GranteeSystemAccountID),
		TeamID:                       strings.TrimSpace(input.TeamID),
		Status:                       status,
		Direction:                    direction,
		SourceType:                   sourceType,
		Keyword:                      keyword,
		Limit:                        pageSize + 1,
		Offset:                       (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]ListItem, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, listItemFromSummary(row, canManageAuthorizationResourceOwner(row.ResourceOwnerSystemAccountID, canAccessAll, scopedSystemAccountID)))
	}
	return ListResult{
		Items:    items,
		Total:    authorizationPagedTotalUpperBound(page, pageSize, len(items), result.HasMore),
		HasMore:  result.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Get(ctx context.Context, input GetInput) (Detail, bool, error) {
	if s.getStore == nil {
		return Detail{}, false, fmt.Errorf("management resource authorization getter is required")
	}
	authorizationID := strings.TrimSpace(input.AuthorizationID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if authorizationID == "" || actor == "" {
		return Detail{}, false, ErrAuthorizationListInvalid
	}
	canAccessAll := isAdminRole(input.ActorRole)
	scopedSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if !canAccessAll {
		scopedSystemAccountID = actor
	}
	row, found, err := s.getStore.FindManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationGetInput{
		AuthorizationID:       authorizationID,
		ActorSystemAccountID:  actor,
		CanAccessAll:          canAccessAll,
		ScopedSystemAccountID: scopedSystemAccountID,
	})
	if err != nil || !found {
		return Detail{}, found, err
	}
	return detailFromSummary(row, canManageAuthorizationResourceOwner(row.ResourceOwnerSystemAccountID, canAccessAll, scopedSystemAccountID)), true, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Summary, error) {
	if s.createStore == nil {
		return Summary{}, fmt.Errorf("management resource authorization creator is required")
	}
	now := s.now().UTC()
	resourceType := normalizeResourceType(input.ResourceType)
	resourceID := strings.TrimSpace(input.ResourceID)
	ownerID := strings.TrimSpace(input.ResourceOwnerSystemAccountID)
	granteeType := normalizeGranteeType(input.GranteeType)
	granteeID := strings.TrimSpace(input.GranteeID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if resourceType == "" || resourceID == "" || ownerID == "" || granteeType == "" || granteeID == "" || actor == "" {
		return Summary{}, ErrAuthorizationCreateInvalid
	}

	targetGroupID := strings.TrimSpace(input.TargetGroupID)
	if resourceType == "account" && granteeType == "system_account" && targetGroupID == "" {
		return Summary{}, fmt.Errorf("授权 AI 账户给个人时必须选择目标分组")
	}
	if targetGroupID != "" && (resourceType != "account" || granteeType != "system_account") {
		return Summary{}, fmt.Errorf("只有授权 AI 账户给个人时可以指定目标分组")
	}

	remark := strings.TrimSpace(input.Remark)
	hasRemark := input.HasRemark && remark != ""
	if utf8.RuneCountInString(remark) > maxRemarkRunes {
		return Summary{}, ErrAuthorizationCreateInvalid
	}

	var expiresAt *time.Time
	if input.HasExpiresAt {
		parsed, err := parseServerDateTime(input.ExpiresAt)
		if err != nil {
			return Summary{}, err
		}
		if !parsed.After(now) {
			return Summary{}, fmt.Errorf("授权到期时间不能早于当前时间")
		}
		expiresAt = &parsed
	}

	limits, limitsJSON, hourlyWindowHours, err := normalizeRequestQuotaLimits(input.Limits, input.HasLimits)
	if err != nil {
		return Summary{}, err
	}
	secretJSON, err := s.encryptJSON(map[string]any{})
	if err != nil {
		return Summary{}, fmt.Errorf("encrypt authorization instance credential: %w", err)
	}

	storeInput := port.ManagementResourceAuthorizationCreateInput{
		ResourceType:                    resourceType,
		ResourceID:                      resourceID,
		ResourceOwnerSystemAccountID:    ownerID,
		GranteeType:                     granteeType,
		GranteeID:                       granteeID,
		TargetGroupID:                   targetGroupID,
		Remark:                          remark,
		HasRemark:                       hasRemark,
		ExpiresAt:                       expiresAt,
		Limits:                          limits,
		LimitsJSON:                      limitsJSON,
		LimitHourlyWindowHours:          hourlyWindowHours,
		AuthorizationInstanceSecretJSON: secretJSON,
		ActorSystemAccountID:            actor,
		CreatedAt:                       now,
	}
	row, err := s.createStore.CreateManagementResourceAuthorization(ctx, storeInput)
	if err != nil {
		return Summary{}, err
	}
	if s.authorizationInvalidator != nil {
		if err := s.authorizationInvalidator.InvalidateAuthorizationChanged(ctx, ResourceAuthorizationCreatedReason); err != nil {
			return Summary{}, err
		}
	}
	return row, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (Summary, bool, error) {
	if s.updateStore == nil {
		return Summary{}, false, fmt.Errorf("management resource authorization updater is required")
	}
	now := s.now().UTC()
	authorizationID := strings.TrimSpace(input.AuthorizationID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if authorizationID == "" || actor == "" {
		return Summary{}, false, ErrAuthorizationUpdateInvalid
	}
	if !input.HasStatus && !input.HasExpiresAt && !input.HasLimits {
		return Summary{}, false, ErrAuthorizationUpdateInvalid
	}
	status := ""
	if input.HasStatus {
		status = strings.TrimSpace(input.Status)
		if status != "active" && status != "paused" {
			return Summary{}, false, ErrAuthorizationUpdateInvalid
		}
	}
	var expiresAt *time.Time
	if input.HasExpiresAt && input.ExpiresAt != nil {
		rawExpiresAt := strings.TrimSpace(*input.ExpiresAt)
		if rawExpiresAt == "" {
			return Summary{}, false, ErrAuthorizationUpdateInvalid
		}
		parsed, err := parseServerDateTime(rawExpiresAt)
		if err != nil {
			return Summary{}, false, err
		}
		if !parsed.After(now) {
			return Summary{}, false, fmt.Errorf("授权到期时间不能早于当前时间")
		}
		expiresAt = &parsed
	}
	var limitsJSON *string
	hourlyWindowHours := 0
	if input.HasLimits && !input.LimitsIsNull {
		_, normalizedLimitsJSON, normalizedHourlyWindowHours, err := normalizeRequestQuotaLimits(input.Limits, true)
		if err != nil {
			return Summary{}, false, err
		}
		limitsJSON = normalizedLimitsJSON
		hourlyWindowHours = normalizedHourlyWindowHours
	}
	canAccessAll := isAdminRole(input.ActorRole)
	scopedSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if !canAccessAll {
		scopedSystemAccountID = actor
	}
	row, found, err := s.updateStore.UpdateManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationUpdateInput{
		AuthorizationID:        authorizationID,
		ActorSystemAccountID:   actor,
		CanAccessAll:           canAccessAll,
		ScopedSystemAccountID:  scopedSystemAccountID,
		HasStatus:              input.HasStatus,
		Status:                 status,
		HasExpiresAt:           input.HasExpiresAt,
		ExpiresAt:              expiresAt,
		HasLimits:              input.HasLimits,
		LimitsJSON:             limitsJSON,
		LimitHourlyWindowHours: hourlyWindowHours,
		UpdatedAt:              now,
	})
	if err != nil {
		return Summary{}, false, err
	}
	if !found {
		return Summary{}, false, nil
	}
	if s.authorizationInvalidator != nil {
		if err := s.authorizationInvalidator.InvalidateAuthorizationChanged(ctx, ResourceAuthorizationUpdatedReason); err != nil {
			return Summary{}, false, err
		}
	}
	return row, true, nil
}

func (s *Service) Return(ctx context.Context, input ReturnInput) (Summary, bool, error) {
	if s.returnStore == nil {
		return Summary{}, false, fmt.Errorf("management resource authorization returner is required")
	}
	now := s.now().UTC()
	authorizationID := strings.TrimSpace(input.AuthorizationID)
	granteeSystemAccountID := strings.TrimSpace(input.GranteeSystemAccountID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if authorizationID == "" || granteeSystemAccountID == "" || actor == "" {
		return Summary{}, false, ErrAuthorizationReturnInvalid
	}
	row, found, err := s.returnStore.ReturnManagementResourceAuthorizationForGrantee(ctx, port.ManagementResourceAuthorizationReturnInput{
		AuthorizationID:        authorizationID,
		GranteeSystemAccountID: granteeSystemAccountID,
		ActorSystemAccountID:   actor,
		ReturnedAt:             now,
	})
	if err != nil {
		return Summary{}, false, err
	}
	if !found {
		return Summary{}, false, nil
	}
	if s.authorizationInvalidator != nil {
		if err := s.authorizationInvalidator.InvalidateAuthorizationChanged(ctx, ResourceAuthorizationReturnedReason); err != nil {
			return Summary{}, false, err
		}
	}
	return row, true, nil
}

func (s *Service) Revoke(ctx context.Context, input RevokeInput) (Summary, bool, error) {
	if s.revokeStore == nil {
		return Summary{}, false, fmt.Errorf("management resource authorization revoker is required")
	}
	now := s.now().UTC()
	authorizationID := strings.TrimSpace(input.AuthorizationID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if authorizationID == "" || actor == "" {
		return Summary{}, false, ErrAuthorizationRevokeInvalid
	}
	canAccessAll := isAdminRole(input.ActorRole)
	scopedSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if !canAccessAll {
		scopedSystemAccountID = actor
	}
	row, found, err := s.revokeStore.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
		AuthorizationID:       authorizationID,
		ActorSystemAccountID:  actor,
		CanAccessAll:          canAccessAll,
		ScopedSystemAccountID: scopedSystemAccountID,
		RevokedAt:             now,
	})
	if err != nil {
		return Summary{}, false, err
	}
	if !found {
		return Summary{}, false, nil
	}
	if s.authorizationInvalidator != nil {
		if err := s.authorizationInvalidator.InvalidateAuthorizationChanged(ctx, ResourceAuthorizationRevokedReason); err != nil {
			return Summary{}, false, err
		}
	}
	return row, true, nil
}

func normalizeResourceType(value string) string {
	switch strings.TrimSpace(value) {
	case "account":
		return "account"
	case "group":
		return "group"
	default:
		return ""
	}
}

func normalizeGranteeType(value string) string {
	switch strings.TrimSpace(value) {
	case "system_account":
		return "system_account"
	case "team":
		return "team"
	default:
		return ""
	}
}

func normalizeAuthorizationStatus(value string) string {
	switch strings.TrimSpace(value) {
	case "active", "paused", "expired", "revoked", "returned":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func normalizeAuthorizationDirection(value string) string {
	switch strings.TrimSpace(value) {
	case "outbound", "inbound":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func normalizeAuthorizationSourceType(value string) string {
	switch strings.TrimSpace(value) {
	case "manual", "team":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func isAdminRole(role string) bool {
	return role == "admin" || role == "super_admin"
}

func authorizationListPageSize(value int) int {
	if value <= 0 {
		return defaultAuthorizationListPageSize
	}
	if value > maxAuthorizationListPageSize {
		return maxAuthorizationListPageSize
	}
	return value
}

func authorizationListPage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	maxPage := max(1, (maxAuthorizationListWindowRows-1)/max(1, pageSize))
	if value > maxPage {
		return maxPage
	}
	return value
}

func authorizationPagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	base := (max(1, page)-1)*max(0, pageSize) + itemCount
	if hasMore {
		return base + 1
	}
	return base
}

func canManageAuthorizationResourceOwner(ownerSystemAccountID string, canAccessAll bool, scopedSystemAccountID string) bool {
	if scopedSystemAccountID != "" {
		return scopedSystemAccountID == ownerSystemAccountID
	}
	return canAccessAll
}

func listItemFromSummary(summary Summary, canManage bool) ListItem {
	item := ListItem{
		ID:                             summary.ID,
		ResourceType:                   summary.ResourceType,
		ResourceID:                     summary.ResourceID,
		ResourceName:                   summary.ResourceName,
		ResourceOwnerSystemAccountID:   summary.ResourceOwnerSystemAccountID,
		ResourceOwnerSystemAccountName: summary.ResourceOwnerSystemAccountName,
		GranteeType:                    summary.GranteeType,
		GranteeSystemAccountID:         summary.GranteeSystemAccountID,
		GranteeSystemAccountName:       summary.GranteeSystemAccountName,
		GranteeUsername:                summary.GranteeUsername,
		GranteeTeamID:                  summary.GranteeTeamID,
		GranteeTeamName:                summary.GranteeTeamName,
		Scope:                          summary.Scope,
		Status:                         summary.Status,
		Remark:                         summary.Remark,
		ExpiresAt:                      summary.ExpiresAt,
		EffectiveSourceType:            summary.EffectiveSourceType,
		EffectiveSourceTeamID:          summary.EffectiveSourceTeamID,
		EffectiveSourceTeamName:        summary.EffectiveSourceTeamName,
		ActivatedAt:                    summary.ActivatedAt,
		LastSourceChangedAt:            summary.LastSourceChangedAt,
		LastUsedAt:                     summary.LastUsedAt,
		CreatedBy:                      summary.CreatedBy,
		CreatedAt:                      summary.CreatedAt,
		RevokedBy:                      summary.RevokedBy,
		RevokedAt:                      summary.RevokedAt,
		RevokedReason:                  summary.RevokedReason,
		UpdatedAt:                      summary.UpdatedAt,
		Permissions: Permissions{
			CanEdit:      canManage,
			CanAuthorize: canManage,
		},
		SourceSummary: sourceSummary(summary.AuthorizationSources, canManage),
	}
	if !canManage {
		item.EffectiveSourceTeamID = ""
		item.EffectiveSourceTeamName = ""
		item.CreatedBy = ""
		item.RevokedBy = ""
	}
	return item
}

func detailFromSummary(summary Summary, canManage bool) Detail {
	if !canManage {
		summary.EffectiveSourceTeamID = ""
		summary.EffectiveSourceTeamName = ""
		summary.AuthorizationSources = sanitizeAuthorizationSourcesForViewer(summary.AuthorizationSources)
		summary.CreatedBy = ""
		summary.RevokedBy = ""
	}
	return Detail{
		Summary: summary,
		Permissions: Permissions{
			CanEdit:      canManage,
			CanAuthorize: canManage,
		},
	}
}

func sanitizeAuthorizationSourcesForViewer(sources []port.ManagementResourceAuthorizationSourceSummary) []port.ManagementResourceAuthorizationSourceSummary {
	if sources == nil {
		return nil
	}
	out := make([]port.ManagementResourceAuthorizationSourceSummary, 0, len(sources))
	for _, source := range sources {
		out = append(out, port.ManagementResourceAuthorizationSourceSummary{
			ID:              source.ID,
			AuthorizationID: source.AuthorizationID,
			SourceType:      source.SourceType,
			SourceTeamName:  source.SourceTeamName,
			Status:          source.Status,
			ActivatedAt:     source.ActivatedAt,
			EndedReason:     source.EndedReason,
			CreatedAt:       source.CreatedAt,
			UpdatedAt:       source.UpdatedAt,
		})
	}
	return out
}

func sourceSummary(sources []port.ManagementResourceAuthorizationSourceSummary, canManage bool) SourceSummary {
	result := SourceSummary{TeamSources: []TeamSourceItem{}}
	for _, source := range sources {
		if source.Status != "active" {
			continue
		}
		result.ActiveSourceCount++
		if source.SourceType == "manual" {
			result.HasManual = true
		}
		if source.SourceType == "team" {
			result.HasTeam = true
			if canManage && strings.TrimSpace(source.SourceTeamID) != "" {
				result.TeamSources = append(result.TeamSources, TeamSourceItem{
					SourceTeamID:   source.SourceTeamID,
					SourceTeamName: source.SourceTeamName,
				})
			}
		}
	}
	return result
}

func parseServerDateTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, fmt.Errorf("过期时间格式不正确")
	}
	if !serverDateTimePattern(text) {
		return time.Time{}, fmt.Errorf("过期时间格式不正确")
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", text)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05Z", text)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("过期时间格式不正确")
	}
	return parsed.UTC(), nil
}

func serverDateTimePattern(value string) bool {
	if len(value) != len("2006-01-02T15:04:05Z") && len(value) != len("2006-01-02T15:04:05.000Z") {
		return false
	}
	for index, char := range value {
		switch index {
		case 4, 7:
			if char != '-' {
				return false
			}
		case 10:
			if char != 'T' {
				return false
			}
		case 13, 16:
			if char != ':' {
				return false
			}
		case 19:
			if len(value) == len("2006-01-02T15:04:05Z") {
				if char != 'Z' {
					return false
				}
			} else if char != '.' {
				return false
			}
		case 23:
			if len(value) == len("2006-01-02T15:04:05.000Z") && char != 'Z' {
				return false
			}
		default:
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func normalizeRequestQuotaLimits(value map[string]any, hasValue bool) (port.ManagementRequestQuotaLimits, *string, int, error) {
	if !hasValue {
		return port.ManagementRequestQuotaLimits{}, nil, 0, nil
	}
	if value == nil {
		return port.ManagementRequestQuotaLimits{}, nil, 0, fmt.Errorf("请求额度限制参数无效")
	}
	allowed := map[string]bool{
		"hourly":  true,
		"daily":   true,
		"weekly":  true,
		"monthly": true,
		"total":   true,
	}
	for key := range value {
		if !allowed[key] {
			return port.ManagementRequestQuotaLimits{}, nil, 0, fmt.Errorf("请求额度限制包含不支持字段：%s", key)
		}
	}
	limits := port.ManagementRequestQuotaLimits{}
	var hourlyWindowHours int
	if item, exists := value["hourly"]; exists {
		limit, err := normalizeHourlyQuotaLimit(item)
		if err != nil {
			return port.ManagementRequestQuotaLimits{}, nil, 0, err
		}
		limits.Hourly = &limit
		hourlyWindowHours = limit.Hours
	}
	for _, key := range []string{"daily", "weekly", "monthly", "total"} {
		item, exists := value[key]
		if !exists {
			continue
		}
		limit, err := normalizeQuotaLimit(item, quotaLimitLabel(key))
		if err != nil {
			return port.ManagementRequestQuotaLimits{}, nil, 0, err
		}
		switch key {
		case "daily":
			limits.Daily = &limit
		case "weekly":
			limits.Weekly = &limit
		case "monthly":
			limits.Monthly = &limit
		case "total":
			limits.Total = &limit
		}
	}
	if !hasEnabledRequestQuotaLimit(limits) {
		return port.ManagementRequestQuotaLimits{}, nil, 0, nil
	}
	jsonText, err := marshalQuotaLimits(limits)
	if err != nil {
		return port.ManagementRequestQuotaLimits{}, nil, 0, err
	}
	return limits, &jsonText, hourlyWindowHours, nil
}

func normalizeHourlyQuotaLimit(value any) (port.ManagementRequestHourlyQuotaLimit, error) {
	fields, ok := value.(map[string]any)
	if !ok || fields == nil {
		return port.ManagementRequestHourlyQuotaLimit{}, fmt.Errorf("小时额度参数无效")
	}
	if err := assertQuotaLimitKeys(fields, map[string]bool{"enabled": true, "limit": true, "hours": true}, "小时额度"); err != nil {
		return port.ManagementRequestHourlyQuotaLimit{}, err
	}
	if enabled, ok := fields["enabled"].(bool); !ok || !enabled {
		return port.ManagementRequestHourlyQuotaLimit{}, fmt.Errorf("小时额度启用状态必须为 true")
	}
	amount, err := positiveAmount(fields["limit"], "小时额度")
	if err != nil {
		return port.ManagementRequestHourlyQuotaLimit{}, err
	}
	hours, err := positiveInteger(fields["hours"], "小时额度窗口")
	if err != nil {
		return port.ManagementRequestHourlyQuotaLimit{}, err
	}
	return port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: hours, Limit: amount}, nil
}

func normalizeQuotaLimit(value any, label string) (port.ManagementRequestQuotaLimit, error) {
	fields, ok := value.(map[string]any)
	if !ok || fields == nil {
		return port.ManagementRequestQuotaLimit{}, fmt.Errorf("%s参数无效", label)
	}
	if err := assertQuotaLimitKeys(fields, map[string]bool{"enabled": true, "limit": true}, label); err != nil {
		return port.ManagementRequestQuotaLimit{}, err
	}
	if enabled, ok := fields["enabled"].(bool); !ok || !enabled {
		return port.ManagementRequestQuotaLimit{}, fmt.Errorf("%s启用状态必须为 true", label)
	}
	amount, err := positiveAmount(fields["limit"], label)
	if err != nil {
		return port.ManagementRequestQuotaLimit{}, err
	}
	return port.ManagementRequestQuotaLimit{Enabled: true, Limit: amount}, nil
}

func assertQuotaLimitKeys(value map[string]any, allowed map[string]bool, label string) error {
	for key := range value {
		if !allowed[key] {
			return fmt.Errorf("%s包含不支持字段：%s", label, key)
		}
	}
	return nil
}

func positiveInteger(value any, label string) (int, error) {
	number, ok := jsonNumber(value)
	if !ok || math.Trunc(number) != number {
		return 0, fmt.Errorf("%s必须是数字", label)
	}
	if number <= 0 || number > maxRequestQuotaHourlyWindowHours {
		return 0, fmt.Errorf("%s必须在 1-%d 之间", label, maxRequestQuotaHourlyWindowHours)
	}
	return int(number), nil
}

func positiveAmount(value any, label string) (float64, error) {
	number, ok := jsonNumber(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || number > maxRequestQuotaAmountUSD {
		return 0, fmt.Errorf("%s金额必须是大于 0 的数字", label)
	}
	scaled := number * float64(quotaAmountPrecision)
	if math.Round(scaled) != scaled {
		return 0, fmt.Errorf("%s金额最多支持 6 位小数", label)
	}
	return math.Round(scaled) / float64(quotaAmountPrecision), nil
}

func jsonNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	default:
		return 0, false
	}
}

func quotaLimitLabel(key string) string {
	switch key {
	case "daily":
		return "日额度"
	case "weekly":
		return "周额度"
	case "monthly":
		return "月额度"
	case "total":
		return "总额度"
	default:
		return "额度"
	}
}

func hasEnabledRequestQuotaLimit(value port.ManagementRequestQuotaLimits) bool {
	return value.Hourly != nil ||
		value.Daily != nil ||
		value.Weekly != nil ||
		value.Monthly != nil ||
		value.Total != nil
}

func marshalQuotaLimits(value port.ManagementRequestQuotaLimits) (string, error) {
	out := map[string]any{}
	if value.Hourly != nil {
		out["hourly"] = value.Hourly
	}
	if value.Daily != nil {
		out["daily"] = value.Daily
	}
	if value.Weekly != nil {
		out["weekly"] = value.Weekly
	}
	if value.Monthly != nil {
		out["monthly"] = value.Monthly
	}
	if value.Total != nil {
		out["total"] = value.Total
	}
	ordered := bytes.NewBufferString("{")
	wrote := false
	for _, key := range []string{"hourly", "daily", "weekly", "monthly", "total"} {
		item, exists := out[key]
		if !exists {
			continue
		}
		if wrote {
			ordered.WriteByte(',')
		}
		valueBytes, err := json.Marshal(item)
		if err != nil {
			return "", err
		}
		keyBytes, _ := json.Marshal(key)
		ordered.Write(keyBytes)
		ordered.WriteByte(':')
		ordered.Write(valueBytes)
		wrote = true
	}
	ordered.WriteByte('}')
	return ordered.String(), nil
}

func (s *Service) encryptJSON(value map[string]any) (string, error) {
	secret := strings.TrimSpace(s.secret)
	if secret == "" {
		secret = "juhe-ai-go-development-secret"
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plain, nil)
	tagSize := aead.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	tag := sealed[len(sealed)-tagSize:]
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" + encode(nonce) + ":" + encode(tag) + ":" + encode(ciphertext), nil
}
