package gatewaysession

import (
	"context"
	"testing"
)

func TestW9CAffinityFinalArms(t *testing.T) {
	// No-op logger surface.
	noopAffinityLogger{}.Warn(map[string]any{"event": "x"}, "message")

	// redisBooleanResult wire shapes.
	if !redisBooleanResult(int64(1)) || redisBooleanResult(int64(0)) {
		t.Fatal("int64 semantics")
	}
	if !redisBooleanResult("1") || redisBooleanResult("0") {
		t.Fatal("string semantics")
	}
	if !redisBooleanResult(true) || redisBooleanResult(false) {
		t.Fatal("bool semantics")
	}
	if redisBooleanResult(nil) {
		t.Fatal("default must be false")
	}

	// canUseProcessLocalLocked under the redis driver never keeps local state.
	redisSvc, _, _, _ := newRedisAffinityService(t)
	redisSvc.mu.Lock()
	if redisSvc.canUseProcessLocalLocked() {
		redisSvc.mu.Unlock()
		t.Fatal("redis driver must refuse the process-local store")
	}
	redisSvc.mu.Unlock()

	// Async migrate on the memory driver delegates to the sync migration.
	memSvc, _, _ := newTestAffinityService(t, nil)
	scope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}
	memSvc.mu.Lock()
	memSvc.setSessionAffinityBindingLocked("w9c-mig-1", SessionBinding{AccountID: "acc-s", Scope: scope})
	memSvc.mu.Unlock()
	result, err := memSvc.MigrateOpenAIAccountSessionAffinityAsync(context.Background(), "acc-s", "acc-t", scope, MigrationOptions{})
	if err != nil || result.MigratedSessionCount != 1 {
		t.Fatalf("memory async migrate = %+v, %v", result, err)
	}

	// Redis migrate with a system-only filter scope (second index-key arm);
	// the binding scope must carry a groupId to survive the redis round trip.
	redisSvc2, _, _, _ := newRedisAffinityService(t)
	sysScope := &OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-9", GroupID: "grp-9"}
	if _, ok := redisSvc2.ClaimOpenAIAccountForSessionAsync(context.Background(), "w9c-mig-2", "acc-s2", sysScope); !ok {
		t.Fatal("claim must land the binding")
	}
	result, err = redisSvc2.MigrateOpenAIAccountSessionAffinityAsync(context.Background(), "acc-s2", "acc-t2", sysScope, MigrationOptions{})
	if err != nil || result.MigratedSessionCount != 1 {
		t.Fatalf("system-scope migrate = %+v, %v", result, err)
	}
	// Unscoped migrate (third index-key arm).
	if _, ok := redisSvc2.ClaimOpenAIAccountForSessionAsync(context.Background(), "w9c-mig-3", "acc-s3", nil); !ok {
		t.Fatal("claim must land the binding")
	}
	result, err = redisSvc2.MigrateOpenAIAccountSessionAffinityAsync(context.Background(), "acc-s3", "acc-t3", nil, MigrationOptions{})
	if err != nil || result.MigratedSessionCount != 1 {
		t.Fatalf("unscoped migrate = %+v, %v", result, err)
	}
	// Degenerate inputs return zero without touching redis.
	empty, err := redisSvc2.MigrateOpenAIAccountSessionAffinityAsync(context.Background(), "acc-x", "acc-x", nil, MigrationOptions{})
	if err != nil || empty.MigratedSessionCount != 0 {
		t.Fatalf("self migrate = %+v, %v", empty, err)
	}
}

func TestW9CAffinityForgetAndCandidateErrorArms(t *testing.T) {
	// Blank keys short-circuit.
	redisSvc, _, _, _ := newRedisAffinityService(t)
	if err := redisSvc.ForgetOpenAIAccountForSessionAsync(context.Background(), "", "acc-1"); err != nil {
		t.Fatalf("blank forget = %v", err)
	}
	// Redis outage warns and returns nil (the Node catch swallows).
	redisSvc2, _, logger, mr := newRedisAffinityService(t)
	if _, ok := redisSvc2.ClaimOpenAIAccountForSessionAsync(context.Background(), "w9c-fg-1", "acc-1", nil); !ok {
		t.Fatal("claim must land")
	}
	mr.Close()
	if err := redisSvc2.ForgetOpenAIAccountForSessionAsync(context.Background(), "w9c-fg-1", "acc-1"); err != nil {
		t.Fatalf("outage forget must be swallowed, got %v", err)
	}
	if !logger.HasEvent("redis_openai_session_affinity_forget_failed") {
		t.Fatalf("outage forget must warn, events = %v", logger.Events())
	}
	// Candidate key read surfaces the redis error.
	if _, err := redisSvc2.redisSessionAffinityMigrationCandidateKeys(context.Background(), "acc-1", nil); err == nil {
		t.Fatal("outage candidate read must error")
	}
	// Scope helper nil arms.
	if scopeAPIKeyID(nil) != "" || scopeSystemAccountID(nil) != "" || scopeGroupID(nil) != "" {
		t.Fatal("nil scope accessors must be empty")
	}
}
