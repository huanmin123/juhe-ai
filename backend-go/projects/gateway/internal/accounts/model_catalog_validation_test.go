package accounts

// 模型目录校验域测试（model_catalog_validation.go）：目录命中 / 未命中 /
// 目录异常三条路径，对照归档 account-gpt-request-overrides.validation.ts 与
// account-model-normalization.ts 的断言文案与触发条件逐条标注。目录读取全部
// 通过 fakeAccountModelCatalog 注入（Mock 可回放、结果稳定），不依赖真实
// provider_model_catalog 数据。

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ---- fake catalog port ----

type modelCatalogCall struct {
	providerCode    string
	systemAccountID string
	includeUnpriced bool
}

type fakeAccountModelCatalog struct {
	mu      sync.Mutex
	calls   []modelCatalogCall
	catalog []AccountModelCatalogFact
	err     error
}

func (f *fakeAccountModelCatalog) ListAccountModelCatalog(_ context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]AccountModelCatalogFact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, modelCatalogCall{providerCode: providerCode, systemAccountID: systemAccountID, includeUnpriced: includeUnpriced})
	if f.err != nil {
		return nil, f.err
	}
	return append([]AccountModelCatalogFact{}, f.catalog...), nil
}

func (f *fakeAccountModelCatalog) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeAccountModelCatalog) lastCall() modelCatalogCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return modelCatalogCall{}
	}
	return f.calls[len(f.calls)-1]
}

// gpt-4o-mini 目录事实：支持 priority/flex 服务等级与 low/high 思考级别、
// chat_completions + responses 协议。
func gptCatalogFact() AccountModelCatalogFact {
	return AccountModelCatalogFact{
		Model:                     "gpt-4o-mini",
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		SupportedServiceTiers:     []string{"priority", "flex"},
		SupportedReasoningEfforts: []string{"low", "high"},
	}
}

func overridesInput(providerCode string, credentials Credentials, supportedModels []string) accountGptRequestOverridesInput {
	return accountGptRequestOverridesInput{
		ProviderCode:    providerCode,
		AccountType:     "api_key",
		Credentials:     credentials,
		SupportedModels: supportedModels,
		SystemAccountID: "owner-1",
	}
}

func assertValidationError(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected validation error %q, got nil", message)
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if validation.Message != message {
		t.Fatalf("error copy: got %q, want %q", validation.Message, message)
	}
}

// ---- gpt 请求覆盖目录断言 ----

func TestGptRequestOverridesSkipsCatalogWithoutOverrides(t *testing.T) {
	store := &Store{}
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store.SetModelCatalogReader(fake)
	// 归档 readGptAccountRequestOverrides 早退：无任何覆盖时不发起目录读取。
	if err := store.assertAccountGptRequestOverridesSupported(context.Background(), overridesInput("gpt", Credentials{"api_key": "sk-x"}, []string{"gpt-4o-mini"})); err != nil {
		t.Fatal(err)
	}
	if fake.callCount() != 0 {
		t.Fatalf("catalog must not be queried without overrides, calls = %d", fake.callCount())
	}
	// accountGptRequestOverridesNeedModelCatalog 镜像同一判定。
	if accountGptRequestOverridesNeedModelCatalog(Credentials{"api_key": "sk-x"}) {
		t.Fatal("no overrides must not need the catalog")
	}
	if !accountGptRequestOverridesNeedModelCatalog(Credentials{"reasoning_effort_override": " low "}) {
		t.Fatal("trimmed override values must count")
	}
}

func TestGptRequestOverridesNilPortIsNoOp(t *testing.T) {
	store := &Store{}
	if err := store.assertAccountGptRequestOverridesSupported(context.Background(), overridesInput("gpt", Credentials{"service_tier_override": "priority"}, []string{"gpt-4o-mini"})); err != nil {
		t.Fatalf("nil port must keep the assertion a no-op: %v", err)
	}
}

