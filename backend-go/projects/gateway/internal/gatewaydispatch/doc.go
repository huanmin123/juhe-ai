// Package gatewaydispatch is the G15 work package: the gateway dispatch
// engine (candidate pipeline + upstream attempt lifecycle), the upstream
// HTTP transport and the OpenAI OAuth Codex request adapter, migrated from
// the Node gateway modules:
//
//	backend/src/modules/gateway/dispatch/*   -> candidate pipeline + engine
//	backend/src/modules/gateway/upstream/*   -> transport + body piping
//	backend/src/modules/gateway/adapters/gpt-codex/* -> codex adapter
//
// Contract authority is git HEAD of the Node tree; every branch, hook point,
// retry classification and user-facing Chinese message below mirrors the
// Node implementation.
//
// This package owns no provider-anchor content: sessionProviderAnchor /
// ProviderAnchor and related symbols that appeared in the Node working tree
// are NOT part of git HEAD and are deliberately absent here.
//
// Composition notes for G20:
//
//   - The exported CandidatePipeline implements gatewaypreauth.CandidatePipeline
//     (compile-time asserted in ports.go).
//   - Collaborators owned by other slices arrive as the port interfaces in
//     ports.go; each port mirrors the consumed surface of the corresponding
//     Node service (the doc comment names the source file).
//   - The transport uses shared/platform/upstreamhttp for connection pooling
//     and proxies; SSE / streaming semantics mirror upstream/request.ts.
package gatewaydispatch
