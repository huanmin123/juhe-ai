// Material-change propagation (design §4.5/§5.9): a source account write marks
// every non-terminal account-resource source needs_review. Group snapshots are
// account-granular, so the group source itself is not marked.
package authz

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

// revisionMarker is the accounts-package hook contract. accounts does not
// import authz (composition wires this method).
type RevisionMarker interface {
	MarkAccountSourcesNeedsReview(ctx context.Context, tx *sql.Tx, accountID, now string) error
}

// MarkAccountSourcesNeedsReview recomputes the account fingerprint and moves
// every changed non-terminal source to needs_review. It is idempotent.
func (s *Store) MarkAccountSourcesNeedsReview(ctx context.Context, tx *sql.Tx, accountID, now string) error {
	if strings.TrimSpace(accountID) == "" {
		return nil
	}
	secret, err := s.revisionSecretOrFail()
	if err != nil {
		return err
	}
	current, version, snapshot, err := authzrevision.Compute(ctx, tx, accountID, secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT s.id, COALESCE(s.observed_source_revision, ''),
			s.revision_version, s.status, s.authorization_id
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		WHERE g.resource_type = 'account' AND g.resource_id = ?
		  AND s.status IN ('pending','active','dormant','needs_review')
		ORDER BY s.id`), accountID)
	if err != nil {
		return err
	}
	type affected struct {
		id, observed, status, runtimeID string
		version                         int
	}
	var items []affected
	for rows.Next() {
		var item affected
		if err := rows.Scan(&item.id, &item.observed, &item.version, &item.status, &item.runtimeID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	detail, _ := json.Marshal(map[string]any{
		"accountId":       accountID,
		"vendorCode":      snapshot.VendorCode,
		"connectionHosts": materialConnectionHosts(snapshot),
		"modelCount":      len(snapshot.SupportedModels),
		"proxyBound":      snapshot.ProxyProfile != nil || snapshot.ProxyPool != nil,
	})
	runtimes := map[string]bool{}
	for _, item := range items {
		if item.observed == current && item.version == version {
			continue
		}
		if item.status == StatusNeedsReview {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
				SET observed_source_revision = ?, revision_version = ?, updated_at = ? WHERE id = ?`),
				current, version, now, item.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'needs_review', observed_source_revision = ?, revision_version = ?,
				needs_review_at = ?, needs_review_reason = 'account_material_changed',
				needs_review_detail_json = ?, updated_at = ?
			WHERE id = ? AND status IN ('pending','active','dormant')`),
			current, version, now, string(detail), now, item.id); err != nil {
			return err
		}
		runtimes[item.runtimeID] = true
	}
	for runtimeID := range runtimes {
		if _, err := s.recomputeRuntime(ctx, tx, runtimeID, "system:material-change", now); err != nil {
			return err
		}
	}
	// Notify affected receivers. Query through sources so a block with
	// return_existing=false still receives the security notice (§5.4).
	var runtimeIDs []string
	for id := range runtimes {
		runtimeIDs = append(runtimeIDs, id)
	}
	sort.Strings(runtimeIDs)
	for _, runtimeID := range runtimeIDs {
		var grantee string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT grantee_system_account_id FROM `+s.table("resource_authorizations")+`
			WHERE id = ?`), runtimeID).Scan(&grantee); err != nil {
			continue
		}
		if err := s.insertNotification(ctx, tx, grantee, "source_needs_review", "", "",
			map[string]any{"accountId": accountID}, "needs-review:"+runtimeID+":"+current, now); err != nil {
			return err
		}
	}
	return nil
}

func materialConnectionHosts(snapshot authzrevision.Snapshot) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, connection := range snapshot.Connections {
		host := connectionHost(connection.BaseURL)
		if host != "" && !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

// connectionHost strips scheme, path, query and userinfo from a base URL; the
// receiver-visible surface may disclose the endpoint host only.
func connectionHost(raw string) string {
	host := raw
	if strings.Contains(host, "://") {
		if idx := strings.Index(host, "://"); idx >= 0 {
			host = host[idx+3:]
		}
	}
	if idx := strings.Index(host, "@"); idx >= 0 {
		host = host[idx+1:]
	}
	if idx := strings.IndexAny(host, "/?#"); idx >= 0 {
		host = host[:idx]
	}
	return host
}

// ReconcileSourceRevisions is the sweep that catches changes made outside the
// account PATCH transaction (proxy profile/pool edits, batch imports, direct
// admin SQL). It processes a bounded batch per call.
func (s *Store) ReconcileSourceRevisions(ctx context.Context, now string, limit int) (int, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	s.reconcileMu.Lock()
	cursor := s.reconcileCursor
	s.reconcileMu.Unlock()
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT DISTINCT g.resource_id
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		WHERE g.resource_type = 'account' AND s.status IN ('pending','active','dormant','needs_review')
		  AND g.resource_id > ?
		ORDER BY g.resource_id LIMIT ?`), cursor, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range ids {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return changed, err
		}
		if err := s.MarkAccountSourcesNeedsReview(ctx, tx, id, now); err != nil {
			tx.Rollback()
			return changed, err
		}
		if err := tx.Commit(); err != nil {
			return changed, err
		}
		changed++
	}
	// A full page advances the keyset cursor; a short page wraps the sweep so
	// the next pass starts from the beginning.
	s.reconcileMu.Lock()
	if len(ids) == limit {
		s.reconcileCursor = ids[len(ids)-1]
	} else {
		s.reconcileCursor = ""
	}
	s.reconcileMu.Unlock()
	if changed > 0 {
		s.invalidateGovernance(ctx, invalidationReasonUpdated)
	}
	return changed, nil
}
