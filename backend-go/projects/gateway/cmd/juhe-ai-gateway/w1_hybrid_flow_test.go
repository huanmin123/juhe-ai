package main

// w1（单元层，本地 mock 上游）：hybrid 目标组选择与辅助派发全链——
// 路由缓存选组（含各缺席守卫）、辅助 chat 派发的成功 / 上游非 200 /
// 传输错误三臂。上游用本地 httptest 造数据，无真实外部依赖。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
)

func TestW1HybridTargetGroupsSelection(t *testing.T) {
	fixture := newChainFixture(t)
	// nil 缓存 / 空 SelectedGroupID：nil 语义。
	if selection, err := (hybridTargetGroups{}).SelectTargetGroup(context.Background(), gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SelectedGroupID: fixture.groupID},
	}); err != nil || selection != nil {
		t.Fatalf("nil cache = %+v, %v", selection, err)
	}
	selector := hybridTargetGroups{cache: fixture.cache}
	if selection, err := selector.SelectTargetGroup(context.Background(), gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{SystemAccountID: fixture.systemAccount},
	}); err != nil || selection != nil {
		t.Fatalf("empty group = %+v, %v", selection, err)
	}
	// 命中：分组 + 组内账户（fixture 预置 group_main + acc_1）。
	selection, err := selector.SelectTargetGroup(context.Background(), gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord: gatewayhybrid.APIKeyRecord{
			SystemAccountID: fixture.systemAccount, SelectedGroupID: fixture.groupID,
		},
		TargetModel: "gpt-test",
	})
	if err != nil || selection == nil {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
	if selection.GroupID != fixture.groupID || len(selection.Accounts) == 0 {
		t.Fatalf("selection = %+v", selection)
	}
}

func w1PointAccountAt(t *testing.T, fixture *chainFixture, baseURL string) {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": baseURL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
}

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

func TestW1HybridAuxiliaryDispatchArms(t *testing.T) {
	fixture := newChainFixture(t)
	dispatcher := newChainHybridAuxiliaryDispatcher(fixture.cache)
	makeInput := func() gatewayhybrid.AuxiliaryDispatchInput {
		return gatewayhybrid.AuxiliaryDispatchInput{
			RawBody:                 []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"打分"}]}`),
			APIKeyRecord:            gatewayhybrid.APIKeyRecord{SystemAccountID: fixture.systemAccount, SelectedGroupID: fixture.groupID},
			TargetModel:             "gpt-test",
			TimeoutMs:               5000,
			ResponseMaxBytes:        65536,
			NoAccountErrorCode:      "aux_no_account",
			NoAccountErrorMessage:   "辅助派发无账户",
			DispatchErrorCode:       "aux_dispatch_failed",
			DispatchErrorMessage:    "辅助派发失败",
			HTTPErrorCode:           "aux_http_error",
			ResponseTooLargeMessage: "辅助响应过大",
		}
	}
	// 成功臂：mock 上游返回 chat completion。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"aux-1","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"辅助完成"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	w1PointAccountAt(t, fixture, upstream.URL)
	success, failure := dispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), makeInput())
	if failure != nil || success.Account.ID == "" {
		t.Fatalf("success = %+v, failure = %+v", success, failure)
	}
	if success.GroupID != fixture.groupID || success.StatusCode != http.StatusOK || !strings.Contains(success.ResponseBodyText, "辅助完成") {
		t.Fatalf("success = %+v", success)
	}
	if success.Finish == nil {
		t.Fatal("成功臂必须携带 Finish")
	}
	if err := success.Finish(context.Background(), gatewayhybrid.AuxiliaryDispatchFinishInput{}); err != nil {
		t.Fatalf("finish = %v", err)
	}
	// 上游非 200：HTTP 错误臂（fallbackErrorCode 生效）。缓存按构造时快照
	// 水合账户，改写凭据后必须重建缓存才能看到新 base_url。
	badUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"过载"}}`))
	}))
	w1PointAccountAt(t, fixture, badUpstream.URL)
	dispatcher = newChainHybridAuxiliaryDispatcher(w1FreshCacheFor(t, fixture))
	_, failure = dispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), makeInput())
	if failure == nil || failure.ErrorCode != "aux_http_error" || failure.StatusCode != http.StatusServiceUnavailable || !failure.ShouldRecordUsage {
		t.Fatalf("http failure = %+v", failure)
	}
	// 传输错误臂：上游已关闭 → 连接失败。
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	w1PointAccountAt(t, fixture, closedURL)
	dispatcher = newChainHybridAuxiliaryDispatcher(w1FreshCacheFor(t, fixture))
	_, failure = dispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), makeInput())
	if failure == nil || failure.ErrorCode != "aux_dispatch_failed" || failure.Account == nil || !failure.ShouldRecordUsage {
		t.Fatalf("transport failure = %+v", failure)
	}
}
