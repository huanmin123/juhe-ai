package accounts

// 模型目录校验域测试（model_catalog_validation.go）：目录命中 / 未命中 /
// 目录异常三条路径，对照归档 account-model-normalization.ts 的断言文案与
// 触发条件逐条标注。B4 ⑥ 起生产端口 AccountModelCatalogReader 已退役，
// 目录断言只读 models 单表；本文件保留测试本地 fake 仅用于历史调用计数
// 断言（当前恒 0），不再注入 Store。
//
// gpt 请求覆盖（service_tier_override / reasoning_effort_override）的目录
// 断言已于 2026-10-18 简化定案删除，本文件的覆盖段改钉写入面协议族值域：
// 形状 + 协议族全集（shared/platform requestoverride），目录未声明档位的值
// 照常保存，运行时才按最终上游模型降级/跳过。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fake catalog port（测试本地形状；生产接口已退役） ----

type AccountModelCatalogFact struct {
	Model                     string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
}

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

// ---- 请求覆盖协议族值域（写入面，2026-10-18 简化定案） ----

// requestOverrideNormalize 以给定上下文直连写入面凭据归一化（api_key 形状：
// base_url + 覆盖字段），返回归一化记录供落库值断言。
func requestOverrideNormalize(extra Credentials, context *EndpointModeDefaultContext) (Credentials, error) {
	merged := Credentials{"api_key": "sk-test-0001", "base_url": "https://api.example.com"}
	for key, value := range extra {
		merged[key] = value
	}
	return NormalizeAccountCredentialsForWrite("api_key", merged, context)
}

func TestRequestOverrideFamilyDomainArms(t *testing.T) {
	anthropic := &EndpointModeDefaultContext{ProviderCode: "anthropic", ProtocolCode: "anthropic", ProtocolVersion: "v1"}
	gemini := &EndpointModeDefaultContext{ProviderCode: "gemini", ProtocolCode: "gemini", ProtocolVersion: "v1beta"}
	gpt := w13gOpenAIContext("gpt")

	// anthropic 族：思考级别全集 low..max，xhigh 可配；服务等级为空集，
	// priority 拒绝；default 哨兵（强制标准档）恒可保存。
	normalized, err := requestOverrideNormalize(Credentials{"reasoning_effort_override": "xhigh"}, anthropic)
	if err != nil {
		t.Fatalf("anthropic xhigh 应通过：%v", err)
	}
	if normalized["reasoning_effort_override"] != "xhigh" {
		t.Fatalf("anthropic xhigh 应落库：%v", normalized["reasoning_effort_override"])
	}
	normalized, err = requestOverrideNormalize(Credentials{"service_tier_override": "default"}, anthropic)
	if err != nil {
		t.Fatalf("anthropic default 哨兵应恒可保存：%v", err)
	}
	if normalized["service_tier_override"] != "default" {
		t.Fatalf("anthropic default 应落库：%v", normalized["service_tier_override"])
	}
	if _, err := requestOverrideNormalize(Credentials{"service_tier_override": "priority"}, anthropic); err == nil ||
		err.Error() != "服务等级覆盖无效" {
		t.Fatalf("anthropic 族服务等级为空集，priority 应拒绝：%v", err)
	}

	// gemini 族：思考级别 minimal..high，max 拒绝（不在族集）；服务等级
	// default/priority/flex 可配，bogus 值域拒绝。
	if _, err := requestOverrideNormalize(Credentials{"reasoning_effort_override": "high"}, gemini); err != nil {
		t.Fatalf("gemini high 应通过：%v", err)
	}
	if _, err := requestOverrideNormalize(Credentials{"reasoning_effort_override": "max"}, gemini); err == nil ||
		err.Error() != "思考级别覆盖无效" {
		t.Fatalf("gemini max 不在族集应拒绝：%v", err)
	}
	if _, err := requestOverrideNormalize(Credentials{"service_tier_override": "priority"}, gemini); err != nil {
		t.Fatalf("gemini priority 应通过：%v", err)
	}
	if _, err := requestOverrideNormalize(Credentials{"service_tier_override": "bogus"}, gemini); err == nil ||
		err.Error() != "服务等级覆盖无效" {
		t.Fatalf("gemini bogus 值域应拒绝：%v", err)
	}

	// gpt 供应商天然 openai 族：max 可配（族集含 max，不含 ultra）；turbo/
	// ultra 拒绝。
	if _, err := requestOverrideNormalize(Credentials{"reasoning_effort_override": "max"}, gpt); err != nil {
		t.Fatalf("gpt max 应通过：%v", err)
	}
	if _, err := requestOverrideNormalize(Credentials{"reasoning_effort_override": "ultra"}, gpt); err == nil ||
		err.Error() != "思考级别覆盖无效" {
		t.Fatalf("gpt ultra 应拒绝：%v", err)
	}
	if _, err := requestOverrideNormalize(Credentials{"service_tier_override": "turbo"}, gpt); err == nil ||
		err.Error() != "服务等级覆盖无效" {
		t.Fatalf("gpt turbo 应拒绝：%v", err)
	}

	// 形状臂（族集判定之前）：首尾空白即非法，不 trim 后比对。
	if _, err := requestOverrideNormalize(Credentials{"service_tier_override": " flex"}, gpt); err == nil ||
		err.Error() != "服务等级覆盖无效" {
		t.Fatalf("首部空白应按形状拒绝：%v", err)
	}
	if _, err := requestOverrideNormalize(Credentials{"reasoning_effort_override": "ultra "}, gpt); err == nil ||
		err.Error() != "思考级别覆盖无效" {
		t.Fatalf("尾部空白应按形状拒绝：%v", err)
	}
}