func TestGptRequestOverridesCatalogHit(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store := &Store{}
	store.SetModelCatalogReader(fake)
	// 支持模型去重（归档 uniqueTextList）后逐条命中目录。
	err := store.assertAccountGptRequestOverridesSupported(context.Background(), overridesInput("gpt",
		Credentials{"service_tier_override": "priority", "reasoning_effort_override": "high"},
		[]string{"gpt-4o-mini", " gpt-4o-mini "}))
	if err != nil {
		t.Fatal(err)
	}
	// 归档 listProviderModelCatalogAsync({includeUnpriced: true})。
	call := fake.lastCall()
	if call.providerCode != "gpt" || call.systemAccountID != "owner-1" || !call.includeUnpriced {
		t.Fatalf("catalog call contract: %+v", call)
	}
	// serviceTier=default 只要求有模型声明服务等级。
	if err := store.assertAccountGptRequestOverridesSupported(context.Background(), overridesInput("gpt",
		Credentials{"service_tier_override": "default"}, []string{"gpt-4o-mini"})); err != nil {
		t.Fatal(err)
	}
}

func TestGptRequestOverridesCatalogMiss(t *testing.T) {
	store := &Store{}
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}})
	ctx := context.Background()
	// 未命中的 tier / effort / 空支持模型集，文案逐字对照归档。
	assertValidationError(t, store.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("gpt", Credentials{"service_tier_override": "priority"}, []string{"gpt-4o"})),
		"所选支持模型中没有模型支持服务等级 priority")
	assertValidationError(t, store.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("gpt", Credentials{"reasoning_effort_override": "xhigh"}, []string{"gpt-4o-mini"})),
		"所选支持模型中没有模型支持思考级别 xhigh")
	assertValidationError(t, store.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("gpt", Credentials{"reasoning_effort_override": "high"}, nil)),
		"请求覆盖要求账户至少配置一个支持模型")
	// 支持模型全部不在目录 → modelItems 为空 → tier 断言失败。
	assertValidationError(t, store.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("gpt", Credentials{"service_tier_override": "priority"}, []string{"gpt-o1"})),
		"所选支持模型中没有模型支持服务等级 priority")
	// default tier 但目录模型未声明任何服务等级。
	bare := gptCatalogFact()
	bare.SupportedServiceTiers = []string{}
	store2 := &Store{}
	store2.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{bare}})
	assertValidationError(t, store2.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("gpt", Credentials{"service_tier_override": "default"}, []string{"gpt-4o-mini"})),
		"所选支持模型中没有模型声明服务等级覆盖")
}

func TestGptRequestOverridesWireMappingAndGeminiGuard(t *testing.T) {
	store := &Store{}
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}})
	ctx := context.Background()
	// 归档顺序：先读目录再断言 wire 映射白名单（白名单在 ByCatalog 内）。
	fake := store.modelCatalog.(*fakeAccountModelCatalog)
	err := store.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("xai", Credentials{"reasoning_effort_override": "high"}, []string{"gpt-4o-mini"}))
	assertValidationError(t, err, "供应商 xai 没有可确认的账户请求覆盖 wire 映射")
	if fake.callCount() != 1 {
		t.Fatalf("wire-mapping check must run after the catalog read, calls = %d", fake.callCount())
	}
	// Interactions-only Gemini 守卫（归档 gemini 分支）。
	gemini := overridesInput("gemini", Credentials{"service_tier_override": "priority"}, []string{"gpt-4o-mini"})
	gemini.Credentials["supported_endpoint_modes"] = []string{"interactions_json", "interactions_sse"}
	assertValidationError(t, store.assertAccountGptRequestOverridesSupported(ctx, gemini),
		"Interactions-only Gemini 账户不能配置 GenerateContent 请求覆盖")
	// 含 generate_content 形态时不触发该守卫（落到 tier 断言并命中）。
	geminiOK := overridesInput("gemini", Credentials{"service_tier_override": "priority", "supported_endpoint_modes": []string{"generate_content_json"}}, []string{"gpt-4o-mini"})
	if err := store.assertAccountGptRequestOverridesSupported(ctx, geminiOK); err != nil {
		t.Fatal(err)
	}
	// endpoint modes 缺失（undefined）不触发守卫。
	if err := store.assertAccountGptRequestOverridesSupported(ctx,
		overridesInput("gemini", Credentials{"service_tier_override": "priority"}, []string{"gpt-4o-mini"})); err != nil {
		t.Fatal(err)
	}
}

