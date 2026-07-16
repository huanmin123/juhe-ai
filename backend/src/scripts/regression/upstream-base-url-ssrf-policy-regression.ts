import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { closeGatewayUpstreamAgentsForTest, requestUpstream } from '../../modules/gateway/upstream/request.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/repositories.js'
import { prepareSafeUpstreamRequestUrl } from '../../shared/upstream-url-policy.js'

const previousPolicy = { ...runtimeConfig.upstreamUrlSecurity }
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = false
runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = []

const unsafeBaseUrls = [
  'http://127.0.0.1:9/v1',
  'http://localhost:9/v1',
  'http://169.254.169.254/latest/meta-data',
  'http://10.0.0.1/v1',
  'http://172.16.0.1/v1',
  'http://192.168.1.1/v1',
  'http://[::1]/v1',
  'http://[::ffff:127.0.0.1]/v1',
  'http://[::ffff:a00:1]/v1',
  'http://[::7f00:1]/v1',
  'http://[fc00::1]/v1',
  'http://[fe80::1]/v1',
  'http://[2001:db8::1]/v1'
]

const invalidShapeBaseUrls = [
  ['https:///api.openai.com/v1', /协议后只能保留两个斜杠/],
  ['https:////api.openai.com/v1', /协议后只能保留两个斜杠/],
  ['https:\\\\api.openai.com\\v1', /反斜杠|完整绝对地址/],
  ['ftp://api.openai.com/v1', /只允许/],
  ['https://user:pass@api.openai.com/v1', /用户名或密码/],
  ['https://api.openai.com//v1', /连续斜杠/],
  ['https://api.openai.com/v1?x=1', /查询参数/],
  ['https://api.openai.com/v1#hash', /片段标识/],
  ['https://api.openai.com/v1/%2f', /编码后的斜杠/],
  ['https://api.openai.com/v1/%5c', /编码后的斜杠/],
  ['https://api.openai.com/v1/.', /\. 或 \.\./],
  ['https://api.openai.com/v1/%2e', /\. 或 \.\./],
  ['https://api.openai.com/v1/responses', /\/v1 后的具体接口路径/],
  ['https://api.openai.com/v1/chat/completions', /\/v1 后的具体接口路径/],
  ['https://api.openai.com/responses', /不能填写具体接口路径/],
  ['https://example.com/openai/responses', /不能填写具体接口路径/]
] as const

