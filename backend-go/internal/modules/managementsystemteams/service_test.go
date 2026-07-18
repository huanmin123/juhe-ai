package managementsystemteams

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
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

func TestUpdateNormalizesInputMapsDetailAndInvalidatesAuthorization(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	joinedAt := now.Add(-time.Hour)
	store := &teamStoreStub{
		updateFound: true,
		updateResult: port.ManagementSystemTeamUpdateResult{
			Before: port.ManagementSystemTeamSummary{
				ID:          "team_ops",
				Name:        "运维团队",
				Description: "旧说明",
				Status:      "active",
				CreatedBy:   "sys_admin",
				CreatedAt:   joinedAt,
				UpdatedAt:   joinedAt,
			},
			Team: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{
					ID:          "team_ops",
					Name:        "新运维团队",
					Description: "新说明",
					Status:      "disabled",
					CreatedBy:   "sys_admin",
					CreatedAt:   joinedAt,
					UpdatedAt:   now,
				},
				Members: []port.ManagementSystemTeamMemberSummary{{
					ID:              "teammem_1",
					TeamID:          "team_ops",
					SystemAccountID: "sys_user",
					MemberRole:      "member",
					Status:          "active",
					JoinedAt:        joinedAt,
					CreatedAt:       joinedAt,
					UpdatedAt:       now,
				}},
			},
			AuthorizationChanged: true,
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})
	name := " 新运维团队 "
	description := " 新说明 "
	status := "disabled"

	result, found, err := service.Update(context.Background(), UpdateInput{
		TeamID:          " team_ops ",
		SystemAccountID: " sys_owner ",
		Name:            &name,
		HasDescription:  true,
		Description:     &description,
		Status:          &status,
		UpdatedBy:       " sys_admin ",
	})

	if err != nil || !found {
		t.Fatalf("Update() found=%v error=%v", found, err)
	}
	if !store.updateCalled ||
		store.updateInput.TeamID != "team_ops" ||
		store.updateInput.SystemAccountID != "sys_owner" ||
		!store.updateInput.HasName ||
		store.updateInput.Name != "新运维团队" ||
		!store.updateInput.HasDescription ||
		store.updateInput.Description == nil ||
		*store.updateInput.Description != "新说明" ||
		!store.updateInput.HasStatus ||
		store.updateInput.Status != "disabled" ||
		store.updateInput.UpdatedBy != "sys_admin" ||
		!store.updateInput.UpdatedAt.Equal(now) {
		t.Fatalf("store update input = %+v", store.updateInput)
	}
	if result.Team.ID != "team_ops" ||
		result.Team.Name != "新运维团队" ||
		result.Team.Status != "disabled" ||
		len(result.Team.Members) != 1 ||
		result.Before.Name != "运维团队" ||
		!result.AuthorizationChanged {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.reason != TeamAuthorizationChangedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestUpdateAllowsClearingDescriptionWithoutInvalidation(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		updateFound: true,
		updateResult: port.ManagementSystemTeamUpdateResult{
			Before: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			Team: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			},
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }, AuthorizationInvalidator: invalidator})

	result, found, err := service.Update(context.Background(), UpdateInput{
		TeamID:         "team_ops",
		HasDescription: true,
		Description:    nil,
		UpdatedBy:      "sys_admin",
	})

	if err != nil || !found {
		t.Fatalf("Update() found=%v error=%v", found, err)
	}
	if !store.updateInput.HasDescription || store.updateInput.Description != nil {
		t.Fatalf("description input = %+v", store.updateInput)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
	if result.Team.Description != "" {
		t.Fatalf("result description = %q", result.Team.Description)
	}
}