func TestRequestOverrideGeminiInteractionsOnlyGuard(t *testing.T) {
	gemini := &EndpointModeDefaultContext{ProviderCode: "gemini", ProtocolCode: "gemini", ProtocolVersion: "v1beta"}
	// Interactions-only（端点能力显式声明且不含 GenerateContent）+ 覆盖字段
	// → 400，文案逐字保留（守卫自 2026-10-18 起收拢在写入面归一化执行）。
	_, err := requestOverrideNormalize(Credentials{
		"supported_endpoint_modes": []any{"interactions_json", "interactions_sse"},
		"service_tier_override":    "priority",
	}, gemini)
	assertValidationError(t, err, "Interactions-only Gemini 账户不能配置 GenerateContent 请求覆盖")
	// 含 generate_content 形态时不触发守卫，priority 在 gemini 族集内放行。
	if _, err := requestOverrideNormalize(Credentials{
		"supported_endpoint_modes": []any{"generate_content_json"},
		"service_tier_override":    "priority",
	}, gemini); err != nil {
		t.Fatalf("generate_content 形态不应触发守卫：%v", err)
	}
	// 无覆盖字段时不触发守卫（写入面早退）。
	if _, err := requestOverrideNormalize(Credentials{
		"supported_endpoint_modes": []any{"interactions_json"},
	}, gemini); err != nil {
		t.Fatalf("无覆盖字段不应触发守卫：%v", err)
	}
}

func TestCreateRequestOverridesFamilyDomainWiring(t *testing.T) {
	store, scope := newCatalogWiringStore(t, "owner-override-family-create", nil)
	// priority 覆盖保存成功；覆盖值域不发起目录读取（写入面只做形状 + 协议族
	// 值域），支持模型目录断言已切 models 身份解析（夹具种子命中）。
	result, err := store.Create(context.Background(), catalogWiringCreateInput("family-hit",
		Credentials{"api_key": "sk-live-secret-1234567890", "service_tier_override": "priority"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == "" {
		t.Fatalf("create with overrides must succeed: id=%q", result.ID)
	}
	// default 哨兵 + 目录未声明任何档位的思考级别：无任何档位声明也恒可保存
	//（原「所选支持模型中没有模型支持/声明…」目录门禁语义反转，运行时降级/
	// 跳过）。
	if _, err := store.Create(context.Background(), catalogWiringCreateInput("family-bare",
		Credentials{"api_key": "sk-live-secret-1234567890", "service_tier_override": "default", "reasoning_effort_override": "xhigh"},
		[]string{"gpt-4o-mini"}, nil), scope); err != nil {
		t.Fatalf("default 与声明外档位应恒可保存：%v", err)
	}
	// 值域拒绝（族集之外）：turbo/ultra 仍被写入面拒绝。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("family-tier-miss",
		Credentials{"api_key": "sk-live-secret-1234567890", "service_tier_override": "turbo"}, []string{"gpt-4o-mini"}, nil), scope)
	assertValidationError(t, err, "服务等级覆盖无效")
	_, err = store.Create(context.Background(), catalogWiringCreateInput("family-effort-miss",
		Credentials{"api_key": "sk-live-secret-1234567890", "reasoning_effort_override": "ultra"}, []string{"gpt-4o-mini"}, nil), scope)
	assertValidationError(t, err, "思考级别覆盖无效")
}

func TestPatchRequestOverridesFamilyDomainWiring(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-override-family-patch", fake)
	created, err := store.Create(context.Background(), catalogWiringCreateInput("patch-family-target",
		Credentials{"api_key": "sk-live-secret-1234567890", "service_tier_override": "priority"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	// 支持模型换成目录内未声明任何档位的模型 → 通过（原目录门禁在该场景
	// 报「所选支持模型中没有模型支持服务等级 priority」，语义反转为放行）。
	if _, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-4o"},
		SupportedModelsPresent: true,
	}, scope); err != nil {
		t.Fatalf("目录未声明档位不得拦截既有覆盖：%v", err)
	}
	// 凭据参与时写入面族集校验仍生效：ultra → 400，事务回滚。
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 2,
		CredentialsPatch:       true,
		CredentialsPresent:     true,
		Credentials:            Credentials{"reasoning_effort_override": "ultra"},
	}, scope)
	assertValidationError(t, err, "思考级别覆盖无效")
	// 回滚后修订号不变，族集内的 xhigh 可保存。
	if _, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 2,
		CredentialsPatch:       true,
		CredentialsPresent:     true,
		Credentials:            Credentials{"reasoning_effort_override": "xhigh"},
	}, scope); err != nil {
		t.Fatalf("族集内档位应可保存：%v", err)
	}
}

