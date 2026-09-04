// Package gatewaycodex is the G18 slice of the W4-W5 gateway chain: the
// codex client profile family and the Responses↔Chat completions bridge.
//
// It mirrors backend/src/modules/gateway:
//
//	codex-responses/contract-types.ts + contract-registry.ts
//	  → contract.go（Codex Responses item 契约注册表）
//	codex-responses/request-history-types.ts + request-history-sanitizer.ts
//	  → historysanitizer.go（历史 item id 清理）
//	codex-responses/chat-bridge-state.ts
//	  → chatbridgestate.go（会话状态 preflight / 恢复 / 保存 / compact snapshot）
//	codex-responses/compact-preflight.ts
//	  → compactpreflight.go（桥摘要 compact preflight）
//	codex-responses/web-search-executor.ts
//	  → 无对应实现（Node 已标记 retired，正文为空导出）
//	client-profiles/source-identity.ts
//	  → sourceidentity.go（来源身份 + HMAC 状态键）
//	client-profiles/strategy.ts
//	  → strategy.go（客户端画像 / 下行协议 / 重试协调）
//	client-profiles/codex-turn-retry.service.ts
//	  → turnretry.go（codex turn 失败避让：内存 + Redis 双驱）
//	client-profiles/codex-turn-availability-probe.service.ts
//	  → turnprobe.go（fence 探活交接与结算，消费 gatewaycircuit.ProbeCoordinator）
//	client-profiles/client-source-avoidance.service.ts + client-source-availability-probe.service.ts
//	  → sourceavoidance.go（仅改名的 re-export，Go 侧以别名呈现）
//	request/codex-encrypted-content-recovery.ts
//	  → encryptedcontent.go（上游拒绝加密上下文后的一次性兼容清理）
//	response/codex-compaction-contract.ts
//	  → compactioncontract.go（codexCompactionExpectedForRequest + compaction trigger 扫描）
//	runtime/account-effects.ts persistOpenAICodexHeadersIfNeeded
//	  → codexheaders.go（OAuth codex 用量响应头识别与副作用投递）
//	storage/codex-context-state.repository.ts（按 import 链）
//	  → contextstore.go（响应/compact 索引 store：sqlitepath 分片 + sqlpool postgres 双模）
//
// The frozen G05 ports (gatewaypreauth.ClientStrategy /
// gatewaypreauth.CodexBridgePreflight) are implemented by the port adapters
// in ports.go with compile-time assertions. The availability probe
// coordinator itself is owned by gatewaycircuit (G11) and consumed through a
// narrow seam; the upstream summary dispatch of the compact preflight is a
// seam the dispatch slice wires (Node fetchFirstAvailableUpstream). All
// Chinese error and log copy is byte-identical with the Node source; all
// time comes from an injected clock.
package gatewaycodex
