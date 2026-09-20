package accounts

// w18 导入来源模型映射 + 模型目录拦截：NewAPI/One-API channel 的 models 列表
// 映射为 supportedModels、Sub2API 账户级模型字段透传（import_source.go），以及
// 目录断言失败时导入链专属的引导后缀（import.go importCatalogGuidance）。
// 目录读取全部经 fakeAccountModelCatalog 注入（Mock 可回放、结果稳定），不依赖
// 真实 provider_model_catalog 数据；CLIProxyAPI 侧用 adaptImportSource 纯函数
// 断言（参照 import_source_yaml_test.go）。

import (
	"context"
	"strings"
	"testing"
)

// w18OpenAIFact 构造一个声明 chat_completions + responses 协议的目录事实，
// 满足 openai 兼容供应商（profile_openai_openai_v1）的协议档案过滤。
func w18OpenAIFact(model string) AccountModelCatalogFact {
	return AccountModelCatalogFact{
		Model:                 model,
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
	}
}

// TestW18ChannelModelsBlockedByCatalog 场景 a：channel 携带 models（含空格与
// 重复项）且模型不在目录 → 预览项 failed，message 同时含共享拦截文案与导入链
// 引导后缀，CanImport=false；ExecuteImport 不创建任何账户。
func TestW18ChannelModelsBlockedByCatalog(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	// 空目录：channel models 中的模型全部不在目录。
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{})

	document := []any{
		map[string]any{"id": float64(1), "type": float64(1), "key": "sk-w18-blocked",
			"name": "模型频道", "base_url": "https://api.openai.com/v1", "status": float64(1),
			"models": "gpt-w18-a, gpt-w18-a ，gpt-w18-b"},
	}
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	result, err := env.store.PreviewImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("预览不应报错：%v", err)
	}
	// models 形态合法（string）时豁免 ignoredFields 计数：来源记录仍被接受，
	// 拦截发生在计划层的目录断言。
	if result.Source.Records != 1 || result.Source.Accepted != 1 || result.Source.IgnoredFields != 0 {
		t.Fatalf("来源摘要不一致：%+v", result.Source)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionFailed {
		t.Fatalf("账户项应为 failed：%+v", result.Accounts)
	}
	joined := strings.Join(result.Accounts[0].Messages, "|")
	// 去重后逐字对照共享断言文案（joinMappingModelSample 样本拼接）。
	if !strings.Contains(joined, "账户支持模型不在供应商模型目录中：gpt-w18-a、gpt-w18-b") {
		t.Fatalf("缺少目录拦截文案：%s", joined)
	}
	if !strings.Contains(joined, "请先在模型目录创建这些模型并配置价格后再导入") {
		t.Fatalf("缺少导入链引导后缀：%s", joined)
	}
	if result.CanImport {
		t.Fatalf("存在失败项时不能允许导入：%+v", result.Summary)
	}
	executed, err := env.store.ExecuteImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("执行不应报错：%v", err)
	}
	if executed.Imported {
		t.Fatalf("CanImport=false 时不得执行导入：%+v", executed.Summary)
	}
	if count := env.count(t, `SELECT COUNT(*) FROM accounts`); count != 0 {
		t.Fatalf("不应创建任何账户：%d", count)
	}
}

// TestW18ChannelModelsPersistDedupOrder 场景 b：模型加入目录后预览通过，
// 导入落库的 supported_models 为映射后的去重保序列表。
func TestW18ChannelModelsPersistDedupOrder(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		w18OpenAIFact("gpt-w18-b"),
		w18OpenAIFact("gpt-4o-mini"),
	}})

	document := []any{
		map[string]any{"type": float64(1), "key": "sk-w18-dedup", "name": "去重频道",
			"models": []any{" gpt-w18-b ", "gpt-4o-mini", "gpt-w18-b", ""}},
	}
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	result, err := env.store.PreviewImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("预览不应报错：%v", err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || len(result.Accounts[0].Messages) != 0 {
		t.Fatalf("目录内模型应通过预览：%+v", result.Accounts)
	}
	if !result.CanImport {
		t.Fatalf("预览应允许导入：%+v", result.Summary)
	}
	executed, err := env.store.ExecuteImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("执行错误：%v", err)
	}
	if !executed.Imported || executed.Summary.Accounts.Create != 1 {
		t.Fatalf("执行汇总不一致：%+v", executed.Summary)
	}
	if executed.Accounts[0].AccountID == nil {
		t.Fatalf("缺少创建的账户 id：%+v", executed.Accounts[0])
	}
	accountID := *executed.Accounts[0].AccountID
	// 落库顺序即写入顺序（replaceAccountSupportedModels 按映射列表逐条插入），
	// 按 rowid 读取验证首次出现顺序与去重。
	rows, err := env.db.Query(`SELECT model FROM account_supported_models WHERE account_id = ? ORDER BY rowid`, accountID)
	if err != nil {
		t.Fatalf("读取 supported_models 失败：%v", err)
	}
	defer rows.Close()
	stored := []string{}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			t.Fatalf("扫描 supported_models 失败：%v", err)
		}
		stored = append(stored, model)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 supported_models 失败：%v", err)
	}
	want := []string{"gpt-w18-b", "gpt-4o-mini"}
	if len(stored) != len(want) || stored[0] != want[0] || stored[1] != want[1] {
		t.Fatalf("supported_models 落库应去重保序 %v：%v", want, stored)
	}
}

