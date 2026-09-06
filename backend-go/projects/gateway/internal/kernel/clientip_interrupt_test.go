package kernel

// 第三轮常驻审查 #4/#5 对齐测试：
//   - ExtractClientIP 对照归档 shared/request-context.ts:456/716：
//     IPv4-only（IPv6 → 空串，携带 Node undefined 语义）、XFF 条目数少于
//     受信代理数时回落 socket 地址（防伪造短链）。
//   - MutationGuardMiddleware 对照归档 mutation-guard.middleware.ts:70-74 的
//     res.once('close') 臂：响应未写出前客户端断开 → 定性 failed。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newGuardRequest builds a direct-to-recorder POST with the headers the
// express.json stage would see on a real request (httptest.NewRequest sets
// ContentLength but not the Content-Length header the parser type-is check
// reads).
func newGuardRequest(target, body string, ctx context.Context) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	return request
}

func TestExtractClientIPDialectAlignment(t *testing.T) {
	request := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/__aisys__/api/x", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	cases := []struct {
		name            string
		remote          string
		xff             string
		trustProxyCount int
		want            string
	}{
		// 链条足够长：取倒数第 trustProxyCount 个条目（Express req.ip）。
		{"twoEntriesOneTrusted", "10.0.0.1:1000", "203.0.113.9, 70.41.3.18", 1, "70.41.3.18"},
		{"twoEntriesTwoTrusted", "10.0.0.1:1000", "203.0.113.9, 70.41.3.18", 2, "203.0.113.9"},
		// 条目数 == 受信数：仍取 XFF（len >= count 边界）。
		{"oneEntryOneTrusted", "10.0.0.1:1000", "203.0.113.9", 1, "203.0.113.9"},
		// 条目数 < 受信数：无法定位未受信客户端，回落 socket（防伪造短链）。
		{"shortChainFallsBackToSocket", "10.0.0.5:1234", "6.6.6.6", 2, "10.0.0.5"},
		{"emptyChainFallsBackToSocket", "10.0.0.5:1234", "", 1, "10.0.0.5"},
		// 不信任代理：XFF 完全忽略。
		{"zeroTrustIgnoresXFF", "10.0.0.5:1234", "6.6.6.6", 0, "10.0.0.5"},
		// IPv4-only 归一化：IPv6 socket/XFF/非法值都归空并回落。
		{"ipv6RemoteIsUndefined", "[2001:db8::1]:443", "", 1, ""},
		{"ipv6CandidateFallsBackToSocket", "10.0.0.5:1234", "2001:db8::1, 10.10.10.1", 2, "10.0.0.5"},
		{"mappedIPv4Stripped", "10.0.0.1:1000", "::ffff:203.0.113.7", 1, "203.0.113.7"},
		{"dottedQuadWithPortStripped", "10.0.0.1:1000", "203.0.113.7:9877", 1, "203.0.113.7"},
		{"bracketedIPv6PortStripped", "[2001:db8::1]:443", "::ffff:203.0.113.7", 1, "203.0.113.7"},
		{"garbageCandidateFallsBackToSocket", "10.0.0.5:1234", "not-an-ip", 1, "10.0.0.5"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ExtractClientIP(request(testCase.remote, testCase.xff), testCase.trustProxyCount); got != testCase.want {
				t.Fatalf("ExtractClientIP = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestMutationGuardDisconnectSettlesFailed mirrors mutation-guard.middleware.ts
// res.once('close'): a client that disappears before any response byte is
// written settles the claim as failed — the retry window follows the default
// failed TTL, not the handler's would-be status.
func TestMutationGuardDisconnectSettlesFailed(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_000_000, 0)}
	store := NewDeduplicationStore(clock.Now)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "test.disconnect",
		Store:        store,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"name": TextField(BodyField(r, "name"))}, nil
		},
	})
	release := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The handler keeps working after the client vanished and writes
		// nothing before the connection close is observed.
		<-release
	})
	wrapped := guard(inner)
	ctx, cancel := context.WithCancel(context.Background())
	request := newGuardRequest("/__aisys__/api/disconnect", `{"name":"a"}`, ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		wrapped.ServeHTTP(httptest.NewRecorder(), request)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mu.Lock()
		_, claimed := store.entry["anonymous::POST:test.disconnect:"+HashStableValue(map[string]any{
			"name": "a",
		})]
		store.mu.Unlock()
		if claimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("claim never appeared")
		}
		time.Sleep(time.Millisecond)
	}

	// The client connection drops (net/http cancels the request context) and
	// the handler observes the disconnect.
	cancel()
	close(release)
	<-done

	store.mu.Lock()
	entry, ok := store.entry["anonymous::POST:test.disconnect:"+HashStableValue(map[string]any{
		"name": "a",
	})]
	store.mu.Unlock()
	if !ok {
		t.Fatal("interrupted claim must settle as failed, not be dropped")
	}
	if entry.Status != DedupFailed {
		t.Fatalf("interrupted claim status = %q, want failed", entry.Status)
	}

	// Inside the default 10s failed window the retry is answered 409 with the
	// failed-copy, mirroring the Node duplicateMessage('failed') arm.
	retry := newGuardRequest("/__aisys__/api/disconnect", `{"name":"a"}`, nil)
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, retry)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "请求刚刚失败，请稍后重试") {
		t.Fatalf("retry after interruption = %d %s, want 409 with failed copy", recorder.Code, recorder.Body.String())
	}
}

// TestMutationGuardCompletedResponseOutranksLateDisconnect keeps the Node
// finish-before-close ordering: when the handler finished its response, a
// connection close afterwards must not rewrite the outcome.
func TestMutationGuardCompletedResponseOutranksLateDisconnect(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_000_000, 0)}
	store := NewDeduplicationStore(clock.Now)
	guard := MutationGuardMiddleware(MutationGuardOptions{
		OperationKey: "test.completed",
		Store:        store,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"name": TextField(BodyField(r, "name"))}, nil
		},
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := newGuardRequest("/__aisys__/api/completed", `{"name":"a"}`, ctx)
	guard(handler).ServeHTTP(httptest.NewRecorder(), request)
	cancel() // connection drops after the response ended

	store.mu.Lock()
	entry, ok := store.entry["anonymous::POST:test.completed:"+HashStableValue(map[string]any{
		"name": "a",
	})]
	store.mu.Unlock()
	if !ok {
		t.Fatal("completed claim must be retained")
	}
	if entry.Status != DedupSucceeded {
		t.Fatalf("completed claim status = %q, want succeeded", entry.Status)
	}
}
