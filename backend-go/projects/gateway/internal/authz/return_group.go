package authz

import (
	"context"
	"database/sql"
	"errors"
)

// ReturnedGroupAuthorization mirrors ReturnedGroupAuthorizationReceipt
// (resource-authorization-return.repository.ts): the runtime authorization
// identity the groups return-authorization route renders its audit log from.
type ReturnedGroupAuthorization struct {
	ID                           string
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	ResourceName                 string
}

// ReturnGroupForGrantee mirrors returnGroupAuthorizationForGranteeAsync
// (resource-authorization-return.repository.ts:344-400): resolve the group's
// runtime authorization row for the grantee, reject owner-self returns,
// require an active manual runtime source, then return the single returnable
// direct grant (same resource identity + grantee, status outside
// revoked/returned). A nil result with a nil error mirrors the Node undefined
// receipt the groups route renders as 404 授权分组不存在或不可归还.
//
// granteeUserID is userVisibleSystemAccountId(access) (the admin scope filter
// when present, otherwise the caller); actor is currentSystemAccountId(access)
// and stamps revoked_by. Node stamps nowIso() on this path — there is no
// optimistic-lock version check to derive the next version from.
func (s *Store) ReturnGroupForGrantee(ctx context.Context, groupID, granteeUserID, actor string) (*ReturnedGroupAuthorization, error) {
	ctx = ensureCtx(ctx)
	if granteeUserID == "" {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The runtime row joined with the group owner binding carries the receipt
	// resource_name (Node SELECT ... groups.name AS resource_name).
	runtimeQuery := s.bind(`SELECT ra.id, ra.resource_type, ra.resource_id, ra.resource_owner_system_account_id, ra.grantee_system_account_id, g.name
		FROM ` + s.table("resource_authorizations") + ` ra
		INNER JOIN ` + s.table("groups") + ` g
			ON g.id = ra.resource_id
			AND g.system_account_id = ra.resource_owner_system_account_id
		WHERE ra.resource_type = 'group'
			AND ra.resource_id = ?
			AND ra.grantee_system_account_id = ?
		LIMIT 1`)
	if s.pg {
		runtimeQuery += " FOR UPDATE"
	}
	var receipt ReturnedGroupAuthorization
	err = tx.QueryRowContext(ctx, runtimeQuery, groupID, granteeUserID).Scan(
		&receipt.ID, &receipt.ResourceType, &receipt.ResourceID,
		&receipt.ResourceOwnerSystemAccountID, &receipt.GranteeSystemAccountID, &receipt.ResourceName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Node :381 — the owner can never "return" their own group.
	if receipt.ResourceOwnerSystemAccountID == granteeUserID {
		return nil, nil
	}
	hasManual, err := s.hasActiveManualSource(ctx, tx, receipt.ID)
	if err != nil {
		return nil, err
	}
	if !hasManual {
		return nil, nil
	}
	// findReturnableDirectGrantForRuntimeAuthorization: the direct grant with
	// the same resource identity and grantee still outside the terminal
	// revoked/returned states.
	grantQuery := s.bind(`SELECT ` + grantColumns + ` FROM ` + s.table("resource_authorization_grants") + ` g
		WHERE g.resource_type = ?
			AND g.resource_id = ?
			AND g.resource_owner_system_account_id = ?
			AND g.grantee_type = 'system_account'
			AND g.grantee_system_account_id = ?
			AND g.status NOT IN ('revoked', 'returned')
		LIMIT 1`)
	if s.pg {
		grantQuery += " FOR UPDATE"
	}
	grant, err := s.scanGrant(tx.QueryRowContext(ctx, grantQuery,
		receipt.ResourceType, receipt.ResourceID, receipt.ResourceOwnerSystemAccountID, granteeUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// returnResourceAuthorizationGrantAsync: the grant lands in returned with
	// the caller stamp, the manual sources revoke with the grantee_returned
	// reason, and the runtime effective source refreshes toward the returned
	// terminal status (no source left → terminal returned, no expired row
	// preservation).
	now := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'returned', revoked_by = ?, revoked_at = ?, updated_at = ?
		WHERE id = ?`), actor, now, now, grant.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked',
			ended_at = COALESCE(ended_at, ?),
			ended_reason = COALESCE(ended_reason, 'grantee_returned'),
			revoked_by = ?,
			revoked_at = ?,
			updated_at = ?
		WHERE authorization_id = ?
			AND source_type = 'manual'
			AND status IN ('active', 'superseded')`), now, actor, now, now, receipt.ID); err != nil {
		return nil, err
	}
	noPreserve := false
	if err := s.refreshEffectiveSource(ctx, tx, receipt.ID, actor, now, refreshOptions{
		noActiveSourceReason:              "grantee_returned",
		preserveExpiredWhenNoActiveSource: &noPreserve,
		terminalStatus:                    StatusReturned,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &receipt, nil
}
