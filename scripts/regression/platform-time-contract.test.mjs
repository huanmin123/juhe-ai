import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')

async function readRepositoryFile(relativePath) {
  return readFile(path.join(repositoryRoot, relativePath), 'utf8')
}

function serviceBlock(compose, serviceName) {
  const expression = new RegExp(`^  ${serviceName}:\\r?\\n([\\s\\S]*?)(?=^  [A-Za-z0-9-]+:|^volumes:)`, 'm')
  const matched = compose.match(expression)
  assert.ok(matched, `missing Compose service: ${serviceName}`)
  return matched[0]
}

const [standaloneCompose, performanceCompose, performanceEnv, startPowerShell, startShell, jenkinsfile] = await Promise.all([
  readRepositoryFile('docker/compose.yml'),
  readRepositoryFile('docker/compose.performance.yml'),
  readRepositoryFile('docker/.env.performance.example'),
  readRepositoryFile('deploy/start.ps1'),
  readRepositoryFile('deploy/start.sh'),
  readRepositoryFile('Jenkinsfile')
])

// X01/X03 go-only 收口：standalone compose.yml 已是 go-only 拓扑
// （gateway + jobs，无 Node 容器）；performance 仍是 hybrid 遗留形态
// （juhe-ai + go-gateway + go-jobs，go-only 变体为 X03 待办）。
// Go 进程的业务时区来自 settings 库（不再读 JUHE_AI_USAGE_STATS_TIMEZONE），
// 因此该变量只在 performance 的 Node 服务上继续断言。
for (const [name, compose, serviceNames] of [
  ['standalone', standaloneCompose, ['gateway', 'jobs']],
  ['performance', performanceCompose, ['juhe-ai', 'go-gateway', 'go-jobs']]
]) {
  for (const serviceName of serviceNames) {
    assert.match(serviceBlock(compose, serviceName), /environment:\r?\n\s+TZ: UTC\r?\n/, `${name}/${serviceName} must fix process TZ to UTC`)
  }
}
assert.match(serviceBlock(performanceCompose, 'juhe-ai'), /JUHE_AI_USAGE_STATS_TIMEZONE: \$\{JUHE_AI_USAGE_STATS_TIMEZONE:-UTC\}/, 'performance/juhe-ai must expose an explicit IANA business timezone')

assert.match(performanceEnv, /^JUHE_AI_USAGE_STATS_TIMEZONE=UTC$/m)
assert.match(startPowerShell, /\$env:TZ = 'UTC'/)
assert.match(startShell, /^export TZ=UTC$/m)
assert.match(jenkinsfile, /^    TZ = 'UTC'$/m)

process.stdout.write('platform time contract regression passed\n')
