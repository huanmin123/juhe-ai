package managementaccountbatchedit

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type fakeBatchStore struct {
	accounts []port.ManagementAccountBatchEditAccount
	updateOK bool
}

func (f fakeBatchStore) LoadManagementAccountBatchEditContext(context.Context, string, []string) ([]port.ManagementAccountBatchEditAccount, bool, error) {
	return f.accounts, true, nil
}
func (f fakeBatchStore) UpdateManagementAccountsBatch(context.Context, port.ManagementAccountBatchEditInput) (port.ManagementAccountBatchEditResult, bool, error) {
	return port.ManagementAccountBatchEditResult{BatchID: "batch_test"}, f.updateOK, nil
}

func TestContextRejectsMixedOwners(t *testing.T) {
	store := fakeBatchStore{accounts: []port.ManagementAccountBatchEditAccount{{ID: "a", SystemAccountID: "one"}, {ID: "b", SystemAccountID: "two"}}}
	_, err := NewService(store, store).Context(context.Background(), "", []string{"a", "b"})
	if !errors.Is(err, ErrSameScope) {
		t.Fatalf("expected same scope error, got %v", err)
	}
}

func TestUpdateMapsVersionConflict(t *testing.T) {
	store := fakeBatchStore{updateOK: false}
	_, err := NewService(store, store).Update(context.Background(), port.ManagementAccountBatchEditInput{Targets: []port.ManagementAccountBatchEditTarget{{AccountID: "a", ConfigRevision: 1}, {AccountID: "b", ConfigRevision: 2}}, Updates: map[string]any{"priority": 2}})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
}
