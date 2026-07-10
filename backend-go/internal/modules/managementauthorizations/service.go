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
	"juhe-ai/backend-go/internal/timezonecompat"
)

const (
	maxRemarkRunes                                 = 200
	defaultAuthorizationListPageSize               = 50
	maxAuthorizationListPageSize                   = 500
	maxAuthorizationListWindowRows                 = 1001
	defaultAuthorizationUsagePageSize              = 20
	defaultAuthorizationUsageDetailPageSize        = 200
	maxAuthorizationUsagePageSize                  = 200
	maxAuthorizationUsageListWindowRows            = 1001
	maxAuthorizationUsageRangeDays                 = 31
	defaultAuthorizationExpirySweepBatchSize       = 20
	defaultUsageStatsTimezone                      = "UTC"
	fixedUsageStatsRangeWindowDays                 = 31
	maxRequestQuotaHourlyWindowHours               = 24 * 30
	maxRequestQuotaAmountUSD                       = 9_007_199_254_740_991
	quotaAmountPrecision                     int64 = 1_000_000
	ResourceAuthorizationCreatedReason             = "resource_authorization_created"
	ResourceAuthorizationUpdatedReason             = "resource_authorization_updated"
	ResourceAuthorizationReturnedReason            = "resource_authorization_returned"
	ResourceAuthorizationRevokedReason             = "resource_authorization_revoked"
	ResourceAuthorizationExpiredReason             = "authorization_expired"
)

var (
	ErrAuthorizationListInvalid   = errors.New("management authorization list invalid")
	ErrAuthorizationCreateInvalid = errors.New("management authorization create invalid")
	ErrAuthorizationUpdateInvalid = errors.New("management authorization update invalid")
	ErrAuthorizationReturnInvalid = errors.New("management authorization return invalid")
	ErrAuthorizationRevokeInvalid = errors.New("management authorization revoke invalid")
	ErrAuthorizationUsageInvalid  = errors.New("management authorization usage invalid")
)

type Service struct {
	listStore                port.ManagementResourceAuthorizationLister
	getStore                 port.ManagementResourceAuthorizationGetter
	createStore              port.ManagementResourceAuthorizationCreator
	updateStore              port.ManagementResourceAuthorizationUpdater
	returnStore              port.ManagementResourceAuthorizationReturner
	resourceReturnStore      port.ManagementResourceAuthorizationResourceReturner
	revokeStore              port.ManagementResourceAuthorizationRevoker
	expirySweepStore         port.ManagementResourceAuthorizationExpirySweeper
	usageStore               port.ManagementAuthorizationUsageOverviewReader
	usageDetailStore         port.ManagementResourceAuthorizationUsageReader
	usageStatsTimezoneStore  port.ManagementUsageStatsTimezoneReader
	usageRangeWindowStore    port.ManagementAuthorizationUsageRangeWindowRefresher
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
	ResourceReturnStore      port.ManagementResourceAuthorizationResourceReturner
	RevokeStore              port.ManagementResourceAuthorizationRevoker
	ExpirySweepStore         port.ManagementResourceAuthorizationExpirySweeper
	UsageStore               port.ManagementAuthorizationUsageOverviewReader
	UsageDetailStore         port.ManagementResourceAuthorizationUsageReader
	UsageStatsTimezoneStore  port.ManagementUsageStatsTimezoneReader
	UsageRangeWindowStore    port.ManagementAuthorizationUsageRangeWindowRefresher
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

type UsageOverviewInput struct {
	ActorSystemAccountID   string
	ActorRole              string
	ScopedSystemAccountID  string
	ResourceType           string
	ResourceID             string
	TeamID                 string
	GranteeSystemAccountID string
	StartDate              string
	EndDate                string
	Page                   int
	PageSize               int
}

type UsageDetailInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	ActorRole             string
	ScopedSystemAccountID string
	StartDate             string
	EndDate               string
	Page                  int
	PageSize              int
}

type UsageRangeWindowRefreshInput struct {
	Now      time.Time
	Timezone string
}

type UsageRangeWindowRefreshResult struct {
	Ranges      []port.ManagementAccountUsageStatsRange `json:"ranges"`
	RangeCount  int                                     `json:"rangeCount"`
	TeamRows    int64                                   `json:"teamRows"`
	UserRows    int64                                   `json:"userRows"`
	Today       string                                  `json:"today"`
	Timezone    string                                  `json:"timezone"`
	RefreshedAt time.Time                               `json:"refreshedAt"`
}

