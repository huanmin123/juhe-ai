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
)

var (
	ErrPasswordResetInvalid    = errors.New("management system account password reset invalid")
	ErrPasswordResetWhitespace = errors.New("management system account password reset whitespace")
	ErrSystemAccountNotFound   = errors.New("management system account not found")
)

type Service struct {
	store        port.ManagementSystemAccountOptionReader
	now          func() time.Time
	hashPassword func(string) (string, error)
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
	Store        port.ManagementSystemAccountOptionReader
	Now          func() time.Time
	HashPassword func(string) (string, error)
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
		store:        opts.Store,
		now:          now,
		hashPassword: hashPassword,
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
