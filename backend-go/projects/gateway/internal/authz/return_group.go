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
	runtimeQuery += " FOR UPDATE"
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
	// Recipient-consent narrowing: the compatibility group endpoint may return
	// only when the runtime has exactly one non-terminal source. Multiple
	// sources must be handled per-source in the authorization center.
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ? AND status IN ('pending','active','dormant','needs_review')
		ORDER BY id LIMIT 2`), receipt.ID)
	if err != nil {
		return nil, err
	}
	var sourceIDs []string
	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			rows.Close()
			return nil, err
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	if len(sourceIDs) > 1 {
		return nil, failCode("return_requires_source_center", "授权分组存在多条来源，请前往授权中心按来源归还")
	}
	// The source-level state machine owns its transaction; release the read
	// transaction before delegating. The deferred rollback becomes a no-op.
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	if err := s.ReturnSourceForGrantee(ctx, sourceIDs[0], actor); err != nil {
		return nil, err
	}
	s.invalidateAfterBusinessWrite(ctx, invalidationReasonReturned)
	return &receipt, nil
}
