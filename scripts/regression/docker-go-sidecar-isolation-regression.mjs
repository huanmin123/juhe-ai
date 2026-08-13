import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const standaloneSource = readFileSync(resolve(root, 'docker', 'compose.yml'), 'utf8').replaceAll('\r\n', '\n')
const performanceSource = readFileSync(resolve(root, 'docker', 'compose.performance.yml'), 'utf8').replaceAll('\r\n', '\n')
const sidecarDockerfile = readFileSync(resolve(root, 'docker', 'Dockerfile.go-sidecar'), 'utf8').replaceAll('\r\n', '\n')
const standaloneNode = serviceBlock(standaloneSource, 'juhe-ai')
const standaloneSidecar = serviceBlock(standaloneSource, 'go-sidecar')
const performanceNode = serviceBlock(performanceSource, 'juhe-ai')
const performanceSidecar = serviceBlock(performanceSource, 'go-sidecar')

assertSingleSidecarContract(standaloneNode, standaloneSidecar, 'standalone')
assertSingleSidecarContract(performanceNode, performanceSidecar, 'performance')
assert.match(performanceNode, /JUHE_AI_INSTANCE_ID:\s*\$\{JUHE_AI_INSTANCE_ID:\?JUHE_AI_INSTANCE_ID is required\}/u, 'performance Compose must fail closed without a stable Node instance ID')
assert.match(sidecarDockerfile, /cmd\/juhe-ai-go-sidecar/u, 'Dockerfile must build the only Go sidecar command')
assert.match(sidecarDockerfile, /__aiinternal__\/health/u, 'sidecar healthcheck binary must probe the loopback input listener')

for (const source of [standaloneSource, performanceSource, sidecarDockerfile]) {
  assert.doesNotMatch(source, /runtime-log-indexer|table-monitor-entrypoint|audit-log-writer|Dockerfile\.runtime-log-indexer|Dockerfile\.table-monitor|Dockerfile\.audit-log-writer/u, 'Docker deployment must not retain standalone Go program paths')
}

console.log('Docker single Go sidecar deployment regression passed')

function serviceBlock(source, name) {
  const header = `  ${name}:\n`
  const start = source.indexOf(header)
  assert.notEqual(start, -1, `missing Compose service: ${name}`)
  const remaining = source.slice(start)
  const body = remaining.slice(header.length)
  const next = body.search(/^  [a-zA-Z0-9_-]+:\n/mu)
  return next === -1 ? remaining : remaining.slice(0, header.length + next)
}

function assertSingleSidecarContract(node, sidecar, mode) {
  assert.match(sidecar, /dockerfile:\s+docker\/Dockerfile\.go-sidecar/u, `${mode} Compose must build the sole Go sidecar image`)
  assert.match(sidecar, /^\s+network_mode:\s+service:juhe-ai\s*$/mu, `${mode} Go sidecar must share Node's loopback network namespace`)
  assert.match(sidecar, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:/u, `${mode} sidecar must receive F1 stable owner identity`)
  assert.match(sidecar, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, `${mode} sidecar must receive F2 stable owner identity`)
  assert.match(sidecar, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:/u, `${mode} sidecar must receive F3 stable owner identity`)
  assert.match(sidecar, /JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, `${mode} sidecar must receive F4 stable owner identity`)
  assert.match(sidecar, /JUHE_AI_AUDIT_LOG_INPUT_SECRET:/u, `${mode} sidecar must receive the explicit input secret`)
  assert.match(sidecar, /JUHE_AI_OPERATION_LOG_INPUT_SECRET:/u, `${mode} sidecar must receive the explicit F4 input secret`)
  assert.match(sidecar, /juhe-ai-audit-log-data:\/app\/backend\/audit-log-data\s*$/mu, `${mode} sidecar must write F3 artifacts through its dedicated volume`)
  if (mode === 'standalone') assert.match(sidecar, /juhe-ai-operation-log-data:\/app\/backend\/operation-log-data\s*$/mu, 'standalone sidecar must write F4 artifacts through its dedicated volume')
  assert.match(sidecar, /juhe-ai-go-sidecar-healthcheck/u, `${mode} sidecar healthcheck must use the Go loopback HTTP probe`)
  assert.match(node, /JUHE_AI_AUDIT_LOG_INPUT_URL:/u, `${mode} Node must send F3 input to the sidecar`)
  assert.match(node, /JUHE_AI_AUDIT_LOG_INPUT_SECRET:/u, `${mode} Node must receive the explicit F3 input secret`)
  assert.match(node, /JUHE_AI_OPERATION_LOG_INPUT_URL:/u, `${mode} Node must send F4 input to the sidecar`)
  assert.match(node, /JUHE_AI_OPERATION_LOG_INPUT_SECRET:/u, `${mode} Node must receive the explicit F4 input secret`)
  assert.match(node, /juhe-ai-audit-log-data:\/app\/backend\/audit-log-data:ro/u, `${mode} Node must mount F3 artifacts read-only`)
}
