# Mockdata 造数设计

> **2026-10-26 T66 第二波（五个造数域全部接线）**：本文按 PostgreSQL 单库多 schema 存储形态重写。`maintenance --mockdata --dsn` 已可执行（EnsurePostgres + SeedPostgresDefaults + 幂等清理 + 五域造数 + 摘要）；`--verify-mockdata-coverage --dsn` 为严格只读覆盖校验。域清单：`business` / `usage` / `stats` / `observability` / `chat_codex_model_check` 均已在同一注册表接线；属主存储（`juhe_j3b` / `juhe_jobs` / 属主日志表）缺失时对应子段跳过并记 `OwnerStoresPending`，不阻塞 Ready。
>
> 面向本地演示、测试联调和页面验收。历史 sqlite 造数链（12 个固定 sqlite3 文件 + usage/codex 分片文件）已随 2026-10-24 存储形态收口（PLAN-20261024T051925945Z）物理删除，git 历史可溯，不再是任何口径的权威。

## 1. 目标

- 一条命令在指定 PostgreSQL 库上生成完整业务闭环数据，覆盖管理端与公开面的全部页面数据来源。
- 存储拓扑是**单库多 schema**（与生产一致）：business→`juhe_business`、usage+catalog→`juhe_usage`（`usage_records` 日分区 + 逻辑 shard 四表）、stats→`juhe_stats`（含 `background_task_runs`）、dataset→`juhe_dataset`（公开接口日志与记录清理目标）、chat→`juhe_chat`（`chat_messages` 日分区）、codex→`juhe_codex_context`、model-check→`juhe_j3b`（属主）、task-runs→`juhe_stats.background_task_runs`、account-health→`juhe_jobs`（属主）、audit/operation/runtime→`juhe_dataset` 属主表（gateway/jobs 运行时建）。
- 覆盖 AI 账户的 OAuth / API Key / `openai_standard` 客户端 / 多上游 Key、图像生成、模型映射、标签、账号内 Key 运行态、待检查、停用、限流、冷却、错误、不可调度和时间计划等状态样本；覆盖 API Key 的优先级故障转移、轮询、加权轮询、绑定禁用、额度窗口、过期、停用和时间计划样本（business 域已全部交付）。
- 默认生成近 31 天明细与监控样本（明细面随 usage 域接线交付）；派生表（`usage_stats_*`、`*_windows`、排行、健康小时、账号质量等）不伪造，全部由 jobs 聚合 / 窗口任务重建；造数清理会整表重置派生面与游标。
- 可重复执行：每次先按固定清理标识删除上一批数据，再重新插入；执行结束时可选写出摘要，`--verify-mockdata-coverage` 做严格只读覆盖校验。

## 2. 职责边界

Mockdata 是项目里「可复用本地造数」的唯一职责入口：

- 造数实现只存在于 `backend-go/projects/maintenance/internal/mockdata/`；不再新增独立的 `seed-*`、`demo-*`、`sample-*`、`fixture-*` 造数脚本。某段造数逻辑一旦需要被脚本、页面验收或人工联调复用，必须收口到 mockdata 包的对应域。
- `maintenance --seed`（SeedPostgresDefaults）只负责系统启动所需的最小默认数据（默认超级管理员、九家厂商、协议与端点族、目录模型、默认分组和系统设置），不是业务演示 / 测试造数入口；`--mockdata` 的主流程会先 EnsurePostgres 再 SeedPostgresDefaults，造数行引用的 vendors / models / admin 全部来自 seed。
- 造数写出的每一行都必须带稳定清理标识（见第 5 节）；派生表一律通过既有聚合器 / 窗口任务重建，不手工伪造。
- 新增功能只要包含页面菜单、列表、筛选项、统计卡片、趋势图、日志、审计、后台任务状态、公开接口、运维监控或可视化表格，就必须同步扩展 Mockdata：补正向样本与不可调度 / 失败 / 停用类样本、扩展覆盖断言，并保证重复执行的清理收敛。
- 新增表或统计窗口时，必须明确默认行数、状态分布、时间跨度、清理标识；页面依赖预聚合表时由 jobs 重建路径覆盖，Mockdata 不直接写派生表。
- 属主表（gateway/jobs/J3b 运行时建表）不在 maintenance 复制 DDL：mockdata 只用 `to_regclass` 探测，缺失时记 `OwnerStoresPending` 并域内跳过，不阻塞覆盖 Ready。

