# Model Check Read API Go Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the eight admin/self model-check GET routes to the opt-in Go management API without moving any model-check writer or runtime executor.

**Architecture:** A focused management-model-check service converts existing PostgreSQL facts into the frontend DTO. The PostgreSQL adapter performs only bounded, parameterized reads from Node-owned tables, while HTTP handlers enforce admin/self scope and use the existing read authentication and rate-limit middleware.

**Tech Stack:** Go 1.24, net/http, chi, pgx, existing Store Port/Adapter architecture, Go testing.

---

### Task 1: Freeze the service contract

**Files:**
- Create: `backend-go/internal/modules/managementmodelchecks/service_test.go`
- Create: `backend-go/internal/store/port/management_model_checks.go`

- [ ] Write failing tests for the exact options DTO, admin/self field projection, actor-owned active DTO, malformed JSON fallback, filters, and `20/100/1001` progressive pagination.
- [ ] Run `go test ./internal/modules/managementmodelchecks -count=1` and confirm RED because the package implementation does not exist.
- [x] Add only the port facts required by active/list/detail reads and the latest preaggregated trust-result overlay.

### Task 2: Implement the service

**Files:**
- Create: `backend-go/internal/modules/managementmodelchecks/service.go`

- [ ] Implement the static model catalog and exact profile/trusted-comparison DTO.
- [ ] Implement list normalization and `pageSize+1` inputs without exact count.
- [x] Implement summary/detail/check conversion with management-only owner fields and safe JSON object parsing.
- [x] Overlay the Node-owned latest preaggregated trust result for eligible detail reports without scanning observations.
- [x] Run `go test ./internal/modules/managementmodelchecks -count=1` and confirm GREEN.

### Task 3: Freeze and implement PostgreSQL reads

**Files:**
- Create: `backend-go/internal/store/postgres/managementmodelchecks_test.go`
- Create: `backend-go/internal/store/postgres/managementmodelchecks.go`

- [ ] Write failing SQL contract tests for `juhe_dataset`, fixed projection, parameter placeholders, admin global/narrow/self scope, actor-owned active selection, `created_at/id` ordering, and bounded limit/offset.
- [ ] Run `go test ./internal/store/postgres -run ManagementModelCheck -count=1` and confirm RED.
- [x] Implement active/list/detail reads, latest trust-result primary-key read, and row scanners with no write statement or schema change.
- [x] Run the target store tests and confirm GREEN.

### Task 4: Freeze and implement HTTP behavior

**Files:**
- Create: `backend-go/internal/httpapi/management_model_checks_test.go`
- Create: `backend-go/internal/httpapi/management_model_checks.go`
- Modify: `backend-go/internal/httpapi/router.go`

- [ ] Write failing handler tests for admin/global/narrow, self-forced scope, active actor ownership, DTO envelope, null active, 404, generic 500, and strict scope errors.
- [ ] Write failing router tests proving all eight GET routes are opt-in, no-store, authenticated read-limited, and use `AuthenticateCookie` without `AuthenticateCookieAndTouch`.
- [ ] Run `go test ./internal/httpapi -run ModelCheck -count=1` and confirm RED.
- [x] Implement handlers and register only the eight GET routes through `managementAPIReadRateLimitMiddleware`.
- [x] Run the target HTTP tests and confirm GREEN.

### Task 5: Wire the application

**Files:**
- Modify: `backend-go/internal/app/server.go`
- Modify: `backend-go/internal/app/server_test.go`

- [ ] Add failing app/router assertions that production handler construction exposes the model-check readers only when management routes are enabled.
- [x] Instantiate the service with the existing PostgreSQL store and wire eight handler slots.
- [x] Run `go test ./internal/app -run ModelCheck -count=1` and confirm GREEN.

### Task 6: Review and verify

**Files:**
- Modify: `docs/superpowers/specs/2026-07-22-model-check-read-go-migration-design.md` only if implementation evidence requires clarification.

- [x] Review the diff against every scope exclusion and confirm no POST/SSE/worker/executor/schema code was added.
- [x] Run `gofmt` on changed Go files.
- [x] Run target module/store/http/app tests.
- [ ] Run repository-wide tests, race checks, real PostgreSQL smoke, and final unified review in the later cross-slice acceptance batch.
- [ ] Fetch and rebase onto the requested upstream branch, rerun focused verification, commit without pushing, and report the commit SHA, completed GET routes, remaining Node-owned write routes, and conflicts.
