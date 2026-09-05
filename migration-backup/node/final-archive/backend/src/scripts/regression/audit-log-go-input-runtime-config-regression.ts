import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import { resolve } from 'node:path'

const backendRoot = resolve(import.meta.dirname, '../../..')
const runtimeProbe = "import { runtimeConfig } from './src/config/runtime.js'; process.stdout.write(JSON.stringify({ secret: runtimeConfig.auditLogInputSecret, timeoutMs: runtimeConfig.auditLogInputTimeoutMs }))"

const explicit = loadRuntime({
  NODE_ENV: 'test',
  JUHE_AI_SECRET: 'f3-runtime-config-business-secret',
  JUHE_AI_AUDIT_LOG_INPUT_URL: 'http://127.0.0.1:3303',
  JUHE_AI_AUDIT_LOG_INPUT_SECRET: 'f3-runtime-config-input-secret'
})
assert.equal(explicit.status, 0, `独立 F3 输入密钥应允许加载：${explicit.output}`)
assert.deepEqual(
  JSON.parse(explicit.output),
  { secret: 'f3-runtime-config-input-secret', timeoutMs: 7_000 },
  'Node 必须读取 F3 显式输入密钥与独立输入超时，而非 JUHE_AI_SECRET'
)

const configuredTimeout = loadRuntime({
  NODE_ENV: 'test',
  JUHE_AI_SECRET: 'f3-runtime-config-business-secret',
  JUHE_AI_AUDIT_LOG_INPUT_URL: 'http://127.0.0.1:3303',
  JUHE_AI_AUDIT_LOG_INPUT_SECRET: 'f3-runtime-config-input-secret',
  JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS: '9000'
})
assert.equal(configuredTimeout.status, 0, `显式 F3 输入超时应允许加载：${configuredTimeout.output}`)
assert.equal(JSON.parse(configuredTimeout.output).timeoutMs, 9_000, 'Node 必须使用显式 F3 输入超时')

const invalidTimeout = loadRuntime({
  NODE_ENV: 'test',
  JUHE_AI_SECRET: 'f3-runtime-config-business-secret',
  JUHE_AI_AUDIT_LOG_INPUT_URL: 'http://127.0.0.1:3303',
  JUHE_AI_AUDIT_LOG_INPUT_SECRET: 'f3-runtime-config-input-secret',
  JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS: '999'
})
assert.notEqual(invalidTimeout.status, 0, '过短 F3 输入超时必须阻断 Node 启动')
assert.match(invalidTimeout.output, /JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS/u)

for (const testCase of [
  { name: 'missing', value: undefined },
  { name: 'blank', value: '   ' }
]) {
  const result = loadRuntime({
    NODE_ENV: 'test',
    JUHE_AI_SECRET: 'f3-runtime-config-business-secret',
    JUHE_AI_AUDIT_LOG_INPUT_URL: 'http://127.0.0.1:3303',
    ...(testCase.value === undefined ? {} : { JUHE_AI_AUDIT_LOG_INPUT_SECRET: testCase.value })
  })
  assert.notEqual(result.status, 0, `${testCase.name} F3 输入密钥必须阻断 Node 启动`)
  assert.match(result.output, /JUHE_AI_AUDIT_LOG_INPUT_SECRET/u)
}

const invalidProductionSecret = loadRuntime({
  NODE_ENV: 'production',
  JUHE_AI_SECRET: 'f3-runtime-config-business-secret-32-characters',
  JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
  JUHE_AI_AUDIT_LOG_INPUT_URL: 'http://127.0.0.1:3303',
  JUHE_AI_AUDIT_LOG_INPUT_SECRET: 'short'
})
assert.notEqual(invalidProductionSecret.status, 0, '生产环境过短 F3 输入密钥必须阻断 Node 启动')
assert.match(invalidProductionSecret.output, /JUHE_AI_AUDIT_LOG_INPUT_SECRET/u)

console.log('F3 input runtime config regression passed: explicit independent secret works; missing, blank, and invalid production secrets fail.')

function loadRuntime(overrides: Record<string, string>): { status: number | null; output: string } {
  const env: NodeJS.ProcessEnv = { ...process.env, JUHE_AI_DISABLE_BASE_ENV: 'true' }
  for (const name of [
    'JUHE_AI_ENV_FILE',
    'JUHE_AI_SECRET',
    'JUHE_AI_ALLOWED_ORIGINS',
    'JUHE_AI_AUDIT_LOG_INPUT_URL',
    'JUHE_AI_AUDIT_LOG_INPUT_SECRET',
    'JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS',
    'NODE_ENV'
  ]) {
    delete env[name]
  }
  Object.assign(env, overrides)
  const result = spawnSync(process.execPath, ['--import', 'tsx', '-e', runtimeProbe], {
    cwd: backendRoot,
    encoding: 'utf8',
    env
  })
  return {
    status: result.status,
    output: `${result.stdout ?? ''}${result.stderr ?? ''}`.trim()
  }
}
