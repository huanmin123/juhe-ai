package managementgroups

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestLoadManagementGroupCurrentConcurrencyDeduplicatesSourceAccounts(t *testing.T) {
	store := &managementGroupListStoreStub{concurrencyAccountRows: []port.ManagementGroupConcurrencyAccountIDRow{
		{GroupID: "grp_owner", AccountID: "acc_source"},
		{GroupID: "grp_owner", AccountID: "acc_source"},
		{GroupID: "grp_owner", AccountID: "acc_other"},
		{GroupID: "grp_authorized", AccountID: "acc_source"},
	}}
	reader := &managementGroupConcurrencyReaderStub{values: map[string]int{"acc_source": 4, "acc_other": -2}}
	service := NewServiceWithOptions(ServiceOptions{Store: store, AccountConcurrency: reader})

	values, available, err := service.loadManagementGroupCurrentConcurrency(context.Background(), []string{"grp_owner", "grp_authorized"}, time.Now())
	if err != nil {
		t.Fatalf("loadManagementGroupCurrentConcurrency() error = %v", err)
	}
	if !available || values["grp_owner"] != 4 || values["grp_authorized"] != 4 {
		t.Fatalf("available=%v values=%#v", available, values)
	}
	if len(reader.calls) != 1 || !sameStrings(reader.calls[0], []string{"acc_source", "acc_other"}) {
		t.Fatalf("concurrency calls = %#v", reader.calls)
	}
}
