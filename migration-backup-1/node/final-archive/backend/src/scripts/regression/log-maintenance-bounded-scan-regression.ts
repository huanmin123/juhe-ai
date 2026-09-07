import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-log-maintenance-bounded-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.log.directory = tempRoot
runtimeConfig.log.fileEnabled = false
runtimeConfig.log.consoleEnabled = false
mkdirSync(tempRoot, { recursive: true })

const { cleanupRotatedLogFilesForTest, logger } = await import('../../shared/logger.js')
logger.level = 'silent'

const originalSort = Array.prototype.sort
let sortCalled = false

try {
  Array.prototype.sort = function patchedSort<T>(this: T[], compareFn?: (a: T, b: T) => number): T[] {
    sortCalled = true
    return originalSort.call(this, compareFn)
  }

  writeFileSync(join(tempRoot, 'juhe-ai.log'), 'current\n', 'utf8')
  for (let index = 0; index < 30; index += 1) {
    const filePath = join(tempRoot, `juhe-ai.20260101T0000${String(index).padStart(2, '0')}Z.${String(index).padStart(8, '0')}-aaaa-bbbb-cccc-dddddddddddd.log`)
    writeFileSync(filePath, `rotated-${index}\n`, 'utf8')
    const mtime = new Date(Date.now() - index * 1000)
    await import('node:fs/promises').then((fs) => fs.utimes(filePath, mtime, mtime))
  }

  const result = await cleanupRotatedLogFilesForTest({
    directory: tempRoot,
    maxFiles: 6,
    retentionDays: 30
  })

  assert.equal(sortCalled, false, '日志维护不应通过数组排序处理 rotated 文件')
  assert.equal(result.currentFileCount, 1, '当前日志文件应计入 maxFiles 保护名额')
  assert.equal(result.retainedRotatedFileCount, 5, 'rotated 文件应按 maxFiles-currentFiles 有界保留')
  assert.equal(result.deletedFileCount, 25, '超过保留窗口的 rotated 文件应被删除')

  console.log('日志维护有界扫描回归通过：rotated 日志清理使用异步目录迭代和固定窗口，不再全量排序')
} finally {
  Array.prototype.sort = originalSort
  rmSync(tempRoot, { recursive: true, force: true })
}
