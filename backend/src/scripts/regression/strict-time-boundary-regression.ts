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
assert.doesNotMatch(statsSource, /new Date\(row\.created_at\)/, '用量统计 PostgreSQL 桶不得按进程时区解析 usage created_at')
assert.match(statsSource, /canonicalUsageStatsRecordCreatedAt\(row\)/, '用量统计 PostgreSQL 读取必须 canonical usage created_at')
assert.match(statsSource, /normalizedUsageStatsSafeCreatedBefore/, '用量统计 supplied safeCreatedBefore 必须严格 canonical 或失败')

const usageStatsTimeBucketsSource = readFileSync(new URL('../../storage/usage-stats-time-buckets.ts', import.meta.url), 'utf8')
assert.doesNotMatch(usageStatsTimeBucketsSource, /new Date\(row\.created_at\)/, '用量统计时间桶不得宽松解析 usage created_at')
assert.match(usageStatsTimeBucketsSource, /usageStatsRecordCreatedAt\(row\)/, '用量统计时间桶必须严格解析 usage created_at')

const usageStatsAccountQualityWriterSource = readFileSync(new URL('../../storage/usage-stats-account-quality-writer.ts', import.meta.url), 'utf8')
assert.doesNotMatch(usageStatsAccountQualityWriterSource, /new Date\(row\.created_at\)/, '账号质量分钟桶不得宽松解析 usage created_at')
assert.match(usageStatsAccountQualityWriterSource, /requiredRfc3339Instant\(updatedAt, '账号质量统计 updatedAt'\)/, '账号质量 updatedAt 必须严格 canonical')

