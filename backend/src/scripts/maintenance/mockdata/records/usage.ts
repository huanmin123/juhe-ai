import type { AccountSummary, GroupSummary, SystemAccountSummary } from '../../../../domain/types.js'
import * as repositories from '../../../../storage/repositories.js'
import {
  chunks,
  dayMs,
  idPrefix,
  minuteMs,
  providerCode,
  pseudoRandom,
  roundCost,
  tracePrefix,
  weightedIndex,
  type ApiKeyWithSecret,
  type CreatedMockdata,
  type MockdataOptions,
  type UsageRecordSeed
} from '../shared.js'
import { authorizationInstanceAccount } from '../core/account-helpers.js'

interface KeyScenario {
  key: ApiKeyWithSecret
  owner: SystemAccountSummary
  group: GroupSummary
  accounts: AccountSummary[]
  label: string
  clientIpBase: string
  trafficSource?: UsageRecordSeed['trafficSource']
}

const modelPrices: Record<string, { input: number; output: number; cached: number }> = {
  'gpt-5.4-mini': { input: 0.15, output: 0.6, cached: 0.04 },
  'gpt-5.4': { input: 1.25, output: 10, cached: 0.3 },
  'gpt-4.1-mini': { input: 0.4, output: 1.6, cached: 0.1 },
  'gpt-4o-mini': { input: 0.15, output: 0.6, cached: 0.04 },
  'gpt-image-1': { input: 5, output: 40, cached: 1 },
  'mockdata-global-image': { input: 5, output: 40, cached: 1 },
  'mockdata-global-long-context': { input: 0.2, output: 0.8, cached: 0.05 }
}