// TestW18Sub2APIModelFieldsPersist 场景 c（目录内臂）：sub2api 账户的
// supportedModels/healthCheckModel/healthCheckEndpointMode/modelMappings 原样
// 透传，导入成功后字段落库，且模型键不再计入 ignoredFields。
func TestW18Sub2APIModelFieldsPersist(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		w18OpenAIFact("gpt-w18-pass"),
		w18OpenAIFact("gpt-4o-mini"),
	}})

	document := map[string]any{"accounts": []any{
		map[string]any{"name": "w18 透传账户", "platform": "openai", "type": "api_key",
			"credentials":             map[string]any{"api_key": "sk-w18-pass", "base_url": "https://api.openai.com/v1"},
			"supportedModels":         []any{"gpt-w18-pass", "gpt-4o-mini"},
			"healthCheckModel":        "gpt-w18-pass",
			"healthCheckEndpointMode": "chat_json",
			"modelMappings": []any{map[string]any{
				"sourceModel": "gpt-4o-mini", "sourceEndpointFamily": "chat_completions",
				"upstreamModel": "gpt-w18-pass", "upstreamEndpointFamily": "chat_completions",
			}},
		},
	}}
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	result, err := env.store.PreviewImport(context.Background(), document, importSourceSub2Api, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("预览不应报错：%v", err)
	}
	if result.Source.IgnoredFields != 0 || result.Source.Accepted != 1 {
		t.Fatalf("模型键不应计入忽略字段：%+v", result.Source)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || len(result.Accounts[0].Messages) != 0 {
		t.Fatalf("目录内模型应通过预览：%+v", result.Accounts)
	}
	executed, err := env.store.ExecuteImport(context.Background(), document, importSourceSub2Api, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("执行错误：%v", err)
	}
	if !executed.Imported || executed.Summary.Accounts.Create != 1 || executed.Accounts[0].AccountID == nil {
		t.Fatalf("执行汇总不一致：%+v", executed)
	}
	accountID := *executed.Accounts[0].AccountID
	if count := env.count(t, `SELECT COUNT(*) FROM account_supported_models
		WHERE account_id = ? AND provider_code = 'openai' AND model IN ('gpt-w18-pass','gpt-4o-mini')`, accountID); count != 2 {
		t.Fatalf("supportedModels 应逐条落库：%d", count)
	}
	var healthModel, healthMode string
	if err := env.db.QueryRow(`SELECT health_check_model, health_check_endpoint_mode FROM accounts WHERE id = ?`, accountID).
		Scan(&healthModel, &healthMode); err != nil {
		t.Fatalf("账户未落库：%v", err)
	}
	if healthModel != "gpt-w18-pass" || healthMode != "chat_json" {
		t.Fatalf("健康检查字段不一致：%q %q", healthModel, healthMode)
	}
	if count := env.count(t, `SELECT COUNT(*) FROM account_model_mappings
		WHERE account_id = ? AND source_model = 'gpt-4o-mini' AND source_endpoint_family = 'chat_completions'
		AND upstream_model = 'gpt-w18-pass' AND upstream_endpoint_family = 'chat_completions' AND enabled = 1`, accountID); count != 1 {
		t.Fatalf("modelMappings 应落库：%d", count)
	}
}

