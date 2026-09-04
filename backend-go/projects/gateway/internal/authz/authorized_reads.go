// Authorized-instance read projection (M10): the authorized-view branch of
// the Node /my-accounts list and detail (account-management-list.repository.ts
// access_type='authorized' rows plus the authorization-quota join shape).
// Runtime authorization rows (resource_authorizations) are per grantee user;
// team provenance lives on resource_authorization_sources, so a viewer reads
// an authorization instance when the runtime row is active and unexpired and
// the viewer is the direct grantee or an active member of the effective team
// source.
package authz

import (
	"context"
	"strings"
	"time"
)

// AuthorizedReadableAccountIDs resolves the authorization instance account
// ids the viewer may read through the my-accounts surface. The query walks
// resource_authorizations (resource_type 'account', status 'active', expires
// not passed, grantee = viewer directly or viewer active in the grantee's
// team source) and joins the authorization instance accounts cloned from the
// source account (accounts.authorization_instance_source_account_id =
// resource_id). Instance rows correlate by authorization id when stamped,
// falling back to the runtime grantee namespace for legacy rows that only
// carry the source account id. The returned map is keyed by the instance
// account id (value true); an absent key means the instance stays invisible
// to this viewer.
func (s *Store) AuthorizedReadableAccountIDs(ctx context.Context, viewerSystemAccountID string) (map[string]bool, error) {
	ctx = ensureCtx(ctx)
	readable := map[string]bool{}
	viewer := strings.TrimSpace(viewerSystemAccountID)
	if viewer == "" {
		return readable, nil
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	query := `SELECT DISTINCT inst.id
		FROM ` + s.table("resource_authorizations") + ` r
		INNER JOIN ` + s.table("accounts") + ` inst
			ON inst.authorization_instance_source_account_id = r.resource_id
			AND inst.deleted_at IS NULL
			AND (
				inst.authorization_instance_authorization_id = r.id
				OR (inst.authorization_instance_authorization_id IS NULL
					AND inst.system_account_id = r.grantee_system_account_id)
			)
		WHERE r.resource_type = 'account'
			AND r.status = 'active'
			AND (r.expires_at IS NULL OR r.expires_at > ?)
			AND (
				r.grantee_system_account_id = ?
				OR EXISTS (
					SELECT 1
					FROM ` + s.table("resource_authorization_sources") + ` s
					INNER JOIN ` + s.table("system_team_members") + ` m
						ON m.team_id = s.source_team_id
						AND m.system_account_id = ?
						AND m.status = 'active'
					WHERE s.authorization_id = r.id
						AND s.source_type = 'team'
						AND s.status = 'active'
				)
			)`
	rows, err := s.db.QueryContext(ctx, s.bind(query), now, viewer, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var instanceID string
		if err := rows.Scan(&instanceID); err != nil {
			return nil, err
		}
		readable[instanceID] = true
	}
	return readable, rows.Err()
}
