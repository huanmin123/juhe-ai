// w13g6_margin_test.go lifts the remaining statement coverage of the aipublic
// package past the 95% gate with a durable margin.
//
// 不可达语句归因登记（本文件登记，供覆盖率审计核对）：
//   - accounts.go:276 账号-模型绑定行扫描错误臂：扫描目标均为 *string，SQLite 动态类型下
//     TEXT 扫入 *string 恒成功。
//   - apikeys.go:422 更新后 validation cache 失效失败臂：该错误仅由 apikeys store 的
//     RefreshSecret 与 Delete 写入，Patch 的 outcome 结构不存在该赋值路径。
//   - accounts.go:896 账号更新第 3 次冲突重试臂：ExpectedConfigRevision 每轮由刚读取的
//     basic.ConfigRevision 刷新，单请求内读与写之间无并发写入方，乐观锁冲突不可复现。
//   - target.go:107 目标用户创建后 ID 为空臂：SQLite 驱动 LastInsertId/RowsAffected 正常时
//     ID 恒非空（需驱动层故障注入）。
//   - target.go:405 rand.Read 失败臂：crypto/rand 读取不失败。
package aipublic

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

const w13g6TokenSeed = "juis_token_w13g6margin0margin0margin"

// w13g6Deps 在给定 DB 上重建 Deps（apiKeyStore 非 nil 时替换 API Key store）。
func w13g6Deps(t *testing.T, db *sql.DB, apiKeyStore *apikeys.Store) *Deps {
	t.Helper()
	systemAccounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupsStore, err := groups.NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	strategyStore, err := routestrategies.NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if apiKeyStore == nil {
		apiKeyStore, err = apikeys.NewStore(db, false, "test-crypto-secret", nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	accountStore, err := accounts.NewStore(db, false, "test-crypto-secret", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Deps{
		DB: db, PGDialect: false, Now: time.Now,
		SystemAccounts: systemAccounts, Groups: groupsStore,
		Strategies: strategyStore, ApiKeys: apiKeyStore, AiAccounts: accountStore,
		Sink: &recordingAIPublicSink{},
	}
}

// w13g6Env 基于 newAIPublicEnv 播种全量 scope 令牌与目标用户。
func w13g6Env(t *testing.T) (*aipublicEnv, string) {
	t.Helper()
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "w13g6_src", "w13g6_tok", w13g6TokenSeed)
	return env, token
}

// TestW13g6NotFoundArms 覆盖 update/del 目标不存在时的 404 分支。
func TestW13g6NotFoundArms(t *testing.T) {
	env, token := w13g6Env(t)

	for _, route := range []struct {
		name, method, path, body string
		wantStatus               int
		wantAction               string
	}{
		{"api-key update 404", http.MethodPost, "/__aipublic__/api-key/update",
			`{"targetUsername":"w14downer","apiKeyId":"w13g6-missing","name":"n"}`, 404, ""},
		{"api-key del 404", http.MethodPost, "/__aipublic__/api-key/del",
			`{"targetUsername":"w14downer","apiKeyId":"w13g6-missing"}`, 200, "not_found"},
		{"group update 404", http.MethodPost, "/__aipublic__/group/update",
			`{"targetUsername":"w14downer","groupId":"w13g6-missing","name":"n","providerCode":"gpt"}`, 404, ""},
		{"group del 404", http.MethodPost, "/__aipublic__/group/del",
			`{"targetUsername":"w14downer","groupId":"w13g6-missing"}`, 404, ""},
		{"strategy update 404", http.MethodPost, "/__aipublic__/route-strategy/update",
			`{"targetUsername":"w14downer","routeStrategyId":"w13g6-missing","name":"n"}`, 404, ""},
		{"strategy del 404", http.MethodPost, "/__aipublic__/route-strategy/del",
			`{"targetUsername":"w14downer","routeStrategyId":"w13g6-missing"}`, 404, ""},
		{"account update 404", http.MethodPost, "/__aipublic__/account/update",
			`{"accountId":"w13g6-missing","targetUsername":"w14downer","name":"n"}`, 404, ""},
		{"account del 404", http.MethodPost, "/__aipublic__/account/del",
			`{"accountId":"w13g6-missing","targetUsername":"w14downer"}`, 200, "not_found"},
	} {
		status, payload, _ := env.doAuth(route.method, route.path, route.body, token)
		if status != route.wantStatus {
			t.Fatalf("%s: %d %v", route.name, status, payload)
		}
		if route.wantAction != "" {
			if data, ok := payload["data"].(map[string]any); !ok || data["action"] != route.wantAction {
				t.Fatalf("%s action: %v", route.name, payload)
			}
		}
	}
}

// TestW13g6ValidationArms 覆盖建改流程的入参校验分支。
func TestW13g6ValidationArms(t *testing.T) {
	env, token := w13g6Env(t)

	// account/add 校验失败（accounts.go 315）。
	status, _, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","name":"x"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("account add validation: %d", status)
	}
	// account/update 校验失败（accounts.go 742）。
	status, _, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"w13g6-x","targetUsername":"w14downer","supportedModels":[7]}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("account update validation: %d", status)
	}
	// api-key/add 校验失败：status 枚举非法（apikeys.go 185）。
	status, _, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add",
		`{"targetUsername":"w14downer","name":"x","routeStrategyId":"w13g6-strategy","status":"bogus"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("api-key add validation: %d", status)
	}
	// route-strategy/update 校验失败（strategies.go 698）。
	status, _, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/update",
		`{"targetUsername":"w14downer","routeStrategyId":"w13g6-x","groupBindings":[{"groupId":" "}]}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("strategy update validation: %d", status)
	}
}

