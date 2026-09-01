# Business SQLite 单 Owner L1 清单

> 状态：本清单仍是其余 Business SQLite 事务组的方案 A 实施前输入，当前 Business SQLite writer 仍是 Node `db-service`。J3b 已在本地/dev 验收边界内由 Go Gateway 接管并完成 Node active-path-zero；其剩余语义与生产门禁以 [J3b 模型检测完整迁移契约](J3b-模型检测完整迁移契约.md) 第 9 节为准。

## 1. 物理文件与进程边界

| 物理存储 | 当前 writer | 方案 A 目标 writer | 非 owner 规则 | 切换前提 |
| --- | --- | --- | --- | --- |
| Business SQLite | Node `db-service` | Go `gateway` | `query_only`，不得通过 Node/Go bridge 代写 | B0-B2 全部事务组迁出、Node 路由/worker/command active-path-zero |
| J3b 专属 SQLite | Go `gateway`（本地/dev） | Go `gateway` | 固定 `JUHE_AI_J3B_DATABASE_PATH` 单一物理文件；J3c 仅经 `J3bHealthReader` 只读消费，禁止写入此文件 | Node writer/reader/cleanup 已退出；生产仍需真实停写、owner epoch 与 rollback 演练 |
| PostgreSQL J3b schema | Go `gateway`（本地/dev） | Go `gateway` | 仅 gateway 写 J3b 事实/投影；J3c 保持自身表/事务 | 本地/dev schema、权限和 route owner 已验证；生产仍需 GitOps 一次切换 |

`maintenance` 只执行显式 schema、seed、backfill、审计与恢复命令后退出。`jobs` 不参与 J3b runtime，不连接 Business SQLite 写入，也不调用 gateway。共享 Go 包只能包含无 I/O、无连接、无 scheduler 副作用的领域值对象、协议和算法。J3b 的 Node 代码归档、Gateway 接管和剩余语义验收以 [J3b 模型检测完整迁移契约](J3b-模型检测完整迁移契约.md) 为准，不再伪装为现行 Node `DbServiceOperation`。

机器可读的逐 operation 清单见 [BusinessSQLite-owner-manifest.json](BusinessSQLite-owner-manifest.json)。从 `backend-go` 目录执行只读命令 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -verify-business-owner-manifest`（或在 maintenance 模块目录执行 `go run ./cmd/juhe-ai-maintenance -verify-business-owner-manifest`），校验器会自动向上解析仓库源码，并严格核对 `DbServiceOperation`、access-mode、handler 引用、声明的 type/access/handler 行号、入口类别、writer 分类、重复/遗漏项及 owner/表/事务组/回滚/验证字段；同一 `transaction_group` 若出现不同 `current_owner` 或 `target_owner` 会 fail-closed。报告同时输出机器可读的 `accessCoverage`、`writerCoverage`、`transactionCoverage` 和 `transactionGroups`，用于核对逐 operation 数量与事务组覆盖。manifest 中的 `write` 是 handoff 粗粒度，覆盖 Node 的 `write|maintenance|runtime`；`status` 的 runtime 只读例外和已移除的历史 alias 在校验器中显式列出。该清单与校验报告是实施与 active-path-zero 扫描的输入，不代表任何目标 writer 已启用；缺少逐路由运行时审计证据时，L1 仍保持未关闭。

System API mutation 路由另有 [GatewayManagementRouteOwnerManifest.json](GatewayManagementRouteOwnerManifest.json)。执行 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -verify-gateway-route-owner-manifest` 可按 Node `system-api-app.ts` 的实际 router 源码重新计数；报告输出 `families`、`mutationRoutes`、`statusCoverage` 与 `pendingFamilies`。任一路由族为 `missing|partial`、源文件计数漂移、重复归属或缺少验收/回滚字段都会 fail-closed（退出码 3 或验证错误），因此“清单完整”不等于 Gateway 已接管；当前 22 个路由族共 98 个 mutation，只有 `model-checks` 有部分 Gateway 基线，其余均待完整迁移。

领域能力矩阵见 [GoBusinessCapabilityManifest.json](GoBusinessCapabilityManifest.json)。它以同一 91 operation/14 transaction group 的现行 Node 清单为唯一输入，逐组登记 Node operation、Gateway 目标模块、迁移方式、状态、验收门和回滚动作；J3b 已从现行 Node operation 矩阵移至其专属归档契约。`partial` 只表示存在局部 Go 实现，`missing` 表示尚未具备完整接管能力，`excluded` 仅用于明确保持当前非 Business 物理存储 owner。维护包中的 `VerifyCapabilityManifest` 会 fail-closed 检查组/operation 一对一覆盖、owner 一致性、operation_count、合法状态、实现证据、验收门和回滚字段。该校验通过只证明矩阵完整，不证明切换完成。

