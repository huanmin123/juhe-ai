// Coverage-table behavior test (网关模型列表账户并集设计 6.3 "写路径 → 失效覆盖
// 依据"): the committed create, patch, and delete writes must clear the gateway
// runtime cache through the K5 bus port after their transactions commit —
// create publishes account_created, a changed patch (base fields or
// supportedModels/mappings relations) publishes account_management_patch, the
// soft delete publishes account_deleted, a no-op patch / idempotent-retry
// create / stale-revision delete / already-deleted lookup publish nothing, and
// an unwired (nil) port keeps the write behavior unchanged.
package accounts

import (
	"context"
	"errors"
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

func TestCreateAndPatchFireRuntimeInvalidationAfterCommit(t *testing.T) {
	db := testDB(t)
	s, err := NewStore(db, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	bus := &recordingInvalidator{}
	s.AttachWriteInvalidator(bus)
	ctx := context.Background()

	// The committed create publishes exactly one account_created publish.
	if _, err := s.create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	wantCreated := inval.TopicGatewayRuntime + " " + invalidationReasonCreated
	if len(bus.calls) != 1 || bus.calls[0] != wantCreated {
		t.Fatalf("bus calls = %v, want [%s]", bus.calls, wantCreated)
	}

	// The idempotent-retry arm (semantically identical create) rolls back and
	// publishes nothing.
	if _, err := s.create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	if len(bus.calls) != 1 {
		t.Fatalf("idempotent retry published %v", bus.calls)
	}

	// An all-nil patch commits an empty transaction and publishes nothing.
	if _, err := s.patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if len(bus.calls) != 1 {
		t.Fatalf("no-op patch published %v", bus.calls)
	}

	// A base-field patch publishes account_management_patch.
	name := "B"
	if _, err := s.patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1, Name: &name}); err != nil {
		t.Fatal(err)
	}
	wantPatched := inval.TopicGatewayRuntime + " " + invalidationReasonPatched
	if len(bus.calls) != 2 || bus.calls[1] != wantPatched {
		t.Fatalf("bus calls = %v, want %v", bus.calls, []string{wantCreated, wantPatched})
	}

	// A relations-only patch (supportedModels/mappings are union-cache
	// dependency rows) publishes account_management_patch too.
	mappings := []ModelMapping{{SourceModel: "gpt-5", SourceEndpointFamily: "chat", UpstreamModel: "gpt-4o", UpstreamEndpointFamily: "chat", Enabled: true}}
	if _, err := s.patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 2, ModelMappings: &mappings}); err != nil {
		t.Fatal(err)
	}
	if len(bus.calls) != 3 || bus.calls[2] != wantPatched {
		t.Fatalf("bus calls = %v, want third %s", bus.calls, wantPatched)
	}

	// A rolled-back create (invalid child binding) publishes nothing.
	bad := input()
	bad.ID = "a2"
	bad.APIKeyBindings = []APIKeyBinding{{ID: "", Fingerprint: "fp"}}
	if _, err := s.create(ctx, bad); err == nil {
		t.Fatal("invalid child binding must fail")
	}
	if len(bus.calls) != 3 {
		t.Fatalf("rolled-back create published %v", bus.calls)
	}

	// A stale-revision delete (CAS conflict) rolls back and publishes nothing.
	if _, err := s.delete(ctx, "sys", "a1", 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	if len(bus.calls) != 3 {
		t.Fatalf("stale-revision delete published %v", bus.calls)
	}

	// The committed soft delete publishes exactly one account_deleted publish.
	if _, err := s.delete(ctx, "sys", "a1", 3); err != nil {
		t.Fatal(err)
	}
	wantDeleted := inval.TopicGatewayRuntime + " " + invalidationReasonDeleted
	if len(bus.calls) != 4 || bus.calls[3] != wantDeleted {
		t.Fatalf("bus calls = %v, want fourth %s", bus.calls, wantDeleted)
	}

	// Deleting an already-deleted account returns at the read arm before the
	// commit and publishes nothing.
	if _, err := s.delete(ctx, "sys", "a1", 4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("already-deleted delete err=%v", err)
	}
	if len(bus.calls) != 4 {
		t.Fatalf("already-deleted delete published %v", bus.calls)
	}
}

func TestAccountsWithoutInvalidatorKeepsWriteBehavior(t *testing.T) {
	db := testDB(t)
	s, err := NewStore(db, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	// Nil port: the committed writes must run unchanged (invalidation off).
	ctx := context.Background()
	if _, err := s.create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	name := "B"
	if _, err := s.patch(ctx, "sys", "a1", Patch{ExpectedConfigRevision: 1, Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.delete(ctx, "sys", "a1", 2); err != nil {
		t.Fatal(err)
	}
}
