import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const projectRoot = resolve('..')

const accountRulesSource = readProjectFile('frontend/src/views/accounts/accountRules.ts')
const accountMenuActionsSource = readProjectFile('frontend/src/views/accounts/useAccountMenuActions.ts')
const accountReauthorizeSource = readProjectFile('frontend/src/views/accounts/useAccountReauthorize.ts')
const accountUsageRegressionSource = readProjectFile('frontend/src/scripts/regression/account-usage-formatters-regression.ts')
const modelCheckServiceSource = readProjectFile('backend/src/modules/model-checks/model-checks.service.ts')

assert.doesNotMatch(
  accountRulesSource,
  new RegExp(['canManage', 'GptOAuth'].join('')),
  '账号菜单规则不应继续使用 GPT 命名的 OAuth 管理函数'
)
assert.doesNotMatch(
  accountMenuActionsSource,
  new RegExp(['只有自有 GPT', ' OAuth 账户'].join('')),
  '刷新令牌提示不应绑定 GPT 供应商名'
)
assert.doesNotMatch(
  accountReauthorizeSource,
  new RegExp(['只有自有 GPT', ' OAuth 账户'].join('')),
  '重新授权提示不应绑定 GPT 供应商名'
)
assert.doesNotMatch(
  accountUsageRegressionSource,
  new RegExp(['非 GPT 供应商', '不应展示 OAuth'].join('')),
  '账户用量回归不应再用 GPT 供应商口径描述 OAuth 用量能力'
)

assert.match(
  modelCheckServiceSource,
  /modelCheckUnsupportedProtocolMessage\(\)/,
  '模型检测协议能力错误文案应通过本地函数复用'
)
assert.doesNotMatch(
  modelCheckServiceSource,
  /replace\('当前仅支持检测 OpenAI v1/,
  '模型检测可信对比错误改写不应硬编码 OpenAI v1 文案'
)
assert.equal(
  existsSync(resolve(projectRoot, 'backend/src/modules/gateway/client-profiles/compatibility-policy.ts')),
  false,
  '网关不应再保留失败后请求体改写策略模块'
)

console.log('供应商边界源码回归通过：账号 OAuth 管理、用量断言、模型检测错误文案和失败后请求体改写模块均符合当前边界')

function readProjectFile(relativePath: string): string {
  return readFileSync(resolve(projectRoot, relativePath), 'utf8')
}
