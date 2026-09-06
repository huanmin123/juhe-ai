package accounts

// M11 return authorization: POST /{id}/return-authorization (Node
// account-authorization-return.routes.ts +
// resource-authorization-return.repository.ts
// returnAccountAuthorizationInstanceForGranteeAsync).
//
// Cross-domain rule (任务约束): the terminal grant write is the authz slice's
// Return state machine (internal/authz Store.Return — the port of
// returnResourceAuthorizationGrant + the source revoke + the effective source
// refresh), never reimplemented here. This package only localizes the
// returnable direct grant from the stamped instance account (the queries the
// accounts slice owns: the instance stamp columns) and hands
// (grantID, grant.updatedAt, grantee) to the injected
// AuthorizationGrantReturner. authz.Store satisfies the interface directly.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// AuthorizationGrantReturner is the narrow port into the authz terminal
// return state machine (internal/authz Store.Return). Status mirrors
// TerminalMutation.Status: updated/unchanged/not_found/conflict.
type AuthorizationGrantReturner interface {
	Return(ctx context.Context, grantID, expectedUpdatedAt, granteeUserID string) (string, error)
}

// SetAuthorizationGrantReturner wires the port (composition-root handover;
// the production bridge passes the authz store adapter).
func (s *Store) SetAuthorizationGrantReturner(returner AuthorizationGrantReturner) {
	s.returner = returner
}

// errReturnAuthorizationMissing marks 授权账户不存在或不可归还 (404).
var errReturnAuthorizationMissing = errors.New("授权账户不存在或不可归还")

// returnableGrant is the direct grant row the return path localizes
// (findReturnableDirectGrantForRuntimeAuthorization).
type returnableGrant struct {
	id         string
	updatedAt  string
	resourceID string
	ownerID    string
}

// ReturnAuthorizationInstance mirrors
// returnAccountAuthorizationInstanceForGranteeAsync: localize the stamped
// instance row for the grantee, verify the runtime authorization, find the
// returnable direct grant and hand the terminal write to the authz Returner.
// The route renders errReturnAuthorizationMissing as 404 and any other error
// as 400.
func (s *Store) ReturnAuthorizationInstance(ctx context.Context, accountID string, access AccessScope) error {
	ctx = ensureCtx(ctx)
	grantee := access.viewerID()
	if grantee == "" || s.returner == nil {
		// No grantee context or an unwired authz port: the grant cannot be
		// returned (Node renders the same 404 for every localization miss).
		return errReturnAuthorizationMissing
	}
	id := strings.TrimSpace(accountID)
	if id == "" {
		return errReturnAuthorizationMissing
	}
	var authorizationID sql.NullString
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id, grantee}
	if scoped := access.manageableID(); scoped != "" && !authorized {
		scopeClause = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT authorization_instance_authorization_id
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND system_account_id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NOT NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(&authorizationID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !authorizationID.Valid) {
		return errReturnAuthorizationMissing
	}
	if err != nil {
		return err
	}
	// The runtime authorization row: grantee-scoped, owner must differ. Its
	// resource identity (type/resource_id/owner) keys the direct grant.
	var runtime struct {
		id          string
		ownerID     string
		resourceType string
		resourceID  string
	}
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT id, resource_owner_system_account_id, resource_type, resource_id
		FROM `+s.table("resource_authorizations")+`
		WHERE id = ?
			AND grantee_system_account_id = ?
		LIMIT 1`), authorizationID.String, grantee).Scan(&runtime.id, &runtime.ownerID, &runtime.resourceType, &runtime.resourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return errReturnAuthorizationMissing
	}
	if err != nil {
		return err
	}
	if runtime.ownerID == grantee {
		return errReturnAuthorizationMissing
	}
	grant, err := s.findReturnableDirectGrant(ctx, runtime.resourceType, runtime.resourceID, runtime.ownerID, grantee)
	if err != nil {
		return err
	}
	if grant == nil {
		return errReturnAuthorizationMissing
	}
	status, err := s.returner.Return(ctx, grant.id, grant.updatedAt, grantee)
	if err != nil {
		return err
	}
	switch status {
	case "updated":
		return nil
	case "not_found", "conflict":
		// Node localizes and writes inside one transaction: a grant that moved
		// between the localization and the terminal write renders 不可归还.
		return errReturnAuthorizationMissing
	default:
		// unchanged cannot happen for a returnable (not returned/revoked)
		// grant; treat it as the terminal success Node's in-transaction
		// returnResourceAuthorizationGrant would produce.
		return nil
	}
}

// findReturnableDirectGrant mirrors
// findReturnableDirectGrantForRuntimeAuthorization: the direct grant row for
// the runtime authorization identity, not revoked/returned.
func (s *Store) findReturnableDirectGrant(ctx context.Context, resourceType, resourceID, ownerID, grantee string) (*returnableGrant, error) {
	var grant returnableGrant
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, updated_at, resource_id, resource_owner_system_account_id
		FROM `+s.table("resource_authorization_grants")+`
		WHERE resource_type = ?
			AND resource_id = ?
			AND resource_owner_system_account_id = ?
			AND grantee_type = 'system_account'
			AND grantee_system_account_id = ?
			AND status NOT IN ('revoked', 'returned')
		LIMIT 1`), resourceType, resourceID, ownerID, grantee).Scan(
		&grant.id, &grant.updatedAt, &grant.resourceID, &grant.ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}
