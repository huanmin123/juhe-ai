package main

// 演练场路由族组装（docs/functions/演练场设计.md §3.1，真实 Key 化修订）：cmd
// 适配层把派发执行器（chatGatewayExecutor 直驱 /v1 链 handler + 所选 API Key
// 明文注入）与 runtime cache 的 Key 行/分组模型并集装载包装成 playground 包的
// 自有端口，挂载 ${systemApiPrefix}/playground。
//
// 派发语义（设计 §3.7 真实 Key 化修订）：每次派发前 fail-closed 解密所选 Key
// （apikeys.Store.FindSecret，归属校验 key.system_account_id == 请求 viewer，
// 非本人含 admin 一律 404），注入 Authorization: Bearer 明文后走完整 /v1 鉴权/
// 调度/计费链；不再有固定派发目标、内部身份通道与用量抑制旁路（已随本修订
// 退役）。RemoteAddr 透传控制台请求的客户端地址。
//
// v1.2 图像生成工作台（设计 §3.4）：注入 PG 任务存储 + 目录计价端口，Register
// 自动装配进程内执行器，装配后立即启动恢复（queued 重入队、running 置
// failed）；图像任务按任务 params 里的 apiKeyId 派发与计价。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/playground"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// composePlaygroundFamily 装配演练场 Deps 并注册路由族。依赖与派发执行器同
// 源（chain / runtime cache / apikeys.Store），缺一 fail fast；产物暂存与
// 图像任务存储走业务库（playground_artifacts / playground_image_jobs）。
func composePlaygroundFamily(composed *composition, services *chainRuntimeServices, chain *gatewayChain, keyStore *apikeys.Store) error {
	if composed == nil {
		return errors.New("演练场组合缺少 composition")
	}
	if services == nil || services.Cache == nil {
		return errors.New("演练场组合缺少网关链 runtime cache")
	}
	if chain == nil {
		return errors.New("演练场组合缺少网关链 runtime")
	}
	if keyStore == nil {
		return errors.New("演练场组合缺少 apikeys.Store")
	}
	deps := &playground.Deps{
		Kernel:         composed.kernel,
		RequireSession: composed.authDeps.RequireSession(true),
		ScopeResolver:  newPlaygroundScopeResolver(),
		// 派发端口：所选 Key 解密 + 归属 fail-closed 校验后经 chatGatewayExecutor
		// 直驱 /v1 链（真实 Bearer，完整鉴权/调度/计费链，设计 §3.1/§3.7）。
		Dispatch: playgroundKeyDispatchAdapter{executor: newChatGatewayExecutor(chain), keys: keyStore},
		// Key 模型候选端口：runtime cache Key 行（owner + GroupBindings）→ 分组
		// 模型并集 ∪ 目录分类 → 提供账户能力域复核（§3.2；v1.3 UI 口径无计价
		// 投影）；runtime 空行时经 apikeys.FindDetail 第二跳区分停用与空绑定。
		KeyModels: playgroundKeyModelsAdapter{cache: services.Cache, keys: keyStore},
		// 产物暂存（§3.8）与图像任务存储（§5.1）：业务库存储，跨节点可读，
		// 无需共享盘；记录行永不删除。
		Artifacts: playground.NewPGArtifactStore(composed.db),
		ImageJobs: playground.NewPGImageJobStore(composed.db),
		// 图像任务扣费端口（§3.4.2：目录行本地计价，无价 → null）。
		ImageCost: playgroundImageCostAdapter{cache: services.Cache},
	}
	deps.Register(composed.kernel, systemAPIPrefix+"/playground")
	// Register 已装配图像任务执行器（ImageJobs 注入即生效，功能不设开关）；
	// 启动恢复（§3.4.2）：queued 重新入队继续执行、running 置 failed。恢复失
	// 败按装配依赖缺失同口径 fail fast——静默跳过会让任务永久悬挂在
	// queued/running 且无任何错误面。
	if deps.ImageRunner == nil {
		return errors.New("演练场图像任务执行器未装配")
	}
	if _, err := deps.ImageRunner.RecoverAtBoot(); err != nil {
		return fmt.Errorf("演练场图像任务启动恢复失败: %w", err)
	}
	return nil
}