当前目录结构（Go 实现，PG 形态）：

| 位置 | 职责 |
| --- | --- |
| `backend-go/projects/maintenance/cmd/juhe-ai-maintenance/mockdata.go` | CLI 层：`--mockdata` / `--verify-mockdata-coverage` 参数校验、JSON 报告输出与退出码（0/1/2/3） |
| `backend-go/projects/maintenance/cmd/juhe-ai-maintenance/main.go` | flag 接线：`--mockdata-days` / `--mockdata-daily-requests` / `--mockdata-summary-file` 与互斥矩阵 |
| `internal/mockdata/mockdata.go` | Run 主流程：EnsurePostgres → SeedPostgresDefaults → 属主探测（juhe_j3b 受控 bootstrap）→ 清理 → 逐域 seed → autofill → 摘要；域注册表、参数边界常量、Report/CoverageReport 契约 |
| `internal/mockdata/env.go` | 存储上下文：目标库单 `*sql.DB`、事务、`insertMap`（列排序 + `$n` + `ON CONFLICT(主键) DO UPDATE` 幂等）、`information_schema` 主键缓存、域记账 |
| `internal/mockdata/schemastore.go` | schema 词表与属主注册表：schema/表探测（`to_regclass`）、表枚举（排除分区子表）、外键依赖图与清理拓扑序、属主缺失探测 |
| `internal/mockdata/partitions.go` | `usage_records` / `chat_messages` 日分区 ensure（语句文本对齐 jobs usagewriter 与 gateway chat partitions；chat 预建当日+次日） |
| `internal/mockdata/cleanup.go` | 清理标识常量与幂等清理：外键拓扑序（子表先删）+ 通用标识扫描 + `juhe_stats` 派生面/游标整表重置；FK 冲突重试轮 |
| `internal/mockdata/summary.go` | 摘要 JSON 写出与域接线快照读取（`--mockdata-summary-file` 显式路径，无隐式回退） |
| `internal/mockdata/coverage.go` | 严格只读覆盖校验：maintenance schema + 属主存储逐表枚举、空表白名单、按域归属的关键状态断言 |
| `internal/mockdata/domain_business.go` | business 域造数 |
| `internal/mockdata/domain_usage.go` | usage 域造数：`usage_records` 日分区写入 + 逻辑 shard 四表登记 + 上游响应模型定向样本 |
| `internal/mockdata/domain_stats.go` | stats 域造数：`background_task_runs`、`account_usage_snapshots`、IP 样本与脏标记（派生聚合不伪造） |
| `internal/mockdata/domain_observability.go` | observability 域造数：审计/操作/运行日志属主表探测式写入 + `public_api_logs` 与清理目标 |
| `internal/mockdata/domain_chat_codex_modelcheck.go` | chat/codex/model-check 域造数：chat 日分区、codex 三表、J3b 样本、账户健康属主表 |
| `internal/mockdata/domain_chat_media_sample.go` | chat 域媒体样本：纯字节/纯值生成器（WAV/MP4 内嵌样本、媒体工具演示消息块），与存储无关 |
| `backend-go/projects/jobs/internal/usagewriter/partitions.go` | `usage_records` 日分区语句的权威来源（mockdata 与 jobs 逐字对齐） |
| `backend-go/projects/gateway/internal/chat/postgres_partitions.go` | `chat_messages` 日分区语句的权威来源 |
| `backend-go/projects/maintenance/internal/j3bmodelcheck/bootstrap.go` | `juhe_j3b` 受控 bootstrap（mockdata 只调用它，不复制 DDL） |

## 3. 命令

### 3.1 使用前提

