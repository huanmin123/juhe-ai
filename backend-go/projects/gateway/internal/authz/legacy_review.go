// Legacy authorization review deadline (design §7.6 #10): migrated sources
// keep working for a bounded window, then unconfirmed legacy consents become
// needs_review and the gateway hard-blocks them until the receiver reconfirms.
package authz

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

// ExpireLegacyReviews moves due legacy active/dormant sources to needs_review.
// It only selects grants that still have at least one live legacy source, so
// repeated sweeps are idempotent and do not notify twice.
func (s *Store) ExpireLegacyReviews(ctx context.Context, now string, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT g.id
		FROM `+s.table("resource_authorization_grants")+` g
		WHERE g.acceptance_mode = 'legacy'
		  AND g.status IN ('active','paused')
		  AND COALESCE(g.legacy_review_deadline, '') <> ''
		  AND g.legacy_review_deadline <= ?
		  AND EXISTS (
			SELECT 1 FROM `+s.table("resource_authorization_sources")+` s
			WHERE s.grant_id = g.id AND s.acceptance_mode = 'legacy'
			  AND s.status IN ('active','dormant')
		  )
		ORDER BY g.legacy_review_deadline ASC, g.id ASC LIMIT ?`), now, limit)
	if err != nil {
		return 0, err
	}
	var grantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		grantIDs = append(grantIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(grantIDs) == 0 {
		return 0, nil
	}
	secret, err := s.revisionSecretOrFail()
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, grantID := range grantIDs {
		type source struct {
			id, runtimeID, resourceType, resourceID, ownerID string
		}
		sourceRows, err := tx.QueryContext(ctx, s.bind(`SELECT s.id, s.authorization_id,
				ra.resource_type, ra.resource_id, ra.resource_owner_system_account_id
			FROM `+s.table("resource_authorization_sources")+` s
			JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
			WHERE s.grant_id = ? AND s.acceptance_mode = 'legacy'
			  AND s.status IN ('active','dormant')
			ORDER BY s.id`), grantID)
		if err != nil {
			return processed, err
		}
		var items []source
		for sourceRows.Next() {
			var item source
			if err := sourceRows.Scan(&item.id, &item.runtimeID, &item.resourceType,
				&item.resourceID, &item.ownerID); err != nil {
				sourceRows.Close()
				return processed, err
			}
			items = append(items, item)
		}
		sourceRows.Close()
		if err := sourceRows.Err(); err != nil {
			return processed, err
		}
		runtimes := map[string]string{}
		for _, item := range items {
			revision := ""
			version := 0
			if item.resourceType == "account" {
				computed, computedVersion, _, err := authzrevision.Compute(ctx, tx, item.resourceID, secret)
				if err == nil {
					revision, version = computed, computedVersion
				} else if !errors.Is(err, sql.ErrNoRows) {
					return processed, err
				}
			} else {
				revision = authzrevision.Group(secret, item.resourceID, item.ownerID)
				version = authzrevision.Version
			}
			result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
				SET status = 'needs_review',
					observed_source_revision = CASE WHEN ? <> '' THEN ? ELSE observed_source_revision END,
					revision_version = CASE WHEN ? > 0 THEN ? ELSE revision_version END,
					needs_review_at = ?, needs_review_reason = 'legacy_review_due',
					needs_review_detail_json = NULL, updated_at = ?
				WHERE id = ? AND status IN ('active','dormant')`),
				revision, revision, version, version, now, now, item.id)
			if err != nil {
				return processed, err
			}
			if affected, _ := result.RowsAffected(); affected == 0 {
				continue
			}
			processed++
			runtimes[item.runtimeID] = item.ownerID
		}
		for runtimeID, ownerID := range runtimes {
			if _, err := s.recomputeRuntime(ctx, tx, runtimeID, "system:legacy-review", now); err != nil {
				return processed, err
			}
			var grantee string
			if err := tx.QueryRowContext(ctx, s.bind(`SELECT grantee_system_account_id
				FROM `+s.table("resource_authorizations")+` WHERE id = ?`), runtimeID).Scan(&grantee); err != nil {
				return processed, err
			}
			payload := map[string]any{"reason": "legacy_review_due"}
			for _, target := range []string{grantee, ownerID} {
				if target == "" {
					continue
				}
				if err := s.insertNotification(ctx, tx, target, "source_needs_review", "", "",
					payload, "legacy-review-due:"+runtimeID+":"+target, now); err != nil {
					return processed, err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return processed, err
	}
	if processed > 0 {
		s.invalidateGovernance(ctx, invalidationReasonUpdated)
	}
	return processed, nil
}
