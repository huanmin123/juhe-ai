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
	"time"
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
	// 非 hybrid 断言前置协议矩阵门（归档 assertAccountModelMappingProtocolAllowed）：
	// openai 档案下 responses 上游要求 native Responses，端点能力缺失先行拒绝；
	// 目录命中段改用同协议 chat_completions 映射验证。
	err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")},
		[]string{"chat_json", "chat_sse"})
	if err != nil {
		t.Fatal(err)
	}
	// 归档 upstreamModelPoolForAccount 不带 includeUnpriced（默认 false）。
	call := fake.lastCall()
	if call.providerCode != "gpt" || call.systemAccountID != "owner-1" || call.includeUnpriced {
		t.Fatalf("catalog call contract: %+v", call)
	}
	// 空 mapping 集不发起目录读取；hybrid 供应商走协议池校验（nil 端口 no-op，
	// 端点能力满足 hybrid 矩阵门禁后才会触达协议池段）。
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, nil, nil); err != nil {
		t.Fatal(err)
	}
	// hybrid 供应商走协议池校验（verdict-ao 登记的跳过段，另一代理已落地）：
	// 池 = 该协议全部启用供应商的目录并集按端点族过滤 → 模型须落在池内。
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "hybrid", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")},
		[]string{"chat_json", "chat_sse"}); err != nil {
		t.Fatal(err)
	}
	if fake.callCount() != 2 {
		t.Fatalf("hybrid pool check must query the catalog once more, calls = %d", fake.callCount())
	}
}

func TestModelMappingCatalogMiss(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-miss")
	store := env.store
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}})
	ctx := context.Background()
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1"}
	// 上游端点能力满足后目录段才会执行（矩阵门禁先于目录校验）。
	modes := []string{"chat_json", "chat_sse"}
	// 来源模型不在目录。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-o1", "chat_completions", "gpt-4o-mini", "chat_completions")}, modes),
		"账号模型别名来源模型不在当前供应商模型目录中：gpt-o1")
	// 目标模型不在目录。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-o1", "chat_completions")}, modes),
		"账号模型别名目标模型不在当前供应商模型目录中：gpt-o1")
	// 目标模型不支持对应上游协议：目录只声明 responses 协议，chat_completions
	// 的端点族池不含 gpt-4o-mini。
	missProtocolStore := env.store
	missProtocolStore.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		{Model: "gpt-4o-mini", SupportedAPIProtocols: []string{"responses"}},
	}})
	assertValidationError(t, missProtocolStore.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")}, modes),
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
		}, modes),
		"账号模型别名来源模型不在当前供应商模型目录中：m1、m2、m3、m4、m5")
}

func TestModelMappingCatalogNilPortAndError(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-port")
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1"}
	mappings := []ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")}
	// 上游端点能力满足矩阵门禁后，nil 目录端口只让目录段保持 no-op。
	modes := []string{"chat_json", "chat_sse"}
	// nil 端口 no-op（不触发协议守卫查询）。
	store := env.store
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, mappings, modes); err != nil {
		t.Fatalf("nil port must keep the assertion a no-op: %v", err)
	}
	// 目录异常透传，不静默降级（协议守卫已通过 gpt/openai profile，异常来自目录读取）。
	boom := errors.New("catalog unavailable")
	store.SetModelCatalogReader(&fakeAccountModelCatalog{err: boom})
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, mappings, modes); !errors.Is(err, boom) {
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
	// 矩阵门禁先于目录段：openai 档案不支持 messages 源 → 400（归档
	// assertAccountModelMappingProtocolAllowed 文案）。
	_, err := store.Create(context.Background(), catalogWiringCreateInput("mapping-gate",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"},
		[]ModelMapping{mapping("gpt-4o-mini", "messages", "gpt-4o-mini", "messages")}), scope)
	assertValidationError(t, err, "当前供应商协议不支持 Anthropic Messages 账号模型别名")
	// 跨协议 chat_completions → responses 需要 native Responses 上游，openai
	// 档案白名单无该配对 → 400（归档 unsupportedProtocolConversionMessage）。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("mapping-capability",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"},
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "responses")}), scope)
	assertValidationError(t, err, "账号模型别名只支持同协议映射；跨协议 Chat Completions 到 Responses 请改用混合供应商账户")
	// 同协议映射 + chat 端点能力 → 创建成功。
	if _, err := store.Create(context.Background(), catalogWiringCreateInput("mapping-hit",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"},
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")}), scope); err != nil {
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
	// 把支持模型改成目录外的模型 → 支持模型目录校验（patch 链
	// normalizeAccountSupportedModelsForProviderAsync 目录段，先于 tier 断言）
	// → 400，文案逐字对照归档。
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-o1"},
		SupportedModelsPresent: true,
	}, scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：gpt-o1")
	// 目录内但未声明任何服务等级的模型 → 目录段通过后 tier 断言生效。
	noTierFake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{{
		Model:                     "gpt-4o",
		SupportedAPIProtocols:     []string{"chat_completions", "responses"},
		SupportedServiceTiers:     []string{},
		SupportedReasoningEfforts: []string{},
	}}}
	store.SetModelCatalogReader(noTierFake)
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-4o"},
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