目标库必须是显式 `--dsn` 指向的 PostgreSQL 库（开发库或测试库）；一次误执行的造数命令不得落到未知库。造数前停止本地常驻 `gateway` 与 `jobs`（`pnpm dev` 会话）：常驻聚合器与造数的清理/重插并发会重复累计同一批用量游标；造数完成后重启服务。属主表（审计/操作/运行日志、`juhe_j3b`、`juhe_jobs`）由 gateway/jobs 首次启动或受控 bootstrap 流程创建——全新库直接跑 `--mockdata` 是允许的，属主缺失按第 6 节语义处理。

### 3.2 调用形态

```powershell
juhe-ai-maintenance --mockdata --dsn <postgres-url> [--mockdata-days N] [--mockdata-daily-requests M] [--mockdata-summary-file <path>]
juhe-ai-maintenance --verify-mockdata-coverage --dsn <postgres-url> [--mockdata-summary-file <path>]
```

| 参数 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| `--dsn` | 无（必填） | postgres URL | 目标 PostgreSQL 库；不接受任何隐式回退 |
| `--mockdata-days` | `31` | `1` 到 `90` | 造数历史跨度天数 |
| `--mockdata-daily-requests` | `120` | `1` 到 `500` | 每日生成的使用记录数 |
| `--mockdata-summary-file` | 空（可选） | 任意路径 | `--mockdata` 把摘要 JSON 写到该路径；`--verify-mockdata-coverage` 从同一路径读域接线快照。空值不读不写，**无隐式回退** |
| `--secret` | 空 | 任意字符串 | 账户凭据信封密钥；空值沿用 Node 开发默认值（与 gateway/jobs seed 同源） |

编排顺序（`--mockdata` 单命令内完成）：EnsurePostgres 建骨架 → SeedPostgresDefaults 落最小种子 → 属主探测（`juhe_j3b` 存在时受控 bootstrap，缺失记 pending）→ 按清理标识删除上一批 → 逐域 seed → autofill 登记未接线域跳过原因 → 写摘要（可选）。派生聚合表的重建仍由常驻 jobs 或 jobs 一次性任务承担，不在 `--mockdata` 内执行。

### 3.3 退出码与报告 JSON

| 命令 | exit 0 | exit 1 | exit 2 | exit 3 |
| --- | --- | --- | --- | --- |
| `--mockdata` | 成功，stdout 输出一份 Report JSON | 造数运行失败（已完成的报告仍输出到 stdout） | 用法错误（缺 `--dsn`、参数越界、与其他命令互斥） | — |
| `--verify-mockdata-coverage` | 覆盖 Ready | 校验运行失败（连不上库、枚举出错） | 用法错误 | 覆盖未 Ready（Empty / NotCovered / OwnerStoresPending 清单见 stdout JSON） |

Report JSON（`--mockdata` stdout）：

```json
{
  "driver": "postgres", "dsn": "...", "days": 31, "dailyRequests": 120,
  "startedAt": "...", "finishedAt": "...", "durationMs": 0,
  "cleanup": {"juhe_business.accounts": 31}, "cleanupSkipped": ["...（表不存在）"],
  "domains": [{"name": "business", "counts": {"accounts": 31}}],
  "counts": {"accounts": 31},
  "autofill": {}, "autofillSkipped": {"domain:usage": "域未接线（PG 重写波次交付中），autofill 随域接线一并交付"},
  "ownerStoresPending": [{"store": "audit_log", "schema": "juhe_dataset", "missingTables": ["audit_logs"]}],
  "summaryPath": "", "warnings": []
}
```

CoverageReport JSON（`--verify-mockdata-coverage` stdout）：`driver` / `dsn` / `ready` / `schemas`（每 schema 或 `owner:<逻辑名>` 的逐表行数、`allowEmpty` 白名单理由）/ `ownerStoresPending` / `empty` / `errors` / `notCovered`。

