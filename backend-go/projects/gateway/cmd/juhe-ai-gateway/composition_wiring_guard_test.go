// 组合根接线守卫（composition wiring guard）。
//
// 守卫语义：这是「注释宣称组合根已接线、但组合根源码中从未出现该调用」
// 这一类缺陷的 CI 闸门。2026-09-20 专项审计发现多个端口在生产组合根静默
// 空转（模型目录校验、授权可见性、冷却设置、策略失效、codex 请求覆盖、
// circuit 观测、key-model 健康检查派发等），本测试把「组合根源码必须出现
// 对应接线标识符」固化为可执行断言：遍历本目录（cmd/juhe-ai-gateway）下
// 所有非 _test.go 的 .go 文件拼成源码池，下方清单中每个标识符都必须在
// 源码池中出现，缺失即测试失败。
//
// 有意移除或更名某个端口时，必须同步增删 compositionWiringGuardEntries
// 清单，否则测试失败；失败信息中的「缺失影响」即该端口的用户可见语义，
// 移除前请先确认业务上确实不再需要。
//
// 边界：本测试是纯源码文本断言，不编译、不运行被测代码；它只堵「端口
// 零生产调用」这一类缺口。接线的具体行为语义由各 *_wiring_test.go 等
// 行为级测试承担，二者互补。

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compositionWiringGuardEntry 是一条组合根接线守卫清单项。
type compositionWiringGuardEntry struct {
	// Identifier 是必须出现在组合根源码池中的字面标识符片段（含调用括号
	// 等上下文以避免命中纯注释里的提及）。
	Identifier string
	// Name 是守卫条目名。
	Name string
	// Purpose 是该接线端口的作用。
	Purpose string
	// MissingImpact 是缺失时的用户可见影响，失败信息直接引用。
	MissingImpact string
}