func TestBatchUpdateRequestOverridesFamilyDomainWiring(t *testing.T) {
	// 目录只声明 low/high 档：xhigh 不在任何目录声明中，族集校验放行（原
	// 「目录不支持 xhigh → 整批失败」语义反转为成功）。
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-override-family-batch", fake)
	first, err := store.Create(context.Background(), catalogWiringCreateInput("batch-family-a",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(context.Background(), catalogWiringCreateInput("batch-family-b",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	targets := []BatchUpdateTarget{
		{AccountID: first.ID, ConfigRevision: 1},
		{AccountID: second.ID, ConfigRevision: 1},
	}
	input := BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"reasoningEffortOverride": {Enabled: true, Value: "xhigh"},
		},
	}
	if _, err := store.BatchUpdate(context.Background(), input, scope); err != nil {
		t.Fatalf("族集内档位批量应成功（目录不再门禁）：%v", err)
	}
	// 上一次批量已提交，两个账户的修订号各递增一次；后续用例改用新修订号，
	// 否则失败会停在版本冲突而不是待验证的值域拒绝。失败批次整批回滚，
	// 修订号不再变化，两个值域臂可共用同一组修订号。
	targets = []BatchUpdateTarget{
		{AccountID: first.ID, ConfigRevision: 2},
		{AccountID: second.ID, ConfigRevision: 2},
	}
	// ultra/turbo 不在 openai 族全集 → 写入面归一化在批量事务内拒绝，整批
	// 回滚（all-or-nothing）。
	_, err = store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"reasoningEffortOverride": {Enabled: true, Value: "ultra"},
		},
	}, scope)
	assertValidationError(t, err, "思考级别覆盖无效")
	_, err = store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"serviceTierOverride": {Enabled: true, Value: "turbo"},
		},
	}, scope)
	assertValidationError(t, err, "服务等级覆盖无效")
}

// ---- modelMappings 目录校验 ----

func mapping(sourceModel, sourceFamily, upstreamModel, upstreamFamily string) ModelMapping {
	return ModelMapping{SourceModel: sourceModel, SourceEndpointFamily: sourceFamily, UpstreamModel: upstreamModel, UpstreamEndpointFamily: upstreamFamily}
}

func TestModelMappingCatalogHit(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-hit")
	store := env.store
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: "prof-gpt"}
	// 非 hybrid：矩阵门 + models 身份解析（target 模型在夹具 models 种子内，
	// vendor gpt + owner 可见 + active）→ 通过。目录读取不再经端口（fake 目录
	// 已随 legacy 面拆除，models 行即权威）。
	err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")},
		[]string{"chat_json", "chat_sse"})
	if err != nil {
		t.Fatal(err)
	}
	// 空 mapping 集早退；hybrid 只走跨协议矩阵门（协议池段已拆除）。
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "gpt", "owner-1", profile, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.assertAccountModelMappingsInProviderCatalog(context.Background(), env.db, "hybrid", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")},
		[]string{"chat_json", "chat_sse"}); err != nil {
		t.Fatal(err)
	}
}

