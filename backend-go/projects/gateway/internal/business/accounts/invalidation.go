// Post-commit gateway runtime cache invalidation for the committed account
// management writes of this Gateway-owned slice (create + patch + delete,
// 网关模型列表账户并集设计 6.3 "写路径 → 失效覆盖依据"). A committed create,
// patch, or soft delete materializes the accounts row and its
// account_supported_models / account_model_mappings relations
// (writeRelations/writeRelationsPatch) — all part of the gateway model-list
// union cache dependency face, so the committed write must clear the gateway
// runtime cache like every other covered write path.
//
// The reason strings reuse the accounts main-package literals
// (internal/accounts/invalidation.go:68-75): both slices materialize the same
// account create/patch/delete state changes, so bus subscribers key on one
// shared reason value. The port is nil-tolerant (the business/authorization
// RuntimeInvalidator shape): an unwired composition keeps invalidation off,
// and this package has no production importer yet — the port takes effect
// only when the composition-root wiring lands. Invalidate never returns an
// error, so the publish is inherently best-effort: it runs strictly after the
// transaction commit and can never block or fail the write.
package accounts

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

// Reason literals aligned with the accounts main package
// (internal/accounts/invalidation.go:68-75): same state change, same reason.
const (
	invalidationReasonCreated = "account_created"
	invalidationReasonPatched = "account_management_patch"
	invalidationReasonDeleted = "account_deleted"
)

// invalidateAfterCommittedWrite runs the post-commit publish for a committed
// create, changed patch, or soft delete. Call it only after a successful
// tx.Commit and only when the transaction actually changed rows — the
// idempotent-retry arm of create, the all-nil patch, the stale-revision
// delete, and the already-deleted lookup arm commit no state change and stay
// silent.
func (s *Store) invalidateAfterCommittedWrite(reason string) {
	if s.invalidator == nil {
		return
	}
	s.invalidator.Invalidate(inval.TopicGatewayRuntime, reason)
}