try {
  for (const [baseUrl, expectedMessage] of invalidShapeBaseUrls) {
    assert.throws(
      () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-url-shape-policy', base_url: baseUrl }),
      expectedMessage,
      `严格路径策略应拒绝无效上游 Base URL：${baseUrl}`
    )
  }

  for (const baseUrl of unsafeBaseUrls) {
    assert.throws(
      () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: baseUrl }),
      /上游 Base URL/,
      `默认策略应拒绝不安全上游 Base URL：${baseUrl}`
    )
  }

  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'https://api.openai.com/v1' }),
    '官方 HTTPS 上游地址应允许保存'
  )
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'http://api.openai.com/v1' }),
    '公网 HTTP 上游域名应允许保存'
  )
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-url-shape-policy', base_url: 'https://api.openai.com' }),
    'OpenAI 兼容账号应允许保存服务根地址'
  )
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-url-shape-policy', base_url: 'https://example.com/openai/v1' }),
    'OpenAI 兼容账号应允许保存带自定义前缀的 /v1 地址'
  )
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-url-shape-policy', base_url: 'https://example.com/openai' }),
    'OpenAI 兼容账号应允许保存带自定义前缀的服务根地址'
  )
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'https://[2606:4700:4700::1111]/v1' }),
    '公网 IPv6 上游地址应允许保存'
  )
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'http://103.236.84.213:48222/v1' }),
    '公网 HTTP IP 上游地址应允许保存'
  )

  runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = ['http://127.0.0.1:9']
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'http://127.0.0.1:9/v1' }),
    '显式 exact-origin allowlist 应允许同协议、同 IP 和同有效端口的私网上游地址'
  )
  assert.throws(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'http://127.0.0.1:10/v1' }),
    /上游 Base URL/,
    'exact-origin allowlist 不能放行同 IP 的其他端口'
  )
  assert.throws(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'https://127.0.0.1:9/v1' }),
    /上游 Base URL/,
    'exact-origin allowlist 不能放行同 IP 和端口的其他协议'
  )
  runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = ['http://127.0.0.1:80']
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'http://127.0.0.1/v1' }),
    'exact-origin allowlist 应按有效默认端口匹配'
  )
  runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = []
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('oauth', { refresh_token: 'rt-ssrf-policy', base_url: 'http://127.0.0.1:9/v1' }),
    '显式测试开关应允许 OAuth 本地 mock 上游地址'
  )

  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = false
  const upstream = http.createServer((_req, res) => {
    assert.fail('SSRF 兜底应在发起本地上游请求前拒绝，不应命中 mock server')
    res.end('unexpected')
  })
  await new Promise<void>((resolve) => upstream.listen(0, '127.0.0.1', resolve))
  try {
    const address = upstream.address()
    assert(address && typeof address === 'object', '本地 mock server 启动失败')
    await assert.rejects(
      () => requestUpstream(`http://127.0.0.1:${address.port}/v1/responses`, { method: 'GET', headers: new Headers() }),
      /上游 Base URL/,
      'requestUpstream 应对旧数据或绕过路径里的本机地址做最终兜底'
    )
  } finally {
    await new Promise<void>((resolve) => upstream.close(() => resolve()))
    closeGatewayUpstreamAgentsForTest()
  }

  let redirectTargetHits = 0
  const redirectTarget = http.createServer((_req, res) => {
    redirectTargetHits += 1
    res.end('redirected')
  })
  await new Promise<void>((resolve) => redirectTarget.listen(0, '127.0.0.1', resolve))
  const redirectTargetAddress = redirectTarget.address()
  assert(redirectTargetAddress && typeof redirectTargetAddress === 'object', '重定向目标 mock server 启动失败')
  const redirectSource = http.createServer((_req, res) => {
    res.writeHead(302, { location: `http://127.0.0.1:${redirectTargetAddress.port}/escaped` })
    res.end()
  })
  await new Promise<void>((resolve) => redirectSource.listen(0, '127.0.0.1', resolve))
  try {
    const redirectSourceAddress = redirectSource.address()
    assert(redirectSourceAddress && typeof redirectSourceAddress === 'object', '重定向来源 mock server 启动失败')
    runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = [`http://127.0.0.1:${redirectSourceAddress.port}`]
    const response = await requestUpstream(`http://127.0.0.1:${redirectSourceAddress.port}/v1/responses`, {
      method: 'GET',
      headers: new Headers(),
      transport: 'fetch'
    })
    assert.equal(response.status, 302, 'fetch 上游传输必须返回原始重定向响应，不得自动跟随')
    assert.equal(redirectTargetHits, 0, 'allowlisted origin 的重定向不得逃逸到其他私网服务')
  } finally {
    await new Promise<void>((resolve) => redirectSource.close(() => resolve()))
    await new Promise<void>((resolve) => redirectTarget.close(() => resolve()))
  }

  const productionAllowPrivateResult = spawnRuntimeImport({
    NODE_ENV: 'production',
    JUHE_AI_SECRET: 'upstream-base-url-ssrf-policy-32-chars-minimum',
    JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
    JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true'
  })
  assert.notEqual(productionAllowPrivateResult.status, 0, '生产环境不应允许启用私网上游放行开关')
  assert.match(
    `${productionAllowPrivateResult.stderr}\n${productionAllowPrivateResult.stdout}`,
    /JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS.*生产环境不能启用/,
    '生产私网上游开关失败信息应明确指向 JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS'
  )

  const productionAllowlistResult = spawnRuntimeImport({
    NODE_ENV: 'production',
    JUHE_AI_SECRET: 'upstream-base-url-ssrf-policy-32-chars-minimum',
    JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
    JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST: 'http://192.168.40.199:8317'
  })
  assert.equal(
    productionAllowlistResult.status,
    0,
    `生产环境应允许显式 exact-origin 私网上游 allowlist：${productionAllowlistResult.stderr}`
  )

  for (const invalidAllowlist of [
    '192.168.40.199',
    'http://private-upstream.example:8317',
    'http://192.168.40.199:8317/v1',
    'http://user:pass@192.168.40.199:8317',
    'ftp://192.168.40.199:8317'
  ]) {
    const invalidResult = spawnRuntimeImport({
      NODE_ENV: 'production',
      JUHE_AI_SECRET: 'upstream-base-url-ssrf-policy-32-chars-minimum',
      JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
      JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST: invalidAllowlist
    })
    assert.notEqual(invalidResult.status, 0, `生产 exact-origin allowlist 应拒绝无效配置：${invalidAllowlist}`)
  }

  console.log('上游 Base URL SSRF 策略回归通过：私网 exact-origin 放行、生产显式配置和出站重定向边界均已锁定')
} finally {
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = previousPolicy.allowPrivateBaseUrls
  runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = previousPolicy.privateBaseUrlAllowlist
}

function spawnRuntimeImport(env: Record<string, string>) {
  return spawnSync(process.execPath, ['--import', 'tsx', '-e', "import('./src/config/runtime.ts').then(() => console.log('runtime-ok'))"], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_SECRET: '',
      JUHE_AI_ALLOWED_ORIGINS: '',
      JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: '',
      JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST: '',
      ...env
    },
    encoding: 'utf8'
  })
}
