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

const [singleServerCompose, startPowerShell, startShell] = await Promise.all([
  readRepositoryFile('docker/single-server/compose.yml'),
  readRepositoryFile('deploy/start.ps1'),
  readRepositoryFile('deploy/start.sh')
])

// 现行唯一 Compose 形态是 docker/single-server/compose.yml（go-only：gateway
// + jobs，Node 后端与 K3s/performance 形态均已下线）。Go 进程时区在该 Compose
// 中显式固定为 Asia/Shanghai，不得回退到镜像或环境默认值；本地 start 脚本
// 仍各自固定 TZ=UTC，两类入口都要求时区显式。
for (const serviceName of ['gateway', 'jobs']) {
  assert.match(serviceBlock(singleServerCompose, serviceName), /environment:\r?\n\s+TZ: Asia\/Shanghai\r?\n/, `single-server/${serviceName} must fix process TZ explicitly to Asia/Shanghai`)
}

assert.match(startPowerShell, /\$env:TZ = 'UTC'/)
assert.match(startShell, /^export TZ=UTC$/m)

process.stdout.write('platform time contract regression passed\n')
