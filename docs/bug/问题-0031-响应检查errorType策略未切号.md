# BUG-0031 响应检查 errorType 策略未切号

## 状态

- 状态：已修复
- 严重程度：P1
- 模块：后端 / 网关 / 响应检查 / 账号调度
- 发现日期：2026-07-04
- 修复日期：2026-07-04

## 现象

`pnpm --filter juhe-ai-backend test:anthropic-gateway-mock-ai` 失败在 `Anthropic JSON error 换号后应返回成功`。

上游主账号返回 `200 + JSON type:error`，错误类型为 `overloaded_error`。测试期望命中管理端响应检查策略后隐藏切到备用账号，实际返回 503 `stream_server_retry_exhausted`。

## 根因

响应检查运行时把所有没有绑定 `clientProfiles` 且包含 `errorCodes` / `errorTypes` 的策略都跳过。

这个保护本意是避免未绑定客户端画像的上游 `errorCode` 直接驱动路由，但实现过宽，连协议原生 `errorType` 也一起屏蔽。Anthropic 的 `overloaded_error` 是协议错误类型，不是客户端专属错误码，因此管理端 provider 策略无法命中，最终只剩默认 `jsonPathsExists: error` 规则。默认规则是 `retry_no_avoidance`，不会排除当前账号，所以服务端重试停止并返回通用 503。

## 修复

- `backend/src/modules/gateway/response/inspection.ts`
  - 保留无画像 `errorCodes` 不命中的保护。
  - 放开无画像 `errorTypes`，允许协议错误类型驱动配置化响应检查策略。
- `backend/src/scripts/regression/response-inspection-policy-regression.ts`
  - 增加 Anthropic `overloaded_error` 无画像 `errorTypes` 策略命中断言。

## 验证

- `pnpm --filter juhe-ai-backend test:response-inspection-policy`：通过
- `pnpm --filter juhe-ai-backend test:anthropic-gateway-mock-ai`：通过
- `pnpm --filter juhe-ai-backend test:response-inspection-gateway-e2e`：通过
- `pnpm --filter juhe-ai-backend test:response-inspection-mock-ai-fields`：通过
- `pnpm --filter juhe-ai-backend test:gemini-gateway-mock-ai`：通过
- `pnpm --filter juhe-ai-backend typecheck`：通过

## 关联

- 关联测试：`backend/src/scripts/regression/anthropic-gateway-mock-ai-regression.ts`
- 关联测试：`backend/src/scripts/regression/response-inspection-policy-regression.ts`
- 与 `BUG-0030` 无直接根因关联；该问题不是 SSE 心跳-only 保护引起。
