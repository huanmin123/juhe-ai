# Announcements Go Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Node 公告公开读取、已读记录和管理维护完整迁移到 Go opt-in 接口，并保留 Node 默认 owner。

**Architecture:** Go 使用独立 announcements module、port/store/httpapi/app 分层；公告写入在 PostgreSQL 事务内完成，提交后通过共享 operation-log 和 `announcements.public` page-data publisher 发布副作用。新增 schema 直接按当前结构设计，不加入旧结构兼容分支。

**Tech Stack:** Go 1.26、chi、pgx/v5、sqlc、goose、Redis page-data publisher、现有 session/admin middleware、Vue TypeScript API smoke。

---

### Task 1: Migration and store contract

**Files:**
- Create: `backend-go/db/migrations/000058_w8_announcements.sql`
- Create: `backend-go/internal/store/port/announcements.go`
- Create: `backend-go/internal/store/postgres/queries/w8_announcements.sql`
- Create: `backend-go/internal/store/postgres/announcements.go`
- Create: `backend-go/internal/store/postgres/announcements_test.go`
- Create: `backend-go/internal/modules/announcements/types.go`
- Test: `backend-go/db/migrationtests/announcement_schema_test.go`

- [ ] Write failing schema and query contract tests for both tables, enum checks, indexes, cascade and stable order.
- [ ] Run `go test ./db/migrationtests ./internal/store/postgres -run Announcement -count=1`; expect failure because migration, port and sqlc queries are absent.
- [ ] Add migration 000058 with current `announcements` and `announcement_reads` schema and indexes.
- [ ] Add port types and sqlc queries for public list, read upsert, admin page, detail, create, update, publish, archive and delete.
- [ ] Run `sqlc generate`, `gofmt`, targeted tests and `go test ./... -count=1`; commit `feat(go): add announcement storage contract`.

### Task 2: Public read and read receipts

**Files:**
- Create: `backend-go/internal/modules/announcements/service.go`
- Create: `backend-go/internal/modules/announcements/service_test.go`
- Create: `backend-go/internal/httpapi/announcements_public.go`
- Create: `backend-go/internal/httpapi/announcements_public_test.go`
- Modify: `backend-go/internal/httpapi/router.go`
- Modify: `backend-go/internal/app/server.go`

- [ ] Write failing service/handler tests for published-only list, 30-item limit, stable order, invalid limit, empty read list, duplicate IDs, unpublished IDs and read idempotency.
- [ ] Implement service and HTTP handler using existing auth, no-store and limiter patterns.
- [ ] Wire public routes behind the existing `PublicAPIEnabled`/management routing boundary without changing default owner behavior.
- [ ] Run `go test ./internal/modules/announcements ./internal/httpapi ./internal/app -count=1` and targeted race; commit `feat(go): add public announcement reads`.

### Task 3: Management list, detail and validation

**Files:**
- Modify: `backend-go/internal/modules/announcements/service.go`
- Create: `backend-go/internal/modules/announcements/service_management_test.go`
- Create: `backend-go/internal/httpapi/announcements_management.go`
- Create: `backend-go/internal/httpapi/announcements_management_test.go`
- Modify: `backend-go/internal/httpapi/router.go`
- Modify: `backend-go/internal/app/server.go`

- [ ] Write failing tests for admin-only authorization, progressive pagination, details, 404, strict JSON, text length and enum validation.
- [ ] Implement management list/detail and exact Chinese error responses.
- [ ] Wire routes with existing write auth/touch and mutation guard conventions.
- [ ] Run targeted tests/race and commit `feat(go): add announcement management reads`.

### Task 4: Management writes and side effects

**Files:**
- Modify: `backend-go/internal/modules/announcements/service.go`
- Create: `backend-go/internal/modules/announcements/service_write_test.go`
- Modify: `backend-go/internal/app/page_data_change.go`
- Modify: `backend-go/internal/app/server.go`
- Modify: existing operation-log adapter files identified by current app wiring
- Modify: `backend-go/internal/httpapi/announcements_management.go`

- [ ] Write failing tests for create/update/publish/unpublish/delete transactions, read reset, state transitions, no-op missing rows, operation log fields and page-data operation/mask.
- [ ] Implement writes with transaction boundaries and post-commit best-effort side effects.
- [ ] Add `announcements.public` upsert/delete event constructors or a narrow adapter using existing range/entity validation.
- [ ] Run targeted tests/race, full Go tests, vet, tidy diff and diff check; commit `feat(go): add announcement management writes`.

### Task 5: Integration and frontend listener smoke

**Files:**
- Create: `backend-go/internal/testkit/integration/w8_management_announcements_smoke_test.go`
- Create or modify: `backend/src/scripts/regression/announcement-go-listener-regression.ts` only if existing frontend smoke patterns require it
- Modify: `docs/plans/计划-0081-Node转Go渐进减法迁移.md`
- Modify: `backend-go/README.md`

- [ ] Add isolated PostgreSQL/Redis/Asynq smoke covering admin create/publish, public list/read, update/unpublish/delete, operation log ingest and page-data event.
- [ ] Add real Go listener Cookie smoke for public and management endpoints with finally cleanup.
- [ ] Run `JUHE_AI_REQUIRE_INTEGRATION=1 go test -tags=integration ./internal/testkit/integration -run '^TestW8ManagementAnnouncementsSmoke$' -count=1 -timeout=5m` against isolated Docker only.
- [ ] Run existing Node announcement regressions, frontend typecheck/build and listener smoke; document `takeoverEvidence=false` until owner gates pass.
- [ ] Have two read-only Agents independently review contract/correctness and boundary/security; perform cross-questioning; commit `test(go): verify announcement migration smoke`.

### Task 6: Remote synchronization checkpoint

- [ ] Run `git fetch origin --prune` and inspect feature/master divergence before each push.
- [ ] Merge latest `origin/master` only after auditing relevant Node diffs and resolve no unrelated user changes.
- [ ] Push every completed commit to `origin/feature/20260706-go` and confirm local/remote equality.
- [ ] Keep production owner manifest unchanged; no deployment, traffic switch, Node deletion or production data writes.
