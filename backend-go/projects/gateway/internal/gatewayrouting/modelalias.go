package gatewayrouting

import "strings"

// volcengine TTS 占位名↔现役名等价组：历史占位名 doubao-tts（2026-10-05 目录
// 占位行）与官方现役名 seed-audio-1.0（2026-10-17 迁移批现役行）指同一 TTS
// 模型能力。出站 adapter 已把占位名归一映射为现役名（gatewaymedia/volcengine_tts.go
// 的 VolcengineTTSPlaceholderModel → VolcengineTTSCurrentModel）；模型匹配把
// 两组名字互相视为同一能力（「派发匹配等价」，契约
// 《媒体上游协议契约与Mock上游规格》§9.2 与 docs/functions/火山方舟账号接入.md
// 「TTS 目录行演进」节），与 adapter 归一同构——账户 supportedModels 声明任一
// 名字时，两种名字的请求都能命中，存量占位账户对现役名请求、现役名账户对
// 占位名请求两方向都不再断代。
//
// 本事实落在本包（gatewayrouting）：路由选组（FilterAccountsByRequestedModel）
// 与派发候选过滤（gatewaydispatch.FilterGatewayAccountsByRequestedModel，本包
// 的下游依赖方）两层镜像实现共用同一判定，避免双份定义漂移；候选窗口 SQL
// （chain_accounts.go direct 谓词经 VolcengineTTSModelAliasVariantsOf 展开）、
// driver 能力判定链（chain_driver.go）与切号承接（gatewaydispatch/switchtarget.go）
// 共五层同构消费。
//
// 匹配判定不含 providerCode 门：等价组两个名字仅存在于 volcengine 档案的
// 目录行，其他供应商账户不会声明同名模型（复审建议-7 记录），如未来出现
// 跨供应商同名模型需在此补 provider 维度。
//
// 边界：
//   - 组内名字按 EqualFold 语义比较（TrimSpace + strings.EqualFold），大小写
//     不敏感；
//   - 等价组之外的模型匹配行为零变化（既有严格相等语义保持，不做大小写归一）；
//   - 等价只作用于匹配环节；modelMappings 的映射表键解析
//     （gatewayopenai.ResolveAccountModelMapping）保持精确匹配，不享受等价。
//
// 名字取值与 gatewaymedia 两个常量同源（契约 §9.2 目录行），本包不为此引入对
// gatewaymedia 的依赖，两侧以契约为准保持一致。
var volcengineTTSModelAliasGroup = [2]string{
	"doubao-tts",     // 历史占位名（gatewaymedia.VolcengineTTSPlaceholderModel）
	"seed-audio-1.0", // 官方现役名（gatewaymedia.VolcengineTTSCurrentModel）
}

// isVolcengineTTSModelAliasMember 判定 model（TrimSpace 后）是否按 EqualFold
// 语义命中 volcengine TTS 等价组成员。
func isVolcengineTTSModelAliasMember(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return false
	}
	for _, name := range volcengineTTSModelAliasGroup {
		if strings.EqualFold(trimmed, name) {
			return true
		}
	}
	return false
}

// VolcengineTTSModelNamesMatch 判定请求侧模型名（客户端请求模型或映射产出
// 的 upstream 模型）与声明侧模型名（supportedModels 成员）是否匹配：先保持
// 既有严格相等语义（等价组之外零变化），再叠加 volcengine TTS 占位名↔现役名
// 等价组（组内按 EqualFold 语义）。导出供 gatewaydispatch 的派发候选过滤
// 复用（同一匹配语义两层镜像）。
func VolcengineTTSModelNamesMatch(requestedModel, supportedModel string) bool {
	if requestedModel == supportedModel {
		return true
	}
	return isVolcengineTTSModelAliasMember(requestedModel) &&
		isVolcengineTTSModelAliasMember(supportedModel)
}

// VolcengineTTSModelAliasVariantsOf 返回请求模型的等价变体列表，供候选窗口
// 的 SQL 谓词展开（direct_models.model IN (?, ...)）：等价组内模型返回组内
// 全部规范名成员（账户 supportedModels 声明任一名字都命中 direct 档）；组外
// 模型返回仅含自身的单元素列表（谓词退化为 `= ?` 精确匹配，行为零变化）。
// 变体取规范名（组内字面量），与 account_supported_models 表的声明形态一致。
func VolcengineTTSModelAliasVariantsOf(requestedModel string) []string {
	trimmed := strings.TrimSpace(requestedModel)
	if !isVolcengineTTSModelAliasMember(trimmed) {
		return []string{requestedModel}
	}
	return volcengineTTSModelAliasGroup[:]
}
