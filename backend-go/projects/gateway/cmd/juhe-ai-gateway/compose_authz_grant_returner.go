package main

// compose_authz_grant_returner.go: the M11 account-instance return route's
// terminal authorization bridge (BUG-0175 follow-up wiring handed from W2-B
// to the W2-C compose scope).
//
// accounts.AuthorizationGrantReturner was implemented and exercised only by
// m11_test; the production composition never called
// Store.SetAuthorizationGrantReturner, so POST /{id}/return-authorization
// degraded to the unwired-port 404 for every grantee. This adapter connects
// the accounts return port onto authz.Store.Return — the terminal state
// machine (returnResourceAuthorizationGrant + the source revoke + the
// effective source refresh) — so the route inherits the authz committed-write
// invalidation fan-out (AttachWriteInvalidator: the group-stats dirty marker
// plus the K5 gateway runtime / api-key validation / authorization-quota
// topics) with no second write implementation.

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// authzGrantReturner adapts authz.Store onto accounts.AuthorizationGrantReturner.
type authzGrantReturner struct {
	store *authz.Store
}

// Return mirrors the narrow port contract: TerminalMutation.Status carries
// updated / unchanged / not_found / conflict verbatim (the accounts route
// renders not_found / conflict as the 404 不可归还 contract and treats the
// remaining outcomes as terminal success).
func (r authzGrantReturner) Return(ctx context.Context, grantID, expectedUpdatedAt, granteeUserID string) (string, error) {
	mutation, err := r.store.Return(ctx, grantID, expectedUpdatedAt, granteeUserID)
	if err != nil {
		return "", err
	}
	if mutation == nil {
		return "", nil
	}
	return mutation.Status, nil
}
