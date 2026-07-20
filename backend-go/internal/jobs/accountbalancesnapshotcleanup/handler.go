package accountbalancesnapshotcleanup

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

func HandleTask(ctx context.Context, store port.AccountBalanceSnapshotCleanupStore, payload []byte) error {
	if store == nil {
		return fmt.Errorf("account balance snapshot cleanup store is required")
	}
	task, err := Decode(payload)
	if err != nil {
		return err
	}
	if err := store.DeleteAccountBalanceSnapshot(ctx, port.AccountBalanceSnapshotCleanupInput{
		AccountID:       task.AccountID,
		SystemAccountID: task.SystemAccountID,
		UpdatedBefore:   task.UpdatedBefore,
		Reason:          task.Reason,
	}); err != nil {
		return fmt.Errorf("delete account balance snapshot task: %w", err)
	}
	return nil
}