func TestModelMappingCatalogMiss(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-mapping-miss")
	store := env.store
	ctx := context.Background()
	profile := protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1"}
	// 上游端点能力满足矩阵门禁后，target 模型必须解析到 models 行（来源模型
	// 是客户端 wire model 字符串，不作 models 外键，§5.1）。
	modes := []string{"chat_json", "chat_sse"}
	// 目标模型不在 models（夹具无 gpt-o1）。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-o1", "chat_completions")}, modes),
		"账号模型别名目标模型不在当前供应商模型目录中：gpt-o1")
	// 错误样本只取前 5 个（归档 slice(0, 5).join('、')）。
	assertValidationError(t, store.assertAccountModelMappingsInProviderCatalog(ctx, env.db, "gpt", "owner-1", profile,
		[]ModelMapping{
			mapping("m1", "chat_completions", "gpt-o1", "chat_completions"),
			mapping("m2", "chat_completions", "gpt-o2", "chat_completions"),
			mapping("m3", "chat_completions", "gpt-o3", "chat_completions"),
			mapping("m4", "chat_completions", "gpt-o4", "chat_completions"),
			mapping("m5", "chat_completions", "gpt-o5", "chat_completions"),
			mapping("m6", "chat_completions", "gpt-o6", "chat_completions"),
		}, modes),
		"账号模型别名目标模型不在当前供应商模型目录中：gpt-o1、gpt-o2、gpt-o3、gpt-o4、gpt-o5")
}

// ---- create / patch / batch 挂接冒烟 ----

func newCatalogWiringStore(t *testing.T, ownerID string, _ *fakeAccountModelCatalog) (*Store, AccessScope) {
	t.Helper()
	env := newTestEnv(t)
	// B4 W2：supportedModels 走 models 身份键解析（种子见 seedFixtureModels）；
	// B4 ⑥：目录读端口已退役，断言只读 models 单表，fake 参数保留仅为历史
	// 用例签名兼容。
	env.seedProviderAndDefaultGroup(t, ownerID)
	return env.store, AccessScope{ViewerID: ownerID, IsAdmin: true}
}

func catalogWiringCreateInput(name string, credentials Credentials, supportedModels []string, mappings []ModelMapping) CreateInput {
	merged := Credentials{"api_key": "sk-live-secret-1234567890", "base_url": "https://api.openai.com/v1"}
	for key, value := range credentials {
		merged[key] = value
	}
	// B1 断点批（设计 §8）：连接集合与健康检查三件套显式提交（检查模型取支持
	// 模型首个，缺失时回退 gpt-4o-mini——健康检查模型归属校验在目录断言之后
	// 的写链执行，不影响目录臂的断言次序）。
	healthModel := "gpt-4o-mini"
	if len(supportedModels) > 0 {
		healthModel = supportedModels[0]
	}
	healthMode := "chat_json"
	healthPreset := "gpt_openai_v1"
	return CreateInput{
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "prof-gpt",
		Name:                      name,
		AccountType:               "api_key",
		Credentials:               merged,
		SupportedModels:           supportedModels,
		ModelMappings:             mappings,
		Status:                    CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
		Connections: []ConnectionInput{{
			ConnectionPresetCode: healthPreset,
			BaseURL:              "https://api.openai.com/v1",
			EndpointModes:        []string{"chat_json", "chat_sse"},
			Enabled:              true,
		}},
		HealthCheckConnectionPresetCode: &healthPreset,
		HealthCheckEndpointMode:         &healthMode,
		HealthCheckModel:                &healthModel,
	}
}

// TestCreateWiresGptRequestOverridesAssertion 已随 2026-10-18 简化定案删除：
// 覆盖字段的写入面族集挂接见 TestCreateRequestOverridesFamilyDomainWiring，
// 支持模型目录断言挂接见 TestCreateWiresSupportedModelsCatalogAssertion。

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
	// 同协议映射 + chat 端点能力：矩阵/目录门禁通过后，写入侧走
	// account_entry_mappings 双解析（B4 W2）——管理面创建链尚无连接行，按
	// mapping_target_unsupported 显式拒绝；连接面迁移归后续切片。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("mapping-hit",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"},
		[]ModelMapping{mapping("gpt-4o-mini", "chat_completions", "gpt-4o-mini", "chat_completions")}), scope)
	if err == nil || !strings.Contains(err.Error(), "mapping_target_unsupported: connection preset") {
		t.Fatalf("mapping write must fail closed without a connection, got %v", err)
	}
}

