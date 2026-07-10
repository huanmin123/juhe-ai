package operationlog

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

func HandleWriteTask(ctx context.Context, store port.OperationLogStore, payload []byte) error {
	if store == nil {
		return fmt.Errorf("operation log store is required")
	}
	input, err := DecodeWriteTaskPayload(payload)
	if err != nil {
		return err
	}
	if err := store.InsertOperationLog(ctx, input); err != nil {
		return fmt.Errorf("write operation log task: %w", err)
	}
	return nil
}
