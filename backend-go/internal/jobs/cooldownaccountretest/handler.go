package cooldownaccountretest

import (
	"context"
	"juhe-ai/backend-go/internal/modules/cooldownaccountretest"
)

func HandleTask(ctx context.Context, processor cooldownaccountretest.Processor, payloadBytes []byte) error {
	payload, err := DecodeTask(payloadBytes)
	if err != nil {
		return err
	}
	if err := processor.RunTask(ctx, payload); err != nil {
		return err
	}
	return nil
}
