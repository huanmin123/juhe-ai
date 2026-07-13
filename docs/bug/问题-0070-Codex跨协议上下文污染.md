# BUG-0070 Codex 跨协议上下文污染

## 基本信息

- 编号：BUG-0070
- 状态：已修复，待生产验证
- 严重程度：P1
- 发现时间：2026-07-13
- 发现方式：用户反馈 / 会话复查
- 模块：后端 / 网关 / Codex Responses / 协议转换 / 上下文压缩
- 关联计划：PLAN-0094
- 关联 bug：BUG-0032、BUG-0060
- 责任人：Codex

## 问题概述

- 现象：同一分组先使用显式 Responses -> Chat 映射账号，再切换到原生 Responses 账号时，原生上游可能收到网关内部的 `juhecmp.v1` compact 内容，或丢失本应由原生上游消费的 `previous_response_id`。
- 期望：每次派发都按实际账号协议从规范请求重新渲染，原生与桥接状态不互相泄漏。
- 实际：预检阶段按候选池提前改写共享请求体，随后选中的账号继承了错误协议的 body。
- 影响范围：Codex `/v1/responses` 混合候选池、跨账号重试和桥接账号停用后的会话续接。

## 复现步骤

1. 在同一分组配置原生 Responses 账号和显式 Responses -> Chat 映射账号。
2. 先通过桥接账号生成上下文状态或网关 compact 摘要。
3. 停用桥接账号、切到原生账号或触发 bridge -> native 重试，继续发送 `/v1/responses`。
4. 检查原生上游请求体是否出现 `juhecmp.v1` 或错误删除 `previous_response_id`。

## 环境信息

- 分支 / 版本：`master`，PLAN-0094 修复前
- 数据状态：同分组混合原生和桥接账号
- 浏览器 / 系统 / Node 版本：服务端协议链路，与浏览器无关
- 是否稳定复现：满足候选顺序和状态条件时稳定复现

## 根因分析

- 表象：原生上游拒绝无法识别的内部 compact item，或续接状态丢失。
- 真实根因：旧实现按整个候选池在预检阶段判断是否存在 bridge 账号，并提前修改共享请求体；账号筛选和实际派发随后可能选择另一种协议能力的账号。
- 为什么会发生：请求规范化状态和某个账号的上游渲染结果共用了同一份可变 body；`clientCompatibility` 又被错误用于推断 bridge 能力，扩大了触发范围。

## 修复方案

- 修改点：预检只建立规范请求和上下文状态；每次派发前按实际账号重新渲染；bridge 能力只认显式模型映射。
- 行为影响：内部 bridge 状态从原生请求中移除，网关 compact 摘要转成 developer 文本；外部原生 `previous_response_id` 透传并排除 Chat bridge；原生 opaque `encrypted_content` 保持不变。
- 发布异常处理：代码无 schema 变更，可直接回滚应用版本；上下文状态文件不迁移、不删除。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 专项回归 | bridge -> native -> bridge、内部摘要、外部 previous id、兼容画像 | `pnpm --filter juhe-ai-backend test:codex-cross-protocol-context` | 按实际账号渲染且无状态泄漏 | 通过 | 已执行 |
| 公共改写与 compact | 权限预检/质量重试改写不被覆盖；外部 compact 只选 native；混合池不全局截获 | 同上 | 公共改写保留，compact 按状态来源选协议 | 通过 | 已执行 |
| 状态存储 | 分片恢复、并发写入和清理屏障 | `test:codex-context-state-sharding`、`test:codex-context-state-writer-pool` | 通过 | 通过 | 已执行 |
| 端点能力 | OpenAI/Anthropic 端点能力矩阵 | `pnpm --filter juhe-ai-backend test:openai-endpoint-modes` | 通过 | 通过 | 已执行 |
| 类型检查 | 后端 TypeScript | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 通过 | 已执行 |
| 相邻 E2E | Codex 轮次切号 | `pnpm --filter juhe-ai-backend test:codex-turn-switch-e2e` | 通过 | 被基线健康检查模型夹具拦截，待 PLAN-0094 健康检查提交合入后复跑 | 待复跑 |

## 复发记录

- 时间：无
- 环境：无
- 现象：无
- 关联处理：无

## 下次遇到

- 先查什么：检查预检是否修改共享 raw/JSON body，检查实际账号模型映射。
- 重点看什么：每次重试是否从规范请求重建，上游是否出现 `juhecmp.v1/v2`。
- 如何避免误判：私有 envelope 使用精确命名空间，不能按公共 `resp_*` 前缀猜测所有权。

## 完成总结

- 完成时间：2026-07-13
- 结论：代码与专项回归已完成，待合入健康检查夹具修复后补轮次切号 E2E，并待生产验证。
- 后续建议：发布后检查跨协议请求审计的上游 body 类型与账号命中结果。
