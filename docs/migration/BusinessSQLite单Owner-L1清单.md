# Business SQLite 单 Owner L1 清单

> 状态：方案 A 的实施前清单，不代表任何 Go writer 已启用。当前 Business SQLite writer 仍是 Node `db-service`；J3b SQLite 配置保持 fail-closed。本清单与 [J3b 模型检测完整迁移契约](J3b-模型检测完整迁移契约.md) 第 9 节共同构成 B1/B2 的可审计输入。

## 1. 物理文件与进程边界

| 物理存储 | 当前 writer | 方案 A 目标 writer | 非 owner 规则 | 切换前提 |
| --- | --- | --- | --- | --- |
| Business SQLite | Node `db-service` | Go `gateway` | `query_only`，不得通过 Node/Go bridge 代写 | B0-B2 全部事务组迁出、Node 路由/worker/command active-path-zero |
| J3b 专属 SQLite | Node dataset/stats/business 分散 writer | Go `gateway` | 固定 `JUHE_AI_J3B_DATABASE_PATH` 单一物理文件；J3c 仅经 `J3bHealthReader` 只读消费，禁止写入此文件 | maintenance 逐表 schema/backfill + digest/row-count 校验；旧 writer/reader/cleanup 退出、rollback epoch 验收 |
| PostgreSQL J3b schema | Node 多表 writer | Go `gateway` | 仅 gateway 写 J3b 事实/投影；J3c 保持自身表/事务 | maintenance schema preflight、权限和 route owner 一次切换 |

`maintenance` 只执行显式 schema、seed、backfill、审计与恢复命令后退出。`jobs` 不参与 J3b runtime，不连接 Business SQLite 写入，也不调用 gateway。共享 Go 包只能包含无 I/O、无连接、无 scheduler 副作用的领域值对象、协议和算法。

机器可读的逐 operation 清单见 [BusinessSQLite-owner-manifest.json](BusinessSQLite-owner-manifest.json)。从 `backend-go` 目录执行只读命令 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -verify-business-owner-manifest`（或在 maintenance 模块目录执行 `go run ./cmd/juhe-ai-maintenance -verify-business-owner-manifest`），校验器会自动向上解析仓库源码，并严格核对 `DbServiceOperation`、access-mode、handler 引用、重复/遗漏项及 owner/表/事务组/回滚/验证字段；当前报告为 92 个 operation（handoff write 52、read 40；源码 access-mode 中 maintenance 16、runtime 2；handlerMatches 92）。manifest 中的 `write` 是 handoff 粗粒度，覆盖 Node 的 `write|maintenance|runtime`；`status` 的 runtime 只读例外和已移除的历史 alias 在校验器中显式列出。该清单与校验报告是实施与 active-path-zero 扫描的输入，不代表任何目标 writer 已启用；缺少逐路由运行时审计证据时，L1 仍保持未关闭。

方案 A 下 `juhe-ai-jobs` 的 J3b 配置无论 SQLite 或 PostgreSQL 均必须 fail-closed；jobs 不得启动 J3b host、listener、scheduler 或 store。Gateway 的 J3b listener 也必须在 Business handoff、专属 schema、health boundary、runtime/source/auth 四项门全部满足后才可监听。

active-path-zero 由维护命令 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -scan-node-j3b-active-path` 只读扫描 `backend/src`，覆盖 Node J3b 路由/proxy、token worker、quality scheduler、`model_quality_command` 及 `model_check_*`/health 写入口；输出文件、行号和模式，仍有命中时以非零状态阻断验收。当前扫描仍有 Node 路由、scheduler、DB-service、dataset/stats writer 和 cleanup/schema 命中，因此 active-path-zero 未通过；命令本身不修改源码或运行时 owner。

J3b Gateway 的启用配置也必须通过四个显式门：`JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true`、`JUHE_AI_J3B_SCHEMA_READY=true`、`JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true`、`JUHE_AI_J3B_RUNTIME_READY=true`。任一缺失时 Gateway 必须 fail-closed；即使四门齐全，在 runtime listener/scheduler/projector 实际接线前仍不得打开 `JUHE_AI_J3B_ENABLED`。

## 2. Node Business SQLite Writer 清单