// newPlaygroundScopeResolver 从登录态解析数据范围（AuthContext.SystemAccountID
// 投影登录者本人，fail-closed 单一 viewer 口径；RequireSession 中间件已挡未
// 登录，此处兜底错误不再透出身份细节）。RemoteAddr 随范围透传（派发
// RemoteAddr 同源）。
func newPlaygroundScopeResolver() playground.ScopeResolver {
	return func(r *http.Request) (playground.Scope, error) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			return playground.Scope{}, errors.New("请先登录")
		}
		return playground.Scope{ViewerID: auth.SystemAccountID, RemoteAddr: r.RemoteAddr}, nil
	}
}

// playgroundKeyDispatchAdapter 把 playground.DispatchPort 适配到派发执行器：
// 每次派发先 fail-closed 解密所选 Key 并校验归属（FindSecret 以非 admin 语义
// pinned to self 读取 + SystemAccountID 复核，双防线），明文注入
// Authorization: Bearer 后直驱 /v1 链 handler。
type playgroundKeyDispatchAdapter struct {
	executor *chatGatewayExecutor
	keys     *apikeys.Store
}

// Dispatch implements playground.DispatchPort.
func (a playgroundKeyDispatchAdapter) Dispatch(ctx context.Context, req playground.DispatchRequest) (*playground.DispatchResponse, error) {
	apiKeyID := strings.TrimSpace(req.APIKeyID)
	viewerID := strings.TrimSpace(req.OwnerID)
	if apiKeyID == "" || viewerID == "" {
		return nil, playground.KeyNotFound()
	}
	// fail-closed（设计 §3.7 真实 Key 化修订）：AccessScope 固定非 admin 语义
	// ——manageableID 收敛为 ViewerID，SQL 按属主过滤；SecretRecord 归属再复核
	// 一道。admin 选他人 Key 同样在此被拒（不泄露存在性）。
	secret, err := a.keys.FindSecret(ctx, apiKeyID, apikeys.AccessScope{ViewerID: viewerID})
	if err != nil {
		// m2 收敛：底层 db/解密实现细节不透用户面（经 dispatchError 直通
		// 502 message 会把 SQLSTATE 等原文外泄），统一通用文案，细节走进程
		// 日志。
		slog.Warn("演练场派发解密所选 Key 失败（用户面收敛通用文案）",
			"apiKeyId", apiKeyID, "viewerID", viewerID, "error", err)
		return nil, playground.DispatchFailed()
	}
	if secret == nil || secret.SystemAccountID != viewerID || strings.TrimSpace(secret.Key) == "" {
		return nil, playground.KeyNotFound()
	}
	headers := make(map[string]string, len(req.Headers)+1)
	for key, value := range req.Headers {
		headers[key] = value
	}
	headers["Authorization"] = "Bearer " + secret.Key
	response, err := a.executor.Dispatch(ctx, GenerationDispatchRequest{
		Path:       req.Path,
		Method:     req.Method,
		Headers:    headers,
		Body:       req.Body,
		RemoteAddr: req.RemoteAddr,
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, nil
	}
	return &playground.DispatchResponse{
		Status: response.Status,
		Header: response.Header,
		Body:   response.Body,
	}, nil
}

// playgroundKeyModelsAdapter 实现 playground.KeyModelsPort（§3.2）：runtime
// cache 读 Key 行（owner + GroupBindings，归属 fail-closed 与派发适配层同口
// 径）→ 激活绑定（超级分组按成员组展开）→ 分组模型并集 + 成员 vendor 目录行
// → 六类目录分类 → 提供账户能力域复核（候选账户快照经
// ListCachedOpenAIAccountsForGroupAsync，域归一与模型清单判定与 preflight 域
// 闸门账户侧同口径）→ (modelId, kind) 去重单行（providerCode 按 scope rank →
// 字典序确定性消歧）。v1.3 UI 口径无计价投影。runtime 读为空时经
// apikeys.FindDetail 第二跳区分「停用/过期 Key（404，与 /v1 状态门同口径）」
// 与「有效但无分组绑定（§3.2 空 models）」。
type playgroundKeyModelsAdapter struct {
	cache *gatewayruntimecache.Service
	keys  *apikeys.Store
}

// ListKeyModels implements playground.KeyModelsPort.
func (a playgroundKeyModelsAdapter) ListKeyModels(ctx context.Context, viewerID, apiKeyID string) ([]playground.KeyModelRow, *playground.HTTPError) {
	if a.cache == nil {
		return nil, &playground.HTTPError{Status: http.StatusInternalServerError, Code: "playground_endpoint_unavailable", Message: "模型候选查询失败"}
	}
	apiKeyID = strings.TrimSpace(apiKeyID)
	viewerID = strings.TrimSpace(viewerID)
	if apiKeyID == "" || viewerID == "" {
		return nil, playground.KeyNotFound()
	}
	ctx, cancel := context.WithTimeout(ctx, chainCatalogLookupTimeout)
	defer cancel()
	runtime, err := a.cache.ReadGatewayRuntimeByAPIKeyIDAsync(ctx, apiKeyID)
	if err != nil {
		return nil, &playground.HTTPError{Status: http.StatusInternalServerError, Code: "playground_endpoint_unavailable", Message: "模型候选查询失败"}
	}
	// fail-closed：Key 行非本人一律 404（含 admin 选他人 Key，不泄露存在性）。
	if runtime.APIKey != nil && runtime.APIKey.SystemAccountID != viewerID {
		return nil, playground.KeyNotFound()
	}
	// runtime 读为空 ≠ Key 不存在：停用/过期 Key 与「有效但无分组绑定」都收敛
	// 为 nil 行。前者 fail-closed 404（状态门与 /v1 同口径），后者经
	// FindDetail 归属复核后按 §3.2 返回空 models（不回退匿名全量）。
	if runtime.APIKey == nil {
		detail, err := a.keys.FindDetail(ctx, apiKeyID, apikeys.AccessScope{ViewerID: viewerID})
		if err != nil {
			slog.Warn("演练场候选查询复核 Key 详情失败（用户面收敛通用文案）",
				"apiKeyId", apiKeyID, "viewerID", viewerID, "error", err)
			return nil, &playground.HTTPError{Status: http.StatusInternalServerError, Code: "playground_endpoint_unavailable", Message: "模型候选查询失败"}
		}
		if detail == nil || detail.Status != "active" || playgroundKeyDetailExpired(detail.ExpiresAt, time.Now()) {
			return nil, playground.KeyNotFound()
		}
		return []playground.KeyModelRow{}, nil
	}
	systemAccountID := runtime.APIKey.SystemAccountID
	bindings := playgroundGatewayModelsBindings(runtime.APIKey)
	if len(bindings) == 0 {
		return []playground.KeyModelRow{}, nil
	}
	groupIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		groupIDs = append(groupIDs, binding.GroupID)
	}
	union, err := a.cache.ListCachedGroupModelUnionAsync(ctx, gatewayruntimecache.GroupModelUnionListOptions{
		CallerSystemAccountID: systemAccountID,
		GroupIDs:              groupIDs,
	})
	if err != nil {
		return nil, &playground.HTTPError{Status: http.StatusInternalServerError, Code: "playground_endpoint_unavailable", Message: "模型候选查询失败"}
	}
	// 提供账户快照按绑定分组装载（runtime cache 候选账户，与 preflight 能力
	// 域闸门账户侧同源；跨分组按账户 ID 去重）。装载失败降级 500（与目录读
	// 取失败同口径，不静默放大或缩小候选面）。
	accounts := []playground.KeyCandidateAccount{}
	seenAccounts := map[string]bool{}
	for _, groupID := range groupIDs {
		groupAccounts, err := a.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{})
		if err != nil {
			return nil, &playground.HTTPError{Status: http.StatusInternalServerError, Code: "playground_endpoint_unavailable", Message: "模型候选查询失败"}
		}
		for _, account := range groupAccounts {
			if account.ID == "" || seenAccounts[account.ID] {
				continue
			}
			seenAccounts[account.ID] = true
			candidate := playground.KeyCandidateAccount{
				CapabilityDomain: account.CapabilityDomain,
				SupportedModels:  account.SupportedModels,
			}
			for _, mapping := range account.ModelMappings {
				candidate.ModelMappings = append(candidate.ModelMappings, playground.KeyAccountModelMapping{
					SourceModel:   mapping.SourceModel,
					UpstreamModel: mapping.UpstreamModel,
					Enabled:       mapping.Enabled,
				})
			}
			accounts = append(accounts, candidate)
		}
	}
	// 目录行按并集成员 vendor 装载（IncludeUnpriced 与目录读取链同口径；
	// custom 继承已在 loader 内生效），按模型名聚簇供分类与消歧消费。
	type catalogEntry struct {
		providerCode string
		scope        string
		protocols    []string
	}
	byModel := map[string][]catalogEntry{}
	for _, vendor := range union.Vendors {
		catalog, err := a.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
			ProviderCode:    vendor,
			IncludeUnpriced: true,
			SystemAccountID: systemAccountID,
		})
		if err != nil {
			return nil, &playground.HTTPError{Status: http.StatusInternalServerError, Code: "playground_endpoint_unavailable", Message: "模型候选查询失败"}
		}
		for _, item := range catalog {
			if item.Status != "active" || strings.TrimSpace(item.Model) == "" {
				continue
			}
			byModel[item.Model] = append(byModel[item.Model], catalogEntry{providerCode: vendor, scope: item.Scope, protocols: item.SupportedAPIProtocols})
		}
	}
	// 分类 × 提供账户复核 × (modelId, kind) 单行消歧（§3.2：目录声明 × 提供
	// 账户能力域；providerCode 取 scope rank 高者再字典序小者）。
	rows := []playground.KeyModelRow{}
	providerEntries := map[string][]playground.KeyRowProviderEntry{}
	order := []string{}
	for _, model := range union.Models {
		for _, entry := range byModel[model] {
			options := playground.ClassifyKeyCatalogModels(entry.providerCode, []playground.CatalogItem{
				{Model: model, ProviderCode: entry.providerCode, SupportedAPIProtocols: entry.protocols},
			})
			for _, option := range options {
				if !playground.KeyModelProvidedByAccounts(option.Kind, model, accounts) {
					continue
				}
				key := option.ModelID + "@" + string(option.Kind)
				if _, hit := providerEntries[key]; !hit {
					order = append(order, key)
				}
				providerEntries[key] = append(providerEntries[key], playground.KeyRowProviderEntry{
					ProviderCode: entry.providerCode,
					Scope:        entry.scope,
				})
			}
		}
	}
	for _, key := range order {
		modelID, kind, _ := strings.Cut(key, "@")
		providerCode, ok := playground.SelectKeyRowProvider(providerEntries[key])
		if !ok {
			continue
		}
		rows = append(rows, playground.KeyModelRow{
			ModelID:      modelID,
			Kind:         playground.ModelKind(kind),
			ProviderCode: providerCode,
		})
	}
	return rows, nil
}

