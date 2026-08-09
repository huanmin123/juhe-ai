import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const source = readFileSync(resolve(root, 'docker', 'compose.yml'), 'utf8').replaceAll('\r\n', '\n')
const runtimeLogIndexer = serviceBlock('runtime-log-indexer')
const tableMonitor = serviceBlock('table-monitor')

assert.match(runtimeLogIndexer, /JUHE_AI_TABLE_MONITOR_DATABASE_PATH:/u, 'F1 Docker sidecar must receive the F2 database path')
assert.match(runtimeLogIndexer, /juhe-ai-table-monitor-data:\/app\/backend\/table-monitor-data:ro/u, 'F1 Docker sidecar must inspect the F2 volume read-only')
for (const name of [
  'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH',
  'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH',
  'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
]) {
  assert.match(runtimeLogIndexer, new RegExp(`${name}:`, 'u'), `F1 Docker sidecar must receive ${name}`)
}

assert.match(tableMonitor, /JUHE_AI_RUNTIME_LOG_DATABASE_PATH:/u, 'F2 Docker sidecar must receive the F1 database path')
assert.match(tableMonitor, /juhe-ai-runtime-log-data:\/app\/backend\/runtime-log-data:ro/u, 'F2 Docker sidecar must inspect the F1 volume read-only')

console.log('Docker Go sidecar SQLite isolation regression passed')

function serviceBlock(name) {
  const header = `  ${name}:\n`
  const start = source.indexOf(header)
  assert.notEqual(start, -1, `missing Compose service: ${name}`)
  const remaining = source.slice(start)
  const body = remaining.slice(header.length)
  const next = body.search(/^  [a-zA-Z0-9_-]+:\n/mu)
  return next === -1 ? remaining : remaining.slice(0, header.length + next)
}
