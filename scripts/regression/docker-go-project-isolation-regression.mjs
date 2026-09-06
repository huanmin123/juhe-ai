import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)))
// X01/X03 go-only 收口：standalone compose.yml 是 go-only 拓扑（gateway 主入口
// + jobs，无 Node 容器、无 network_mode: service:<Node>）；performance 仍是
// hybrid 遗留形态（juhe-ai + go-gateway + go-jobs，go-only 变体为 X03 待办）。
const standaloneSource = readFileSync(resolve(root, 'docker', 'compose.yml'), 'utf8').replaceAll('\r\n', '\n')
const performanceSource = readFileSync(resolve(root, 'docker', 'compose.performance.yml'), 'utf8').replaceAll('\r\n', '\n')
const projectDockerfile = readFileSync(resolve(root, 'docker', 'Dockerfile.go-project'), 'utf8').replaceAll('\r\n', '\n')

// ---- standalone：go-only 终态 ----
{
  const mode = 'standalone'
  const gateway = serviceBlock(standaloneSource, 'gateway')
  const jobs = serviceBlock(standaloneSource, 'jobs')
  assert.doesNotMatch(standaloneSource, /^  juhe-ai:\n/mu, 'standalone compose must not keep a Node service')
  assert.doesNotMatch(standaloneSource, /network_mode:\s*service:juhe-ai/u, 'standalone compose must not share the retired Node loopback namespace')
  assert.match(standaloneSource, /^volumes:/mu, 'standalone compose must declare named volumes')

  assertProjectContract(gateway, 'gateway', mode)
  // go-only 主入口开关：gateway 必须以 SYSTEM_API_ENABLED=true 绑定业务端口。
  assert.match(gateway, /JUHE_AI_GATEWAY_SYSTEM_API_ENABLED: \$\{JUHE_AI_GATEWAY_SYSTEM_API_ENABLED:-true\}/u, 'standalone gateway must own the main HTTP entry by default')
  assert.match(gateway, /JUHE_AI_GATEWAY_CHAIN_ENABLED: \$\{JUHE_AI_GATEWAY_CHAIN_ENABLED:-false\}/u, 'standalone gateway must expose the chain gate to the Go runtime')
  for (const name of [
    'JUHE_AI_BUSINESS_OWNER', 'JUHE_AI_BUSINESS_HANDOFF_CONFIRMED',
    'JUHE_AI_BUSINESS_NODE_WRITER_STOPPED', 'JUHE_AI_BUSINESS_SCHEMA_READY',
    'JUHE_AI_BUSINESS_OWNER_EPOCH', 'JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH',
    'JUHE_AI_BUSINESS_DATABASE_PATH', 'JUHE_AI_BUSINESS_POSTGRES_URL'
  ]) {
    assert.match(gateway, new RegExp(`${name}:`, 'u'), `standalone gateway must receive ${name}`)
  }
  assert.match(gateway, /ports:/u, 'standalone gateway must publish the public HTTP port')
  assert.match(gateway, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:/u, 'standalone gateway must own F3')
  assert.match(gateway, /JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, 'standalone gateway must own F4')
  assert.doesNotMatch(gateway, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:|JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, 'standalone gateway must not receive F1/F2 ownership')
  assert.match(gateway, /JUHE_AI_RUNTIME_LOG_DATABASE_PATH:/u, 'standalone gateway must receive the F1 source path for F3/F4 SQLite isolation checks')
  assert.match(gateway, /JUHE_AI_TABLE_MONITOR_DATABASE_PATH:/u, 'standalone gateway must receive the F2 source path for F3/F4 SQLite isolation checks')
  assert.match(gateway, /juhe-ai-runtime-log-data:\/app\/backend\/runtime-log-data:ro/u, 'standalone gateway must mount the F1 source read-only')
  assert.match(gateway, /juhe-ai-table-monitor-data:\/app\/backend\/table-monitor-data:ro/u, 'standalone gateway must mount the F2 source read-only')
  // go-only 卷语义：gateway 是业务库唯一 writer，业务卷不能只读。
  assert.match(gateway, /- juhe-ai-data:\/app\/backend\/data\s*$/mu, 'standalone gateway must own the business database volume read-write')

  assertProjectContract(jobs, 'jobs', mode)
  assert.match(jobs, /depends_on:\s*\n\s+gateway:\s*\n\s+condition:\s+service_healthy/u, 'standalone jobs must start after the gateway is healthy')
  assert.match(jobs, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:/u, 'standalone jobs must own F1')
  assert.match(jobs, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, 'standalone jobs must own F2')
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_ENABLED: \$\{JUHE_AI_ACCOUNT_HEALTH_ENABLED:-false\}/u, 'standalone jobs must keep J1 disabled by default')
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER:/u, 'standalone jobs must receive the J1 owner declaration')
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY:/u, 'standalone jobs must receive the J1 signed-request directory')
  assert.match(jobs, /juhe-ai-account-health-data:\/app\/backend\/account-health-data\s*$/mu, 'standalone jobs must own the J1 SQLite store volume')
  assert.match(jobs, /juhe-ai-account-health-inputs:\/app\/backend\/account-health-inputs\s*$/mu, 'standalone jobs must consume J1 signed requests from the shared directory')
  assert.match(jobs, /- juhe-ai-data:\/app\/backend\/data:ro/u, 'standalone jobs must keep the business database volume read-only')
  assert.doesNotMatch(jobs, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:|JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, 'standalone jobs must not receive F3/F4 ownership')
}

