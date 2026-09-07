import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'

const inspectRuntimeConfigCode = `
import { runtimeConfig } from './src/config/runtime.ts'
console.log(JSON.stringify({
  allowedOrigins: runtimeConfig.httpSecurity.cors.allowedOrigins,
  allowAnyOrigin: runtimeConfig.httpSecurity.cors.allowAnyOrigin,
  cookieSecure: runtimeConfig.httpSecurity.cookie.secure,
  cookieSameSite: runtimeConfig.httpSecurity.cookie.sameSite,
  computerAdapterEndpoint: runtimeConfig.computerAdapter.endpoint
}))
`

const developmentCookieResult = spawnRuntime({
  NODE_ENV: 'development',
  JUHE_AI_ALLOWED_ORIGINS: '*',
  JUHE_AI_COOKIE_SAME_SITE: 'none',
  JUHE_AI_COOKIE_SECURE: 'false'
})
assert.equal(developmentCookieResult.status, 0, '开发环境 Cookie 安全策略冲突不应阻止启动')
assert.deepEqual(parseOutput(developmentCookieResult.stdout), {
  allowedOrigins: [],
  allowAnyOrigin: true,
  cookieSecure: false,
  cookieSameSite: 'none'
})

const developmentInvalidOriginResult = spawnRuntime({
  NODE_ENV: 'development',
  JUHE_AI_ALLOWED_ORIGINS: '*,not-a-url'
})
assert.notEqual(developmentInvalidOriginResult.status, 0, '开发环境通配 Origin 不能掩盖同一配置中的格式错误')
assert.match(`${developmentInvalidOriginResult.stderr}\n${developmentInvalidOriginResult.stdout}`, /JUHE_AI_ALLOWED_ORIGINS.*无效 Origin/)

const productionCookieResult = spawnRuntime({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: 'runtime-development-security-regression-secret-32-chars',
  JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
  JUHE_AI_COOKIE_SAME_SITE: 'none',
  JUHE_AI_COOKIE_SECURE: 'false'
})
assert.notEqual(productionCookieResult.status, 0, '生产环境 Cookie 安全策略冲突必须阻止启动')
assert.match(`${productionCookieResult.stderr}\n${productionCookieResult.stdout}`, /JUHE_AI_COOKIE_SAME_SITE=none/)

const developmentComputerResult = spawnRuntime({
  NODE_ENV: 'development',
  JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED: 'true',
  JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT: 'http://192.168.40.199:8317/adapter?token=development'
})
assert.equal(developmentComputerResult.status, 0, '开发环境远程 HTTP adapter 不应因安全策略阻止启动')
assert.equal(parseOutput(developmentComputerResult.stdout).computerAdapterEndpoint, 'http://192.168.40.199:8317/adapter?token=development')

const productionComputerResult = spawnRuntime({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: 'runtime-development-security-regression-secret-32-chars',
  JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
  JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED: 'true',
  JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT: 'http://192.168.40.199:8317'
})
assert.notEqual(productionComputerResult.status, 0, '生产环境远程 HTTP adapter 必须阻止启动')
assert.match(`${productionComputerResult.stderr}\n${productionComputerResult.stdout}`, /COMPUTER_BROWSER_ADAPTER_ENDPOINT.*loopback/)

for (const endpoint of [
  'https://adapter.example.com/browser?token=production',
  'https://adapter.example.com/browser#production'
]) {
  const result = spawnRuntime({
    NODE_ENV: 'production',
    JUHE_AI_SECRET: 'runtime-development-security-regression-secret-32-chars',
    JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
    JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED: 'true',
    JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT: endpoint
  })
  assert.notEqual(result.status, 0, '生产环境 adapter 查询参数和片段必须阻止启动')
  assert.match(`${result.stderr}\n${result.stdout}`, /COMPUTER_BROWSER_ADAPTER_ENDPOINT.*查询参数或片段/)
}

for (const [endpoint, pattern] of [
  ['', /COMPUTER_BROWSER_ADAPTER_ENDPOINT.*必须配置/],
  ['not-a-url', /COMPUTER_BROWSER_ADAPTER_ENDPOINT.*有效 URL/],
  ['ftp://127.0.0.1/adapter', /COMPUTER_BROWSER_ADAPTER_ENDPOINT.*http 或 https/],
  ['http://developer:secret@127.0.0.1/adapter', /COMPUTER_BROWSER_ADAPTER_ENDPOINT.*用户名密码/]
] as const) {
  const result = spawnRuntime({
    NODE_ENV: 'development',
    JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED: 'true',
    JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT: endpoint
  })
  assert.notEqual(result.status, 0, '开发环境仍必须拒绝 adapter 功能性配置错误')
  assert.match(`${result.stderr}\n${result.stdout}`, pattern)
}

console.log('开发环境安全启动策略回归通过：非生产允许安全策略冲突，生产继续 fail-fast')

function spawnRuntime(env: Record<string, string>) {
  const childEnv = { ...process.env }
  for (const key of Object.keys(childEnv)) {
    if (key.startsWith('JUHE_AI_') || key === 'NODE_ENV') delete childEnv[key]
  }
  return spawnSync(process.execPath, ['--import', 'tsx', '--input-type=module', '-e', inspectRuntimeConfigCode], {
    cwd: process.cwd(),
    env: { ...childEnv, ...env },
    encoding: 'utf8'
  })
}

function parseOutput(stdout: string): {
  allowedOrigins: string[]
  allowAnyOrigin: boolean
  cookieSecure: boolean
  cookieSameSite: string
  computerAdapterEndpoint?: string
} {
  const line = stdout.trim().split(/\r?\n/).find((item) => item.startsWith('{') && item.endsWith('}'))
  assert.ok(line, `未找到运行时配置 JSON 输出：${stdout}`)
  return JSON.parse(line) as {
    allowedOrigins: string[]
    allowAnyOrigin: boolean
    cookieSecure: boolean
    cookieSameSite: string
    computerAdapterEndpoint?: string
  }
}
