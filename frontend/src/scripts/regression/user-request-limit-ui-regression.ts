import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = (relativePath: string) => readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')

const settingsSource = source('../../views/settings/SettingsView.vue')
const settingsFormSource = source('../../views/settings/settingsForm.ts')
const systemAccountsSource = source('../../views/system-accounts/SystemAccountsView.vue')
const profileSource = source('../../views/profile/ProfileView.vue')
const identitySource = source('../../types/domain/identity.ts')

for (const field of [
  'gatewayUserRequestLimitPerMinute',
  'gatewayUserRequestLimitPerDay',
  'gatewayUserRequestLimitPerWeek',
  'gatewayUserRequestLimitPerMonth'
]) {
  assert(settingsSource.includes(field), `系统设置缺少用户请求限制字段：${field}`)
  assert(settingsFormSource.includes(field), `系统设置表单契约缺少字段：${field}`)
}
assert.match(settingsSource, /sectionErrors\['user-request-limit'\][\s\S]*retrySection\('user-request-limit'\)/, '用户请求限制分区加载失败后必须提供重试入口')
assert.equal((settingsSource.match(/:precision="0"/g) ?? []).length >= 4, true, '全局请求限制输入必须限制为整数')

assert.match(systemAccountsSource, /留空继承全局，填写 0 表示该用户无限/, '系统账户编辑必须说明三态语义')
assert.match(systemAccountsSource, /requestLimitExpiresOn[\s\S]*value-format="YYYY-MM-DD"/, '系统账户编辑必须支持年月日到期日')
assert.match(systemAccountsSource, /次日 00:00 起按系统统计时区自动继承全局/, '到期日必须说明自然日与统计时区语义')
assert.match(systemAccountsSource, /requestLimits:\s*updated\.requestLimits/, '清空全部覆盖后必须显式清除列表行中的旧 requestLimits')
assert.match(systemAccountsSource, /normalizedOptionalRequestLimit/, '系统账户提交前必须校验可选限制为整数')
assert.equal((systemAccountsSource.match(/:precision="0"/g) ?? []).length >= 4, true, '用户覆盖输入必须限制为整数')

assert.match(identitySource, /export interface EffectiveUserRequestLimits/, '前端必须声明最终请求限制契约')
assert.match(identitySource, /expiresOn\?: string[\s\S]*overrideActive: boolean/, '前端必须声明用户覆盖到期契约')
assert.match(profileSource, /profile\.effectiveRequestLimits\.timezone/, '个人信息必须展示请求限制统计时区')
assert.match(profileSource, /单独配置[\s\S]*全局默认/, '个人信息必须区分用户覆盖与全局默认')
assert.match(profileSource, /limit === 0 \? '无限制'/, '无限请求限制必须显示为“无限制”')
assert.match(profileSource, /overrideExpiresOn[\s\S]*当前已继承全局/, '个人信息必须展示覆盖到期状态')

console.log('USER_REQUEST_LIMIT_UI_REGRESSION_OK')
