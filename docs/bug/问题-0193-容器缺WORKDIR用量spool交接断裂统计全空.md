# BUG-0193 单机容器缺 WORKDIR 用量 spool 交接断裂导致统计全空

## 基本信息

- 编号：BUG-0193
- 状态：已修复
- 严重程度：P0
- 发现时间：2026-09-26
- 发现方式：用户反馈（生产"我的使用记录 / AI 健康监控 / 全部统计页面查询为空"）
- 模块：网关 / 后台任务 / 部署配置（docker/single-server）
- 关联计划：无
- 关联 bug：无（与 BUG-0184/0192 的可用性投影死锁为并存独立问题）
- 责任人：待定

## 问题概述

- 现象：国内单机生产（103.36.63.105）所有统计面为空——我的/管理使用记录、用量总览、AI 健康监控、账户用量全部查不到数据；`/my-usage-records`、`/my-stats/*` 接口均返回 200。
- 期望：`/v1` 网关流量产生使用记录，统计聚合按节奏出数。
- 实际：`juhe_usage.usage_records` 自上线起恒为 0 行；一切以使用记录为源的统计与聚合随之为空。
- 影响范围：生产全量统计可观测性；更严重的是网关用量记录持续写入**容器私有临时层**，每次容器重建即静默丢失（已实际丢失 22:06 前的全部记录）。

## 复现步骤

1. 打开生产管理后台任一统计页面（我的使用记录 / AI 健康监控等），恒为空。
2. `psql` 查 `SELECT count(*) FROM juhe_usage.usage_records;` 返回 0。
3. 对比两侧容器：`docker exec juhe-ai-go-gateway ls data/usage-record-spool/gateway-chain | wc -l` 持续增长；`docker exec juhe-ai-go-jobs ls data/usage-record-spool` 不存在（drain 无从消费）。

## 环境信息

- 分支 / 版本：master（go-only 单机 Docker 形态，compose working_dir 修复前）
- 数据状态：`juhe_usage.usage_records` 0 行，`juhe_stats.*` 聚合表 0 行
- 部署：Debian 13 + Docker Compose，`docker/single-server/compose.yml` + `Dockerfile.runtime`
- 是否稳定复现：是（修复前 100%）

## 根因分析

- 表象：疑似查询缺陷或 jobs 后台未调度。
- 真实根因：**写入链路断裂，且两侧各写各的容器私有目录**。用量记录的投递设计为"网关写 spool 文件 → jobs 的 usage spool drain 读同一目录写入 PG"，目录由 `JUHE_AI_USAGE_SPOOL_DIRECTORY` 配置，未配置时按 datadir 约定派生 `<DATA_DIR>/usage-record-spool`（gateway/jobs 的 `internal/datadir` 均注释明确：缺省 `./data` **相对进程 cwd**）。而：
  - `Dockerfile.runtime` 创建并挂载了 `/app/backend/{data,logs}`，但**未设置 `WORKDIR`**，Alpine 默认 cwd 为 `/`；
  - compose 未设 `working_dir`，生产 `.env` 也未配置 `JUHE_AI_USAGE_SPOOL_DIRECTORY`。
  - 结果：网关把 spool 写进自己容器的 `/data/usage-record-spool/gateway-chain/`，jobs drain 监听自己容器的 `/data/usage-record-spool`（不存在），两侧永不会合；共享挂载 `/app/backend/data` 无人使用。文件日志、chat-assets、account-health-input 等其余相对路径同样落进容器临时层。
  - 反证：`.env` 中唯一显式绝对路径 `JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=/app/backend/data/audit-payload-blobs` 工作正常。
- 为什么会发生：单机形态上线时镜像布局（`/app/backend`）与 datadir 的 cwd 相对约定之间缺少 `WORKDIR`/`working_dir` 锚定，零配置默认值在 PG-only 生产下静默落到无意义路径，无启动校验兜底（gateway 侧"缺少 spool 目录"的 fail-fast 只覆盖 env 与 stats 路径均缺失的分支）。