摘要文件（`--mockdata-summary-file` 指定路径时写出）：记录 mock 用户明文口令（`mockdata123456`）、API Key 明文、资源计数和域接线快照（`domains[].wired`）。该文件只用于本地联调，不提交、不脱敏外发；覆盖校验命令在独立进程运行，域接线状态只能从这份落盘快照读取——文件缺失时全部域按未接线评估，无隐式回退。

## 4. 数据边界

造数只写 `--dsn` 指向库的六个 maintenance schema；属主 schema 只探测不建。各域覆盖范围（域 → schema 映射）：

| 域 | schema / 表 | 造数内容 | 状态 |
| --- | --- | --- | --- |
| `business` | `juhe_business` | 配套用户 7 个（1 个普通管理员 + 6 个角色用户）、13 个分组（含个人分组、高并发分组与 1 个超级分组）、25+ AI 账户样本（含状态与类型覆盖、连接面三表、支持模型与入口映射）、路由策略与 API Key 18 条（含本地网关明文 Key）、38 条授权样本（含团队来源与授权实例账户）、团队、代理、公告、模型目录样本、响应检查策略、外部来源系统与 Token、账户测试任务三态、熔断/锁/Key 运行态、媒体任务（video 六状态 + 音频三样本）、题库三态 | 已交付 |
| `usage` | `juhe_usage`：`usage_records` 日分区 + 逻辑 shard 四表（`usage_record_shards` / `usage_record_shard_entries` / `usage_record_account_shards` / `usage_record_api_key_shards`） | 按天插入使用记录并在 shard 四表登记；插入前 ensure 日分区；含三条上游响应模型定向样本与音频/视频计量样本 | 已交付 |
| `stats` | `juhe_stats`（`background_task_runs`、`account_usage_snapshots`、dirty 标记） | 原始写入面（`background_task_runs` 由本域独占）；派生聚合表不伪造 | 已交付 |
| `observability` | `juhe_dataset` 维护面（`public_api_logs`、记录清理目标）+ 属主表（`audit_logs` / `operation_logs` / `runtime_logs` 族） | 公开接口日志、清理目标；属主表缺失时域内跳过 | 已交付 |
| `chat_codex_model_check` | `juhe_chat` 日分区面、`juhe_codex_context`、`juhe_j3b`（属主）、`juhe_jobs` 账户健康（属主） | 会话/消息（插入前 ensure 日分区）、codex context、模型检测 runs/items/observations、账户健康 | 已交付 |

补充约束：

- 列集与列型逐列对照现行 PG DDL（`maintenance/internal/schema/pg_schema_*.go`）：布尔列（`proxy_profiles.enabled`、`models.catalog_visible`、`account_test_tasks.cancel_requested`）写 Go bool，`timestamptz` 列写 `time.Time`，其余 0/1 列写 integer，时间文本列沿用 UTC 毫秒 ISO 形态；`accounts.capability_domain`、`vendor_code`（generic 账户为 SQL NULL）与连接面三表（`account_connections` / `account_endpoint_modes` / `account_entry_mappings`）按供应商与上游协议面重构设计 §2/§5 落库。
- 派生表不造假：`usage_stats_*`、各 `*_windows`、`usage_rank_snapshots`、`account_health_hourly`、`group_account_stats`、`client_ip_*`、`authorization_*_usage_*`、`account_quality_*` 的小时与趋势表，全部由 jobs 聚合 / 窗口任务重建；造数清理在删除上一批明细后整表重置派生面与 `stats_job_state` 游标。
- 上游响应模型定向样本固定三条（usage 域交付）：trace 分别为 `mockdata-usage-coverage_upstream_response_model_match` / `..._mismatch` / `..._unmapped_mismatch`。
- 本地网关 Key、上游 API Key、OAuth Token 和代理密码均为模拟值，不会真实请求外部服务。mock 用户明文口令固定为 `mockdata123456`；摘要同时记录各 API Key 明文。
- 身份归属：主数据归属 seed 超管 `admin`（`sys_admin`，账户/分组/API Key/标签的主体），免登录联调推荐 `JUHE_AI_DEV_AUTO_LOGIN_USERNAME=admin`；`mockdata_admin` 是普通管理员（admin 角色）视角样本，名下有自有账户、标签与团队成员。

