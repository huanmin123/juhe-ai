package managementsystemaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultListPage         = 1
	defaultListPageSize     = 20
	maxListPageSize         = 100
	maxListWindowRows       = 1001
	defaultOptionLimit      = 50
	maxOptionLimit          = 50
	maxOptionFilterItemSize = 50
	maxDescriptionRunes     = 200
)

var (
	ErrPasswordResetInvalid         = errors.New("management system account password reset invalid")
	ErrPasswordResetWhitespace      = errors.New("management system account password reset whitespace")
	ErrStatusUpdateInvalid          = errors.New("management system account status update invalid")
	ErrImageGenerationUpdateInvalid = errors.New("management system account image generation update invalid")
	ErrProfileUpdateInvalid         = errors.New("management system account profile update invalid")
	ErrProfileUpdateWhitespace      = errors.New("management system account profile update whitespace")
	ErrProfileUpdateDisplayNameDup  = errors.New("management system account profile display name exists")
	ErrActiveSuperAdminRequired     = errors.New("management active super admin required")
	ErrSystemAccountNotFound        = errors.New("management system account not found")
)

type Service struct {
	store                    port.ManagementSystemAccountOptionReader
	now                      func() time.Time
	hashPassword             func(string) (string, error)
	secret                   string
	systemAccountInvalidator SystemAccountInvalidator
}

type OptionListInput struct {
	IDs     []string
	Keyword string
	Limit   int
}

