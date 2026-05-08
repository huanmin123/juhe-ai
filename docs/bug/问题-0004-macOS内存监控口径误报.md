# BUG-0004 macOS 内存监控口径误报

## 基本信息

- 编号：BUG-0004
- 状态：已修复
- 严重程度：P2
- 发现时间：2026-05-08
- 发现方式：用户反馈
- 模块：后端 / 存储 / 文档
- 关联计划：无
- 关联 bug：BUG-0002
- 责任人：AI

## 问题概述

- 现象：统计概览的“系统性能 / 网络吞吐趋势”中，macOS 主机内存长期显示 80% 以上。
- 期望：macOS 下内存曲线应接近活动监视器的实际压力口径，不应把可快速回收的系统缓存误报为应用占用。
- 实际：后台 worker 使用 Node `os.totalmem()` 和 `os.freemem()` 计算 `(total - free) / total`，macOS 上会把 inactive、speculative 和文件缓存等可回收页面计入已用内存。
- 影响范围：仅影响系统监控图中的主机内存百分比和写入 `system_metrics_samples` / `system_metrics_hourly` 的后续样本；不影响网关调度、账户状态、业务用量和成本统计。

## 复现步骤

1. 在 macOS 主机运行后端和后台 worker。
2. 打开统计概览，查看“内存平均”曲线。
3. 同时对比活动监视器或 `vm_stat`，可见 `swap` 为 0 且存在大量 inactive / file-backed 可回收页面，但页面内存仍显示 80% 以上。

## 环境信息

- 分支 / 版本：当前工作区
- 数据状态：Mac 发布环境 SQLite 已有系统监控采样数据
- 浏览器 / 系统 / Node 版本：macOS Darwin 24.6.0，Node 25.9.0
- 是否稳定复现：是

## 根因分析

- 表象：图表显示内存占用高，用户怀疑 Docker 或本项目占用过大。
- 真实根因：macOS 的 `os.freemem()` 只接近完全空闲内存，不能代表活动监视器的可用内存或内存压力；系统缓存会被算入已用，导致 `memory_used_percent` 偏高。
- 为什么会发生：后台监控一开始使用了跨平台的 Node 内存 API，没有针对 macOS 的虚拟内存页分类做平台适配。

## 修复方案

- 修改点：`backend/src/modules/background/background-jobs.ts` 新增 macOS 内存采样分支，优先读取 `vm_stat`，按 `Anonymous pages + Pages wired down + Pages occupied by compressor` 计算实际已用内存；解析失败时回退原 Node 口径。
- 兼容影响：Linux / Windows 保持原有算法；macOS 新样本会低于历史旧样本，小时聚合会在新采样进入后逐步回归正常。
- 回滚方式：恢复为 `os.totalmem()` / `os.freemem()` 计算即可，但 macOS 会重新出现缓存误报。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 类型检查 | 代码类型检查 | `pnpm typecheck` | 通过 | 通过 | 通过 |
| 构建验证 | 项目构建 | `pnpm build` | 通过 | 待填 | 未执行 |
| 功能验证 | Mac 采样口径 | 对比 `vm_stat` 输出中的 anonymous / wired / compressor 页面 | 内存百分比接近活动监视器实际占用，不再接近 `top PhysMem used` | 当前 Mac 旧 Node 口径约 87.8%，新口径约 51.5%，约 8.23 GiB | 通过 |
| 回归验证 | 非 macOS 平台 | 代码审查 | Linux / Windows 保持原有计算方式 | 符合预期 | 通过 |

## 复发记录

- 时间：暂无
- 环境：暂无
- 现象：暂无
- 关联处理：暂无

## 下次遇到

- 先查什么：先对比页面 `memory_used_percent`、Node `os.freemem()`、`vm_stat`、`memory_pressure` 和活动监视器。
- 重点看什么：macOS 是否有 swap、compressor 是否快速增长，以及 Docker / Colima 虚拟机 RSS 是否真的接近配置上限。
- 如何避免误判：不要把 macOS `top` 的 `PhysMem used` 或 Node `totalmem - freemem` 直接解释成应用实际占用；需要区分可回收缓存和真实内存压力。

## 完成总结

- 完成时间：2026-05-08
- 结论：已修复 macOS 内存统计口径，新采样使用更接近活动监视器的实际内存压力口径。
- 后续建议：部署后观察 1 到 2 个采样周期，并等待小时聚合数据自然刷新；旧采样历史不会自动重算。
