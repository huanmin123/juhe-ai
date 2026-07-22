package managementaccountlist

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListBoundsAndForcesSelfScope(t *testing.T) {
	reader := &listReaderStub{page: port.ManagementAccountListPage{
		Rows: []port.ManagementAccountListRow{{ID: "acc_1", SystemAccountID: "sys_user", Name: "Account", AccessType: "owner"}},
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

func TestServiceListProxyDisplayIsRoleScoped(t *testing.T) {
	enabled := true
	disabled := false
	reader := &listReaderStub{page: port.ManagementAccountListPage{Rows: []port.ManagementAccountListRow{
		{ID: "enabled", SystemAccountID: "sys_user", Name: "Enabled", AccessType: "owner", ProxyProfileID: "proxy-enabled", ProxyProfileName: "启用代理", ProxyProfileType: "http", ProxyProfileEnabled: &enabled},
		{ID: "disabled", SystemAccountID: "sys_user", Name: "Disabled", AccessType: "owner", ProxyProfileID: "proxy-disabled", ProxyProfileName: "停用代理", ProxyProfileType: "socks5", ProxyProfileEnabled: &disabled},
		{ID: "missing", SystemAccountID: "sys_user", Name: "Missing", AccessType: "owner", ProxyProfileID: "proxy-missing"},
		{ID: "authorized-enabled", SystemAccountID: "sys_user", Name: "Authorized enabled", AccessType: "authorized", ProxyProfileID: "proxy-enabled", ProxyProfileName: "启用代理", ProxyProfileType: "http", ProxyProfileEnabled: &enabled},
		{ID: "authorized-disabled", SystemAccountID: "sys_user", Name: "Authorized disabled", AccessType: "authorized", ProxyProfileID: "proxy-disabled", ProxyProfileName: "停用代理", ProxyProfileType: "socks5", ProxyProfileEnabled: &disabled},
		{ID: "authorized-missing", SystemAccountID: "sys_user", Name: "Authorized missing", AccessType: "authorized", ProxyProfileID: "proxy-missing"},
	}}}

	userResult, err := NewService(reader).List(context.Background(), Input{ActorSystemAccountID: "sys_user", ActorRole: "user"})
	if err != nil {
		t.Fatalf("user List() error = %v", err)
	}
	if got := userResult.Items[0]; got.ProxyProfileID != "proxy-enabled" || got.ProxyProfileName != "启用代理" || got.ProxyProfileType != "http" || got.ProxyProfileEnabled == nil || !*got.ProxyProfileEnabled || got.ProxyProfileUnavailable {
		t.Fatalf("enabled proxy display = %+v", got)
	}
	if got := userResult.Items[1]; got.ProxyProfileID != "proxy-disabled" || got.ProxyProfileName != "" || got.ProxyProfileType != "" || got.ProxyProfileEnabled != nil || !got.ProxyProfileUnavailable || got.ProxyProfileErrorMessage == "" {
		t.Fatalf("disabled proxy display = %+v", got)
	}
	if got := userResult.Items[2]; got.ProxyProfileID != "proxy-missing" || got.ProxyProfileName != "" || !got.ProxyProfileUnavailable || got.ProxyProfileErrorMessage == "" {
		t.Fatalf("missing proxy display = %+v", got)
	}
	if got := userResult.Items[3]; got.AccessType != "authorized" || got.ProxyProfileName != "启用代理" || got.ProxyProfileEnabled == nil || !*got.ProxyProfileEnabled {
		t.Fatalf("authorized enabled proxy display = %+v", got)
	}
	if got := userResult.Items[4]; got.AccessType != "authorized" || got.ProxyProfileID != "proxy-disabled" || got.ProxyProfileName != "" || got.ProxyProfileEnabled != nil || !got.ProxyProfileUnavailable {
		t.Fatalf("authorized disabled proxy display = %+v", got)
	}
	if got := userResult.Items[5]; got.AccessType != "authorized" || got.ProxyProfileID != "proxy-missing" || got.ProxyProfileName != "" || !got.ProxyProfileUnavailable {
		t.Fatalf("authorized missing proxy display = %+v", got)
	}

	adminResult, err := NewService(reader).List(context.Background(), Input{ActorSystemAccountID: "sys_admin", ActorRole: "admin"})
	if err != nil {
		t.Fatalf("admin List() error = %v", err)
	}
	if got := adminResult.Items[1]; got.ProxyProfileID != "proxy-disabled" || got.ProxyProfileName != "停用代理" || got.ProxyProfileType != "socks5" || got.ProxyProfileEnabled == nil || *got.ProxyProfileEnabled || !got.ProxyProfileUnavailable {
		t.Fatalf("admin disabled proxy display = %+v", got)
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
