// Post-commit gateway runtime cache invalidation for the committed
// expiry/revoke cleanup pass (Store.ExpireDue, 网关模型列表账户并集设计 6.3
// "写路径 → 失效覆盖依据"). A committed ExpireDue pass that actually changed
// rows materialized authorization terminal states (resource_authorizations /
// resource_authorization_grants → expired) and removed their dependent
// group_accounts and group_authorization_settings rows — all part of the
// gateway model-list union cache dependency face, so the committed write must
// clear the gateway runtime cache like every other covered write path. The
// pass publishes on the runtime topic plus the authorization quota topic,
// mirroring the two-topic arm the authz slice (invalidateAfterBusinessWrite)
// and systemteams (afterCommit) run for committed authorization writes; the
// stats / API-key-validation arms stay package-local concerns of those slices
// (this Store has no stats or validation-cache port).
//
// The port is nil-tolerant (the groups/authz RuntimeInvalidator shape): an
// unwired composition keeps invalidation off. The wiring point lives in the
// composition root (cmd), outside this package — until it is wired the port
// stays off and behavior is unchanged. Invalidate never returns an error, so
// the fan-out is inherently best-effort: it runs strictly after the
// transaction commit and can never block or fail the sweep.
package authorization

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// RuntimeInvalidator is the K5 invalidation bus port. *inval.Bus satisfies it
// directly (the groups/authz/systemteams RuntimeInvalidator shape — no
// adapter translation); nil keeps invalidation off.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// AttachWriteInvalidator wires the committed-write invalidation port
// (compose-root handover, the Attach* convention of the authz store).
func (s *Store) AttachWriteInvalidator(invalidator RuntimeInvalidator) {
	s.invalidator = invalidator
}

// invalidationReasonExpired mirrors the authz slice's expired reason string:
// both slices materialize the same authorization-expiry state change.
const invalidationReasonExpired = "resource_authorization_expired"

// invalidateAfterExpiryCleanup runs the post-commit publish for a committed
// ExpireDue pass. A no-change pass (nothing was due) publishes nothing so a
// periodic driver does not clear caches on every tick.
func (s *Store) invalidateAfterExpiryCleanup(result ExpireResult) {
	if s.invalidator == nil {
		return
	}
	if result.GrantsExpired == 0 && result.AuthorizationsExpired == 0 && result.BindingsRemoved == 0 {
		return
	}
	s.invalidator.Invalidate(inval.TopicGatewayRuntime, invalidationReasonExpired)
	s.invalidator.Invalidate(inval.TopicAuthorizationQuota, invalidationReasonExpired)
}
