package main

import (
	"errors"
	"strings"
)

// gatewaychain.go is the G20 phase-1 registry for the AI gateway /v1 chain
// (Node server.ts gateway section + openAIGatewayRouter). The Go packages of
// every chain slice are already migrated and unit-green:
//
//	gatewaybody / gatewayproto / gatewayopenai / gatewayanthropic /
//	gatewaygemini (G01-G04, G06), gatewayruntimecache + inval (K5),
//	gatewayrouting + gatewayhybrid (G08/G09), gatewaycircuit (G11),
//	gatewayaccounteffects (G12), gatewayclientip / gatewayhotquality /
//	gatewayproxyhealth (G13), gatewaysession (G14), gatewaydispatch (G15),
//	gatewayresponse (G16), gatewayusage (G17), gatewaycodex (G18),
//	gatewayobs (G19), ratelimit, legacybridge.
//
// What the composition root must additionally AUTHOR before the chain can
// serve traffic (the orchestration freezes these as ports whose concrete
// adapters "belong to the composition root" per the package docs):
//
//  1. gatewaypreauth.RouteResolver adapter (resolverport.go): translate
//     gatewayrouting.NormalModelRouteService / gatewayhybrid.RouteService
//     results into the preauth Normal/HybridRouteResult contract.
//  2. gatewaydispatch.ProviderDriver registry adapter (engine.go): the
//     providers/drivers/registry.ts surface over gatewayopenai /
//     gatewayanthropic / gatewaygemini protocol packages.
//  3. gatewaypreauth.GatewayAPIKeyValidator adapter (service.go): the models
//     fast-path raw key validation over gatewayruntimecache.
//  4. gatewaypreauth.ImagePermissionPreflight implementation (ports.go):
//     request/image-permission-preflight.ts has no Go owner yet.
//  5. gatewayusage.UsageRecorder durable writer: the J-F writer lives in the
//     jobs module and the three-project baseline forbids cross-module imports;
//     the gateway needs its own spool-backed writer assembly (G17 doc).
//  6. The top-level /v1 HTTP orchestrator: Node handleOpenAIGatewayRequest
//     second half (preflight result -> engine dispatch loop -> response
//     piping -> finalization) has no exported Go entry yet; the engine
//     internals (dispatchSingleAccount / runUpstreamAttemptLoop) and the
//     response pipes exist but the glue is the flip deliverable.
//
// Until every entry above is authored, enabling the chain fails fast instead
// of serving traffic through nil ports; /v1 traffic stays on the Node origin
// through the legacy bridge.
var chainMissingAdapters = []string{
	"gatewaypreauth.RouteResolver 组合适配器（gatewayrouting/gatewayhybrid -> preauth 契约）",
	"gatewaydispatch.ProviderDriver 注册表适配器（gatewayopenai/anthropic/gemini 协议包）",
	"gatewaypreauth.GatewayAPIKeyValidator 适配器（models 快路径原始密钥校验）",
	"gatewaypreauth.ImagePermissionPreflight 实现（request/image-permission-preflight.ts）",
	"gatewayusage.UsageRecorder 持久 writer（jobs 模块边界内不可导入，需网关侧 spool writer）",
	"/v1 顶层编排器（handleOpenAIGatewayRequest 后半段：dispatch 循环 + 响应管道 + 终态）",
}

// gateGatewayChain implements the phase-1 chain gate: enabled=false keeps /v1
// on the legacy bridge; enabled=true fails startup with the explicit missing
// adapter list. Nothing in the chain is ever wired nil and silently served.
func gateGatewayChain(enabled bool) error {
	if !enabled {
		return nil
	}
	return errors.New("AI 网关链尚未满足装配条件，拒绝启动（JUHE_AI_GATEWAY_CHAIN_ENABLED=true）：" +
		strings.Join(chainMissingAdapters, "；"))
}