export function createUsageMockdata(created: CreatedMockdata, options: MockdataOptions): UsageRecordSeed[] {
  const devPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.dev)
  const devBurstFastInstance = authorizationInstanceAccount(created.accounts.burstFast, created.users.dev)
  const testerPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.tester)
  const opsPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.ops)
  const opsProxiedInstance = authorizationInstanceAccount(created.accounts.proxied, created.users.ops)
  const opsBurstFallbackInstance = authorizationInstanceAccount(created.accounts.burstFallback, created.users.ops)
  const financeOauthInstance = authorizationInstanceAccount(created.accounts.oauth, created.users.finance)
  const viewerBurstImageInstance = authorizationInstanceAccount(created.accounts.burstImage, created.users.viewer)
  const viewerBurstFallbackInstance = authorizationInstanceAccount(created.accounts.burstFallback, created.users.viewer)
  const scenarios: KeyScenario[] = [
    {
      key: created.apiKeys.adminMain,
      owner: created.users.admin,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.proxied, created.accounts.normal],
      label: 'admin-main',
      clientIpBase: '10.10.1.'
    },
    {
      key: created.apiKeys.adminRoundRobin,
      owner: created.users.admin,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.standardClient, created.accounts.multiKeyPool],
      label: 'admin-round-robin',
      clientIpBase: '10.10.5.'
    },
    {
      key: created.apiKeys.adminWeighted,
      owner: created.users.admin,
      group: created.groups.highConcurrency,
      accounts: [created.accounts.burstFast, created.accounts.burstImage, created.accounts.burstFallback],
      label: 'admin-weighted-route',
      clientIpBase: '10.10.6.'
    },
    {
      key: created.apiKeys.adminScheduled,
      owner: created.users.admin,
      group: created.groups.experiment,
      accounts: [created.accounts.scheduledInactive],
      label: 'admin-scheduled-key',
      clientIpBase: '10.10.12.'
    },
    {
      key: created.apiKeys.adminHighFrequency,
      owner: created.users.admin,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.normal],
      label: 'admin-high',
      clientIpBase: '10.10.2.'
    },
    {
      key: created.apiKeys.adminHighConcurrency,
      owner: created.users.admin,
      group: created.groups.highConcurrency,
      accounts: [created.accounts.burstFast, created.accounts.burstImage, created.accounts.burstFallback],
      label: 'admin-high-concurrency',
      clientIpBase: '10.10.7.'
    },
    {
      key: created.apiKeys.adminBackup,
      owner: created.users.admin,
      group: created.groups.backup,
      accounts: [created.accounts.fallback, created.accounts.rateLimited],
      label: 'admin-backup',
      clientIpBase: '10.10.3.'
    },
    {
      key: created.apiKeys.adminOAuth,
      owner: created.users.admin,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'admin-oauth',
      clientIpBase: '10.10.4.'
    },
    {
      key: created.apiKeys.adminAuthorizedGroups,
      owner: created.users.admin,
      group: created.groups.adminGrantedDev,
      accounts: [created.accounts.devShared],
      label: 'admin-authorized-dev-group',
      clientIpBase: '10.10.8.'
    },
    {
      key: created.apiKeys.adminMain,
      owner: created.users.admin,
      group: created.groups.experiment,
      accounts: [created.accounts.image],
      label: 'admin-image-generation',
      clientIpBase: '10.10.9.'
    },
    {
      key: created.apiKeys.adminDisabled,
      owner: created.users.admin,
      group: created.groups.experiment,
      accounts: [created.accounts.disabled],
      label: 'admin-disabled-key',
      clientIpBase: '10.10.10.'
    },
    {
      key: created.apiKeys.adminMain,
      owner: created.users.admin,
      group: created.groups.experiment,
      accounts: [created.accounts.pendingTest, created.accounts.unschedulable, created.accounts.scheduledInactive],
      label: 'admin-unschedulable-statuses',
      clientIpBase: '10.10.11.'
    },
    {
      key: created.apiKeys.managerMain,
      owner: created.users.manager,
      group: created.groups.managerMain,
      accounts: [created.accounts.managerPrimary],
      label: 'manager-main',
      clientIpBase: '10.23.1.'
    },
    {
      key: created.apiKeys.managerMain,
      owner: created.users.manager,
      group: created.groups.managerMain,
      accounts: [created.accounts.managerPrimary],
      label: 'manager-manual-account-test',
      clientIpBase: '10.23.3.',
      trafficSource: 'manual_account_test'
    },
    {
      key: created.apiKeys.managerHighConcurrency,
      owner: created.users.manager,
      group: created.groups.managerHighConcurrency,
      accounts: [created.accounts.managerBurst],
      label: 'manager-high-concurrency',
      clientIpBase: '10.23.2.'
    },
    {
      key: created.apiKeys.devGroupAuthorized,
      owner: created.users.dev,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.proxied, created.accounts.normal],
      label: 'dev-direct-group-auth',
      clientIpBase: '10.20.1.'
    },
    {
      key: created.apiKeys.devGroupAuthorized,
      owner: created.users.dev,
      group: created.groups.devDefault,
      accounts: [devPrimaryInstance],
      label: 'dev-team-account-auth',
      clientIpBase: '10.20.2.'
    },
    {
      key: created.apiKeys.devGroupAuthorized,
      owner: created.users.dev,
      group: created.groups.devDefault,
      accounts: [devBurstFastInstance],
      label: 'dev-direct-burst-account-auth',
      clientIpBase: '10.20.7.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.backup,
      accounts: [created.accounts.fallback, created.accounts.rateLimited],
      label: 'tester-team-group-auth',
      clientIpBase: '10.20.3.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.testerDefault,
      accounts: [testerPrimaryInstance],
      label: 'tester-team-account-auth',
      clientIpBase: '10.20.4.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.experiment,
      accounts: [created.accounts.temporary, created.accounts.error],
      label: 'tester-direct-experiment-group-auth',
      clientIpBase: '10.20.8.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.experiment,
      accounts: [created.accounts.temporary],
      label: 'tester-cooldown-retest',
      clientIpBase: '10.20.11.',
      trafficSource: 'cooldown_retest'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'ops-team-group-auth',
      clientIpBase: '10.20.5.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.opsDefault,
      accounts: [opsProxiedInstance, opsPrimaryInstance],
      label: 'ops-account-auth',
      clientIpBase: '10.20.6.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.highConcurrency,
      accounts: [created.accounts.burstFast, created.accounts.burstImage, created.accounts.burstFallback],
      label: 'ops-direct-high-concurrency-group-auth',
      clientIpBase: '10.20.9.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.opsDefault,
      accounts: [opsBurstFallbackInstance],
      label: 'ops-team-burst-account-auth',
      clientIpBase: '10.20.10.'
    },
    {
      key: created.apiKeys.financeAuthorized,
      owner: created.users.finance,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'finance-direct-oauth-group-auth',
      clientIpBase: '10.21.1.'
    },
    {
      key: created.apiKeys.financeAuthorized,
      owner: created.users.finance,
      group: created.groups.financeDefault,
      accounts: [financeOauthInstance],
      label: 'finance-direct-oauth-account-auth',
      clientIpBase: '10.21.2.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.backup,
      accounts: [created.accounts.fallback, created.accounts.rateLimited],
      label: 'viewer-direct-backup-group-auth',
      clientIpBase: '10.22.1.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'viewer-team-oauth-group-auth',
      clientIpBase: '10.22.2.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.viewerDefault,
      accounts: [viewerBurstImageInstance],
      label: 'viewer-direct-burst-image-account-auth',
      clientIpBase: '10.22.3.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.viewerDefault,
      accounts: [viewerBurstFallbackInstance],
      label: 'viewer-team-burst-account-auth',
      clientIpBase: '10.22.4.'
    }
  ]

  const records: UsageRecordSeed[] = []
  const endAt = new Date(Date.now() - 10 * minuteMs)
  const startAt = new Date(endAt)
  startAt.setUTCDate(endAt.getUTCDate() - options.days + 1)
  startAt.setUTCHours(0, 0, 0, 0)

  for (let dayIndex = 0; dayIndex < options.days; dayIndex += 1) {
    const dayStart = new Date(startAt.getTime() + dayIndex * dayMs)
    const latestForDay = dayIndex === options.days - 1
      ? endAt
      : new Date(dayStart.getTime() + dayMs - minuteMs)
    const minuteRange = Math.max(1, Math.floor((latestForDay.getTime() - dayStart.getTime()) / minuteMs))
    for (let requestIndex = 0; requestIndex < options.dailyRequests; requestIndex += 1) {
      const ordinal = dayIndex * options.dailyRequests + requestIndex
      const scenario = scenarios[weightedIndex(ordinal, scenarios.length)]
      const account = scenario.accounts[weightedIndex(ordinal + 11, scenario.accounts.length)]
      const minuteOfDay = Math.min(
        minuteRange,
        Math.floor(((requestIndex + pseudoRandom(ordinal, 2)) / options.dailyRequests) * minuteRange)
      )
      const createdAt = new Date(dayStart.getTime() + minuteOfDay * minuteMs + Math.floor(pseudoRandom(ordinal, 3) * 45_000))
      const record = buildUsageRecord({
        ordinal,
        createdAt: createdAt > latestForDay ? latestForDay : createdAt,
        scenario,
        account
      })
      records.push(record)
    }
  }

  appendCoverageUsageRecords(records, created, endAt)

  for (const chunk of chunks(records, 500)) {
    repositories.createUsageRecordsBatch(chunk)
  }
  return records
}