方案 A 下 `juhe-ai-jobs` 的 J3b 配置无论 SQLite 或 PostgreSQL 均必须 fail-closed；jobs 不得启动 J3b host、listener、scheduler 或 store。Gateway 的 J3b listener 也必须在 Business handoff、专属 schema、health boundary、runtime/source/auth 四项门全部满足后才可监听。

active-path-zero 由维护命令 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -scan-node-j3b-active-path` 只读扫描 `backend/src`；规则版本固定为 `j3b-active-path-v2`，覆盖 Node J3b 路由/proxy、token worker、quality scheduler、`model_quality_command` 及 `model_check_*`/health 写入口。每个命中输出文件、行号、类别、`block` disposition 和阻断原因，同时输出显式跳过目录及 allow 原因，报告包含 `blockedFindings` 与规则清单，仍有阻断命中时以非零状态阻断验收。2026-08-31 本地/dev 工作树实测扫描 `912` 个文件、`blockedFindings=0`，退出码 0；命令本身不修改源码或运行时 owner。该结果证明 Node J3b active path 已归零，不替代 J3b 专属契约中的语义 golden、真实停写和生产切换证据。

Business/J3b 文件切换前还必须执行隔离前置验证：`go run ./projects/maintenance/cmd/juhe-ai-maintenance -verify-business-sqlite-handoff -business-sqlite-path <Business SQLite> -j3b-sqlite-path <专属 J3b SQLite>`（也可使用 `JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH` 与 `JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH`）。命令只对两个用户文件做存在性、regular-file、同 inode/路径检查，不打开或写入用户数据库；`query_only` 写入拒绝和行数未变化断言始终在一次性临时 SQLite 中执行。输出 `ready`、`pathsDistinct`、`sameFile`、`queryOnlyEnabled`、`writeRejected`、`userDatabaseTouched` 等机器可读字段，路径缺失、共享文件或任一隔离断言失败均以退出码 3 fail-closed。

Business SQLite 的 Gateway `schemaReady` 还必须有实际结构证据：执行 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -verify-business-sqlite-schema -business-sqlite-path <Business SQLite>`。v12 按 `business-sqlite-gateway-v12` 核对 Gateway 已依赖的认证、`system_settings`、账户、模型目录、分组、质量策略、调度、enforcement、circuit control-plane 及 announcement 表、列、关键索引、外键和关键 PK/UNIQUE 约束；SQLite 通过 `PRAGMA table_info/index_list/index_info` 严格核对 `system_accounts.username`、`system_sessions.token_hash`、`system_settings(system_account_id,key)`、`model_quality_schedules(system_account_id,account_id)`、`account_quality_enforcements(account_id)`、`account_circuit_incidents(circuit_scope_key)` 的列顺序与唯一性，错误结构进入 `missingConstraints`/`errors` 并令 `ready=false`。对已登记 `IndexDefinitions` 的索引同时读取 SQLite `PRAGMA index_list/index_info` 与 `sqlite_master.sql`，fail-closed 检查名称、列顺序、UNIQUE 和 partial `WHERE` 语义。`account_circuit_incidents` 的 `idx_account_circuit_incidents_key_model_capability` 必须是 `UNIQUE(scope_kind, capability_hash) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`；不执行 DDL。Gateway PostgreSQL schema 同样通过 `pg_catalog.pg_constraint` 核对上述 PK/UNIQUE 语义。其余输出和 owner 门保持不变。

J3b 专属 SQLite 回填后必须执行只读 readback：设置 `JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH`、`JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH`、`JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH` 后运行 `go run ./projects/maintenance/cmd/juhe-ai-maintenance -verify-j3b-model-check-sqlite-backfill`。命令只读打开 legacy dataset/stats 与专属 target，逐表核对 mandatory/optional 表、行数和按源列投影的 SHA-256 digest，并拒绝共享物理文件；输出 `ready`、`pathsDistinct`、`sourceReadOnly`、`statsReadOnly`、`targetReadOnly`、`tables`、`sourceRows`、`targetRows`、`sourceDigest`、`targetDigest`，任何漂移、缺表或路径冲突均以退出码 3 fail-closed。该 readback 是切换/rollback epoch 的证据，不代表 Node active-path-zero 已完成。

