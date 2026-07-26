import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-rotation-'))
const logDir = join(root, 'logs')
mkdirSync(logDir)
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'

const [loggerModule, importerModule] = await Promise.all([
  import('../../shared/logger.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js')
])

try {
  const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
  assert.match(
    workerSource,
    /if \(isIngestWorker\(\)\)[\s\S]+startRuntimeLogFileImport\(\)/,
    'ingest-worker 必须启动 file importer'
  )
  assert.doesNotMatch(
    workerSource,
    /if \(runtimeConfig\.databaseDriver === 'sqlite'\) \{\s*startRuntimeLogFileImport\(\)/,
    'PostgreSQL ingest-worker 不得跳过 file importer'
  )

  const crowdedLogDir = join(root, 'crowded-logs')
  mkdirSync(crowdedLogDir)
  for (let index = 0; index < 2050; index += 1) {
    writeFileSync(join(crowdedLogDir, `aaa-unrelated-${String(index).padStart(4, '0')}.txt`), '')
  }
  const crowdedCurrentPath = join(crowdedLogDir, 'juhe-ai.ops-worker.log')
  const crowdedRotatedPath = join(crowdedLogDir, 'juhe-ai.ops-worker.20260721T000000Z.00000000-0000-0000-0000-000000000099.log')
  writeFileSync(crowdedCurrentPath, 'current\n')
  writeFileSync(crowdedRotatedPath, 'rotated\n')
  runtimeConfig.log.directory = crowdedLogDir
  await importerModule.resetRuntimeLogFileDiscoveryForTest()
  const crowdedDiscovery = [
    ...(await importerModule.discoverRuntimeLogFilesForTest()),
    ...(await importerModule.discoverRuntimeLogFilesForTest()),
    ...(await importerModule.discoverRuntimeLogFilesForTest())
  ]
  assert.ok(crowdedDiscovery.some((file) => file.path === crowdedCurrentPath), '超过 2048 个非日志目录项后仍必须发现受控 current 文件')
  assert.ok(crowdedDiscovery.some((file) => file.path === crowdedRotatedPath), '超过 2048 个非日志目录项后仍必须发现受控 rotated 文件')

  const fairnessLogDir = join(root, 'fairness-logs')
  mkdirSync(fairnessLogDir)
  for (let index = 0; index < 2050; index += 1) {
    const uniqueId = index.toString(16).padStart(32, '0')
    writeFileSync(join(fairnessLogDir, `juhe-ai.ops-worker.20260721T000000Z.${uniqueId}.log`), '')
  }
  runtimeConfig.log.directory = fairnessLogDir
  await importerModule.resetRuntimeLogFileDiscoveryForTest()
  const firstWindow = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(importerModule.getRuntimeLogDiscoveryReadCountForTest() <= 2048, '每轮目录发现读取的真实目录项不得超过固定上限')
  const secondWindow = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(importerModule.getRuntimeLogDiscoveryReadCountForTest() <= 2048, '目录 continuation 每轮读取的真实目录项仍不得超过固定上限')
  assert.equal(firstWindow.length, 2048, '单轮只允许返回有界数量的受控日志文件')
  assert.equal(secondWindow.length, 2, '下一轮必须从受控匹配进度继续，不能永久饿死窗口外文件')
  assert.equal(new Set([...firstWindow, ...secondWindow].map((file) => file.path)).size, 2050, '跨轮询发现必须完整且不重复')

  const exactLimitLogDir = join(root, 'exact-limit-logs')
  mkdirSync(exactLimitLogDir)
  for (let index = 0; index < 2048; index += 1) {
    const uniqueId = index.toString(16).padStart(32, '0')
    writeFileSync(join(exactLimitLogDir, `juhe-ai.ops-worker.20260721T010000Z.${uniqueId}.log`), '')
  }
  runtimeConfig.log.directory = exactLimitLogDir
  await importerModule.resetRuntimeLogFileDiscoveryForTest()
  const exactLimitFirstWindow = await importerModule.discoverRuntimeLogFilesForTest()
  const exactLimitSecondWindow = await importerModule.discoverRuntimeLogFilesForTest()
  assert.equal(exactLimitFirstWindow.length, 2048, '恰好达到目录读取上限时必须返回完整窗口')
  assert.equal(exactLimitSecondWindow.length, 2048, '恰好达到上限并已到 EOF 时下一轮必须重开目录，不能先返回空轮')
  runtimeConfig.log.directory = logDir

  const currentPath = join(logDir, 'juhe-ai.stats-worker.log')
  writeFileSync(currentPath, 'one\n')
  const discovered = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(discovered.some((file) => file.path === currentPath), '角色当前文件必须被发现')

  const rotatedFileName = loggerModule.rotatedLogFileName(
    'juhe-ai.stats-worker.log',
    new Date('2026-07-21T01:02:03.000Z'),
    '00000000-0000-0000-0000-000000000002'
  )
  const rotatedPath = join(logDir, rotatedFileName)
  writeFileSync(rotatedPath, 'two\n')
  const discoveredWithRotation = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(discoveredWithRotation.some((file) => file.path === rotatedPath), '角色 basename 必须保留并被轮转发现')
  assert.match(rotatedPath, /juhe-ai\.stats-worker\./, '轮转文件名必须包含角色 basename')

  const performanceCurrentName = 'juhe-ai.log-worker.node-a.log'
  const performanceCurrentPath = join(logDir, performanceCurrentName)
  const performanceRotatedName = loggerModule.rotatedLogFileName(
    performanceCurrentName,
    new Date('2026-07-21T02:03:04.000Z'),
    '00000000-0000-0000-0000-000000000003'
  )
  const performanceRotatedPath = join(logDir, performanceRotatedName)
  writeFileSync(performanceCurrentPath, 'performance current\n')
  writeFileSync(performanceRotatedPath, 'performance rotated\n')
  const discoveredPerformanceFiles = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(
    discoveredPerformanceFiles.some((file) => file.path === performanceCurrentPath && file.role === 'log-worker:node-a' && file.kind === 'current'),
    'performance log-worker current 文件必须携带 instanceId 被发现'
  )
  assert.ok(
    discoveredPerformanceFiles.some((file) => file.path === performanceRotatedPath && file.role === 'log-worker:node-a' && file.kind === 'rotated'),
    'performance log-worker rotated 文件必须携带 instanceId 被发现'
  )

  const result = await loggerModule.cleanupRotatedLogFilesForTest({
    directory: logDir,
    maxFiles: 0,
    retentionDays: 0,
    canDeleteRotatedFile: async () => false
  })
  assert.equal(result.deletedFileCount, 0, '未消费轮转文件必须受到清理保护')

  console.log('运行日志轮转与清理回归通过')
} finally {
  rmSync(root, { recursive: true, force: true })
}
