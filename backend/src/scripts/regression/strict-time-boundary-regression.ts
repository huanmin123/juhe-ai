import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { canonicalizeRfc3339Instant, requiredRfc3339Instant } from '../../shared/rfc3339.js'

const instant = '2026-08-15T22:34:49.137Z'
assert.equal(canonicalizeRfc3339Instant(instant), instant)
assert.equal(canonicalizeRfc3339Instant('2026-08-16T06:34:49.137+08:00'), instant)
for (const value of ['2026-08-16T06:34:49.137', '2026-08-16 06:34:49.137', 'not-a-time']) {
  assert.equal(canonicalizeRfc3339Instant(value), undefined, `必须拒绝裸时间或非法时间：${value}`)
}
assert.throws(() => requiredRfc3339Instant('not-a-time'), /RFC3339/)
assert.equal(
  requiredRfc3339Instant('2026-08-16T06:34:49.137+08:00', 'account side effect observedAt'),
  instant,
  'account side effect observedAt 必须 canonical 到 UTC'
)
for (const value of [undefined, '2026-08-16T06:34:49.137', 'not-a-time']) {
  assert.throws(
    () => requiredRfc3339Instant(value, 'account side effect observedAt'),
    /account side effect observedAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
    `account side effect observedAt 缺失、裸时间或非法时间必须可见失败：${String(value)}`
  )
}

