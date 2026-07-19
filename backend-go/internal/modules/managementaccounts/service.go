package managementaccounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOptionLimit      = 50
	maxOptionLimit          = 50
	defaultOptionPage       = 1
	optionWindowRows        = 1001
	maxOptionFilterItemSize = 50
	maxTagsPerAccount       = 24
	maxTagNameLength        = 40
	pageDataPublishTimeout  = 5 * time.Second
)

type Service struct {
	store             port.ManagementAccountOptionReader
	granteeReader     AccountAuthorizationGranteeReader
	pageDataPublisher AccountStaticChangePublisher
	logger            *slog.Logger
}

type AccountAuthorizationGranteeReader interface {
	ListAccountAuthorizationGranteeIDs(ctx context.Context, accountID string) ([]string, error)
}

type AccountStaticChangePublisher interface {
	PublishAccountStaticChange(ctx context.Context, input AccountStaticChangeInput) error
}

type AccountStaticChangeInput struct {
	AccountID             string
	OwnerSystemAccountIDs []string
	FieldMask             []string
	MembershipChanged     bool
	OrderChanged          bool
	FilterChanged         bool
	PageChanged           bool
	AllScopes             bool
}

type ServiceOptions struct {
	Store             port.ManagementAccountOptionReader
	GranteeReader     AccountAuthorizationGranteeReader
	PageDataPublisher AccountStaticChangePublisher
	Logger            *slog.Logger
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

type TagDeleteInput struct {
	ID              string
	SystemAccountID string
}

type TagUpdateInput struct {
	AccountID       string
	SystemAccountID string
	Tags            []string
}

type TagUpdateResult struct {
	Account      TagUpdateAccount
	PreviousTags []TagUpdateTag
}

var ErrAccountTagInUse = errors.New("account tag in use")
var ErrAccountNotFound = errors.New("account not found")

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

func ValidationMessage(err error) (string, bool) {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "请求参数无效", true
	}
	return validationErr.Message, true
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
	ID                                        string              `json:"id"`
	SystemAccountID                           string              `json:"systemAccountId,omitempty"`
	SystemAccountName                         string              `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID                      string              `json:"ownerSystemAccountId"`
	OwnerSystemAccountName                    string              `json:"ownerSystemAccountName,omitempty"`
	ProviderCode                              string              `json:"providerCode"`
	ProviderProtocolProfileID                 string              `json:"providerProtocolProfileId"`
	ProtocolCode                              string              `json:"protocolCode"`
	ProtocolVersion                           string              `json:"protocolVersion"`
	Name                                      string              `json:"name"`
	Type                                      string              `json:"type"`
	Status                                    string              `json:"status"`
	AccessType                                string              `json:"accessType"`
	AccountAuthorizationID                    string              `json:"accountAuthorizationId,omitempty"`
	AuthorizationStatus                       string              `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt                    string              `json:"authorizationExpiresAt,omitempty"`
	AuthorizationInstanceSourceAccountID      string              `json:"authorizationInstanceSourceAccountId,omitempty"`
	AuthorizationInstanceOwnerSystemAccountID string              `json:"authorizationInstanceOwnerSystemAccountId,omitempty"`
	AccountExpiresAt                          string              `json:"accountExpiresAt,omitempty"`
	Permissions                               ResourcePermissions `json:"permissions"`
}

