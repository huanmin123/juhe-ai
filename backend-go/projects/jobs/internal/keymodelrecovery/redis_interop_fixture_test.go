package keymodelrecovery

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

// TestNodeRedisRecoveryInteropFixture is opt-in. Its Node driver owns an
// isolated Redis namespace, publishes the failure intent, and removes all
// fixture keys after this test completes.
func TestNodeRedisRecoveryInteropFixture(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("JUHE_AI_KEY_MODEL_REDIS_INTEROP_URL"))
	namespace := strings.TrimSpace(os.Getenv("JUHE_AI_KEY_MODEL_REDIS_INTEROP_NAMESPACE"))
	sourceID := strings.TrimSpace(os.Getenv("JUHE_AI_KEY_MODEL_REDIS_INTEROP_SOURCE_ID"))
	if url == "" || namespace == "" || sourceID == "" {
		t.Skip("Node Redis interop fixture 未启用")
	}
	store, err := OpenRedisStore(RedisConfig{Enabled: true, URL: url, Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping fixture Redis: %v", err)
	}

	key := CapabilityKey{
		CredentialSourceAccountID: sourceID,
		KeyFingerprint:            "node-go-interop-key-fingerprint",
		ClientModel:               "gpt-node-go-interop",
		ClientEndpointFamily:      "chat_completions",
		FinalUpstreamModel:        "gpt-node-go-interop",
		UpstreamEndpointMode:      "chat_json",
		DispatchRevision:          1,
	}
	hash, err := Hash(key)
	if err != nil {
		t.Fatal(err)
	}
	now, err := store.ServerNow(ctx)
	if err != nil {
		t.Fatalf("读取 interop Redis 时间: %v", err)
	}
	due, err := store.ListDue(ctx, now, recoveryBatchLimit)
	if err != nil {
		t.Fatalf("读取 Node 发布的 interop due state: %v", err)
	}
	if len(due) != 1 || due[0].CapabilityHash != hash || due[0].Phase != Open {
		t.Fatalf("Node 发布的 interop due state 不存在或不精确: %#v", due)
	}
	runner := NewRunner(store, redisInteropLoader{input: accounthealth.Input{AccountID: sourceID, DispatchRevision: key.DispatchRevision}}, nil)
	runner.probe = func(_ context.Context, state State, input accounthealth.Input) Outcome {
		if state.CapabilityHash != hash || input.AccountID != sourceID || input.DispatchRevision != key.DispatchRevision {
			t.Errorf("精确恢复输入不匹配: state=%#v input=%#v", state, input)
		}
		return CompleteSuccess
	}

	for expectedSuccesses := 1; expectedSuccesses <= RecoverySuccessThreshold; expectedSuccesses++ {
		if err := runner.RunCycle(ctx); err != nil {
			t.Fatalf("recovery cycle %d: %v", expectedSuccesses, err)
		}
		state := waitForInteropState(t, ctx, store, hash, expectedSuccesses)
		if expectedSuccesses == RecoverySuccessThreshold {
			if state.Phase != Closed {
				t.Fatalf("第 %d 次恢复后 phase=%s, want CLOSED", expectedSuccesses, state.Phase)
			}
			return
		}
		if state.Phase != Recovering || state.RecoverySuccessCount != expectedSuccesses || state.LastRecoverySuccessAt.IsZero() {
			t.Fatalf("第 %d 次恢复 state=%#v", expectedSuccesses, state)
		}
		wait := time.Until(state.RetryAt) + 100*time.Millisecond
		if wait > 0 {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(wait):
			}
		}
	}
}

type redisInteropLoader struct{ input accounthealth.Input }

func (l redisInteropLoader) LoadAccount(context.Context, string) ([]accounthealth.Input, error) {
	return []accounthealth.Input{l.input}, nil
}

func waitForInteropState(t *testing.T, ctx context.Context, store *RedisStore, hash string, expectedSuccesses int) State {
	t.Helper()
	for {
		raw, err := store.client.Get(ctx, store.keys.State(hash)).Bytes()
		if err == nil {
			var state State
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatalf("读取 interop state: %v", err)
			}
			if state.RecoverySuccessCount >= expectedSuccesses || state.Phase == Closed {
				return state
			}
		} else {
			t.Fatalf("读取 interop Redis state: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("等待第 %d 次 interop 恢复超时: %v", expectedSuccesses, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