function appendCoverageUsageRecords(records: UsageRecordSeed[], created: CreatedMockdata, endAt: Date): void {
  const coverageInputs: Array<Omit<BuildUsageRecordInput, 'createdAt'>> = [
    {
      ordinal: nextOrdinal(records.length + 1000, (value) => value % 41 !== 0 && value % 11 !== 0 && value % 3 !== 0),
      idSuffix: 'coverage_model_mapping',
      modelOverride: 'mockdata-global-long-context',
      scenario: {
        key: created.apiKeys.adminRoundRobin,
        owner: created.users.admin,
        group: created.groups.main,
        accounts: [created.accounts.standardClient],
        label: 'admin-model-mapping-coverage',
        clientIpBase: '10.10.13.'
      },
      account: created.accounts.standardClient
    },
    {
      ordinal: nextOrdinal(records.length + 1100, (value) => value % 11 === 0),
      idSuffix: 'coverage_models_endpoint',
      scenario: {
        key: created.apiKeys.adminMain,
        owner: created.users.admin,
        group: created.groups.main,
        accounts: [created.accounts.primary],
        label: 'admin-models-endpoint-coverage',
        clientIpBase: '10.10.14.'
      },
      account: created.accounts.primary
    },
    {
      ordinal: nextOrdinal(records.length + 1200, (value) => value % 3 === 0 && value % 41 !== 0),
      idSuffix: 'coverage_chat_endpoint',
      scenario: {
        key: created.apiKeys.adminMain,
        owner: created.users.admin,
        group: created.groups.main,
        accounts: [created.accounts.primary],
        label: 'admin-chat-endpoint-coverage',
        clientIpBase: '10.10.15.'
      },
      account: created.accounts.primary
    },
    {
      ordinal: nextOrdinal(records.length + 1300, (value) => value % 41 === 0),
      idSuffix: 'coverage_image_endpoint',
      scenario: {
        key: created.apiKeys.adminMain,
        owner: created.users.admin,
        group: created.groups.experiment,
        accounts: [created.accounts.image],
        label: 'admin-image-endpoint-coverage',
        clientIpBase: '10.10.16.'
      },
      account: created.accounts.image
    },
    {
      ordinal: nextOrdinal(records.length + 1400, (value) => value % 41 !== 0 && value % 11 !== 0 && value % 3 !== 0),
      idSuffix: 'coverage_manual_account_test',
      scenario: {
        key: created.apiKeys.managerMain,
        owner: created.users.manager,
        group: created.groups.managerMain,
        accounts: [created.accounts.managerPrimary],
        label: 'manager-manual-account-test-coverage',
        clientIpBase: '10.23.4.',
        trafficSource: 'manual_account_test'
      },
      account: created.accounts.managerPrimary
    },
    {
      ordinal: nextOrdinal(records.length + 1500, (value) => value % 41 !== 0 && value % 11 !== 0 && value % 3 !== 0),
      idSuffix: 'coverage_cooldown_retest',
      scenario: {
        key: created.apiKeys.testerTeamAuthorized,
        owner: created.users.tester,
        group: created.groups.experiment,
        accounts: [created.accounts.temporary],
        label: 'tester-cooldown-retest-coverage',
        clientIpBase: '10.20.12.',
        trafficSource: 'cooldown_retest'
      },
      account: created.accounts.temporary
    },
    {
      ordinal: nextOrdinal(records.length + 1600, (value) => value % 41 !== 0 && value % 11 !== 0 && value % 3 !== 0),
      idSuffix: 'coverage_hybrid_scoring',
      scenario: {
        key: created.apiKeys.adminMain,
        owner: created.users.admin,
        group: created.groups.main,
        accounts: [created.accounts.primary],
        label: 'admin-hybrid-scoring-coverage',
        clientIpBase: '10.10.17.',
        trafficSource: 'hybrid_scoring'
      },
      account: created.accounts.primary
    },
    {
      ordinal: nextOrdinal(records.length + 1700, (value) => value % 41 !== 0 && value % 11 !== 0 && value % 3 !== 0),
      idSuffix: 'coverage_hybrid_quality_scoring',
      scenario: {
        key: created.apiKeys.adminMain,
        owner: created.users.admin,
        group: created.groups.main,
        accounts: [created.accounts.primary],
        label: 'admin-hybrid-quality-scoring-coverage',
        clientIpBase: '10.10.18.',
        trafficSource: 'hybrid_quality_scoring'
      },
      account: created.accounts.primary
    }
  ]

  coverageInputs.forEach((input, index) => {
    records.push(buildUsageRecord({
      ...input,
      createdAt: new Date(endAt.getTime() - (coverageInputs.length - index) * minuteMs)
    }))
  })
}

