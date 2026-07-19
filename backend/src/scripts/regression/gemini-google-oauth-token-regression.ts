import assert from 'node:assert/strict'

import { geminiGoogleOAuthProviderFingerprint } from '../../modules/providers/drivers/gemini/driver.js'
import {
  GOOGLE_OAUTH_TOKEN_ENDPOINT,
  createGeminiGoogleOAuthTokenProvider,
  geminiGoogleOAuthTokenRequestTimeoutMs,
  geminiGoogleOAuthTokenResponseMaxBytes,
  type GeminiGoogleOAuthCredentials,
  type GeminiGoogleOAuthTokenTransport
} from '../../modules/providers/drivers/gemini/google-oauth-token.service.js'

const credentials: GeminiGoogleOAuthCredentials = {
  access_token: 'existing-access-token',
  refresh_token: 'refresh-token-that-must-not-leak',
  client_id: 'client-id.apps.googleusercontent.com',
  client_secret: 'client-secret-that-must-not-leak',
  expires_at: '2026-07-18T12:00:00.000Z'
}

async function run(): Promise<void> {
  assert.notEqual(
    geminiGoogleOAuthProviderFingerprint({ credentials, proxyUrl: 'socks5://proxy-a.example:1080' }),
    geminiGoogleOAuthProviderFingerprint({ credentials, proxyUrl: 'socks5://proxy-b.example:1080' }),
    '账户代理变化必须使 Google OAuth provider 缓存失效'
  )
  assert.equal(
    geminiGoogleOAuthProviderFingerprint({ credentials, proxyUrl: '  socks5://proxy-a.example:1080  ' }),
    geminiGoogleOAuthProviderFingerprint({ credentials, proxyUrl: 'socks5://proxy-a.example:1080' }),
    'Google OAuth provider 指纹必须使用规范化后的账户代理'
  )

  let nowMs = Date.parse('2026-07-18T10:00:00.000Z')
  let requests = 0
  let requestUrl: string | undefined
  let requestBody: string | undefined
  let requestHeaders: Headers | undefined
  let requestProxyUrl: string | undefined
  let requestTimeoutMs: number | undefined
  let requestMaxResponseBytes: number | undefined
  let resolveRefresh: (() => void) | undefined

  const transport: GeminiGoogleOAuthTokenTransport = async (input) => {
    requests += 1
    requestUrl = input.url
    requestBody = input.body
    requestHeaders = input.headers
    requestProxyUrl = input.proxyUrl
    requestTimeoutMs = input.timeoutMs
    requestMaxResponseBytes = input.maxResponseBytes
    if (requests === 1) {
      await new Promise<void>((resolve) => {
        resolveRefresh = resolve
      })
    }
    return { statusCode: 200, bodyText: JSON.stringify({ access_token: 'refreshed-access-token', expires_in: 3600 }), truncated: false }
  }

  const freshProvider = createGeminiGoogleOAuthTokenProvider(credentials, {
    transport,
    now: () => nowMs,
    cacheLeadSeconds: 60,
    proxyUrl: 'socks5://proxy-user:proxy-pass@proxy.example:1080'
  })
  assert.equal(await freshProvider.getAccessToken(), 'existing-access-token', '有效 access_token 应直接复用')
  assert.equal(requests, 0, '有效 access_token 不应触发刷新')

  const accessOnlyProvider = createGeminiGoogleOAuthTokenProvider({
    access_token: 'access-only-token',
    expires_at: '2026-07-18T12:00:00.000Z'
  }, { transport, now: () => nowMs })
  assert.equal(await accessOnlyProvider.getAccessToken(), 'access-only-token', '有效 access_token 不应强制要求 refresh credentials')
  assert.equal(requests, 0, 'access-only 账户不应触发刷新')

  const staticAccessOnlyProvider = createGeminiGoogleOAuthTokenProvider({
    access_token: 'static-access-only-token'
  }, { transport, now: () => nowMs })
  assert.equal(await staticAccessOnlyProvider.getAccessToken(), 'static-access-only-token', '没有 refresh_token 的静态 access_token 应可直接使用')
  assert.equal(requests, 0, '静态 access-only 账户不得尝试刷新')

  nowMs = Date.parse('2026-07-18T12:00:00.000Z') - 30_000
  const first = freshProvider.getAccessToken({ signal: AbortSignal.abort('request-cancelled') })
  const second = freshProvider.getAccessToken()
  assert.equal(resolveRefresh !== undefined, true, '临近过期时应开始刷新')
  await assert.rejects(first, /aborted/, '下游取消只应停止等待 token')
  resolveRefresh?.()
  assert.equal(await second, 'refreshed-access-token', '刷新后应返回新 access_token')
  assert.equal(requests, 1, '并发刷新必须 single-flight')
  assert.equal(requestUrl, GOOGLE_OAUTH_TOKEN_ENDPOINT, '必须调用 Google 官方 token endpoint')
  assert.equal(requestHeaders?.get('content-type'), 'application/x-www-form-urlencoded')
  assert.equal(requestProxyUrl, 'socks5://proxy-user:proxy-pass@proxy.example:1080', 'Google token refresh 必须复用账户代理')
  assert.equal(requestTimeoutMs, geminiGoogleOAuthTokenRequestTimeoutMs, 'Google token refresh 必须使用独立超时')
  assert.equal(requestMaxResponseBytes, geminiGoogleOAuthTokenResponseMaxBytes, 'Google token refresh 必须有界读取响应')
  assert.deepEqual(
    Object.fromEntries(new URLSearchParams(requestBody)),
    {
      client_id: credentials.client_id,
      client_secret: credentials.client_secret,
      refresh_token: credentials.refresh_token,
      grant_type: 'refresh_token'
    },
    'Google OAuth refresh 必须发送官方 form 字段'
  )
  const refreshedSnapshot = freshProvider.getTokenSnapshot()
  assert.ok(refreshedSnapshot)
  assert.equal(refreshedSnapshot.access_token, 'refreshed-access-token')
  assert.equal(refreshedSnapshot.expires_at, '2026-07-18T12:59:30.000Z')

  nowMs = Date.parse('2026-07-18T10:00:00.000Z')
  let malformedRequests = 0
  const malformed = createGeminiGoogleOAuthTokenProvider({
    ...credentials,
    access_token: undefined,
    expires_at: undefined
  }, {
    transport: async () => {
      malformedRequests += 1
      return { statusCode: 200, bodyText: JSON.stringify({ access_token: 'token', expires_in: 0 }), truncated: false }
    },
    now: () => nowMs
  })
  await assert.rejects(malformed.getAccessToken(), /expires_in/, '无效 expires_in 必须拒绝')
  assert.equal(malformedRequests, 1)

  const failing = createGeminiGoogleOAuthTokenProvider({
    ...credentials,
    access_token: undefined,
    expires_at: undefined
  }, {
    transport: async () => ({ statusCode: 401, bodyText: JSON.stringify({ error: `upstream echoed ${credentials.refresh_token} ${credentials.client_secret}` }), truncated: false }),
    now: () => nowMs
  })
  await assert.rejects(
    failing.getAccessToken(),
    (error: unknown) => {
      assert.ok(error instanceof Error)
      assert.match(error.message, /HTTP 401/)
      assert.equal(error.message.includes(credentials.refresh_token!), false, 'refresh token 不得泄露到错误消息')
      assert.equal(error.message.includes(credentials.client_secret!), false, 'client secret 不得泄露到错误消息')
      return true
    }
  )

  assert.throws(
    () => createGeminiGoogleOAuthTokenProvider({
      ...credentials,
      access_token: undefined,
      refresh_token: ''
    }, { transport }),
    /refresh_token/
  )
  assert.throws(
    () => createGeminiGoogleOAuthTokenProvider({
      ...credentials,
      client_secret: ''
    }, { transport }),
    /client_secret/
  )

  const oversized = createGeminiGoogleOAuthTokenProvider({
    ...credentials,
    access_token: undefined,
    expires_at: undefined
  }, {
    transport: async () => ({ statusCode: 200, bodyText: '{', truncated: true }),
    now: () => nowMs
  })
  await assert.rejects(oversized.getAccessToken(), /too large|过大/, '超限 Google token JSON 必须拒绝')
}

await run()
console.log('gemini google oauth token regression passed')
