package managementsystemteams

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestCreateNormalizesInputAndMapsSummary(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		result: port.ManagementSystemTeamSummary{
			ID:                "team_new",
			Name:              "运维团队",
			Description:       "负责稳定性",
			Status:            "active",
			MemberCount:       0,
			ActiveMemberCount: 0,
			CreatedBy:         "sys_admin",
			CreatedAt:         now,
			UpdatedAt:         now,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	description := " 负责稳定性 "

	result, err := service.Create(context.Background(), CreateInput{
		Name:        " 运维团队 ",
		Description: &description,
		CreatedBy:   " sys_admin ",
	})

	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !store.called ||
		store.input.ID == "" ||
		!strings.HasPrefix(store.input.ID, "team_") ||
		store.input.Name != "运维团队" ||
		store.input.Description == nil ||
		*store.input.Description != "负责稳定性" ||
		store.input.Status != "active" ||
		store.input.CreatedBy != "sys_admin" ||
		!store.input.CreatedAt.Equal(now) ||
		!store.input.UpdatedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if result.ID != "team_new" ||
		result.Name != "运维团队" ||
		result.Description != "负责稳定性" ||
		result.Status != "active" ||
		result.CreatedBy != "sys_admin" ||
		result.CreatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("result = %+v", result)
	}
}

func TestCreateAllowsDisabledAndDropsBlankDescription(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{result: port.ManagementSystemTeamSummary{
		ID:        "team_disabled",
		Name:      "停用团队",
		Status:    "disabled",
		CreatedBy: "sys_admin",
		CreatedAt: now,
		UpdatedAt: now,
	}}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})
	description := "   "

	result, err := service.Create(context.Background(), CreateInput{
		Name:        "停用团队",
		Description: &description,
		Status:      "disabled",
		CreatedBy:   "sys_admin",
	})

	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.input.Description != nil || store.input.Status != "disabled" {
		t.Fatalf("store input = %+v", store.input)
	}
	if result.Status != "disabled" || result.Description != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	longName := strings.Repeat("名", maxTeamNameRunes+1)
	longDescription := strings.Repeat("说明", maxTeamDescriptionRunes)
	tests := []struct {
		name  string
		input CreateInput
	}{
		{name: "missing name", input: CreateInput{CreatedBy: "sys_admin"}},
		{name: "long name", input: CreateInput{Name: longName, CreatedBy: "sys_admin"}},
		{name: "missing created by", input: CreateInput{Name: "团队"}},
		{name: "invalid status", input: CreateInput{Name: "团队", CreatedBy: "sys_admin", Status: "archived"}},
		{name: "long description", input: CreateInput{Name: "团队", CreatedBy: "sys_admin", Description: &longDescription}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &teamStoreStub{}
			service := NewService(store)

			_, err := service.Create(context.Background(), tt.input)

			if !errors.Is(err, ErrSystemTeamCreateInvalid) {
				t.Fatalf("Create() error = %v, want %v", err, ErrSystemTeamCreateInvalid)
			}
			if store.called {
				t.Fatal("store should not be called for invalid input")
			}
		})
	}
}

func TestCreateMapsStoreErrors(t *testing.T) {
	storeErr := errors.New("postgres down")
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "duplicate name", err: port.ErrManagementSystemTeamNameExists, wantErr: ErrSystemTeamNameExists},
		{name: "store error", err: storeErr, wantErr: storeErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewService(&teamStoreStub{err: tt.err})

			_, err := service.Create(context.Background(), CreateInput{Name: "团队", CreatedBy: "sys_admin"})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

type teamStoreStub struct {
	called bool
	input  port.ManagementSystemTeamCreateInput
	result port.ManagementSystemTeamSummary
	err    error
}

func (s *teamStoreStub) CreateManagementSystemTeam(_ context.Context, input port.ManagementSystemTeamCreateInput) (port.ManagementSystemTeamSummary, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}
