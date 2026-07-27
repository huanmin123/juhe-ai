# BUG-0131 Codex Responses 响应防护编辑时不可见

- 状态：已修复（待生产验证）
- 优先级：P1
- 影响范围：前端 / AI 账户 / Codex Responses
- 发现时间：2026-07-27

## 现象

新增 Codex Responses 账户时能看到响应防护开关，但编辑已保存的 API Key 账户时防护区域消失，无法确认或修改保存值。

## 根因

编辑回填错误调用了人工测试兼容性推导，并以 `account_default` 重新推导 API Key 账户为 `openai_standard`，覆盖了账户详情中已持久化的 `clientCompatibility = codex_responses`。防护区的显示条件因此不成立。

## 修复

- 编辑和克隆表单直接采用账户详情的 `clientCompatibility` 事实。
- 保留并回填 `codex_responses_safe_repair_enabled` 与 `codex_responses_strict_intercept_enabled`。
- 新增真实表单加载回归，验证编辑时区域可见且两个开关值准确。

