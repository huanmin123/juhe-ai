// Runtime projection for the recipient-consent model. The runtime row is the
// only status the gateway reads, so every source mutation funnels through
// recomputeRuntime, which materializes the design §4.4 precedence exactly.
package authz

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type projectionDecision struct {
	Status          string
	Reason          string
	EffectiveType   string
	EffectiveTeamID string
	ExpiresAt       string
	LimitsJSON      string
	ParentGrantID   string
}

// recomputeRuntime locks the runtime and all of its sources, applies the
// manual/team fallback rules, then writes the projection. The caller must
// already hold the runtime row lock in the same transaction.
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
	var activeTeam *sourceRecord
	var activeTeamParent *offerRecord
	var activeManual *sourceRecord
	var activeManualParent *offerRecord
	var dormantManual *sourceRecord
	var dormantParent *offerRecord
	var needsReview *offerRecord
	var pending *offerRecord
	var pausedParent *offerRecord
	var latestTerminal *sourceRecord
	var latestTerminalParent *offerRecord

	parentAllowsPending := func(parent *offerRecord) bool {
		if parent == nil {
			return false
		}
		switch parent.Status {
		case StatusActive, StatusPending, StatusPendingOwnerApproval, StatusPendingMemberAcceptance:
			return true
		default:
			return false
		}
	}
	parentActive := func(parent *offerRecord) bool {
		return parent != nil && parent.Status == StatusActive &&
			(parent.ExpiresAt == "" || parent.ExpiresAt > now) &&
			(parent.PendingExpiresAt == "" || parent.PendingExpiresAt > now)
	}
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
		if source.SourceType == "team" && source.Status == StatusActive && parentActive(parent) {
			if activeTeam == nil || source.ActivatedAt < activeTeam.ActivatedAt ||
				(source.ActivatedAt == activeTeam.ActivatedAt && source.ID < activeTeam.ID) {
				activeTeam = source
				activeTeamParent = parent
			}
			continue
		}
		if source.SourceType == "manual" && source.Status == StatusActive && parentActive(parent) {
			activeManual = source
			activeManualParent = parent
			continue
		}
		if source.SourceType == "manual" && source.Status == StatusDormant && parentActive(parent) {
			if dormantManual == nil || source.ActivatedAt < dormantManual.ActivatedAt {
				dormantManual = source
				dormantParent = parent
			}
			continue
		}
		if source.Status == StatusNeedsReview && parentAllowsPending(parent) {
			needsReview = parent
			continue
		}
		if source.Status == StatusPending && parentAllowsPending(parent) {
			pending = parent
			continue
		}
		if parent.Status == StatusPaused {
			pausedParent = parent
		}
	}

	decision := projectionDecision{}
	switch {
	case activeTeam != nil:
		decision.Status = StatusActive
		decision.Reason = "active_team"
		decision.EffectiveType = "team"
		decision.EffectiveTeamID = activeTeam.SourceTeamID
		decision.ParentGrantID = activeTeam.GrantID
		decision.ExpiresAt = activeTeamParent.ExpiresAt
		decision.LimitsJSON = activeTeamParent.LimitsJSON
		// Team coverage makes accepted personal sources dormant, preserving
		// them for fallback.
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'dormant', updated_at = ? WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'`),
			now, runtimeID); err != nil {
			return nil, err
		}
	case activeManual != nil:
		decision.Status = StatusActive
		decision.Reason = "active_manual"
		decision.EffectiveType = "manual"
		decision.ParentGrantID = activeManual.GrantID
		decision.ExpiresAt = activeManualParent.ExpiresAt
		decision.LimitsJSON = activeManualParent.LimitsJSON
	case dormantManual != nil:
		// Promote the preserved manual source only after the team source is
		// gone/inactive/needs_review/paused.
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'active', activated_at = COALESCE(activated_at, ?), updated_at = ? WHERE id = ? AND status = 'dormant'`),
			now, now, dormantManual.ID); err != nil {
			return nil, err
		}
		decision.Status = StatusActive
		decision.Reason = "manual_fallback"
		decision.EffectiveType = "manual"
		decision.ParentGrantID = dormantManual.GrantID
		decision.ExpiresAt = dormantParent.ExpiresAt
		decision.LimitsJSON = dormantParent.LimitsJSON
	case needsReview != nil:
		decision.Status = StatusNeedsReview
		decision.Reason = "source_needs_review"
		decision.ParentGrantID = needsReview.ID
	case pending != nil:
		decision.Status = StatusPending
		decision.Reason = "source_pending"
		decision.ParentGrantID = pending.ID
	case pausedParent != nil:
		decision.Status = StatusPaused
		decision.Reason = "grant_paused"
		decision.ParentGrantID = pausedParent.ID
	case latestTerminal != nil:
		decision.Status = latestTerminal.Status
		decision.Reason = latestTerminal.EndedReason
		if decision.Reason == "" {
			decision.Reason = "terminal_source"
		}
		if latestTerminalParent != nil {
			decision.ParentGrantID = latestTerminalParent.ID
		}
	default:
		decision.Status = StatusRevoked
		decision.Reason = "no_sources"
	}

	if decision.ExpiresAt == "" && decision.ParentGrantID != "" {
		if parent := parents[decision.ParentGrantID]; parent != nil {
			decision.ExpiresAt = parent.ExpiresAt
			decision.LimitsJSON = parent.LimitsJSON
		}
	}
	statusChanged := runtime.Status != decision.Status
	nextStatusChangedAt := runtime.UpdatedAt
	if statusChanged {
		nextStatusChangedAt = now
	}
	// Non-terminal projections always mirror the effective source, including
	// clearing the expiry when the new parent grant never expires. Only a
	// terminal projection with no source expiry preserves the last value for
	// audit; every other empty decision must become NULL, otherwise a stale
	// past expiry permanently blocks a re-accepted grant.
	preserveExpiresAt := isTerminalStatus(decision.Status) && decision.ExpiresAt == ""
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = ?, status_reason = ?, status_changed_at = CASE WHEN ? THEN ? ELSE status_changed_at END,
			effective_source_type = NULLIF(?, ''), effective_source_team_id = NULLIF(?, ''),
			expires_at = CASE WHEN ? THEN expires_at ELSE NULLIF(?, '') END,
			limits_json = CASE WHEN ? = '' THEN NULL ELSE ? END,
			acceptance_mode = CASE WHEN acceptance_mode = 'legacy' THEN 'legacy' ELSE 'recipient' END,
			last_source_changed_at = ?, updated_at = ?
		WHERE id = ?`),
		decision.Status, decision.Reason, statusChanged, nextStatusChangedAt,
		decision.EffectiveType, decision.EffectiveTeamID,
		preserveExpiresAt, decision.ExpiresAt,
		decision.LimitsJSON, decision.LimitsJSON,
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
// history; re-acceptance requires an explicit rebind.
func (s *Store) disableRuntimeBindings(ctx context.Context, tx *sql.Tx, runtimeID, now string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_accounts")+`
		SET enabled = 0, updated_at = ? WHERE account_authorization_id = ? AND enabled = 1`), now, runtimeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_authorization_settings")+`
		SET enabled = 0, updated_at = ? WHERE authorization_id = ? AND enabled = 1`), now, runtimeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = 'disabled', schedulable = 0, updated_at = ?
		WHERE authorization_instance_authorization_id = ? AND deleted_at IS NULL AND status <> 'disabled'`), now, runtimeID); err != nil {
		return err
	}
	return nil
}