执行 J3b SQLite 或 PostgreSQL backfill 前，还必须提供 maintenance 的 `-j3b-backfill-evidence <json>`。该证据在打开目标数据库前校验 owner/epoch、Node drain、`inFlight=0`、`activePathZero`、备份 SHA-256、源摘要和 freshness；target digest 可在回填前为空。回填完成后必须再执行完整 cutover evidence readback，不能将前置证据或三个确认布尔值单独视为切换完成。

session retention 已在 Gateway 主进程接入：`internal/business/session_retention.Store` 与 auth、enforcement、scheduler 共用经 handoff 门控的 Business 连接，启动前只读执行 `CheckContract`，随后由独立 supervisor component 按 `JUHE_AI_SESSION_RETENTION_INTERVAL`（默认 15 分钟）和 `JUHE_AI_SESSION_RETENTION_BATCH_SIZE`（默认 10000，属于事务恢复窗口而非产品限流）执行严格 `expires_at < expiredBefore` 清理。组件首次清理成功前不报告 `sessionRetentionReady`；任何清理错误进入 supervisor 重试并令 Gateway 健康检查失败。该接线只证明 Go 目标能力存在，Node `cleanup_expired_system_sessions` 路径仍需 active-path-zero、epoch drain、回滚与真实 SQLite/PG 演练后才能移除。

J3b Gateway 的启用配置也必须通过四个显式门：`JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true`、`JUHE_AI_J3B_SCHEMA_READY=true`、`JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true`、`JUHE_AI_J3B_RUNTIME_READY=true`，并提供 `JUHE_AI_J3B_OWNER_EPOCH` 与 `JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH`。启用时 Gateway 会在打开 Business 数据库和 management listener 前校验证据文件中的 `newOwner=go-gateway`、epoch、drain、active-path 和 readback 条件；任一缺失或不匹配时必须 fail-closed。即使所有门齐全，在 runtime listener/scheduler/projector 实际接线前仍不得打开 `JUHE_AI_J3B_ENABLED`。

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
| balance、health、OAuth | balance refresh、health outcome projector、OAuth refresh commands | accounts、health cursor、credential revision | OAuth refresh 必须在 `gateway` 同进程完成 refresh lock、凭据解密、直接上游交换、credential CAS、失败处置和运行态缓存失效；不得只交付 opaque handle、Node/Go IPC 或跨进程回写 | `gateway` 作为 Business writer；未完成完整纵切时保持 Node owner，拒绝 fallback/bridge |
| Codex usage observation（非 Business OAuth） | `persist_openai_codex_usage_headers` | stats `account_usage_snapshots` 的 typed `openai_codex` observation | 网关只允许白名单响应头解析为有界 typed observation；原始 headers 不得落库、不得进入 OAuth credential transaction，也不得由 Business SQLite owner 代写 stats | 保持独立 stats owner；其完整持久化迁移另立任务，不能作为 OAuth 或 J3b 完成证据 |
| account test task | maintenance/running/cancel/complete/fail/message commands | account_test_tasks | queued/running/canceled/terminal state machine | `gateway` 或该完整功能随业务迁移；在 Business handoff 前 Node 路径必须归零 |
| availability/authorization sweeps | sync availability、expire authorization | accounts、api_keys、resource_authorizations | lease、schedule status、sources/grants/binding 一致 | `gateway` scheduler 或显式 owner；同一 Business file 不能保留 Node writer |
| group dirty/cursor | group-account stats dirty commands | group_account_stats_dirty 与 cursor | dirty 标记和 cursor 保证下游可重放 | `gateway` 写 Business dirtiness；stats consumer 不得反写此文件 |
| circuit control plane | dispatch revision、incident/outbox claim/ack/release/cleanup | accounts、account_circuit_incidents、account_circuit_outbox | incident revision 与 dedupe outbox 同一事务、replay fence | `gateway` 迁移整组；drain/回滚核对 outbox 与 cursor |
| deleted account/API key/session cleanup | cleanup jobs、data retention repositories | account_record_cleanup_targets、system_sessions 及删除状态 | 跨 dataset/stats/usage 顺序、batch cursor、session limit | `gateway` 迁移 Business 部分；维护跨库 completion evidence，不能 Node/Go 双 consumer |
| J3c business effect | failure-precheck queue | accounts/key runtime state | J3c 自己的 threshold、precheck 与 availability 语义 | 另立 J3c migration；J3b 不调用、不写入、不归档其 owner |
| offline maintenance exception | `JUHE_AI_SQLITE_OFFLINE_MAINTENANCE`、temporary maintenance worker | 由具体命令决定 | 必须在常驻 writer 全部退出时运行，不能成为共存 writer | `maintenance` 显式替代；生产不得用该环境变量绕过 owner epoch |