## 修复方案

- 修改点：`docker/single-server/compose.yml` 为 `gateway`、`jobs` 增加 `working_dir: /app/backend`（与镜像 `mkdir /app/backend/{data,logs}` 及挂载注释的设计意图一致，一并修正文件日志/chat-assets/account-health-input 等全部相对路径落盘位置）；已同步服务器 `/opt/juhe-ai/compose.yml`（原文件备份为 `compose.yml.bak-20260926`）并 `docker compose up -d gateway jobs` 重建。
- 行为影响：网关与 jobs 的相对路径数据全部改落宿主机持久目录；两侧 spool 目录自然同源为 `/app/backend/data/usage-record-spool`；jobs 容器内生成的 `account-health-input.key` 迁移到持久层（首启重新生成）。
- 发布异常处理：重建容器会丢失容器私有层内未投递的 spool 文件——本次修复前已用 `docker cp` 将网关容器内积压的 45 个文件抢救到宿主机共享目录，由 drain 补写入库；22:06 之前（用户自行重建容器时）已丢失的记录无法追回。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 部署验证 | 容器健康 | `docker compose ps` | 五容器 healthy | 五容器 healthy，gateway/jobs WorkingDir=/app/backend | 通过 |
| 功能验证 | spool 交接 | 观察共享目录 + jobs drain 日志 | 积压被消费、无报错 | 45 个抢救文件 + 新流量全部被 drain 消费，目录清零，无错误日志 | 通过 |
| 功能验证 | 记录落库 | `SELECT count(*), min/max(created_at) FROM juhe_usage.usage_records` | 行数 > 0 且持续增长 | 修复后约 1 分钟 50 行，持续增长（56+），时间跨度含抢救文件 | 通过 |
| 聚合验证 | 统计出数 | `SELECT count(*) FROM juhe_stats.usage_stats_hourly` | 聚合开始填充 | 数分钟内 32 行（2026-09-26 22 时窗口起） | 通过 |
| 持久化验证 | 文件日志/资产落盘 | `ls /opt/juhe-ai/data/app/{logs,data}` | logs/chat-assets 等出现在宿主机 | logs、chat-assets、account-health-input、usage-record-spool 全部落盘 | 通过 |

## 复发记录

- 时间：-
- 环境：-
- 现象：-
- 关联处理：-

## 下次遇到

- 先查什么：统计为空先查**源头表行数**（`juhe_usage.usage_records`），再查接口状态码——200 空列表 ≠ 查询故障；不要先怀疑 jobs 调度。
- 重点看什么：涉及双进程交接的文件目录，两侧 `pwd`/`WorkingDir` 是否一致、目录是否为容器私有层；`docker inspect` 挂载与 `docker exec ls` 实际内容比对。
- 如何避免误判：容器重建（compose up）会清空容器私有层，诊断期间的文件计数变化要先核对容器 `Created/StartedAt/RestartCount` 再下结论。

## 完成总结

- 完成时间：2026-09-26
- 结论：已修复并生产验证通过；AI 健康监控（`account_health_hourly`）待探针下一轮运行产生 `account_health_check` 类记录后由小时聚合填充，源头链路已通。
- 后续建议：
  - 部署文档已沉淀本契约（2026-09-26）：`docker/single-server/README.md`"运行时数据目录契约"章节为权威，`docs/deploy/部署指南.md` 要点+验证清单、`docs/deploy/linux/README.md` 直跑差异已同步；
  - `maintenance` 服务同为该镜像，如未来用到相对路径输出，建议一并加 `working_dir: /app/backend`；
  - 可考虑给 datadir 派生的关键交接路径（spool 目录）加启动期可写性/同源校验，避免零配置默认值再次静默失效；
  - 生产日志中 `account-list-availability-projection-maintenance` 连续超时失败并伴随 PG 死锁（40P01）为独立问题，已在 BUG-0184/0192 登记范围，需单独跟进。