interface BuildUsageRecordInput {
  ordinal: number
  idSuffix?: string
  modelOverride?: string
  createdAt: Date
  scenario: KeyScenario
  account: AccountSummary
}

function buildUsageRecord(input: BuildUsageRecordInput): UsageRecordSeed {
  const endpointInfo = endpointForOrdinal(input.ordinal)
  const model = input.modelOverride ?? modelForOrdinal(input.ordinal)
  const forcedFailure = input.account.status === 'error'
    || input.account.status === 'pending_test'
    || input.account.status === 'disabled'
    || input.account.status === 'rate_limited'
    || input.account.status === 'temporary_unavailable'
    || input.account.schedulable === false
    || input.account.availabilityScheduleActive === false
  const failureRoll = pseudoRandom(input.ordinal, 10)
  const success = !forcedFailure && failureRoll > 0.11
  const error = success ? undefined : errorForOrdinal(input.ordinal, forcedFailure)
  const inputTokens = success ? 120 + Math.floor(pseudoRandom(input.ordinal, 20) * 6800) : Math.floor(pseudoRandom(input.ordinal, 21) * 300)
  const outputTokens = success ? 40 + Math.floor(pseudoRandom(input.ordinal, 22) * 2200) : Math.floor(pseudoRandom(input.ordinal, 23) * 80)
  const cacheReadTokens = success && input.ordinal % 5 === 0 ? Math.floor(inputTokens * (0.15 + pseudoRandom(input.ordinal, 24) * 0.4)) : 0
  const firstTokenMs = success && endpointInfo.path !== '/v1/models'
    ? 80 + Math.floor(pseudoRandom(input.ordinal, 30) * 1450)
    : undefined
  const durationMs = success
    ? (firstTokenMs ?? 60) + 300 + Math.floor(pseudoRandom(input.ordinal, 31) * 4200)
    : 90 + Math.floor(pseudoRandom(input.ordinal, 32) * 1200)
  const cost = usageCost(model, inputTokens, outputTokens, cacheReadTokens, success)
  const modelMapping = modelMappingForRecord(input.account, model)
  const inputImageTokens = isImageModel(model) || endpointInfo.path.includes('/images/')
    ? 85 + (input.ordinal % 400)
    : input.ordinal % 37 === 0 ? 85 + (input.ordinal % 400) : undefined
  const outputImageTokens = isImageModel(model) || endpointInfo.path.includes('/images/')
    ? 120 + (input.ordinal % 240)
    : input.ordinal % 97 === 0 ? 120 + (input.ordinal % 240) : undefined
  const recordKey = input.idSuffix ?? String(input.ordinal + 1).padStart(5, '0')
  const traceId = `${tracePrefix}usage-${recordKey}`
  return {
    id: `${idPrefix}usage_${recordKey}`,
    systemAccountId: input.scenario.owner.id,
    traceId,
    trafficSource: input.scenario.trafficSource ?? 'gateway',
    clientIp: `${input.scenario.clientIpBase}${20 + (input.ordinal % 180)}`,
    apiKeyId: input.scenario.key.id,
    groupId: input.scenario.group.id,
    accountId: input.account.id,
    endpoint: `${endpointInfo.method} ${endpointInfo.path}`,
    providerCode,
    model,
    upstreamModel: modelMapping?.upstreamModel,
    modelMappingApplied: Boolean(modelMapping),
    modelMappingSource: modelMapping?.sourceModel,
    stream: endpointInfo.stream,
    statusCode: success ? 200 : error?.statusCode,
    success,
    firstTokenMs,
    durationMs,
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheReadCostUsd: cost.cacheReadCost,
    inputImageTokens,
    outputImageTokens,
    costUsd: cost.totalCost,
    errorCode: error?.code,
    errorMessage: error?.message,
    requestSnapshot: {
      method: endpointInfo.method,
      path: endpointInfo.path,
      scenario: input.scenario.label,
      body: endpointInfo.path === '/v1/models'
        ? undefined
        : {
            model,
            stream: endpointInfo.stream,
            input: `Mockdata 请求 ${input.ordinal + 1}`,
            max_output_tokens: 1024,
            ...(endpointInfo.path.includes('/images/') ? { prompt: `Mockdata 图片请求 ${input.ordinal + 1}`, size: '1024x1024' } : {})
          }
    },
    responseSnapshot: success
      ? {
          status: 200,
          model,
          upstream_model: modelMapping?.upstreamModel,
          usage: {
            input_tokens: inputTokens,
            output_tokens: outputTokens,
            cache_read_tokens: cacheReadTokens,
            input_image_tokens: inputImageTokens,
            output_image_tokens: outputImageTokens
          }
        }
      : {
          status: error?.statusCode,
          error: {
            code: error?.code,
            message: error?.message
          }
        },
    createdAt: input.createdAt.toISOString()
  }
}

