# BUG-0228 F3/F4 租约启动 fail-fast 发布窗口重启循环

## 基本信息

- 编号：BUG-0228
- 状态：已修复（待发布：配置修复随 compose.yml 生效，发布时须同步上传，见"修复方案"）
- 严重程度：P2（发布过渡窗口 gateway 分钟级不可用，租约 TTL 过期后自愈，无数据丢失、无双写）
- 发现时间：2026-09-28
- 发现方式：自查（16:00 发布后验证发现 gateway Restarting；取证容器日志实际为 8 次启动失败，runbook 原记录 2 次失真）
- 模块：网关 / 脚本
- 关联计划：无
- 关联 bug：BUG-0225（同发布窗口部署形态变更：非 root + chown）；BUG-0227（同发布窗口旧 root 容器遗留 root:root spool 文件事故，drain 代码修复另行任务，本文只登记部署侧 chown 时序配套）；BUG-0208（F4 租约运行期语义，与本文启动期 fail-fast 不同面）
- 责任人：待定

## 问题概述

- 现象：2026-09-28 16:00 全量发布（compose recreate gateway）后，新 gateway 容器从 16:00:25 到 16:00:37 连续 8 次启动失败退出，错误原文 `gateway startup failed: F3 audit owner lease held by another owner process`，16:00:44 才成功接管转 healthy（容器日志取证）。
- 期望：发布 recreate 窗口新 gateway 在启动期等待前任租约过期后一次性接管，不出现 fail-fast 退出与重启循环。
- 实际：新容器按 compose `restart: unless-stopped` 反复"退出→Docker 指数退避重启"，直到前任 F3 租约 TTL 过期才自愈；本次观测循环窗口约 19s（16:00:25 首败 → 16:00:44 成功）、共 8 次失败。
- 影响范围：仅发布/重启过渡窗口的 gateway 可用性（管理面与 `/v1` 同窗口不可用）；租约正确性无损（fail-fast 保证不双写）。F4 operation log 为同款启动 fail-fast（`backend-go/projects/gateway/cmd/juhe-ai-gateway/main.go:645-653`），理论同窗口，本次先撞在 F3（`main.go:587-595` 先于 F4 获取）。

## 复现步骤

1. 生产 gateway 正常运行，持有 F3 audit / F4 operation log DB 租约（TTL 默认 30s）。
2. 执行 `docker compose up -d gateway`（recreate：compose 先 stop 旧容器，SIGTERM 10s 宽限后 SIGKILL）。
3. 旧容器优雅关闭预算超 10s（见根因分析第 2 步）→ 被 SIGKILL，`defer` 租约释放链未执行。
4. 新容器启动 acquire 租约失败 → fail-fast `os.Exit(1)` → Docker 重启循环，直至租约 TTL 过期（观测 ≤30s + 退避节奏）。

概率性复现：取决于旧容器优雅关闭是否超 10s 宽限。历史同类观测：2026-09-27 23:19 发布 2 次 Restarting、2026-09-28 16:00 发布 8 次启动失败（次数随关闭耗时与退避节奏浮动）。

## 环境信息

- 分支 / 版本：生产单机 Docker（`docker/single-server/`）；16:00 发布 gateway 二进制 `ab8d131c9`
- 数据状态：PG 租约行 `juhe_dataset.audit_log_owner_leases`（lease_key `f3-audit-log-persistence`）lease_until 未过期
- 浏览器 / 系统 / Node 版本：Debian 13 + Docker Compose（不适用）
- 是否稳定复现：否（概率性，见复现步骤）

## 根因分析

- 表象：新 gateway 容器循环重启，日志 `gateway startup failed: F3 audit owner lease held by another owner process`。
- 机制链（逐步取证确认，以下行号均为当前源码）：
  1. compose recreate 先 stop 旧容器：SIGTERM 后 10s 宽限即 SIGKILL。
  2. 旧 gateway 优雅关闭预算超 10s 宽限是常态路径：mainServer Shutdown 10s（`backend-go/projects/gateway/cmd/juhe-ai-gateway/main.go:923`）+ J3b management Shutdown 5s（`main.go:934`）+ health server Shutdown 5s（`main.go:944`）三段串行，其后才是 defer 链（F4 producer drain → 租约 Close → store Close → 共享 pool Close）；任一 HTTP shutdown 偏慢即耗尽 10s 宽限。
  3. SIGKILL 截断 defer：`defer auditLease.Close()`（`main.go:598`）/`defer operationLease.Close()`（`main.go:657`）未执行。Close 的主动释放本应把 lease_until 置 epoch（`backend-go/projects/gateway/internal/auditlog/store.go:321-324`，PG 写 `to_timestamp(0)`），让新进程立即可接管。
  4. 于是 DB 租约行存活到最后一次续租时刻 + TTL：默认 30s（`internal/auditlog/config.go:21`、`internal/operationlog/config.go:40`），续租节奏 TTL/3、最低 1s（`internal/auditlog/owner.go:20-22`）。
  5. 新进程启动 `AcquireOwnerLease`（`internal/auditlog/store.go:249`；PG SQL `WHERE juhe_dataset.audit_log_owner_leases.lease_until <= clock_timestamp()`，`store.go:81-84`）条件不满足 → 返回 ok=false、err=nil。
  6. F3 fail-fast：`main.go:594` `fail(errors.New("F3 audit owner lease held by another owner process"))`，fail() 输出 `gateway startup failed` 日志并 `os.Exit(1)`（`main.go:1206-1213`）；F4 同款在 `main.go:651-653`。
  7. compose `restart: unless-stopped` 让 Docker 按指数退避重启 → 循环，直到 TTL 过期自愈（本次 16:00:44）。