// compositionWiringGuardEntries 是 gateway 组合根（cmd/juhe-ai-gateway）
// 必须存在的接线清单。新增受审计约束的组合根端口时，请在这里同步加一行。
var compositionWiringGuardEntries = []compositionWiringGuardEntry{
	{
		Identifier:    "SetModelCatalogReader(",
		Name:          "模型目录校验端口",
		Purpose:       "把账户模型目录校验接到 model catalog 读取（compose.go accountModelCatalogReaderAdapter）",
		MissingImpact: "账户默认模型/模型目录校验静默空转，目录数据错误不再被拦截，用户可能把不存在的模型配置成账户默认模型，直到真实请求打到上游才报错",
	},
	{
		Identifier:    "SetRuntimeCooldownSettings(",
		Name:          "rate_limited 冷却设置端口",
		Purpose:       "账户存储从设置读取 rate_limited 运行时冷却时长（compose.go runtimeCooldownSettingsAdapter）",
		MissingImpact: "账户触发 429 后的冷却节奏退化为内置默认值，用户看到的冷却/恢复时间与管理端配置的冷却设置不一致",
	},
	{
		Identifier:    "SetValidationCacheInvalidator(",
		Name:          "策略路由后 API-Key 校验缓存失效端口",
		Purpose:       "路由策略/分组变更后失效 API-Key 校验缓存（compose.go gatewayAPIKeyValidationBusInvalidator）",
		MissingImpact: "路由策略或分组绑定变更后校验缓存不失效，权限与路由调整延迟生效或继续按旧策略放行，用户请求被错误路由或错误拒绝",
	},
	{
		Identifier:    "SetSpeedFirstRuntimeFacade(",
		Name:          "speed-first runtime 端点与降级清理端口",
		Purpose:       "把速度优先路由的运行时端点与延迟降级清理挂到 routestrategies Deps（compose.go routeStrategySpeedFirstFacade）",
		MissingImpact: "speed-first 路由模式缺少运行时端点与降级清理接线，速度优先请求退化或降级清理不执行，用户遇到模式不生效或残留脏状态",
	},
	{
		Identifier:    "SetGptRequestOverrideModelCatalog(",
		Name:          "codex 请求覆盖目录端口",
		Purpose:       "为 codex/gpt 请求覆盖提供模型目录依据（chain_driver.go）",
		MissingImpact: "codex 请求覆盖没有模型目录依据，覆盖后的模型名无法校验与映射，codex 请求可能打到上游不存在的模型而报错",
	},
	{
		Identifier:    "SetGptRequestOverrideModelCandidates(",
		Name:          "codex 请求覆盖候选端口",
		Purpose:       "为 codex/gpt 请求覆盖提供候选模型展开（chain_driver.go）",
		MissingImpact: "codex 请求覆盖缺少候选模型展开，覆盖目标候选为空，请求覆盖静默不生效",
	},
	{
		// SetAuthorizedReader 的生产调用位于 internal/accounts/routes.go 的
		// Mount 流程，不在 cmd 目录；组合根这一侧的接线形态是 compose.go 中
		// accounts.Deps 字面量里的 Authorized: authzStore 字段注入。
		Identifier:    "Authorized:",
		Name:          "授权可见性注入（accounts.Deps.Authorized）",
		Purpose:       "compose.go 的 accounts.Deps 字面量必须显式注入授权读取存储（Authorized: authzStore），对应 internal/accounts Store.SetAuthorizedReader",
		MissingImpact: "Authorized 恒零值（等价 SetAuthorizedReader(nil)），被授权人视角的实例账户投影整体缺失，被授权管理员看不到本应可见的账户",
	},
	{
		Identifier:    "SetObservabilitySink(",
		Name:          "circuit 观测 sink 端口",
		Purpose:       "把账户电路服务的观测输出接到链路观测（chain_obs_wiring.go）",
		MissingImpact: "账户熔断（circuit）无观测输出，电路开合在管理面与日志中不可见，故障排查失去熔断视图",
	},
	{
		Identifier:    "SetDispatcher(",
		Name:          "key-model attempt 健康检查派发端口",
		Purpose:       "经 chainKeyModelAdmission 把健康检查派发挂到每个 admitted attempt（chain_wiring_w2c.go preparation.Attempt.SetDispatcher）",
		MissingImpact: "GatewayKeyModelAttempt.SetDispatcher 无生产调用，key-model 失败后的健康检查派发静默丢失，坏账户不能被及时标记或恢复",
	},
	{
		Identifier:    "KeyModelHealthDispatch",
		Name:          "chainRuntimeDeps.KeyModelHealthDispatch 字段",
		Purpose:       "组合根把 runtime-reset bridge 适配为健康检查派发器（chainKeyModelHealthDispatcher）并注入 chainRuntimeDeps，是上一条 attempt 级派发的装配源头",
		MissingImpact: "健康检查派发装配链在组合根断裂，attempt 级 SetDispatcher 必然拿到 nil，效果等同上一条缺口",
	},
}

// TestCompositionWiringGuard 断言 gateway 组合根源码池包含全部接线清单项。
// 测试名是外部验证入口，请勿改名。
func TestCompositionWiringGuard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取组合根目录失败: %v", err)
	}
	var pool strings.Builder
	fileCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("读取组合根源码文件 %s 失败: %v", name, err)
		}
		pool.Write(source)
		fileCount++
	}
	sourcePool := pool.String()
	// 防御性自检：源码池必须像组合根（package main 且非空），否则下面循环
	// 会在错误路径上静默全绿。
	if fileCount == 0 || !strings.Contains(sourcePool, "package main") {
		t.Fatalf("组合根源码池异常（文件数=%d，缺失 package main）：请确认测试运行目录是 cmd/juhe-ai-gateway", fileCount)
	}
	for _, guard := range compositionWiringGuardEntries {
		if strings.Contains(sourcePool, guard.Identifier) {
			continue
		}
		t.Errorf("组合根接线守卫失败 [%s]：标识符 %q 未出现在 cmd/juhe-ai-gateway 的任何非测试 .go 源码中。端口作用：%s。缺失时的用户可见影响：%s。若属有意移除该端口，必须同步更新本测试的 compositionWiringGuardEntries 清单；否则请在组合根补上对应接线。",
			guard.Name, guard.Identifier, guard.Purpose, guard.MissingImpact)
	}
}
