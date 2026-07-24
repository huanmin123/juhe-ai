import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const apiKeyModalSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeyEditModal.vue', import.meta.url)),
  'utf8'
)
const routeStrategiesViewSource = readFileSync(
  fileURLToPath(new URL('../../views/route-strategies/RouteStrategiesView.vue', import.meta.url)),
  'utf8'
)

assert.match(apiKeyModalSource, /<a-input v-model:value="form\.name" :disabled="editingNameLocked"/, '默认 API Key 和 AI 对话 API Key 的名称输入必须禁用')
assert.match(apiKeyModalSource, /editingNameLocked\.value = apiKey\.isDefault === true \|\| apiKey\.purpose === 'chat'/, 'API Key 名称锁定必须同时识别默认标识和 AI 对话用途')
assert.match(apiKeyModalSource, /editingNameLocked\.value = false/, '新建 API Key 时名称输入必须保持可编辑')

assert.match(routeStrategiesViewSource, /<a-input v-model:value="form\.name" :disabled="editingIsDefault"/, '默认策略路由的名称输入必须禁用')
assert.match(routeStrategiesViewSource, /editingIsDefault\.value = record\.isDefault/, '策略路由名称锁定必须使用默认标识')
assert.match(routeStrategiesViewSource, /editingIsDefault\.value = false/, '新建策略路由时名称输入必须保持可编辑')

console.log('默认资源名称锁定前端回归通过')
