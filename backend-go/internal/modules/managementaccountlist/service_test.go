package managementaccountlist

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListBoundsAndForcesSelfScope(t *testing.T) {
	reader := &listReaderStub{page: port.ManagementAccountListPage{
		Rows: []port.ManagementAccountListRow{{ID: "acc_1", SystemAccountID: "sys_user", Name: "Account", AccessType: "owner", HealthCheckModel: "gpt-5.5", HealthCheckEndpointMode: "responses_sse"}},
	}}
	service := NewService(reader)

	result, err := service.List(context.Background(), Input{
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		SystemAccountID:      "sys_other",
		SelfOnly:             true,
		Page:                 99,
		PageSize:             999,
		PageSizeProvided:     true,
		Statuses:             []string{"active", "bad", "active"},
		Schedulable:          "bad",
		Sorts:                []Sort{{Field: "priority", Order: "desc"}, {Field: "credentials", Order: "asc"}},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if reader.input.SystemAccountID != "sys_user" || reader.input.Limit != 201 || reader.input.Offset != 800 {
		t.Fatalf("reader input = %+v", reader.input)
	}
	if len(reader.input.Statuses) != 1 || reader.input.Statuses[0] != "active" || reader.input.Schedulable != "all" {
		t.Fatalf("filters = %+v / %q", reader.input.Statuses, reader.input.Schedulable)
	}
	if len(reader.input.Sorts) != 1 || reader.input.Sorts[0].Field != "priority" {
		t.Fatalf("sorts = %+v", reader.input.Sorts)
	}
	if result.Page != 5 || result.PageSize != 200 || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].SystemAccountID != "" || result.Items[0].OwnerSystemAccountID != "sys_user" {
		t.Fatalf("item scope fields = %+v", result.Items[0])
	}
	if result.Items[0].HealthCheckModel != "gpt-5.5" || result.Items[0].HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("health check fields = %+v", result.Items[0])
	}
}

func TestServiceListAdminGlobalIncludesSystemAccount(t *testing.T) {
	reader := &listReaderStub{page: port.ManagementAccountListPage{Rows: []port.ManagementAccountListRow{
		{ID: "acc_1", SystemAccountID: "sys_owner", SystemAccountName: "Owner", Name: "Account", AccessType: "owner"},
	}}}
	result, err := NewService(reader).List(context.Background(), Input{ActorSystemAccountID: "sys_admin", ActorRole: "admin"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if reader.input.SystemAccountID != "" || result.Items[0].SystemAccountID != "sys_owner" {
		t.Fatalf("scope/result = %+v / %+v", reader.input, result.Items[0])
	}
}

type listReaderStub struct {
	input port.ManagementAccountListInput
	page  port.ManagementAccountListPage
	err   error
}

func (s *listReaderStub) ListManagementAccounts(_ context.Context, input port.ManagementAccountListInput) (port.ManagementAccountListPage, error) {
	s.input = input
	return s.page, s.err
}