| 事务组 | 当前 Node 路径/命令 | Business 表或副作用 | 不可拆分约束 | 方案 A 目标与切换/回滚 |
| --- | --- | --- | --- | --- |
| schema、seed 与兼容 DDL | `storage/database.ts`、`storage/schema/business-schema.ts`、`seed-defaults.ts` | 全部 Business schema、builtin system account/group/route/API key | `accounts` 重建、FK 检查、seed 版本必须在同一维护过程 | `maintenance` 显式运行；失败只回滚该 schema transaction，runtime 不补 DDL |
| 账户生命周期 | `system-api-app.ts` accounts 路由、`storage/repositories.ts` `create/update/deleteAccount` | accounts、account_model_*、account_tags、account_api_key_*、group_accounts、cleanup target | 账户删除与 dataset/stats/usage cleanup target 的顺序、revision/CAS | `gateway` 完整 API/事务后停 Node routes；回滚整组恢复旧 owner 与未完成 target cursor |
| group、route strategy、API key | group/route/API-key repositories | groups、group_accounts、route_strategies、route_strategy_groups、api_keys、api_key_groups | binding 替换、默认/引用保护、owner/access check 与 dependent cleanup 同事务 | `gateway` transaction owner；回滚前 drain mutation 并验证 binding/readback |
| system account、team、授权 | system account/team/resource authorization repositories | system_accounts、system_teams、system_team_members、resource_authorizations、sources、grants | 禁用/改密必须 revoke session；grant/source/binding 同事务 | `gateway` 迁移完整事务；不可只迁主表或保留 Node revoke |
| session/auth lifecycle | `auth.routes.ts`、`auth.middleware.ts`、`system-accounts.repository.ts` | system_sessions、system_accounts.last_login_at | login 创建与 last-login、touch、logout、密码/禁用 revoke 语义完整保持 | `gateway` 同进程认证；切换时 Node 所有 authenticated mutation 与 touch 路径归零 |
| provider/settings | settings/provider repositories | providers、provider_models、provider_model_catalog、settings | 默认 model 引用清理、cache invalidation、settings revision | `gateway` 直接事务和失效；拒绝 Node fallback |
| proxy 与访问策略 | proxy/response inspection/client-IP repositories | proxy_profiles、response_inspection_policies、client_ip_policies | optimistic revision 与 `BEGIN IMMEDIATE` 语义 | `gateway` 原子 patch/delete；回滚验证 revision 未漂移 |
| external/OIDC | external integration/OAuth repositories | external_integration_sources、source_tokens、oauth_clients/grants | token/source 关联和 revoke/删除同事务 | `gateway` 完整 command group；不可将 secret update 留给 Node |
| announcement 与 OpenAI artifact metadata | announcement management、OpenAI-compatible DB-service operations | announcements、announcement_reads、OpenAI file/vector metadata | artifact metadata 与访问 scope、删除关系一致 | `gateway` 路由/command 全量迁出后清零 Node DB-service branch |
| gateway failure/runtime | `apply_account_error_handling`、stream failure、temporary unavailable、exception commands | accounts、authorization binding、runtime state | availability/cooldown/status/revision 副作用必须一致 | `gateway` 直接写；jobs/J3b 不得补偿或回写 |
| API-key runtime/cooldown | `record_account_api_key_*`、`defer_*`、probe cursor | account_api_key_runtime_states、cursor | account revision、claim token、cooldown state 的 CAS | `gateway` 完整状态机；切换前处理在途 probe claim |
| balance、health、OAuth | balance refresh、health outcome projector、OAuth refresh commands | accounts、health cursor、credential revision | outcome/cursor 和 credential CAS 不能拆 | `gateway` 作为 Business writer；外部 I/O 可另 owner，但不得跨进程回写 |
| account test task | maintenance/running/cancel/complete/fail/message commands | account_test_tasks | queued/running/canceled/terminal state machine | `gateway` 或该完整功能随业务迁移；在 Business handoff 前 Node 路径必须归零 |
| availability/authorization sweeps | sync availability、expire authorization | accounts、api_keys、resource_authorizations | lease、schedule status、sources/grants/binding 一致 | `gateway` scheduler 或显式 owner；同一 Business file 不能保留 Node writer |
| group dirty/cursor | group-account stats dirty commands | group_account_stats_dirty 与 cursor | dirty 标记和 cursor 保证下游可重放 | `gateway` 写 Business dirtiness；stats consumer 不得反写此文件 |
| circuit control plane | dispatch revision、incident/outbox claim/ack/release/cleanup | accounts、account_circuit_incidents、account_circuit_outbox | incident revision 与 dedupe outbox 同一事务、replay fence | `gateway` 迁移整组；drain/回滚核对 outbox 与 cursor |
| deleted account/API key/session cleanup | cleanup jobs、data retention repositories | account_record_cleanup_targets、system_sessions 及删除状态 | 跨 dataset/stats/usage 顺序、batch cursor、session limit | `gateway` 迁移 Business 部分；维护跨库 completion evidence，不能 Node/Go 双 consumer |
| J3b business projection | `model_quality_command`、J3b scheduler/recovery | accounts、model_quality_policies、model_quality_schedules、account_quality_enforcements | evidence、policy revision、lease/fence、enforcement generation CAS | gateway 同进程 J3b；旧 Node J3b route/scheduler/DB-service branch 一次退出 |
| J3c business effect | failure-precheck queue | accounts/key runtime state | J3c 自己的 threshold、precheck 与 availability 语义 | 另立 J3c migration；J3b 不调用、不写入、不归档其 owner |
| offline maintenance exception | `JUHE_AI_SQLITE_OFFLINE_MAINTENANCE`、temporary maintenance worker | 由具体命令决定 | 必须在常驻 writer 全部退出时运行，不能成为共存 writer | `maintenance` 显式替代；生产不得用该环境变量绕过 owner epoch |

