package publicapilog

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

func HandleWriteTask(ctx context.Context, store port.PublicAPILogStore, payload []byte) error {
	if store == nil {
		return fmt.Errorf("public api log store is required")
	}
	input, err := DecodeWriteTaskPayload(payload)
	if err != nil {
		return err
	}
	if err := store.InsertPublicAPILog(ctx, input); err != nil {
		return fmt.Errorf("write public api log task: %w", err)
	}
	return nil
}
