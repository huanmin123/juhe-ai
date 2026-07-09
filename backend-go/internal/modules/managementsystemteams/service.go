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
)

var (
	ErrSystemTeamCreateInvalid = errors.New("management system team create invalid")
	ErrSystemTeamNameExists    = errors.New("management system team name exists")
)

type Service struct {
	store port.ManagementSystemTeamCreator
	now   func() time.Time
}

type CreateInput struct {
	Name        string
	Description *string
	Status      string
	CreatedBy   string
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

type ServiceOptions struct {
	Store port.ManagementSystemTeamCreator
	Now   func() time.Time
}

func NewService(store port.ManagementSystemTeamCreator) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, now: now}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Summary, error) {
	if s.store == nil {
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
	row, err := s.store.CreateManagementSystemTeam(ctx, port.ManagementSystemTeamCreateInput{
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
