import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const providersViewSource = readSource('src/views/providers/ProvidersView.vue')
const providerModelOptionsSource = readSource('src/views/accounts/useAccountProviderModelOptions.ts')

assertIncludes(
  providersViewSource,
  'editingCustomModelProviderCode',
  '自定义模型编辑态应记录来源供应商'
)
assertIncludes(
  providersViewSource,
  'const targetProviderCode = editingCustomModelId.value',
  '保存自定义模型时应按编辑对象的来源供应商路由'
)
assertIncludes(
  providersViewSource,
  'await api.providers.updateModel(targetProviderCode, editingCustomModelId.value, payload)',
  '更新自定义模型应调用来源供应商的模型接口'
)
assertIncludes(
  providersViewSource,
  'await api.providers.deleteModel(record.providerCode, record.id)',
  '删除自定义模型应调用来源供应商的模型接口'
)
assertIncludes(
  providersViewSource,
  'invalidateAccountProviderModelOptionsCache()',
  '自定义模型变更后应清理全部账户模型选项缓存'
)
assertNotIncludes(
  providersViewSource,
  'record.providerCode !== activeProvider.value.code',
  '聚合目录里可见的自定义模型不应因来源供应商不同而隐藏操作'
)
assertIncludes(
  providerModelOptionsSource,
  'export function invalidateAccountProviderModelOptionsCache(providerCode?: string): void',
  '账户模型选项缓存应支持按供应商失效'
)

console.log('供应商模型目录操作回归通过：聚合目录可见自定义模型按来源供应商发起编辑 / 删除')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}

function assertIncludes(source: string, pattern: string, message: string): void {
  if (!source.includes(pattern)) {
    throw new Error(`${message}：缺少 ${pattern}`)
  }
}

function assertNotIncludes(source: string, pattern: string, message: string): void {
  if (source.includes(pattern)) {
    throw new Error(`${message}：不应出现 ${pattern}`)
  }
}
