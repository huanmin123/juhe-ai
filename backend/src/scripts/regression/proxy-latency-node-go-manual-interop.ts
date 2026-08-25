import assert from 'node:assert/strict'

import { runProxyLatencyManualViaGo } from '../../modules/background/proxy-latency-handover.js'

const proxyHost = requiredEnvironment('J3A_NODE_GO_MANUAL_INTEROP_PROXY_HOST')
const proxyPort = Number(requiredEnvironment('J3A_NODE_GO_MANUAL_INTEROP_PROXY_PORT'))
if (!Number.isSafeInteger(proxyPort) || proxyPort < 1 || proxyPort > 65535) throw new Error('J3a interop proxy port 无效')

let requestBody: string | undefined
const fetchBefore = globalThis.fetch
globalThis.fetch = async (input, init) => {
  requestBody = typeof init?.body === 'string' ? init.body : undefined
  return fetchBefore(input, init)
}
let report
try {
  report = await runProxyLatencyManualViaGo({
    id: 'proxy-manual',
    name: 'Manual proxy',
    type: 'http',
    host: proxyHost,
    port: proxyPort,
    enabled: true,
    testStatus: 'unknown',
    updatedAt: '2026-08-23T00:00:00.123Z',
    configUpdatedAt: '2026-08-23T00:00:00.123Z',
    proxyUrl: `http://${proxyHost}:${proxyPort}`
  }, { timeoutMs: 1_000 }, {
    providers: async () => [
      { enabled: true, code: 'openai', name: 'OpenAI', baseUrl: 'http://provider.invalid/', defaultProtocolProfileId: 'profile-openai' },
      { enabled: true, code: 'hybrid', name: 'Hybrid', baseUrl: '' },
      { enabled: true, code: 'unsupported', name: 'Unsupported', baseUrl: 'ftp://provider.invalid/v1' }
    ]
  })
} finally {
  globalThis.fetch = fetchBefore
}

const request = JSON.parse(requestBody ?? '') as { input?: { targets?: Array<{ provider?: string; profile_id?: string }> } }
assert.deepEqual(request.input?.targets?.map((target) => ({ provider: target.provider, profile_id: target.profile_id })), [
  { provider: 'openai', profile_id: 'profile-openai' },
  { provider: 'hybrid', profile_id: 'hybrid' },
  { provider: 'unsupported', profile_id: 'unsupported' }
], 'Node manual payload must use the real default protocol profile id and preserve provider-code fallback')

assert.equal(report.proxyId, 'proxy-manual')
assert.equal(report.items.length, 4, 'Go manual report must retain the base item and all providers')
const hybrid = report.items[2]
assert.deepEqual(hybrid, {
  name: 'Hybrid',
  status: 'unknown',
  message: '未形成真实代理检测请求：Invalid URL',
  targetUrl: ''
}, 'Node adapter must preserve the legacy empty provider target contract through Go')
assert.deepEqual(report.items[3], {
  name: 'Unsupported',
  status: 'unknown',
  message: '未形成真实代理检测请求：不支持的目标协议：ftp:',
  targetUrl: 'ftp://provider.invalid/v1'
}, 'Node adapter must retain unsupported provider URLs only in the outward legacy report')

console.log('proxy latency node-go manual interop passed')

function requiredEnvironment(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required for the explicit J3a Node-Go interop test`)
  return value
}
