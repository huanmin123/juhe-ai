import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-settings-query-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'settings-query-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, settingsRepository, accountErrorPolicy] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/policy/account-error-policy.service.js')
])

try {
  assertSettingsSourceReadsKnownKeysOnly()
  assertAccountCooldownSettingNoRuntimeFallback()
  const database = databaseModule.getBusinessDatabase()
  const now = new Date().toISOString()
  const insert = database.prepare(`
    INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES ('sys_admin', ?, 'true', ?)
  `)
  for (let index = 0; index < 2000; index += 1) {
    insert.run(`rogue_settings_key_${index}`, now)
  }

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let settingsSelectSql = ''
  let selectedRowCount = 0
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+system_settings\b/i.test(sql) && /\bSELECT\s+key,\s+value_json\b/i.test(sql)) {
      settingsSelectSql = sql
      const originalAll = statement.all.bind(statement) as typeof statement.all
      return {
        all: (...params: Parameters<typeof statement.all>) => {
          const rows = originalAll(...params) as unknown[]
          selectedRowCount = rows.length
          return rows
        }
      } as unknown as ReturnType<typeof database.prepare>
    }
    return statement
  }) as typeof database.prepare

  try {
    const settings = settingsRepository.getSettings()
    assert(settingsSelectSql.includes('key IN'), '系统设置读取必须在 SQL 层限定已知 key，不能读出全账户设置后再过滤')
    assert(selectedRowCount <= settingsRepository.systemSettingKeys.length, '系统设置读取行数必须受白名单 key 数量约束')
    assert.equal(Object.prototype.hasOwnProperty.call(settings, 'rogue_settings_key_0'), false, '系统设置结果不能暴露未知 key')
  } finally {
    database.prepare = originalPrepare
  }

  assert.throws(
    () => settingsRepository.updateSettings({ rogue_settings_key_0: true }),
    /未知系统设置字段/,
    '系统设置更新不能静默吞掉未知字段'
  )
  assert.throws(
    () => settingsRepository.updateSettings({ statsAggregationBatchSize: '2000' }),
    /必须是整数/,
    '系统设置更新不能接受字符串数字'
  )
  assert.throws(
    () => settingsRepository.updateGlobalSettings({ legacyBrandName: '旧字段' }),
    /未知全局设置字段/,
    '全局设置更新不能静默吞掉未知字段'
  )
  database
    .prepare("UPDATE system_settings SET value_json = ?, updated_at = ? WHERE system_account_id = 'sys_admin' AND key = 'defaultTemporaryUnschedulableMinutes'")
    .run('"5"', now)
  settingsRepository.clearSettingsRepositoryCache()
  assert.throws(
    () => accountErrorPolicy.readGatewaySettings(),
    /defaultTemporaryUnschedulableMinutes 必须是整数/,
    '网关运行路径读取非法临时不可调用设置时不能静默回退默认值'
  )

  console.log('系统设置查询和严格契约回归通过：读取在 SQL 层按固定 key 白名单约束，更新拒绝未知字段和字符串数字，网关运行路径不再二次兜底非法设置')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertSettingsSourceReadsKnownKeysOnly(): void {
  const source = readFileSync(resolve('src/storage/settings.repository.ts'), 'utf8')
  const body = sourceFunctionBlock(source, 'export function getSettings')
  assert(body.includes('key IN'), 'getSettings 查询必须包含 key IN 白名单')
  assert(!/rows\.filter\(\(row\)\s*=>\s*isSystemSettingKey/.test(body), 'getSettings 不能先全量读取再 JS 过滤系统设置 key')
}

function assertAccountCooldownSettingNoRuntimeFallback(): void {
  const body = sourceFunctionBlockFromAnyFile([
    'src/storage/account-runtime-mutation-helpers.ts',
    'src/storage/repositories.ts'
  ], 'function defaultTemporaryUnschedulableMinutes')
  assert(!body.includes('Math.trunc'), '临时不可调用默认设置读取不能截断小数')
  assert(!body.includes('return 5'), '临时不可调用默认设置读取不能静默回退 5 分钟')
}

function sourceFunctionBlockFromAnyFile(paths: string[], marker: string): string {
  const errors: string[] = []
  for (const path of paths) {
    try {
      const source = readFileSync(resolve(path), 'utf8')
      return sourceFunctionBlock(source, marker)
    } catch (error) {
      errors.push(`${path}: ${error instanceof Error ? error.message : String(error)}`)
    }
  }
  throw new Error(`未找到源码片段：${marker}\n${errors.join('\n')}`)
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const bodyStart = source.indexOf('{', start)
  assert(bodyStart >= 0, `源码片段缺少函数体：${marker}`)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(start, index + 1)
      }
    }
  }
  throw new Error(`源码片段函数体未闭合：${marker}`)
}