const usageStatsHelpersSource = readFileSync(new URL('../../storage/usage-stats-helpers.ts', import.meta.url), 'utf8')
assert.doesNotMatch(usageStatsHelpersSource, /Date\.parse\(/, '聚合摘要不得宽松解析 last_used_at')
assert.match(usageStatsHelpersSource, /rfc3339InstantMilliseconds\(normalized\)/, '聚合摘要跨 offset 排序必须使用 epoch')

const usageStatsWritersSource = readFileSync(new URL('../../storage/usage-stats-writers.ts', import.meta.url), 'utf8')
assert.match(usageStatsWritersSource, /function compareUsageStatsTimestamp/, '使用统计写入必须以 epoch 比较绝对时间')
assert.match(usageStatsWritersSource, /canonicalUsageStatsRecordCreatedAt\(row\)/, '使用统计写入必须 canonical usage created_at')
const usageStatsWriters = await import('../../storage/usage-stats-writers.js')
assert.throws(
  () => usageStatsWriters.aggregateUsageStatsRecords(undefined as never, [], '2026-08-16T06:34:49.137'),
  /用量统计 updatedAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  '空批次 supplied updatedAt 也不得静默跳过非法裸时间'
)

const usageRecordsSource = readFileSync(new URL('../../storage/usage-records.repository.ts', import.meta.url), 'utf8')
assert.match(usageRecordsSource, /requiredRfc3339Instant\(input\.createdAt, '使用记录 createdAt'\)/, '使用记录 supplied createdAt 必须严格 canonical')
assert.doesNotMatch(usageRecordsSource, /const fallback = nowIso\(\)/, '使用记录分片日期不得把非法 createdAt 静默替换为当前时间')
assert.match(usageRecordsSource, /function compareUsageRecordTimestamp/, '跨 shard 使用记录排序必须按 epoch 比较 created_at')

const usageStatsHelpers = await import('../../storage/usage-stats-helpers.js')
assert.equal(
  usageStatsHelpers.addUsageSummaries(
    { ...usageStatsHelpers.emptyAccountUsageSummary(), lastUsedAt: '2026-08-16T06:34:49.137+08:00' },
    { ...usageStatsHelpers.emptyAccountUsageSummary(), lastUsedAt: '2026-08-15T21:34:49.137Z' }
  ).lastUsedAt,
  instant,
  '聚合摘要 lastUsedAt 必须按 epoch 选择较晚瞬间并 canonical UTC'
)
assert.throws(
  () => usageStatsHelpers.addUsageSummaries(
    { ...usageStatsHelpers.emptyAccountUsageSummary(), lastUsedAt: '2026-08-16T06:34:49.137' },
    usageStatsHelpers.emptyAccountUsageSummary()
  ),
  /统计聚合 last_used_at必须是带 Z 或数值 offset 的 RFC3339 时间/,
  '聚合摘要不得接受裸 lastUsedAt'
)

const resourceAuthorizationListHelpersSource = readFileSync(new URL('../../storage/resource-authorization-list-helpers.ts', import.meta.url), 'utf8')
assert.doesNotMatch(resourceAuthorizationListHelpersSource, /Date\.parse\(/, '资源授权列表不得宽松解析 createdAt')
assert.match(resourceAuthorizationListHelpersSource, /requiredRfc3339Instant\(left\.createdAt, '授权 createdAt'\)/, '资源授权列表 createdAt 必须严格解析')
assert.match(resourceAuthorizationListHelpersSource, /rfc3339InstantMilliseconds\(/, '资源授权列表排序必须按 epoch 比较')
const resourceAuthorizationListHelpers = await import('../../storage/resource-authorization-list-helpers.js')
const resourceAuthorizationSummary = (id: string, createdAt: string) => ({ id, createdAt } as never)
assert.equal(
  resourceAuthorizationListHelpers.compareResourceAuthorizationOperations(
    resourceAuthorizationSummary('older', '2026-08-15T21:34:49.137Z'),
    resourceAuthorizationSummary('newer', '2026-08-16T05:34:49.137+08:00')
  ),
  -1,
  '资源授权列表 createdAt 的 Z 与 numeric offset 等价时必须先按同一 epoch 比较，再按 id 稳定排序'
)
for (const value of ['2026-08-16T06:34:49.137', 'not-a-time']) {
  assert.throws(
    () => resourceAuthorizationListHelpers.compareResourceAuthorizationOperations(
      resourceAuthorizationSummary('invalid', value),
      resourceAuthorizationSummary('valid', instant)
    ),
    /RFC3339/,
    `资源授权列表 createdAt 必须拒绝裸时间或非法时间：${value}`
  )
}

const resourceAuthorizationUsageSource = readFileSync(new URL('../../storage/resource-authorization-usage.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(resourceAuthorizationUsageSource, /Date\.parse\(/, '资源授权使用排序不得宽松解析 lastUsedAt')
assert.match(resourceAuthorizationUsageSource, /value === undefined \|\| value === null\) return 0/, '资源授权使用排序必须保留缺失 lastUsedAt 的可选语义')
assert.match(resourceAuthorizationUsageSource, /requiredRfc3339Instant\(value, '授权使用 lastUsedAt'\)/, '资源授权使用 lastUsedAt 必须严格解析')
assert.match(resourceAuthorizationUsageSource, /rfc3339InstantMilliseconds\(/, '资源授权使用排序必须按 epoch 比较')

const resourceAuthorizationWriteStateSource = readFileSync(new URL('../../storage/resource-authorization-write-state.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(resourceAuthorizationWriteStateSource, /Date\.parse\(/, '资源授权写状态不得宽松解析 now')
assert.match(resourceAuthorizationWriteStateSource, /requiredRfc3339Instant\(now, '授权当前时间'\)/, '资源授权写状态 now 必须严格解析并 canonical')
assert.match(resourceAuthorizationWriteStateSource, /rfc3339InstantMilliseconds\(/, '资源授权写状态 now 判断必须按 epoch 比较')

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

const systemMetricsRepositorySource = readFileSync(new URL('../../storage/system-metrics.repository.ts', import.meta.url), 'utf8')
assert.match(
  systemMetricsRepositorySource,
  /function normalizedSampledAt\(value: string \| undefined, label: string\): string \{[\s\S]*?value === undefined \? nowIso\(\) : requiredRfc3339Instant\(value, label\)/,
  '系统指标 sampledAt 只有在字段缺失时才能生成当前时间，supplied 值必须严格 canonical'
)
assert.doesNotMatch(
  systemMetricsRepositorySource,
  /const sampledAt = input\.sampledAt \?\? nowIso\(\)/,
  '系统指标不得把 supplied sampledAt 原样写入数据库'
)
assert.match(
  systemMetricsRepositorySource,
  /sampledAt: requiredRfc3339Instant\(row\.sampled_at,/,
  '系统指标数据库 sampled_at 读取必须严格 canonical，非法值不得原样输出'
)
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

const quotaSnapshotSource = readFileSync(new URL('../../modules/gateway/quota/quota-snapshot-cache.service.ts', import.meta.url), 'utf8')
const quotaReplaceSource = quotaSnapshotSource.match(
  /export function replaceGatewayQuotaSnapshot\([\s\S]*?\n}\n\nexport function clearGatewayQuotaSnapshot/
)
assert.ok(quotaReplaceSource, '网关额度快照本地替换边界必须存在')
assert.match(
  quotaReplaceSource?.[0] ?? '',
  /requiredRfc3339Instant\(snapshot\.generatedAt, '网关额度快照 generatedAt'\)/,
  '本地额度快照 generatedAt 必须严格 canonical，不能因 cache driver 分支被跳过'
)
const quotaSharedReadSource = quotaSnapshotSource.match(
  /async function readSharedGatewayQuotaSnapshot\([\s\S]*?\n}\n\nfunction runtimeState/
)
assert.ok(quotaSharedReadSource, 'Redis 额度快照读取边界必须存在')
assert.doesNotMatch(
  quotaSharedReadSource?.[0] ?? '',
  /\.catch\(\(error\)/,
  'Redis 额度快照严格时间解析错误不得被通用 fallback catch 静默吞掉'
)
assert.match(
  quotaSnapshotSource,
  /if \(snapshot === undefined\)/,
  'Redis 额度快照只有真正缺失时才可清空，缺失 generatedAt 的对象必须失败'
)

const latencyDegradationSource = readFileSync(new URL('../../modules/gateway/runtime/normal-route-latency-degradation.service.ts', import.meta.url), 'utf8')
const latencyGenerationLoadSource = latencyDegradationSource.match(
  /async function loadLatencyGenerationEvent\([\s\S]*?\n}\n\nasync function loadOrCreateLatencyGenerationEvent/
)
assert.ok(latencyGenerationLoadSource, '普通路由速度优先 generation 读取边界必须存在')
assert.match(
  latencyGenerationLoadSource?.[0] ?? '',
  /normalizeLatencyGenerationEvent\(event\)/,
  'runtime-state generation publishedAt 读取后必须 canonical UTC'
)
assert.match(
  latencyGenerationLoadSource?.[0] ?? '',
  /compareSetJson\(/,
  'runtime-state generation 非 canonical 值必须通过 CAS 修正，不能继续传播 offset 原文'
)
assert.doesNotMatch(latencyGenerationLoadSource?.[0] ?? '', /Date\.parse\(/, 'generation publishedAt 不得使用宽松 Date.parse')

const auditF3QuerySource = readFileSync(new URL('../../storage/audit-log-f3-query.repository.ts', import.meta.url), 'utf8')
assert.doesNotMatch(auditF3QuerySource, /Date\.parse\(options\.(?:startAt|endAt)/, 'F3 审计查询 supplied 时间不得按本机时区解析')
assert.doesNotMatch(auditF3QuerySource, /Date\.parse\(row\.createdAt/, 'F3 热搜索记录 createdAt 不得按本机时区解析')
assert.match(auditF3QuerySource, /function optionalF3Timestamp\([\s\S]*?requiredRfc3339Instant/, 'F3 审计列表时间筛选必须严格 canonical')
assert.match(auditF3QuerySource, /f3TimestampMilliseconds\(row\.createdAt, 'F3 热搜索 createdAt'\)/, 'F3 热搜索记录时间必须严格解析')
assert.match(auditF3QuerySource, /const f3AbsoluteTimestampColumns = new Set/, 'F3 DB 时间字段读取必须 canonical UTC')

const chatAssetCleanupSource = readFileSync(new URL('../../modules/chat/chat-asset-cleanup.ts', import.meta.url), 'utf8')
assert.doesNotMatch(chatAssetCleanupSource, /Date\.parse\(/, '聊天资产后台清理不得按本机时区解析 now')
assert.match(chatAssetCleanupSource, /requiredRfc3339Instant\(input\.now, '聊天资产清理 now'\)/, '聊天资产后台清理 now 必须严格 canonical')

const chatRoutesSource = readFileSync(new URL('../../modules/chat/chat.routes.ts', import.meta.url), 'utf8')
const chatAssetDeleteRouteStart = chatRoutesSource.indexOf("chatRouter.delete('/conversations/:conversationId/assets/:assetId'")
const chatAssetDeleteRouteEnd = chatRoutesSource.indexOf("\nchatRouter.get('/conversations/:conversationId/models'", chatAssetDeleteRouteStart)
assert.ok(chatAssetDeleteRouteStart >= 0 && chatAssetDeleteRouteEnd > chatAssetDeleteRouteStart, '必须能定位聊天资产删除路由')
const chatAssetDeleteRouteSource = chatRoutesSource.slice(chatAssetDeleteRouteStart, chatAssetDeleteRouteEnd)
assert.doesNotMatch(chatAssetDeleteRouteSource, /Date\.parse\(now\)/, '聊天资产删除路由不得宽松重解析内部 now')
assert.match(chatAssetDeleteRouteSource, /const nowMs = Date\.now\(\)[\s\S]*retryAt: new Date\(nowMs \+ 60_000\)\.toISOString\(\)/, '聊天资产删除 retryAt 必须基于同一 epoch 生成')

const workerSchedulerSource = readFileSync(new URL('../../modules/background/worker-scheduler.ts', import.meta.url), 'utf8')
assert.doesNotMatch(workerSchedulerSource, /Date\.parse\(/, 'worker scheduler 不得按本机时区解析内部绝对时间')
assert.match(workerSchedulerSource, /rfc3339InstantMilliseconds\(value\)/, 'worker scheduler 状态时间必须严格解析')

const balanceCleanupSource = readFileSync(new URL('../../modules/accounts/account-balance-snapshot-cleanup.service.ts', import.meta.url), 'utf8')
assert.doesNotMatch(balanceCleanupSource, /Date\.parse\(/, '余额快照清理不得按本机时区解析快照时间')
assert.match(balanceCleanupSource, /function normalizeSuppressionRead\(/, '余额快照读取代次必须先 canonical')
assert.match(balanceCleanupSource, /余额快照配置 nextRefreshAt/, '余额快照配置 nextRefreshAt 必须拒绝裸时间')
assert.match(balanceCleanupSource, /余额快照 nextRefreshAfter/, '余额快照 nextRefreshAfter 必须拒绝裸时间')

const { runtimeConfig } = await import('../../config/runtime.js')
const { createRuntimeStateStore } = await import('../../shared/runtime-state-store.js')
const quotaSnapshot = await import('../../modules/gateway/quota/quota-snapshot-cache.service.js')
const previousCacheDriver = runtimeConfig.cacheDriver
const previousRuntimeStateDriver = runtimeConfig.runtimeStateDriver
try {
  runtimeConfig.cacheDriver = 'memory'
  quotaSnapshot.clearGatewayQuotaSnapshot()
  quotaSnapshot.replaceGatewayQuotaSnapshot({
    generatedAt: '2026-08-16T06:34:49.137+08:00',
    costEntries: [],
    authorizationEntries: []
  })
  assert.equal(
    quotaSnapshot.gatewayQuotaSnapshotRuntime().generatedAt,
    instant,
    '本地网关额度快照 generatedAt 必须 canonical 到 UTC'
  )
  assert.throws(
    () => quotaSnapshot.replaceGatewayQuotaSnapshot({
      generatedAt: '2026-08-16T06:34:49.137',
      costEntries: [],
      authorizationEntries: []
    }),
    /网关额度快照 generatedAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
    '本地网关额度快照必须拒绝裸 generatedAt'
  )

  if (previousRuntimeStateDriver === 'memory') {
    runtimeConfig.runtimeStateDriver = 'memory'
    const latency = await import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js')
    const latencyStore = createRuntimeStateStore('gateway-normal-route-latency-degradation')
    await latencyStore.setJson('v1:generation', {
      version: 'strict-preexisting-offset',
      publishedAt: '2026-08-16T07:34:49.137+09:00'
    }, 60_000)
    await latency.clearAllNormalRouteLatencyDegradationAsync({
      version: 'strict-preexisting-offset',
      publishedAt: instant
    })
    assert.deepEqual(
      await latencyStore.getJson('v1:generation'),
      { version: 'strict-preexisting-offset', publishedAt: instant },
      '已有 offset generation marker 读取后必须通过 CAS canonical 为 UTC'
    )
  }
} finally {
  quotaSnapshot.clearGatewayQuotaSnapshot()
  runtimeConfig.cacheDriver = previousCacheDriver
  runtimeConfig.runtimeStateDriver = previousRuntimeStateDriver
}

console.log('严格时间边界回归通过：RFC3339、Grok、Redis 进程指标、runtime observedAt、队列/epoch/游标失败均可见')
