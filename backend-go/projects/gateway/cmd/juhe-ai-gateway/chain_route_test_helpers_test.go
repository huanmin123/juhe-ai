package main

// Shared request-chain fixtures for the super-group and cross-cutting chain
// tests (SQLite chainFixture baseline, spool polling, audit capture, resolver
// quota stub).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

const chainRouteTestNow = "2026-09-04T00:00:00.000Z"

// w1FreshCacheFor 在凭据更新后重建运行时缓存（缓存快照在构造时生成）。
func w1FreshCacheFor(t *testing.T, fixture *chainFixture) *gatewayruntimecache.Service {
	t.Helper()
	models, err := gatewayruntimecache.NewSQLReadModels(fixture.db, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("read models = %v", err)
	}
	selector, err := newChainAccountsSelectorWithStats(fixture.db, fixture.statsDB, false, "chain-test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("selector = %v", err)
	}
	models.SetAccountsSelector(selector)
	catalogSource, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("catalog source = %v", err)
	}
	models.SetCatalogSource(catalogSource)
	models.SetConcurrencySource(gatewayclientip.NewMemoryAccountConcurrency(nil))
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{Clock: gatewayruntimecache.SystemClock()})
	if err != nil {
		t.Fatalf("runtime cache = %v", err)
	}
	t.Cleanup(cache.Close)
	return cache
}

// pointAccountAt 把账号指向 mock 上游并重建运行时缓存（缓存快照在构造时生成）。
func pointAccountAt(t *testing.T, fixture *chainFixture, accountID, baseURL string) {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		w1vEncrypt(t, map[string]any{"api_key": "sk-upstream", "base_url": baseURL}), accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	// B4 第一批单连接事实源：上游指向同步改连接行。
	chainPointAccountAtUpstream(t, fixture.db, accountID, baseURL)
	fixture.cache = w1FreshCacheFor(t, fixture)
}

// waitForSpoolRecordsUntil 轮询 spool 目录直到条件满足，返回满足时刻的
// 全部记录；超时返回最后一次快照（由断言侧按缺行报错）。per-attempt 失败行
// 经 spooledUsageRecorder 异步 drain 逐条落盘，按"任意 N 条"等待会拿到只有
// 首账号的截断快照，断言侧对后发账号报空值；等待条件必须表达断言真正需要
// 的记录在场。
func waitForSpoolRecordsUntil(t *testing.T, dir string, satisfied func(records []gatewayusage.UsageRecordInput) bool) []gatewayusage.UsageRecordInput {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		records := spoolRecords(dir)
		if satisfied(records) {
			return records
		}
		time.Sleep(25 * time.Millisecond)
	}
	return spoolRecords(dir)
}

func spoolRecords(dir string) []gatewayusage.UsageRecordInput {
	var records []gatewayusage.UsageRecordInput
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var record gatewayusage.UsageRecordInput
		if json.Unmarshal(data, &record) == nil {
			records = append(records, record)
		}
		return nil
	})
	return records
}

// composeChainWithSpool 组装真实链并返回 spool 目录；mutate 供注入
// LatencyService / 审计派发器等。
func composeChainWithSpool(t *testing.T, fixture *chainFixture, mutate func(*chainRuntimeDeps)) (*gatewayChain, string, func()) {
	t.Helper()
	spoolRoot, err := os.MkdirTemp("", "chain-spool-")
	if err != nil {
		t.Fatalf("create spool root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(spoolRoot) })
	spoolDir := filepath.Join(spoolRoot, "spool")
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir)
	if mutate != nil {
		mutate(&deps)
	}
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	return chain, spoolDir, shutdown
}

// stubQuotaChecker 是解析期配额批查的测试桩。
type stubQuotaChecker struct {
	denied    map[string]bool
	deniedAll bool
	calls     int
}

func (q *stubQuotaChecker) CheckBatchAsync(_ context.Context, _ gatewayruntimecache.GroupUsageAccessMetadata, accounts []gatewaydispatch.AccountCandidate) (map[string]gatewaydispatch.QuotaDecision, error) {
	q.calls++
	decisions := make(map[string]gatewaydispatch.QuotaDecision, len(accounts))
	for _, account := range accounts {
		decisions[account.ID] = gatewaydispatch.QuotaDecision{Allowed: !q.deniedAll && !q.denied[account.ID]}
	}
	return decisions, nil
}

// capturingAuditDispatcher 捕获最终化审计日志。审计派发发生在响应返回之后
// 的异步 finalize goroutine，与测试轮询读取并发：logs 必须由互斥锁保护，
// 否则读者可能观察到 len 已更新而元素字段尚未可见的撕裂状态（GroupID 读成
// 空串），全量负载下偶发断言失败。
type capturingAuditDispatcher struct {
	mu   sync.Mutex
	logs []gatewayusage.AuditLogInput
}

func (d *capturingAuditDispatcher) DispatchAuditLog(_ gatewayusage.Ctx, input gatewayusage.AuditLogInput) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logs = append(d.logs, input)
}

func (d *capturingAuditDispatcher) snapshot() []gatewayusage.AuditLogInput {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]gatewayusage.AuditLogInput(nil), d.logs...)
}

func routeRequest(model string) *gatewaypreauth.GatewayRequest {
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	request.Body = &gatewaybody.Request{
		RawBody: []byte(`{"model":"` + model + `"}`),
		Body:    map[string]any{"model": model},
		State:   &gatewaybody.BodyState{Model: &model},
	}
	return request
}

func accountSecretIDs(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}
