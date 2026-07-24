package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) LoadGatewayAccountCircuitIncidentForProjection(ctx context.Context, event port.GatewayAccountCircuitOutboxEvent) (port.GatewayAccountCircuitIncidentLoad, error) {
	if err := validateGatewayAccountCircuitIncidentEvent(event); err != nil {
		return port.GatewayAccountCircuitIncidentLoad{}, err
	}
	incident, currentRevision, err := scanGatewayAccountCircuitIncident(s.pool.QueryRow(ctx, loadGatewayAccountCircuitIncidentForProjectionSQL, event.CircuitScopeKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.GatewayAccountCircuitIncidentLoad{Status: port.GatewayAccountCircuitIncidentMissing}, nil
	}
	if err != nil {
		return port.GatewayAccountCircuitIncidentLoad{}, fmt.Errorf("load gateway account circuit incident projection: %w", err)
	}
	if incident.AccountID != event.AccountID || incident.AccountRuntimeKey != event.AccountRuntimeKey {
		return port.GatewayAccountCircuitIncidentLoad{}, fmt.Errorf("gateway account circuit incident projection identity mismatch")
	}
	if incident.Generation < event.Generation || incident.LedgerRevision < event.LedgerRevision {
		return port.GatewayAccountCircuitIncidentLoad{}, fmt.Errorf("gateway account circuit incident projection ledger regressed")
	}
	if incident.LedgerRevision == event.LedgerRevision && (incident.IncidentID != event.IncidentID || incident.Generation != event.Generation || incident.TransitionID != event.TransitionID) {
		return port.GatewayAccountCircuitIncidentLoad{}, fmt.Errorf("gateway account circuit incident event snapshot identity mismatch")
	}
	if currentRevision != event.DispatchRevision || incident.DispatchRevision != currentRevision {
		return port.GatewayAccountCircuitIncidentLoad{
			Status:                  port.GatewayAccountCircuitIncidentStale,
			CurrentDispatchRevision: currentRevision,
			Incident:                incident,
		}, nil
	}
	return port.GatewayAccountCircuitIncidentLoad{
		Status:                  port.GatewayAccountCircuitIncidentCurrent,
		CurrentDispatchRevision: currentRevision,
		Incident:                incident,
	}, nil
}

func (s *Store) ListGatewayAccountCircuitIncidentsForRebuild(ctx context.Context, input port.GatewayAccountCircuitIncidentRebuildInput) (port.GatewayAccountCircuitIncidentRebuildPage, error) {
	if input.Now.IsZero() || input.Limit < 1 || input.Limit > port.GatewayAccountCircuitIncidentMaxPage {
		return port.GatewayAccountCircuitIncidentRebuildPage{}, fmt.Errorf("gateway account circuit incident rebuild input is invalid")
	}
	afterMS := int64(-1)
	afterScope := ""
	if input.After != nil {
		if input.After.UpdatedAt.IsZero() || !validGatewayAccountCircuitOutboxText(input.After.CircuitScopeKey, 2048) {
			return port.GatewayAccountCircuitIncidentRebuildPage{}, fmt.Errorf("gateway account circuit incident rebuild cursor is invalid")
		}
		afterMS = input.After.UpdatedAt.UTC().UnixMilli()
		afterScope = input.After.CircuitScopeKey
	}
	rows, err := s.pool.Query(ctx, listGatewayAccountCircuitIncidentsForRebuildSQL, input.Now.UTC().UnixMilli(), afterMS, afterScope, input.Limit+1)
	if err != nil {
		return port.GatewayAccountCircuitIncidentRebuildPage{}, fmt.Errorf("list gateway account circuit incidents for rebuild: %w", err)
	}
	defer rows.Close()
	items := make([]port.GatewayAccountCircuitIncident, 0, input.Limit)
	for rows.Next() {
		incident, _, err := scanGatewayAccountCircuitIncident(rows)
		if err != nil {
			return port.GatewayAccountCircuitIncidentRebuildPage{}, fmt.Errorf("scan gateway account circuit incident rebuild: %w", err)
		}
		items = append(items, incident)
	}
	if err := rows.Err(); err != nil {
		return port.GatewayAccountCircuitIncidentRebuildPage{}, fmt.Errorf("read gateway account circuit incident rebuild: %w", err)
	}
	page := port.GatewayAccountCircuitIncidentRebuildPage{Items: items}
	if len(items) > input.Limit {
		page.Items = items[:input.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &port.GatewayAccountCircuitIncidentCursor{UpdatedAt: last.UpdatedAt, CircuitScopeKey: last.CircuitScopeKey}
	}
	return page, nil
}

type gatewayAccountCircuitIncidentScanner interface {
	Scan(...any) error
}

func scanGatewayAccountCircuitIncident(scanner gatewayAccountCircuitIncidentScanner) (port.GatewayAccountCircuitIncident, int64, error) {
	var incident port.GatewayAccountCircuitIncident
	var keyFingerprint, protocolCode, requestLane, modelFamily pgtype.Text
	var openUntilMS, nextTransitionMS, leaseUntilMS, retainedUntilMS pgtype.Int8
	var leaseID, leasePurpose pgtype.Text
	var updatedAtMS, currentRevision int64
	err := scanner.Scan(
		&incident.CircuitScopeKey, &incident.AccountID, &incident.AccountRuntimeKey, &incident.ScopeKind,
		&keyFingerprint, &protocolCode, &requestLane, &modelFamily, &incident.IncidentID, &incident.State,
		&incident.Generation, &incident.DispatchRevision, &incident.LedgerRevision, &incident.TransitionID,
		&openUntilMS, &nextTransitionMS, &leaseID, &leasePurpose, &leaseUntilMS, &incident.BackoffLevel,
		&incident.RecoveringSuccesses, &retainedUntilMS, &updatedAtMS, &currentRevision,
	)
	if err != nil {
		return port.GatewayAccountCircuitIncident{}, 0, err
	}
	incident.KeyFingerprint = nullableTextValue(keyFingerprint)
	incident.ProtocolCode = nullableTextValue(protocolCode)
	incident.RequestLane = nullableTextValue(requestLane)
	incident.ModelFamily = nullableTextValue(modelFamily)
	incident.LeaseID = nullableTextValue(leaseID)
	incident.LeasePurpose = nullableTextValue(leasePurpose)
	incident.OpenUntil = nullableMillisTime(openUntilMS)
	incident.NextTransitionAt = nullableMillisTime(nextTransitionMS)
	incident.LeaseUntil = nullableMillisTime(leaseUntilMS)
	incident.RetainedUntil = nullableMillisTime(retainedUntilMS)
	incident.UpdatedAt = time.UnixMilli(updatedAtMS).UTC()
	if err := validateGatewayAccountCircuitIncident(incident); err != nil {
		return port.GatewayAccountCircuitIncident{}, 0, err
	}
	return incident, currentRevision, nil
}

func nullableTextValue(value pgtype.Text) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func nullableMillisTime(value pgtype.Int8) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := time.UnixMilli(value.Int64).UTC()
	return &parsed
}

func validateGatewayAccountCircuitIncidentEvent(event port.GatewayAccountCircuitOutboxEvent) error {
	if event.EventType != port.GatewayAccountCircuitIncidentChanged || !validGatewayAccountCircuitOutboxText(event.CircuitScopeKey, 2048) || !validGatewayAccountCircuitOutboxText(event.AccountID, 256) || !validGatewayAccountCircuitOutboxText(event.AccountRuntimeKey, 1024) || !validGatewayAccountCircuitOutboxText(event.IncidentID, 256) || event.Generation < 0 || event.DispatchRevision < 1 || event.LedgerRevision < 1 {
		return fmt.Errorf("gateway account circuit incident event is invalid")
	}
	return nil
}

func validateGatewayAccountCircuitIncident(value port.GatewayAccountCircuitIncident) error {
	if !validGatewayAccountCircuitOutboxText(value.CircuitScopeKey, 2048) || !validGatewayAccountCircuitOutboxText(value.AccountID, 256) || !validGatewayAccountCircuitOutboxText(value.AccountRuntimeKey, 1024) || !validGatewayAccountCircuitOutboxText(value.IncidentID, 256) || !validGatewayAccountCircuitOutboxText(value.TransitionID, 256) || value.Generation < 0 || value.DispatchRevision < 1 || value.LedgerRevision < 1 || value.UpdatedAt.IsZero() {
		return fmt.Errorf("gateway account circuit incident is invalid")
	}
	switch value.State {
	case "CLOSED", "SUSPECT", "OPEN", "HALF_OPEN", "RECOVERING", "PERSISTING", "SHADOWED_BY_PERSISTENT":
	default:
		return fmt.Errorf("gateway account circuit incident state is invalid")
	}
	if (value.State == "CLOSED") != (value.RetainedUntil != nil) {
		return fmt.Errorf("gateway account circuit incident retention is invalid")
	}
	switch value.ScopeKind {
	case "account":
		if value.KeyFingerprint != "" || value.ProtocolCode != "" || value.RequestLane != "" || value.ModelFamily != "" {
			return fmt.Errorf("gateway account circuit account scope is invalid")
		}
	case "key":
		if strings.TrimSpace(value.KeyFingerprint) == "" || value.ProtocolCode != "" || value.RequestLane != "" || value.ModelFamily != "" {
			return fmt.Errorf("gateway account circuit key scope is invalid")
		}
	case "protocol_model":
		if strings.TrimSpace(value.ProtocolCode) == "" || strings.TrimSpace(value.RequestLane) == "" || strings.TrimSpace(value.ModelFamily) == "" || value.KeyFingerprint != "" {
			return fmt.Errorf("gateway account circuit protocol scope is invalid")
		}
	default:
		return fmt.Errorf("gateway account circuit scope kind is invalid")
	}
	return nil
}

var _ port.GatewayAccountCircuitIncidentReader = (*Store)(nil)
