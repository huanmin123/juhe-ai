package authzrevision

import "context"

// GroupBindingValidator is the narrow port the route-strategy write path uses
// before binding a group owned by another account. It verifies the recipient's
// runtime/source acceptance and the group snapshot still match the current
// material revisions; the gateway repeats the check at request time.
type GroupBindingValidator interface {
	ValidateGroupBinding(ctx context.Context, q Queryer, granteeID, groupID, ownerID, now string) (bool, error)
}
