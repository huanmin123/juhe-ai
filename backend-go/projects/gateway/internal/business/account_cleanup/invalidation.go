// Post-commit gateway runtime cache invalidation for the committed
// account-cleanup writes (softDeleteOrphan's orphan-instance tombstone arm and
// deleteBusiness's physical-delete arm, 网关模型列表账户并集设计 6.3 "写路径 →
// 失效覆盖依据"). A committed cleanup write materializes authorization
// terminal states (resource_authorizations / resource_authorization_sources /
// resource_authorization_grants → revoked), removes dependent group_accounts
// rows, and physically deletes accounts plus their account_supported_models /
// account_model_mappings relations — all part of the gateway model-list union
// cache dependency face, so the committed write must clear the gateway runtime
// cache like every other covered write path. Both changed arms also
// materialize or remove authorization-grant rows (softDeleteOrphan's
// revokeInstance/revokeAccountAuthorizations flip grants/sources/authorizations
// to revoked; deleteBusiness physically deletes resource_authorization_grants /
// resource_authorizations / resource_authorization_sources), so the publish
// mirrors the business/authorization ExpireDue two-topic shape: the runtime
// topic plus the authorization-quota topic. This slice has no archived Node
// refresh fan-out; the reason string is slice-local.
//
// The port is nil-tolerant (the business/authorization RuntimeInvalidator
// shape): an unwired composition keeps invalidation off. The wiring point
// lives in the composition root (cmd), outside this package — the package has
// no production importer yet, so the port takes effect only when that wiring
// lands. Invalidate never returns an error, so the publish is inherently
// best-effort: it runs strictly after the transaction commit and can never
// block or fail the cleanup.
package accountcleanup

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// RuntimeInvalidator is the K5 invalidation bus port. *inval.Bus satisfies it
// directly (the business/authorization RuntimeInvalidator shape — no adapter
// translation); nil keeps invalidation off.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// AttachWriteInvalidator wires the committed-write invalidation port
// (compose-root handover, the Attach* convention of the authz store).
func (s *Store) AttachWriteInvalidator(invalidator RuntimeInvalidator) {
	s.invalidator = invalidator
}

// invalidationReasonCleanupApplied marks a committed cleanup write that
// actually changed union-cache dependency rows. There is no archived Node
// reason for this slice; the behavior contract is the ExpireDue precedent: a
// no-change pass publishes nothing so a periodic driver does not clear caches
// on every tick.
const invalidationReasonCleanupApplied = "account_cleanup_applied"

// invalidateAfterCleanupApplied runs the post-commit publish for a committed
// cleanup write that changed rows, in the business/authorization ExpireDue
// two-topic shape (runtime + authorization-quota): both changed arms touch
// grant/authorization rows, so the quota arm follows the same materialization
// the authz slice runs for committed authorization writes. Call sites sit
// strictly after a successful tx.Commit and only on changed arms —
// softDeleteOrphan's two no-change commit arms (already tombstoned /
// concurrently completed) return before publishing, and deleteBusiness's CAS
// failures roll back before the commit.
func (s *Store) invalidateAfterCleanupApplied() {
	if s.invalidator == nil {
		return
	}
	s.invalidator.Invalidate(inval.TopicGatewayRuntime, invalidationReasonCleanupApplied)
	s.invalidator.Invalidate(inval.TopicAuthorizationQuota, invalidationReasonCleanupApplied)
}