// syncRuntimeQuotaBindings rebuilds the hourly-window scope binding for one
// affected grant after a projection change. It reuses the legacy helper with
// a synthetic grant row because the SQL only needs the resource + grantee
// dimensions.
func (s *Store) syncRuntimeQuotaBindings(ctx context.Context, tx *sql.Tx, parent *offerRecord, now string) error {
	if parent == nil {
		return nil
	}
	grant := &grantRow{
		ID:            parent.ID,
		ResourceType:  parent.ResourceType,
		ResourceID:    parent.ResourceID,
		OwnerID:       parent.OwnerID,
		GranteeType:   parent.GranteeType,
		GranteeUserID: sql.NullString{String: parent.GranteeUserID, Valid: parent.GranteeUserID != ""},
		GranteeTeamID: sql.NullString{String: parent.GranteeTeamID, Valid: parent.GranteeTeamID != ""},
	}
	return s.syncGrantQuotaScopeBindings(ctx, tx, grant, now)
}

// latestTerminalByEndedAt is exposed for tests and migration diagnostics.
func latestTerminalByEndedAt(sources []sourceRecord) *sourceRecord {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].EndedAt != sources[j].EndedAt {
			return sources[i].EndedAt > sources[j].EndedAt
		}
		return sources[i].ID > sources[j].ID
	})
	for i := range sources {
		if isTerminalStatus(sources[i].Status) {
			return &sources[i]
		}
	}
	return nil
}

var _ = fmt.Sprintf
var _ = time.Now
