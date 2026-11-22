package main

// 演练场 cmd 适配器直接用例（§3.2/§3.7）：
//   - playgroundKeyDispatchAdapter：归属 fail-closed 负例（跨属主 / Key 不存在 /
//     缺解密载体）、解密失败 502 通用文案（m2 收敛）与 Bearer 注入 full-chain
//     断言（注入头经 /v1 鉴权后才可能命中 mock 上游）；
//   - playgroundKeyModelsAdapter：runtime 归属复核 404（跨属主 / 停用 Key）、
//     空绑定空 models 与提供账户能力域复核（账户域改 embedding 后 chat 行消失）。
// 装载复用 newChainFixture（pgtestkit 授权链路；外部依赖未授权时自动 SKIP，
// 与 w1 全链用例同一门）。

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/playground"
)

// seedPlaygroundAPIKeyRow 直插一条演练场派发用 Key 行（可选密文载体）。
func seedPlaygroundAPIKeyRow(t *testing.T, fixture *chainFixture, id, secret string, withEncrypted bool) {
	t.Helper()
	var encrypted string
	if withEncrypted {
		payload, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"key": secret})
		if err != nil {
			t.Fatalf("encrypt key secret: %v", err)
		}
		encrypted = payload
	}
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO api_keys
			(id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, key_prefix, key_suffix, key_secret_encrypted, created_at, updated_at)
		VALUES (?, ?, 'rs_1', '演练场适配器', ?, 'active', 'schpref-builtin-chat-cost-first', ?, ?, ?, ?, ?)`),
		id, fixture.systemAccount, gatewayruntimecache.HashSecret(secret), secret[:3], secret[len(secret)-4:], nullableText(encrypted), "2026-09-04T00:00:00.000Z", "2026-09-04T00:00:00.000Z"); err != nil {
		t.Fatalf("seed playground api key row: %v", err)
	}
}

// nullableText 把空串折叠为 SQL NULL（key_secret_encrypted 缺载体用例）。
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func TestPlaygroundKeyDispatchAdapter(t *testing.T) {
	fixture := newChainFixture(t)
	store, err := apikeys.NewStore(fixture.db, "chain-test-secret", time.Now, newCompositionID, apikeys.BusInvalidator{})
	if err != nil {
		t.Fatalf("create apikeys store: %v", err)
	}
	// 夹具内置 key_1（key_hash 有、密文载体无）；补两条演练场专用行。
	seedPlaygroundAPIKeyRow(t, fixture, "key_nosecret", "sk-nosecret", false)
	seedPlaygroundAPIKeyRow(t, fixture, "key_pg", "sk-playground-key", true)

	adapter := playgroundKeyDispatchAdapter{keys: store}

	// 归属 fail-closed：他人（含 admin 语义缺席）请求他人 Key → 404，不泄露
	// 存在性；Key 不存在同码。
	if _, err := adapter.Dispatch(context.Background(), playground.DispatchRequest{APIKeyID: "key_pg", OwnerID: "sys_other"}); !isPlaygroundKeyNotFound(t, err) {
		t.Fatalf("跨属主派发应 404 playground_key_not_found: %v", err)
	}
	if _, err := adapter.Dispatch(context.Background(), playground.DispatchRequest{APIKeyID: "key_missing", OwnerID: "sys_owner"}); !isPlaygroundKeyNotFound(t, err) {
		t.Fatalf("Key 不存在应 404 playground_key_not_found: %v", err)
	}

	// 解密失败（密文载体缺失 → SecretUnavailableError）：502 通用文案，不透
	// 底层细节（m2 收敛口径）。
	if _, err := adapter.Dispatch(context.Background(), playground.DispatchRequest{APIKeyID: "key_nosecret", OwnerID: "sys_owner"}); err == nil {
		t.Fatal("缺解密载体应派发失败")
	} else {
		httpErr := playgroundHTTPErrorOf(t, err)
		if httpErr == nil || httpErr.Status != http.StatusBadGateway || httpErr.Code != "playground_dispatch_failed" || httpErr.Message != "演练场派发失败，请稍后重试" {
			t.Fatalf("解密失败应收敛 502 通用文案: %+v（err %v）", httpErr, err)
		}
	}

	// Bearer 注入 + full-chain：注入的 Authorization 经 /v1 鉴权（key_hash 比对
	// key_pg 的明文）后才可能命中 mock 上游——200 即注入头真实生效。
	upstream := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.server.URL)
	chain, _ := composeChatDispatchChain(t, fixture)
	adapter.executor = newChatGatewayExecutor(chain)
	response, err := adapter.Dispatch(context.Background(), playground.DispatchRequest{
		APIKeyID:   "key_pg",
		OwnerID:    "sys_owner",
		RemoteAddr: "203.0.113.9:47800",
		Path:       "/v1/chat/completions",
		Method:     http.MethodPost,
		Headers: map[string]string{
			"content-type": "application/json",
			"accept":       "application/json",
		},
		Body: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if response == nil || response.Status != http.StatusOK {
		status := 0
		if response != nil {
			status = response.Status
		}
		t.Fatalf("Bearer 注入派发应 200: status=%d err=%v", status, err)
	}
	if upstream.hits != 1 {
		t.Fatalf("上游命中数 = %d，期望 1", upstream.hits)
	}
	// Bearer 校验反向钉测：同一行换错误明文（改行密文）后派发必须被 /v1 鉴权
	// 拒绝，证明上游命中来自注入头而非匿名放行。
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET key_secret_encrypted = ? WHERE id = 'key_pg'`),
		mustEncryptPlaygroundSecret(t, "sk-rotated-wrong")); err != nil {
		t.Fatalf("rotate key secret: %v", err)
	}
	wrongBearer, err := adapter.Dispatch(context.Background(), playground.DispatchRequest{
		APIKeyID: "key_pg", OwnerID: "sys_owner", RemoteAddr: "203.0.113.9:47800",
		Path: "/v1/chat/completions", Method: http.MethodPost,
		Headers: map[string]string{"content-type": "application/json"},
		Body:    []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`),
	})
	if err != nil {
		t.Fatalf("wrong-bearer dispatch: %v", err)
	}
	if wrongBearer == nil || wrongBearer.Status == http.StatusOK {
		status := 0
		if wrongBearer != nil {
			status = wrongBearer.Status
		}
		t.Fatalf("错误 Bearer 不应通过 /v1 鉴权: status=%d", status)
	}
}

func isPlaygroundKeyNotFound(t *testing.T, err error) bool {
	t.Helper()
	httpErr := playgroundHTTPErrorOf(t, err)
	return httpErr != nil && httpErr.Status == http.StatusNotFound && httpErr.Code == "playground_key_not_found"
}

func playgroundHTTPErrorOf(t *testing.T, err error) *playground.HTTPError {
	t.Helper()
	var httpErr *playground.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr
	}
	return nil
}

func mustEncryptPlaygroundSecret(t *testing.T, secret string) string {
	t.Helper()
	payload, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"key": secret})
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	return payload
}

func TestPlaygroundKeyModelsAdapter(t *testing.T) {
	fixture := newChainFixture(t)
	keyStore, err := apikeys.NewStore(fixture.db, "chain-test-secret", time.Now, newCompositionID, apikeys.BusInvalidator{})
	if err != nil {
		t.Fatalf("create apikeys store: %v", err)
	}
	adapter := playgroundKeyModelsAdapter{cache: fixture.cache, keys: keyStore}
	ctx := context.Background()

	// 基线：夹具账户（chat 域，清单含 gpt-test）× 目录行（chat_completions）
	// → 单行 {gpt-test, chat, openai}。
	rows, httpErr := adapter.ListKeyModels(ctx, "sys_owner", "key_1")
	if httpErr != nil {
		t.Fatalf("baseline ListKeyModels: %+v", httpErr)
	}
	if len(rows) != 1 || rows[0].ModelID != "gpt-test" || rows[0].Kind != playground.KindChat {
		t.Fatalf("基线候选不符: %+v", rows)
	}

	// 归属复核 fail-closed：跨属主与 Key 不存在 → 404。
	if _, httpErr := adapter.ListKeyModels(ctx, "sys_other", "key_1"); httpErr == nil || httpErr.Status != http.StatusNotFound || httpErr.Code != "playground_key_not_found" {
		t.Fatalf("跨属主候选应 404 playground_key_not_found: %+v", httpErr)
	}
	if _, httpErr := adapter.ListKeyModels(ctx, "sys_owner", "key_missing"); httpErr == nil || httpErr.Status != http.StatusNotFound || httpErr.Code != "playground_key_not_found" {
		t.Fatalf("Key 不存在候选应 404 playground_key_not_found: %+v", httpErr)
	}

	// 停用 Key：runtime 读取层按 status 门收空行 → 404（不泄露存在性）。
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET status = 'disabled' WHERE id = 'key_1'`)); err != nil {
		t.Fatalf("disable key: %v", err)
	}
	fixture.cache.ClearGatewayRuntimeCacheLocal(gatewayruntimecache.ClearOptions{})
	if _, httpErr := adapter.ListKeyModels(ctx, "sys_owner", "key_1"); httpErr == nil || httpErr.Status != http.StatusNotFound {
		t.Fatalf("停用 Key 候选应 404: %+v", httpErr)
	}
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET status = 'active' WHERE id = 'key_1'`)); err != nil {
		t.Fatalf("re-enable key: %v", err)
	}

	// 空绑定：Key 路由策略不再绑定分组 → 200 空 models（不回退匿名全量）。
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE route_strategy_groups SET status = 'disabled' WHERE id = 'rsg_1'`)); err != nil {
		t.Fatalf("unbind group: %v", err)
	}
	fixture.cache.ClearGatewayRuntimeCacheLocal(gatewayruntimecache.ClearOptions{})
	rows, httpErr = adapter.ListKeyModels(ctx, "sys_owner", "key_1")
	if httpErr != nil {
		t.Fatalf("empty-binding ListKeyModels: %+v", httpErr)
	}
	if len(rows) != 0 {
		t.Fatalf("空绑定应返回空候选: %+v", rows)
	}
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE route_strategy_groups SET status = 'active' WHERE id = 'rsg_1'`)); err != nil {
		t.Fatalf("rebind group: %v", err)
	}

	// 提供账户能力域复核：账户域改 embedding 后，chat 目录行失去 chat 域提供
	// 账户 → 空候选（纯目录分类会被复核剔除）。
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE accounts SET capability_domain = 'embedding' WHERE id = ?`), fixture.accountID); err != nil {
		t.Fatalf("flip account domain: %v", err)
	}
	fixture.cache.ClearGatewayRuntimeCacheLocal(gatewayruntimecache.ClearOptions{})
	rows, httpErr = adapter.ListKeyModels(ctx, "sys_owner", "key_1")
	if httpErr != nil {
		t.Fatalf("review ListKeyModels: %+v", httpErr)
	}
	if len(rows) != 0 {
		t.Fatalf("无 chat 域提供账户时 chat 行应被复核剔除: %+v", rows)
	}
}
