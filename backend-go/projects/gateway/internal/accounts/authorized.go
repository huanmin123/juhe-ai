// Authorized-instance read hookup (M10): the my-accounts list/detail pass
// accounts through the owner scope filter. Authorization instance accounts
// provisioned from a resource authorization (accounts table columns
// authorization_instance_authorization_id / authorization_instance_source_account_id)
// belong to the authorized-view branch of the Node contract: the grantee (or
// an active member of the grantee team) reads the instance account even when
// the owner scope filter would exclude it. The authz slice owns the
// authorization state machine, so this package only depends on the narrow
// reader interface below, injected by the composition root (main) or the
// test wiring.
package accounts

import (
	"context"
	"sort"
)

// AuthorizedAccountReader is the narrow cross-package port of the authz
// slice's authorized-instance projection
// (authz.Store.AuthorizedReadableAccountIDs). It maps the readable
// authorization instance account ids for one viewer.
type AuthorizedAccountReader interface {
	AuthorizedReadableAccountIDs(ctx context.Context, viewerSystemAccountID string) (map[string]bool, error)
}

// SetAuthorizedReader wires the reader. A nil reader keeps the pure owner
// view (the legacy behavior before the M10 hookup).
func (s *Store) SetAuthorizedReader(reader AuthorizedAccountReader) {
	s.authorized = reader
}

// authorizedReadableIDs resolves the authorized instance account id set for
// the scope viewer. Admins see every row, so the projection is skipped; a
// failing reader degrades to the owner view (logged, never fatal for reads).
func (s *Store) authorizedReadableIDs(ctx context.Context, access AccessScope) map[string]bool {
	if access.canAccessAll() || s.authorized == nil {
		return nil
	}
	viewer := access.manageableID()
	if viewer == "" {
		viewer = access.ViewerID
	}
	if viewer == "" {
		return nil
	}
	ids, err := s.authorized.AuthorizedReadableAccountIDs(ctx, viewer)
	if err != nil {
		println("accounts slice authorized read error: " + err.Error())
		return nil
	}
	return ids
}

// authorizedIDList returns the map keys in a stable order for SQL IN clauses.
func authorizedIDList(ids map[string]bool) []string {
	if len(ids) == 0 {
		return nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		if id != "" {
			list = append(list, id)
		}
	}
	sort.Strings(list)
	return list
}

// authorizedPermissions mirrors accountManagementPermissions for
// access_type='authorized': use/lock stay, editing and credentials do not.
// canReturnAuthorization is true only when the runtime authorization's
// effective source type is 'manual'
// (Node account-management-list.repository.ts:747-770 — the type comes from
// resource_authorizations.effective_source_type via the list join :296, so a
// team-carried grant or a missing value renders false; grantees of team
// sources return via /my-authorizations).
func authorizedPermissions(effectiveSourceType *string) Permissions {
	return Permissions{
		CanUse:                 true,
		CanEdit:                false,
		CanDelete:              false,
		CanReturnAuthorization: effectiveSourceType != nil && *effectiveSourceType == "manual",
		CanAuthorize:           false,
		CanViewCredentials:     false,
		CanLock:                true,
	}
}
