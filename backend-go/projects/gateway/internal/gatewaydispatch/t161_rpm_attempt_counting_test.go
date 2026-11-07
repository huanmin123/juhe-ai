package gatewaydispatch

// T161：RPM 必须按真实上游出站尝试逐次计数。TryAcquire 已为当前并发槽的
// 首次出站计 1 次；同一槽内的同账户重试、Key 轮换和 OAuth 刷新在出站前再
// 逐次消费。断言统一用「acquire + consume = 实际上游请求数」，避免只测内部
// 调用次数而漏掉真实出站。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
)

type t161RPMCountingStore struct {
	mu            sync.Mutex
	acquires      int
	consumes      int
	active        map[string]int
	releases      int
	rejectAccount string
}

func (s *t161RPMCountingStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *t161RPMCountingStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *t161RPMCountingStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return map[string]AccountRPMState{}, nil
}

func (s *t161RPMCountingStore) TryAcquireAsync(_ context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]int{}
	}
	s.acquires++
	s.active[accountID]++
	acquires := s.acquires
	s.mu.Unlock()
	laneLimit := concurrencyLimit
	if options.LaneLimit != nil {
		laneLimit = *options.LaneLimit
	}
	return ConcurrencySlot{
		Acquired:        true,
		Current:         1,
		Limit:           concurrencyLimit,
		Lane:            options.Lane,
		LaneCurrent:     1,
		LaneLimit:       laneLimit,
		RPMCurrent:      acquires,
		RPMLimit:        options.RPMLimit,
		RPMResetAtMs:    gatewayupstream.NowMs() + 60_000,
		Release:         func() { s.release(accountID) },
		MarkFirstOutput: func() {},
	}, nil
}

func (s *t161RPMCountingStore) release(accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[accountID]--
	s.releases++
}

func (s *t161RPMCountingStore) ConsumeRPM(_ context.Context, accountID string, rpmLimit int) (bool, int, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumes++
	if accountID == s.rejectAccount {
		return false, rpmLimit, gatewayupstream.NowMs() + 60_000, nil
	}
	return true, s.consumes, gatewayupstream.NowMs() + 60_000, nil
}

func (s *t161RPMCountingStore) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquires + s.consumes
}

func (s *t161RPMCountingStore) counts() (acquires, consumes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquires, s.consumes
}

// countUpstream 返回计数服务器；所有请求按固定状态码失败。
func countUpstream(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestT161RPMConsumeRejectionReleasesSlotAndSkipsCandidate(t *testing.T) {
	failServer, _ := countUpstream(t, http.StatusInternalServerError)
	okServer := sequentialServer(t, 0, http.StatusOK)
	t.Cleanup(okServer.Close)
	engine, driver, _ := newTestEngine(t)
	store := &t161RPMCountingStore{rejectAccount: "a-1"}
	engine.Concurrency = store
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	first := testAccounts("a-1")[0]
	first.RPMLimit = 10
	second := testAccounts("a-2")[0]
	second.RPMLimit = 10
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, []AccountCandidate{first, second})
	args.Settings.SameAccountRetryFastFailMaxMs = 60_000
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("a-1 RPM 消费拒绝后应切换到 a-2：%v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("应选中 a-2，got %s", result.Account.ID)
	}
	if result.Response != nil && result.Response.Body != nil {
		_ = result.Response.Body.Close()
	}
	store.mu.Lock()
	activeFirst, releases := store.active["a-1"], store.releases
	store.mu.Unlock()
	if activeFirst != 0 || releases < 1 {
		t.Fatalf("RPM 消费拒绝必须释放 a-1 并发槽：active=%d releases=%d", activeFirst, releases)
	}
}

func TestT161RPMCountsSameAccountRetries(t *testing.T) {
	server, outbound := countUpstream(t, http.StatusInternalServerError)
	engine, driver, _ := newTestEngine(t)
	store := &t161RPMCountingStore{}
	engine.Concurrency = store
	driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	account := testAccounts("a-1")[0]
	account.RPMLimit = 10
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, []AccountCandidate{account})
	args.Settings.SameAccountRetryFastFailMaxMs = 60_000
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), args); err == nil {
		t.Fatal("同账户重试耗尽后应返回上游失败")
	}
	acquires, consumes := store.counts()
	if got := store.total(); got != int(outbound.Load()) {
		t.Fatalf("RPM 计数 = %d（acquire %d + consume %d），实际出站 = %d", got, acquires, consumes, outbound.Load())
	}
	if acquires != 1 || consumes != 2 || outbound.Load() != 3 {
		t.Fatalf("同账户重试应 1 次占位 + 2 次消费 = 3 次出站：acquire=%d consume=%d outbound=%d", acquires, consumes, outbound.Load())
	}
}

func TestT161RPMCountsKeyRotation(t *testing.T) {
	server, outbound := countUpstream(t, http.StatusInternalServerError)
	engine, driver, _ := newTestEngine(t)
	store := &t161RPMCountingStore{}
	engine.Concurrency = store
	engine.FailureDispatcher = &tryNextKeyDispatcher{}
	driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	account := multiKeyTestAccount("a-1", "key-a", "key-b")
	account.RPMLimit = 10
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, []AccountCandidate{account})
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), args); err == nil {
		t.Fatal("两把 Key 均失败后应返回上游失败")
	}
	acquires, consumes := store.counts()
	if got := store.total(); got != int(outbound.Load()) {
		t.Fatalf("RPM 计数 = %d（acquire %d + consume %d），实际出站 = %d", got, acquires, consumes, outbound.Load())
	}
	if outbound.Load() != 2 || acquires != 1 || consumes != 1 {
		t.Fatalf("Key 轮换应 1 次占位 + 1 次消费 = 2 次出站：outbound=%d acquire=%d consume=%d", outbound.Load(), acquires, consumes)
	}
}

func TestT161RPMCountsOAuthRefreshRetry(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
	t.Cleanup(server.Close)

	dispatcher := &refreshScriptedDispatcher{refreshed: refreshRetryAccount("a-1", "new-access-token")}
	driver := &refreshDriver{}
	driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	engine := newRefreshRetryEngine(driver, dispatcher)
	store := &t161RPMCountingStore{}
	engine.Concurrency = store
	account := refreshRetryAccount("a-1", "old-access-token")
	account.RPMLimit = 10
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, []AccountCandidate{account}))
	if err != nil || !result.Response.OK() {
		t.Fatalf("OAuth 刷新重试应成功：err=%v", err)
	}
	acquires, consumes := store.counts()
	if got := store.total(); got != int(calls.Load()) {
		t.Fatalf("RPM 计数 = %d（acquire %d + consume %d），实际出站 = %d", got, acquires, consumes, calls.Load())
	}
	if calls.Load() != 2 || acquires != 1 || consumes != 1 {
		t.Fatalf("OAuth 刷新应 1 次占位 + 1 次消费 = 2 次出站：出站=%d acquire=%d consume=%d", calls.Load(), acquires, consumes)
	}
}
