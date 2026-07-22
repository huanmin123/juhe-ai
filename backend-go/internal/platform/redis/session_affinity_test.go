package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSessionAffinityStoreSetAndGetRoundTrip(t *testing.T) {
	var storedKey string
	var storedValue []byte
	var storedTTL time.Duration
	store := &SessionAffinityStore{
		keyPrefix: "juhe-ai:test:session-affinity:binding",
		newRevision: func() string {
			return "revision-1"
		},
		set: func(_ context.Context, key string, value []byte, ttl time.Duration) error {
			storedKey = key
			storedValue = append([]byte(nil), value...)
			storedTTL = ttl
			return nil
		},
	}

	record, err := store.Set(t.Context(), " session-token ", []byte(`{"accountId":"account-1"}`), time.Hour)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if record.Revision != "revision-1" || string(record.Value) != `{"accountId":"account-1"}` {
		t.Fatalf("Set() record = %#v", record)
	}
	if storedTTL != time.Hour {
		t.Fatalf("Set() ttl = %v, want %v", storedTTL, time.Hour)
	}
	if !strings.HasPrefix(storedKey, "juhe-ai:test:session-affinity:binding:") {
		t.Fatalf("Set() key = %q", storedKey)
	}
	if strings.Contains(storedKey, "session-token") || len(storedKey) > len("juhe-ai:test:session-affinity:binding:")+43 {
		t.Fatalf("Set() key must contain only a fixed-size token digest: %q", storedKey)
	}

	store.get = func(_ context.Context, key string) ([]byte, error) {
		if key != storedKey {
			t.Fatalf("Get() key = %q, want %q", key, storedKey)
		}
		return append([]byte(nil), storedValue...), nil
	}
	loaded, err := store.Get(t.Context(), "session-token")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Revision != record.Revision || string(loaded.Value) != string(record.Value) {
		t.Fatalf("Get() record = %#v, want %#v", loaded, record)
	}
	loaded.Value[0] = 'X'
	if storedValue[len("revision-1\n")] == 'X' {
		t.Fatal("Get() returned storage-owned value bytes")
	}
}

func TestNewSessionAffinityStoreVersionsItsKeyspace(t *testing.T) {
	client, err := NewClient("redis://127.0.0.1:6379/0", "juhe-ai:test:cache")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	store, err := NewSessionAffinityStore(client)
	if err != nil {
		t.Fatalf("NewSessionAffinityStore() error = %v", err)
	}
	if got, want := store.keyPrefix, "juhe-ai:test:cache:session-affinity:"+SessionAffinityFormatVersion+":binding"; got != want {
		t.Fatalf("keyPrefix = %q, want %q", got, want)
	}
}

func TestSessionAffinityStoreCompareAndSetSupportsCreateAndExactRevision(t *testing.T) {
	var calls int
	store := &SessionAffinityStore{
		keyPrefix: "juhe-ai:test:session-affinity:binding",
		newRevision: func() string {
			return "revision-new"
		},
		compareAndSet: func(_ context.Context, key, expected string, value []byte, ttl time.Duration) (bool, error) {
			calls++
			if !strings.HasPrefix(key, "juhe-ai:test:session-affinity:binding:") {
				t.Fatalf("CompareAndSet() key = %q", key)
			}
			if calls == 1 && expected != "" {
				t.Fatalf("create expected revision = %q, want empty", expected)
			}
			if calls == 2 && expected != "revision-old" {
				t.Fatalf("replace expected revision = %q", expected)
			}
			if got := string(value); got != "revision-new\naccount-2" {
				t.Fatalf("encoded value = %q", got)
			}
			if ttl != 30*time.Minute {
				t.Fatalf("ttl = %v", ttl)
			}
			return calls == 1, nil
		},
	}

	created, swapped, err := store.CompareAndSet(t.Context(), "token", "", []byte("account-2"), 30*time.Minute)
	if err != nil || !swapped || created.Revision != "revision-new" {
		t.Fatalf("create record=%#v swapped=%v error=%v", created, swapped, err)
	}
	notReplaced, swapped, err := store.CompareAndSet(t.Context(), "token", "revision-old", []byte("account-2"), 30*time.Minute)
	if err != nil || swapped || notReplaced.Revision != "" || notReplaced.Value != nil {
		t.Fatalf("replace record=%#v swapped=%v error=%v", notReplaced, swapped, err)
	}
}