// TestW13g6AccountBoundGroupArms 覆盖账号更新响应的绑定分组回退臂。
func TestW13g6AccountBoundGroupArms(t *testing.T) {
	env, token := w13g6Env(t)
	env.seedTargetUser("user_w13g6own", "w13g6own", "active")
	if _, err := env.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_gpt_openai_v1', 'gpt', 'OpenAI v1', 1, 'openai', 'v1', 'https://api.openai.com', 'gpt-4o-mini', '["api_key"]', '{}', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"juhe_ai_public:account_add:write", "juhe_ai_public:account_update:write"}
	sourceToken := env.seedSource("w13g6_src2", "w13g6_tok2", w13g6TokenSeed+"a", "active", "active", scopes, "[]", "", "")

	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w13g6own","targetGroupName":"绑定组","name":"站点B","providerCode":"gpt",
		"providerProtocolProfileId":"profile_gpt_openai_v1","type":"api_key","baseUrl":"https://a.example",
		"apiKey":"sk-b","supportedModels":["gpt-4o-mini"]}`, sourceToken)
	if status != http.StatusCreated {
		t.Fatalf("account add: %d %v", status, payload)
	}
	accountID := payload["data"].(map[string]any)["account"].(map[string]any)["id"].(string)

	// 更新不带 targetGroupName：响应分组字段走 summary.BoundGroup* 回退臂（927/930）。
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"`+accountID+`","targetUsername":"w13g6own","name":"站点B2"}`, sourceToken)
	if status != http.StatusOK {
		t.Fatalf("account update: %d %v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	account, _ := data["account"].(map[string]any)
	if account == nil || account["boundGroupId"] == "" || account["boundGroupName"] != "绑定组" {
		t.Fatalf("bound group overlay: %v", payload)
	}

	// 空绑定组的投影载体（accounts.go 1003-1007）。
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w13g6own","name":"空组","providerCode":"gpt"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("empty group add: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)
	request := httptest.NewRequest(http.MethodPost, "/__aipublic__/account/update", nil)
	deps := w13g6Deps(t, env.db, nil)
	item, err := deps.readAccountGroupItem(request, accounts.AccessScope{}, groupID)
	if err != nil || item == nil || item.BoundGroupID == nil || *item.BoundGroupID != groupID {
		t.Fatalf("empty carrier: %+v %v", item, err)
	}
}

