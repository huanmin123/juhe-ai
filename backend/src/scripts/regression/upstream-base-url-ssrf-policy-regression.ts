import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { closeGatewayUpstreamAgentsForTest, requestUpstream } from '../../modules/gateway/openai-gateway-upstream.js'
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
  ['http://api.openai.com/v1', /生产地址只允许 https/],
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

  runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = ['127.0.0.1']
  assert.doesNotThrow(
    () => normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ssrf-policy', base_url: 'http://127.0.0.1:9/v1' }),
    '显式 allowlist 应允许本地回归 mock 上游地址'
  )
  runtimeConfig.upstreamUrlSecurity.privateBaseUrlAllowlist = ['localhost']
  await assert.doesNotReject(
    () => prepareSafeUpstreamRequestUrl('http://localhost:9/v1'),
    '显式 allowlist 应同时允许主机名保存和出站前 DNS 兜底'
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
    JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST: '127.0.0.1'
  })
  assert.notEqual(productionAllowlistResult.status, 0, '生产环境不应允许配置私网上游 allowlist')

  console.log('上游 Base URL SSRF 策略回归通过：保存层、生产配置和网关出站兜底均拒绝私网地址')
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
