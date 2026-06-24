import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))

const modalSource = readSource('frontend/src/views/api-keys/ApiKeyEditModal.vue')
const accessTypesSource = readSource('frontend/src/types/domain/access.ts')
const apiKeyFormatterSource = readSource('frontend/src/views/api-keys/apiKeyFormatters.ts')
const apiKeyListSource = readSource('frontend/src/views/api-keys/ApiKeyResponsiveList.vue')
const apiKeysApiSource = readSource('frontend/src/api/domains/apiKeys.ts')

assert(modalSource.includes('scoringFallbackMaxLevel'), 'API Key 混合表单必须使用评分不可用兜底上限字段')
assert(!modalSource.includes('failureDefaultLevel'), 'API Key 混合表单不得继续暴露失败参考等级旧字段')
assert(modalSource.includes('评分不可用兜底上限'), 'API Key 混合表单应展示评分不可用兜底上限中文字段')
assert(modalSource.includes('评分模型不可用时，系统只在该等级以内从低到高寻找可用目标模型'), '评分不可用兜底字段必须有成本边界悬浮说明')
assert(modalSource.includes('质量评分不可用默认放行原 200'), '质量评分开关必须提示不可用默认放行')
assert(modalSource.includes('unavailableAction: qualityInspection?.unavailableAction ?? \'pass_through\''), '质量评分不可用默认值必须是放行原 200')
assert(modalSource.includes('混合路由至少需要配置 2 个不同的目标模型'), '前端必须校验至少 2 个不同目标模型')
assert(modalSource.includes('最低档必须从等级 1 开始，并覆盖 1-2 到 1-5 之间的范围'), '前端必须校验最低档范围边界')
assert(modalSource.includes('normalizeIntegerField(form.hybridRoutingConfig.scoringFallbackMaxLevel, 2, 5'), '评分不可用兜底上限必须限制在 2-5')
assert(modalSource.includes('不可用处理'), '质量评分配置必须提供不可用处理选项')

assert(accessTypesSource.includes('ApiKeyHybridQualityInspectionUnavailableAction'), '前端领域类型必须声明质量评分不可用处理枚举')
assert(accessTypesSource.includes('scoringFallbackMaxLevel: number'), '前端领域类型必须声明评分不可用兜底上限字段')
assert(accessTypesSource.includes('unavailableAction: ApiKeyHybridQualityInspectionUnavailableAction'), '前端质量评分类型必须声明不可用处理字段')

assert(apiKeyFormatterSource.includes('评分不可用兜底：1-'), 'API Key 列表混合摘要必须展示评分不可用兜底范围')
assert(apiKeyFormatterSource.includes('不可用${quality.unavailableAction === \'return_error\' ? \'返回错误\' : \'放行原 200\'}'), 'API Key 列表混合摘要必须展示质量评分不可用处理')
assert(apiKeyListSource.includes('class="route-tooltip"'), 'API Key 列表路由模式 tooltip 必须支持多行配置摘要')
assert(apiKeyListSource.includes('white-space: pre-line'), 'API Key 列表路由模式 tooltip 必须保留摘要换行')

assert(apiKeysApiSource.includes('export interface ApiKeyMutationPayload'), 'API Key 前端请求层必须使用显式 mutation payload 类型')
assert(!apiKeysApiSource.includes('payload: Record<string, unknown>'), 'API Key 前端请求层不得继续使用宽泛 Record payload')

console.log('API Key 混合模型前端配置回归通过：字段、提示、校验、列表摘要和请求类型均符合预期')

function readSource(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8')
}