func TestSessionAffinityStoreCompareAndDeleteAndTouchUseRevision(t *testing.T) {
	store := &SessionAffinityStore{
		keyPrefix: "juhe-ai:test:session-affinity:binding",
		compareAndDelete: func(_ context.Context, _ string, expected string) (bool, error) {
			return expected == "revision-1", nil
		},
		touch: func(_ context.Context, _ string, expected string, ttl time.Duration) (bool, error) {
			return expected == "revision-1" && ttl == time.Hour, nil
		},
	}

	deleted, err := store.CompareAndDelete(t.Context(), "token", "revision-1")
	if err != nil || !deleted {
		t.Fatalf("CompareAndDelete() deleted=%v error=%v", deleted, err)
	}
	touched, err := store.Touch(t.Context(), "token", "revision-1", time.Hour)
	if err != nil || !touched {
		t.Fatalf("Touch() touched=%v error=%v", touched, err)
	}
}

func TestSessionAffinityStoreValidatesBoundariesBeforeRedis(t *testing.T) {
	redisCalls := 0
	store := &SessionAffinityStore{
		keyPrefix: "juhe-ai:test:session-affinity:binding",
		newRevision: func() string {
			return "revision-1"
		},
		set: func(context.Context, string, []byte, time.Duration) error {
			redisCalls++
			return nil
		},
		compareAndSet: func(context.Context, string, string, []byte, time.Duration) (bool, error) {
			redisCalls++
			return true, nil
		},
		compareAndDelete: func(context.Context, string, string) (bool, error) {
			redisCalls++
			return true, nil
		},
		touch: func(context.Context, string, string, time.Duration) (bool, error) {
			redisCalls++
			return true, nil
		},
	}

	invalidSets := []struct {
		token string
		value []byte
		ttl   time.Duration
	}{
		{token: "", value: []byte("value"), ttl: time.Hour},
		{token: strings.Repeat("x", SessionAffinityMaxTokenBytes+1), value: []byte("value"), ttl: time.Hour},
		{token: "token", value: nil, ttl: time.Hour},
		{token: "token", value: make([]byte, SessionAffinityMaxValueBytes+1), ttl: time.Hour},
		{token: "token", value: []byte("value"), ttl: time.Nanosecond},
		{token: "token", value: []byte("value"), ttl: SessionAffinityMaxTTL + time.Millisecond},
	}
	for index, input := range invalidSets {
		if _, err := store.Set(t.Context(), input.token, input.value, input.ttl); err == nil {
			t.Fatalf("Set(invalid case %d) error = nil", index)
		}
	}
	if _, _, err := store.CompareAndSet(t.Context(), "token", strings.Repeat("r", SessionAffinityMaxRevisionBytes+1), []byte("value"), time.Hour); err == nil {
		t.Fatal("CompareAndSet(long revision) error = nil")
	}
	if _, err := store.CompareAndDelete(t.Context(), "token", ""); err == nil {
		t.Fatal("CompareAndDelete(empty revision) error = nil")
	}
	if _, err := store.Touch(t.Context(), "token", "", time.Hour); err == nil {
		t.Fatal("Touch(empty revision) error = nil")
	}
	if redisCalls != 0 {
		t.Fatalf("Redis calls = %d, want 0", redisCalls)
	}
}

func TestSessionAffinityStoreHonorsCanceledContext(t *testing.T) {
	redisCalls := 0
	store := &SessionAffinityStore{
		keyPrefix: "juhe-ai:test:session-affinity:binding",
		get: func(context.Context, string) ([]byte, error) {
			redisCalls++
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(ctx, "token"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get(canceled) error = %v, want context.Canceled", err)
	}
	if redisCalls != 0 {
		t.Fatalf("Redis calls = %d, want 0", redisCalls)
	}
}

func TestSessionAffinityStoreRejectsCorruptRecord(t *testing.T) {
	store := &SessionAffinityStore{
		keyPrefix: "juhe-ai:test:session-affinity:binding",
		get: func(context.Context, string) ([]byte, error) {
			return []byte("missing-separator"), nil
		},
	}
	if _, err := store.Get(t.Context(), "token"); !errors.Is(err, ErrInvalidSessionAffinityRecord) {
		t.Fatalf("Get(corrupt) error = %v", err)
	}
}

func TestSessionAffinityLuaGuardsRevisionBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		lua  string
		want []string
	}{
		{
			name: "compare-and-set",
			lua:  compareAndSetSessionAffinityLua,
			want: []string{"redis.call('GET', KEYS[1])", "current_revision ~= expected_revision", "redis.call('SET', KEYS[1]", "'PX', ARGV[3]"},
		},
		{
			name: "compare-and-delete",
			lua:  compareAndDeleteSessionAffinityLua,
			want: []string{"redis.call('GET', KEYS[1])", "current_revision ~= ARGV[1]", "redis.call('DEL', KEYS[1])"},
		},
		{
			name: "touch",
			lua:  touchSessionAffinityLua,
			want: []string{"redis.call('GET', KEYS[1])", "current_revision ~= ARGV[1]", "redis.call('PEXPIRE', KEYS[1], ARGV[2])"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, want := range test.want {
				if !strings.Contains(test.lua, want) {
					t.Fatalf("Lua missing %q", want)
				}
			}
			guardIndex := strings.Index(test.lua, "current_revision ~=")
			mutationIndex := strings.Index(test.lua, "redis.call('SET'")
			if mutationIndex < 0 {
				mutationIndex = strings.Index(test.lua, "redis.call('DEL'")
			}
			if mutationIndex < 0 {
				mutationIndex = strings.Index(test.lua, "redis.call('PEXPIRE'")
			}
			if guardIndex < 0 || mutationIndex < 0 || guardIndex > mutationIndex {
				t.Fatal("revision guard must precede mutation")
			}
		})
	}
}

