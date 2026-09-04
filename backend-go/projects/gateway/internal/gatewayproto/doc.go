// Package gatewayproto is the G01 slice of the W4-W5 gateway chain: the
// protocol driver contract plus the driver registry.
//
// It mirrors the Node contract
// backend/src/modules/gateway/protocols/_shared/types.ts and
// backend/src/modules/gateway/protocols/registry.ts. A ProtocolDriver owns
// everything protocol-specific about proxying one upstream dialect:
//
//   - MatchPath: recognize native client request paths,
//   - BuildUpstreamRequest: transform a client request into an upstream
//     request (endpoint mode, request lane, model mapping),
//   - NewStreamInspector: incremental SSE stream inspection,
//   - InspectResponse: buffered (non-stream) response inspection,
//   - ExtractUsage*: usage extraction from JSON bodies / values / fragments,
//   - ParseErrorPayload: upstream error body normalization.
//
// The package deliberately carries no openai/anthropic/gemini specific
// logic; concrete drivers live in sibling packages (G02:
// internal/gatewayopenai) and register into a Registry. It also hosts the
// protocol-agnostic shared vocabulary of the chain: ParsedUsage, semantic
// frames, stream inspection snapshots and the frozen four-way outcome
// classification (complete_success / framing_complete_neutral /
// upstream_failure / probe_task_failure).
package gatewayproto
