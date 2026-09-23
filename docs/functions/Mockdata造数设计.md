# Mockdata 造数设计

> 面向本地演示、测试联调和页面验收。
> 唯一造数入口是仓库根目录的 `pnpm mockdata`，编排 Go `maintenance` 与 Go `jobs` 的既有一次性命令，在本地 SQLite 数据根上重建一套可重复执行的离线造数；不进入后端运行请求链路。历史（Node）实现已归档至 `migration-backup/node/final-archive/`，仅作只读参照，不再是权威实现。

## 1. 目标

- 一条命令在指定 SQLite 数据根上生成完整业务闭环数据，覆盖管理端与公开面的全部页面数据来源。
- 覆盖 business 库（系统账户与配套用户、AI 账户、分组、路由策略与分组绑定、API Key、授权与授权来源、团队、代理、公告、自定义模型目录、响应检查策略、外部来源系统、模型检测题库、OAuth/OIDC 第三方接入、账户测试任务、熔断与时间计划样本）、usage 分片与分片登记、stats 原始采样、observability（审计、操作、运行日志、公开接口日志、后台清理目标）、chat/codex-context/J3b 模型检测。
- 覆盖 OpenAI OAuth、OpenAI API Key、`openai_standard`、`codex_responses`、多上游 Key、图像生成、模型映射、标签、账号内 Key 运行态、待检查、停用、限流、冷却、错误、不可调度和时间计划等状态 / 类型样本；覆盖 API Key 的优先级故障转移、轮询、加权轮询、绑定禁用、额度窗口、过期、停用和时间计划样本。
- 默认生成近 31 天明细与监控样本；派生表（`usage_stats_*`、`*_windows`、排行、健康小时、账号质量等）不伪造，全部由 jobs 聚合 / 窗口任务重建。
- 可重复执行：每次先按固定清理标识删除上一批数据，再重新插入；执行结束时做只读覆盖校验。

## 2. 职责边界

Mockdata 是项目里「可复用本地造数」的唯一职责入口：

- 造数实现只存在于 `backend-go/projects/maintenance/internal/mockdata/` 与编排脚本 `scripts/mockdata.mjs`；不再新增独立的 `seed-*`、`demo-*`、`sample-*`、`fixture-*` 造数脚本。某段造数逻辑一旦需要被脚本、页面验收或人工联调复用，必须收口到 mockdata 包的对应域。
- `maintenance --seed`（seedDefaults）只负责系统启动所需的最小默认数据（默认超级管理员、供应商、默认分组和系统设置），不是业务演示 / 测试造数入口。
- 造数写出的每一行都必须带稳定清理标识（见第 5 节）；派生表一律通过既有聚合器 / 窗口任务重建，不手工伪造。
- 新增功能只要包含页面菜单、列表、筛选项、统计卡片、趋势图、日志、审计、后台任务状态、公开接口、运维监控或可视化表格，就必须同步扩展 Mockdata：补正向样本与不可调度 / 失败 / 停用类样本、扩展覆盖断言，并保证重复执行的清理收敛。
- 新增表或统计窗口时，必须明确默认行数、状态分布、时间跨度、清理标识；页面依赖预聚合表时由 jobs 重建路径覆盖，Mockdata 不直接写派生表。
- 新增功能验收时必须执行 `pnpm mockdata` 并检查相关页面不是空态；没有适合的模拟数据时，在本文件说明原因和替代验证方式。

当前目录结构（Go 实现）：

