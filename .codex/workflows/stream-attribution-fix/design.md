# Stream Attribution Fix Design

- Goal: A stream that has not committed output must return a non-2xx HTTP failure, and a downstream socket close must not be attributed to a user action without proof.
- Scope: Gateway pre-commit retry exhaustion, audit finalization, audit API outcome contract, audit-log UI, focused regression checks, and the relevant behavior documentation.
- Non-goals: Changing committed-stream wire behavior, usage-record compatibility, database schema, deployment, or production data.
- Risk: Medium. The response status is externally visible and audit outcome is a cross-layer contract.
- Rollback: Revert only this scoped diff; no migration or external state change is involved.

## Evidence

- `routes.ts` sends `200` plus an SSE failure event in `sendPreCommitStreamRetryExhaustedResponse` before headers are committed.
- `AuditCaptureContext.flushFinalizedAudit` replaces any failed terminal outcome with `client_aborted` when its close listener ran.
- Node request/response close signals prove a downstream closure, not intentional client action.

## Decision

1. Before headers commit, return the standard gateway JSON failure response with HTTP `503`, preserving the stream-failure audit root cause.
2. Record downstream closure separately as `gateway_metadata` with `trigger: unknown_unproven`; use `downstream_closed` only when no prior terminal root failure/error source exists.
3. Retain `markClientAborted` as a compatibility alias, but route lifecycle listeners call a neutral `markDownstreamClosed` method.
4. Add `downstream_closed` to storage/API/frontend contracts and render both it and legacy `client_aborted` records with neutral wording.
5. UI status display treats `success=false` as semantic failure even when transport status is `200`.

## Acceptance

- Pre-commit retry exhaustion is HTTP 503 and not an SSE 200 response.
- A prior `stream_failed`, `upstream_failed`, `gateway_failed`, or error source survives a later close unchanged.
- Close-only audit records contain `downstream_closed`, downstream error fields, and neutral metadata.
- Audit filters/types/rendering support `downstream_closed`; legacy `client_aborted` wording is neutral.
