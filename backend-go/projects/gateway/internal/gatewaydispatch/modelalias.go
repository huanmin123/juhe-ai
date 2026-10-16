package gatewaydispatch

import "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"

// volcengine TTS 占位名↔现役名等价的模型匹配判定：等价组事实与其语义边界
// 单点维护在 gatewayrouting.VolcengineTTSModelNamesMatch（路由选组与派发候选
// 过滤两层镜像实现共用，见 gatewayrouting/modelalias.go 的契约引用）；本包
// 保留原名薄委托，既有调用点（FilterGatewayAccountsByRequestedModel 的直接
// 匹配与映射放行两处）零改动。
func gatewayDispatchModelNamesMatch(requestedModel, supportedModel string) bool {
	return gatewayrouting.VolcengineTTSModelNamesMatch(requestedModel, supportedModel)
}
