// Runtime projection for the send-and-claim model. The runtime row is the
// only status the gateway reads, so every source mutation funnels through
// recomputeRuntime. The resolution is deliberately simple: the earliest
// activated source whose parent grant is live makes the runtime active;
// otherwise a claimable source keeps it pending, a paused grant keeps it
// paused, and otherwise the latest terminal source decides.
package authz

import (
	"context"
	"database/sql"
)

type projectionDecision struct {
	Status          string
	EffectiveType   string
	EffectiveTeamID string
	ExpiresAt       string
	ParentGrantID   string
}

// recomputeRuntime locks the runtime and all of its sources, resolves the
// projection, then writes it. The caller must already hold the runtime row
// lock in the same transaction.
func (s *Store) recomputeRuntime(ctx context.Context, tx *sql.Tx, runtimeID, actor, now string) (*runtimeRecord, error) {
	runtime, err := s.getRuntimeForUpdate(ctx, tx, runtimeID)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, nil
	}
	sources, err := s.listSourcesForRuntime(ctx, tx, runtimeID)
	if err != nil {
		return nil, err
	}
	parents := map[string]*offerRecord{}
	for _, source := range sources {
		if source.GrantID == "" {
			// fail closed: an unmigrated source must never make the runtime
			// schedulable.
			continue
		}
		if _, ok := parents[source.GrantID]; ok {
			continue
		}
		parent, err := s.getOfferForUpdate(ctx, tx, source.GrantID)
		if err != nil {
			return nil, err
		}
		parents[source.GrantID] = parent
	}

	parentLive := func(parent *offerRecord) bool {
		return parent != nil && parent.Status == StatusActive &&
			(parent.ExpiresAt == "" || parent.ExpiresAt > now)
	}
	var activeSource *sourceRecord
	var activeParent *offerRecord
	var pendingParent *offerRecord
	var pausedParent *offerRecord
	var latestTerminal *sourceRecord
	var latestTerminalParent *offerRecord
	for i := range sources {
		source := &sources[i]
		parent := parents[source.GrantID]
		if parent == nil {
			continue
		}
		if isTerminalStatus(source.Status) {
			if latestTerminal == nil || source.EndedAt > latestTerminal.EndedAt ||
				(source.EndedAt == latestTerminal.EndedAt && source.ID > latestTerminal.ID) {
				latestTerminal = source
				latestTerminalParent = parent
			}
			continue
		}
		if source.Status == StatusActive && parentLive(parent) {
			if activeSource == nil || source.ActivatedAt < activeSource.ActivatedAt ||
				(source.ActivatedAt == activeSource.ActivatedAt && source.ID < activeSource.ID) {
				activeSource = source
				activeParent = parent
			}
			continue
		}
		if source.Status == StatusPending &&
			(parent.Status == StatusPending || parentLive(parent)) {
			if pendingParent == nil {
				pendingParent = parent
			}
			continue
		}
		if parent.Status == StatusPaused {
			pausedParent = parent
		}
	}

	decision := projectionDecision{}
	switch {
	case activeSource != nil:
		decision.Status = StatusActive
		decision.EffectiveType = activeSource.SourceType
		decision.EffectiveTeamID = activeSource.SourceTeamID
		decision.ParentGrantID = activeSource.GrantID
		decision.ExpiresAt = activeParent.ExpiresAt
	case pendingParent != nil:
		decision.Status = StatusPending
		decision.ParentGrantID = pendingParent.ID
	case pausedParent != nil:
		decision.Status = StatusPaused
		decision.ParentGrantID = pausedParent.ID
	case latestTerminal != nil:
		decision.Status = latestTerminal.Status
		if latestTerminalParent != nil {
			decision.ParentGrantID = latestTerminalParent.ID
		}
	default:
		decision.Status = StatusRevoked
	}

	if decision.ExpiresAt == "" && decision.ParentGrantID != "" {
		if parent := parents[decision.ParentGrantID]; parent != nil {
			decision.ExpiresAt = parent.ExpiresAt
		}
	}
	// Non-terminal projections always mirror the effective source, including
	// clearing the expiry when the new parent grant never expires. Only a
	// terminal projection with no source expiry preserves the last value for
	// audit; every other empty decision must become NULL, otherwise a stale
	// past expiry permanently blocks a re-claim.
	preserveExpiresAt := isTerminalStatus(decision.Status) && decision.ExpiresAt == ""
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = ?,
			effective_source_type = NULLIF(?, ''), effective_source_team_id = NULLIF(?, ''),
			expires_at = CASE WHEN ? THEN expires_at ELSE NULLIF(?, '') END,
			last_source_changed_at = ?, updated_at = ?
		WHERE id = ?`),
		decision.Status,
		decision.EffectiveType, decision.EffectiveTeamID,
		preserveExpiresAt, decision.ExpiresAt,
		now, now, runtimeID); err != nil {
		return nil, err
	}
	if isTerminalStatus(decision.Status) {
		if err := s.disableRuntimeBindings(ctx, tx, runtimeID, now); err != nil {
			return nil, err
		}
	}
	return s.getRuntimeForUpdate(ctx, tx, runtimeID)
}

// disableRuntimeBindings is the durable-exit side effect: only a terminal
// runtime stops bindings and instance scheduling. The rows are retained for
// history; a re-claim re-enables them. The group authorization settings row
// stays in place: visibility is driven by the runtime status alone.
func (s *Store) disableRuntimeBindings(ctx context.Context, tx *sql.Tx, runtimeID, now string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_accounts")+`
		SET enabled = 0, updated_at = ? WHERE account_authorization_id = ? AND enabled = 1`), now, runtimeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = 'disabled', schedulable = 0, updated_at = ?
		WHERE authorization_instance_authorization_id = ? AND deleted_at IS NULL AND status <> 'disabled'`), now, runtimeID); err != nil {
		return err
	}
	return nil
}