// TestW18Sub2APIModelFieldsBlockedAndGuidanceScope 场景 c（目录外臂）+ 后缀
// 作用域：目录外 supportedModels 的 message 带引导后缀；healthCheckModel 不属
// 于 supportedModels 的既有校验不加后缀。
func TestW18Sub2APIModelFieldsBlockedAndGuidanceScope(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		w18OpenAIFact("gpt-4o-mini"),
	}})

	document := map[string]any{"accounts": []any{
		map[string]any{"name": "w18 拦截账户", "platform": "openai", "type": "api_key",
			"credentials":     map[string]any{"api_key": "sk-w18-miss", "base_url": "https://api.openai.com/v1"},
			"supportedModels": []any{"gpt-w18-missing"}},
		map[string]any{"name": "w18 健康模型账户", "platform": "openai", "type": "api_key",
			"credentials":      map[string]any{"api_key": "sk-w18-health", "base_url": "https://api.openai.com/v1"},
			"supportedModels":  []any{"gpt-4o-mini"},
			"healthCheckModel": "gpt-4o-mini-x"},
	}}
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	result, err := env.store.PreviewImport(context.Background(), document, importSourceSub2Api, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("预览不应报错：%v", err)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("应产出两个账户项：%+v", result.Accounts)
	}
	blocked := strings.Join(result.Accounts[0].Messages, "|")
	if !strings.Contains(blocked, "账户支持模型不在供应商模型目录中：gpt-w18-missing") ||
		!strings.Contains(blocked, "请先在模型目录创建这些模型并配置价格后再导入") {
		t.Fatalf("目录外支持模型应带拦截文案与引导后缀：%s", blocked)
	}
	health := strings.Join(result.Accounts[1].Messages, "|")
	if !strings.Contains(health, "账户 healthCheckModel 必须属于 supportedModels") {
		t.Fatalf("缺少健康模型校验文案：%s", health)
	}
	if strings.Contains(health, "请先在模型目录创建这些模型并配置价格后再导入") {
		t.Fatalf("非目录断言校验不应带引导后缀：%s", health)
	}
	if result.CanImport {
		t.Fatalf("存在失败项时不能允许导入：%+v", result.Summary)
	}
}

// TestW18ChannelWithoutModelsFallsBackToProviderDefaults 场景 d：channel 无
// models 字段时维持回退供应商默认支持模型并导入成功（既有行为回归保护）。
func TestW18ChannelWithoutModelsFallsBackToProviderDefaults(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	// 目录只含供应商默认模型 gpt-4o-mini（default_supported_models_json）。
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		w18OpenAIFact("gpt-4o-mini"),
	}})

	document := []any{
		map[string]any{"type": float64(1), "key": "sk-w18-default", "name": "默认频道"},
	}
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	result, err := env.store.PreviewImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("预览不应报错：%v", err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || len(result.Accounts[0].Messages) != 0 {
		t.Fatalf("无 models 字段应回退默认支持模型：%+v", result.Accounts)
	}
	executed, err := env.store.ExecuteImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("执行错误：%v", err)
	}
	if !executed.Imported || executed.Summary.Accounts.Create != 1 || executed.Accounts[0].AccountID == nil {
		t.Fatalf("执行汇总不一致：%+v", executed)
	}
	accountID := *executed.Accounts[0].AccountID
	if count := env.count(t, `SELECT COUNT(*) FROM account_supported_models
		WHERE account_id = ? AND model = 'gpt-4o-mini'`, accountID); count != 1 {
		t.Fatalf("应落库供应商默认支持模型：%d", count)
	}
}

// TestW18CPASourceKeepsNoSupportedModels 场景 e：CPA 输入含模型类键时，适配
// 产出的账户记录仍无 supportedModels（模型类键维持计入 ignoredFields）。
func TestW18CPASourceKeepsNoSupportedModels(t *testing.T) {
	yamlInput := `openai-compatibility:
  - name: cpa-models-provider
    base-url: https://cpa-models.example.com/v1
    models: [gpt-4o-mini, gpt-4.1]
    supportedModels: [gpt-4o-mini]
    api-key-entries:
      - api-key: sk-cpa-models-1
        models: [gpt-4o-mini]
`
	document, source := cpaAdapt(t, yamlInput)
	// provider 层 models/supportedModels 与 entry 层 models 均不在 CPA 白名单，
	// 逐层计入 ignoredFields（总量 3）。
	assertSourceSummary(t, source, 1, 1, 0, 3)
	accounts := cpaAccounts(t, document)
	if len(accounts) != 1 {
		t.Fatalf("cpa accounts: %d", len(accounts))
	}
	if _, exists := accounts[0]["supportedModels"]; exists {
		t.Fatalf("CPA 适配不得产出 supportedModels：%v", accounts[0])
	}
}

