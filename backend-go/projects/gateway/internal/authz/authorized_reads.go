// Authorized-instance read projection (M10): the authorized-view branch of
// the Node /my-accounts list and detail (account-management-list.repository.ts
// access_type='authorized' rows plus the authorization-quota join shape).
// Runtime authorization rows (resource_authorizations) are per grantee user:
// a team grant fans out one row per active member whose grantee is the member
// himself (resource-authorization-write.repository.ts:202-218), and the
// provisioned instance account is stamped with that row's id inside the
// grantee's namespace (:1880-1915, system_account_id =
// grantee_system_account_id at :1893). A viewer therefore reads an instance
// only through his own runtime row, filtered to status IN ('active', 'paused',
// 'expired') exactly like the Node management-list guard
// (account-management-list.repository.ts:331-334); there is no cross-member
// team branch on the read path.
package authz

import (
	"context"
	"strings"
)

// AuthorizedReadableAccountIDs resolves the authorization instance account
// ids the viewer may read through the my-accounts surface. The query walks
// the viewer's own runtime authorization rows (resource_type 'account',
// status IN ('active','paused','expired') — Node
// account-management-list.repository.ts:331-334; expiry is materialized as a
// status flip by the sweep, expireDueResourceAuthorizationsAsync :930-969, so
// no wall-clock comparison lives here) and joins the authorization instance
// accounts cloned from the source account
// (accounts.authorization_instance_source_account_id = resource_id). Instance
// rows correlate by the stamped authorization id; the un-stamped fallback
// stays scoped to the viewer's own namespace (system_account_id = grantee) so
// it cannot leak another member's instance. The returned map is keyed by the
// instance account id (value true); an absent key means the instance stays
// invisible to this viewer.
func (s *Store) AuthorizedReadableAccountIDs(ctx context.Context, viewerSystemAccountID string) (map[string]bool, error) {
	ctx = ensureCtx(ctx)
	readable := map[string]bool{}
	viewer := strings.TrimSpace(viewerSystemAccountID)
	if viewer == "" {
		return readable, nil
	}
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
			AND r.status IN ('active', 'paused', 'expired')
			AND r.grantee_system_account_id = ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), viewer)
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