- 真实根因：启动 fail-fast 契约的设计目标是拒绝"活 owner 并存"（第二个持有者会永久 fence 第一个，见 `main.go:574-579` 注释），但未区分发布过渡态——前任已被 SIGKILL，DB 租约只是 TTL 残留，并不存在活 owner。等待机制 `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT`（`main.go:1115-1174`，F3/F4 共用，在两个获取点之前解析一次，`main.go:580-586`）已实现且默认空保持 fail-fast，但仓库 compose 与生产 .env 均未配置，发布窗口等效 fail-fast 裸奔。
- 为什么会发生：单机 Docker 形态（BUG-0193 迁移）以来 compose stop 默认 10s 宽限从未与 gateway 优雅关闭预算（10s+5s+5s+defer 链）对齐；低频发布场景下该循环被长期当作"预期瞬态"接受（runbook 09-27/09-28 多次记录"启动期 Restarting 属预期"），16:00 发布 8 次循环把可用性代价放大到必须修复。
- runbook 记录失真：16:00 发布记录原写"前置 2 次 Restarting"，与容器日志 8 次（16:00:25-16:00:37）不符，已在私有 runbook 勘误。

## 修复方案

部署侧候选对比（取证报告三选项）：

- **缩短租约 TTL（如 30s→5s）：否决**——放大 PG 抖动脆弱性：续租间隔 TTL/3（最低 1s），TTL 过小时任一次续租失败/网络抖动即让活 owner 误判丢租，运行期可靠性受损。
- **调大 compose stop_grace_period（10s→30s+）：否决**——只覆盖"优雅关闭预算落在 10-30s 区间"的场景，预算上限仍可能超出（20s+ defer 链）；且每次发布 recreate 都拉长停机窗口，代价由常态发布承担。
- **启动期有界等待（`JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT`）：采纳**——机制已实现（`main.go:1115-1174`）：被持有时 1s 间隔重试到 deadline，超时维持 fail-fast 原文案（不静默排队活 owner 并存场景）；`err` 非 nil 立即返回不重试（传输错误与"被持有"语义不同）；默认空/0 完全保持既有 fail-fast 契约。等待成本只发生在发布过渡窗口。

修改点：

- `docker/single-server/compose.yml`：gateway `environment` 增 `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT: 45s`（> 30s TTL 的生产实例值；jobs 无启动 fail-fast 租约——F1/F2/J1/J3a 均为运行期 supervisor 重试——不配）；gateway `healthcheck.start_period` 30s→75s（等待发生在 health server 监听之前：45s 等待 + 启动余量；探测间隔与次数不放宽）。
- `docker/single-server/README.md`：".env 契约"节登记该变量（作用、默认值、生产取值、生效面 F3/F4、healthcheck 联动）；BUG-0225 非 root 前置补 chown 时序（见下）。
- `docs/deploy/部署指南.md`：同步发布序列说明（发布时须同步上传新 compose.yml）。
- `docker/single-server/deploy.sh`：头注释第 3 条更新——配置等待后发布不再预期重启循环，healthy 滞后至多约 1 分钟属预期。
- `.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md`：16:00 发布记录勘误为 8 次；env 与 healthcheck 变更登记（发布需同步上传 compose.yml）；chown 时序步骤。

同窗口部署侧配套（BUG-0227 的部署面，drain 代码修复另行任务）：BUG-0225 发布前置 `chown -R 1000:1000 /opt/juhe-ai/data/app` 在 16:00 发布序列中执行于旧容器仍在运行时，旧 root 容器停止前又写入 2 个 root:root 0600 spool 文件，app 侧进程无权读取。部署侧修正：**chown 必须在旧容器完全停止之后执行**，或发布完成后复查 `data/app/data/usage-record-spool` 属主并对 root 属主残留文件补 chown。

行为影响：

