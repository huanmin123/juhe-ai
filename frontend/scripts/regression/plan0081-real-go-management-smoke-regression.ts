import { strict as assert } from 'node:assert'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

import {
  formatRealGoManagementSmokeSummary,
  realGoManagementSmokeEnv,
  runRealGoManagementSmokeFromEnvironment,
  type SmokeEnvironment
} from '../smoke/plan0081-real-go-management-smoke'

interface RequestRecord {
  method?: string
  url?: string
  headers: IncomingMessage['headers']
}

const cookie = 'juhe_ai_session=regression-secret; another_cookie=opaque-value'
const systemAccountId = 'sys_plan0081_target'
const selectedGroupId = 'grp_plan0081_owner_secondary'
const requestRecords: RequestRecord[] = []
let failurePath: string | undefined

const server = createServer(handleRequest)
await listen(server)

try {
  const baseUrl = serverBaseUrl(server)
  const env = smokeEnvironment(baseUrl)

  await assertSuccessfulSmoke(env)
  await assertErrorStatus(env)
  await assertMissingConfiguration()
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go management smoke regression passed')

async function assertSuccessfulSmoke(env: SmokeEnvironment): Promise<void> {
  requestRecords.length = 0
  failurePath = undefined
  const output: string[] = []
  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))

  assert.deepEqual(summary, {
    groupCount: 3,
    selectedGroupId,
    providerCount: 2,
    modelOptionCount: 2
  })
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 selectedGroupId=grp_plan0081_owner_secondary providers=2 modelOptions=2'
  )
  assert.equal(output.some((line) => line.includes(cookie)), false, 'success output must not expose the Cookie header')

  assert.deepEqual(
    requestRecords.map((record) => `${record.method} ${record.url}`),
    [
      `GET /__aisys__/api/groups?page=1&pageSize=500&systemAccountId=${systemAccountId}`,
      `GET /__aisys__/api/groups/${selectedGroupId}?systemAccountId=${systemAccountId}`,
      `GET /__aisys__/api/providers/options?systemAccountId=${systemAccountId}`,
      `GET /__aisys__/api/providers/models/options?systemAccountId=${systemAccountId}`
    ]
  )

  for (const record of requestRecords) {
    assert.equal(record.headers.cookie, cookie)
    assert.equal(record.headers.accept, 'application/json')
    assert.equal(record.headers['user-agent'], 'juhe-ai-plan0081-real-go-management-smoke/1.0')
    assert.equal(record.headers['x-juhe-ai-smoke'], 'plan0081-real-go-management')
  }
}

async function assertErrorStatus(env: SmokeEnvironment): Promise<void> {
  requestRecords.length = 0
  failurePath = '/__aisys__/api/providers/options'
  const output: string[] = []

  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message)),
    /providers\/options failed with HTTP 503/
  )
  assert.deepEqual(output, [])
  assert.equal(requestRecords.at(-1)?.url, `/__aisys__/api/providers/options?systemAccountId=${systemAccountId}`)
  assert.equal(requestRecords.some((record) => record.headers.cookie !== cookie), false)
  failurePath = undefined
}

async function assertMissingConfiguration(): Promise<void> {
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment({}, () => undefined),
    new RegExp(`Missing required environment variable: ${realGoManagementSmokeEnv.baseUrl}`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment({
      [realGoManagementSmokeEnv.baseUrl]: 'http://127.0.0.1:5173'
    }, () => undefined),
    new RegExp(`Missing required environment variable: ${realGoManagementSmokeEnv.cookie}`)
  )
}

function smokeEnvironment(baseUrl: string): SmokeEnvironment {
  return {
    [realGoManagementSmokeEnv.baseUrl]: baseUrl,
    [realGoManagementSmokeEnv.cookie]: cookie,
    [realGoManagementSmokeEnv.systemAccountId]: systemAccountId
  }
}

