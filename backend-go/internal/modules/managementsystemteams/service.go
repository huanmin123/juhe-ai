package managementsystemteams

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	maxTeamNameRunes        = 100
	maxTeamDescriptionRunes = 200

	TeamAuthorizationChangedReason = "team_authorization_changed"
)

var (
	ErrSystemTeamCreateInvalid = errors.New("management system team create invalid")
	ErrSystemTeamReadInvalid   = errors.New("management system team read invalid")
	ErrSystemTeamUpdateInvalid = errors.New("management system team update invalid")
	ErrSystemTeamNotFound      = errors.New("management system team not found")
	ErrSystemTeamNameExists    = errors.New("management system team name exists")
)

type Service struct {
	store                    any
	now                      func() time.Time
	authorizationInvalidator AuthorizationInvalidator
}

type ListInput struct {
	SystemAccountID string
	Keyword         string
	Page            int
	PageSize        int
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type CreateInput struct {
	Name        string
	Description *string
	Status      string
	CreatedBy   string
}

type UpdateInput struct {
	TeamID          string
	SystemAccountID string
	Name            *string
	HasDescription  bool
	Description     *string
	Status          *string
	UpdatedBy       string
}

type UpdateResult struct {
	Before               Summary `json:"before"`
	Team                 Detail  `json:"team"`
	AuthorizationChanged bool    `json:"authorizationChanged"`
}

type Summary struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Status            string `json:"status"`
	MemberCount       int    `json:"memberCount"`
	ActiveMemberCount int    `json:"activeMemberCount"`
	CreatedBy         string `json:"createdBy"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type Detail struct {
	Summary
	Members []MemberSummary `json:"members"`
}

type MemberSummary struct {
	ID                string `json:"id"`
	TeamID            string `json:"teamId"`
	SystemAccountID   string `json:"systemAccountId"`
	SystemAccountName string `json:"systemAccountName,omitempty"`
	Username          string `json:"username,omitempty"`
	MemberRole        string `json:"memberRole"`
	Status            string `json:"status"`
	JoinedAt          string `json:"joinedAt"`
	RemovedAt         string `json:"removedAt,omitempty"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type ServiceOptions struct {
	Store                    any
	Now                      func() time.Time
	AuthorizationInvalidator AuthorizationInvalidator
}

type AuthorizationInvalidator interface {
	InvalidateAuthorizationChanged(ctx context.Context, reason string) error
}

func NewService(store port.ManagementSystemTeamCreator) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, now: now, authorizationInvalidator: opts.AuthorizationInvalidator}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	reader, ok := s.store.(port.ManagementSystemTeamReader)
	if !ok || reader == nil {
		return ListResult{}, fmt.Errorf("management system team reader is required")
	}
	pageSize := listPageSize(input.PageSize)
	page := listPage(input.Page, pageSize)
	result, err := reader.ListManagementSystemTeams(ctx, port.ManagementSystemTeamListInput{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Keyword:         strings.TrimSpace(input.Keyword),
		Limit:           pageSize + 1,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, summaryFromPort(row))
	}
	return ListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), result.HasMore),
		HasMore:  result.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Detail(ctx context.Context, teamID string, systemAccountID string) (Detail, bool, error) {
	reader, ok := s.store.(port.ManagementSystemTeamReader)
	if !ok || reader == nil {
		return Detail{}, false, fmt.Errorf("management system team reader is required")
	}
	id := strings.TrimSpace(teamID)
	if id == "" {
		return Detail{}, false, ErrSystemTeamReadInvalid
	}
	row, ok, err := reader.FindManagementSystemTeam(ctx, id, strings.TrimSpace(systemAccountID))
	if err != nil || !ok {
		return Detail{}, ok, err
	}
	return detailFromPort(row), true, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Summary, error) {
	creator, ok := s.store.(port.ManagementSystemTeamCreator)
	if !ok || creator == nil {
		return Summary{}, fmt.Errorf("management system team creator is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTeamNameRunes {
		return Summary{}, ErrSystemTeamCreateInvalid
	}
	createdBy := strings.TrimSpace(input.CreatedBy)
	if createdBy == "" {
		return Summary{}, ErrSystemTeamCreateInvalid
	}
	status := normalizeTeamStatus(input.Status)
	if status == "" {
		return Summary{}, ErrSystemTeamCreateInvalid
	}

	var description *string
	if input.Description != nil {
		text := strings.TrimSpace(*input.Description)
		if text != "" {
			if utf8.RuneCountInString(text) > maxTeamDescriptionRunes {
				return Summary{}, ErrSystemTeamCreateInvalid
			}
			description = &text
		}
	}

	now := s.now().UTC()
	row, err := creator.CreateManagementSystemTeam(ctx, port.ManagementSystemTeamCreateInput{
		ID:          "team_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Name:        name,
		Description: description,
		Status:      status,
		CreatedBy:   createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		if errors.Is(err, port.ErrManagementSystemTeamNameExists) {
			return Summary{}, ErrSystemTeamNameExists
		}
		return Summary{}, err
	}
	return summaryFromPort(row), nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, bool, error) {
	updater, ok := s.store.(port.ManagementSystemTeamUpdater)
	if !ok || updater == nil {
		return UpdateResult{}, false, fmt.Errorf("management system team updater is required")
	}
	teamID := strings.TrimSpace(input.TeamID)
	updatedBy := strings.TrimSpace(input.UpdatedBy)
	if teamID == "" || updatedBy == "" {
		return UpdateResult{}, false, ErrSystemTeamUpdateInvalid
	}

	storeInput := port.ManagementSystemTeamUpdateInput{
		TeamID:          teamID,
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		UpdatedBy:       updatedBy,
		UpdatedAt:       s.now().UTC(),
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || utf8.RuneCountInString(name) > maxTeamNameRunes {
			return UpdateResult{}, false, ErrSystemTeamUpdateInvalid
		}
		storeInput.HasName = true
		storeInput.Name = name
	}
	if input.HasDescription {
		if input.Description != nil {
			description := strings.TrimSpace(*input.Description)
			if description != "" {
				if utf8.RuneCountInString(description) > maxTeamDescriptionRunes {
					return UpdateResult{}, false, ErrSystemTeamUpdateInvalid
				}
				storeInput.Description = &description
			}
		}
		storeInput.HasDescription = true
	}
	if input.Status != nil {
		status := normalizeTeamUpdateStatus(*input.Status)
		if status == "" {
			return UpdateResult{}, false, ErrSystemTeamUpdateInvalid
		}
		storeInput.HasStatus = true
		storeInput.Status = status
	}

	row, found, err := updater.UpdateManagementSystemTeam(ctx, storeInput)
	if err != nil {
		if errors.Is(err, port.ErrManagementSystemTeamNameExists) {
			return UpdateResult{}, false, ErrSystemTeamNameExists
		}
		return UpdateResult{}, false, err
	}
	if !found {
		return UpdateResult{}, false, nil
	}
	if row.AuthorizationChanged && s.authorizationInvalidator != nil {
		if err := s.authorizationInvalidator.InvalidateAuthorizationChanged(ctx, TeamAuthorizationChangedReason); err != nil {
			return UpdateResult{}, true, err
		}
	}
	return UpdateResult{
		Before:               summaryFromPort(row.Before),
		Team:                 detailFromPort(row.Team),
		AuthorizationChanged: row.AuthorizationChanged,
	}, true, nil
}

func normalizeTeamStatus(status string) string {
	switch status {
	case "":
		return "active"
	case "active":
		return "active"
	case "disabled":
		return "disabled"
	default:
		return ""
	}
}

func normalizeTeamUpdateStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "active":
		return "active"
	case "disabled":
		return "disabled"
	default:
		return ""
	}
}