// playgroundKeyDetailExpired 报告 Key 详情的过期时间是否已过（ISO 时间解析
// 失败按过期 fail-closed；无过期时间为 false）。
func playgroundKeyDetailExpired(expiresAt *string, now time.Time) bool {
	if expiresAt == nil || strings.TrimSpace(*expiresAt) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, *expiresAt)
	if err != nil {
		return true
	}
	return now.After(parsed)
}

// playgroundGatewayModelsBindings 是 Key 激活绑定到并集装载输入的投影（与
// gatewaypreauth gatewayModelsBindings 同口径：仅 active 绑定，超级分组目标按
// MemberGroupIDs 展开、保持绑定行 priority 序，去重保序）。
func playgroundGatewayModelsBindings(record *gatewayruntimecache.GatewayAPIKeyRow) []gatewayresponse.GatewayModelBinding {
	seen := map[string]bool{}
	bindings := []gatewayresponse.GatewayModelBinding{}
	appendGroup := func(groupID string) {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" || seen[groupID] {
			return
		}
		seen[groupID] = true
		bindings = append(bindings, gatewayresponse.GatewayModelBinding{GroupID: groupID})
	}
	for _, binding := range record.GroupBindings {
		if binding.Status != "active" {
			continue
		}
		if binding.GroupKind == "super" {
			for _, memberID := range binding.MemberGroupIDs {
				appendGroup(memberID)
			}
			continue
		}
		appendGroup(binding.GroupID)
	}
	return bindings
}

