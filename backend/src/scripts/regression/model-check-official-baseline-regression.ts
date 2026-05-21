import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-baseline-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'model-check-baseline-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })

const [
  { getRecordDatabase },
  { getModelCheckOptions, ModelCheckRequestError, runModelCheck }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/model-checks/model-checks.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const options = getModelCheckOptions(access)
assert.equal(options.officialBaseline.enabledByDefault, false, '官网对照必须默认关闭')
assert.equal(options.officialBaseline.available, false, '空环境不应报告官网基线账户可用')

await assert.rejects(
  () => runModelCheck({
    targetType: 'api_key',
    targetId: 'key_missing',
    model: 'gpt-5.5',
    profile: 'full',
    officialBaseline: true
  }, access),
  (error) => {
    assert(error instanceof ModelCheckRequestError)
    assert.equal(error.statusCode, 400)
    assert.match(error.message, /官网基线账户/)
    return true
  },
  '显式开启官网对照但无基线账户时必须失败'
)

const row = getRecordDatabase()
  .prepare('SELECT COUNT(*) AS count FROM model_check_runs')
  .get() as { count: number }
assert.equal(row.count, 0, '官网对照开启失败时不应创建成功或失败检测报告')

console.log('模型检测官网对照回归通过：默认关闭，无官网基线账户时显式开启会失败且不写报告')