| 位置 | 职责 |
| --- | --- |
| `scripts/mockdata.mjs` | 唯一编排脚本：参数解析、env 解析、常驻进程预检、顺序执行四步子命令、透传退出码 |
| `backend-go/projects/maintenance/cmd/juhe-ai-maintenance/mockdata.go` | CLI 层：`--mockdata` / `--verify-mockdata-coverage` 参数校验、路径解析、JSON 报告输出与退出码 |
| `backend-go/projects/maintenance/internal/mockdata/mockdata.go` | 骨架：五个造数域注册、参数边界常量（`--days` 1..90、`--daily-requests` 1..500）、主流程 |
| `backend-go/projects/maintenance/internal/mockdata/paths.go` | 数据根 / 日志目录与各存储路径解析（含 usage、codex-context 分片布局） |
| `backend-go/projects/maintenance/internal/mockdata/env.go` | 存储上下文：SQLite 句柄懒打开、事务、日志器、域产出记账 |
| `backend-go/projects/maintenance/internal/mockdata/cleanup.go` | 清理标识常量与幂等清理（含外键子表的显式清理规则） |
| `backend-go/projects/maintenance/internal/mockdata/summary.go` | `mockdata-summary.json` 摘要写出与读取 |
| `backend-go/projects/maintenance/internal/mockdata/coverage.go` | 只读覆盖校验：全存储全表枚举、空表白名单、关键状态硬断言 |
| `backend-go/projects/maintenance/internal/mockdata/autofill.go` | 自动补全集合：对无业务域写入的表插入 1..3 行结构合法占位行 |
| `backend-go/projects/maintenance/internal/mockdata/domain_business.go` | business 库造数域 |
| `backend-go/projects/maintenance/internal/mockdata/domain_usage.go` | usage-catalog 库与 usage 分片造数域 |
| `backend-go/projects/maintenance/internal/mockdata/domain_stats.go` | stats 库原始写入面造数域（派生聚合调用既有聚合器重建） |
| `backend-go/projects/maintenance/internal/mockdata/domain_observability.go` | 运行日志、审计、操作日志、公开接口日志、后台清理目标等 observability 造数域 |
| `backend-go/projects/maintenance/internal/mockdata/domain_chat_codex_modelcheck.go` | chat 库、codex context 分片、J3b 模型检测造数域 |
| `backend-go/projects/jobs/cmd/juhe-ai-jobs/run_jobs_once.go` | jobs `-run-jobs-once` 一次性执行入口（脚本第 5 步用它重建派生表） |

## 3. 命令

### 3.1 使用前提

先停止本地常驻 `gateway` 与 `jobs`（`pnpm dev` 会话），再执行造数；常驻聚合器与脚本的一次性派生重建并发会重复累计同一批用量游标，并争抢 SQLite 写锁。脚本启动时会探测两个 health 端口（缺省 `127.0.0.1:3306` / `127.0.0.1:3305`），检测到常驻进程默认中止；确认目标数据根完全隔离时可加 `--force` 跳过预检。造数完成后重启服务。

### 3.2 编排入口

在项目根目录执行：

```powershell
pnpm mockdata
```

调整时间跨度和每日请求数：

```powershell
pnpm mockdata -- --days 31 --daily-requests 120
```

脚本参数：

| 参数 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| `--days` | `31` | `1` 到 `90` | 造数历史跨度天数 |
| `--daily-requests` | `120` | `1` 到 `500` | 每日生成的使用记录数 |
| `--data-dir` | `JUHE_AI_DATA_DIR`，缺省 `.local/dev/data` | 任意路径 | SQLite 数据根，全部存储路径由它派生 |
| `--log-dir` | `JUHE_AI_LOG_DIR`，缺省 `.local/dev/logs` | 任意路径 | 日志文件目录（运行日志 JSONL 等） |
| `--skip-rebuild` | 关 | 开关 | 跳过 jobs `-run-jobs-once` 派生重建 |
| `--skip-verify` | 关 | 开关 | 跳过 `--verify-mockdata-coverage` 覆盖校验 |
| `--force` | 关 | 开关 | 检测到常驻 gateway/jobs 时仍继续（不建议） |
| `--help` / `-h` | — | — | 打印用法 |

脚本编排顺序（任一步非零退出即停，子进程输出透传）：

1. 预检：探测常驻 gateway/jobs，检测到即中止（`--force` 跳过）。
2. `juhe-ai-maintenance --ensure-schema --seed`：幂等建库与最小种子；脚本按 datadir 固定名表显式传 `--paths`，保证 maintenance 建的库就是 jobs 随后打开的同一批文件，codex 分片数取 `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT`（缺省 16）。
3. `juhe-ai-maintenance --mockdata --driver sqlite --mockdata-data-dir <dir> --mockdata-log-dir <dir> --mockdata-days N --mockdata-daily-requests M`：清库后逐域造数。
4. `juhe-ai-jobs -run-jobs-once=<SQLite 适用任务集合>`：一次性执行已注册的聚合 / 窗口 / 维护任务（复用真实聚合器与窗口刷新逻辑），重建全部派生表；集合由 jobs 注册表快照推导（当前 28 个 go-wired 且 SQLite 适用任务）。未知任务名 exit 2 并列出全部可用任务。
5. `juhe-ai-maintenance --verify-mockdata-coverage --mockdata-data-dir <dir>`：只读覆盖校验，未 Ready 时 exit 3。