// ---- 支持模型目录校验挂接（normalizeAccountSupportedModelsForProvider 家族） ----

func TestCreateWiresSupportedModelsCatalogAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	// 本测试后半段需要直写 providers/groups 种子（openai-compatible 供应商的
	// 协议档案过滤），直接持有 env 而不是 newCatalogWiringStore 包装。
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-supported-create")
	store := env.store
	store.SetModelCatalogReader(fake)
	scope := AccessScope{ViewerID: "owner-supported-create", IsAdmin: true}
	// 目录内 → 创建成功，目录读取带 includeUnpriced=true（归档 :48）。
	created, err := store.Create(context.Background(), catalogWiringCreateInput("supported-hit",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("created id must not be empty")
	}
	last := fake.lastCall()
	if !last.includeUnpriced || last.providerCode != "gpt" || last.systemAccountID != scope.viewerID() {
		t.Fatalf("supported-models catalog call contract: %+v", last)
	}
	// 目录外 → 400，文案逐字对照归档。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("supported-miss",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-o1", "gpt-o2"}, nil), scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：gpt-o1、gpt-o2")
	// 错误样本只取前 5 个（归档 slice(0, 5).join('、')）。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("supported-sample",
		Credentials{"api_key": "sk-live-secret-1234567890"},
		[]string{"m1", "m2", "m3", "m4", "m5", "m6"}, nil), scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：m1、m2、m3、m4、m5")
	// 协议档案过滤：gpt 供应商在 providerModelSupportsProtocolProfile 短路放行
	// （归档 isGptVendorCode 分支），需换 openai-compatible 供应商验证档案过滤。
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-openai-compat', 'openai', 'OpenAI 兼容', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('prof-openai-compat', 'openai', 'OpenAI 兼容协议', 1, 'openai', 'v1', 'https://compat.example.com/v1',
		'gpt-4o-mini', '["api_key","oauth"]', '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-openai-compat-default', ?, '兼容默认分组', 'openai', 1, 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, scope.viewerID())
	messagesOnly := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{{
		Model:                 "gpt-4o-mini",
		SupportedAPIProtocols: []string{"messages"},
	}}}
	store.SetModelCatalogReader(messagesOnly)
	compatInput := catalogWiringCreateInput("supported-profile-miss",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil)
	compatInput.ProviderCode = "openai"
	compatInput.ProviderProtocolProfileID = "prof-openai-compat"
	_, err = store.Create(context.Background(), compatInput, scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：gpt-4o-mini")
	// 同一目录事实在 gpt 供应商下放行（归档 isGptVendorCode 短路）。
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{{
		Model:                 "gpt-4o-mini",
		SupportedAPIProtocols: []string{"messages"},
	}}})
	if _, err := store.Create(context.Background(), catalogWiringCreateInput("supported-gpt-passthrough",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSupportedModelsCatalogNilPort(t *testing.T) {
	// nil 端口 no-op（self-contained 约定）：目录外模型也能创建，不发起目录读取。
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-supported-nil", fake)
	store.SetModelCatalogReader(nil)
	if _, err := store.Create(context.Background(), catalogWiringCreateInput("supported-nil",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"anything"}, nil), scope); err != nil {
		t.Fatal(err)
	}
	if fake.callCount() != 0 {
		t.Fatalf("nil port must not query the catalog, calls = %d", fake.callCount())
	}
}

func TestBatchUpdateWiresSupportedModelsCatalogAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-supported-batch", fake)
	first, err := store.Create(context.Background(), catalogWiringCreateInput("batch-supported-a",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(context.Background(), catalogWiringCreateInput("batch-supported-b",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	targets := []BatchUpdateTarget{
		{AccountID: first.ID, ConfigRevision: 1},
		{AccountID: second.ID, ConfigRevision: 1},
	}
	// 支持模型未变化（重复项/空白归一化后相同集合）→ 不触发目录校验。
	callsBefore := fake.callCount()
	if _, err := store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"supportedModels": {Enabled: true, Value: []any{" gpt-4o-mini ", "gpt-4o-mini"}},
		},
	}, scope); err != nil {
		t.Fatal(err)
	}
	if fake.callCount() != callsBefore {
		t.Fatalf("unchanged supported models must skip the catalog assertion, calls %d -> %d", callsBefore, fake.callCount())
	}
	// 支持模型变化且目录外 → 整批失败，文案逐字对照归档（事务回滚，revision 不变）。
	_, err = store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"supportedModels": {Enabled: true, Value: []any{"gpt-o1"}},
		},
	}, scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：gpt-o1")
	// 目录内变化 → 批量成功。
	store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		gptCatalogFact(),
		{Model: "gpt-4o", SupportedAPIProtocols: []string{"chat_completions", "responses"}},
	}})
	if _, err := store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"supportedModels": {Enabled: true, Value: []any{"gpt-4o-mini", "gpt-4o"}},
		},
	}, scope); err != nil {
		t.Fatal(err)
	}
}

