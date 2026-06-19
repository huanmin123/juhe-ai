import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-trusted-comparison-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-trusted-comparison-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })

const [
  { getDatasetDatabase },
  { getModelCheckOptions, ModelCheckRequestError, runModelCheck },
  { isModelCheckSupportedProtocolProfile, modelCheckSupportedProtocolLabel }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/model-checks/model-checks.service.js'),
  import('../../modules/model-checks/model-checks.provider-capabilities.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const options = getModelCheckOptions(access)
assert.equal(options.trustedComparison.enabledByDefault, false, '可信对比必须默认关闭')
assert.equal(options.trustedComparison.available, true, '可信对比能力不应依赖自动扫描账户')
assert.equal(modelCheckSupportedProtocolLabel, 'OpenAI v1', '模型检测当前能力边界应由协议标签表达')
assert.equal(isModelCheckSupportedProtocolProfile({ protocolCode: 'openai', protocolVersion: 'v1' }), true, 'OpenAI v1 协议账户应支持模型检测')
assert.equal(isModelCheckSupportedProtocolProfile({ protocolCode: 'anthropic', protocolVersion: 'v1' }), false, 'Anthropic v1 协议账户不应进入当前 OpenAI 模型检测')
const trustedComparisonMessage = options.trustedComparison.message ?? ''
assert.match(trustedComparisonMessage, /OpenAI v1/, '可信对比文案应描述协议能力边界')
assert.doesNotMatch(trustedComparisonMessage, /GPT/, '可信对比文案不应绑定 GPT 供应商名')

await assert.rejects(
  () => runModelCheck({
    targetType: 'account',
    targetId: 'acc_missing',
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: true
  }, access),
  (error) => {
    assert(error instanceof ModelCheckRequestError)
    assert.equal(error.statusCode, 400)
    assert.match(error.message, /可信对比账户/)
    return true
  },
  '显式开启可信对比但未选择对比账户时必须失败'
)

const row = getDatasetDatabase()
  .prepare('SELECT COUNT(*) AS count FROM model_check_runs')
  .get() as { count: number }
assert.equal(row.count, 0, '可信对比开启失败时不应创建成功或失败检测报告')

console.log('模型检测可信对比回归通过：默认关闭，未选择对比账户时显式开启会失败且不写报告')