## 3. Go 能力、缺口与处置清单

| 域 | 已存在事实 | 缺口 | 方案 A 处置 | 允许复用边界 |
| --- | --- | --- | --- | --- |
| gateway 管理面 | 已有部分管理 API 迁移记录与独立 Go 项目 | 没有全量 Business SQLite API/owner manifest | 在 `gateway` 内按上表事务组完整接管 | 共享 DTO、错误码、SQL/HTTP 基础设施；不能 import jobs/internal |
| session/auth | jobs `modelcheckauth` 已对齐 token 校验与 touch | 缺 login/create/logout/revoke/password-change 的完整 lifecycle | 在 gateway 重写为完整 auth module，复用无副作用 hash/DTO 才可下沉 | 不共享数据库连接或 repository |
| J3b 领域算法 | jobs `modelcheckprofile`、input、probe、evaluation、quality 有基线 | 多数位于 `jobs/internal`，且 runtime evidence/projector 未完整 | 纯值对象/协议/评分可迁 shared；有 I/O 的 command/source/store/runtime 在 gateway 重写 | shared 不持有 Store、连接、goroutine、scheduler |
| J3b durable/store | jobs `modelcheckdurable`、`modelcheckstore` 仅作迁移参考；gateway `internal/modelcheckowner.Store` 已提供专属 SQLite/PG schema preflight 与 run/item/observation/outcome writer | 完整证据族、trust/quality 形成、三库回填与端到端切换证据仍缺 | 继续在 gateway 建专属 J3b Store，完成 replay/fence、observation receipt 与双模式验证；runtime 只允许已迁移 schema | 不从 gateway import jobs/internal；`CheckSchema` 不执行 DDL |
| J3b management/SSE | Gateway `internal/modelcheckowner` 已具备 run/stream/active/stop、policy/schedule/options/account-options 与 token-baseline activate 基线；jobs host 对启用配置硬失败 | 完整 Business API handoff、release route/readiness、Node route active-path-zero 仍未完成 | gateway 进程内继续收口管理契约；owner 未达到 readiness 前保持 listener 关闭，jobs host 不得启用 | HTTP contract 和纯 handler DTO 可复用 |
| J3b scheduler/projector | Gateway 已有 scheduled、quality_recovery、health_sync_retry coordinator、lease/fence/retry 与 fail-closed projector 基线 | 完整 evidence/trust 形成、Business enforcement handoff、health boundary 与隔离 PG/SQLite 验证仍缺 | gateway 内完成 formed evidence、lease/CAS、专属 health 发布与 retry；未通过门禁不得切换 | 不建立 jobs->gateway transport |
| schema/seed | maintenance 有 J3b bootstrap 基线 | 缺 Business/J3b owner manifest、upgrade/data cutover | maintenance 承担显式 migration/preflight/report | runtime 只能只读 readiness |
| jobs | 已有 J3a 等完整后台边界 | 当前 J3b host 与新目标冲突 | 禁止启用 J3b；逐包迁移/删除后再归档 Node 与旧 Go host | 无 J3b Business/专属文件 writer |

## 4. L1 退出条件

当前实现进度（J3b 以外仅为能力基线，尚未接入 Gateway 主路由，也不改变 Node owner）：Gateway 已新增 `internal/businessauth` session 生命周期原语，以及 `internal/business/{accounts,groups,settings,authorization,account_test_task}` 的 owner-gated 事务服务；这些模块均有 SQLite/SQL/CAS/回放定向测试，但 accounts、groups/routes/API keys、settings/authorization 和 account-test-task 尚未完成现行 91 operation 的全量覆盖，能力 manifest 仍保持 `missing/partial`，不得据此打开 handoff。

1. 每个上表行有机器可读 manifest：Node path、operation、表、transaction group、owner、epoch、drain、回滚与验证用例。
2. Go gateway 对应模块和 J3b 专属文件的 schema/data cutover 已实现；jobs 不再编译或启动 J3b runtime。
3. Node `db-service`、System API mutation、background registry、maintenance exception 对 Business SQLite 的写路径均为 active-path-zero。
4. J3b/J3c 的专属文件读写审计证明：gateway 唯一写 J3b，J3c 仅只读消费，不反写 Business/J3b 文件。
5. 隔离 SQLite、PostgreSQL/PgBouncer 分别通过并发、重启、lease/fence、rollback 与恢复演练；未通过时保持 fail-closed。