function endpointForOrdinal(ordinal: number): { method: string; path: string; stream: boolean } {
  if (ordinal % 41 === 0) return { method: 'POST', path: '/v1/images/generations', stream: false }
  if (ordinal % 11 === 0) return { method: 'GET', path: '/v1/models', stream: false }
  if (ordinal % 3 === 0) return { method: 'POST', path: '/v1/chat/completions', stream: ordinal % 2 === 0 }
  return { method: 'POST', path: '/v1/responses', stream: ordinal % 4 === 0 }
}

function modelForOrdinal(ordinal: number): string {
  if (ordinal % 41 === 0) return ordinal % 82 === 0 ? 'mockdata-global-image' : 'gpt-image-1'
  if (ordinal % 29 === 0) return 'mockdata-global-long-context'
  const models = ['gpt-5.4-mini', 'gpt-5.4-mini', 'gpt-5.4', 'gpt-4.1-mini', 'gpt-4o-mini']
  return models[ordinal % models.length]
}

function modelMappingForRecord(account: AccountSummary, model: string): { sourceModel: string; upstreamModel: string } | undefined {
  const mapping = (account.modelMappings ?? []).find((item) =>
    item.enabled !== false
    && item.sourceModel === model
    && item.upstreamModel !== item.sourceModel
  )
  return mapping ? { sourceModel: mapping.sourceModel, upstreamModel: mapping.upstreamModel } : undefined
}

