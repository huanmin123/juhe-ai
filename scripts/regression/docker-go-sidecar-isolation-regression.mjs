import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const standaloneSource = readFileSync(resolve(root, 'docker', 'compose.yml'), 'utf8').replaceAll('\r\n', '\n')
const performanceSource = readFileSync(resolve(root, 'docker', 'compose.performance.yml'), 'utf8').replaceAll('\r\n', '\n')
const auditWriterDockerfile = readFileSync(resolve(root, 'docker', 'Dockerfile.audit-log-writer'), 'utf8').replaceAll('\r\n', '\n')
const runtimeLogIndexer = serviceBlock(standaloneSource, 'runtime-log-indexer')
const tableMonitor = serviceBlock(standaloneSource, 'table-monitor')
const standaloneNode = serviceBlock(standaloneSource, 'juhe-ai')
const standaloneAuditWriter = serviceBlock(standaloneSource, 'audit-log-writer')
const performanceNode = serviceBlock(performanceSource, 'juhe-ai')
const performanceAuditWriter = serviceBlock(performanceSource, 'audit-log-writer')

assertDatabasePathMount(runtimeLogIndexer, {
  environment: 'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  mountTarget: '/app/backend/runtime-log-data',
  readOnly: false,
  label: 'F1 自身 SQLite 输出库'
})
assertDatabasePathMount(runtimeLogIndexer, {
  environment: 'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
  mountTarget: '/app/backend/table-monitor-data',
  readOnly: true,
  label: 'F1 读取 F2 SQLite 输出库'
})
for (const name of [
  'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH',
  'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH',
  'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
]) {
  assert.match(runtimeLogIndexer, new RegExp(`${name}:`, 'u'), `F1 Docker sidecar must receive ${name}`)
}

assertDatabasePathMount(tableMonitor, {
  environment: 'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
  mountTarget: '/app/backend/table-monitor-data',
  readOnly: false,
  label: 'F2 自身 SQLite 输出库'
})
assertDatabasePathMount(tableMonitor, {
  environment: 'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  mountTarget: '/app/backend/runtime-log-data',
  readOnly: true,
  label: 'F2 读取 F1 SQLite 输出库'
})

assertAuditWriterContract(standaloneNode, standaloneAuditWriter, 'standalone')
assertAuditWriterContract(performanceNode, performanceAuditWriter, 'performance')
assert.match(auditWriterDockerfile, /__aiinternal__\/health/u, 'F3 healthcheck binary must probe the loopback input listener')

console.log('Docker Go sidecar deployment isolation regression passed')

function serviceBlock(source, name) {
  const header = `  ${name}:\n`
  const start = source.indexOf(header)
  assert.notEqual(start, -1, `missing Compose service: ${name}`)
  const remaining = source.slice(start)
  const body = remaining.slice(header.length)
  const next = body.search(/^  [a-zA-Z0-9_-]+:\n/mu)
  return next === -1 ? remaining : remaining.slice(0, header.length + next)
}

function assertAuditWriterContract(node, writer, mode) {
  assert.match(writer, /dockerfile:\s+docker\/Dockerfile\.audit-log-writer/u, `${mode} Compose must build F3 from its dedicated Dockerfile`)
  assert.match(writer, /^\s+network_mode:\s+service:juhe-ai\s*$/mu, `${mode} F3 must share Node's loopback network namespace`)
  assert.match(writer, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:/u, `${mode} F3 must require a stable instance ID`)
  assert.match(writer, /JUHE_AI_AUDIT_LOG_INPUT_SECRET:/u, `${mode} F3 must receive the explicit input secret`)
  assert.match(writer, /JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY:/u, `${mode} F3 must own a blob directory`)
  assert.match(writer, /juhe-ai-audit-log-data:\/app\/backend\/audit-log-data\s*$/mu, `${mode} F3 must write its dedicated volume`)
  assert.match(writer, /audit-log-healthcheck/u, `${mode} F3 healthcheck must use the HTTP probe binary`)
  assert.match(node, /JUHE_AI_AUDIT_LOG_INPUT_URL:/u, `${mode} Node must send audit input to F3`)
  assert.match(node, /JUHE_AI_AUDIT_LOG_INPUT_SECRET:/u, `${mode} Node must receive the explicit F3 input secret`)
  assert.match(node, /juhe-ai-audit-log-data:\/app\/backend\/audit-log-data:ro/u, `${mode} Node must mount F3 artifacts read-only`)
}

function assertDatabasePathMount(block, { environment, mountTarget, readOnly, label }) {
  const configuredPath = defaultEnvironmentPath(block, environment)
  assert.equal(
    configuredPath === mountTarget || configuredPath.startsWith(`${mountTarget}/`),
    true,
    `${label} 的 ${environment} 默认路径必须位于 ${mountTarget}`
  )
  const expectedSuffix = readOnly ? ':ro' : ''
  assert.match(
    block,
    new RegExp(`^\\s+- [^\\s:]+:${escapeRegExp(mountTarget)}${escapeRegExp(expectedSuffix)}\\s*$`, 'mu'),
    `${label} 的 ${environment} 必须由同一 service 中${readOnly ? '只读' : '可写'}挂载的 ${mountTarget} 提供`
  )
}

function defaultEnvironmentPath(block, name) {
  const match = block.match(new RegExp(`^\\s+${escapeRegExp(name)}:\\s*\\$\\{[^}:]+:-([^}]+)\\}\\s*$`, 'mu'))
  assert(match, `${name} 必须以显式容器内默认 SQLite 路径配置`)
  return match[1]
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&')
}
