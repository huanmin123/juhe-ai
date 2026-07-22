import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-team-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-team-single-read-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const team = repositories.createSystemTeam({
      name: `团队单条读取回归-${String(index).padStart(3, '0')}`,
      status: 'active'
    }, access)
    if (index === 0) {
      targetId = team.id
    }
  }

  const firstPageLikeList = repositories.listSystemTeams(access).slice(0, 200)
  assert.equal(firstPageLikeList.some((team) => team.id === targetId), false, '最早创建的第 250 条外团队不应出现在前 200 条列表窗口里')

  const target = repositories.findSystemTeamSummary(targetId, access)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的系统团队')
  assert.equal(target?.name, '团队单条读取回归-000', '按 ID 单条读取应返回完整团队摘要')

  const updated = repositories.updateSystemTeam(targetId, { description: '已通过单条读取更新' }, access)
  assert.equal(updated?.description, '已通过单条读取更新', '更新团队应通过单条读取返回目标团队摘要')

  console.log('系统团队单条读取回归通过：写路径不再依赖全量团队列表装配')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
