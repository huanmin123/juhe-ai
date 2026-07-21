package accounttest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

type Runner interface {
	RunAccountTest(context.Context, port.ManagementAccountTestTask) (map[string]any, error)
}

func HandleTask(ctx context.Context, store port.AccountTestWorkerStore, runner Runner, payload []byte) error {
	if store == nil || runner == nil {
		return fmt.Errorf("account test worker dependencies are required")
	}
	taskPayload, err := Decode(payload)
	if err != nil {
		return err
	}
	task, claimed, err := store.ClaimAccountTestTask(ctx, taskPayload.TaskID)
	if err != nil {
		return fmt.Errorf("claim account test task: %w", err)
	}
	if !claimed {
		return nil
	}
	result, runErr := runner.RunAccountTest(ctx, task)
	if runErr == nil {
		if err := store.FinishAccountTestTask(ctx, port.AccountTestWorkerFinishInput{TaskID: task.ID, Status: "success", Result: result}); err != nil {
			return fmt.Errorf("complete account test task: %w", err)
		}
		return nil
	}
	status := "failed"
	message := strings.TrimSpace(runErr.Error())
	if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		status = "canceled"
		message = "账户测试已取消"
	}
	if err := store.FinishAccountTestTask(context.WithoutCancel(ctx), port.AccountTestWorkerFinishInput{TaskID: task.ID, Status: status, Message: message}); err != nil {
		return fmt.Errorf("finish failed account test task: %w", err)
	}
	return fmt.Errorf("run account test task: %w", runErr)
}
