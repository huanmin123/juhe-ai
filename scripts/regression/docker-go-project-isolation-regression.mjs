import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../..', import.meta.url)))
// 现行唯一 Compose 形态是 docker/single-server/compose.yml（go-only：gateway
// 主入口 + jobs，无 Node 容器、无 network_mode: service:<Node>）；镜像经
// Dockerfile.runtime 从交叉编译二进制构建，各服务以 TARGET_BINARY 选定项目，
// 密钥与连接串统一经同目录 .env 注入，不再在 Compose 里逐项展开。
const composeSource = readFileSync(resolve(root, 'docker', 'single-server', 'compose.yml'), 'utf8').replaceAll('\r\n', '\n')
const projectDockerfile = readFileSync(resolve(root, 'docker', 'Dockerfile.go-project'), 'utf8').replaceAll('\r\n', '\n')

// 2026-09-19 零配置收口：7 个 worker 任务族开关与 6 个 retention 子开关已删除、
// 任务恒开，Compose 不得再向任何服务注入这些 env；J1 与 worker 主开关同理。
const removedEnvSwitchNames = [
  'JUHE_AI_JOBS_STATS_ENABLED', 'JUHE_AI_JOBS_OAUTH_ENABLED', 'JUHE_AI_JOBS_TASK_RUNS_ENABLED',
  'JUHE_AI_JOBS_USAGE_WRITER_ENABLED', 'JUHE_AI_JOBS_BALANCE_DETECT_ENABLED',
  'JUHE_AI_JOBS_RETENTION_ENABLED', 'JUHE_AI_JOBS_PROBE_ENABLED',
  'JUHE_AI_JOBS_RETENTION_CHAT_ENABLED', 'JUHE_AI_JOBS_RETENTION_DATA_ENABLED',
  'JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED', 'JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED',
  'JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED', 'JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED',
  'JUHE_AI_ACCOUNT_HEALTH_ENABLED', 'JUHE_AI_JOBS_WORKER_ENABLED'
]
for (const name of removedEnvSwitchNames) {
  assert.doesNotMatch(composeSource, new RegExp(`${name}:`, 'u'), `single-server compose must not inject the removed job family switch ${name}`)
}

// ---- single-server：go-only 终态 ----
{
  const mode = 'single-server'
  const gateway = serviceBlock(composeSource, 'gateway')
  const jobs = serviceBlock(composeSource, 'jobs')
  assert.doesNotMatch(composeSource, /^  juhe-ai:\n/mu, 'single-server compose must not keep a Node service')
  assert.doesNotMatch(composeSource, /network_mode:\s*service:juhe-ai/u, 'single-server compose must not share the retired Node loopback namespace')
  assert.match(composeSource, /^volumes:/mu, 'single-server compose must declare named volumes')

  assertProjectContract(gateway, 'gateway', mode)
  assertProjectContract(jobs, 'jobs', mode)
  // 跨进程交接契约：usage spool、chat-assets、codex context shard 等文件数据按
  // 进程 cwd 相对路径派生，gateway 与 jobs 必须挂载同源 bind mount 才能完成
  // 用量交接（单机形态以共享宿主目录替代旧 named volume 语义）。
  assert.match(gateway, /- \.\/data\/app\/data:\/app\/backend\/data\s*$/mu, 'single-server gateway must mount the shared app data directory')
  assert.match(jobs, /- \.\/data\/app\/data:\/app\/backend\/data\s*$/mu, 'single-server jobs must mount the same shared app data directory (usage spool handoff)')
}

assert.match(projectDockerfile, /GO_PROJECT/u, 'Go project Dockerfile must select one project at build time')
assert.match(projectDockerfile, /projects\/\$GO_PROJECT/u, 'Go project Dockerfile must build the selected independent module')
assert.doesNotMatch(projectDockerfile, /juhe-ai-go-sidecar/u, 'Dockerfile must not retain the deleted monolithic sidecar')
assert.match(projectDockerfile, /COPY frontend\/dist \.\/frontend\/dist/u, 'Go project Dockerfile must include the packaged frontend dist')
assert.match(projectDockerfile, /COPY --from=build \/source\/frontend\/dist \/app\/frontend\/dist/u, 'Go project runtime must expose the packaged frontend dist')

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

// single-server 形态的项目契约：两个 Go 服务各自经 Dockerfile.runtime +
// TARGET_BINARY 构建独立项目二进制（源码构建形态的 Dockerfile.go-project
// 契约另行断言），并各自暴露容器健康检查。
function assertProjectContract(service, project, mode) {
  assert.match(service, /dockerfile:\s+Dockerfile\.runtime/u, `${mode} ${project} must build from the runtime image Dockerfile`)
  assert.match(service, new RegExp(`TARGET_BINARY:\\s+juhe-ai-${project}`, 'u'), `${mode} ${project} build must select its project binary`)
  assert.match(service, /healthcheck:/u, `${mode} ${project} must expose project health`)
}
