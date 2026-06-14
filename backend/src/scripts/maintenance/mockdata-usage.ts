import type { AccountSummary, GroupSummary, SystemAccountSummary } from '../../domain/types.js'
import * as repositories from '../../storage/repositories.js'
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
} from './mockdata-shared.js'
import { authorizationInstanceAccount } from './mockdata-account-helpers.js'

interface KeyScenario {
  key: ApiKeyWithSecret
  owner: SystemAccountSummary
  group: GroupSummary
  accounts: AccountSummary[]
  label: string
  clientIpBase: string
}

const modelPrices: Record<string, { input: number; output: number; cached: number }> = {
  'gpt-5.4-mini': { input: 0.15, output: 0.6, cached: 0.04 },
  'gpt-5.4': { input: 1.25, output: 10, cached: 0.3 },
  'gpt-4.1-mini': { input: 0.4, output: 1.6, cached: 0.1 },
  'gpt-4o-mini': { input: 0.15, output: 0.6, cached: 0.04 }
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
      key: created.apiKeys.managerMain,
      owner: created.users.manager,
      group: created.groups.managerMain,
      accounts: [created.accounts.managerPrimary],
      label: 'manager-main',
      clientIpBase: '10.23.1.'
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

  for (const chunk of chunks(records, 500)) {
    repositories.createUsageRecordsBatch(chunk)
  }
  return records
}

function buildUsageRecord(input: {
  ordinal: number
  createdAt: Date
  scenario: KeyScenario
  account: AccountSummary
}): UsageRecordSeed {
  const endpointInfo = endpointForOrdinal(input.ordinal)
  const model = modelForOrdinal(input.ordinal)
  const forcedFailure = input.account.status === 'error'
    || input.account.status === 'disabled'
    || input.account.status === 'rate_limited'
    || input.account.status === 'temporary_unavailable'
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
  const traceId = `${tracePrefix}usage-${String(input.ordinal + 1).padStart(5, '0')}`
  return {
    id: `${idPrefix}usage_${String(input.ordinal + 1).padStart(5, '0')}`,
    systemAccountId: input.scenario.owner.id,
    traceId,
    trafficSource: 'gateway',
    clientIp: `${input.scenario.clientIpBase}${20 + (input.ordinal % 180)}`,
    apiKeyId: input.scenario.key.id,
    groupId: input.scenario.group.id,
    accountId: input.account.id,
    endpoint: `${endpointInfo.method} ${endpointInfo.path}`,
    providerCode,
    model,
    stream: endpointInfo.stream,
    statusCode: success ? 200 : error?.statusCode,
    success,
    firstTokenMs,
    durationMs,
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheReadCostUsd: cost.cacheReadCost,
    inputImageTokens: input.ordinal % 37 === 0 ? 85 + (input.ordinal % 400) : undefined,
    outputImageTokens: input.ordinal % 97 === 0 ? 120 + (input.ordinal % 240) : undefined,
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
            max_output_tokens: 1024
          }
    },
    responseSnapshot: success
      ? {
          status: 200,
          model,
          usage: {
            input_tokens: inputTokens,
            output_tokens: outputTokens,
            cache_read_tokens: cacheReadTokens
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
  if (ordinal % 11 === 0) return { method: 'GET', path: '/v1/models', stream: false }
  if (ordinal % 3 === 0) return { method: 'POST', path: '/v1/chat/completions', stream: ordinal % 2 === 0 }
  return { method: 'POST', path: '/v1/responses', stream: ordinal % 4 === 0 }
}

function modelForOrdinal(ordinal: number): string {
  const models = ['gpt-5.4-mini', 'gpt-5.4-mini', 'gpt-5.4', 'gpt-4.1-mini', 'gpt-4o-mini']
  return models[ordinal % models.length]
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
