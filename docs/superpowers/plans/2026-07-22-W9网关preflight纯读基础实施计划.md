# W9 网关 preflight 纯读基础 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不挂 HTTP、不接管 Node 网关、不写运行态的前提下，实现 Go W9 API Key preflight 结构读模型、额度 current snapshot 判定和可选版本缓存。

**Architecture:** PostgreSQL store 只提供单 key、最多 20 条绑定和固定设置键的有界事实；service 负责顺序判定和 immutable DTO；cache 只缓存结构事实，quota current 每次独立读取。默认不注入 cache 时直接读 store，Redis version 或 quota reader 都通过小接口注入。

**Tech Stack:** Go 1.26、pgx/v5、PostgreSQL、Redis raw runtime state、标准库 `sync` / `encoding/json`、Go testing/race/vet。

---

## File Map

- Create `backend-go/internal/store/port/gatewaypreflight.go`: W9 结构事实、设置和 quota current snapshot 端口。
- Create `backend-go/internal/store/postgres/gatewaypreflight.go`: 参数化、有界 PostgreSQL reader。
- Create `backend-go/internal/store/postgres/gatewaypreflight_test.go`: SQL query contract 与解析测试。
- Create `backend-go/internal/modules/gatewaypreflight/dto.go`: 字段不导出的 immutable result / API key / binding / settings / decision DTO。
- Create `backend-go/internal/modules/gatewaypreflight/service.go`: hash、精确结构 Decision、quota current 判定。
- Create `backend-go/internal/modules/gatewaypreflight/service_test.go`: service RED/GREEN 行为表和并发调用。
- Create `backend-go/internal/modules/gatewaypreflight/cache.go`: 可选结构缓存、shared version reader、quota runtime state reader。
- Create `backend-go/internal/modules/gatewaypreflight/cache_test.go`: 默认 bypass、版本变化、读中失效、runtime state JSON 测试。
- Create `docs/superpowers/specs/2026-07-22-W9网关preflight纯读基础设计.md`: 设计边界。
- Create `docs/superpowers/plans/2026-07-22-W9网关preflight纯读基础实施计划.md`: TDD 执行清单。

### Task 1: Lock service and immutable DTO behavior in RED

- [x] Write tests for raw key prefix rejection and SHA-256 hash forwarding.
- [x] Write table tests for disabled, expired, account disabled, strategy disabled and no bindings Decisions.
- [x] Write tests for stable 20-binding cap, settings projection and returned slice copy isolation.
- [x] Run `go test ./internal/modules/gatewaypreflight -count=1` after implementation is added.

### Task 2: Lock quota and cache behavior in RED

- [x] Write tests for no-quota fast path, snapshot missing, incomplete missing entry, unavailable JSON/read, exceeded and allowed entry.
- [x] Write tests proving the default service does not cache and injected shared version changes invalidate cached structure.
- [x] Write a concurrent Resolve test suitable for `go test -race`.
- [x] Re-run the package after implementation is added.

### Task 3: Add port and minimal service GREEN

- [x] Add W9 port records/readers and immutable DTO accessors with copy-on-read slices.
- [x] Implement service status priority, `apikeysecret.Hash`, binding normalization/cap and fixed settings conversion.
- [x] Implement quota current matching and limit comparison without exact usage fallback.
- [x] Run `go test ./internal/modules/gatewaypreflight -count=1` until GREEN.

### Task 4: Add optional version cache and runtime state reader GREEN

- [x] Implement nil-cache bypass, mutex-protected entries, TTL/max bounds, pre/post version checks and copy-on-store/load.
- [x] Implement shared version reader for API Key validation + system settings keys + gateway runtime topic, with separate cache/state getters.
- [x] Implement quota current raw Redis reader for `gateway_quota_snapshot/current`, including legacy omitted completeness default.
- [x] Run module tests and targeted race until GREEN.

### Task 5: Add PostgreSQL store and query contracts

- [x] First add static SQL assertions for `$n` placeholders, `LIMIT 1`, binding `LIMIT $5`, stable ordering, fixed settings bound and forbidden candidate/usage tables.
- [x] Implement single-key, bindings and settings store methods with pgx scanning and strict quota/settings JSON parsing.
- [x] Run `go test ./internal/store/postgres -run GatewayPreflight -count=1` until GREEN.

### Task 6: Verify and review

- [x] Run `gofmt` on all new Go files.
- [x] Run targeted unit/store tests, targeted `-race`, and targeted `go vet`; full-repository verification is deliberately deferred to the parallel-batch integration gate.
- [x] Review requirements, side effects, query bounds, candidate exclusions, immutable copies, cache races, Node differences and docs alignment.
- [ ] Commit all changes without pushing and report commit SHA plus RED/GREEN evidence and deferred candidate dependencies.