- 配置 45s 后：发布 recreate 窗口新 gateway 最多等待 45s 后接管（启动日志 `owner lease held by another owner process, waiting for predecessor lease expiry`），healthy 滞后至多约 1 分钟（75s start_period 内探测失败不计入）；等待超时仍 fail-fast，真·双 owner 场景不被吞掉。
- 非发布场景（误起第二个 gateway 进程）：等待 45s 后仍 fail-fast 退出，行为可预期、可观测。
- 默认空/未配置：与既有 fail-fast 契约完全一致（compose 显式值只影响本生产实例）。

发布异常处理：

- 回滚 = 服务器 `/opt/juhe-ai/compose.yml` 回退上一版（该 env 与 start_period 同文件），行为即回到 fail-fast + 重启循环（TTL 过期自愈）。
- **发布时须同步上传新 compose.yml**：deploy.sh 只上传 `build/bin` 二进制不上传配置；不上传则等待配置静默不生效，重启循环照旧。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元/二进制级 | 等待机制（解析、1s 重试到 deadline、超时维持 fail-fast、err 不重试） | `backend-go/projects/gateway/cmd/juhe-ai-gateway/w1_owner_acquire_wait_test.go`（随等待机制实现交付） | 全部通过 | 本次未能执行：当前工作区 cmd 包存在并行在途改动（`chain_catalog.go:204` `undefined: chainInheritCustomCatalogCapabilities` 编译失败，与本文档改动无关——本文档未改任何 Go 代码）。测试随机制交付时已通过 | 待工作区稳定后复跑 |
| 配置校验 | compose.yml 结构（gateway env、healthcheck、jobs 不受影响） | 本机无 docker（`docker compose config` 不可用），改用 python `yaml.safe_load` 解析并断言 | YAML 解析通过；gateway `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=45s`、`start_period=75s`；jobs 仍 `30s` 且无该 env | 解析与断言全部通过（2026-09-28 本机执行） | 通过 |
| 脚本语法 | deploy.sh 注释更新后语法 | `bash -n docker/single-server/deploy.sh` | 语法通过 | 通过（2026-09-28 本机执行） | 通过 |
| 生产验证 | 发布过渡窗口行为 | 下次发布（deploy.sh all + 手动上传新 compose.yml）后观察 gateway 启动 | 启动期出现等待日志、无重启循环、healthy 滞后 ≤75s | 待发布 | 待执行 |

## 复发记录

- 时间：无（修复待发布。历史同类观测：2026-09-27 23:19 发布 2 次、2026-09-28 16:00 发布 8 次循环，均为同一机制、非独立复发）
- 环境：不适用
- 现象：不适用
- 关联处理：不适用

## 下次遇到

- 先查什么：发布/重启窗口 gateway Restarting 循环 + `F3/F4 ... lease held by another owner process` → 属本机制（TTL 过期自愈）。先确认服务器 `/opt/juhe-ai/compose.yml` 是否已上传新版并配置 `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT`（deploy.sh 不上传配置，漏传则修复不生效）。
- 重点看什么：**非发布窗口**出现该文案 = 真·双 owner（误起第二进程/配置错误），fail-fast 是正确行为——查租约归属 `SELECT lease_until, owner_id, fence_token FROM juhe_dataset.audit_log_owner_leases;`，按 owner 进程排查，不要等自愈。
- 如何避免误判：①不再把"发布期 Restarting 属预期"当常态接受——本修复已消除该过渡态，若配置后仍现循环说明 compose.yml 未上传或版本回退；②runbook 登记失败次数时以容器日志为准（`docker compose ps` / `docker inspect` RestartCount / logs），不凭印象写（16:00 记录 2 次 vs 实际 8 次的失真教训）。

## 完成总结

- 完成时间：2026-09-28（配置与文档交付）；生产生效待下次发布
- 结论：根因 = compose stop 10s 宽限 < gateway 优雅关闭预算（10s+5s+5s+defer 链）→ SIGKILL 截断 defer 租约释放 → DB 租约 TTL 残留 → 新进程启动 fail-fast `os.Exit(1)` → `restart: unless-stopped` 重启循环直至 TTL 过期自愈。修复 = 既有等待机制（`main.go:1115-1174`）的部署侧配置：compose 显式 `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=45s` + gateway healthcheck `start_period=75s`，文档与脚本注释全链同步；同发布窗口的 spool root 属主问题按 BUG-0227 另行任务修复，部署侧已补 chown 时序契约。
- 后续建议：下次发布时同步上传新 compose.yml 并按"验证记录"生产栏观察；发布后复查一次 `data/app/data/usage-record-spool` 属主（BUG-0227 部署侧）；若未来 gateway 优雅关闭预算发生变化，需重估 45s/75s 取值与 stop_grace_period 的关系。