type ListInput struct {
	Keyword  string
	Page     int
	PageSize int
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Summary struct {
	ID                     string `json:"id"`
	Username               string `json:"username"`
	DisplayName            string `json:"displayName"`
	Description            string `json:"description,omitempty"`
	Role                   string `json:"role"`
	Status                 string `json:"status"`
	MustChangePassword     bool   `json:"mustChangePassword"`
	ImageGenerationEnabled bool   `json:"imageGenerationEnabled"`
	LastLoginAt            string `json:"lastLoginAt,omitempty"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

type Option struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

type ServiceOptions struct {
	Store                    port.ManagementSystemAccountOptionReader
	Now                      func() time.Time
	HashPassword             func(string) (string, error)
	Secret                   string
	SystemAccountInvalidator SystemAccountInvalidator
}

type SystemAccountInvalidator interface {
	InvalidateSystemAccountStatusChanged(ctx context.Context, systemAccountID string) error
	InvalidateSystemAccountImageGenerationChanged(ctx context.Context, systemAccountID string) error
}

type PasswordResetInput struct {
	SystemAccountID    string
	Password           string
	MustChangePassword *bool
}

type PasswordResetResult struct {
	Before              Summary
	Account             Summary
	RevokedSessionCount int
}

type StatusUpdateInput struct {
	SystemAccountID string
	Status          string
}

type StatusUpdateResult struct {
	Before              Summary
	Account             Summary
	RevokedSessionCount int
}

type ImageGenerationUpdateInput struct {
	SystemAccountID        string
	ImageGenerationEnabled bool
}

type ImageGenerationUpdateResult struct {
	Before  Summary
	Account Summary
	Changed bool
}

type ProfileUpdateInput struct {
	SystemAccountID    string
	DisplayName        *string
	HasDescription     bool
	Description        *string
	Role               *string
	MustChangePassword *bool
}

type ProfileUpdateResult struct {
	Before  Summary
	Account Summary
	Changed bool
}

type UpdateInput struct {
	SystemAccountID        string
	DisplayName            *string
	HasDescription         bool
	Description            *string
	Password               *string
	Role                   *string
	Status                 *string
	MustChangePassword     *bool
	ImageGenerationEnabled *bool
}

type UpdateResult struct {
	Before              Summary
	Account             Summary
	Changed             bool
	PasswordChanged     bool
	RevokedSessionCount int
}

func NewService(store port.ManagementSystemAccountOptionReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	hashPassword := opts.HashPassword
	if hashPassword == nil {
		hashPassword = managementauth.HashPassword
	}
	return &Service{
		store:                    opts.Store,
		now:                      now,
		hashPassword:             hashPassword,
		secret:                   opts.Secret,
		systemAccountInvalidator: opts.SystemAccountInvalidator,
	}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management system account store is required")
	}
	pageSize := listPageSize(input.PageSize)
	page := listPage(input.Page, pageSize)
	result, err := s.store.ListManagementSystemAccounts(ctx, port.ManagementSystemAccountListInput{
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   pageSize + 1,
		Offset:  (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, systemAccountSummaryFromPort(row))
	}
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), result.HasMore),
		HasMore:  result.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
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

func (s *Service) ResetPassword(ctx context.Context, input PasswordResetInput) (PasswordResetResult, error) {
	if s.store == nil {
		return PasswordResetResult{}, fmt.Errorf("management system account store is required")
	}
	resetter, ok := s.store.(port.ManagementSystemAccountPasswordResetter)
	if !ok || resetter == nil {
		return PasswordResetResult{}, fmt.Errorf("management system account password resetter is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" || utf8.RuneCountInString(input.Password) < 4 {
		return PasswordResetResult{}, ErrPasswordResetInvalid
	}
	if strings.ContainsFunc(input.Password, unicode.IsSpace) {
		return PasswordResetResult{}, ErrPasswordResetWhitespace
	}
	passwordHash, err := s.hashPassword(input.Password)
	if err != nil {
		return PasswordResetResult{}, err
	}
	storeInput := port.ManagementSystemAccountPasswordResetInput{
		SystemAccountID: systemAccountID,
		PasswordHash:    passwordHash,
		UpdatedAt:       s.now().UTC(),
	}
	if input.MustChangePassword != nil {
		storeInput.HasMustChangePassword = true
		storeInput.MustChangePassword = *input.MustChangePassword
	}
	result, found, err := resetter.ResetManagementSystemAccountPassword(ctx, storeInput)
	if err != nil {
		return PasswordResetResult{}, err
	}
	if !found {
		return PasswordResetResult{}, ErrSystemAccountNotFound
	}
	return PasswordResetResult{
		Before:              systemAccountSummaryFromPort(result.Before),
		Account:             systemAccountSummaryFromPort(result.Account),
		RevokedSessionCount: result.RevokedSessionCount,
	}, nil
}

func (s *Service) UpdateStatus(ctx context.Context, input StatusUpdateInput) (StatusUpdateResult, error) {
	if s.store == nil {
		return StatusUpdateResult{}, fmt.Errorf("management system account store is required")
	}
	updater, ok := s.store.(port.ManagementSystemAccountStatusUpdater)
	if !ok || updater == nil {
		return StatusUpdateResult{}, fmt.Errorf("management system account status updater is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" || !validSystemAccountStatus(input.Status) {
		return StatusUpdateResult{}, ErrStatusUpdateInvalid
	}
	result, found, err := updater.UpdateManagementSystemAccountStatus(ctx, port.ManagementSystemAccountStatusUpdateInput{
		SystemAccountID: systemAccountID,
		Status:          input.Status,
		UpdatedAt:       s.now().UTC(),
	})
	if err != nil {
		return StatusUpdateResult{}, err
	}
	if !found {
		return StatusUpdateResult{}, ErrSystemAccountNotFound
	}
	if result.BlockedLastActiveSuperAdmin {
		return StatusUpdateResult{}, ErrActiveSuperAdminRequired
	}
	if result.Before.Status != result.Account.Status && s.systemAccountInvalidator != nil {
		if err := s.systemAccountInvalidator.InvalidateSystemAccountStatusChanged(ctx, systemAccountID); err != nil {
			return StatusUpdateResult{}, fmt.Errorf("invalidate management system account status gateway cache: %w", err)
		}
	}
	return StatusUpdateResult{
		Before:              systemAccountSummaryFromPort(result.Before),
		Account:             systemAccountSummaryFromPort(result.Account),
		RevokedSessionCount: result.RevokedSessionCount,
	}, nil
}

func (s *Service) UpdateImageGeneration(ctx context.Context, input ImageGenerationUpdateInput) (ImageGenerationUpdateResult, error) {
	if s.store == nil {
		return ImageGenerationUpdateResult{}, fmt.Errorf("management system account store is required")
	}
	updater, ok := s.store.(port.ManagementSystemAccountImageGenerationUpdater)
	if !ok || updater == nil {
		return ImageGenerationUpdateResult{}, fmt.Errorf("management system account image generation updater is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return ImageGenerationUpdateResult{}, ErrImageGenerationUpdateInvalid
	}
	result, found, err := updater.UpdateManagementSystemAccountImageGeneration(ctx, port.ManagementSystemAccountImageGenerationUpdateInput{
		SystemAccountID:        systemAccountID,
		ImageGenerationEnabled: input.ImageGenerationEnabled,
		UpdatedAt:              s.now().UTC(),
	})
	if err != nil {
		return ImageGenerationUpdateResult{}, err
	}
	if !found {
		return ImageGenerationUpdateResult{}, ErrSystemAccountNotFound
	}
	if result.Before.ImageGenerationEnabled != result.Account.ImageGenerationEnabled && s.systemAccountInvalidator != nil {
		if err := s.systemAccountInvalidator.InvalidateSystemAccountImageGenerationChanged(ctx, systemAccountID); err != nil {
			return ImageGenerationUpdateResult{}, fmt.Errorf("invalidate management system account image gateway cache: %w", err)
		}
	}
	before := systemAccountSummaryFromPort(result.Before)
	account := systemAccountSummaryFromPort(result.Account)
	return ImageGenerationUpdateResult{
		Before:  before,
		Account: account,
		Changed: before.ImageGenerationEnabled != account.ImageGenerationEnabled,
	}, nil
}

func (s *Service) UpdateProfile(ctx context.Context, input ProfileUpdateInput) (ProfileUpdateResult, error) {
	if s.store == nil {
		return ProfileUpdateResult{}, fmt.Errorf("management system account store is required")
	}
	updater, ok := s.store.(port.ManagementSystemAccountProfileUpdater)
	if !ok || updater == nil {
		return ProfileUpdateResult{}, fmt.Errorf("management system account profile updater is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" || !profileUpdateHasField(input) {
		return ProfileUpdateResult{}, ErrProfileUpdateInvalid
	}
	storeInput := port.ManagementSystemAccountProfileUpdateInput{
		SystemAccountID:       systemAccountID,
		HasMustChangePassword: input.MustChangePassword != nil,
		UpdatedAt:             s.now().UTC(),
	}
	if input.DisplayName != nil {
		displayName, err := normalizeSystemAccountDisplayName(*input.DisplayName)
		if err != nil {
			return ProfileUpdateResult{}, err
		}
		storeInput.HasDisplayName = true
		storeInput.DisplayName = displayName
	}
	if input.HasDescription {
		description, err := normalizeSystemAccountDescription(input.Description)
		if err != nil {
			return ProfileUpdateResult{}, err
		}
		storeInput.HasDescription = true
		storeInput.Description = description
	}
	if input.Role != nil {
		if !validManagementSystemAccountProfileRole(*input.Role) {
			return ProfileUpdateResult{}, ErrProfileUpdateInvalid
		}
		storeInput.HasRole = true
		storeInput.Role = *input.Role
	}
	if input.MustChangePassword != nil {
		storeInput.MustChangePassword = *input.MustChangePassword
	}
	result, found, err := updater.UpdateManagementSystemAccountProfile(ctx, storeInput)
	if errors.Is(err, port.ErrManagementSystemAccountDisplayNameExists) {
		return ProfileUpdateResult{}, ErrProfileUpdateDisplayNameDup
	}
	if err != nil {
		return ProfileUpdateResult{}, err
	}
	if !found {
		return ProfileUpdateResult{}, ErrSystemAccountNotFound
	}
	if result.BlockedLastActiveSuperAdmin {
		return ProfileUpdateResult{}, ErrActiveSuperAdminRequired
	}
	before := systemAccountSummaryFromPort(result.Before)
	account := systemAccountSummaryFromPort(result.Account)
	return ProfileUpdateResult{
		Before:  before,
		Account: account,
		Changed: before.DisplayName != account.DisplayName ||
			before.Description != account.Description ||
			before.Role != account.Role ||
			before.MustChangePassword != account.MustChangePassword,
	}, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	if s.store == nil {
		return UpdateResult{}, fmt.Errorf("management system account store is required")
	}
	updater, ok := s.store.(port.ManagementSystemAccountUpdater)
	if !ok || updater == nil {
		return UpdateResult{}, fmt.Errorf("management system account updater is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" || !updateHasField(input) {
		return UpdateResult{}, ErrProfileUpdateInvalid
	}
	storeInput := port.ManagementSystemAccountUpdateInput{
		SystemAccountID:           systemAccountID,
		HasMustChangePassword:     input.MustChangePassword != nil,
		HasImageGenerationEnabled: input.ImageGenerationEnabled != nil,
		UpdatedAt:                 s.now().UTC(),
	}
	if input.DisplayName != nil {
		displayName, err := normalizeSystemAccountDisplayName(*input.DisplayName)
		if err != nil {
			return UpdateResult{}, err
		}
		storeInput.HasDisplayName = true
		storeInput.DisplayName = displayName
	}
	if input.HasDescription {
		description, err := normalizeSystemAccountDescription(input.Description)
		if err != nil {
			return UpdateResult{}, err
		}
		storeInput.HasDescription = true
		storeInput.Description = description
	}
	if input.Password != nil {
		if utf8.RuneCountInString(*input.Password) < 4 {
			return UpdateResult{}, ErrPasswordResetInvalid
		}
		if strings.ContainsFunc(*input.Password, unicode.IsSpace) {
			return UpdateResult{}, ErrPasswordResetWhitespace
		}
		passwordHash, err := s.hashPassword(*input.Password)
		if err != nil {
			return UpdateResult{}, err
		}
		storeInput.HasPassword = true
		storeInput.PasswordHash = passwordHash
	}
	if input.Role != nil {
		if !validManagementSystemAccountProfileRole(*input.Role) {
			return UpdateResult{}, ErrProfileUpdateInvalid
		}
		storeInput.HasRole = true
		storeInput.Role = *input.Role
	}
	if input.Status != nil {
		if !validSystemAccountStatus(*input.Status) {
			return UpdateResult{}, ErrProfileUpdateInvalid
		}
		storeInput.HasStatus = true
		storeInput.Status = *input.Status
	}
	if input.MustChangePassword != nil {
		storeInput.MustChangePassword = *input.MustChangePassword
	}
	if input.ImageGenerationEnabled != nil {
		storeInput.ImageGenerationEnabled = *input.ImageGenerationEnabled
	}
	result, found, err := updater.UpdateManagementSystemAccount(ctx, storeInput)
	if errors.Is(err, port.ErrManagementSystemAccountDisplayNameExists) {
		return UpdateResult{}, ErrProfileUpdateDisplayNameDup
	}
	if err != nil {
		return UpdateResult{}, err
	}
	if !found {
		return UpdateResult{}, ErrSystemAccountNotFound
	}
	if result.BlockedLastActiveSuperAdmin {
		return UpdateResult{}, ErrActiveSuperAdminRequired
	}
	before := systemAccountSummaryFromPort(result.Before)
	account := systemAccountSummaryFromPort(result.Account)
	if s.systemAccountInvalidator != nil {
		if before.Status != account.Status {
			if err := s.systemAccountInvalidator.InvalidateSystemAccountStatusChanged(ctx, systemAccountID); err != nil {
				return UpdateResult{}, fmt.Errorf("invalidate management system account status gateway cache: %w", err)
			}
		} else if before.ImageGenerationEnabled != account.ImageGenerationEnabled {
			if err := s.systemAccountInvalidator.InvalidateSystemAccountImageGenerationChanged(ctx, systemAccountID); err != nil {
				return UpdateResult{}, fmt.Errorf("invalidate management system account image gateway cache: %w", err)
			}
		}
	}
	passwordChanged := storeInput.HasPassword
	return UpdateResult{
		Before:              before,
		Account:             account,
		Changed:             passwordChanged || systemAccountSummaryChanged(before, account),
		PasswordChanged:     passwordChanged,
		RevokedSessionCount: result.RevokedSessionCount,
	}, nil
}

func listPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultListPageSize
	}
	return min(pageSize, maxListPageSize)
}

func listPage(page int, pageSize int) int {
	if page <= 0 {
		return defaultListPage
	}
	maxPage := max(1, (maxListWindowRows-1)/max(1, pageSize))
	return min(page, maxPage)
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

func systemAccountSummaryFromPort(row port.ManagementSystemAccountSummary) Summary {
	return Summary{
		ID:                     row.ID,
		Username:               row.Username,
		DisplayName:            row.DisplayName,
		Description:            row.Description,
		Role:                   row.Role,
		Status:                 row.Status,
		MustChangePassword:     effectiveMustChangePassword(row.Role, row.MustChangePassword),
		ImageGenerationEnabled: row.ImageGenerationEnabled,
		LastLoginAt:            formatOptionalTime(row.LastLoginAt),
		CreatedAt:              formatTime(row.CreatedAt),
		UpdatedAt:              formatTime(row.UpdatedAt),
	}
}

func effectiveMustChangePassword(role string, mustChangePassword bool) bool {
	if role == "admin" || role == "super_admin" {
		return false
	}
	return mustChangePassword
}

func validSystemAccountStatus(status string) bool {
	return status == "active" || status == "disabled"
}

func validManagementSystemAccountProfileRole(role string) bool {
	return role == "admin" || role == "user"
}

func profileUpdateHasField(input ProfileUpdateInput) bool {
	return input.DisplayName != nil ||
		input.HasDescription ||
		input.Role != nil ||
		input.MustChangePassword != nil
}

func updateHasField(input UpdateInput) bool {
	return input.DisplayName != nil ||
		input.HasDescription ||
		input.Password != nil ||
		input.Role != nil ||
		input.Status != nil ||
		input.MustChangePassword != nil ||
		input.ImageGenerationEnabled != nil
}

func systemAccountSummaryChanged(before Summary, account Summary) bool {
	return before.DisplayName != account.DisplayName ||
		before.Description != account.Description ||
		before.Role != account.Role ||
		before.Status != account.Status ||
		before.MustChangePassword != account.MustChangePassword ||
		before.ImageGenerationEnabled != account.ImageGenerationEnabled
}

func normalizeSystemAccountDisplayName(value string) (string, error) {
	if value == "" {
		return "", ErrProfileUpdateInvalid
	}
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return "", ErrProfileUpdateWhitespace
	}
	displayName := strings.TrimSpace(value)
	if displayName == "" {
		return "", ErrProfileUpdateInvalid
	}
	return displayName, nil
}

func normalizeSystemAccountDescription(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	description := strings.TrimSpace(*value)
	if utf8.RuneCountInString(description) > maxDescriptionRunes {
		return nil, ErrProfileUpdateInvalid
	}
	if description == "" {
		return nil, nil
	}
	return &description, nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}

func pagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	total := (max(1, page) - 1) * max(0, pageSize)
	total += max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}
