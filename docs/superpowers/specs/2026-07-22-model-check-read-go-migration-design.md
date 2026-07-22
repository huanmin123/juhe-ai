# Model Check Read API Go Migration Design

## Goal

Move only the existing model-check GET surface to the opt-in Go management API while Node remains the sole owner of run creation, stop, streaming, upstream probes, aggregation, retention, and schema.

## Scope

The Go router registers these admin and self paths:

- `GET /__aisys__/api/model-checks/options`
- `GET /__aisys__/api/my-model-checks/options`
- `GET /__aisys__/api/model-checks/run/active`
- `GET /__aisys__/api/my-model-checks/run/active`
- `GET /__aisys__/api/model-checks/runs`
- `GET /__aisys__/api/my-model-checks/runs`
- `GET /__aisys__/api/model-checks/runs/{id}`
- `GET /__aisys__/api/my-model-checks/runs/{id}`

No POST route, SSE route, executor, worker, Redis state, schema migration, cleanup task, observation aggregation, or real upstream call is added.

## Contract Sources

- Node HTTP and scope behavior: `backend/src/modules/model-checks/model-checks.routes.ts`
- Node PostgreSQL read facts and pagination: `backend/src/storage/model-checks.repository.ts`
- Node static options catalog: `backend/src/modules/model-checks/model-checks.profiles.ts` and `model-checks.service.ts`
- Public DTOs: `frontend/src/types/domain/model-checks.ts`

## Architecture

`internal/modules/managementmodelchecks` owns normalization, bounded pagination, frontend-compatible DTOs, options, and row-to-response conversion. `internal/store/port` exposes four read operations: active run by actor, scoped run list, scoped run detail with ordered checks, and the latest preaggregated account trust result. `internal/store/postgres` reads the existing Node-owned `juhe_dataset.model_check_runs` and `model_check_items` tables with parameterized SQL; detail may read exactly one `juhe_stats.model_account_trust_results` primary-key row to match Node's latest trust-report enrichment. It never scans observations, windows, or source facts. `internal/httpapi` supplies admin/self scope, validation, envelope, 404, and internal-error mapping. `internal/app` wires the service and handlers into the existing opt-in management router.

## Scope and ownership

- Admin routes require `admin` or `super_admin`.
- Admin history/detail are global when `systemAccountId` is absent, blank, or `all`; a concrete value narrows `model_check_runs.system_account_id`.
- Self history/detail ignore forged `systemAccountId` and force the authenticated `SystemAccountID`.
- Admin DTOs include `systemAccountId`, `actorSystemAccountId`, and `targetOwnerSystemAccountId`; self DTOs omit those management-only fields.
- Active ownership follows Node's active-run key: the authenticated actor, not the selected resource scope. Go queries the newest Node-written `status = 'running'` row by `actor_system_account_id`; forged or narrowed resource scope does not expose another actor's run. Node's source of truth is still process-local state, so this is a historical read approximation: a newly started run can precede its persisted row and a process failure can leave a stale running row. It is not sufficient to route model-check runtime control to Go.
- `stopRequested` is returned as `false` because the existing PostgreSQL row has no stop-requested field. Node remains the runtime owner and will finalize the row as `canceled`; this migration does not invent shared mutable state.
- Detail retains Node's latest-trust enrichment: unless the stored report records `model_response_evidence_unavailable` (or an unavailable result has no observed model), Go overlays the one latest preaggregated result onto `resultSummary.trustReport`, preserving the stored `requestedModel` when present. Missing or malformed latest data leaves the stored report intact.

## Bounded reads and ordering

- List defaults to `page=1`, `pageSize=20`.
- `pageSize` is clamped to `1..100`.
- The progressive list window is capped at 1001 rows, matching Node; page is clamped to `max(1, floor(1000/pageSize))`.
- The store reads `pageSize + 1` rows and never executes an exact count.
- Runs order by `created_at DESC, id DESC`.
- Checks order by `created_at ASC, id ASC`.
- List queries project no request/result summary JSON. Detail reads one scoped run plus its items only.
- All SQL values are bound parameters; filters are selected from a fixed whitelist.

## DTO and error behavior

- Success uses the existing `{ "data": ... }` envelope.
- Active with no row returns `{ "data": null }`.
- Missing detail returns `404 { "message": "模型检测记录不存在" }`.
- Non-admin access to admin paths returns `403 { "message": "需要管理员权限" }`.
- Invalid repeated or blank strict scope parameters on active/detail return `400 { "message": "查询参数不合法" }`; self paths ignore the query scope.
- Store failures return the generic Chinese internal error and do not leak SQL or JSON parsing details.
- Malformed stored JSON is converted to `{}` as in Node.

## HTTP runtime boundaries

All eight routes use the existing management read middleware, so they inherit IP and authenticated read rate limiting, `Cache-Control: no-store`, and non-touching session authentication. They are only registered when the existing Go management API opt-in is enabled.

## Verification

Unit tests cover options and DTO shape, pagination and filter normalization, active actor ownership, admin/self scope, error semantics, stable SQL ordering, bounded projections, latest trust-result primary-key reads without observation scans, router registration, read limiter use, no-store, and no session touch. The first migration pass runs focused package tests, scoped vet, `go mod tidy -diff`, `gofmt`, and `git diff --check`; repository-wide tests, race checks, and a real PostgreSQL Node-writer-to-Go-reader smoke remain the later unified acceptance gate.