function isImageModel(model: string): boolean {
  const normalized = model.toLowerCase()
  return normalized.startsWith('gpt-image') || normalized.startsWith('dall-e') || normalized === 'mockdata-global-image'
}

function nextOrdinal(start: number, predicate: (value: number) => boolean): number {
  let ordinal = start
  while (!predicate(ordinal)) {
    ordinal += 1
  }
  return ordinal
}

function errorForOrdinal(ordinal: number, forcedFailure: boolean): { statusCode: number; code: string; message: string } {
  if (forcedFailure && ordinal % 2 === 0) {
    return { statusCode: 429, code: 'rate_limit_exceeded', message: 'Mockdata 模拟账户当前处于冷却或限流状态' }
  }
  const errors = [
    { statusCode: 429, code: 'rate_limit_exceeded', message: 'Mockdata 模拟上游限流' },
    { statusCode: 503, code: 'service_unavailable', message: 'Mockdata 模拟上游维护' },
    { statusCode: 500, code: 'upstream_error', message: 'Mockdata 模拟上游内部错误' },
    { statusCode: 402, code: 'insufficient_balance', message: 'Mockdata 模拟服务商余额不足' },
    { statusCode: 401, code: 'invalid_api_key', message: 'Mockdata 模拟认证失败' }
  ]
  return errors[ordinal % errors.length]
}

function usageCost(model: string, inputTokens: number, outputTokens: number, cacheReadTokens: number, success: boolean): { totalCost: number; cacheReadCost: number } {
  if (!success) return { totalCost: 0, cacheReadCost: 0 }
  const price = modelPrices[model] ?? modelPrices['gpt-5.4-mini']
  const billableInputTokens = Math.max(0, inputTokens - cacheReadTokens)
  const cacheReadCost = roundCost((cacheReadTokens / 1_000_000) * price.cached)
  const totalCost = roundCost((billableInputTokens / 1_000_000) * price.input + (outputTokens / 1_000_000) * price.output + cacheReadCost)
  return { totalCost, cacheReadCost }
}