// TestW13g6StrategyBindingArms 覆盖策略绑定汇总的空/显式绑定臂与扫描错误臂。
func TestW13g6StrategyBindingArms(t *testing.T) {
	env, token := w13g6Env(t)

	// 先建绑定策略，再移除绑定行得到"无绑定策略"：绑定汇总的 nil 值兜底臂（strategies.go 231-233）。
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"汇总组","providerCode":"gpt"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("summary group add: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add",
		`{"targetUsername":"w14downer","name":"无绑定策略","groupBindings":[{"groupId":"`+groupID+`"}]}`, token)
	if status != http.StatusCreated {
		t.Fatalf("unbound strategy add: %d %v", status, payload)
	}
	strategyID := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)["id"].(string)
	if _, err := env.db.Exec(`DELETE FROM route_strategy_groups WHERE route_strategy_id = ?`, strategyID); err != nil {
		t.Fatalf("drop bindings: %v", err)
	}
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=w14downer", "", token)
	if status != http.StatusOK {
		t.Fatalf("strategy list: %d %v", status, payload)
	}

	// 显式 bindings 参数覆盖 strategyMutationFrom 的 source 覆盖臂（452-454）。
	mutation := strategyMutationFrom(&strategyAddBody{Name: "w13g6"}, []strategyBindingInput{{GroupID: "w13g6-g"}})
	if len(mutation.Bindings) != 1 || mutation.Bindings[0].GroupID != "w13g6-g" {
		t.Fatalf("explicit bindings: %+v", mutation.Bindings)
	}

	// 绑定行 priority 列破坏 -> 汇总查询扫描错误臂（220-222）。
	ownerID := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'w14downer'`).Scan(&ownerID); err != nil {
		t.Fatalf("owner lookup: %v", err)
	}
	if _, err := env.db.Exec(`INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('w13g6-broken', ?, '坏行策略', 'normal', 'active', 0, datetime('now'), datetime('now'))`, ownerID); err != nil {
		t.Fatalf("seed broken strategy: %v", err)
	}
	if _, err := env.db.Exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('w13g6-rsg-broken', 'w13g6-broken', ?, ?, 'abc', 100, 'active', datetime('now'), datetime('now'))`, ownerID, groupID); err != nil {
		t.Fatalf("seed broken binding: %v", err)
	}
	status, _, _ = env.doAuth(http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=w14downer", "", token)
	if status != http.StatusInternalServerError {
		t.Fatalf("broken binding scan: %d", status)
	}
}

// ---------------------------------------------------------------------------
// validation cache 失效失败臂
// ---------------------------------------------------------------------------

type w13g6FailingCacheInvalidator struct{}

