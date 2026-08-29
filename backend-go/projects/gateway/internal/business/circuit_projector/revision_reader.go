package circuitprojector

import (
	"context"
	"errors"

	control "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	runtime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
)

// DispatchRevisionReader adapts the Business SQL read model to the runtime
// index backfiller without exposing control-plane implementation details to
// the Redis owner.
type DispatchRevisionReader struct {
	Store *control.Store
}

func (r DispatchRevisionReader) ListGatewayAccountCircuitDispatchRevisions(ctx context.Context, input runtime.GatewayAccountCircuitDispatchRevisionPageInput) (runtime.GatewayAccountCircuitDispatchRevisionPage, error) {
	if r.Store == nil {
		return runtime.GatewayAccountCircuitDispatchRevisionPage{}, errors.New("dispatch revision reader store is required")
	}
	page, err := r.Store.ListDispatchRevisions(ctx, input.AfterAccountID, input.Limit)
	if err != nil {
		return runtime.GatewayAccountCircuitDispatchRevisionPage{}, err
	}
	items := make([]runtime.GatewayAccountCircuitDispatchRevisionSnapshot, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, runtime.GatewayAccountCircuitDispatchRevisionSnapshot{AccountID: item.AccountID, DispatchRevision: item.DispatchRevision})
	}
	return runtime.GatewayAccountCircuitDispatchRevisionPage{Items: items, NextAfterAccountID: page.NextAfterAccountID}, nil
}