func TestUpdateRejectsInvalidInput(t *testing.T) {
	longName := strings.Repeat("名", maxTeamNameRunes+1)
	longDescription := strings.Repeat("说明", maxTeamDescriptionRunes)
	tests := []struct {
		name  string
		input UpdateInput
	}{
		{name: "missing id", input: UpdateInput{UpdatedBy: "sys_admin"}},
		{name: "missing updater", input: UpdateInput{TeamID: "team_ops"}},
		{name: "blank name", input: UpdateInput{TeamID: "team_ops", Name: stringPtr(" "), UpdatedBy: "sys_admin"}},
		{name: "long name", input: UpdateInput{TeamID: "team_ops", Name: &longName, UpdatedBy: "sys_admin"}},
		{name: "invalid status", input: UpdateInput{TeamID: "team_ops", Status: stringPtr("archived"), UpdatedBy: "sys_admin"}},
		{name: "long description", input: UpdateInput{TeamID: "team_ops", HasDescription: true, Description: &longDescription, UpdatedBy: "sys_admin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &teamStoreStub{}
			service := NewServiceWithOptions(ServiceOptions{Store: store})

			_, _, err := service.Update(context.Background(), tt.input)

			if !errors.Is(err, ErrSystemTeamUpdateInvalid) {
				t.Fatalf("Update() error = %v, want %v", err, ErrSystemTeamUpdateInvalid)
			}
			if store.updateCalled {
				t.Fatal("store should not be called for invalid input")
			}
		})
	}
}

func TestUpdateMapsStoreErrorsAndNotFound(t *testing.T) {
	storeErr := errors.New("postgres down")
	tests := []struct {
		name      string
		found     bool
		err       error
		wantFound bool
		wantErr   error
	}{
		{name: "not found", found: false, err: nil, wantFound: false, wantErr: nil},
		{name: "duplicate name", found: true, err: port.ErrManagementSystemTeamNameExists, wantFound: false, wantErr: ErrSystemTeamNameExists},
		{name: "store error", found: true, err: storeErr, wantFound: false, wantErr: storeErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewServiceWithOptions(ServiceOptions{
				Store: &teamStoreStub{updateFound: tt.found, updateErr: tt.err},
			})

			_, found, err := service.Update(context.Background(), UpdateInput{TeamID: "team_ops", UpdatedBy: "sys_admin"})

			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateReturnsSuccessWhenInvalidationFailsAfterWrite(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		updateFound: true,
		updateResult: port.ManagementSystemTeamUpdateResult{
			Before:               port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			Team:                 port.ManagementSystemTeamDetail{ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "disabled", CreatedAt: now, UpdatedAt: now}},
			AuthorizationChanged: true,
		},
	}
	invalidator := &authorizationInvalidatorStub{
		err: errors.New("redis down"),
		onCall: func(reason string) {
			if !store.updateCalled {
				t.Fatal("invalidator called before update store returned")
			}
			if reason != TeamAuthorizationChangedReason {
				t.Fatalf("invalidation reason = %q, want %q", reason, TeamAuthorizationChangedReason)
			}
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		AuthorizationInvalidator: invalidator,
	})
	status := "disabled"

	result, found, err := service.Update(context.Background(), UpdateInput{TeamID: "team_ops", Status: &status, UpdatedBy: "sys_admin"})

	if err != nil || !found {
		t.Fatalf("Update() found=%v error=%v, want successful write", found, err)
	}
	if result.Team.ID != "team_ops" || result.Team.Status != "disabled" || !result.AuthorizationChanged {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.reason != TeamAuthorizationChangedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestAddMembersNormalizesInputMapsDetailAndInvalidatesAuthorization(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		addFound: true,
		addResult: port.ManagementSystemTeamMemberAddResult{
			Before: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "运维团队", Status: "active", CreatedAt: now, UpdatedAt: now},
				Members: []port.ManagementSystemTeamMemberSummary{{
					SystemAccountID: "sys_old",
					MemberRole:      "member",
					Status:          "active",
					JoinedAt:        now,
					CreatedAt:       now,
					UpdatedAt:       now,
				}},
			},
			Team: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "运维团队", Status: "active", CreatedAt: now, UpdatedAt: now},
				Members: []port.ManagementSystemTeamMemberSummary{{
					SystemAccountID: "sys_old",
					MemberRole:      "member",
					Status:          "active",
					JoinedAt:        now,
					CreatedAt:       now,
					UpdatedAt:       now,
				}, {
					SystemAccountID: "sys_new",
					MemberRole:      "member",
					Status:          "active",
					JoinedAt:        now,
					CreatedAt:       now,
					UpdatedAt:       now,
				}},
			},
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }, AuthorizationInvalidator: invalidator})

	result, found, err := service.AddMembers(context.Background(), AddMembersInput{
		TeamID:           " team_ops ",
		SystemAccountID:  " sys_owner ",
		SystemAccountIDs: []string{" sys_new "},
		CreatedBy:        " sys_admin ",
	})

	if err != nil || !found {
		t.Fatalf("AddMembers() found=%v error=%v", found, err)
	}
	if !store.addCalled ||
		store.addInput.TeamID != "team_ops" ||
		store.addInput.SystemAccountID != "sys_owner" ||
		len(store.addInput.SystemAccountIDs) != 1 ||
		store.addInput.SystemAccountIDs[0] != "sys_new" ||
		store.addInput.CreatedBy != "sys_admin" ||
		!store.addInput.UpdatedAt.Equal(now) {
		t.Fatalf("store add input = %+v", store.addInput)
	}
	if len(result.Before.Members) != 1 || len(result.Team.Members) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.reason != TeamMembersChangedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestAddMembersRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input AddMembersInput
	}{
		{name: "missing team", input: AddMembersInput{SystemAccountIDs: []string{"sys_user"}, CreatedBy: "sys_admin"}},
		{name: "missing creator", input: AddMembersInput{TeamID: "team_ops", SystemAccountIDs: []string{"sys_user"}}},
		{name: "empty ids", input: AddMembersInput{TeamID: "team_ops", CreatedBy: "sys_admin"}},
		{name: "blank id", input: AddMembersInput{TeamID: "team_ops", SystemAccountIDs: []string{" "}, CreatedBy: "sys_admin"}},
		{name: "duplicate ids", input: AddMembersInput{TeamID: "team_ops", SystemAccountIDs: []string{"sys_user", " sys_user "}, CreatedBy: "sys_admin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &teamStoreStub{}
			service := NewServiceWithOptions(ServiceOptions{Store: store})

			_, _, err := service.AddMembers(context.Background(), tt.input)

			if !errors.Is(err, ErrSystemTeamMemberInvalid) {
				t.Fatalf("AddMembers() error = %v, want %v", err, ErrSystemTeamMemberInvalid)
			}
			if store.addCalled {
				t.Fatal("store should not be called for invalid input")
			}
		})
	}
}