// ---- hybrid 供应商：支持模型直通 + 映射协议池校验 ----

func TestHybridAccountWriteSkipsSupportedModelsCatalogButChecksPools(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	env := newTestEnv(t)
	// hybrid 供应商 + 启用档案 + owner 默认分组。
	ownerID := "owner-hybrid-write"
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-hybrid', 'hybrid', '混合供应商', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('prof-hybrid', 'hybrid', '混合协议', 1, 'openai', 'v1', 'https://hybrid.example.com/v1',
		'gpt-4o-mini', '["api_key","oauth"]', '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-hybrid-default', ?, '混合默认分组', 'hybrid', 1, 1, 'personal', ?, ?)`, ownerID, now, now)
	store := env.store
	store.SetModelCatalogReader(fake)
	scope := AccessScope{ViewerID: ownerID, IsAdmin: true}

	input := catalogWiringCreateInput("hybrid-hit",
		Credentials{"api_key": "sk-live-secret-1234567890", "supported_endpoint_modes": []any{"chat_json", "chat_sse"}},
		[]string{"anything-outside-catalog"}, nil)
	input.ProviderCode = "hybrid"
	input.ProviderProtocolProfileID = "prof-hybrid"
	// hybrid 支持模型直通（归档 :69），目录不被支持模型断言读取；chat_completions
	// 同协议映射无映射时也不读目录。
	created, err := store.Create(context.Background(), input, scope)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("hybrid create must succeed")
	}
	if fake.callCount() != 0 {
		t.Fatalf("hybrid supported models must bypass the catalog, calls = %d", fake.callCount())
	}

	// hybrid 映射：模型落协议池（fake 目录归属 gpt 供应商 → openai 池命中）。
	poolFake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store.SetModelCatalogReader(poolFake)
	poolHit, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		ModelMappingsPresent:   true,
		ModelMappings: []ModelMapping{{
			SourceModel: "gpt-4o-mini", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-4o-mini", UpstreamEndpointFamily: "chat_completions",
		}},
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if poolFake.callCount() == 0 {
		t.Fatal("hybrid mapping patch must query the protocol pool catalogs")
	}

	// hybrid 映射：池外模型 → 拒绝，文案逐字对照归档
	// （账号模型别名来源模型不在对应协议模型池中：…）。期望 revision 取上一次
	// 成功 patch 返回的 config_revision（映射替换会递增）。
	outsideFake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store.SetModelCatalogReader(outsideFake)
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: poolHit.ConfigRevision,
		ModelMappingsPresent:   true,
		ModelMappings: []ModelMapping{{
			SourceModel: "outside-model", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-4o-mini", UpstreamEndpointFamily: "chat_completions",
		}},
	}, scope)
	assertValidationError(t, err, "账号模型别名来源模型不在对应协议模型池中：outside-model")

	// hybrid 映射：上游池外 → 拒绝（目标模型不在对应上游协议模型池中）。
	// 上一 patch 校验失败回滚，revision 仍是 poolHit.ConfigRevision；提交相对
	// 持久行的实际变更（上游模型换成池外模型）才会进入协议池校验。
	upstreamFake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store.SetModelCatalogReader(upstreamFake)
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: poolHit.ConfigRevision,
		ModelMappingsPresent:   true,
		ModelMappings: []ModelMapping{{
			SourceModel: "gpt-4o-mini", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "outside-upstream", UpstreamEndpointFamily: "chat_completions",
		}},
	}, scope)
	assertValidationError(t, err, "账号模型别名目标模型不在对应上游协议模型池中：outside-upstream")
}

// TestPatchSupportedModelsUnorderedEqualityAndEmptyArray 对照归档
// normalizedSupportedModelsForPatch（account-management-patch.repository.ts:1513-1527）：
// 无序集合相等（unorderedStringListEquals）即视为未变化——同集合不同顺序的重
// 提交沿用当前集合，不触发目录校验；空数组在 Go patch 链由归一化入口的类型
// 守卫拒绝（anySliceOrNil 空集折叠，见断言处注释），create 链空数组保持归档
// assertAccountSupportedModelsRequired 文案。
func TestPatchSupportedModelsUnorderedEqualityAndEmptyArray(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		gptCatalogFact(),
		{Model: "aaa-model", SupportedAPIProtocols: []string{"chat_completions", "responses"},
			SupportedServiceTiers: []string{"priority"}, SupportedReasoningEfforts: []string{"low"}},
	}}
	store, scope := newCatalogWiringStore(t, "owner-supported-patch-order", fake)
	created, err := store.Create(context.Background(), catalogWiringCreateInput("patch-order-target",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini", "aaa-model"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	// create 链的目录断言读取次数即基线（gpt 无请求覆盖，仅支持模型断言读一次）。
	baseline := fake.callCount()
	// 同集合不同顺序的重提交：DB 行按 model ASC 排序（aaa-model, gpt-4o-mini），
	// 输入保持创建顺序（gpt-4o-mini, aaa-model）→ 无序相等 → 未变化，不触发
	// 目录校验（归档 unorderedStringListEquals 早退）。
	if _, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-4o-mini", "aaa-model"},
		SupportedModelsPresent: true,
	}, scope); err != nil {
		t.Fatal(err)
	}
	if fake.callCount() != baseline {
		t.Fatalf("unordered-equal resubmission must skip the catalog assertion, calls %d -> %d", baseline, fake.callCount())
	}
	// 空数组：Go patch 链的归一化入口 anySliceOrNil 把空集折叠为 nil，
	// normalizeSupportedModelsInput 按非数组输入拒绝（400）；空集文案
	// （账户支持模型不能为空…）由 create 链断言（下方），这里固定 patch
	// 链的实际契约。
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{},
		SupportedModelsPresent: true,
	}, scope)
	assertValidationError(t, err, "账户支持模型必须是字符串数组")
	// create 链空数组同文案（归档 create 写侧 normalizeAccountSupportedModels
	// Input 空集 + assertAccountSupportedModelsRequired）。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("supported-empty",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{}, nil), scope)
	assertValidationError(t, err, "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型")
}