func TestGptRequestOverridesCatalogErrorPropagates(t *testing.T) {
	boom := errors.New("catalog unavailable")
	store := &Store{}
	store.SetModelCatalogReader(&fakeAccountModelCatalog{err: boom})
	err := store.assertAccountGptRequestOverridesSupported(context.Background(),
		overridesInput("gpt", Credentials{"service_tier_override": "priority"}, []string{"gpt-4o-mini"}))
	if !errors.Is(err, boom) {
		t.Fatalf("catalog failure must propagate verbatim, got %v", err)
	}
}

// ---- modelMappings 目录校验 ----

func mapping(sourceModel, sourceFamily, upstreamModel, upstreamFamily string) ModelMapping {
	return ModelMapping{SourceModel: sourceModel, SourceEndpointFamily: sourceFamily, UpstreamModel: upstreamModel, UpstreamEndpointFamily: upstreamFamily}
}

func TestModelMappingCatalogHit(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-hit")
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store := env.store
	store.SetModelCatalogReader(fake)
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: "prof-gpt"}
	err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "responses")})
	if err != nil {
		t.Fatal(err)
	}
	// 归档 upstreamModelPoolForAccount 不带 includeUnpriced（默认 false）。
	call := fake.lastCall()
	if call.providerCode != "gpt" || call.systemAccountID != "owner-1" || call.includeUnpriced {
		t.Fatalf("catalog call contract: %+v", call)
	}
	// 空 mapping 集与 hybrid 供应商都不发起目录读取。
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "hybrid", "owner-1", profile,
		[]ModelMapping{mapping("a", "chat_completions", "b", "chat_completions")}); err != nil {
		t.Fatal(err)
	}
	if fake.callCount() != 1 {
		t.Fatalf("empty/hybrid inputs must skip the catalog, calls = %d", fake.callCount())
	}
}

func TestModelMappingCatalogMiss(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-miss")
	store := env.store
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}})
	ctx := context.Background()
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1"}
	// 来源模型不在目录。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-o1", "chat_completions", "gpt-4o-mini", "chat_completions")}),
		"账号模型别名来源模型不在当前供应商模型目录中：gpt-o1")
	// 目标模型不在目录。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-o1", "chat_completions")}),
		"账号模型别名目标模型不在当前供应商模型目录中：gpt-o1")
	// 目标模型不支持对应上游协议。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "messages_json")}),
		"账号模型别名目标模型不支持对应上游协议：gpt-4o-mini")
	// 错误样本只取前 5 个（归档 slice(0, 5).join('、')）。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{
			mapping("m1", "chat_completions", "gpt-4o-mini", "chat_completions"),
			mapping("m2", "chat_completions", "gpt-4o-mini", "chat_completions"),
			mapping("m3", "chat_completions", "gpt-4o-mini", "chat_completions"),
			mapping("m4", "chat_completions", "gpt-4o-mini", "chat_completions"),
			mapping("m5", "chat_completions", "gpt-4o-mini", "chat_completions"),
			mapping("m6", "chat_completions", "gpt-4o-mini", "chat_completions"),
		}),
		"账号模型别名来源模型不在当前供应商模型目录中：m1、m2、m3、m4、m5")
}

func TestModelMappingCatalogNilPortAndError(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-port")
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1"}
	mappings := []ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")}
	// nil 端口 no-op（不触发协议守卫查询）。
	store := env.store
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, mappings); err != nil {
		t.Fatalf("nil port must keep the assertion a no-op: %v", err)
	}
	// 目录异常透传，不静默降级（协议守卫已通过 gpt/openai profile，异常来自目录读取）。
	boom := errors.New("catalog unavailable")
	store.SetModelCatalogReader(&fakeAccountModelCatalog{err: boom})
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, mappings); !errors.Is(err, boom) {
		t.Fatalf("catalog failure must propagate verbatim, got %v", err)
	}
}

// ---- create / patch / batch 挂接冒烟 ----

func newCatalogWiringStore(t *testing.T, ownerID string, fake *fakeAccountModelCatalog) (*Store, AccessScope) {
	t.Helper()
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, ownerID)
	store := env.store
	store.SetModelCatalogReader(fake)
	return store, AccessScope{ViewerID: ownerID, IsAdmin: true}
}