两个 Go 入口由脚本 `go build` 到临时目录后直接运行（不用 `go run`，保证退出码逐值保真），结束前清理临时目录。显式 `JUHE_AI_DATABASE_DRIVER=postgres` 会被脚本直接拒绝（exit 2）：造数只支持 SQLite 数据根。

### 3.3 底层命令与退出码

`juhe-ai-maintenance --mockdata` 与 `--verify-mockdata-coverage` 互斥（也不与其他 maintenance 命令同用）；两者都要求显式数据根（`--mockdata-data-dir` 或 `JUHE_AI_DATA_DIR`），不提供隐式回退，`--driver` 只接受空或 `sqlite`，`--dsn` 一律拒绝。

| 命令 | exit 0 | exit 1 | exit 2 | exit 3 |
| --- | --- | --- | --- | --- |
| `--mockdata` | 成功，stdout 输出一份 JSON Report | 造数运行失败（已完成的报告仍会输出） | 用法错误（参数越界、驱动不受支持等） | — |
| `--verify-mockdata-coverage` | 覆盖 Ready | 校验运行失败 | 用法错误 | 覆盖未 Ready（空表 / 断言未过清单见 stdout JSON） |
| `juhe-ai-jobs -run-jobs-once` | 全部任务处理完成（注册表 wired 但当前部署形态 disabled 的任务按 skipped 计入成功并带 warning） | 任一任务执行返回错误（已完成结果仍输出到 stdout） | 任务名清单解析失败或未知任务名 | — |

摘要文件写在 `<数据根>/mockdata-summary.json`：记录 mock 用户明文口令（`mockdata123456`）、API Key 明文、资源计数和域接线快照（`domains`）。该文件只用于本地联调，不提交、不脱敏外发。

## 4. 数据边界

造数只写 SQLite 数据根下的存储；数据根必须显式给出。各域覆盖范围：

| 存储 | 造数内容 |
| --- | --- |
| `business.sqlite3` | 配套用户 7 个（1 个普通管理员 + 普通用户）、分组（含个人分组与高并发分组）、31 个 AI 账户样本（含状态与类型覆盖）、路由策略与 API Key（含本地网关明文 Key）、38 条授权样本、团队、代理、公告、自定义模型目录、响应检查策略、外部来源系统、模型检测题库、OAuth/OIDC 第三方接入、账户测试任务、熔断与时间计划样本 |
| `usage-shards/` + `usage-catalog.sqlite3` | 按 `<root>/usage-shards/<YYYY>/<MM>/<DD>/usage-YYYYMMDD-sNN.sqlite3` 三级布局写入分片，并在 catalog 四表登记；默认 31 天约 1.5 万条量级，含健康检查逐账户逐小时样本、三条上游响应模型定向样本 |
| `stats.sqlite3` | 原始写入面：系统指标与事件循环采样、`background_task_runs`、`account_usage_snapshots`、dirty 标记；派生聚合表不在此伪造 |
| observability 各专库 | 审计日志（含 payload blob）、操作日志、运行日志索引 + JSONL 文件（写 `--log-dir`）、公开接口日志、后台记录清理目标 |
| `chat.sqlite3` | 会话、消息、图片资产文件、上下文与图像生成样本 |
| `codex-context/state-shards/` | Codex context 状态分片；分片数与 `JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT` 对齐（脚本已处理） |
| J3b 模型检测专库 | 检测 runs/items/observations、信任与基线、输入与调度样本 |

补充约束：

- 派生表不造假：`usage_stats_*`、各 `*_windows`、`usage_rank_snapshots`、`account_health_hourly`、`group_account_stats`、`client_ip_*`、`authorization_*_usage_*`、`account_quality_*`、`system_metrics_*` / `process_event_loop_*` 的小时与趋势表，全部由脚本第 4 步的 jobs `-run-jobs-once` 或常驻 jobs 启动后重建；页面读取路径与真实数据一致。
- 上游响应模型定向样本固定三条：trace 分别为 `mockdata-usage-coverage_upstream_response_model_match`、`mockdata-usage-coverage_upstream_response_model_mismatch`、`mockdata-usage-coverage_upstream_response_model_unmapped_mismatch`，摘要 `upstreamResponseModelSamples` 提供 ID、trace 与相关原始字段，供覆盖断言与页面检索。
- 本地网关 Key、上游 API Key、OAuth Token 和代理密码均为模拟值，不会真实请求外部服务。
- mock 用户明文口令固定为 `mockdata123456`，只用于本地登录联调；摘要同时记录各 API Key 明文。
- 身份归属：主数据归属 seed 超管 `admin`（`sys_admin`，账户/分组/API Key/标签的主体），免登录联调推荐 `JUHE_AI_DEV_AUTO_LOGIN_USERNAME=admin`；`mockdata_admin` 是普通管理员（admin 角色）视角样本，名下有自有账户、标签与团队成员，用于管理员自有资源与 owner 作用域页面验收。