func listPageSize(value int) int {
	if value <= 0 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return value
}

func listPage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	if value > 1000 {
		return 1000
	}
	return value
}

func pagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	base := (page-1)*pageSize + itemCount
	if hasMore {
		return base + 1
	}
	return base
}

func summaryFromPort(row port.ManagementSystemTeamSummary) Summary {
	return Summary{
		ID:                row.ID,
		Name:              row.Name,
		Description:       row.Description,
		Status:            row.Status,
		MemberCount:       row.MemberCount,
		ActiveMemberCount: row.ActiveMemberCount,
		CreatedBy:         row.CreatedBy,
		CreatedAt:         row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:         row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func detailFromPort(row port.ManagementSystemTeamDetail) Detail {
	members := make([]MemberSummary, 0, len(row.Members))
	for _, member := range row.Members {
		members = append(members, memberFromPort(member))
	}
	return Detail{Summary: summaryFromPort(row.ManagementSystemTeamSummary), Members: members}
}

func memberFromPort(row port.ManagementSystemTeamMemberSummary) MemberSummary {
	removedAt := ""
	if row.RemovedAt != nil {
		removedAt = row.RemovedAt.UTC().Format(time.RFC3339Nano)
	}
	return MemberSummary{
		ID:                row.ID,
		TeamID:            row.TeamID,
		SystemAccountID:   row.SystemAccountID,
		SystemAccountName: row.SystemAccountName,
		Username:          row.Username,
		MemberRole:        row.MemberRole,
		Status:            row.Status,
		JoinedAt:          row.JoinedAt.UTC().Format(time.RFC3339Nano),
		RemovedAt:         removedAt,
		CreatedAt:         row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:         row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}