// playgroundImageCostAdapter 实现 playground.ImageCostPort（§3.4.2 扣费计算）：
// 目录行 → chainCatalogPricing → pricing.BuildCostBreakdown 本地计算（image_
// generation 按张/按 token 两形态与统一媒体模板档位单价都由引擎分支承担；
// AccountChargeUsd 缺失 → false → cost_usd=null，不虚构金额）。目录查找键用
// providerCode + 公共目录（SystemAccountID 空）：演练场生图候选是注册白名单
// 模型（gpt-image-2 / grok-imagine 系），价格在公共目录行；端口签名（§3.4.2
// 任务口径）不携带账户维度。
type playgroundImageCostAdapter struct {
	cache *gatewayruntimecache.Service
}

// ImageCost implements playground.ImageCostPort.
func (a playgroundImageCostAdapter) ImageCost(providerCode, model, size string, imageCount int, inputTokens, outputTokens int64) (float64, bool) {
	if a.cache == nil || strings.TrimSpace(providerCode) == "" || strings.TrimSpace(model) == "" || imageCount <= 0 {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), chainCatalogLookupTimeout)
	defer cancel()
	catalog, err := a.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:               providerCode,
		IncludeInvalidMediaPricing: true,
	})
	if err != nil {
		return 0, false
	}
	item := findChainCatalogItem(catalog, model)
	if item == nil {
		return 0, false
	}
	row := chainCatalogPricing(item)
	if row == nil {
		return 0, false
	}
	input := pricing.CostInput{
		ProviderCode:     row.ProviderCode,
		Model:            row.Model,
		OutputImageCount: chainTokensFloat(float64(imageCount)),
	}
	// 档位按实际发送 size 归桶（1k/2k/4k）；size 空（上游默认）不设档，由引
	// 擎按模板 baseTier 处理。
	if width, height, ok := mediapricing.ParseSize(size); ok {
		if tier, tierOK := mediapricing.ImageTierForSize(width, height); tierOK {
			input.ResolutionTier = tier
		}
	}
	// usage 按需传入（缺省 0 不设指针，避免 hasUnpricedUsage 误判）；image_
	// generation 的按张置零逻辑由引擎分支承担（billing.go 同口径）。
	if inputTokens > 0 {
		input.InputTokens = chainTokensFloat(float64(inputTokens))
	}
	if outputTokens > 0 {
		input.OutputTokens = chainTokensFloat(float64(outputTokens))
	}
	breakdown := pricing.BuildCostBreakdown(row, input)
	if breakdown == nil || breakdown.AccountChargeUsd == nil {
		return 0, false
	}
	return *breakdown.AccountChargeUsd, true
}
