import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'

const strongSecret = 'http-security-production-guard-32-chars-minimum'
const inspectRuntimeConfigCode = `
import { runtimeConfig } from './src/config/runtime.ts'
import { isCorsOriginAllowed, sessionCookieOptions } from './src/shared/http-security.ts'

const cookie = sessionCookieOptions({ maxAge: 1 })
console.log(JSON.stringify({
  allowedOrigins: runtimeConfig.httpSecurity.cors.allowedOrigins,
  allowAnyOrigin: runtimeConfig.httpSecurity.cors.allowAnyOrigin,
  allowedAdminOrigin: isCorsOriginAllowed('https://admin.example.com'),
  deniedUnknownOrigin: isCorsOriginAllowed('https://evil.example.com'),
  allowedLocalDevOrigin: isCorsOriginAllowed('http://127.0.0.1:5173'),
  cookieSecure: cookie.secure,
  cookieSameSite: cookie.sameSite,
  trustProxy: runtimeConfig.httpSecurity.trustProxy
}))
`

const missingOriginResult = spawnRuntimeInspect({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: strongSecret,
  JUHE_AI_ALLOWED_ORIGINS: ''
})
assert.notEqual(missingOriginResult.status, 0, '生产环境缺少 JUHE_AI_ALLOWED_ORIGINS 时不应允许启动')
assert.match(
  `${missingOriginResult.stderr}\n${missingOriginResult.stdout}`,
  /JUHE_AI_ALLOWED_ORIGINS.*生产环境/,
  '生产 CORS 失败信息应明确指向 JUHE_AI_ALLOWED_ORIGINS'
)

const wildcardOriginResult = spawnRuntimeInspect({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: strongSecret,
  JUHE_AI_ALLOWED_ORIGINS: '*'
})
assert.notEqual(wildcardOriginResult.status, 0, '生产环境不应允许 JUHE_AI_ALLOWED_ORIGINS=*')

const invalidSameSiteResult = spawnRuntimeInspect({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: strongSecret,
  JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
  JUHE_AI_COOKIE_SAME_SITE: 'none',
  JUHE_AI_COOKIE_SECURE: 'false'
})
assert.notEqual(invalidSameSiteResult.status, 0, 'SameSite=None 且 Cookie Secure=false 时不应允许启动')
assert.match(
  `${invalidSameSiteResult.stderr}\n${invalidSameSiteResult.stdout}`,
  /JUHE_AI_COOKIE_SAME_SITE=none.*JUHE_AI_COOKIE_SECURE=true/,
  'Cookie SameSite=None 失败信息应明确要求 Secure'
)

const productionResult = spawnRuntimeInspect({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: strongSecret,
  JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com,https://admin.example.com/'
})
assert.equal(productionResult.status, 0, '生产环境配置强密钥和 Origin 白名单时应允许加载运行配置')
const productionConfig = parseRuntimeInspectOutput(productionResult.stdout)
assert.deepEqual(productionConfig.allowedOrigins, ['https://admin.example.com'], 'Origin 白名单应规范化并去重')
assert.equal(productionConfig.allowAnyOrigin, false, '生产环境不应反射任意 Origin')
assert.equal(productionConfig.allowedAdminOrigin, true, '生产白名单 Origin 应允许跨域凭据请求')
assert.equal(productionConfig.deniedUnknownOrigin, false, '生产未知 Origin 不应被反射放行')
assert.equal(productionConfig.cookieSecure, true, '生产 Cookie 默认必须启用 Secure')
assert.equal(productionConfig.cookieSameSite, 'lax', 'Cookie SameSite 默认应为 lax')

const developmentResult = spawnRuntimeInspect({
  NODE_ENV: 'development',
  JUHE_AI_SECRET: '',
  JUHE_AI_ALLOWED_ORIGINS: '',
  JUHE_AI_COOKIE_SECURE: '',
  JUHE_AI_COOKIE_SAME_SITE: ''
})
assert.equal(developmentResult.status, 0, '开发环境默认安全配置应允许加载')
const developmentConfig = parseRuntimeInspectOutput(developmentResult.stdout)
assert.equal(developmentConfig.allowAnyOrigin, true, '开发环境未配置 Origin 白名单时可继续支持本地跨域联调')
assert.equal(developmentConfig.allowedLocalDevOrigin, true, '开发环境默认应允许本地前端 Origin')
assert.equal(developmentConfig.cookieSecure, false, '开发环境默认 HTTP Cookie 不应启用 Secure')
assert.equal(developmentConfig.cookieSameSite, 'lax', '开发环境 Cookie SameSite 默认应为 lax')

console.log('HTTP 安全生产保护回归通过：生产 CORS 白名单和 Cookie Secure 默认已收口')

function spawnRuntimeInspect(env: Record<string, string>) {
  return spawnSync(process.execPath, ['--import', 'tsx', '-e', inspectRuntimeConfigCode], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_SECRET: '',
      JUHE_AI_ALLOWED_ORIGINS: '',
      JUHE_AI_COOKIE_SECURE: '',
      JUHE_AI_COOKIE_SAME_SITE: '',
      JUHE_AI_TRUST_PROXY: '',
      ...env
    },
    encoding: 'utf8'
  })
}

function parseRuntimeInspectOutput(stdout: string) {
  const line = stdout.trim().split(/\r?\n/).find((item) => item.startsWith('{') && item.endsWith('}'))
  assert.ok(line, `未找到运行时配置 JSON 输出：${stdout}`)
  return JSON.parse(line) as {
    allowedOrigins: string[]
    allowAnyOrigin: boolean
    allowedAdminOrigin: boolean
    deniedUnknownOrigin: boolean
    allowedLocalDevOrigin: boolean
    cookieSecure: boolean
    cookieSameSite: string
    trustProxy: boolean | number
  }
}