func TestAddMembersReturnsSuccessWhenInvalidationFailsAfterWrite(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		addFound: true,
		addResult: port.ManagementSystemTeamMemberAddResult{
			Before: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			},
			Team: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
				Members: []port.ManagementSystemTeamMemberSummary{{
					ID:              "teammem_new",
					TeamID:          "team_ops",
					SystemAccountID: "sys_new",
					MemberRole:      "member",
					Status:          "active",
					JoinedAt:        now,
					CreatedAt:       now,
					UpdatedAt:       now,
				}},
			},
		},
	}
	invalidator := &authorizationInvalidatorStub{
		err: errors.New("redis down"),
		onCall: func(reason string) {
			if !store.addCalled {
				t.Fatal("invalidator called before add members store returned")
			}
			if reason != TeamMembersChangedReason {
				t.Fatalf("invalidation reason = %q, want %q", reason, TeamMembersChangedReason)
			}
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		AuthorizationInvalidator: invalidator,
	})

	result, found, err := service.AddMembers(context.Background(), AddMembersInput{
		TeamID:           "team_ops",
		SystemAccountIDs: []string{"sys_new"},
		CreatedBy:        "sys_admin",
	})

	if err != nil || !found {
		t.Fatalf("AddMembers() found=%v error=%v, want successful write", found, err)
	}
	if result.Team.ID != "team_ops" || len(result.Team.Members) != 1 || result.Team.Members[0].SystemAccountID != "sys_new" {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.reason != TeamMembersChangedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestRemoveMemberNormalizesInputMapsRemovedMemberAndInvalidatesAuthorization(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		removeFound: true,
		removeResult: port.ManagementSystemTeamMemberRemoveResult{
			Team: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "运维团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			},
			RemovedMember: port.ManagementSystemTeamMemberSummary{
				ID:              "teammem_old",
				SystemAccountID: "sys_old",
				MemberRole:      "member",
				Status:          "active",
				JoinedAt:        now,
				CreatedAt:       now,
				UpdatedAt:       now,
			},
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }, AuthorizationInvalidator: invalidator})

	result, found, err := service.RemoveMember(context.Background(), RemoveMemberInput{
		TeamID:          " team_ops ",
		MemberID:        " teammem_old ",
		SystemAccountID: " sys_owner ",
		UpdatedBy:       " sys_admin ",
	})

	if err != nil || !found {
		t.Fatalf("RemoveMember() found=%v error=%v", found, err)
	}
	if !store.removeCalled ||
		store.removeInput.TeamID != "team_ops" ||
		store.removeInput.MemberID != "teammem_old" ||
		store.removeInput.SystemAccountID != "sys_owner" ||
		store.removeInput.UpdatedBy != "sys_admin" ||
		!store.removeInput.UpdatedAt.Equal(now) {
		t.Fatalf("store remove input = %+v", store.removeInput)
	}
	if result.Team.ID != "team_ops" || result.RemovedMember.SystemAccountID != "sys_old" {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.reason != TeamMembersChangedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestRemoveMemberReturnsSuccessWhenInvalidationFailsAfterWrite(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &teamStoreStub{
		removeFound: true,
		removeResult: port.ManagementSystemTeamMemberRemoveResult{
			Before: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			},
			Team: port.ManagementSystemTeamDetail{
				ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Name: "团队", Status: "active", CreatedAt: now, UpdatedAt: now},
			},
			RemovedMember: port.ManagementSystemTeamMemberSummary{
				ID:              "teammem_old",
				TeamID:          "team_ops",
				SystemAccountID: "sys_old",
				MemberRole:      "member",
				Status:          "removed",
				JoinedAt:        now,
				CreatedAt:       now,
				UpdatedAt:       now,
			},
		},
	}
	invalidator := &authorizationInvalidatorStub{
		err: errors.New("redis down"),
		onCall: func(reason string) {
			if !store.removeCalled {
				t.Fatal("invalidator called before remove member store returned")
			}
			if reason != TeamMembersChangedReason {
				t.Fatalf("invalidation reason = %q, want %q", reason, TeamMembersChangedReason)
			}
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		AuthorizationInvalidator: invalidator,
	})

	result, found, err := service.RemoveMember(context.Background(), RemoveMemberInput{
		TeamID:    "team_ops",
		MemberID:  "teammem_old",
		UpdatedBy: "sys_admin",
	})

	if err != nil || !found {
		t.Fatalf("RemoveMember() found=%v error=%v, want successful write", found, err)
	}
	if result.Team.ID != "team_ops" || result.RemovedMember.ID != "teammem_old" || result.RemovedMember.SystemAccountID != "sys_old" {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.reason != TeamMembersChangedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestTeamWritesPublishAccountsStaticResetAfterCommittedWrite(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	member := func(id string) port.ManagementSystemTeamMemberSummary {
		return port.ManagementSystemTeamMemberSummary{SystemAccountID: id, Status: "active", JoinedAt: now, CreatedAt: now, UpdatedAt: now}
	}
	tests := []struct {
		name       string
		store      *teamStoreStub
		invoke     func(context.Context, *Service) error
		wantOwners []string
	}{
		{
			name: "status update publishes current team members",
			store: &teamStoreStub{updateFound: true, updateResult: port.ManagementSystemTeamUpdateResult{
				Before: port.ManagementSystemTeamSummary{ID: "team_ops", Status: "active", CreatedAt: now, UpdatedAt: now},
				Team: port.ManagementSystemTeamDetail{
					ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Status: "disabled", CreatedAt: now, UpdatedAt: now},
					Members:                     []port.ManagementSystemTeamMemberSummary{member(" owner-b "), member("owner-a"), member("owner-a")},
				},
				AuthorizationChanged: true,
			}},
			invoke: func(ctx context.Context, service *Service) error {
				status := "disabled"
				_, _, err := service.Update(ctx, UpdateInput{TeamID: "team_ops", Status: &status, UpdatedBy: "admin"})
				return err
			},
			wantOwners: []string{"owner-a", "owner-b"},
		},
		{
			name: "add publishes only actual new members",
			store: &teamStoreStub{addFound: true, addResult: port.ManagementSystemTeamMemberAddResult{
				Before: port.ManagementSystemTeamDetail{Members: []port.ManagementSystemTeamMemberSummary{member("owner-old")}},
				Team:   port.ManagementSystemTeamDetail{Members: []port.ManagementSystemTeamMemberSummary{member("owner-old"), member("owner-new")}},
			}},
			invoke: func(ctx context.Context, service *Service) error {
				_, _, err := service.AddMembers(ctx, AddMembersInput{TeamID: "team_ops", SystemAccountIDs: []string{"owner-new"}, CreatedBy: "admin"})
				return err
			},
			wantOwners: []string{"owner-new"},
		},
		{
			name: "remove publishes removed member",
			store: &teamStoreStub{removeFound: true, removeResult: port.ManagementSystemTeamMemberRemoveResult{
				RemovedMember: member(" owner-old "),
			}},
			invoke: func(ctx context.Context, service *Service) error {
				_, _, err := service.RemoveMember(ctx, RemoveMemberInput{TeamID: "team_ops", MemberID: "member-old", UpdatedBy: "admin"})
				return err
			},
			wantOwners: []string{"owner-old"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &accountsStaticResetPublisherStub{err: errors.New("redis unavailable")}
			var logs bytes.Buffer
			service := NewServiceWithOptions(ServiceOptions{
				Store:     test.store,
				Publisher: publisher,
				Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := test.invoke(ctx, service); err != nil {
				t.Fatalf("write returned publisher error: %v", err)
			}
			if publisher.calls != 1 || publisher.allScopes || !reflect.DeepEqual(publisher.owners, test.wantOwners) {
				t.Fatalf("publisher calls=%d owners=%#v allScopes=%v", publisher.calls, publisher.owners, publisher.allScopes)
			}
			if publisher.contextErr != nil {
				t.Fatalf("publisher context error = %v, want detached context", publisher.contextErr)
			}
			if !publisher.hasDeadline || publisher.deadlineRemaining <= 0 || publisher.deadlineRemaining > pageDataPublishTimeout {
				t.Fatalf("publisher deadline present=%v remaining=%v", publisher.hasDeadline, publisher.deadlineRemaining)
			}
			if !strings.Contains(logs.String(), "level=WARN") ||
				!strings.Contains(logs.String(), "domain=accounts.static") ||
				!strings.Contains(logs.String(), "redis unavailable") {
				t.Fatalf("warning log = %q", logs.String())
			}
		})
	}
}

func TestTeamWritesSkipAccountsStaticResetWhenVisibleAccountsDoNotChange(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	publisher := &accountsStaticResetPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store: &teamStoreStub{updateFound: true, updateResult: port.ManagementSystemTeamUpdateResult{
			Before: port.ManagementSystemTeamSummary{ID: "team_ops", Status: "active", CreatedAt: now, UpdatedAt: now},
			Team:   port.ManagementSystemTeamDetail{ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", Status: "active", CreatedAt: now, UpdatedAt: now}},
		}},
		Publisher: publisher,
	})
	name := "renamed"
	if _, found, err := service.Update(context.Background(), UpdateInput{TeamID: "team_ops", Name: &name, UpdatedBy: "admin"}); err != nil || !found {
		t.Fatalf("Update() found=%v error=%v", found, err)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0", publisher.calls)
	}
}

func TestTeamWritesPublishDependentPageDataResetsAfterCommittedWrite(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		store  *teamStoreStub
		invoke func(context.Context, *Service) error
	}{
		{
			name:  "create",
			store: &teamStoreStub{result: port.ManagementSystemTeamSummary{ID: "team_new", CreatedAt: now, UpdatedAt: now}},
			invoke: func(ctx context.Context, service *Service) error {
				_, err := service.Create(ctx, CreateInput{Name: "新团队", CreatedBy: "admin"})
				return err
			},
		},
		{
			name: "update",
			store: &teamStoreStub{updateFound: true, updateResult: port.ManagementSystemTeamUpdateResult{
				Team: port.ManagementSystemTeamDetail{ManagementSystemTeamSummary: port.ManagementSystemTeamSummary{ID: "team_ops", CreatedAt: now, UpdatedAt: now}},
			}},
			invoke: func(ctx context.Context, service *Service) error {
				name := "重命名团队"
				_, _, err := service.Update(ctx, UpdateInput{TeamID: "team_ops", Name: &name, UpdatedBy: "admin"})
				return err
			},
		},
		{
			name:  "add members",
			store: &teamStoreStub{addFound: true},
			invoke: func(ctx context.Context, service *Service) error {
				_, _, err := service.AddMembers(ctx, AddMembersInput{TeamID: "team_ops", SystemAccountIDs: []string{"sys_new"}, CreatedBy: "admin"})
				return err
			},
		},
		{
			name: "remove member",
			store: &teamStoreStub{removeFound: true, removeResult: port.ManagementSystemTeamMemberRemoveResult{
				RemovedMember: port.ManagementSystemTeamMemberSummary{SystemAccountID: "sys_old"},
			}},
			invoke: func(ctx context.Context, service *Service) error {
				_, _, err := service.RemoveMember(ctx, RemoveMemberInput{TeamID: "team_ops", MemberID: "member_old", UpdatedBy: "admin"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &accountsStaticResetPublisherStub{domainErr: errors.New("page data unavailable"), domainDelay: 40 * time.Millisecond}
			service := NewServiceWithOptions(ServiceOptions{
				Store: test.store, Publisher: publisher, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			startedAt := time.Now()
			if err := test.invoke(ctx, service); err != nil {
				t.Fatalf("write returned page data error: %v", err)
			}
			if elapsed := time.Since(startedAt); elapsed >= 150*time.Millisecond {
				t.Fatalf("five page data resets took %s, want concurrent completion", elapsed)
			}
			assertTeamDependentPageDataResets(t, publisher)
		})
	}
}

func TestTeamWritesSkipDependentPageDataResetsBeforeCommit(t *testing.T) {
	wantErr := errors.New("postgres unavailable")
	tests := []struct {
		name   string
		store  *teamStoreStub
		invoke func(*Service) error
	}{
		{
			name: "create store error", store: &teamStoreStub{err: wantErr},
			invoke: func(service *Service) error {
				_, err := service.Create(context.Background(), CreateInput{Name: "团队", CreatedBy: "admin"})
				return err
			},
		},
		{
			name: "update not found", store: &teamStoreStub{},
			invoke: func(service *Service) error {
				name := "重命名"
				_, _, err := service.Update(context.Background(), UpdateInput{TeamID: "team_ops", Name: &name, UpdatedBy: "admin"})
				return err
			},
		},
		{
			name: "add not found", store: &teamStoreStub{},
			invoke: func(service *Service) error {
				_, _, err := service.AddMembers(context.Background(), AddMembersInput{TeamID: "team_ops", SystemAccountIDs: []string{"sys_new"}, CreatedBy: "admin"})
				return err
			},
		},
		{
			name: "remove not found", store: &teamStoreStub{},
			invoke: func(service *Service) error {
				_, _, err := service.RemoveMember(context.Background(), RemoveMemberInput{TeamID: "team_ops", MemberID: "member_old", UpdatedBy: "admin"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &accountsStaticResetPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{Store: test.store, Publisher: publisher})
			_ = test.invoke(service)
			if len(publisher.domainCalls) != 0 {
				t.Fatalf("page data calls = %#v, want none", publisher.domainCalls)
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
	updateCalled          bool
	updateInput           port.ManagementSystemTeamUpdateInput
	updateResult          port.ManagementSystemTeamUpdateResult
	updateFound           bool
	updateErr             error
	addCalled             bool
	addInput              port.ManagementSystemTeamMemberAddInput
	addResult             port.ManagementSystemTeamMemberAddResult
	addFound              bool
	addErr                error
	removeCalled          bool
	removeInput           port.ManagementSystemTeamMemberRemoveInput
	removeResult          port.ManagementSystemTeamMemberRemoveResult
	removeFound           bool
	removeErr             error
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

func (s *teamStoreStub) UpdateManagementSystemTeam(_ context.Context, input port.ManagementSystemTeamUpdateInput) (port.ManagementSystemTeamUpdateResult, bool, error) {
	s.updateCalled = true
	s.updateInput = input
	return s.updateResult, s.updateFound, s.updateErr
}

func (s *teamStoreStub) AddManagementSystemTeamMembers(_ context.Context, input port.ManagementSystemTeamMemberAddInput) (port.ManagementSystemTeamMemberAddResult, bool, error) {
	s.addCalled = true
	s.addInput = input
	return s.addResult, s.addFound, s.addErr
}

func (s *teamStoreStub) RemoveManagementSystemTeamMember(_ context.Context, input port.ManagementSystemTeamMemberRemoveInput) (port.ManagementSystemTeamMemberRemoveResult, bool, error) {
	s.removeCalled = true
	s.removeInput = input
	return s.removeResult, s.removeFound, s.removeErr
}

type authorizationInvalidatorStub struct {
	calls  int
	reason string
	err    error
	onCall func(reason string)
}

type accountsStaticResetPublisherStub struct {
	mu                sync.Mutex
	calls             int
	owners            []string
	allScopes         bool
	contextErr        error
	hasDeadline       bool
	deadlineRemaining time.Duration
	err               error
	domainCalls       []teamPageDataResetCall
	domainErr         error
	domainDelay       time.Duration
}

type teamPageDataResetCall struct {
	domain      string
	owners      []string
	allScopes   bool
	contextErr  error
	hasDeadline bool
}

func (s *accountsStaticResetPublisherStub) PublishAccountsStaticReset(ctx context.Context, owners []string, allScopes bool) error {
	s.calls++
	s.owners = append([]string(nil), owners...)
	s.allScopes = allScopes
	s.contextErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	s.hasDeadline = ok
	if ok {
		s.deadlineRemaining = time.Until(deadline)
	}
	return s.err
}

func (s *accountsStaticResetPublisherStub) PublishPageDataReset(ctx context.Context, domain string, owners []string, allScopes bool) error {
	if s.domainDelay > 0 {
		time.Sleep(s.domainDelay)
	}
	_, hasDeadline := ctx.Deadline()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domainCalls = append(s.domainCalls, teamPageDataResetCall{
		domain: domain, owners: append([]string(nil), owners...), allScopes: allScopes, contextErr: ctx.Err(), hasDeadline: hasDeadline,
	})
	return s.domainErr
}

func assertTeamDependentPageDataResets(t *testing.T, publisher *accountsStaticResetPublisherStub) {
	t.Helper()
	wantDomains := []string{"teams.options", "groups.static", "stats.overview", "stats.accountUsage", "stats.aiPerformance"}
	publisher.mu.Lock()
	calls := append([]teamPageDataResetCall(nil), publisher.domainCalls...)
	publisher.mu.Unlock()
	if len(calls) != len(wantDomains) {
		t.Fatalf("page data calls = %#v, want domains %#v", calls, wantDomains)
	}
	byDomain := make(map[string]teamPageDataResetCall, len(calls))
	for _, call := range calls {
		byDomain[call.domain] = call
	}
	for _, wantDomain := range wantDomains {
		call, ok := byDomain[wantDomain]
		if !ok || len(call.owners) != 0 || !call.allScopes || call.contextErr != nil || !call.hasDeadline {
			t.Fatalf("page data call for %q = %+v found=%v, want global detached deadline", wantDomain, call, ok)
		}
	}
}

func (s *authorizationInvalidatorStub) InvalidateAuthorizationChanged(_ context.Context, reason string) error {
	s.calls++
	s.reason = reason
	if s.onCall != nil {
		s.onCall(reason)
	}
	return s.err
}

func stringPtr(value string) *string {
	return &value
}
