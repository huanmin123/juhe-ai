package accounttest

import (
	"context"
	"fmt"
)

type Dispatcher interface {
	Dispatch(context.Context, string) error
}

func HandleDispatchTask(ctx context.Context, dispatcher Dispatcher, payload []byte) error {
	if dispatcher == nil {
		return fmt.Errorf("account test dispatcher is required")
	}
	decoded, err := Decode(payload)
	if err != nil {
		return err
	}
	if err := dispatcher.Dispatch(ctx, decoded.TaskID); err != nil {
		return fmt.Errorf("dispatch account test task to node: %w", err)
	}
	return nil
}