// TestW18ChannelModelsInvalidShapeCountsIgnoredField 场景 f：channel models
// 为非法形态（数字、null、布尔、对象）时账户仍被接受、不跳过，回退供应商
// 默认支持模型导入成功，且来源摘要恰好计入 1 个 ignoredFields（异常形态可
// 观测）。各形态独立 env，互不污染。
func TestW18ChannelModelsInvalidShapeCountsIgnoredField(t *testing.T) {
	invalidShapes := []struct {
		name   string
		models any
	}{
		{name: "float64", models: float64(42)},
		{name: "json_null", models: nil},
		{name: "bool", models: true},
		{name: "map", models: map[string]any{"shape": "invalid"}},
	}
	for _, shape := range invalidShapes {
		t.Run(shape.name, func(t *testing.T) {
			env := newTestEnv(t)
			seedOpenAICompatibleProvider(t, env)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
				w18OpenAIFact("gpt-4o-mini"),
			}})

			document := []any{
				map[string]any{"id": float64(1), "type": float64(1), "key": "sk-w18-shape",
					"name": "异常模型频道", "base_url": "https://api.openai.com/v1", "status": float64(1),
					"models": shape.models},
			}
			scope := AccessScope{ViewerID: adminID, IsAdmin: true}
			result, err := env.store.PreviewImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
			if err != nil {
				t.Fatalf("预览不应报错：%v", err)
			}
			if result.Source.Records != 1 || result.Source.Accepted != 1 || result.Source.IgnoredFields != 1 {
				t.Fatalf("非法形态 models 应恰好计入 1 个 ignoredFields：%+v", result.Source)
			}
			if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || len(result.Accounts[0].Messages) != 0 {
				t.Fatalf("非法形态 models 应回退默认支持模型并通过预览：%+v", result.Accounts)
			}
			executed, err := env.store.ExecuteImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
			if err != nil {
				t.Fatalf("执行错误：%v", err)
			}
			if !executed.Imported || executed.Summary.Accounts.Create != 1 || executed.Accounts[0].AccountID == nil {
				t.Fatalf("执行汇总不一致：%+v", executed)
			}
			accountID := *executed.Accounts[0].AccountID
			if count := env.count(t, `SELECT COUNT(*) FROM account_supported_models
				WHERE account_id = ? AND model = 'gpt-4o-mini'`, accountID); count != 1 {
				t.Fatalf("应仅落库供应商默认支持模型（未写入异常 models）：%d", count)
			}
		})
	}
}

// TestW18ChannelModelsLegalEmptyShapeNotCounted 场景 g：channel models 为
// []any{float64(1)}（形态合法但归一为空）时不计 ignoredFields、不写
// supportedModels，回退供应商默认支持模型导入成功。
func TestW18ChannelModelsLegalEmptyShapeNotCounted(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{
		w18OpenAIFact("gpt-4o-mini"),
	}})

	document := []any{
		map[string]any{"id": float64(1), "type": float64(1), "key": "sk-w18-empty",
			"name": "空模型频道", "base_url": "https://api.openai.com/v1", "status": float64(1),
			"models": []any{float64(1)}},
	}
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	result, err := env.store.PreviewImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("预览不应报错：%v", err)
	}
	if result.Source.Records != 1 || result.Source.Accepted != 1 || result.Source.IgnoredFields != 0 {
		t.Fatalf("形态合法的空 models 不应计入 ignoredFields：%+v", result.Source)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || len(result.Accounts[0].Messages) != 0 {
		t.Fatalf("归一为空的 models 应回退默认支持模型并通过预览：%+v", result.Accounts)
	}
	executed, err := env.store.ExecuteImport(context.Background(), document, importSourceNewAPI, ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("执行错误：%v", err)
	}
	if !executed.Imported || executed.Summary.Accounts.Create != 1 || executed.Accounts[0].AccountID == nil {
		t.Fatalf("执行汇总不一致：%+v", executed)
	}
	accountID := *executed.Accounts[0].AccountID
	if count := env.count(t, `SELECT COUNT(*) FROM account_supported_models
		WHERE account_id = ? AND model = 'gpt-4o-mini'`, accountID); count != 1 {
		t.Fatalf("应仅落库供应商默认支持模型：%d", count)
	}
}
