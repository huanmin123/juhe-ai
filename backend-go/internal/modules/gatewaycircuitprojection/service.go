package gatewaycircuitprojection

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultRebuildPageSize = 500
	DefaultRebuildMaxPages = 10000
)

type IncidentProjector struct {
	reader   port.GatewayAccountCircuitIncidentReader
	restorer port.GatewayAccountCircuitIncidentRestorer
}

func NewIncidentProjector(reader port.GatewayAccountCircuitIncidentReader, restorer port.GatewayAccountCircuitIncidentRestorer) (*IncidentProjector, error) {
	if reader == nil {
		return nil, fmt.Errorf("gateway account circuit incident reader is required")
	}
	if restorer == nil {
		return nil, fmt.Errorf("gateway account circuit incident restorer is required")
	}
	return &IncidentProjector{reader: reader, restorer: restorer}, nil
}

func (p *IncidentProjector) ProjectGatewayAccountCircuitIncident(ctx context.Context, event port.GatewayAccountCircuitOutboxEvent) (port.GatewayAccountCircuitRevisionProjection, error) {
	load, err := p.reader.LoadGatewayAccountCircuitIncidentForProjection(ctx, event)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	switch load.Status {
	case port.GatewayAccountCircuitIncidentStale:
		if load.CurrentDispatchRevision < 1 {
			return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("stale account circuit incident has invalid current revision")
		}
		return port.GatewayAccountCircuitRevisionProjection{
			Status: port.GatewayAccountCircuitRevisionStale, CurrentRevision: load.CurrentDispatchRevision, Obsolete: true,
		}, nil
	case port.GatewayAccountCircuitIncidentMissing:
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit incident ledger snapshot is missing")
	case port.GatewayAccountCircuitIncidentCurrent:
		projection, err := p.restorer.RestoreGatewayAccountCircuitIncident(ctx, load.Incident)
		if err != nil {
			return port.GatewayAccountCircuitRevisionProjection{}, err
		}
		if projection.Status == port.GatewayAccountCircuitRevisionStale {
			projection.Obsolete = true
			return projection, nil
		}
		projection.IncidentID = load.Incident.IncidentID
		projection.LedgerRevision = load.Incident.LedgerRevision
		return projection, nil
	default:
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit incident load status is invalid")
	}
}

type RebuildInput struct {
	Now      time.Time
	PageSize int
	MaxPages int
}

type RebuildResult struct {
	Loaded     int
	Pages      int
	Applied    int
	Idempotent int
	Stale      int
}

func (p *IncidentProjector) Rebuild(ctx context.Context, input RebuildInput) (RebuildResult, error) {
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	if input.PageSize == 0 {
		input.PageSize = DefaultRebuildPageSize
	}
	if input.MaxPages == 0 {
		input.MaxPages = DefaultRebuildMaxPages
	}
	if input.PageSize < 1 || input.PageSize > port.GatewayAccountCircuitIncidentMaxPage || input.MaxPages < 1 || input.MaxPages > DefaultRebuildMaxPages {
		return RebuildResult{}, fmt.Errorf("gateway account circuit incident rebuild bounds are invalid")
	}
	var result RebuildResult
	var cursor *port.GatewayAccountCircuitIncidentCursor
	for result.Pages < input.MaxPages {
		page, err := p.reader.ListGatewayAccountCircuitIncidentsForRebuild(ctx, port.GatewayAccountCircuitIncidentRebuildInput{
			Now: input.Now, After: cursor, Limit: input.PageSize,
		})
		if err != nil {
			return result, err
		}
		result.Pages++
		for _, incident := range page.Items {
			projection, err := p.restorer.RestoreGatewayAccountCircuitIncident(ctx, incident)
			if err != nil {
				return result, err
			}
			result.Loaded++
			switch projection.Status {
			case port.GatewayAccountCircuitRevisionApplied:
				result.Applied++
			case port.GatewayAccountCircuitRevisionIdempotent:
				result.Idempotent++
			case port.GatewayAccountCircuitRevisionStale:
				result.Stale++
			default:
				return result, fmt.Errorf("gateway account circuit incident rebuild projection status is invalid")
			}
		}
		if page.NextCursor == nil {
			return result, nil
		}
		if cursor != nil && !page.NextCursor.UpdatedAt.After(cursor.UpdatedAt) && (page.NextCursor.UpdatedAt.Before(cursor.UpdatedAt) || page.NextCursor.CircuitScopeKey <= cursor.CircuitScopeKey) {
			return result, fmt.Errorf("gateway account circuit incident rebuild cursor did not advance")
		}
		cursor = page.NextCursor
	}
	return result, fmt.Errorf("gateway account circuit incident rebuild exceeded page bound")
}

var _ port.GatewayAccountCircuitIncidentProjector = (*IncidentProjector)(nil)
