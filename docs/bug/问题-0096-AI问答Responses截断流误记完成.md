# BUG-0096 AI 问答 Responses 截断流误记完成

## 基本信息

- 状态：已修复（回归验证）
- 严重程度：P1
- 模块：后端 / AI 问答 / Responses / SSE
- 发现时间：2026-07-13
- 关联计划：PLAN-0103

## 现象与根因

Responses 上游只返回部分 delta 后断流时，收集器会直接返回已收集正文，路由随后把助手消息持久化为 `completed`。根因是完成判定只依赖迭代结束，没有要求出现 `response.completed`。

## 修复与验证

收集器显式跟踪完成事件；EOF 前未收到 `response.completed` 必须抛错并走失败终态。同时限制单事件/pending block `64 KiB`、reasoning/tool 累计 `192 KiB` 和 2048 个事件，不再保留未使用的完整事件数组；落库前对结构块再做有界降级，保证失败终态可写。`test:chat-responses-sse` 覆盖截断流、超大/累计辅助过程、无分隔大块和事件洪泛。

## 防复发

流式协议必须用协议终止事件判定成功，TCP EOF 只能表示传输结束，不能替代业务终态。
