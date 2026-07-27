import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  GROK_SSO_ACCOUNTS_URL,
  GROK_SSO_APPROVE_URL,
  GROK_SSO_BUILD_SCOPE,
  GROK_SSO_CLIENT_ID,
  GROK_SSO_DEVICE_URL,
  GROK_SSO_TOKEN_URL,
  GROK_SSO_VERIFY_URL,
  convertGrokSSOToOAuth,
  grokSSOConversionTimeoutMs,
  grokSSOMaxAuthBodyBytes,
  normalizeGrokSSOImportTokens,
  normalizeGrokSSOToken,
  type GrokSSODeviceRequest,
  type GrokSSODeviceResponse
} from '../../modules/grok-oauth/grok-sso-device-flow.js'
import { GROK_OAUTH_CLIENT_ID } from '../../modules/grok-oauth/grok-oauth.service.js'

assert.equal(GROK_SSO_CLIENT_ID, GROK_OAUTH_CLIENT_ID, 'SSO device flow 必须复用 Grok CLI OAuth client id')
assert.equal(GROK_SSO_BUILD_SCOPE, 'openid profile email offline_access grok-cli:access api:access conversations:read conversations:write')
assert.equal(GROK_SSO_ACCOUNTS_URL, 'https://accounts.x.ai/')
assert.equal(GROK_SSO_DEVICE_URL, 'https://auth.x.ai/oauth2/device/code')
assert.equal(GROK_SSO_VERIFY_URL, 'https://auth.x.ai/oauth2/device/verify')
assert.equal(GROK_SSO_APPROVE_URL, 'https://auth.x.ai/oauth2/device/approve')
assert.equal(GROK_SSO_TOKEN_URL, 'https://auth.x.ai/oauth2/token')
assert.equal(grokSSOConversionTimeoutMs, 90_000, '每个 SSO device HTTP 请求必须保持参考实现的 90 秒超时')

assert.equal(normalizeGrokSSOToken('Cookie: foo=bar; sso=token-1; sso-rw=token-2'), 'token-1')
assert.equal(normalizeGrokSSOToken('sso-rw=token-2; foo=bar'), 'token-2')
assert.equal(normalizeGrokSSOToken(' raw-token ; ignored=1'), 'raw-token')
assert.equal(normalizeGrokSSOToken('sso=token\r\n-injection'), 'token-injection')
assert.deepEqual(
  normalizeGrokSSOImportTokens(['sso=token-1, token-2\r\nsso-rw=token-1', ''], 'token-0'),
  ['token-0', 'token-1', 'token-2'],
  '批量输入必须支持单项、逗号、换行，并按归一化后的 SSO token 去重'
)

const requests: GrokSSODeviceRequest[] = []
const sleeps: number[] = []
let tokenPolls = 0
let now = 1_000
const token = await convertGrokSSOToOAuth({
  ssoToken: 'sso=sso-secret; ignored=1',
  proxyUrl: 'http://proxy.example:8080',
  dependencies: {
    request: async (request) => {
      requests.push(request)
      assert.equal(request.proxyUrl, 'http://proxy.example:8080', 'device flow 每一步都必须沿用账户代理')
      return fakeResponse(request)
    },
    sleep: async (delayMs) => {
      sleeps.push(delayMs)
      now += delayMs
    },
    now: () => now
  }
})

assert.deepEqual(token, {
  accessToken: 'access-token',
  refreshToken: 'refresh-token',
  idToken: 'id-token',
  tokenType: 'Bearer',
  expiresIn: 3_600,
  scope: GROK_SSO_BUILD_SCOPE
})
assert.deepEqual(sleeps, [1_000, 1_000, 6_000], 'authorization_pending 保持间隔，slow_down 必须增加 5 秒')
assert.equal(tokenPolls, 3)

const firstCookie = stringHeader(requests[0]?.headers.cookie)
const finalCookie = stringHeader(requests.at(-1)?.headers.cookie)
assert.match(firstCookie, /(?:^|; )sso=sso-secret(?:;|$)/u)
assert.match(firstCookie, /(?:^|; )sso-rw=sso-secret(?:;|$)/u)
assert.match(finalCookie, /(?:^|; )session=web-session(?:;|$)/u)
assert.match(finalCookie, /(?:^|; )csrf=csrf-token(?:;|$)/u)

