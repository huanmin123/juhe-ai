import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, symlink, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'

import {
  ReleasePackageValidationError,
  validateReleasePackagePaths
} from './validate-release-package.mjs'

const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'juhe-release-validator-'))

async function resetFixture() {
  const fixturePath = path.join(tempRoot, 'fixture')
  await rm(fixturePath, { force: true, recursive: true })
  await mkdir(fixturePath, { recursive: true })
  return fixturePath
}

async function expectRejected(relativePath, contents = 'blocked') {
  const fixturePath = await resetFixture()
  const targetPath = path.join(fixturePath, ...relativePath.split('/'))
  await mkdir(path.dirname(targetPath), { recursive: true })
  await writeFile(targetPath, contents)

  await assert.rejects(
    validateReleasePackagePaths([fixturePath]),
    ReleasePackageValidationError,
    relativePath
  )
}

try {
  const validFixture = await resetFixture()
  await mkdir(path.join(validFixture, 'backend', 'dist'), { recursive: true })
  await mkdir(path.join(validFixture, 'backend-go'), { recursive: true })
  await mkdir(path.join(validFixture, 'docs', 'deploy'), { recursive: true })
  await mkdir(path.join(validFixture, 'frontend', 'dist'), { recursive: true })
  await writeFile(path.join(validFixture, 'backend', '.env.example'), 'PORT=3001\n')
  await writeFile(path.join(validFixture, 'backend', 'dist', 'server.js'), 'export {}\n')
  await writeFile(path.join(validFixture, 'frontend', 'dist', 'index.html'), '<!doctype html>\n')
  await writeFile(path.join(validFixture, 'start.sh'), '#!/usr/bin/env bash\n')
  await writeFile(path.join(validFixture, 'start.ps1'), 'exit 0\n')
  for (const project of ['jobs', 'gateway', 'maintenance']) {
    await writeFile(path.join(validFixture, 'backend-go', `juhe-ai-${project}`), 'binary\n')
  }
  await writeFile(path.join(validFixture, 'docs', 'deploy', 'migration.sql'), 'select 1;\n')
  await validateReleasePackagePaths([validFixture])

  await rm(path.join(validFixture, 'backend-go', 'juhe-ai-gateway'))
  await assert.rejects(
    validateReleasePackagePaths([validFixture]),
    /backend-go\/juhe-ai-gateway.*required Go project release binary is missing/u
  )

  for (const forbiddenPath of [
    'backend/data/state.json',
    'backend/logs/server.txt',
    'backend/node_modules/pkg/index.js',
    'backend/.env',
    'backend/.env.production',
    'backend/.env.example.local',
    'backend/app.log',
    'backend/state.sqlite',
    'backend/state.sqlite3',
    'backend/state.db',
    'backend/state.db3',
    'backend/backup.dump',
    'backend/dump.rdb',
    'backend/appendonly.aof.1.incr.aof',
    'backend/state.db-wal',
    'backend/state.db-shm',
    'backend/state.db-journal'
  ]) {
    await expectRejected(forbiddenPath)
  }

  const linksOnlyFixture = await resetFixture()
  await mkdir(path.join(linksOnlyFixture, 'data'), { recursive: true })
  await writeFile(path.join(linksOnlyFixture, 'data', 'allowed-during-source-scan.txt'), 'ok')
  await validateReleasePackagePaths([linksOnlyFixture], { linksOnly: true })

  // X01/X03：缺省 deploy mode 为 go。go-only 包不含 backend/dist，但必须带
  // 全部三个 Go 二进制；hybrid / node 分支仅为历史发布包校验保留。
  const goFixture = await resetFixture()
  await mkdir(path.join(goFixture, 'backend-go'), { recursive: true })
  await mkdir(path.join(goFixture, 'frontend', 'dist'), { recursive: true })
  await writeFile(path.join(goFixture, 'start.sh'), '#!/usr/bin/env bash\n')
  await writeFile(path.join(goFixture, 'start.ps1'), 'exit 0\n')
  await writeFile(path.join(goFixture, 'frontend', 'dist', 'index.html'), '<!doctype html>\n')
  for (const project of ['jobs', 'gateway', 'maintenance']) {
    await writeFile(path.join(goFixture, 'backend-go', `juhe-ai-${project}`), 'binary\n')
  }
  // 缺省调用（不传 deployMode）即 go 模式：历史 hybrid 包必需的 backend/dist
  // 缺失时仍然通过，证明默认已切到 go。
  await validateReleasePackagePaths([goFixture])
  await assert.rejects(
    validateReleasePackagePaths([goFixture], { deployMode: 'hybrid' }),
    /backend\/dist\/server\.js.*required release file is missing/u
  )
  await rm(path.join(goFixture, 'backend-go', 'juhe-ai-jobs'))
  await assert.rejects(
    validateReleasePackagePaths([goFixture]),
    /backend-go\/juhe-ai-jobs.*required Go project release binary is missing/u
  )

  const nodeFixture = await resetFixture()
  await mkdir(path.join(nodeFixture, 'backend', 'dist'), { recursive: true })
  await mkdir(path.join(nodeFixture, 'frontend', 'dist'), { recursive: true })
  await writeFile(path.join(nodeFixture, 'start.sh'), '#!/usr/bin/env bash\n')
  await writeFile(path.join(nodeFixture, 'start.ps1'), 'exit 0\n')
  await writeFile(path.join(nodeFixture, 'backend', 'dist', 'server.js'), 'export {}\n')
  await writeFile(path.join(nodeFixture, 'frontend', 'dist', 'index.html'), '<!doctype html>\n')
  await validateReleasePackagePaths([nodeFixture], { deployMode: 'node' })

  await assert.rejects(
    validateReleasePackagePaths([goFixture], { deployMode: 'bogus' }),
    /Unknown deploy mode: bogus/u
  )

  const linkFixture = await resetFixture()
  const linkTarget = path.join(tempRoot, 'link-target')
  await mkdir(linkTarget, { recursive: true })
  await symlink(
    linkTarget,
    path.join(linkFixture, 'linked-directory'),
    process.platform === 'win32' ? 'junction' : 'dir'
  )
  await assert.rejects(
    validateReleasePackagePaths([linkFixture]),
    /symbolic links and junctions are forbidden/u
  )

  process.stdout.write('Release package validator tests passed.\n')
} finally {
  await rm(tempRoot, { force: true, recursive: true })
}
