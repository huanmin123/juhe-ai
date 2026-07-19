import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const helperSource = readFileSync(fileURLToPath(new URL('../../composables/useProviderOptionsResource.ts', import.meta.url)), 'utf8')
assert.match(helperSource, /getDefaultPageDataResourceCache/, '供应商选项应使用统一 IndexedDB resource cache')
assert.match(helperSource, /domain:\s*'providers\.catalog'/, '供应商选项应使用 providers.catalog domain')
assert.match(helperSource, /authState\.currentUser/, '供应商选项缓存必须按当前用户和角色隔离')

for (const relativePath of [
  '../../views/usage-stats/UsageStatsView.vue',
  '../../views/response-inspection-policies/ResponseInspectionPoliciesView.vue',
  '../../views/providers/ProvidersView.vue',
  '../../views/accounts/useAccountListData.ts',
  '../../views/groups/GroupsView.vue'
]) {
  const source = readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')
  assert.match(source, /loadProviderOptionsResource/, `${relativePath} 应复用供应商选项资源入口`)
  assert.doesNotMatch(source, /api\.providers\.options\(/, `${relativePath} 不应继续直接请求供应商 options`)
}

const providersViewSource = readFileSync(fileURLToPath(new URL('../../views/providers/ProvidersView.vue', import.meta.url)), 'utf8')
assert.match(providersViewSource, /async function reloadActiveProviderModels\(force = false\)/, '普通打开模型目录必须默认使用持久缓存，只有显式刷新才清理')
assert.match(providersViewSource, /loadProviderModelCatalogResource\(\{[\s\S]{0,180}force,/, '模型目录 resource 必须透传显式 force，而不是每次硬编码强制刷新')
assert.match(providersViewSource, /modelResult\.confirmation/, 'IndexedDB 快照后台确认发现变化后必须更新当前模型目录')
assert.match(providersViewSource, /modelRequestSequence/, '切换供应商或目标账户时必须阻止旧确认结果覆盖新视图')

console.log('供应商选项持久缓存接线回归通过：常用页面共用 providers.catalog resource')
