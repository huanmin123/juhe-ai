// Coverage-table behavior test (网关模型列表账户并集设计 6.3 "写路径 → 失效覆盖
// 依据"): the committed ExpireDue cleanup pass must clear the gateway runtime
// cache through the K5 bus port after its transaction commits — a changed pass
// publishes exactly the runtime + authorization-quota pair with
// resource_authorization_expired, a no-change pass publishes nothing, and an
// unwired (nil) port keeps the sweep behavior unchanged.
package authorization

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// recordingInvalidator captures the bus publishes as "topic reason" pairs.
type recordingInvalidator struct {
	calls []string
}

func (r *recordingInvalidator) Invalidate(topic, reason string) {
	r.calls = append(r.calls, topic+" "+reason)
}

func TestExpireDueFiresRuntimeInvalidationAfterCommit(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	bus := &recordingInvalidator{}
	s.AttachWriteInvalidator(bus)

	// A no-change pass (nothing due) commits no state change and publishes
	// nothing, so a periodic driver never clears caches on an empty tick.
	if _, e := s.ExpireDue(context.Background(), 10); e != nil {
		t.Fatal(e)
	}
	if len(bus.calls) != 0 {
		t.Fatalf("no-change pass published %v", bus.calls)
	}

	// One due grant + one active authorization + their dependent binding rows
	// (the same seed as TestExpireCleansTerminalBindings).
	if _, e := db.Exec(`INSERT INTO resource_authorizations VALUES('a','{}','active','2026-08-28T11:00:00Z',NULL,NULL,'r');INSERT INTO resource_authorization_grants VALUES('g','active','2026-08-28T11:00:00Z',NULL,'r');INSERT INTO group_accounts VALUES('a');INSERT INTO group_authorization_settings VALUES('a')`); e != nil {
		t.Fatal(e)
	}
	r, e := s.ExpireDue(context.Background(), 10)
	if e != nil || r.GrantsExpired != 1 || r.AuthorizationsExpired != 1 || r.BindingsRemoved != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	want := []string{
		inval.TopicGatewayRuntime + " " + invalidationReasonExpired,
		inval.TopicAuthorizationQuota + " " + invalidationReasonExpired,
	}
	if len(bus.calls) != len(want) {
		t.Fatalf("bus calls = %v, want %v", bus.calls, want)
	}
	for i := range want {
		if bus.calls[i] != want[i] {
			t.Fatalf("bus call[%d] = %q, want %q", i, bus.calls[i], want[i])
		}
	}
}

func TestExpireDueWithoutInvalidatorKeepsSweepBehavior(t *testing.T) {
	s, db := testStore(t)
	defer db.Close()
	// Nil port: the committed sweep must run unchanged (invalidation off).
	if _, e := db.Exec(`INSERT INTO resource_authorizations VALUES('a','{}','active','2026-08-28T11:00:00Z',NULL,NULL,'r');INSERT INTO resource_authorization_grants VALUES('g','active','2026-08-28T11:00:00Z',NULL,'r');INSERT INTO group_accounts VALUES('a');INSERT INTO group_authorization_settings VALUES('a')`); e != nil {
		t.Fatal(e)
	}
	r, e := s.ExpireDue(context.Background(), 10)
	if e != nil || r.GrantsExpired != 1 || r.AuthorizationsExpired != 1 || r.BindingsRemoved != 1 {
		t.Fatalf("%+v %v", r, e)
	}
}
