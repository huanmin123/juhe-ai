import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const backendRoot = resolve(import.meta.dirname, '../../..')
const runtimeProbe = "import { runtimeConfig } from './src/config/runtime.js'; process.stdout.write(JSON.stringify(runtimeConfig.accountHealthJobs))"
const backgroundJobsSource = readFileSync(resolve(backendRoot, 'src/modules/background/background-jobs.ts'), 'utf8')

assertLoads('默认 Node owner 不应要求 J1 配置', {}, (config) => {
  assert.equal(config.owner, 'node')
  assert.equal(config.inputPublisherEnabled, false)
  assert.equal(config.projectionEnabled, false)
  assert.equal(config.sourceFenceConsumerEnabled, false)
})

assertRejects('Go owner 缺失签名输入配置必须失败', {
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go'
}, /JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY/u)

assertRejects('Node owner 不得开启 J1 input publisher', {
  JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_ENABLED: 'true'
}, /JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_ENABLED/u)

assertLoads('Go owner 配齐输入协议后允许加载', {
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: 'j1-inputs',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: 'j1-runtime-config-signing-key'
}, (config) => {
  assert.equal(config.owner, 'go')
  assert.equal(config.inputDirectory, 'j1-inputs')
})

assertRejects('J1 projector 缺少 jobs outcome SQLite 路径必须失败', {
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: 'j1-inputs',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: 'j1-runtime-config-signing-key',
  JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_ENABLED: 'true'
}, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH/u)

assertLoads('J1 projector 配置 jobs outcome SQLite 路径后允许加载', {
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: 'j1-inputs',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: 'j1-runtime-config-signing-key',
  JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_ENABLED: 'true',
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH: 'j1-outcomes.sqlite3'
}, (config) => {
  assert.equal(config.projectionEnabled, true)
  assert.equal(config.outcomeSqlitePath, 'j1-outcomes.sqlite3')
})

assert.match(
  backgroundJobsSource,
  /function scheduleCooldownAccountRetestJob\(\): void \{[\s\S]*?if \(runtimeConfig\.accountHealthJobs\.owner !== 'node'\)[\s\S]*?return[\s\S]*?runIfNodeOwnsWorkerJob/u,
  'J1 Go owner 必须同时阻断 Node account-health 与 account cooldown 两个 scheduler，不能只停周期 health scan'
)

console.log('account-health-jobs-runtime-config-regression passed')

function assertLoads(label: string, overrides: Record<string, string>, verify: (config: Record<string, unknown>) => void): void {
  const result = loadRuntime(overrides)
  assert.equal(result.status, 0, `${label}: ${result.output}`)
  verify(JSON.parse(result.output) as Record<string, unknown>)
}

function assertRejects(label: string, overrides: Record<string, string>, expected: RegExp): void {
  const result = loadRuntime(overrides)
  assert.notEqual(result.status, 0, label)
  assert.match(result.output, expected, `${label}: ${result.output}`)
}

function loadRuntime(overrides: Record<string, string>): { status: number | null, output: string } {
  const env: NodeJS.ProcessEnv = { ...process.env, JUHE_AI_DISABLE_BASE_ENV: 'true', NODE_ENV: 'test' }
  for (const name of [
    'JUHE_AI_ENV_FILE',
    'JUHE_AI_RUNTIME_MODE',
    'JUHE_AI_DATABASE_DRIVER',
    'JUHE_AI_CACHE_DRIVER',
    'JUHE_AI_RUNTIME_STATE_DRIVER',
    'JUHE_AI_QUEUE_DRIVER',
    'JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER',
    'JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY',
    'JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY',
    'JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_ENABLED',
    'JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_ENABLED',
    'JUHE_AI_ACCOUNT_HEALTH_JOBS_SOURCE_FENCE_CONSUMER_ENABLED',
    'JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH',
    'JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL'
  ]) {
    delete env[name]
  }
  Object.assign(env, {
    JUHE_AI_RUNTIME_MODE: 'standalone',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_CACHE_DRIVER: 'memory',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
    JUHE_AI_QUEUE_DRIVER: 'memory'
  }, overrides)
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
