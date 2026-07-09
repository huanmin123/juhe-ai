package managementsystemteams

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListNormalizesPagingAndMapsItems(t *testing.T) {
	updatedAt := time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		listResult: port.ManagementSystemTeamListResult{
			Items: []port.ManagementSystemTeamSummary{{
				ID:                "team_ops",
				Name:              "运维团队",
				Description:       "负责稳定性",
				Status:            "active",
				MemberCount:       2,
				ActiveMemberCount: 2,
				CreatedBy:         "sys_admin",
				CreatedAt:         updatedAt.Add(-time.Hour),
				UpdatedAt:         updatedAt,
			}},
			HasMore: true,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store})

	result, err := service.List(context.Background(), ListInput{
		SystemAccountID: " sys_user ",
		Keyword:         " 运维 ",
		Page:            2,
		PageSize:        1,
	})

	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !store.listCalled ||
		store.listInput.SystemAccountID != "sys_user" ||
		store.listInput.Keyword != "运维" ||
		store.listInput.Limit != 2 ||
		store.listInput.Offset != 1 {
		t.Fatalf("list input = %+v", store.listInput)
	}
	if result.Total != 3 || !result.HasMore || result.Page != 2 || result.PageSize != 1 {
		t.Fatalf("list result paging = %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "team_ops" || result.Items[0].MemberCount != 2 || result.Items[0].UpdatedAt != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("list items = %+v", result.Items)
	}
}

func TestDetailMapsMembers(t *testing.T) {
	joinedAt := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		detailFound: true,
		detailResult: port.ManagementSystemTeamDetail{
			ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{
				ID:                "team_ops",
				Name:              "运维团队",
				Status:            "active",
				MemberCount:       1,
				ActiveMemberCount: 1,
				CreatedBy:         "sys_admin",
				CreatedAt:         joinedAt,
				UpdatedAt:         joinedAt,
			},
			Members: []port.ManagementSystemTeamMemberSummary{{
				ID:                "teammem_1",
				TeamID:            "team_ops",
				SystemAccountID:   "sys_user",
				SystemAccountName: "用户",
				Username:          "user",
				MemberRole:        "member",
				Status:            "active",
				JoinedAt:          joinedAt,
				CreatedAt:         joinedAt,
				UpdatedAt:         joinedAt,
			}},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store})

	result, found, err := service.Detail(context.Background(), " team_ops ", " sys_user ")

	if err != nil || !found {
		t.Fatalf("Detail() found=%v error=%v", found, err)
	}
	if !store.detailCalled || store.detailTeamID != "team_ops" || store.detailSystemAccountID != "sys_user" {
		t.Fatalf("detail args team=%q systemAccount=%q", store.detailTeamID, store.detailSystemAccountID)
	}
	if result.ID != "team_ops" || len(result.Members) != 1 || result.Members[0].SystemAccountID != "sys_user" {
		t.Fatalf("detail result = %+v", result)
	}
	if result.Members[0].JoinedAt != joinedAt.Format(time.RFC3339Nano) {
		t.Fatalf("member joinedAt = %q", result.Members[0].JoinedAt)
	}
}

func TestDetailRejectsBlankID(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{Store: &teamStoreStub{}})

	_, _, err := service.Detail(context.Background(), "   ", "sys_user")

	if !errors.Is(err, ErrSystemTeamReadInvalid) {
		t.Fatalf("Detail() error = %v, want %v", err, ErrSystemTeamReadInvalid)
	}
}

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
	called                bool
	input                 port.ManagementSystemTeamCreateInput
	result                port.ManagementSystemTeamSummary
	err                   error
	listCalled            bool
	listInput             port.ManagementSystemTeamListInput
	listResult            port.ManagementSystemTeamListResult
	listErr               error
	detailCalled          bool
	detailTeamID          string
	detailSystemAccountID string
	detailResult          port.ManagementSystemTeamDetail
	detailFound           bool
	detailErr             error
}

func (s *teamStoreStub) CreateManagementSystemTeam(_ context.Context, input port.ManagementSystemTeamCreateInput) (port.ManagementSystemTeamSummary, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}

func (s *teamStoreStub) ListManagementSystemTeams(_ context.Context, input port.ManagementSystemTeamListInput) (port.ManagementSystemTeamListResult, error) {
	s.listCalled = true
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *teamStoreStub) FindManagementSystemTeam(_ context.Context, teamID string, systemAccountID string) (port.ManagementSystemTeamDetail, bool, error) {
	s.detailCalled = true
	s.detailTeamID = teamID
	s.detailSystemAccountID = systemAccountID
	return s.detailResult, s.detailFound, s.detailErr
}