type Tag struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AccountCount int    `json:"accountCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type TagUpdateTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TagUpdateAccount struct {
	ID                   string         `json:"id"`
	SystemAccountID      string         `json:"-"`
	OwnerSystemAccountID string         `json:"-"`
	Name                 string         `json:"-"`
	Tags                 []TagUpdateTag `json:"tags"`
}

func NewService(store port.ManagementAccountOptionReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:             opts.Store,
		granteeReader:     opts.GranteeReader,
		pageDataPublisher: opts.PageDataPublisher,
		logger:            logger,
	}
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
		accessType := accountAccessType(row.AccessType)
		items = append(items, Option{
			ID:                                   row.ID,
			SystemAccountID:                      row.SystemAccountID,
			SystemAccountName:                    row.SystemAccountName,
			OwnerSystemAccountID:                 row.OwnerSystemAccountID,
			OwnerSystemAccountName:               row.OwnerSystemAccountName,
			ProviderCode:                         row.ProviderCode,
			ProviderProtocolProfileID:            row.ProviderProtocolProfileID,
			ProtocolCode:                         row.ProtocolCode,
			ProtocolVersion:                      row.ProtocolVersion,
			Name:                                 row.Name,
			Type:                                 row.Type,
			Status:                               row.Status,
			AccessType:                           accessType,
			AccountAuthorizationID:               row.AccountAuthorizationID,
			AuthorizationStatus:                  row.AuthorizationStatus,
			AuthorizationExpiresAt:               formatOptionalTime(row.AuthorizationExpiresAt),
			AuthorizationInstanceSourceAccountID: row.AuthorizationInstanceSourceAccountID,
			AuthorizationInstanceOwnerSystemAccountID: row.AuthorizationInstanceOwnerSystemAccountID,
			AccountExpiresAt: formatOptionalTime(row.AccountExpiresAt),
			Permissions:      accountPermissions(accessType, row.HasActiveManualAuthorizationSource),
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

func (s *Service) DeleteTag(ctx context.Context, input TagDeleteInput) (bool, error) {
	if s.store == nil {
		return false, fmt.Errorf("management account tag store is required")
	}
	deleted, err := s.store.DeleteManagementAccountTag(ctx, port.ManagementAccountTagDeleteInput{
		TagID:           strings.TrimSpace(input.ID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if errors.Is(err, port.ErrManagementAccountTagInUse) {
		return false, ErrAccountTagInUse
	}
	return deleted, err
}

func (s *Service) UpdateTags(ctx context.Context, input TagUpdateInput) (TagUpdateResult, error) {
	if s.store == nil {
		return TagUpdateResult{}, fmt.Errorf("management account tag store is required")
	}
	accountID := strings.TrimSpace(input.AccountID)
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if accountID == "" || systemAccountID == "" {
		return TagUpdateResult{}, ErrAccountNotFound
	}
	if len(input.Tags) > maxTagsPerAccount {
		return TagUpdateResult{}, &ValidationError{Message: fmt.Sprintf("单个账户最多配置 %d 个标签", maxTagsPerAccount)}
	}
	tags, err := normalizeTagUpdateInput(input.Tags)
	if err != nil {
		return TagUpdateResult{}, err
	}
	saved, ok, err := s.store.UpdateManagementAccountTags(ctx, port.ManagementAccountTagUpdateInput{
		AccountID:       accountID,
		SystemAccountID: systemAccountID,
		Tags:            tags,
	})
	if err != nil {
		return TagUpdateResult{}, err
	}
	if !ok {
		return TagUpdateResult{}, ErrAccountNotFound
	}
	s.publishTagPageData(ctx, saved.Account, systemAccountID)
	return TagUpdateResult{
		Account:      tagUpdateAccountFromPort(saved.Account),
		PreviousTags: tagUpdateTagsFromPort(saved.PreviousTags),
	}, nil
}

func (s *Service) publishTagPageData(ctx context.Context, account port.ManagementAccountTagUpdateAccount, requestSystemAccountID string) {
	if s.pageDataPublisher == nil {
		return
	}
	owners := normalizedSortedStrings([]string{
		account.SystemAccountID,
		account.OwnerSystemAccountID,
		requestSystemAccountID,
	})
	allScopes := false
	if s.granteeReader == nil {
		allScopes = true
		s.logger.WarnContext(context.WithoutCancel(ctx), "account page data grantee lookup unavailable",
			"accountId", account.ID,
		)
	} else {
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageDataPublishTimeout)
		granteeIDs, err := s.granteeReader.ListAccountAuthorizationGranteeIDs(lookupCtx, account.ID)
		cancel()
		if err != nil {
			allScopes = true
			s.logger.WarnContext(context.WithoutCancel(ctx), "account page data grantee lookup failed",
				"accountId", account.ID,
				"error", err,
			)
		} else {
			owners = normalizedSortedStrings(append(owners, granteeIDs...))
		}
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageDataPublishTimeout)
	defer cancel()
	if err := s.pageDataPublisher.PublishAccountStaticChange(publishCtx, AccountStaticChangeInput{
		AccountID:             strings.TrimSpace(account.ID),
		OwnerSystemAccountIDs: owners,
		FieldMask:             []string{"tags"},
		FilterChanged:         true,
		PageChanged:           true,
		AllScopes:             allScopes,
	}); err != nil {
		s.logger.WarnContext(context.WithoutCancel(ctx), "account page data publish failed",
			"accountId", account.ID,
			"error", err,
		)
	}
}

func normalizedSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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

func normalizeTagUpdateInput(values []string) ([]port.ManagementAccountTagUpsertInput, error) {
	seen := make(map[string]struct{}, len(values))
	output := make([]port.ManagementAccountTagUpsertInput, 0, len(values))
	for _, value := range values {
		name := normalizeTagName(value)
		if name == "" {
			continue
		}
		if len([]rune(name)) > maxTagNameLength {
			return nil, &ValidationError{Message: fmt.Sprintf("账户标签不能超过 %d 个字符", maxTagNameLength)}
		}
		if _, exists := seen[name]; exists {
			continue
		}
		if len(output) >= maxTagsPerAccount {
			return nil, &ValidationError{Message: fmt.Sprintf("单个账户最多配置 %d 个标签", maxTagsPerAccount)}
		}
		seen[name] = struct{}{}
		output = append(output, port.ManagementAccountTagUpsertInput{
			ID:   "acctag_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			Name: name,
		})
	}
	return output, nil
}

func normalizeTagName(value string) string {
	return strings.Join(strings.Fields(value), " ")
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

func tagUpdateAccountFromPort(row port.ManagementAccountTagUpdateAccount) TagUpdateAccount {
	return TagUpdateAccount{
		ID:                   row.ID,
		SystemAccountID:      row.SystemAccountID,
		OwnerSystemAccountID: row.OwnerSystemAccountID,
		Name:                 row.Name,
		Tags:                 tagUpdateTagsFromPort(row.Tags),
	}
}

func tagUpdateTagsFromPort(rows []port.ManagementAccountTag) []TagUpdateTag {
	tags := make([]TagUpdateTag, 0, len(rows))
	for _, row := range rows {
		tags = append(tags, TagUpdateTag{
			ID:   row.ID,
			Name: row.Name,
		})
	}
	return tags
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

func authorizedAccountPermissions(canReturnAuthorization bool) ResourcePermissions {
	return ResourcePermissions{
		CanUse:                 true,
		CanEdit:                false,
		CanDelete:              false,
		CanReturnAuthorization: canReturnAuthorization,
		CanAuthorize:           false,
		CanViewCredentials:     false,
		CanManageAccounts:      false,
		CanBindToAPIKey:        false,
	}
}

func accountPermissions(accessType string, canReturnAuthorization bool) ResourcePermissions {
	if accessType == "authorized" {
		return authorizedAccountPermissions(canReturnAuthorization)
	}
	return ownerPermissions()
}

func accountAccessType(value string) string {
	if strings.TrimSpace(value) == "authorized" {
		return "authorized"
	}
	return "owner"
}