## 3. Go 能力、缺口与处置清单

| 域 | 已存在事实 | 缺口 | 方案 A 处置 | 允许复用边界 |
| --- | --- | --- | --- | --- |
| gateway 管理面 | 已有部分管理 API 迁移记录与独立 Go 项目 | 没有全量 Business SQLite API/owner manifest | 在 `gateway` 内按上表事务组完整接管 | 共享 DTO、错误码、SQL/HTTP 基础设施；不能 import jobs/internal |
| session/auth | jobs `modelcheckauth` 已对齐 token 校验与 touch | 缺 login/create/logout/revoke/password-change 的完整 lifecycle | 在 gateway 重写为完整 auth module，复用无副作用 hash/DTO 才可下沉 | 不共享数据库连接或 repository |
| J3b 领域算法 | jobs `modelcheckprofile`、input、probe、evaluation、quality 有基线 | 多数位于 `jobs/internal`，且 runtime evidence/projector 未完整 | 纯值对象/协议/评分可迁 shared；有 I/O 的 command/source/store/runtime 在 gateway 重写 | shared 不持有 Store、连接、goroutine、scheduler |
| J3b durable/store | jobs `modelcheckdurable`、`modelcheckstore` 有 SQLite/PG 基线；gateway `internal/modelcheckowner.Store` 已提供只读 schema preflight | 现有 store 位置、数据文件与 gateway owner 不匹配；尚无 run/item/outcome writer 接线 | 在 gateway 建专属 J3b Store，迁移契约和 replay/fence 测试一起迁；runtime 只允许已迁移 schema | 不从 gateway import jobs/internal；`CheckSchema` 不执行 DDL |
| J3b management/SSE | jobs `modelcheckhttp`/`modelcheckapp` 有未完成 host；gateway `internal/modelcheckowner` 仅有 fail-closed owner config | 缺 gateway router、lifecycle、policy/schedule/options API、release route | gateway 进程内实现；当前 owner config 仅做显式 owner/storage/readiness 校验，jobs host 保持关闭直至删除/归档 | HTTP contract 和纯 handler DTO 可复用 |
| J3b scheduler/projector | jobs quality schedule/recovery primitives存在 | evidence 未 formed、health sync/J3c 边界未完成 | gateway 内实现 formed evidence、lease/CAS、专属 health 发布与 retry | 不建立 jobs->gateway transport |
| schema/seed | maintenance 有 J3b bootstrap 基线 | 缺 Business/J3b owner manifest、upgrade/data cutover | maintenance 承担显式 migration/preflight/report | runtime 只能只读 readiness |
| jobs | 已有 J3a 等完整后台边界 | 当前 J3b host 与新目标冲突 | 禁止启用 J3b；逐包迁移/删除后再归档 Node 与旧 Go host | 无 J3b Business/专属文件 writer |

## 4. L1 退出条件

1. 每个上表行有机器可读 manifest：Node path、operation、表、transaction group、owner、epoch、drain、回滚与验证用例。
2. Go gateway 对应模块和 J3b 专属文件的 schema/data cutover 已实现；jobs 不再编译或启动 J3b runtime。
3. Node `db-service`、System API mutation、background registry、maintenance exception 对 Business SQLite 的写路径均为 active-path-zero。
4. J3b/J3c 的专属文件读写审计证明：gateway 唯一写 J3b，J3c 仅只读消费，不反写 Business/J3b 文件。
5. 隔离 SQLite、PostgreSQL/PgBouncer 分别通过并发、重启、lease/fence、rollback 与恢复演练；未通过时保持 fail-closed。
