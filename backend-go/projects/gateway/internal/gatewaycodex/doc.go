// Package gatewaycodex is the G18 slice of the W4-W5 gateway chain: the codex
// client profile family and the native Responses compaction contract.
//
// It mirrors backend/src/modules/gateway:
//
//	codex-responses/contract-types.ts + contract-registry.ts
//	  → contract.go（Codex Responses item 契约注册表）
//	codex-responses/request-history-types.ts + request-history-sanitizer.ts
//	  → historysanitizer.go（历史 item id 清理）
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
//
// 2026-11-07 退役记录：codex-responses/chat-bridge-state.ts（chatbridgestate.go）、
// codex-responses/compact-preflight.ts（compactpreflight.go）与
// storage/codex-context-state.repository.ts（contextstore.go + segments.go
// 分片子面）随跨协议 Responses→Chat 桥整体删除；Responses 客户端只落到原生
// Responses 账户，/responses/compact 由原生上游承接（compaction contract 的
// 用量标记与预算豁免保留）。
//
// The frozen G05 port (gatewaypreauth.ClientStrategy) is implemented by the
// port adapter in ports.go with a compile-time assertion. The availability
// probe coordinator itself is owned by gatewaycircuit (G11) and consumed
// through a narrow seam. All Chinese error and log copy is byte-identical
// with the Node source; all time comes from an injected clock.
package gatewaycodex
