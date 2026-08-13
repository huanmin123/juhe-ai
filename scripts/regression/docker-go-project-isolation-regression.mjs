import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const standaloneSource = readFileSync(resolve(root, 'docker', 'compose.yml'), 'utf8').replaceAll('\r\n', '\n')
const performanceSource = readFileSync(resolve(root, 'docker', 'compose.performance.yml'), 'utf8').replaceAll('\r\n', '\n')
const projectDockerfile = readFileSync(resolve(root, 'docker', 'Dockerfile.go-project'), 'utf8').replaceAll('\r\n', '\n')

for (const [source, mode] of [[standaloneSource, 'standalone'], [performanceSource, 'performance']]) {
  const node = serviceBlock(source, 'juhe-ai')
  const gateway = serviceBlock(source, 'go-gateway')
  const jobs = serviceBlock(source, 'go-jobs')
  assertProjectContract(gateway, 'gateway', mode)
  assertProjectContract(jobs, 'jobs', mode)
  assert.match(gateway, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:/u, `${mode} gateway must own F3`)
  assert.match(gateway, /JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, `${mode} gateway must own F4`)
  assert.doesNotMatch(gateway, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:|JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, `${mode} gateway must not receive F1/F2 ownership`)
  assert.match(jobs, /JUHE_AI_RUNTIME_LOG_INSTANCE_ID:/u, `${mode} jobs must own F1`)
  assert.match(jobs, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID:/u, `${mode} jobs must own F2`)
  assert.doesNotMatch(jobs, /JUHE_AI_AUDIT_LOG_INSTANCE_ID:|JUHE_AI_OPERATION_LOG_INSTANCE_ID:/u, `${mode} jobs must not receive F3/F4 ownership`)
  assert.match(node, /JUHE_AI_AUDIT_LOG_INPUT_URL:|JUHE_AI_OPERATION_LOG_INPUT_URL:/u, `${mode} Node must retain loopback producer URLs`)
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
  assert.match(service, /^\s+network_mode:\s+service:juhe-ai\s*$/mu, `${mode} ${project} must share Node loopback namespace during this migration`)
  assert.match(service, /juhe-ai-go-project-healthcheck/u, `${mode} ${project} must expose project health`)
}
