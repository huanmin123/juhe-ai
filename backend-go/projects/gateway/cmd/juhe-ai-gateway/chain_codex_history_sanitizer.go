package main

import "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"

// wireCodexHistorySanitizer 安装生产 Codex/Responses 历史脱敏实现（D-93）。
// 独立成文件的原因：main.go 的 `gatewaydispatch` 标识符别名指向
// internal/business/gateway_dispatch，不能直接引用内部 gatewaydispatch 包。
func wireCodexHistorySanitizer() {
	gatewaydispatch.WireCodexHistorySanitizer()
}