func catalogWiringCreateInput(name string, credentials Credentials, supportedModels []string, mappings []ModelMapping) CreateInput {
	merged := Credentials{"api_key": "sk-live-secret-1234567890", "base_url": "https://api.openai.com/v1"}
	for key, value := range credentials {
		merged[key] = value
	}
	return CreateInput{
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "prof-gpt",
		Name:                      name,
		AccountType:               "api_key",
		Credentials:               merged,
		SupportedModels:           supportedModels,
		ModelMappings:             mappings,
		Status:                    CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
	}
}

func TestCreateWiresGptRequestOverridesAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-catalog-create", fake)
	// 命中：priority 覆盖 + gpt-4o-mini 在目录 → 创建成功且断言查过目录。
	result, err := store.Create(context.Background(), catalogWiringCreateInput("catalog-hit",
		Credentials{"api_key": "sk-live-secret-1234567890", "service_tier_override": "priority"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || fake.callCount() == 0 {
		t.Fatalf("create must run the override assertion: id=%q calls=%d", result.ID, fake.callCount())
	}
	if call := fake.lastCall(); call.systemAccountID != scope.viewerID() {
		t.Fatalf("override assertion must use the request scope: %+v", call)
	}
	// 未命中：支持模型全部不在目录 → modelItems 为空 → tier 断言失败 → 400。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("catalog-miss",
		Credentials{"service_tier_override": "priority"}, []string{"gpt-o1"}, nil), scope)
	assertValidationError(t, err, "所选支持模型中没有模型支持服务等级 priority")
}

func TestCreateWiresModelMappingCatalogAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-catalog-mapping", fake)
	// 目标模型协议不匹配 → 400（归档 create 写侧 normalize 的目录段）。
	_, err := store.Create(context.Background(), catalogWiringCreateInput("mapping-miss",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"},
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "messages_json")}), scope)
	assertValidationError(t, err, "账号模型别名目标模型不支持对应上游协议：gpt-4o-mini")
	// 命中 → 创建成功。
	if _, err := store.Create(context.Background(), catalogWiringCreateInput("mapping-hit",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"},
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "responses")}), scope); err != nil {
		t.Fatal(err)
	}
}

func TestPatchWiresGptRequestOverridesAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-catalog-patch", fake)
	created, err := store.Create(context.Background(), catalogWiringCreateInput("patch-target",
		Credentials{"api_key": "sk-live-secret-1234567890", "service_tier_override": "priority"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	// supportedModels 变化触发断言：新集合仍被目录支持 → 通过（无实际变更时
	// config_revision 不递增，归档/Go 两侧一致）。
	if _, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-4o-mini", "gpt-4o-mini"},
		SupportedModelsPresent: true,
	}, scope); err != nil {
		t.Fatal(err)
	}
	// 把支持模型改成目录外的模型 → tier=priority 无模型支持 → 400。
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-o1"},
		SupportedModelsPresent: true,
	}, scope)
	assertValidationError(t, err, "所选支持模型中没有模型支持服务等级 priority")
}

func TestBatchUpdateWiresGptRequestOverridesAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-catalog-batch", fake)
	first, err := store.Create(context.Background(), catalogWiringCreateInput("batch-a",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(context.Background(), catalogWiringCreateInput("batch-b",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{
			{AccountID: first.ID, ConfigRevision: 1},
			{AccountID: second.ID, ConfigRevision: 1},
		},
		Updates: map[string]BatchUpdateField{
			// reasoning_effort 归一化接受 high（gpt 枚举），目录仅声明 low/high 之外的
			// 值由目录断言拒绝：这里用 xhigh（归档 gpt 枚举也接受）。
			"reasoningEffortOverride": {Enabled: true, Value: "xhigh"},
		},
	}
	// 目录不支持 xhigh → 整批失败（all-or-nothing）。
	_, err = store.BatchUpdate(context.Background(), input, scope)
	assertValidationError(t, err, "所选支持模型中没有模型支持思考级别 xhigh")
	// 目录支持 xhigh → 批量成功。
	catalog := gptCatalogFact()
	catalog.SupportedReasoningEfforts = []string{"low", "high", "xhigh"}
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{catalog}})
	if _, err := store.BatchUpdate(context.Background(), input, scope); err != nil {
		t.Fatal(err)
	}
}
