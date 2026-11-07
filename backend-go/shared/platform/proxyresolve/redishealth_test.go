package proxyresolve

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

type fakeMGet struct {
	values []any
	err    error
	calls  int
	keys   []string
}

func (f *fakeMGet) MGet(_ context.Context, keys ...string) *redis.SliceCmd {
	f.calls++
	f.keys = append([]string{}, keys...)
	return redis.NewSliceResult(f.values, f.err)
}

func TestRedisHealthReaderPresentStates(t *testing.T) {
	exec := &fakeMGet{values: []any{"healthy", []byte("dead"), nil, "degraded", "bogus"}}
	reader := NewRedisHealthReader(exec, "dev")
	got, err := reader.ReadMemberHealth(context.Background(), []string{"p1", "p2", "p3", "p4", "p5", "p1"})
	if err != nil {
		t.Fatalf("ReadMemberHealth: %v", err)
	}
	want := map[string]HealthState{"p1": HealthHealthy, "p2": HealthDead, "p4": HealthDegraded}
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for id, state := range want {
		if got[id] != state {
			t.Fatalf("state[%s] = %q, want %q", id, got[id], state)
		}
	}
	if exec.calls != 1 || len(exec.keys) != 5 {
		t.Fatalf("MGET calls/keys = %d/%d, want 1/5 (deduped)", exec.calls, len(exec.keys))
	}
	if exec.keys[0] != "juhe-ai:{dev}:proxy-pool:member-health:p1" {
		t.Fatalf("first key = %q", exec.keys[0])
	}
}

func TestRedisHealthReaderErrorAndEmpty(t *testing.T) {
	reader := NewRedisHealthReader(&fakeMGet{err: errors.New("redis down")}, "dev")
	if _, err := reader.ReadMemberHealth(context.Background(), []string{"p1"}); err == nil {
		t.Fatal("bulk error must propagate for PG fallback")
	}
	empty := NewRedisHealthReader(nil, "dev")
	got, err := empty.ReadMemberHealth(context.Background(), []string{"p1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("nil client = (%v, %v), want empty/no error", got, err)
	}
	noNamespace := NewRedisHealthReader(&fakeMGet{}, "")
	got, err = noNamespace.ReadMemberHealth(context.Background(), []string{"p1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty namespace = (%v, %v), want empty/no error", got, err)
	}
}
