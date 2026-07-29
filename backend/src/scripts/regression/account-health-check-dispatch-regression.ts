import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountTestResult } from '../../domain/types.js'
import {
  accountUpdateNeedsImmediateHealthCheck,
  dispatchAccountHealthCheck,
  dispatchPendingAccountHealthCheck
} from '../../modules/accounts/account-health-check-dispatch.service.js'
import { accountHealthCheckTriggerPriority } from '../../modules/accounts/account-health-check-trigger.js'
import { runWithBackgroundAccountAvailabilityProbe } from '../../modules/background/account-probe-limits.js'
import { dispatchRequestFailureAccountHealthCheck } from '../../modules/gateway/response/request-failure-health-check.js'

const originalProcessRole = runtimeConfig.processRole
const originalSend = process.send
const originalDateNow = Date.now
const messages: unknown[] = []
let nowMs = originalDateNow()

try {
  runtimeConfig.processRole = 'db-service'
  Date.now = () => nowMs
  process.send = ((message: unknown, ...args: unknown[]) => {
    messages.push(message)
    const callback = args.find((item): item is (error: Error | null) => void => typeof item === 'function')
    callback?.(null)
    return true
  }) as typeof process.send

  assert.equal(dispatchPendingAccountHealthCheck({ id: 'acc_disabled', status: 'disabled' }), false)
  assert.deepEqual(messages, [], '停用账户不应投递后台健康检查')

  assert.equal(dispatchPendingAccountHealthCheck({ id: ' acc_pending ', status: 'pending_test' }), true)
  assert.deepEqual(messages, [{
    type: 'background_worker_account_health_check_trigger',
    accountId: 'acc_pending',
    reason: 'activation'
  }], '待检查账户应只向后台 worker 投递规范化账户 ID')

  assert.equal(dispatchAccountHealthCheck(' acc_request_failed ', 'request_failure'), true)
  assert.deepEqual(messages.at(-1), {
    type: 'background_worker_account_health_check_trigger',
    accountId: 'acc_request_failed',
    reason: 'request_failure'
  }, '真实网关失败应向后台 worker 投递规范化的独立健康检查')

  const requestFailureMessageCount = messages.length
  assert.equal(dispatchAccountHealthCheck('acc_request_failed', 'request_failure'), false, '本地投递端必须在 worker 前执行请求失败冷却')
  assert.equal(messages.length, requestFailureMessageCount, '冷却中的请求失败不得重复写入 worker IPC')
  nowMs += 5 * 60_000 - 1
  assert.equal(dispatchAccountHealthCheck('acc_request_failed', 'request_failure'), false, '请求失败探针在 5 分钟边界前必须继续限流')
  nowMs += 1
  assert.equal(dispatchAccountHealthCheck('acc_request_failed', 'request_failure'), true, '请求失败探针满 5 分钟后必须允许下一轮确认')
  assert.equal(dispatchAccountHealthCheck('acc_request_failed', 'configuration'), true, '请求失败冷却不得阻止更高优先级配置复检')

  const gatewayRequest = {} as Parameters<typeof dispatchRequestFailureAccountHealthCheck>[0]
  assert.equal(dispatchRequestFailureAccountHealthCheck(gatewayRequest, 'manual_account_test', 'acc_manual'), false)
  assert.equal(dispatchRequestFailureAccountHealthCheck(gatewayRequest, 'gateway', ''), false, '未接受的投递不得占用请求去重名额')
  assert.equal(dispatchRequestFailureAccountHealthCheck(gatewayRequest, 'gateway', 'acc_first'), true)
  assert.equal(dispatchRequestFailureAccountHealthCheck(gatewayRequest, 'gateway', 'acc_second'), false, '已接受投递后同一请求不得再触发第二个账户')
  assert.deepEqual(messages.at(-1), {
    type: 'background_worker_account_health_check_trigger',
    accountId: 'acc_first',
    reason: 'request_failure'
  })

  assert.equal(accountUpdateNeedsImmediateHealthCheck({ notes: '仅改备注' }), false)
  assert.equal(accountUpdateNeedsImmediateHealthCheck({ credentials: { api_key: 'sk-updated' } }), true)
  assert.equal(accountUpdateNeedsImmediateHealthCheck({ healthCheckModel: 'gpt-5.5' }), true)
  assert.equal(accountUpdateNeedsImmediateHealthCheck({ healthCheckEndpointMode: 'responses_sse' }), true)
  assert.deepEqual([
    accountHealthCheckTriggerPriority('activation'),
    accountHealthCheckTriggerPriority('configuration'),
    accountHealthCheckTriggerPriority('request_failure'),
    accountHealthCheckTriggerPriority('scheduled')
  ], [0, 10, 15, 20], '首次激活、配置复检、请求失败确认和周期复检必须保持稳定优先级')

  let releaseSharedProbe!: () => void
  const sharedProbeGate = new Promise<void>((resolve) => {
    releaseSharedProbe = resolve
  })
  const sharedObservation = {
    result: {
      accountId: 'acc_singleflight',
      accountName: 'singleflight',
      providerCode: 'openai',
      type: 'api_key',
      success: true,
      message: 'ok'
    } as AccountTestResult
  }
  let sharedProbeExecutions = 0
  const firstSharedProbe = runWithBackgroundAccountAvailabilityProbe('acc_singleflight', async () => {
    sharedProbeExecutions += 1
    await sharedProbeGate
    return sharedObservation
  }, async (_observation, context) => context.joined)
  const secondSharedProbe = runWithBackgroundAccountAvailabilityProbe('acc_singleflight', async () => {
    sharedProbeExecutions += 1
    return sharedObservation
  }, async (_observation, context) => context.joined)
  releaseSharedProbe()
  assert.deepEqual(await Promise.all([firstSharedProbe, secondSharedProbe]), [false, true], '同账户并发消费者必须区分首个执行者与复用者')
  assert.equal(sharedProbeExecutions, 1, '同账户健康与质量消费者并发时只能执行一次上游探针')

  for (const [name, sourcePath] of [
    ['普通账户新增', '../../modules/accounts/accounts.routes.ts'],
    ['账户导入', '../../modules/accounts/account-import-account-creator.ts'],
    ['OpenAI OAuth 新增', '../../modules/openai-oauth/openai-oauth.routes.ts'],
    ['外部账户推送', '../../modules/external-integrations/external-public-account-push.service.ts']
  ] as const) {
    const source = readFileSync(new URL(sourcePath, import.meta.url), 'utf8')
    assert(source.includes('dispatchPendingAccountHealthCheck('), `${name}必须在保存完成后立即投递后台健康检查`)
  }

  const accountRoutesSource = readFileSync(new URL('../../modules/accounts/accounts.routes.ts', import.meta.url), 'utf8')
  const accountManagementPatchSource = readFileSync(new URL('../../storage/account-management-patch.repository.ts', import.meta.url), 'utf8')
  assert(
    accountRoutesSource.includes('dispatchAccountHealthCheck(account.id, account.healthCheckReason)'),
    '账户路由必须投递集中写入层声明的配置复检优先级'
  )
  assert(accountManagementPatchSource.includes("healthCheckReason: healthCheckRequired ? 'configuration' : undefined"), '账户连接配置变更必须声明配置复检优先级')
  const failureDispatchSource = readFileSync(new URL('../../modules/gateway/response/failure-dispatch.ts', import.meta.url), 'utf8')
  const requestFailureDispatchSource = readFileSync(new URL('../../modules/gateway/response/request-failure-health-check.ts', import.meta.url), 'utf8')
  const nonStreamInspectionSource = readFileSync(new URL('../../modules/gateway/response/non-stream-json-inspection.ts', import.meta.url), 'utf8')
  const responseFinalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
  const healthCheckServiceSource = readFileSync(new URL('../../modules/background/account-health-check.service.ts', import.meta.url), 'utf8')
  const probeLimitsSource = readFileSync(new URL('../../modules/background/account-probe-limits.ts', import.meta.url), 'utf8')
  const internalDispatchSource = readFileSync(new URL('../../modules/internal-api/account-health-check-dispatch.service.ts', import.meta.url), 'utf8')
  const runtimeConfigSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
  assert(failureDispatchSource.match(/dispatchRequestFailureAccountHealthCheck\(req, usageContext\.trafficSource, account\.id\)/g)?.length === 3,
    '普通完整 HTTP 失败、retry_next 显式策略和最终 transport failure 都必须投递去重的独立账户可用性确认')
  assert(
    requestFailureDispatchSource.includes("trafficSource !== 'gateway'"),
    '人工测试和后台探针失败不得递归投递请求失败确认'
  )
  assert(healthCheckServiceSource.includes('requestFailureHealthCheckCooldownMs = 5 * 60_000'), '请求失败健康检查必须按账户执行 5 分钟限频')
  assert(healthCheckServiceSource.includes("failureThreshold: effectiveReason === 'request_failure' ? 1 : settings.failureThreshold"), '请求失败后的独立确认失败必须立即阻止继续调度')
  assert(requestFailureDispatchSource.includes('requestFailureHealthCheckDispatched'), '单个真实请求最多只能触发一个账户独立检查')
  assert(nonStreamInspectionSource.includes('dispatchRequestFailureAccountHealthCheck(input.req, input.usageContext.trafficSource, input.account.id)'), '完整 2xx JSON 协议失败必须投递独立账户可用性确认')
  assert(responseFinalizationSource.includes('context.availabilityProbeEligible'), '流式协议失败、缺失终态和读取失败必须按明确资格投递独立账户可用性确认')
  assert(responseFinalizationSource.includes('if (provenTransportFailure)'), '非流式响应正文读取中断必须投递独立账户可用性确认')
  assert(healthCheckServiceSource.includes("replaceExistingOnlyIfHigherPriority: effectiveReason === 'request_failure'"), '请求失败只能升级低优先级周期检查，不得覆盖激活或配置复检')
  assert(healthCheckServiceSource.includes("priorityAtMost: accountHealthCheckTriggerPriority('request_failure')"), '请求失败探针必须能够使用健康检查队列的保留并发槽')
  assert(healthCheckServiceSource.includes("ignoreSchedule: reason !== 'scheduled'"), '主动触发检查入队时必须绕过周期到期门槛')
  assert(healthCheckServiceSource.includes("ignoreSchedule: item.reason !== 'scheduled'"), '主动触发检查执行前必须继续绕过周期到期门槛')
  assert(healthCheckServiceSource.includes('runWithBackgroundAccountAvailabilityProbe'), '周期和即时健康检查必须加入账户级 single-flight')
  assert(probeLimitsSource.includes('backgroundAccountAvailabilityProbesInFlight'), '后台可用性探针必须共享同一账户占用表')
  assert(internalDispatchSource.includes('createAccountHealthCheckDispatchSignature'), 'performance gateway 必须通过 HMAC 内部通道投递到 control')
  assert(internalDispatchSource.includes("runtimeConfig.performanceNodeRole === 'gateway'"), '独立 gateway 节点不得把触发消息留在本进程 IPC 队列')
  assert(internalDispatchSource.includes('rememberAcceptedRequestFailureDispatch'), 'standalone 与 performance 必须在投递端共享请求失败冷却语义')
  assert(runtimeConfigSource.includes("throw new Error(`${name} 在 performance gateway server 模式下必须配置为 control 的 loopback Origin`)"), 'performance gateway 缺少 control 投递地址时必须拒绝启动')

  console.log('账户健康检查即时投递回归通过：所有新增入口统一投递，非健康配置编辑不误触发')
} finally {
  runtimeConfig.processRole = originalProcessRole
  process.send = originalSend
  Date.now = originalDateNow
}