type TeamUsageOverview struct {
	Range     port.ManagementAccountUsageStatsRange      `json:"range"`
	Summary   port.ManagementAccountUsageSummary         `json:"summary"`
	Rows      []port.ManagementAuthorizationTeamUsageRow `json:"rows"`
	TeamCount int                                        `json:"teamCount"`
	Total     int                                        `json:"total"`
	Page      int                                        `json:"page"`
	PageSize  int                                        `json:"pageSize"`
	HasMore   bool                                       `json:"hasMore"`
}

type UserUsageOverview struct {
	Range     port.ManagementAccountUsageStatsRange      `json:"range"`
	Summary   port.ManagementAccountUsageSummary         `json:"summary"`
	Rows      []port.ManagementAuthorizationUserUsageRow `json:"rows"`
	UserCount int                                        `json:"userCount"`
	Total     int                                        `json:"total"`
	Page      int                                        `json:"page"`
	PageSize  int                                        `json:"pageSize"`
	HasMore   bool                                       `json:"hasMore"`
}

type Detail struct {
	Summary
	Permissions Permissions `json:"permissions"`
}

type UsageDetail struct {
	Detail
	UsageBySystemAccount         []port.ManagementResourceAuthorizationUsageDetail `json:"usageBySystemAccount"`
	UsageBySystemAccountTotal    int                                               `json:"usageBySystemAccountTotal"`
	UsageBySystemAccountPage     int                                               `json:"usageBySystemAccountPage"`
	UsageBySystemAccountPageSize int                                               `json:"usageBySystemAccountPageSize"`
	UsageBySystemAccountHasMore  bool                                              `json:"usageBySystemAccountHasMore"`
	UsageRange                   port.ManagementAccountUsageStatsRange             `json:"usageRange"`
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

type ResourceReturnInput struct {
	ResourceType           string
	ResourceID             string
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

type ExpirySweepInput struct {
	Limit int
}

type ExpirySweepResult struct {
	Expired int `json:"expired"`
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
	resourceReturnStore := opts.ResourceReturnStore
	if resourceReturnStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationResourceReturner); ok {
			resourceReturnStore = candidate
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
	expirySweepStore := opts.ExpirySweepStore
	if expirySweepStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationExpirySweeper); ok {
			expirySweepStore = candidate
		}
	}
	usageStore := opts.UsageStore
	if usageStore == nil {
		if candidate, ok := opts.Store.(port.ManagementAuthorizationUsageOverviewReader); ok {
			usageStore = candidate
		}
	}
	usageDetailStore := opts.UsageDetailStore
	if usageDetailStore == nil {
		if candidate, ok := opts.Store.(port.ManagementResourceAuthorizationUsageReader); ok {
			usageDetailStore = candidate
		}
	}
	usageStatsTimezoneStore := opts.UsageStatsTimezoneStore
	if usageStatsTimezoneStore == nil {
		if candidate, ok := opts.Store.(port.ManagementUsageStatsTimezoneReader); ok {
			usageStatsTimezoneStore = candidate
		}
	}
	usageRangeWindowStore := opts.UsageRangeWindowStore
	if usageRangeWindowStore == nil {
		if candidate, ok := opts.Store.(port.ManagementAuthorizationUsageRangeWindowRefresher); ok {
			usageRangeWindowStore = candidate
		}
	}
	return &Service{
		listStore:                listStore,
		getStore:                 getStore,
		createStore:              opts.Store,
		updateStore:              updateStore,
		returnStore:              returnStore,
		resourceReturnStore:      resourceReturnStore,
		revokeStore:              revokeStore,
		expirySweepStore:         expirySweepStore,
		usageStore:               usageStore,
		usageDetailStore:         usageDetailStore,
		usageStatsTimezoneStore:  usageStatsTimezoneStore,
		usageRangeWindowStore:    usageRangeWindowStore,
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

func (s *Service) TeamUsageOverview(ctx context.Context, input UsageOverviewInput) (TeamUsageOverview, error) {
	if s.usageStore == nil {
		return TeamUsageOverview{}, fmt.Errorf("management authorization usage overview reader is required")
	}
	storeInput, usageRange, page, pageSize, err := s.authorizationUsageOverviewStoreInput(ctx, input)
	if err != nil {
		return TeamUsageOverview{}, err
	}
	result, err := s.usageStore.ListManagementAuthorizationTeamUsageOverview(ctx, storeInput)
	if err != nil {
		return TeamUsageOverview{}, err
	}
	total := authorizationPagedTotalUpperBound(page, pageSize, len(result.Rows), result.HasMore)
	return TeamUsageOverview{
		Range:     usageRange,
		Summary:   result.Summary,
		Rows:      result.Rows,
		TeamCount: total,
		Total:     total,
		Page:      page,
		PageSize:  pageSize,
		HasMore:   result.HasMore,
	}, nil
}

func (s *Service) UserUsageOverview(ctx context.Context, input UsageOverviewInput) (UserUsageOverview, error) {
	if s.usageStore == nil {
		return UserUsageOverview{}, fmt.Errorf("management authorization usage overview reader is required")
	}
	storeInput, usageRange, page, pageSize, err := s.authorizationUsageOverviewStoreInput(ctx, input)
	if err != nil {
		return UserUsageOverview{}, err
	}
	result, err := s.usageStore.ListManagementAuthorizationUserUsageOverview(ctx, storeInput)
	if err != nil {
		return UserUsageOverview{}, err
	}
	total := authorizationPagedTotalUpperBound(page, pageSize, len(result.Rows), result.HasMore)
	return UserUsageOverview{
		Range:     usageRange,
		Summary:   result.Summary,
		Rows:      result.Rows,
		UserCount: total,
		Total:     total,
		Page:      page,
		PageSize:  pageSize,
		HasMore:   result.HasMore,
	}, nil
}

func (s *Service) UsageDetail(ctx context.Context, input UsageDetailInput) (UsageDetail, bool, error) {
	if s.usageDetailStore == nil {
		return UsageDetail{}, false, fmt.Errorf("management resource authorization usage reader is required")
	}
	storeInput, usageRange, page, pageSize, err := s.authorizationUsageDetailStoreInput(ctx, input)
	if err != nil {
		return UsageDetail{}, false, err
	}
	result, found, err := s.usageDetailStore.FindManagementResourceAuthorizationUsage(ctx, storeInput)
	if err != nil || !found {
		return UsageDetail{}, found, err
	}
	canManage := canManageAuthorizationResourceOwner(result.Summary.ResourceOwnerSystemAccountID, storeInput.CanAccessAll, storeInput.ScopedSystemAccountID)
	return UsageDetail{
		Detail:                       detailFromSummary(result.Summary, canManage),
		UsageBySystemAccount:         result.UsageBySystemAccount,
		UsageBySystemAccountTotal:    result.UsageBySystemAccountTotal,
		UsageBySystemAccountPage:     page,
		UsageBySystemAccountPageSize: pageSize,
		UsageBySystemAccountHasMore:  result.UsageBySystemAccountHasMore,
		UsageRange:                   usageRange,
	}, true, nil
}

func (s *Service) authorizationUsageOverviewStoreInput(ctx context.Context, input UsageOverviewInput) (port.ManagementAuthorizationUsageOverviewInput, port.ManagementAccountUsageStatsRange, int, int, error) {
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if actor == "" {
		return port.ManagementAuthorizationUsageOverviewInput{}, port.ManagementAccountUsageStatsRange{}, 0, 0, ErrAuthorizationUsageInvalid
	}
	resourceType := normalizeResourceType(input.ResourceType)
	if strings.TrimSpace(input.ResourceType) != "" && resourceType == "" {
		return port.ManagementAuthorizationUsageOverviewInput{}, port.ManagementAccountUsageStatsRange{}, 0, 0, ErrAuthorizationUsageInvalid
	}
	pageSize := authorizationUsagePageSize(input.PageSize)
	page := authorizationUsagePage(input.Page, pageSize)
	usageRange, err := s.normalizeAuthorizationUsageRange(ctx, input.StartDate, input.EndDate)
	if err != nil {
		return port.ManagementAuthorizationUsageOverviewInput{}, port.ManagementAccountUsageStatsRange{}, 0, 0, err
	}
	canAccessAll := isAdminRole(input.ActorRole)
	scopedSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if !canAccessAll {
		scopedSystemAccountID = actor
	}
	return port.ManagementAuthorizationUsageOverviewInput{
		ActorSystemAccountID:   actor,
		CanAccessAll:           canAccessAll,
		ScopedSystemAccountID:  scopedSystemAccountID,
		ResourceType:           resourceType,
		ResourceID:             strings.TrimSpace(input.ResourceID),
		TeamID:                 strings.TrimSpace(input.TeamID),
		GranteeSystemAccountID: strings.TrimSpace(input.GranteeSystemAccountID),
		StartDate:              usageRange.StartDate,
		EndDate:                usageRange.EndDate,
		Limit:                  pageSize + 1,
		Offset:                 (page - 1) * pageSize,
	}, usageRange, page, pageSize, nil
}

func (s *Service) authorizationUsageDetailStoreInput(ctx context.Context, input UsageDetailInput) (port.ManagementResourceAuthorizationUsageInput, port.ManagementAccountUsageStatsRange, int, int, error) {
	authorizationID := strings.TrimSpace(input.AuthorizationID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if authorizationID == "" || actor == "" {
		return port.ManagementResourceAuthorizationUsageInput{}, port.ManagementAccountUsageStatsRange{}, 0, 0, ErrAuthorizationUsageInvalid
	}
	pageSize := authorizationUsageDetailPageSize(input.PageSize)
	page := authorizationUsagePage(input.Page, pageSize)
	usageRange, err := s.normalizeAuthorizationUsageRange(ctx, input.StartDate, input.EndDate)
	if err != nil {
		return port.ManagementResourceAuthorizationUsageInput{}, port.ManagementAccountUsageStatsRange{}, 0, 0, err
	}
	canAccessAll := isAdminRole(input.ActorRole)
	scopedSystemAccountID := strings.TrimSpace(input.ScopedSystemAccountID)
	if !canAccessAll {
		scopedSystemAccountID = actor
	}
	return port.ManagementResourceAuthorizationUsageInput{
		AuthorizationID:       authorizationID,
		ActorSystemAccountID:  actor,
		CanAccessAll:          canAccessAll,
		ScopedSystemAccountID: scopedSystemAccountID,
		StartDate:             usageRange.StartDate,
		EndDate:               usageRange.EndDate,
		Limit:                 pageSize + 1,
		Offset:                (page - 1) * pageSize,
	}, usageRange, page, pageSize, nil
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
	s.invalidateAuthorizationChangedBestEffort(ctx, ResourceAuthorizationCreatedReason)
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
	s.invalidateAuthorizationChangedBestEffort(ctx, ResourceAuthorizationUpdatedReason)
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
	s.invalidateAuthorizationChangedBestEffort(ctx, ResourceAuthorizationReturnedReason)
	return row, true, nil
}

func (s *Service) ReturnByResource(ctx context.Context, input ResourceReturnInput) (Summary, bool, error) {
	if s.resourceReturnStore == nil {
		return Summary{}, false, fmt.Errorf("management resource authorization resource returner is required")
	}
	now := s.now().UTC()
	resourceType := strings.TrimSpace(input.ResourceType)
	resourceID := strings.TrimSpace(input.ResourceID)
	granteeSystemAccountID := strings.TrimSpace(input.GranteeSystemAccountID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if (resourceType != "account" && resourceType != "group") || resourceID == "" || granteeSystemAccountID == "" || actor == "" {
		return Summary{}, false, ErrAuthorizationReturnInvalid
	}
	row, found, err := s.resourceReturnStore.ReturnManagementResourceAuthorizationForGranteeByResource(ctx, port.ManagementResourceAuthorizationReturnResourceInput{
		ResourceType:           resourceType,
		ResourceID:             resourceID,
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
	s.invalidateAuthorizationChangedBestEffort(ctx, ResourceAuthorizationReturnedReason)
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
	s.invalidateAuthorizationChangedBestEffort(ctx, ResourceAuthorizationRevokedReason)
	return row, true, nil
}

func (s *Service) ExpireDue(ctx context.Context, input ExpirySweepInput) (ExpirySweepResult, error) {
	if s.expirySweepStore == nil {
		return ExpirySweepResult{}, fmt.Errorf("management resource authorization expiry sweeper is required")
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultAuthorizationExpirySweepBatchSize
	}
	if limit < 0 {
		limit = 1
	}
	result, err := s.expirySweepStore.ExpireDueManagementResourceAuthorizations(ctx, port.ManagementResourceAuthorizationExpirySweepInput{
		Limit:     limit,
		ExpiredAt: s.now().UTC(),
	})
	if err != nil {
		return ExpirySweepResult{}, err
	}
	if result.Expired > 0 {
		s.invalidateAuthorizationChangedBestEffort(ctx, ResourceAuthorizationExpiredReason)
	}
	return ExpirySweepResult{Expired: result.Expired}, nil
}

func (s *Service) invalidateAuthorizationChangedBestEffort(ctx context.Context, reason string) {
	if s.authorizationInvalidator == nil {
		return
	}
	_ = s.authorizationInvalidator.InvalidateAuthorizationChanged(ctx, reason)
}

func (s *Service) RefreshUsageRangeWindows(ctx context.Context, input UsageRangeWindowRefreshInput) (UsageRangeWindowRefreshResult, error) {
	if s.usageRangeWindowStore == nil {
		return UsageRangeWindowRefreshResult{}, fmt.Errorf("management authorization usage range window refresher is required")
	}
	now := input.Now
	if now.IsZero() {
		now = s.now()
	}
	timezone, location, err := s.usageRangeWindowTimezone(ctx, input.Timezone)
	if err != nil {
		return UsageRangeWindowRefreshResult{}, err
	}
	today := usageStatsDateKey(now, location)
	ranges := hotUsageStatsRangesForToday(today)
	if len(ranges) == 0 {
		return UsageRangeWindowRefreshResult{
			Ranges:      []port.ManagementAccountUsageStatsRange{},
			RangeCount:  0,
			Today:       today,
			Timezone:    timezone,
			RefreshedAt: now.UTC(),
		}, nil
	}
	refreshedAt := now.UTC()
	result, err := s.usageRangeWindowStore.RefreshManagementAuthorizationUsageRangeWindows(ctx, port.ManagementAuthorizationUsageRangeWindowRefreshInput{
		Ranges:      ranges,
		RefreshedAt: refreshedAt,
	})
	if err != nil {
		return UsageRangeWindowRefreshResult{}, err
	}
	return UsageRangeWindowRefreshResult{
		Ranges:      ranges,
		RangeCount:  result.Ranges,
		TeamRows:    result.TeamRows,
		UserRows:    result.UserRows,
		Today:       today,
		Timezone:    timezone,
		RefreshedAt: refreshedAt,
	}, nil
}

func (s *Service) usageRangeWindowTimezone(ctx context.Context, input string) (string, *time.Location, error) {
	timezone := strings.TrimSpace(input)
	if timezone == "" && s.usageStatsTimezoneStore != nil {
		value, found, err := s.usageStatsTimezoneStore.GetManagementUsageStatsTimezone(ctx)
		if err != nil {
			return "", nil, err
		}
		if found {
			timezone = strings.TrimSpace(value)
		}
	}
	if timezone == "" {
		timezone = defaultUsageStatsTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	return timezone, location, nil
}

func usageStatsDateKey(now time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	year, month, day := now.In(location).Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

func hotUsageStatsRangesForToday(today string) []port.ManagementAccountUsageStatsRange {
	endDate, ok := parseUsageStatsDateKey(today)
	if !ok {
		return nil
	}
	fixedStartDate := endDate.AddDate(0, 0, -(fixedUsageStatsRangeWindowDays - 1))
	monthStartDate := time.Date(endDate.Year(), endDate.Month(), 1, 0, 0, 0, 0, time.UTC)
	candidates := []struct {
		start time.Time
		end   time.Time
	}{
		{start: endDate, end: endDate},
		{start: endDate.AddDate(0, 0, -1), end: endDate.AddDate(0, 0, -1)},
		{start: endDate.AddDate(0, 0, -6), end: endDate},
		{start: fixedStartDate, end: endDate},
		{start: laterUsageStatsDate(monthStartDate, fixedStartDate), end: endDate},
	}
	seen := map[string]bool{}
	ranges := make([]port.ManagementAccountUsageStatsRange, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.end.Before(fixedStartDate) || candidate.start.After(endDate) {
			continue
		}
		start := laterUsageStatsDate(candidate.start, fixedStartDate)
		end := candidate.end
		if start.After(end) {
			continue
		}
		startKey := formatUsageStatsDateKey(start)
		endKey := formatUsageStatsDateKey(end)
		key := startKey + ":" + endKey
		if seen[key] {
			continue
		}
		seen[key] = true
		ranges = append(ranges, port.ManagementAccountUsageStatsRange{
			StartDate: startKey,
			EndDate:   endKey,
			Days:      int(end.Sub(start).Hours()/24) + 1,
			MaxDays:   fixedUsageStatsRangeWindowDays,
		})
	}
	return ranges
}

func parseUsageStatsDateKey(value string) (time.Time, bool) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	if formatUsageStatsDateKey(parsed) != strings.TrimSpace(value) {
		return time.Time{}, false
	}
	return parsed, true
}

func formatUsageStatsDateKey(value time.Time) string {
	return value.Format("2006-01-02")
}

func laterUsageStatsDate(left time.Time, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
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

func authorizationUsagePageSize(value int) int {
	if value <= 0 {
		return defaultAuthorizationUsagePageSize
	}
	if value > maxAuthorizationUsagePageSize {
		return maxAuthorizationUsagePageSize
	}
	return value
}

func authorizationUsageDetailPageSize(value int) int {
	if value <= 0 {
		return defaultAuthorizationUsageDetailPageSize
	}
	if value > maxAuthorizationUsagePageSize {
		return maxAuthorizationUsagePageSize
	}
	return value
}

func authorizationUsagePage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	maxPage := max(1, (maxAuthorizationUsageListWindowRows-1)/max(1, pageSize))
	if value > maxPage {
		return maxPage
	}
	return value
}

func (s *Service) normalizeAuthorizationUsageRange(ctx context.Context, startDate string, endDate string) (port.ManagementAccountUsageStatsRange, error) {
	location, err := s.authorizationUsageStatsLocation(ctx)
	if err != nil {
		return port.ManagementAccountUsageStatsRange{}, err
	}
	todayText := s.now().In(location).Format("2006-01-02")
	today, _ := time.Parse("2006-01-02", todayText)
	earliestSupported := today.AddDate(0, 0, -(maxAuthorizationUsageRangeDays - 1))

	startDate = strings.TrimSpace(startDate)
	endDate = strings.TrimSpace(endDate)
	if startDate == "" && endDate == "" {
		startDate = earliestSupported.Format("2006-01-02")
		endDate = todayText
	} else {
		if startDate == "" {
			startDate = endDate
		}
		if endDate == "" {
			endDate = startDate
		}
	}

	end, err := parseOptionalDateKey(endDate)
	if err != nil {
		return port.ManagementAccountUsageStatsRange{}, ErrAuthorizationUsageInvalid
	}
	endValue := *end
	if endValue.After(today) {
		endValue = today
	}
	if endValue.Before(earliestSupported) {
		endValue = earliestSupported
	}

	start, err := parseOptionalDateKey(startDate)
	if err != nil {
		return port.ManagementAccountUsageStatsRange{}, ErrAuthorizationUsageInvalid
	}
	startValue := *start
	if startValue.After(today) {
		startValue = today
	}
	if startValue.Before(earliestSupported) {
		startValue = earliestSupported
	}
	if startValue.After(endValue) {
		startValue = endValue
	}
	earliestStart := endValue.AddDate(0, 0, -(maxAuthorizationUsageRangeDays - 1))
	if startValue.Before(earliestStart) {
		startValue = earliestStart
	}

	days := int(endValue.Sub(startValue).Hours()/24) + 1
	return port.ManagementAccountUsageStatsRange{
		StartDate: startValue.Format("2006-01-02"),
		EndDate:   endValue.Format("2006-01-02"),
		Days:      days,
		MaxDays:   maxAuthorizationUsageRangeDays,
	}, nil
}

func (s *Service) authorizationUsageStatsLocation(ctx context.Context) (*time.Location, error) {
	if s.usageStatsTimezoneStore == nil {
		return nil, fmt.Errorf("management usage stats timezone reader is required")
	}
	timezone, found, err := s.usageStatsTimezoneStore.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	timezone = strings.TrimSpace(timezone)
	if !found || timezone == "" {
		return nil, fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := timezonecompat.LoadNodeLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	return location, nil
}

func parseOptionalDateKey(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", trimmed)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
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