// TestPatchWiresGptRequestOverridesAssertion / TestBatchUpdateWires
// GptRequestOverridesAssertion 已随 2026-10-18 简化定案删除（覆盖字段不再有
// 目录断言，族集挂接见 TestPatchRequestOverridesFamilyDomainWiring /
// TestBatchUpdateRequestOverridesFamilyDomainWiring）。patch 链「支持模型
// 目录外拒绝」的独立臂保留如下。

func TestPatchWiresSupportedModelsCatalogAssertion(t *testing.T) {
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-catalog-patch", fake)
	created, err := store.Create(context.Background(), catalogWiringCreateInput("patch-target",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	// supportedModels 无序相等重提交不触发目录校验（config_revision 不递增）。
	if _, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-4o-mini", "gpt-4o-mini"},
		SupportedModelsPresent: true,
	}, scope); err != nil {
		t.Fatal(err)
	}
	// 把支持模型改成目录外的模型 → 支持模型目录校验（patch 链
	// normalizeAccountSupportedModelsForProviderAsync 目录段）→ 400，文案
	// 逐字对照归档。
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		SupportedModels:        []string{"gpt-o1"},
		SupportedModelsPresent: true,
	}, scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：gpt-o1")
}

// ---- 支持模型目录校验挂接（normalizeAccountSupportedModelsForProvider 家族） ----

func TestCreateWiresSupportedModelsCatalogAssertion(t *testing.T) {
	// 本测试后半段需要直写 providers/groups 种子（generic 语义供应商臂），
	// 直接持有 env 而不是 newCatalogWiringStore 包装。
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-supported-create")
	store := env.store
	scope := AccessScope{ViewerID: "owner-supported-create", IsAdmin: true}
	// models 种子内 → 创建成功（vendor gpt + upstream_model + owner 可见 +
	// active，models 身份口径）。
	created, err := store.Create(context.Background(), catalogWiringCreateInput("supported-hit",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil), scope)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("created id must not be empty")
	}
	// 目录外 → 400，文案逐字对照归档。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("supported-miss",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-o1", "gpt-o2"}, nil), scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：gpt-o1、gpt-o2")
	// 错误样本只取前 5 个（归档 slice(0, 5).join('、')）。
	_, err = store.Create(context.Background(), catalogWiringCreateInput("supported-sample",
		Credentials{"api_key": "sk-live-secret-1234567890"},
		[]string{"m2", "m3", "m4", "m5", "m6", "gpt-o9"}, nil), scope)
	assertValidationError(t, err, "账户支持模型不在供应商模型目录中：m2、m3、m4、m5、m6")
	// generic 语义供应商（openai，vendor_code=NULL）：身份键 COALESCE(vendor,'')
	// 命中夹具的 NULL vendor 行 → 同名模型放行（协议档案过滤段已随 legacy 面
	// 拆除，models 行即权威）。
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-openai-compat', 'openai', 'OpenAI 兼容', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('prof-openai-compat', 'openai', 'OpenAI 兼容协议', 1, 'openai', 'v1', 'https://compat.example.com/v1',
		'gpt-4o-mini', '["api_key","oauth"]', '[]', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-openai-compat-default', ?, '兼容默认分组', 1, 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, scope.EffectiveViewerID())
	compatInput := catalogWiringCreateInput("supported-generic-hit",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"gpt-4o-mini"}, nil)
	compatInput.ProviderCode = "openai"
	compatInput.ProviderProtocolProfileID = "prof-openai-compat"
	b1SetGenericConnections(&compatInput, "https://compat.example.com/v1")
	if _, err := store.Create(context.Background(), compatInput, scope); err != nil {
		t.Fatalf("generic provider create must succeed: %v", err)
	}
}

