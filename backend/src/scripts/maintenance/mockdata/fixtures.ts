import type { AccountSummary, ApiKeySummary, GroupSummary } from '../../../domain/types.js'
import type { AccessScope } from '../../../storage/access-scope.js'
import * as repositories from '../../../storage/repositories.js'

type ApiKeyWithSecret = ApiKeySummary & { key: string }

export interface MockGatewayFixtureOptions {
  label: string
  upstreamBaseUrl: string
  systemAccountId?: string
  accountCount?: number
  accountConcurrencyLimit?: number
  createApiKey?: boolean
}

export interface MockGatewayFixture {
  group: GroupSummary
  accounts: AccountSummary[]
  apiKey?: ApiKeyWithSecret
}

const namePrefix = '造数-'

export function createMockGatewayFixture(options: MockGatewayFixtureOptions): MockGatewayFixture {
  const accountCount = boundedInteger(options.accountCount, 1, 1, 1000)
  const concurrencyLimit = boundedInteger(options.accountConcurrencyLimit, 20, 1, 1000000)
  const systemAccountId = options.systemAccountId?.trim() || 'sys_admin'
  const access: AccessScope = { systemAccountId, role: 'user' }
  const runId = mockGatewayFixtureRunId()
  const nameScope = `${namePrefix}${options.label}`

  const group = repositories.createGroup({
    name: `${nameScope}分组-${runId}`,
    providerCode: 'gpt',
    description: `${options.label}通过 Mockdata 共享夹具生成的临时分组`,
    enabled: true
  }, access)

  const accounts: AccountSummary[] = []
  for (let index = 0; index < accountCount; index += 1) {
    accounts.push(repositories.createAccount({
      providerCode: 'gpt',
      name: `${nameScope}账户-${index + 1}-${runId}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-mockdata-${asciiToken(options.label)}-${index + 1}-${runId}`,
        base_url: options.upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit,
      priority: index,
      notes: `${options.label}通过 Mockdata 共享夹具生成，使用后可按造数前缀清理`
    }, access))
  }

  const apiKey = options.createApiKey === false
    ? undefined
    : repositories.createApiKeyRecord({
      name: `${nameScope}Key-${runId}`,
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active',
      description: `${options.label}通过 Mockdata 共享夹具生成的临时本地网关 Key`
    }, access) as ApiKeyWithSecret

  return { group, accounts, apiKey }
}

function mockGatewayFixtureRunId(): string {
  const time = new Date().toISOString().replace(/[-:TZ.]/g, '')
  return `${time}-${Math.random().toString(36).slice(2, 8)}`
}

function asciiToken(value: string): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return normalized || 'gateway-fixture'
}

function boundedInteger(value: number | undefined, fallback: number, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return fallback
  const integer = Math.trunc(Number(value))
  return Math.min(max, Math.max(min, integer))
}