function handleRequest(req: IncomingMessage, res: ServerResponse): void {
  requestRecords.push({
    method: req.method,
    url: req.url,
    headers: { ...req.headers }
  })

  const url = new URL(req.url ?? '/', 'http://127.0.0.1')
  res.setHeader('Cache-Control', 'no-store')
  res.setHeader('Content-Type', 'application/json; charset=utf-8')

  if (url.pathname === failurePath) {
    res.statusCode = 503
    res.end(JSON.stringify({ message: 'mock unavailable' }))
    return
  }

  const data = fixtureFor(url.pathname)
  if (data === undefined) {
    res.statusCode = 404
    res.end(JSON.stringify({ message: 'not found' }))
    return
  }
  res.end(JSON.stringify({ data }))
}

function fixtureFor(pathname: string): unknown {
  if (pathname === '/__aisys__/api/groups') {
    return {
      items: [
        groupFixture('grp_plan0081_default', '默认分组', true, 'owner'),
        groupFixture('grp_plan0081_authorized', '授权分组', false, 'authorized'),
        groupFixture(selectedGroupId, '真实 Go 管理 smoke 分组', false, 'owner')
      ],
      total: 3,
      hasMore: false,
      page: 1,
      pageSize: 500,
      runtimeSnapshot: {
        accountConcurrencyAvailable: true
      }
    }
  }
  if (pathname === `/__aisys__/api/groups/${selectedGroupId}`) {
    return {
      ...groupFixture(selectedGroupId, '真实 Go 管理 smoke 分组', false, 'owner'),
      accountIds: ['acct_plan0081_one']
    }
  }
  if (pathname === '/__aisys__/api/providers/options') {
    return [
      providerFixture('gpt', 'GPT'),
      providerFixture('openai', 'OpenAI')
    ]
  }
  if (pathname === '/__aisys__/api/providers/models/options') {
    return [
      {
        providerCode: 'gpt',
        model: 'gpt-5.6-sol',
        supportedApiProtocols: ['responses'],
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['low', 'high']
      },
      {
        providerCode: 'openai',
        model: 'gpt-4.1',
        supportedApiProtocols: ['chat_completions']
      }
    ]
  }
  return undefined
}

function groupFixture(
  id: string,
  name: string,
  isDefault: boolean,
  accessType: 'owner' | 'authorized'
): Record<string, unknown> {
  return {
    id,
    ownerSystemAccountId: accessType === 'owner' ? systemAccountId : 'sys_plan0081_owner',
    ownerSystemAccountName: 'Owner',
    name,
    providerCode: 'gpt',
    enabled: true,
    isDefault,
    groupType: 'personal',
    accountStats: {
      total: 1,
      available: 1
    },
    accessType,
    authorizationLimits: {},
    permissions: {
      canManageAccounts: accessType === 'owner'
    },
    accountCount: accessType === 'owner' ? 1 : 0
  }
}

function providerFixture(code: string, name: string): Record<string, unknown> {
  const profileId = `profile_${code}_openai_v1`
  return {
    id: `provider_${code}`,
    code,
    name,
    enabled: true,
    defaultProtocolProfileId: profileId,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://api.example.test/v1',
    defaultHealthCheckModel: code === 'gpt' ? 'gpt-5.6-sol' : 'gpt-4.1',
    defaultSupportedModels: code === 'gpt' ? ['gpt-5.6-sol'] : ['gpt-4.1'],
    accountTypes: ['api_key'],
    capabilities: ['chat_completions', 'responses'],
    protocolProfiles: [{
      id: profileId,
      providerCode: code,
      name: `${name} OpenAI v1`,
      enabled: true,
      protocolCode: 'openai',
      protocolVersion: 'v1',
      baseUrl: 'https://api.example.test/v1',
      defaultHealthCheckModel: code === 'gpt' ? 'gpt-5.6-sol' : 'gpt-4.1',
      accountTypes: ['api_key'],
      capabilities: ['chat_completions', 'responses'],
      endpointFamilies: []
    }]
  }
}

function listen(target: Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    target.once('error', rejectPromise)
    target.listen(0, '127.0.0.1', () => {
      target.off('error', rejectPromise)
      resolvePromise()
    })
  })
}

function close(target: Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    target.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}

function serverBaseUrl(target: Server): string {
  const address = target.address()
  assert(address && typeof address !== 'string')
  return `http://127.0.0.1:${(address as AddressInfo).port}`
}