func (w13g6FailingCacheInvalidator) InvalidateValidation(string, string, []string) error {
	return assertErr("w13g6 validation cache failure")
}
func (w13g6FailingCacheInvalidator) InvalidateQuota(string, string)      {}
func (w13g6FailingCacheInvalidator) InvalidateRuntime(string, string)    {}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// TestW13g6ValidationCacheErrorArms 覆盖 API Key 更新/删除后 validation cache
// 失效失败的 500 臂（apikeys.go 422-425 / 501-504）。
func TestW13g6ValidationCacheErrorArms(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "w13g6_srcc", "w13g6_tokc", w13g6TokenSeed+"c")

	failingStore, err := apikeys.NewStore(env.db, false, "test-crypto-secret", nil, nil, w13g6FailingCacheInvalidator{})
	if err != nil {
		t.Fatal(err)
	}
	k := kernelForTest()
	deps := w13g6Deps(t, env.db, failingStore)
	deps.Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)

	// 先经原环境播种一枚 API Key（需要合法绑定策略）。
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"缓存组","providerCode":"gpt"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("cache group add: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)
	strategyID := seedStrategyForTest(t, env.db, "user_w14downer", groupID)
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add",
		`{"targetUsername":"w14downer","name":"缓存键","routeStrategyId":"`+strategyID+`"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("api-key add: %d %v", status, payload)
	}
	apiKeyID := payload["data"].(map[string]any)["apiKey"].(map[string]any)["id"].(string)

	// 删除后 validation cache 失效失败（501-504）。更新臂 422 不可达：
	// Patch 的 outcome 从不设置 ValidationCacheError（仅 RefreshSecret/Delete 设置），
	// 已在本文件头部登记。
	if status, payload := w13g6DoAuth(t, server, http.MethodPost, "/__aipublic__/api-key/del",
		`{"targetUsername":"w14downer","apiKeyId":"`+apiKeyID+`"}`, token); status != http.StatusInternalServerError {
		t.Fatalf("api-key del cache error: %d %v", status, payload)
	}
}

// w13g6DoAuth 向指定 server 发送带 Bearer 令牌的请求。
func w13g6DoAuth(t *testing.T, server *httptest.Server, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	if payload == nil {
		payload = map[string]any{}
	}
	return response.StatusCode, payload
}

// ---------------------------------------------------------------------------
// closed-DB 存储臂与纯函数
// ---------------------------------------------------------------------------

// TestW13g6ClosedStoreArms 覆盖 closed DB 下的账号/分组/策略/目标用户错误臂。
func TestW13g6ClosedStoreArms(t *testing.T) {
	closed, err := sql.Open("sqlite", "file:w13g6-closed-aipublic?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	groupsStore, err := groups.NewStore(closed, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	strategyStore, err := routestrategies.NewStore(closed, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := accounts.NewStore(closed, false, "test-crypto-secret", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	systemAccounts, err := authsys.NewAccountStore(closed, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{
		DB: closed, PGDialect: false, Now: time.Now,
		SystemAccounts: systemAccounts, Groups: groupsStore,
		Strategies: strategyStore, ApiKeys: nil, AiAccounts: accountStore,
	}
	ctx := context.Background()

	// 账号重复检测 / 读取的错误臂（accounts.go 555/570）。
	if duplicate := deps.findDuplicateAccount(ctx, "w13g6-owner", "gpt", "p", "", "n"); duplicate {
		t.Fatal("closed duplicate check must report false")
	}
	if item := deps.readAccountItem(httptest.NewRequest(http.MethodPost, "/x", nil), "w13g6-a", "w13g6-owner"); item != nil {
		t.Fatalf("closed readAccountItem must be nil: %+v", item)
	}

	// 目标用户保障的错误臂（target.go 88-90）。
	if _, err := deps.ensureTargetSystemAccount(ctx, "w13g6-user", nil); err == nil {
		t.Fatal("closed ensureTargetSystemAccount must fail")
	}
	// 目标分组保障的错误臂（target.go 187-205）。
	if _, _, err := deps.ensureTargetGroup(ctx, "w13g6-owner", "gpt", "w13g6-group"); err == nil {
		t.Fatal("closed ensureTargetGroup must fail")
	}
	// 策略绑定汇总查询错误臂（strategies.go 212-214）。
	if _, err := deps.loadStrategyBindings(ctx, []string{"w13g6-rs"}); err == nil {
		t.Fatal("closed binding summaries must fail")
	}
}

// TestW13g6PureHelpers 覆盖 bearer/zod/日志/限流的纯函数分支。
func TestW13g6PureHelpers(t *testing.T) {
	// bearerToken：非 Bearer 头与空 token 臂（aipublic.go 154-156）。
	basic := httptest.NewRequest(http.MethodGet, "/", nil)
	basic.Header.Set("Authorization", "Basic abc")
	if bearerToken(basic) != "" {
		t.Fatal("non-bearer header must be empty")
	}
	blankBearer := httptest.NewRequest(http.MethodGet, "/", nil)
	blankBearer.Header.Set("Authorization", "Bearer   ")
	if bearerToken(blankBearer) != "" {
		t.Fatal("blank bearer token must be empty")
	}
	// requiredTrimmedBody 缺键臂（zod.go 319-321）。
	if _, issue := requiredTrimmedBody(map[string]any{}, "name", 1, 5); issue != zodRequired {
		t.Fatalf("missing key issue: %q", issue)
	}
	// scalarText / marshalRawMessage 的序列化失败臂（operationlog.go 120/129）。
	if scalarText(make(chan int)) != "" {
		t.Fatal("scalarText marshal error arm")
	}
	if marshalRawMessage(map[string]any{"x": make(chan int)}) != nil {
		t.Fatal("marshalRawMessage error arm")
	}
	// 限位器裁剪：最旧条目处于惩罚封禁期时跳过删除（ratelimit.go 336-337）。
	limiter := NewPenaltyWindowLimiter(func() time.Time { return time.UnixMilli(10_000) })
	limiter.maxEntries = 1
	limiter.entries["w13g6-blocked"] = &penaltyEntry{
		lastSeenAtMs:   100,
		blockedUntilMs: 20_000,
		hasBlock:       true,
	}
	limiter.entries["w13g6-fresh"] = &penaltyEntry{lastSeenAtMs: 200}
	limiter.trim(10_000)
	if _, exists := limiter.entries["w13g6-blocked"]; !exists {
		t.Fatal("blocked entry must survive the trim")
	}
	if _, exists := limiter.entries["w13g6-fresh"]; exists {
		t.Fatal("fresh entry must be trimmed")
	}
	_ = json.Marshal
}