const revisionTimeHelpers = [
  ['../../storage/resource-authorization-return.repository.ts', 'nextResourceAuthorizationReturnVersion', '授权归还 updatedAt'],
  ['../../storage/api-key.repository.ts', 'nextApiKeyRevision', 'API Key revision'],
  ['../../storage/announcement-management-write.repository.ts', 'nextAnnouncementRevision', '公告 revision'],
  ['../../storage/external-integration-source-write-helpers.ts', 'nextExternalIntegrationUpdatedAt', '外部集成来源 updatedAt'],
  ['../../storage/provider-model-catalog.repository.ts', 'nextProviderModelUpdatedAt', '供应商模型 updatedAt'],
  ['../../storage/custom-provider-models.repository.ts', 'nextUpdatedAt', '自定义供应商模型 updatedAt'],
  ['../../storage/system-team.repository.ts', 'nextSystemTeamUpdatedAt', '系统团队 updatedAt'],
  ['../../storage/response-inspection-policy.repository.ts', 'nextPolicyUpdatedAt', '响应检查策略 updatedAt']
] as const
for (const [path, functionName, label] of revisionTimeHelpers) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const helper = source.match(new RegExp(`function ${functionName}\\([\\s\\S]*?\\n}`))?.[0]
  assert.ok(helper, `${label} revision helper 必须存在`)
  assert.match(helper, /rfc3339InstantMilliseconds\(/, `${label} 必须严格解析 supplied/persisted timestamp`)
  assert.match(helper, /=== undefined\) throw new Error/, `${label} 裸时间或非法时间必须 fail closed`)
  const canonicalOutputHelper = helper.includes('toISOString()')
    ? helper
    : source.match(/function apiKeyRevisionFromTimestamp\([\s\S]*?\n}/)?.[0]
  assert.match(canonicalOutputHelper ?? '', /toISOString\(\)/, `${label} 递增结果必须 canonical 为 UTC`)
  assert.doesNotMatch(helper, /Date\.parse\(|Number\.isFinite\(/, `${label} 不得回退到宽松时间解析`)
}

const externalIntegrationHelpers = await import('../../storage/external-integration-source-write-helpers.js')
assert.equal(
  externalIntegrationHelpers.nextExternalIntegrationUpdatedAt('2099-08-16T06:34:49.137+08:00'),
  '2099-08-15T22:34:49.138Z',
  '外部集成来源 revision helper 必须把 numeric offset canonical 为 UTC'
)
for (const value of ['2026-08-16T06:34:49.137', 'not-a-time']) {
  assert.throws(
    () => externalIntegrationHelpers.nextExternalIntegrationUpdatedAt(value),
    /外部集成来源 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间/,
    `外部集成来源 revision helper 必须拒绝裸时间或非法时间：${value}`
  )
}

const queueSource = readFileSync(new URL('../../modules/gateway/usage/record-queue.service.ts', import.meta.url), 'utf8')
assert.doesNotMatch(queueSource, /Date\.parse\(/, 'usage queue 不得使用宽松 Date.parse')
assert.match(queueSource, /value === undefined \? nowIso\(\) : requiredRfc3339Instant/, 'usage createdAt 只能对缺失值生成 now')
assert.match(queueSource, /requiredRfc3339Instant\(item\.input\.createdAt/, '队列 oldest createdAt 必须校验 canonical 输入')

const sideEffectSource = readFileSync(new URL('../../modules/gateway/runtime/account-side-effect-queue.ts', import.meta.url), 'utf8')
assert.doesNotMatch(sideEffectSource, /Date\.parse\(/, 'side effect epoch 不得使用宽松 Date.parse')
assert.match(sideEffectSource, /requiredRfc3339Instant\(observation\.observedAt/, 'side effect observedAt 必须严格解析')

const sideEffectServiceSource = readFileSync(new URL('../../modules/gateway/runtime/account-side-effects.service.ts', import.meta.url), 'utf8')
const normalizedSideEffectOperationSource = sideEffectServiceSource.match(
  /function normalizedObservedAccountSideEffectOperation\([\s\S]*?\n}\n\nexport function degradeGatewayAccountForRuntimeFailure/
)?.[0]
assert.ok(normalizedSideEffectOperationSource, 'account side effect observedAt 规范化边界必须存在')
assert.match(
  normalizedSideEffectOperationSource,
  /requiredRfc3339Instant\(operation\.input\.observedAt, 'account side effect observedAt'\)/,
  'account side effect service 必须在 epoch registry 前严格校验 supplied observedAt'
)
assert.doesNotMatch(
  normalizedSideEffectOperationSource,
  /Date\.parse\(|new Date\(\)\.toISOString\(\)/,
  'account side effect service 不得将缺失、裸时间或非法 observedAt 静默替换为 now'
)
const accountSideEffects = await import('../../modules/gateway/runtime/account-side-effects.service.js')
for (const observedAt of [undefined, '2026-08-16T06:34:49.137', 'not-a-time']) {
  await assert.rejects(
    accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect({
      type: 'apply_account_error_handling',
      account: { dispatchRevision: 1 },
      input: { success: true, observedAt }
    } as Parameters<typeof accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect>[0]),
    /account side effect observedAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
    `account side effect service 必须拒绝无效 observedAt：${String(observedAt)}`
  )
}

const modelHealthSource = readFileSync(new URL('../../storage/model-quality-health.repository.ts', import.meta.url), 'utf8')
assert.match(modelHealthSource, /return requiredRfc3339Instant\(value, '模型质量健康 observedAt'\)/, '模型质量 observedAt 非法时必须失败')

const rangeSource = readFileSync(new URL('../../storage/usage-range-window-requests.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(rangeSource, /Date\.parse\(/, 'usage range requestedAt 不得使用宽松 Date.parse')
assert.match(rangeSource, /requiredRfc3339Instant\(requestedAt, '用量范围窗口请求 requestedAt'\)/, 'requestedAt 必须 canonical/throw')

const statsSource = readFileSync(new URL('../../storage/usage-stats.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(statsSource, /const parsedPreviousBoundaryMs = Date\.parse/, 'cursor invalid 不得回退 now-1h')
assert.match(statsSource, /expiry cursor_created_at 必须是带 Z 或数值 offset 的 RFC3339 时间/, 'cursor invalid 必须保留可见错误')
assert.match(statsSource, /cursor_created_at 必须是带 Z 或数值 offset 的 RFC3339 时间：\$\{cursorCreatedAt\}/, 'lag cursor invalid 必须失败而非 lag=0')

const tableRouteSource = readFileSync(new URL('../../modules/table-monitor/table-monitor.routes.ts', import.meta.url), 'utf8')
assert.match(tableRouteSource, /cutoffAt: absoluteDateTimeQuerySchema/, 'cleanup cutoff 必须复用严格 schema')
assert.match(tableRouteSource, /nonBusinessDataCleanupSchema\.safeParse/, 'cleanup 非法时间必须走 400 safeParse contract')

const accountTestTasksSource = readFileSync(new URL('../../storage/account-test-tasks.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(accountTestTasksSource, /Date\.parse\(/, '账号测试任务回执不得用宽松时间解析数据库值')
assert.match(accountTestTasksSource, /return requiredRfc3339Instant\(value, column\)/, '账号测试任务字符串时间必须严格 canonical 或失败')

const authorizedDispatchSource = readFileSync(new URL('../../storage/account-authorized-dispatch.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(authorizedDispatchSource, /Date\.parse\(/, '授权账户调度不得按进程时区解释到期或冷却时间')
assert.match(authorizedDispatchSource, /rfc3339InstantMilliseconds\(value\)/, '授权账户调度必须严格解析到期与冷却时间')
assert.match(authorizedDispatchSource, /授权账户到期时间必须是带 Z 或数值 offset 的 RFC3339 时间/, '授权到期裸时间必须可见失败')
assert.match(authorizedDispatchSource, /授权账户冷却时间必须是带 Z 或数值 offset 的 RFC3339 时间/, '授权冷却裸时间必须可见失败')

const apiKeyRuntimeStateSource = readFileSync(new URL('../../storage/account-api-key-runtime-state.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(apiKeyRuntimeStateSource, /Date\.parse\(/, 'API Key 运行态不得用宽松时间解析租约或 fence')
assert.match(apiKeyRuntimeStateSource, /requiredRfc3339Timestamp/, 'API Key 运行态租约必须基于严格 RFC3339 epoch')
assert.match(apiKeyRuntimeStateSource, /canonicalizeRfc3339Instant\(value\)/, 'API Key probe fence 必须 canonical 数值 offset 时间')
assert.match(apiKeyRuntimeStateSource, /value === undefined\) return normalizedFallback/, 'API Key observedAt 只有缺失时才能使用 fallback')
assert.match(apiKeyRuntimeStateSource, /input\.cooldownUntil === undefined/, 'API Key supplied cooldownUntil 必须先区分缺失和空串')
assert.match(apiKeyRuntimeStateSource, /requiredRfc3339Instant\(input\.cooldownUntil, 'cooldownUntil'\)/, 'API Key cooldownUntil 必须严格 canonical')

const openAICompatibleFileRouteSource = readFileSync(new URL('../../modules/openai-compatible-files/files.routes.ts', import.meta.url), 'utf8')
assert.doesNotMatch(openAICompatibleFileRouteSource, /function openAITimestamp\([\s\S]*?Date\.parse\(/, 'OpenAI 兼容文件输出不得按本机时区解析时间')
assert.match(openAICompatibleFileRouteSource, /rfc3339InstantMilliseconds\(value\)/, 'OpenAI 兼容文件输出必须严格解析存储时间')
assert.match(openAICompatibleFileRouteSource, /OpenAI 兼容文件时间必须是带 Z 或数值 offset 的 RFC3339 时间/, 'OpenAI 兼容文件非法时间不得伪造当前秒')

const openAICompatibleVectorStoreRouteSource = readFileSync(new URL('../../modules/openai-compatible-vector-stores/vector-stores.routes.ts', import.meta.url), 'utf8')
assert.doesNotMatch(openAICompatibleVectorStoreRouteSource, /function openAITimestamp\([\s\S]*?Date\.parse\(/, 'OpenAI 兼容向量库输出不得按本机时区解析时间')
assert.match(openAICompatibleVectorStoreRouteSource, /rfc3339InstantMilliseconds\(value\)/, 'OpenAI 兼容向量库输出必须严格解析存储时间')
assert.match(openAICompatibleVectorStoreRouteSource, /OpenAI 兼容向量库时间必须是带 Z 或数值 offset 的 RFC3339 时间/, 'OpenAI 兼容向量库非法时间不得伪造当前秒')

const clientIpPolicyCacheSource = readFileSync(new URL('../../modules/gateway/runtime/client-ip-policy-cache.service.ts', import.meta.url), 'utf8')
assert.doesNotMatch(clientIpPolicyCacheSource, /Date\.parse\(/, 'Client-IP 策略缓存不得按本机时区解析 expiresAt')
assert.match(clientIpPolicyCacheSource, /policy\.expiresAt === undefined/, 'Client-IP 策略只有 expiresAt 缺失时才表示永久')
assert.match(clientIpPolicyCacheSource, /Client-IP 策略 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间/, 'Client-IP supplied invalid expiresAt 必须可见失败')

const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
assert.doesNotMatch(preflightSource, /Date\.parse\(/, '可恢复账户冷却等待不得按本机时区解析 cooldownUntil')
assert.match(preflightSource, /可恢复账户 cooldownUntil 必须是带 Z 或数值 offset 的 RFC3339 时间/, '可恢复账户 supplied invalid cooldownUntil 必须可见失败')

const runtimeSideEffectsSource = readFileSync(new URL('../../modules/gateway/runtime/account-side-effects.service.ts', import.meta.url), 'utf8')
assert.doesNotMatch(runtimeSideEffectsSource, /Date\.parse\(/, '账户运行态 probe 展示不得按本机时区解析 nextAttemptAt')
assert.match(runtimeSideEffectsSource, /账户运行态 probe nextAttemptAt 必须是带 Z 或数值 offset 的 RFC3339 时间/, 'runtime-state supplied invalid nextAttemptAt 必须可见失败')

const anthropicRouteHelpers = await import('../../modules/gateway/protocols/anthropic-v1/route-helpers.js')
assert.equal(
  anthropicRouteHelpers.buildAnthropicModelsResponse([{ model: 'strict-release-date', releaseDate: '2026-08-16' } as never]).data[0]?.created_at,
  '2026-08-16T00:00:00.000Z',
  '模型 releaseDate 是日期业务字段，转换到协议绝对时间时必须明确使用 UTC 日界线'
)
assert.throws(
  () => anthropicRouteHelpers.buildAnthropicModelsResponse([{ model: 'invalid-release-date', releaseDate: '2026-08-16T12:00:00' } as never]),
  /Anthropic 模型 releaseDate 必须是 YYYY-MM-DD 日期/,
  '模型 releaseDate 不得以裸绝对时间混入协议输出'
)
assert.throws(
  () => anthropicRouteHelpers.buildAnthropicModelsResponse([{ model: 'invalid-created-at', createdAt: '2026-08-16T12:00:00' } as never]),
  /Anthropic 模型 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  '模型 createdAt 裸时间必须可见失败'
)

const grokOAuthSource = readFileSync(new URL('../../modules/grok-oauth/grok-oauth.routes.ts', import.meta.url), 'utf8')
const grokSsoExpirySource = grokOAuthSource.match(
  /function grokSSOImportAccountExpiresAt\([\s\S]*?\n}\n\nasync function mapWithConcurrency/
)?.[0]
assert.ok(grokSsoExpirySource, 'Grok SSO accountExpiresAt 归一化边界必须存在')
assert.doesNotMatch(grokSsoExpirySource, /Date\.parse\(/, 'Grok SSO accountExpiresAt 不得使用宽松 Date.parse')
assert.match(grokSsoExpirySource, /requested === undefined \|\| requested === null/, '只有缺失或 null 的 Grok accountExpiresAt 可以保留默认语义')
assert.match(grokSsoExpirySource, /requiredRfc3339Instant\(requested, 'Grok OAuth accountExpiresAt'\)/, 'Grok supplied accountExpiresAt 必须严格 canonical')
assert.match(grokSsoExpirySource, /requiredRfc3339Instant\(tokenInfo\.expiresAt, 'Grok OAuth token expiresAt'\)/, 'Grok token expiresAt 必须严格 canonical')

const processMetricsRegistrySource = readFileSync(new URL('../../shared/performance-process-metrics-registry.ts', import.meta.url), 'utf8')
assert.doesNotMatch(processMetricsRegistrySource, /Date\.parse\(/, 'Redis 进程指标采样不得使用宽松 Date.parse')
assert.match(processMetricsRegistrySource, /requiredRfc3339Instant\(sample\.sampledAt, '高性能进程指标采样时间'\)/, 'Redis 写入必须拒绝非法 sampledAt')
assert.match(processMetricsRegistrySource, /JSON\.stringify\(\{ \.\.\.sample, sampledAt \}\)/, 'Redis 写入必须保存 canonical UTC sampledAt')
assert.match(processMetricsRegistrySource, /canonicalizeRfc3339Instant\(parsed\.sampledAt\)/, 'Redis 读取必须拒绝裸/非法 sampledAt')
const processMetricsRegistry = await import('../../shared/performance-process-metrics-registry.js')
let publishedRegistryArguments: string[] | undefined
const registryClient = {
  eval: async (_script: string, payload: { arguments: string[] }) => {
    publishedRegistryArguments = payload.arguments
    return undefined
  }
}
await processMetricsRegistry.writePerformanceProcessMetricsRegistrySample(registryClient as never, 'strict-time-boundary', {
  processRole: 'server',
  processPid: 1,
  sampledAt: '2026-08-16T06:34:49.137+08:00'
})
assert.equal(
  JSON.parse(publishedRegistryArguments?.[0] ?? '{}').sampledAt,
  instant,
  'Redis 进程指标写入必须把 numeric offset 规范为 UTC Z'
)
await assert.rejects(
  processMetricsRegistry.writePerformanceProcessMetricsRegistrySample(registryClient as never, 'strict-time-boundary', {
    processRole: 'server',
    processPid: 1,
    sampledAt: '2026-08-16T06:34:49.137'
  }),
  /高性能进程指标采样时间必须是带 Z 或数值 offset 的 RFC3339 时间/,
  'Redis 进程指标写入必须拒绝裸 sampledAt'
)

const statsRouteSource = readFileSync(new URL('../../modules/stats/stats.routes.ts', import.meta.url), 'utf8')
const runtimeSummarySource = statsRouteSource.match(
  /statsRouter\.get\('\/system-metrics\/runtime\/summary'[\s\S]*?\n}\)\n\nstatsRouter\.get\('\/system-metrics\/runtime\/jobs'/
)?.[0]
assert.ok(runtimeSummarySource, '系统指标 runtime summary 边界必须存在')
assert.doesNotMatch(runtimeSummarySource, /Date\.parse\(/, 'runtime observedAt 不得使用宽松 Date.parse')
assert.match(runtimeSummarySource, /runtime\?\.observedAt !== undefined/, 'runtime observedAt 缺失时必须保留已有 optional 语义')
assert.match(runtimeSummarySource, /rfc3339InstantMilliseconds\(runtime\.observedAt\)/, 'runtime supplied observedAt 必须严格解析')
assert.match(runtimeSummarySource, /系统指标运行时快照 observedAt必须是带 Z 或数值 offset 的 RFC3339 时间/, 'runtime 非法 observedAt 必须可见失败')

console.log('严格时间边界回归通过：RFC3339、Grok、Redis 进程指标、runtime observedAt、队列/epoch/游标失败均可见')
