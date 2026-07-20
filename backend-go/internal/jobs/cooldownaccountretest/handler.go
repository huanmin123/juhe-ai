package cooldownaccountretest

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
	"juhe-ai/backend-go/internal/modules/cooldownaccountretest"
)

func HandleTask(ctx context.Context, processor cooldownaccountretest.Processor, task *asynq.Task) error {
	if task == nil {
		return fmt.Errorf("cooldown account retest task is required")
	}
	payload, err := DecodeTask(task.Payload())
	if err != nil {
		return err
	}
	if err := processor.RunTask(ctx, payload); err != nil {
		return err
	}
	return nil
}
