import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  gatewayRequestAbsoluteDeadlineAtMs,
  gatewayRequestDeadlineExpired,
  gatewayRequestDeadlineRemainingMs,
  gatewayRequestWaitWithinDeadlineMs
} from '../../modules/gateway/runtime/gateway-request-deadline.js'

const startedAtMs = 1_000_000
assert.equal(gatewayRequestAbsoluteDeadlineAtMs(startedAtMs, 10), startedAtMs + 10_000)
assert.equal(gatewayRequestAbsoluteDeadlineAtMs(startedAtMs, 270), startedAtMs + 270_000)
assert.equal(gatewayRequestAbsoluteDeadlineAtMs(startedAtMs, 3600), startedAtMs + 270_000, '配置超过 270 秒必须封顶')
assert.equal(gatewayRequestAbsoluteDeadlineAtMs(startedAtMs, 0), startedAtMs + 10_000, '零等待配置必须按 10 秒下限归一化')
assert.equal(gatewayRequestAbsoluteDeadlineAtMs(startedAtMs, -1), startedAtMs + 10_000, '负等待配置必须按 10 秒下限归一化')
assert.equal(gatewayRequestAbsoluteDeadlineAtMs(startedAtMs, Number.NaN), startedAtMs + 10_000, 'NaN 等待配置必须按 10 秒下限归一化')

const deadlineAtMs = startedAtMs + 10_000
assert.equal(gatewayRequestDeadlineRemainingMs(deadlineAtMs, startedAtMs + 2500), 7500)
assert.equal(gatewayRequestDeadlineRemainingMs(deadlineAtMs, deadlineAtMs + 1), 0)
assert.equal(gatewayRequestDeadlineExpired(deadlineAtMs, deadlineAtMs - 1), false)
assert.equal(gatewayRequestDeadlineExpired(deadlineAtMs, deadlineAtMs), true)
assert.equal(gatewayRequestWaitWithinDeadlineMs(12_000, deadlineAtMs, startedAtMs + 2500), 7500)
assert.equal(gatewayRequestWaitWithinDeadlineMs(1000, deadlineAtMs, startedAtMs + 2500), 1000)

const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
const preparationSource = readFileSync(new URL('../../modules/gateway/dispatch/preparation.ts', import.meta.url), 'utf8')
const suppressionSource = readFileSync(new URL('../../modules/gateway/runtime/local-suppression-preflight.ts', import.meta.url), 'utf8')
const dispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')

assert.match(preflightSource, /requestDeadlineAtMs:\s*number/, '预检上下文必须携带首次请求计算出的绝对截止时间')
assert.match(preflightSource, /gatewayRequestAbsoluteDeadlineAtMs\(startedAt,\s*activeGatewaySettings\.streamClientTotalWaitTimeoutSeconds\)/, '绝对截止时间必须从原始 startedAt 计算')
assert.match(preflightSource, /requestStartedAtMs:\s*input\.startedAt[\s\S]*deadlineAtMs:\s*input\.requestDeadlineAtMs/, '候选恢复等待必须复用原始请求截止时间')
assert.match(preparationSource, /requestDeadlineAtMs:\s*number/, '派发准备阶段必须接收同一个绝对截止时间')
assert.match(suppressionSource, /deadlineAtMs:\s*input\.requestDeadlineAtMs/, '运行态屏蔽等待不得重新开启独立窗口')
assert.match(routesSource, /precheckHalfOpenEligible === true,[\s\S]*currentPreflight\.requestDeadlineAtMs/, '主派发必须传递当前请求固定截止时间')
assert.match(dispatchSource, /assertGatewayRequestDeadlineActive\(requestDeadlineAtMs\)/, '每次真实上游尝试前必须经过截止时间闸门')
assert.match(dispatchSource, /gatewayRequestWaitWithinDeadlineMs\(delayMs,\s*requestDeadlineAtMs\)/, '重试延迟必须裁剪到请求剩余预算')
assert.match(dispatchSource, /maxWaitMs:\s*Math\.max\(1, requestDeadlineAtMs - requestStartedAtMs\)/, 'precheck 等待必须使用请求总预算而非重新固定 30 秒')
assert.match(routesSource, /gateway_request_deadline_exceeded/, '请求截止必须返回独立错误码而非伪装客户端中断')
assert.match(routesSource, /AbortSignal\.any\(\[abortController\.signal, requestDeadlineSignal\]\)/, '进行中的上游 fetch/read 必须同时受客户端和请求 deadline 信号约束')
assert.match(routesSource, /!abortController\.signal\.aborted[\s\S]*GatewayRequestDeadlineExceededError/, 'deadline 中断必须映射为独立错误且不能误标客户端中断')

console.log('gateway request deadline regression passed')