const deviceRequest = requests.find((request) => request.url === GROK_SSO_DEVICE_URL)
assert.equal(deviceRequest?.method, 'POST')
assert.deepEqual(Object.fromEntries(new URLSearchParams(deviceRequest?.body)), {
  client_id: GROK_SSO_CLIENT_ID,
  scope: GROK_SSO_BUILD_SCOPE
})
const approveRequest = requests.find((request) => request.url === GROK_SSO_APPROVE_URL)
assert.deepEqual(Object.fromEntries(new URLSearchParams(approveRequest?.body)), {
  user_code: 'USER-1',
  action: 'allow',
  principal_type: 'User',
  principal_id: ''
})
const pollingRequest = requests.find((request) => request.url === GROK_SSO_TOKEN_URL)
assert.deepEqual(Object.fromEntries(new URLSearchParams(pollingRequest?.body)), {
  grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
  client_id: GROK_SSO_CLIENT_ID,
  device_code: 'device-1'
})

await assert.rejects(
  convertGrokSSOToOAuth({
    ssoToken: 'valid-looking-token',
    dependencies: {
      request: async (request) => request.url === GROK_SSO_ACCOUNTS_URL
        ? response(200, '{}')
        : response(200, JSON.stringify({
            device_code: 'device',
            user_code: 'code',
            verification_uri_complete: 'https://attacker.example/steal',
            interval: 1,
            expires_in: 60
          }))
    }
  }),
  /device flow 响应不完整/u,
  'verification_uri_complete 必须限制为 HTTPS x.ai 域名'
)

await assert.rejects(
  convertGrokSSOToOAuth({
    ssoToken: 'valid-looking-token',
    dependencies: {
      request: async () => response(200, 'x'.repeat(grokSSOMaxAuthBodyBytes + 1))
    }
  }),
  /超过 2 MiB/u,
  '每个 device flow HTTP 响应必须有 2 MiB 硬上限'
)

const routeSource = readFileSync(resolve('src/modules/grok-oauth/grok-oauth.routes.ts'), 'utf8')
assert.equal(routeSource.includes("post('/sso-to-oauth'"), true, 'Grok OAuth 必须暴露 SSO Cookie 批量导入接口')
assert.equal(routeSource.includes('mapWithConcurrency(tokens, 3'), true, 'SSO 批量导入必须固定最多 3 并发')
assert.equal(routeSource.includes("ssoTokens: sensitiveFingerprint(sortedTextValues(bodyField(req, 'ssoTokens')).join('\\n'))"), true, '批量 SSO token 必须先稳定排序再只保留幂等哈希')
assert.equal(routeSource.includes("operationKey: 'grok_oauth.sso_to_oauth'"), true, 'SSO 导入必须记录专属操作日志')
assert.equal(routeSource.includes('account: sanitizeAccountResponse(account)'), true, '逐项成功结果不得返回账户凭据')
assert.equal(routeSource.includes('created: results.filter'), true)
assert.equal(routeSource.includes('failed: results.filter'), true)

console.log('Grok SSO device flow 回归通过：Cookie、device code、批准、轮询、代理、边界与批量契约均符合参考实现')

async function fakeResponse(request: GrokSSODeviceRequest): Promise<GrokSSODeviceResponse> {
  if (request.url === GROK_SSO_ACCOUNTS_URL) {
    assert.equal(request.method, 'GET')
    return response(200, '{}', { 'set-cookie': ['session=web-session; Path=/'] })
  }
  if (request.url === GROK_SSO_DEVICE_URL) {
    return response(200, JSON.stringify({
      device_code: 'device-1',
      user_code: 'USER-1',
      verification_uri_complete: 'https://auth.x.ai/oauth2/device/complete',
      interval: 1,
      expires_in: 60
    }), { 'set-cookie': ['csrf=csrf-token; Path=/'] })
  }
  if (request.url === 'https://auth.x.ai/oauth2/device/complete') return response(200, '<html>ok</html>')
  if (request.url === GROK_SSO_VERIFY_URL) return response(302, '', { location: '/oauth2/device/consent' })
  if (request.url === 'https://auth.x.ai/oauth2/device/consent') return response(200, '<html>consent</html>')
  if (request.url === GROK_SSO_APPROVE_URL) return response(303, '', { location: '/oauth2/device/done' })
  if (request.url === 'https://auth.x.ai/oauth2/device/done') return response(200, '<html>done</html>')
  if (request.url === GROK_SSO_TOKEN_URL) {
    tokenPolls += 1
    if (tokenPolls === 1) return response(400, JSON.stringify({ error: 'authorization_pending' }))
    if (tokenPolls === 2) return response(400, JSON.stringify({ error: 'slow_down' }))
    return response(200, JSON.stringify({
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      id_token: 'id-token',
      token_type: 'Bearer',
      expires_in: 3_600,
      scope: GROK_SSO_BUILD_SCOPE
    }))
  }
  throw new Error(`unexpected request: ${request.method} ${request.url}`)
}

function response(
  statusCode: number,
  body: string,
  headers: GrokSSODeviceResponse['headers'] = {}
): GrokSSODeviceResponse {
  return { statusCode, headers, body }
}

function stringHeader(value: string | number | undefined): string {
  return typeof value === 'string' ? value : ''
}