已知边界：

- 五个域全部接线后，`--verify-mockdata-coverage` 的 Empty 只应来自「属主存储未就绪时的属主表」或白名单表；若出现 maintenance schema 的业务空表，按真实缺陷处理。
- 属主存储缺失（gateway/jobs 未启动过、`juhe_j3b` 未由受控流程预置）不是失败：Run 把它们列入 `Report.OwnerStoresPending` 并在对应域内跳过，覆盖断言归入 NotCovered，**不阻塞 Ready 判定中「属主」这一维度**；属主就位后（常驻进程建表 / `juhe_j3b` 受控 bootstrap）对应断言自动转为硬门槛。
- 表监控快照与 Go runtime metrics 已随 2026-10-14 监控改造整体删除，均不属于造数范围。
- `--mockdata` 与 `--verify-mockdata-coverage` 互斥，也不与其他 maintenance 命令同用。

## 5. 清理策略

造数写出的每一行都必须至少在一个可检索列上带以下前缀之一，清理才能幂等收敛：

| 标识 | 适用范围 |
| --- | --- |
| 名称前缀 `造数-` | 业务数据名称（分组、账户、API Key、公告、策略等） |
| ID 前缀 `mockdata_` | 统计数据集域 ID 与配套系统用户名 |
| trace 前缀 `mockdata-` | 使用记录等记录类数据的 trace |

`--mockdata` 可重复执行。PG 形态的清理分三段：

1. **外键拓扑序枚举**：从 `information_schema` 读 maintenance schema 内的外键依赖图，Kahn 拓扑排序保证子表先于父表删除（自引用边不参与排序，由显式规则处理；互引环 `accounts`↔`account_connections` 由 FK 冲突重试轮兜底）。
2. **显式规则 + 通用标识扫描**：对拓扑序内每张表先执行显式规则（accounts 授权实例列的两段式删除、`usage_record_shards` 按「分片内已无条目」回收），再执行通用扫描——任一标识列（`id`/`name`/`trace_id`/`*_id`/`*_key`/`*_name` 等文本列）命中任一清理前缀即删。扫描只可能命中造数自己写出的值，seed 行（vendors、models 目录、默认分组与默认 Key）不受影响。
3. **派生面重置**：整表清空 `juhe_stats` 聚合/窗口/队列族与 `stats_job_state` 游标（游标与桶必须一并清零），重建交给 jobs。

对空库、缺表，清理是安全 no-op（缺表记入 `cleanupSkipped`）。

## 6. 验证点

`--mockdata` 退出码 0 即造数成功；`--verify-mockdata-coverage` exit 0 表示覆盖 Ready、exit 3 表示未 Ready（剩余空表与 NotCovered 清单见 stdout JSON）。常见非故障状态是按处置方式分类：属主存储缺失（`OwnerStoresPending` 非空）——审计/操作/运行日志、账户健康由 gateway/jobs 首次启动建库，`juhe_j3b` 由受控 bootstrap 流程预置 schema，之后重跑即可转为硬门槛；白名单空表（瞬时队列/运行态游标）按表名与理由逐项登记。执行完成后建议检查：

- business 相关页面应有可见数据：AI 账户、分组、API Key、路由策略、授权、团队、代理、公告、供应商与模型目录、响应检查、外部来源、题库、媒体任务。
- 使用记录、审计、操作日志等页面在对应域接线后，可按 `mockdata` 或 `造数` 关键字检索。
- `--mockdata-summary-file` 写出的摘要中的 mock 用户口令与 API Key 明文可用于本地登录和网关请求验证。
- 派生页面（用量统计、AI 性能、AI 健康、IP 统计等）有数据的前提是常驻 jobs 重建完成；重建完成前这些页面可能暂时为空。
- 覆盖校验 exit 3 时，不能把「页面有部分数据」当成验收通过：先按输出的 Empty / NotCovered 清单区分「未接线域」与「真实缺陷」，后者修复后重跑。
