package operationlog

import (
	"context"
	"testing"
	"time"
)

func TestW11FLeaseFaultArms(t *testing.T) {
	ctx := context.Background()
	store, script, lease := w11fFaultStore(t)

	script.failQuery("INSERT INTO operation_log_owner_leases")
	if _, _, err := store.AcquireOwnerLease(ctx, "w11f-owner-2", time.Minute); err == nil {
		t.Fatal("acquire fault must fail")
	}
	script.failQuery("INSERT INTO operation_log_owner_leases")
	if _, _, err := store.AcquireOwnerLease(ctx, "w11f-owner-2", time.Minute); err == nil {
		t.Fatal("acquire insert fault must fail")
	}
	script.failExec("UPDATE operation_log_owner_leases")
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil {
		t.Fatal("release fault must fail")
	}
}
