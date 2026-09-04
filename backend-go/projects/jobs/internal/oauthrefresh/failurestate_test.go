package oauthrefresh

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func jsonUnmarshal(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// scriptRecorder is a minimal in-memory Scripter capturing Eval payloads so
// the Lua record semantics can be asserted without a Redis server.
type scriptRecorder struct {
	store map[string]string
	runs  []scriptRun
}

type scriptRun struct {
	script string
	keys   []string
	args   []any
}

func (s *scriptRecorder) Eval(_ context.Context, script string, keys []string, args ...any) (any, error) {
	s.runs = append(s.runs, scriptRun{script: script, keys: keys, args: args})
	isRecord := len(keys) == 1 && len(args) == 5
	if isRecord {
		key := keys[0]
		current, ok := s.store[key]
		storedRevision := 0
		if ok {
			var parsed struct {
				ConfigRevision int64 `json:"configRevision"`
			}
			if err := unmarshalJSON(current, &parsed); err == nil {
				storedRevision = int(parsed.ConfigRevision)
			}
		}
		if storedRevision > int(argsRevision(args)) {
			return []any{int64(9), int64(0), int64(0), int64(storedRevision), int64(0), "stored", current}, nil
		}
		count := 0
		localCount := 0
		if ok && storedRevision == int(argsRevision(args)) {
			var parsed struct {
				Count                   int64 `json:"count"`
				LocalConfigurationCount int64 `json:"localConfigurationCount"`
			}
			if err := unmarshalJSON(current, &parsed); err == nil {
				count = int(parsed.Count)
				localCount = int(parsed.LocalConfigurationCount)
			}
		}
		count++
		if args[2] == "1" {
			localCount++
		} else {
			localCount = 0
		}
		payload := `{"count":` + itoa(count) + `,"localConfigurationCount":` + itoa(localCount) + `,"backoffUntil":` + stringOf(args[0]) + `,"configRevision":` + stringOf(args[3]) + `,"mutationId":"` + stringOf(args[4]) + `"}`
		s.store[key] = payload
		return []any{int64(count), int64(0), int64(localCount), int64(storedRevision), int64(1), stringOf(args[4]), payload}, nil
	}
	// compare-delete
	key := keys[0]
	if raw, ok := s.store[key]; ok && raw == args[0].(string) {
		delete(s.store, key)
		return int64(1), nil
	}
	return int64(0), nil
}

func (s *scriptRecorder) Get(_ context.Context, key string) (string, error) {
	return s.store[key], nil
}

func argsRevision(args []any) int {
	value := stringOf(args[3])
	number := 0
	for _, char := range value {
		if char < '0' || char > '9' {
			return number
		}
		number = number*10 + int(char-'0')
	}
	return number
}

func stringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func TestRedisFailureStateStoreRecordReadClear(t *testing.T) {
	redis := &scriptRecorder{store: map[string]string{}}
	store := NewRedisFailureStateStore(redis)
	ctx := context.Background()

	first, err := store.Record(ctx, "acc-1", 1000, FailureKindLocalConfiguration, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.Count != 1 || first.LocalConfigurationCount != 1 || !first.Applied {
		t.Fatalf("first=%+v", first)
	}
	second, err := store.Record(ctx, "acc-1", 2000, FailureKindLocalConfiguration, 3)
	if err != nil {
		t.Fatal(err)
	}
	if second.Count != 2 || second.LocalConfigurationCount != 2 {
		t.Fatalf("second=%+v", second)
	}
	// A runtime failure resets the local counter.
	third, err := store.Record(ctx, "acc-1", 3000, FailureKindUntrustedUpstream, 3)
	if err != nil {
		t.Fatal(err)
	}
	if third.Count != 3 || third.LocalConfigurationCount != 0 {
		t.Fatalf("third=%+v", third)
	}

	// Read honours backoff expiry.
	read, err := store.Read(ctx, "acc-1", 2500, 3)
	if err != nil {
		t.Fatal(err)
	}
	if read == nil || read.BackoffUntil != 3000 {
		t.Fatalf("read=%+v", read)
	}
	expired, err := store.Read(ctx, "acc-1", 4000, 3)
	if err != nil {
		t.Fatal(err)
	}
	if expired == nil || expired.BackoffUntil != 0 {
		t.Fatalf("expired read=%+v", expired)
	}

	// Newer account revisions shadow the record; older revisions clear it.
	shadowed, err := store.Read(ctx, "acc-1", 100, 4)
	if err != nil || shadowed != nil {
		t.Fatalf("shadowed=%+v err=%v", shadowed, err)
	}
	cleared, err := store.Read(ctx, "acc-1", 100, 2)
	if err != nil || cleared != nil {
		t.Fatalf("cleared=%+v err=%v", cleared, err)
	}
	if len(redis.store) != 0 {
		t.Fatalf("older revision read must clear the record: %v", redis.store)
	}

	// Clear honours the snapshot guard.
	state, err := store.Record(ctx, "acc-2", 1, FailureKindLocalConfiguration, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(ctx, "acc-2", state); err != nil {
		t.Fatal(err)
	}
	if len(redis.store) != 0 {
		t.Fatalf("clear must delete: %v", redis.store)
	}
	// A guard without a snapshot is a no-op.
	_ = store.Clear(ctx, "acc-2", RefreshFailureState{})
}

func unmarshalJSON(raw string, target any) error {
	return json.Unmarshal([]byte(raw), target)
}