// b1SetGenericConnections 把 §8 连接集合与健康检查三件套改写为
// generic_openai_v1 形态（provider 覆写为 openai/hybrid 等 generic 语义的
// catalog wiring 测试臂使用；B1 断点批）。检查模型取载荷支持模型首个
// （缺失时回退 gpt-4o-mini，两模型均在夹具 models 种子内）。
func b1SetGenericConnections(input *CreateInput, baseURL string) {
	preset := "generic_openai_v1"
	mode := "chat_json"
	model := "gpt-4o-mini"
	if len(input.SupportedModels) > 0 {
		model = input.SupportedModels[0]
	}
	input.Connections = []ConnectionInput{{
		ConnectionPresetCode: preset,
		BaseURL:              baseURL,
		EndpointModes:        []string{"chat_json", "chat_sse"},
		Enabled:              true,
	}}
	input.HealthCheckConnectionPresetCode = &preset
	input.HealthCheckEndpointMode = &mode
	input.HealthCheckModel = &model
}

func TestCreateSupportedModelsCatalogNilPort(t *testing.T) {
	// B4 ⑥：目录读端口已退役，创建只按 models 单表解析；本用例保留原断言
	// 面（模型不在任何注入目录也不受目录门禁），fake 计数恒 0。
	fake := &fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}}
	store, scope := newCatalogWiringStore(t, "owner-supported-nil", fake)
	// B4 W2：模型名须解析到 models 行；m1 在夹具 models 内。
	if _, err := store.Create(context.Background(), catalogWiringCreateInput("supported-nil",
		Credentials{"api_key": "sk-live-secret-1234567890"}, []string{"m1"}, nil), scope); err != nil {
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
	// 目录内变化 → 批量成功（gpt-4o 在 seedFixtureModels 内；B4 ⑥ 起目录
	// 断言只读 models 单表）。
	if _, err := store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"supportedModels": {Enabled: true, Value: []any{"gpt-4o-mini", "gpt-4o"}},
		},
	}, scope); err != nil {
		t.Fatal(err)
	}
}

// ---- hybrid 供应商：models 身份解析 + 跨协议矩阵门 ----

func TestHybridAccountWriteChecksModelsIdentityAndMatrixGate(t *testing.T) {
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
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-hybrid-default', ?, '混合默认分组', 1, 1, 'personal', ?, ?)`, ownerID, now, now)
	store := env.store
	scope := AccessScope{ViewerID: ownerID, IsAdmin: true}

	// hybrid 账户 vendor_code=NULL：支持模型按 COALESCE(vendor,'') 身份键解析
	// （夹具 NULL vendor 行命中 m1）；映射无协议池段，仅跨协议矩阵门。
	input := catalogWiringCreateInput("hybrid-hit",
		Credentials{"api_key": "sk-live-secret-1234567890", "supported_endpoint_modes": []any{"chat_json", "chat_sse"}},
		[]string{"m1"}, nil)
	input.ProviderCode = "hybrid"
	input.ProviderProtocolProfileID = "prof-hybrid"
	b1SetGenericConnections(&input, "https://hybrid.example.com/v1")
	created, err := store.Create(context.Background(), input, scope)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("hybrid create must succeed")
	}

	// hybrid 映射：矩阵门 + target 模型 models 身份解析（写入侧双解析需要
	// 连接 + 支持成员前置事实，设计 §5.1/§9）。
	env.seedAccountConnection(t, created.ID, "generic_openai_v1", "gpt-4o-mini", "outside-upstream")
	if _, err := store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1,
		ModelMappingsPresent:   true,
		ModelMappings: []ModelMapping{{
			SourceModel: "gpt-4o-mini", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-4o-mini", UpstreamEndpointFamily: "chat_completions",
			TargetConnectionPresetCode: "generic_openai_v1",
		}},
	}, scope); err != nil {
		t.Fatal(err)
	}

	// hybrid 映射：target 模型不在 models → 拒绝。patch 链映射整替换先行，
	// 写入侧 resolveEntryModelID 以 mapping_target_unsupported 显式拒绝（400
	// 族；协议池文案随段拆除退役）。上一 patch 递增 revision，期望值取成功返
	// 回的 config_revision。
	_, err = store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 2,
		ModelMappingsPresent:   true,
		ModelMappings: []ModelMapping{{
			SourceModel: "gpt-4o-mini", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-o1", UpstreamEndpointFamily: "chat_completions",
			TargetConnectionPresetCode: "generic_openai_v1",
		}},
	}, scope)
	assertValidationError(t, err, `mapping_target_unsupported: target model "gpt-o1" resolves to no active owner-visible models row`)
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
