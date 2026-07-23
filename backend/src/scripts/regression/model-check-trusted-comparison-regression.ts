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
const supportedModelIds = options.supportedModels.map((item) => item.value)
assert.equal(options.trustedComparison.enabledByDefault, false, '可信对比必须默认关闭')
assert.equal(options.trustedComparison.available, true, '可信对比能力不应依赖自动扫描账户')
assert(supportedModelIds.includes('claude-opus-4-8'), '模型检测 options 应包含 Anthropic 完整模型 ID claude-opus-4-8')
assert(supportedModelIds.includes('claude-opus-4-7'), '模型检测 options 应包含 Anthropic 完整模型 ID claude-opus-4-7')
assert(supportedModelIds.includes('glm-5.2'), '模型检测 options 应包含 GLM 完整模型 ID glm-5.2')
assert(supportedModelIds.includes('glm-5.1'), '模型检测 options 应包含 GLM 完整模型 ID glm-5.1')
assert(supportedModelIds.includes('deepseek-v4-flash'), '模型检测 options 应包含 DeepSeek 完整模型 ID deepseek-v4-flash')
assert(supportedModelIds.includes('deepseek-v4-pro'), '模型检测 options 应包含 DeepSeek 完整模型 ID deepseek-v4-pro')
assert(supportedModelIds.includes('gemini-3.5-flash'), '模型检测 options 应包含 Gemini 完整模型 ID gemini-3.5-flash')
assert(supportedModelIds.includes('gemini-3.1-pro-preview'), '模型检测 options 应包含 Gemini 完整模型 ID gemini-3.1-pro-preview')
assert(!supportedModelIds.includes('opus-4-8'), '模型检测 options 不应暴露 Anthropic 缩写模型 ID')
assert.match(modelCheckSupportedProtocolLabel, /Anthropic Messages/, '模型检测当前能力边界应包含 Anthropic Messages')
assert.equal(isModelCheckSupportedProtocolProfile({ providerCode: 'openai', providerProtocolProfileId: 'profile_openai_openai_v1', protocolCode: 'openai', protocolVersion: 'v1' }), true, 'OpenAI-compatible Responses 账户应支持模型检测')
assert.equal(isModelCheckSupportedProtocolProfile({ providerCode: 'deepseek', providerProtocolProfileId: 'profile_deepseek_openai_v1', protocolCode: 'openai', protocolVersion: 'v1' }), true, 'DeepSeek Chat 账户应支持模型检测')
assert.equal(isModelCheckSupportedProtocolProfile({ providerCode: 'glm', providerProtocolProfileId: 'profile_glm_general_openai_v1', protocolCode: 'openai', protocolVersion: 'v1' }), true, 'GLM OpenAI Chat 账户应支持模型检测')
assert.equal(isModelCheckSupportedProtocolProfile({ providerCode: 'anthropic', providerProtocolProfileId: 'profile_anthropic_anthropic_v1', protocolCode: 'anthropic', protocolVersion: 'v1' }), true, 'Anthropic v1 协议账户应支持模型检测')
assert.equal(isModelCheckSupportedProtocolProfile({ providerCode: 'gemini', providerProtocolProfileId: 'profile_gemini_native_v1beta', protocolCode: 'gemini', protocolVersion: 'v1beta' }), true, 'Gemini native 账户应支持模型检测')
const trustedComparisonMessage = options.trustedComparison.message ?? ''
assert.match(trustedComparisonMessage, /Anthropic Messages/, '可信对比文案应描述多供应商协议能力边界')
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
