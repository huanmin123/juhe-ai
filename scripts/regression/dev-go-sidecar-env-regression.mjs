import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const scriptsRoot = join(repoRoot, 'scripts')
const sourcePath = join(scriptsRoot, 'dev.mjs')
const fixtureRoot = mkdtempSync(join(repoRoot, '.dev-go-sidecar-env-regression-'))
const modulePath = join(scriptsRoot, `.dev-go-sidecar-env-regression-${process.pid}-${Date.now()}.mjs`)
const environmentKeys = [
  'JUHE_AI_DATABASE_DRIVER',
  'JUHE_AI_RUNTIME_LOG_STORE',
  'JUHE_AI_TABLE_MONITOR_STORE',
  'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
  'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH',
  'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH',
  'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
]
const previousEnvironment = new Map(environmentKeys.map((key) => [key, process.env[key]]))

try {
  for (const key of environmentKeys) delete process.env[key]
  const backendRoot = join(fixtureRoot, 'backend')
  mkdirSync(backendRoot, { recursive: true })
  writeFileSync(join(backendRoot, '.env'), [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3',
    'JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3',
    'JUHE_AI_DATASET_DATABASE_PATH=./data/juhe-ai-dataset.sqlite3',
    'JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/juhe-ai-usage-catalog.sqlite3',
    'JUHE_AI_STATS_DATABASE_PATH=./data/juhe-ai-stats.sqlite3',
    'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=./data/codex-context/state-shards'
  ].join('\n'))

  writeFileSync(modulePath, buildTestableModule(readFileSync(sourcePath, 'utf8'), fixtureRoot))
  const module = await import(`${pathToFileURL(modulePath).href}?v=${Date.now()}`)
  const runtimeLogEnv = module.resolveRuntimeLogIndexerEnv()
  const tableMonitorEnv = module.resolveTableMonitorEnv()

  assert.equal(runtimeLogEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(backendRoot, 'data', 'table-monitor.sqlite3'))
  assert.equal(runtimeLogEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, join(backendRoot, 'data', 'juhe-ai-usage-catalog.sqlite3'))
  assert.equal(runtimeLogEnv.JUHE_AI_STATS_DATABASE_PATH, join(backendRoot, 'data', 'juhe-ai-stats.sqlite3'))
  assert.equal(runtimeLogEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, join(backendRoot, 'data', 'codex-context', 'state-shards'))
  assert.equal(tableMonitorEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(backendRoot, 'data', 'runtime-log.sqlite3'))
  assert.equal(runtimeLogEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(backendRoot, 'data', 'runtime-log.sqlite3'))
  assert.equal(tableMonitorEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(backendRoot, 'data', 'table-monitor.sqlite3'))
} finally {
  for (const [key, value] of previousEnvironment) {
    if (value === undefined) delete process.env[key]
    else process.env[key] = value
  }
  rmSync(modulePath, { force: true })
  rmSync(fixtureRoot, { recursive: true, force: true })
}

console.log('dev Go sidecar environment regression passed')

function buildTestableModule(source, root) {
  const startup = /try \{\r?\n  console\.log\('\[dev\] starting backend\.\.\.'\)[\s\S]*?\r?\nawait new Promise\(\(\) => undefined\)\r?\n\r?\n/u
  const withoutStartup = source.replace(startup, '')
  assert.notEqual(withoutStartup, source, 'could not remove dev process startup block for resolver regression')
  const rootDeclaration = "const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')"
  assert.match(withoutStartup, new RegExp(rootDeclaration.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&'), 'u'))
  const fixtureSource = withoutStartup.replace(rootDeclaration, `const root = ${JSON.stringify(root)}`)
  return `${fixtureSource}\nexport { resolveRuntimeLogIndexerEnv, resolveTableMonitorEnv }\n`
}
