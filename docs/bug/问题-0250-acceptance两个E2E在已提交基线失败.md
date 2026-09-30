# BUG-0250 acceptance 两个 E2E 在已提交基线失败（会话模型列表缺 gpt-5.6-sol / 操作日志未持久化 announcements.create）

## 基本信息

- 编号：BUG-0250
- 状态：已立案，待修复（预存缺陷，非当日批次引入）
- 严重程度：P1（验收链门禁失败：chat flow 与 management backbone 两个核心 E2E 红灯）
- 发现时间：2026-09-30
- 发现方式：全面审查收尾批次全量验证
- 模块：后端（gateway / chat models 目录聚合 + operationlog 持久化）
- 关联 bug：无直接关联；嫌疑引入链为 f42c5f42b（模型清单演进）至 e020a6d90（内置同名清理）之间的用户提交
- 责任人：待定

## 问题概述

- 现象 1：`TestAcceptanceChatFlow` 失败 `conversation models never listed gpt-5.6-sol`（GET /my-chat/conversations/{id}/models 返回 200 但不含期望模型，10 秒轮询超时）。
- 现象 2：`TestAcceptanceManagementBackbone/operation_logs` 失败 `management operation logs never persisted the announcements.create entry`。
- 期望：验收环境 seed 目录含 gpt-5.6-sol（model_catalog_data.go 该行 CatalogVisible:true）且会话模型列表可见；公告创建的操作日志按契约持久化。
- 实际：两项断言超时失败。

## 复现步骤

1. `cd backend-go && go test ./projects/gateway/cmd/juhe-ai-gateway/acceptance/ -count=1`；
2. 两个测试稳定失败。

## 环境信息

- 分支 / 版本：master（HEAD，工作区改动经 `git stash` 二分验证：干净 HEAD 同样失败，确认非当日批次引入）
- 是否稳定复现：是

## 根因分析

- 未定（待诊断）。已核实的事实边界：
  - 种子数据正常：model_catalog_data.go 的 gpt-5.6-sol 目录行（provider_model_gpt_gpt_5_6_sol_69ec47b65152）与 pg_schema.go DefaultSupportedModelsJSON 清单在 HEAD 均完好；
  - 会话模型列表链路：listConversationModels（generation_deps.go:427）候选=会话绑定账户可路由模型，经 runtime cache 聚合——断点应在夹具系统账户的可路由模型与目录聚合的交集为空或缺失；
  - 排除项：当日批次全部改动（0239~0249、C1~C10 清理）经 stash 二分排除。
- 排查方向：夹具创建系统账户的 supportedModels 与目录演进（f42c5f42b 清单收敛、e6922afbd 新增 gpt-6.1-sol、e020a6d90 同名清理）的交互；operation_logs 失败与之可能同源（同一夹具链路）或独立。

## 修复方案

- 待诊断后定。

## 验证结果

- 立案待修。诊断入口：`go test ./projects/gateway/cmd/juhe-ai-gateway/acceptance/ -run TestAcceptanceChatFlow -count=1 -v`（gateway 日志 tail 显示 models 端点 200 轮询至超时）。

## 防回归与观测性备注

- 本立案与当日批次解耦的证据必须保留：stash 二分结论写入本文件即为此目的。