// ---- performance：hybrid 遗留形态（未收口，X03 待办） ----
{
  const mode = 'performance'
  const node = serviceBlock(performanceSource, 'juhe-ai')
  const gateway = serviceBlock(performanceSource, 'go-gateway')
  const jobs = serviceBlock(performanceSource, 'go-jobs')
  assertProjectContractLegacy(gateway, 'gateway', mode)
  assertProjectContractLegacy(jobs, 'jobs', mode)
  assert.match(gateway, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:/u, `${mode} gateway must own F3`)
  assert.match(gateway, /JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, `${mode} gateway must own F4`)
  assert.doesNotMatch(gateway, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:|JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, `${mode} gateway must not receive F1/F2 ownership`)
  assert.match(jobs, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:/u, `${mode} jobs must own F1`)
  assert.match(jobs, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, `${mode} jobs must own F2`)
  assert.match(node, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: \$\{JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER:-go\}/u, `${mode} Node must start with the fixed Go J1 owner`)
  assert.match(node, /JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY:/u, `${mode} Node must receive the J1 signed-request directory`)
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_ENABLED: \$\{JUHE_AI_ACCOUNT_HEALTH_ENABLED:-false\}/u, `${mode} jobs must keep J1 disabled by default`)
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER:/u, `${mode} jobs must receive the J1 owner declaration`)
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY:/u, `${mode} jobs must receive the J1 signed-request directory`)
  assert.doesNotMatch(jobs, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:|JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, `${mode} jobs must not receive F3/F4 ownership`)
  assert.match(node, /JUHE_AI_AUDIT_LOG_INPUT_URL:|JUHE_AI_OPERATION_LOG_INPUT_URL:/u, `${mode} Node must retain loopback producer URLs`)
  assert.match(node, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL:/u, 'performance Node must receive the J1 jobs outcome read URL')
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_STORE: \$\{JUHE_AI_ACCOUNT_HEALTH_STORE:-postgres\}/u, 'performance jobs must default J1 store to PostgreSQL')
  assert.match(jobs, /JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE: \$\{JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE:-postgres\}/u, 'performance jobs must default J1 input to read-only PostgreSQL')
  assert.match(node, /juhe-ai-account-health-inputs:\/app\/backend\/account-health-inputs\s*$/mu, 'performance Node must share the J1 signed-request directory')
  assert.match(jobs, /juhe-ai-account-health-inputs:\/app\/backend\/account-health-inputs\s*$/mu, 'performance jobs must consume J1 signed requests from the shared directory')
}

assert.match(projectDockerfile, /GO_PROJECT/u, 'Go project Dockerfile must select one project at build time')
assert.match(projectDockerfile, /projects\/\$GO_PROJECT/u, 'Go project Dockerfile must build the selected independent module')
assert.doesNotMatch(projectDockerfile, /juhe-ai-go-sidecar/u, 'Dockerfile must not retain the deleted monolithic sidecar')

console.log('Docker Go project isolation regression passed')

function serviceBlock(source, name) {
  const header = `  ${name}:\n`
  const start = source.indexOf(header)
  assert.notEqual(start, -1, `missing Compose service: ${name}`)
  const remaining = source.slice(start)
  const body = remaining.slice(header.length)
  const next = body.search(/^  [a-zA-Z0-9_-]+:\n/mu)
  return next === -1 ? remaining : remaining.slice(0, header.length + next)
}

function assertProjectContract(service, project, mode) {
  assert.match(service, /dockerfile:\s+docker\/Dockerfile\.go-project/u, `${mode} ${project} must use the generic Go project Dockerfile`)
  assert.match(service, new RegExp(`GO_PROJECT:\\s+${project}`, 'u'), `${mode} ${project} build must select its module`)
  assert.match(service, /juhe-ai-go-project-healthcheck/u, `${mode} ${project} must expose project health`)
}

// performance 仍处于 hybrid 遗留形态：两个 Go 服务共享 Node 的 loopback
// network namespace（F3/F4 input listener 只绑 loopback）。go-only 变体落地时
// 应改为 standalone 的 assertProjectContract 契约。
function assertProjectContractLegacy(service, project, mode) {
  assertProjectContract(service, project, mode)
  assert.match(service, /^\s+network_mode:\s+service:juhe-ai\s*$/mu, `${mode} ${project} must share the Node loopback namespace during this migration`)
}
