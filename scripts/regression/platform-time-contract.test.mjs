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

const [standaloneCompose, performanceCompose, standaloneEnv, performanceEnv, startPowerShell, startShell, jenkinsfile] = await Promise.all([
  readRepositoryFile('docker/compose.yml'),
  readRepositoryFile('docker/compose.performance.yml'),
  readRepositoryFile('docker/.env.example'),
  readRepositoryFile('docker/.env.performance.example'),
  readRepositoryFile('deploy/start.ps1'),
  readRepositoryFile('deploy/start.sh'),
  readRepositoryFile('Jenkinsfile')
])

for (const [name, compose] of [['standalone', standaloneCompose], ['performance', performanceCompose]]) {
  for (const serviceName of ['juhe-ai', 'go-gateway', 'go-jobs']) {
    assert.match(serviceBlock(compose, serviceName), /environment:\r?\n\s+TZ: UTC\r?\n/, `${name}/${serviceName} must fix process TZ to UTC`)
  }
  assert.match(serviceBlock(compose, 'juhe-ai'), /JUHE_AI_USAGE_STATS_TIMEZONE: \$\{JUHE_AI_USAGE_STATS_TIMEZONE:-UTC\}/, `${name}/juhe-ai must expose an explicit IANA business timezone`)
}

assert.match(standaloneEnv, /^JUHE_AI_USAGE_STATS_TIMEZONE=UTC$/m)
assert.match(performanceEnv, /^JUHE_AI_USAGE_STATS_TIMEZONE=UTC$/m)
assert.match(startPowerShell, /\$env:TZ = 'UTC'/)
assert.match(startShell, /^export TZ=UTC$/m)
assert.match(jenkinsfile, /^    TZ = 'UTC'$/m)

process.stdout.write('platform time contract regression passed\n')