已知边界：

- SQLite-only：PostgreSQL 部署形态在脚本与 CLI 两层都被显式拒绝，不做静默造数。
- 表监控快照（F2）与 Go runtime metrics（opt-in 功能）不造数：F2 快照由 jobs 常驻采样产生，Go runtime metrics 需要显式 opt-in 的独立建表与开关，均不属于造数范围。
- 审计、操作日志、运行日志、表监控等专库文件在「从未启动过后端」的全新数据根不存在；首次 `pnpm dev` 启动后由各 owner 建库，之后再执行 `pnpm mockdata` 即可填充。
- `--mockdata` 与 `--verify-mockdata-coverage` 互斥；造数与校验通过脚本顺序编排，不合并成一条命令。
- 覆盖校验对空表有白名单（owner 运行态 / 运行时表，如 `background_job_leases`、`stats_job_state`、`account_health_current_state`、`usage_range_window_requests`、`codex_context_storage_cleanup_queue` 等），白名单外的空表或关键状态断言未过都会让校验 exit 3。

## 5. 清理策略

造数写出的每一行都必须至少在一个可检索列上带以下前缀之一，清理才能幂等收敛：

| 标识 | 适用范围 |
| --- | --- |
| 名称前缀 `造数-` | 业务数据名称（分组、账户、API Key、公告、策略等） |
| ID 前缀 `mockdata_` | 统计数据集域 ID 与配套系统用户名 |
| trace 前缀 `mockdata-` | 使用记录等记录类数据的 trace |

`--mockdata` 可重复执行：每次先按上述标识扫描删除上一批数据（外键子表本身不带标识，由显式清理规则按父键先删），再重新插入；重复执行不会叠加旧样本或重复累计派生统计。

## 6. 验证点

`pnpm mockdata` 退出码为 0 即视为编排成功；exit 3 表示覆盖校验未 Ready（剩余空表与缺库清单见脚本输出的 JSON）。常见原因按处置方式分三类：一是审计、操作日志、运行日志、表监控、任务运行、账户健康六个专库文件尚不存在——它们由 gateway/jobs 首次启动时建库，`--ensure-schema` 不建，全新数据根请先 `pnpm dev` 启动一次（随后停止）再执行造数，停止常驻进程无法解决这一类；二是派生聚合表尚未重建（使用 `--skip-rebuild`、jobs 重建未跑完或尚未启动常驻 jobs），启动常驻 jobs 后会自动补齐；三是真实断言未过，按清单修复后重跑。执行完成后建议检查：

- 各页面应有可见数据：AI 账户、分组、API Key、路由策略、授权、团队、代理、公告、供应商与模型目录、响应检查、外部来源、公开接口日志、使用记录、统计概览、用量统计、AI 性能、AI 健康、模型检测、审计、操作、运行日志、IP 统计、系统指标、表监控、后台任务、聊天、题库。
- 使用记录、审计日志、操作日志、运行日志均可按 `mockdata` 或 `造数` 关键字检索；三条上游响应模型样本可分别按 `mockdata-usage-coverage_upstream_response_model_match`、`mockdata-usage-coverage_upstream_response_model_mismatch`、`mockdata-usage-coverage_upstream_response_model_unmapped_mismatch` 的 trace 检索。
- `<数据根>/mockdata-summary.json` 中的 mock 用户口令与 API Key 明文可用于本地登录和网关请求验证。
- 派生页面（用量统计、AI 性能、AI 健康、IP 统计等）有数据的前提是第 4 步派生重建成功；用 `--skip-rebuild` 跳过或常驻 jobs 尚未重建完成时，这些页面可能暂时为空，等待重建完成后刷新即可。
- 脚本内置覆盖断言失败（exit 3）时，不能把「页面有部分数据」当成验收通过：先按输出的空表与 NotCovered 清单修复，再重跑。
