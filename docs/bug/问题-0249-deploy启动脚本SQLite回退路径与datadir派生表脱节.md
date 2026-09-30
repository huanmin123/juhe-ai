# BUG-0249 deploy 启动脚本 SQLite 回退路径与 datadir 派生表脱节

## 基本信息

- 编号：BUG-0249
- 状态：已修复（2026-09-30 批次）
- 严重程度：P2（触发条件窄：发布包直跑 + SQLite + 五路径 env 全未配置 + bootstrap=true）
- 发现时间：2026-09-30
- 发现方式：全面审查（多子代理复审+主代理核验）
- 模块：部署脚本（deploy/start.sh + start.ps1）
- 关联计划：无
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：启动脚本 bootstrap 的 SQLite 路径回退值与 gateway/jobs 零配置派生表不一致，ensure+seed 写到运行时不会打开的库文件。
- 期望：maintenance bootstrap 写入的库文件与 gateway/jobs 实际打开的库文件同源。
- 实际：回退使用 Node 时代长名，gateway/jobs 使用短名派生，5 个库文件脱节（预检空转、遗留无用库文件）。
- 影响范围：发布包直跑 + SQLite + 五路径 env 全未配置 + bootstrap=true 场景；README 最低配置样例显式配置全套路径时无此问题；PG 模式不受影响。

## 复现步骤

1. 取发布包直跑形态，不配置五个 SQLite 路径 env，bootstrap=true；
2. 执行 start.sh/start.ps1 的 `run_go_maintenance_bootstrap`；
3. `./data/` 下生成 Node 时代长名库文件（如 `juhe-ai.sqlite3`），gateway/jobs 启动后打开的是短名派生库（如 `business.sqlite3`），bootstrap 结果不被消费。

## 环境信息

- 分支 / 版本：master
- 数据状态：生产为 PG 模式不受影响；SQLite 直跑形态仅产生无用库文件，不损坏数据
- 是否稳定复现：是（满足触发条件即必现）

## 根因分析

- 表象：bootstrap 库文件与运行库文件脱节。
- 真实根因：start.sh:278-284/start.ps1:104-109 的 `run_go_maintenance_bootstrap` 在路径 env 未配置时回退 Node 时代长名（`./data/juhe-ai.sqlite3`、`juhe-ai-chat.sqlite3` 等），而 gateway/jobs 自 2026-09-19 起零配置派生表用短名（`business.sqlite3`、`chat.sqlite3`、`dataset.sqlite3`、`usage-catalog.sqlite3`、`stats.sqlite3`；datadir.go:29-38、jobs worker_config.go:225-262）。
- 为什么会发生：2026-09-19 派生表切换未同步启动脚本的回退默认值。

## 修复方案

- fallback 改为短名派生同源值，或未配置时 fail-fast 提示；同步 `deploy/README.md`。

## 验证结果
- start.sh/start.ps1 五路径 fallback 改短名派生同源值（business.sqlite3 等），注释指明与 datadir 固定名表同源（2026-09-19 起）；codex shard root 不变。
- 相邻缺陷一并修复：数据根先解析 JUHE_AI_DATA_DIR（进程 env 优先、backend/.env 回退、缺省 ./data，对齐 datadir.Dir）；五路径与 shard root 读取改进程 env 优先、.env 回退（对齐 postgres 分支 JUHE_AI_POSTGRES_URL 读法，消除 bootstrap 与 gateway/jobs 的优先级不对称）。
- deploy/README.md 同步：bootstrap 段回退事实 + 旧长名"仅显式配置时有效"说明。
- 验证：bash -n 与 pwsh 语法检查通过；长名残留扩展复查两脚本零命中（README 仅样例显式配置场景保留）。
