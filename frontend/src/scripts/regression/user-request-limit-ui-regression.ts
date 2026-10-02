import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = (relativePath: string) => readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')

const settingsSource = source('../../views/settings/SettingsView.vue')
const settingsDisplaySource = source('../../views/settings/settingsDisplay.ts')
const settingsFormSource = source('../../views/settings/settingsForm.ts')
const systemAccountsSource = source('../../views/system-accounts/SystemAccountsView.vue')
const systemAccountEditFormSource = source('../../views/system-accounts/systemAccountEditForm.ts')
const profileSource = source('../../views/profile/ProfileView.vue')
const identitySource = source('../../types/domain/identity.ts')

for (const field of [
  'gatewayUserRequestLimitPerMinute',
  'gatewayUserRequestLimitPerDay',
  'gatewayUserRequestLimitPerWeek',
  'gatewayUserRequestLimitPerMonth',
  'userAiAccountLimit'
]) {
  // 2026-10-02 方案 C 重设计：字段注册表（settingsDisplay.ts）是字段事实源，视图按注册表渲染
  assert(settingsDisplaySource.includes(field), `系统设置字段注册表缺少用户限制字段：${field}`)
  assert(settingsFormSource.includes(field), `系统设置表单契约缺少字段：${field}`)
}
assert(settingsDisplaySource.includes('用户限制'), '系统设置分区应使用用户限制名称')
assert.match(settingsSource, /groupErrorMessage[\s\S]*重新加载[\s\S]*retryGroup/, '用户限制所在分组加载失败后必须提供重试入口')
// 2026-10-02 日志与审计组：数字精度改为注册表驱动（缺省 0 = 整数；仅审计成功采样率为 4 位小数）
assert.match(settingsSource, /:precision="entry\.precision \?\? 0"/, '数字输入精度按注册表统一渲染，缺省必须为整数')
assert.equal((settingsDisplaySource.match(/precision: \d+/g) ?? []).length, 1, '仅审计成功采样率可输入小数，其余设置数字字段保持整数')

assert.match(systemAccountsSource, /<strong>用户限制<\/strong>/, '系统账户编辑应使用用户限制名称')
assert.match(systemAccountsSource, /留空继承全局，填写 0 表示不限/, '系统账户编辑必须说明三态语义')
assert.match(systemAccountsSource, /AI 账户数量限制[\s\S]*form\.aiAccountLimit/, '系统账户编辑必须支持 AI 账户数量覆盖')
assert.match(systemAccountsSource, /requestLimitExpiresOn[\s\S]*value-format="YYYY-MM-DD"/, '系统账户编辑必须支持年月日到期日')
assert.match(systemAccountsSource, /请求限制可设置到期日/, '到期日必须说明自然日与统计时区语义')
assert.match(systemAccountEditFormSource, /next\.requestLimits = mutation\.requestLimits \?\? undefined/, '清空全部覆盖后必须显式清除列表行中的旧 requestLimits')
assert.match(systemAccountEditFormSource, /next\.aiAccountLimit = mutation\.aiAccountLimit \?\? undefined/, '清空 AI 账户数量覆盖后必须显式清除列表行中的旧值')
assert.match(systemAccountsSource, /normalizedOptionalRequestLimit/, '系统账户提交前必须校验可选限制为整数')
assert.match(systemAccountsSource, /normalizedOptionalAiAccountLimit/, '系统账户提交前必须校验 AI 账户数量覆盖为整数')
assert.equal((systemAccountsSource.match(/:precision="0"/g) ?? []).length >= 5, true, '用户覆盖输入必须限制为整数')

assert.match(identitySource, /export interface EffectiveUserRequestLimits/, '前端必须声明最终请求限制契约')
assert.match(identitySource, /expiresOn\?: string[\s\S]*overrideActive: boolean/, '前端必须声明用户覆盖到期契约')
assert.doesNotMatch(profileSource, /effectiveRequestLimits|请求限制|统计时区/, '个人信息页不得展示用户无需关心的请求限制与统计时区')

console.log('USER_REQUEST_LIMIT_UI_REGRESSION_OK')
