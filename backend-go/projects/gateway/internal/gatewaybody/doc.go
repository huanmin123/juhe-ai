// Package gatewaybody is the G06 slice of the W4-W5 gateway chain: the
// gateway request body pipeline.
//
// It mirrors backend/src/modules/gateway/request:
//
//   - body.go            <- request/body.ts (limits, body state, in-flight bytes limiter)
//   - middleware.go      <- request/body-middleware.ts plus the express.raw
//     boundary wired in server.ts (wrapGatewayRawBodyParser /
//     handleGatewayRawBodyError) and the Content-Length admission
//   - jsonmetadata.go    <- request/json-metadata-scanner.ts (byte-level scanner)
//   - jsonparser.go      <- request/json-parser.ts + request/json-worker.ts
//   - multipart.go       <- request/multipart-image-metadata.ts
//   - serialized.go      <- request/serialized-json-body.ts
//   - imagetools.go      <- the subset of request/image-generation-tools.ts that
//     body.ts consumes (inspection + downgrade + re-exports)
//   - usageoptions.go    <- the usage/reasoning-effort.ts + usage/service-tier.ts
//     capability-token normalizers reachable from body state creation
//
// Approved architecture adaptation: Node parses large JSON bodies in
// worker_threads; Go parses in-process with a bounded goroutine pool (size /
// queue / byte caps / timeout / context cancellation). Externally observable
// behavior — parse results, error copy, size limits, timeouts — stays aligned
// with the Node pipeline. metadata.ts belongs to G05 (gatewaypreauth) and the
// codex normalize worker job belongs to the codex adapter slice; neither is
// part of this package.
//
// Known micro-divergences of the in-process parser (engine-level edges, all
// outside the contract surface):
//
//   - Escaped lone surrogates in JSON strings are kept by V8 JSON.parse but
//     replaced with U+FFFD by Go's string unquoting; they can only surface
//     inside decoded metadata strings.
//   - Full materialization of documents nested deeper than Go's decoder
//     limit (10000 levels) fails where V8 may succeed up to its stack limit;
//     the capture-time scanner has no depth limit in either implementation,
//     so jsonParseStatus stays identical.
//   - Parser Stop() rejects queued jobs with the Node shutdown copy; a job
//     already executing finishes in the background and its result is
//     delivered, where Node terminates the worker thread.
package gatewaybody
