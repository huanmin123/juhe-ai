package authz

import "testing"

// The receiver must not learn about an admin-created offer before the resource
// owner approves it, even by requesting the grant id directly.
func TestVisibleToHidesPendingOwnerApprovalFromReceiver(t *testing.T) {
	d := &Deps{}
	receiver := "receiver"
	summary := &Summary{OwnerID: "owner", GranteeUserID: &receiver, Status: StatusPendingOwnerApproval}
	if d.visibleTo(summary, receiver) {
		t.Fatal("receiver must not see a pending_owner_approval offer")
	}
	if !d.visibleTo(summary, "owner") {
		t.Fatal("resource owner must see their pending approval")
	}
	summary.Status = StatusPending
	if !d.visibleTo(summary, receiver) {
		t.Fatal("receiver must see the offer after owner approval")
	}
}
