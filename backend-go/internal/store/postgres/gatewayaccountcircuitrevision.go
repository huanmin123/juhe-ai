package postgres

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) ListGatewayAccountCircuitDispatchRevisions(ctx context.Context, input port.GatewayAccountCircuitDispatchRevisionPageInput) (port.GatewayAccountCircuitDispatchRevisionPage, error) {
	if input.Limit < 1 || input.Limit > port.GatewayAccountCircuitRuntimeMaxRevisionPage || (input.AfterAccountID != "" && !validGatewayAccountCircuitOutboxText(input.AfterAccountID, 256)) {
		return port.GatewayAccountCircuitDispatchRevisionPage{}, fmt.Errorf("gateway account circuit dispatch revision page input is invalid")
	}
	rows, err := s.pool.Query(ctx, listGatewayAccountCircuitDispatchRevisionsSQL, input.AfterAccountID, input.Limit+1)
	if err != nil {
		return port.GatewayAccountCircuitDispatchRevisionPage{}, fmt.Errorf("list gateway account circuit dispatch revisions: %w", err)
	}
	defer rows.Close()
	items := make([]port.GatewayAccountCircuitDispatchRevisionSnapshot, 0, input.Limit+1)
	for rows.Next() {
		var item port.GatewayAccountCircuitDispatchRevisionSnapshot
		if err := rows.Scan(&item.AccountID, &item.DispatchRevision); err != nil {
			return port.GatewayAccountCircuitDispatchRevisionPage{}, fmt.Errorf("scan gateway account circuit dispatch revision: %w", err)
		}
		if !validGatewayAccountCircuitOutboxText(item.AccountID, 256) || item.DispatchRevision < 1 {
			return port.GatewayAccountCircuitDispatchRevisionPage{}, fmt.Errorf("gateway account circuit dispatch revision snapshot is invalid")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return port.GatewayAccountCircuitDispatchRevisionPage{}, fmt.Errorf("read gateway account circuit dispatch revisions: %w", err)
	}
	result := port.GatewayAccountCircuitDispatchRevisionPage{Items: items}
	if len(items) > input.Limit {
		result.Items = items[:input.Limit]
		result.NextAfterAccountID = result.Items[len(result.Items)-1].AccountID
	}
	return result, nil
}

var _ port.GatewayAccountCircuitDispatchRevisionReader = (*Store)(nil)
