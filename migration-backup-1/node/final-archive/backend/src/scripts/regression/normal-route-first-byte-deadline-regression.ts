import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { GatewayRequestWallBudget } from '../../modules/gateway/routing/route-coordination.js'
import { normalRouteAttemptFirstByteDeadline } from '../../modules/gateway/routing/normal-route-first-byte-deadline.js'
import { normalRouteFirstByteDeadlineAppliesToLane } from '../../modules/gateway/policy/speed-first-lane.js'

let nowMs = 100_000
const wallBudget = new GatewayRequestWallBudget({
  requestAcceptedAtMs: nowMs,
  budgetMs: 270_000,
  now: () => nowMs
})

const speedFirst = normalRouteAttemptFirstByteDeadline({
  config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 30_000 },
  gatewayRequestWallBudget: wallBudget,
  attemptStartedAtMs: nowMs,
  laneFirstByteTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 180_000
})
assert.equal(speedFirst.effectiveDeadlineMs, 30_000, '速度优先必须消费自己的首字截止')
assert.equal(speedFirst.limitingFactor, 'configured', '未裁剪时速度慢样本由速度优先配置阈值触发')

nowMs = 367_000
const wallClipped = normalRouteAttemptFirstByteDeadline({
  config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 30_000 },
  gatewayRequestWallBudget: wallBudget,
  attemptStartedAtMs: nowMs,
  laneFirstByteTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 180_000
})
assert.equal(wallClipped.effectiveDeadlineMs, 1_000, '首字截止必须为墙钟尾窗预留最终响应时间')
assert.equal(wallClipped.limitingFactor, 'wall_precommit', '墙钟裁剪不得伪装成速度慢样本阈值')

const uncommittedClipped = normalRouteAttemptFirstByteDeadline({
  config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 30_000 },
  gatewayRequestWallBudget: new GatewayRequestWallBudget({ requestAcceptedAtMs: 0, budgetMs: 270_000, now: () => 10_000 }),
  attemptStartedAtMs: 10_000,
  laneFirstByteTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 8_000
})
assert.equal(uncommittedClipped.effectiveDeadlineMs, 8_000)
assert.equal(uncommittedClipped.limitingFactor, 'uncommitted_attempt')

const laneClipped = normalRouteAttemptFirstByteDeadline({
  config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 30_000 },
  gatewayRequestWallBudget: new GatewayRequestWallBudget({ requestAcceptedAtMs: 0, budgetMs: 270_000, now: () => 10_000 }),
  attemptStartedAtMs: 10_000,
  laneFirstByteTimeoutMs: 6_000,
  uncommittedAttemptMaxLifetimeMs: 180_000
})
assert.equal(laneClipped.effectiveDeadlineMs, 6_000)
assert.equal(laneClipped.limitingFactor, 'lane_timeout')

const simultaneousLimit = normalRouteAttemptFirstByteDeadline({
  config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 30_000 },
  gatewayRequestWallBudget: new GatewayRequestWallBudget({ requestAcceptedAtMs: 0, budgetMs: 270_000, now: () => 10_000 }),
  attemptStartedAtMs: 10_000,
  laneFirstByteTimeoutMs: 30_000,
  uncommittedAttemptMaxLifetimeMs: 30_000
})
assert.equal(simultaneousLimit.effectiveDeadlineMs, 30_000)
assert.equal(simultaneousLimit.limitingFactor, 'configured', '多个限制同时到期时应归因用户配置，允许累计真实慢样本')

const exhaustedWall = normalRouteAttemptFirstByteDeadline({
  config: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 30_000 },
  gatewayRequestWallBudget: new GatewayRequestWallBudget({ requestAcceptedAtMs: 0, budgetMs: 5_000, now: () => 8_000 }),
  attemptStartedAtMs: 8_000,
  laneFirstByteTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 180_000
})
assert.equal(exhaustedWall.effectiveDeadlineMs, 0, '墙钟已耗尽时必须立即触发 timer，不得继续扩大超时窗口')
assert.equal(exhaustedWall.limitingFactor, 'wall_precommit')

assert.equal(normalRouteFirstByteDeadlineAppliesToLane('text'), true, '文本 lane 应使用普通路由首字截止')
assert.equal(normalRouteFirstByteDeadlineAppliesToLane('image'), false, '图片 lane 只能使用图片 timeout profile，不得套普通路由首字截止')

const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
const dispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
const upstreamRequestSource = readFileSync(new URL('../../modules/gateway/upstream/request.ts', import.meta.url), 'utf8')
assert.match(
  preflightSource,
  /normalConfig\.schedulingPreference !== 'speed_first'\) return undefined[\s\S]*schedulingPreference: 'speed_first'/,
  '网关只能为速度优先策略创建首字截止快照'
)
assert.match(dispatchSource, /normalRouteAttemptFirstByteDeadline\(/, '每次真实 attempt 必须在 HTTP 派发前计算有效首字截止')
assert.match(
  dispatchSource,
  /!compactionTimeoutsDisabled[\s\S]*normalRouteFirstByteDeadlineAppliesToLane\(requestLane\)[\s\S]*requestCoordination\.normalRouteFirstByteConfig[\s\S]*normalRouteAttemptFirstByteDeadline\(/,
  'dispatch 必须排除图片和 Responses 压缩请求的首字切换；其他请求不得另建重放门禁'
)
assert.doesNotMatch(dispatchSource, /automaticUpstreamReplayAllowedAfterDispatch|UpstreamReplayBlockedError/, '首字截止不得再依赖第二套自动重放许可')
assert.match(dispatchSource, /firstByteDeadlineDecision \?\?=/, '响应头与响应体必须共享同一次截止判定，不能把一个 attempt 重复累计为两次慢样本')
assert.doesNotMatch(routesSource, /normal_route_cost_first_first_byte/, '成本优先不得保留首字截止切号分支')
assert.match(routesSource, /deadline\.limitingFactor === 'wall_precommit'/, '墙钟预算截止必须交回外层处理')
assert.match(routesSource, /deadline\.limitingFactor === 'lane_timeout'[\s\S]*deadline\.limitingFactor === 'uncommitted_attempt'[\s\S]*return 'continue'/, 'lane 或 attempt 硬上限必须让真实 transport timer 归因，且不得累计速度慢样本')
assert.match(dispatchSource, /throw new NormalRouteFirstByteCutoverError\(/, '响应头前的配置型截止必须显式交回外层，不能在 dispatch 内绕过预占与重试预算')
const neutralCutoverStart = dispatchSource.indexOf('if (neutralFirstByteDeadline && normalRouteFirstByteDeadline)')
const neutralCutoverThrow = dispatchSource.indexOf('throw new NormalRouteFirstByteCutoverError(', neutralCutoverStart)
assert.ok(neutralCutoverStart >= 0, '必须保留配置型首字截止处理分支')
assert.ok(
  neutralCutoverThrow > neutralCutoverStart,
  '配置型首字截止必须直接交回统一候选切换，不得经过请求类型重放门禁'
)
assert.match(routesSource, /firstByteDeadlineMs: effectiveFirstByteDeadlineMs/g, '流式和非流式读取必须共用同一个有效截止')
assert.match(upstreamRequestSource, /firstByteDeadlineTimer = setTimeout/g, '响应头到达前也必须受公共首字截止约束')

console.log('普通路由速度优先首字截止回归通过：仅速度优先文本 lane 启用，墙钟/attempt/lane 裁剪与慢样本边界均正确')
