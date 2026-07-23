import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const view = readFileSync(new URL('../../views/providers/ProvidersView.vue', import.meta.url), 'utf8')
const api = readFileSync(new URL('../../api/domains/providers.ts', import.meta.url), 'utf8')
const resource = readFileSync(new URL('../../composables/useProviderOptionsResource.ts', import.meta.url), 'utf8')

assert.match(api, /listItems:.*\/providers\/list/, '首屏必须使用窄 provider list')
assert.match(api, /detail:.*\/providers\//, '模型目录必须有 provider detail API')
assert.match(resource, /listItemsOnly/, 'provider list resource 必须支持窄 DTO')
assert.match(view, /listItemsOnly: true/, '供应商页面首屏不得预加载完整 protocol profiles')
assert.match(view, /Promise\.all\(\[[\s\S]*api\.providers\.detail\([\s\S]*loadProviderModelCatalogResource/, '打开模型目录必须并行请求 detail 与 models')
assert.match(view, /authState\.revision\.value[\s\S]*viewer\?\.id[\s\S]*viewer\?\.role[\s\S]*isManagementView\.value[\s\S]*providerCode/, '模型请求签名必须包含 auth revision、viewer、viewScope 与 provider code')
assert.match(view, /onDeactivated\([\s\S]*invalidateProviderPageRequests/, 'KeepAlive 停用必须作废模型请求')
assert.match(view, /onBeforeUnmount\(invalidateProviderPageRequests\)/, '卸载必须作废模型请求')
assert.match(view, /onActivated\([\s\S]*loadProviders\(true\)/, '重新激活必须刷新供应商列表')
assert.match(view, /watch\(\(\) => authState\.revision\.value[\s\S]*resetModelModal\(\)/, '身份切换必须关闭旧模型弹窗')
console.log('provider-catalog-progressive-loading-regression: ok')