func TestSessionAffinityStoreRedisIntegration(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_URL"))
	if rawURL == "" {
		if os.Getenv("JUHE_AI_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("JUHE_AI_TEST_REDIS_URL is required when JUHE_AI_REQUIRE_INTEGRATION=1")
		}
		t.Skip("JUHE_AI_TEST_REDIS_URL is not configured")
	}

	client, err := NewClient(rawURL, "session-affinity-test-"+uuid.NewString())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := client.Ping(t.Context()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	store, err := NewSessionAffinityStore(client)
	if err != nil {
		t.Fatalf("NewSessionAffinityStore() error = %v", err)
	}
	const token = "integration-token"
	key, err := store.redisKey(token)
	if err != nil {
		t.Fatalf("redisKey() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.client.Del(context.Background(), key).Err(); err != nil {
			t.Errorf("cleanup DEL error = %v", err)
		}
	})

	base, created, err := store.CompareAndSet(t.Context(), token, "", []byte("base"), time.Minute)
	if err != nil || !created {
		t.Fatalf("initial CompareAndSet() record=%#v created=%v error=%v", base, created, err)
	}
	if _, created, err := store.CompareAndSet(t.Context(), token, "", []byte("duplicate"), time.Minute); err != nil || created {
		t.Fatalf("duplicate create created=%v error=%v", created, err)
	}

	const contenders = 32
	results := make(chan bool, contenders)
	errorsCh := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(candidate int) {
			defer wait.Done()
			_, swapped, swapErr := store.CompareAndSet(
				t.Context(),
				token,
				base.Revision,
				[]byte(fmt.Sprintf("candidate-%02d", candidate)),
				time.Minute,
			)
			results <- swapped
			errorsCh <- swapErr
		}(index)
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	winners := 0
	for swapped := range results {
		if swapped {
			winners++
		}
	}
	for swapErr := range errorsCh {
		if swapErr != nil {
			t.Fatalf("concurrent CompareAndSet() error = %v", swapErr)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent CAS winners = %d, want 1", winners)
	}

	current, err := store.Get(t.Context(), token)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if touched, err := store.Touch(t.Context(), token, base.Revision, 30*time.Minute); err != nil || touched {
		t.Fatalf("stale Touch() touched=%v error=%v", touched, err)
	}
	if deleted, err := store.CompareAndDelete(t.Context(), token, base.Revision); err != nil || deleted {
		t.Fatalf("stale CompareAndDelete() deleted=%v error=%v", deleted, err)
	}
	if touched, err := store.Touch(t.Context(), token, current.Revision, 30*time.Minute); err != nil || !touched {
		t.Fatalf("current Touch() touched=%v error=%v", touched, err)
	}
	ttl, err := client.client.PTTL(t.Context(), key).Result()
	if err != nil || ttl < 29*time.Minute || ttl > 30*time.Minute {
		t.Fatalf("PTTL() = %v, error = %v", ttl, err)
	}
	if deleted, err := store.CompareAndDelete(t.Context(), token, current.Revision); err != nil || !deleted {
		t.Fatalf("current CompareAndDelete() deleted=%v error=%v", deleted, err)
	}
	if _, err := store.Get(t.Context(), token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}
}
