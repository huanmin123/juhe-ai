package accountbalanceautodetect

import (
	"context"
	"fmt"

	accountbalanceservice "juhe-ai/backend-go/internal/modules/accountbalanceautodetect"
)

type Runner interface {
	Run(ctx context.Context, input accountbalanceservice.Input) (accountbalanceservice.Result, error)
}

func HandleTask(ctx context.Context, runner Runner, payload []byte) error {
	if runner == nil {
		return fmt.Errorf("account balance auto detect runner is required")
	}
	task, err := Decode(payload)
	if err != nil {
		return err
	}
	_, err = runner.Run(ctx, accountbalanceservice.Input{AccountID: task.AccountID, ConfigRevision: task.ConfigRevision})
	if err != nil {
		return fmt.Errorf("run account balance auto detect task: %w", err)
	}
	return nil
}
