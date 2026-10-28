import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// 模型广场页与供应商目录退役设计 §4.3/§5：模型写操作接线已从 ProvidersView 抽到
// CustomModelFormModal（api 调用留在组件内），编辑后就地替换在 ModelPlazaView 消费 saved 负载。
const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const formModalSource = readSource('src/views/providers/CustomModelFormModal.vue')
const plazaViewSource = readSource('src/views/providers/ModelPlazaView.vue')

assertIncludes(
  formModalSource,
  'editingCustomModelProviderCode',
  '自定义模型编辑态应记录来源供应商'
)
assertIncludes(
  formModalSource,
  'const targetProviderCode = editingCustomModelId.value',
  '保存自定义模型时应按编辑对象的来源供应商路由'
)
assertIncludes(
  formModalSource,
  'const result = await api.providers.updateModel(targetProviderCode, editingCustomModelId.value, {',
  '更新自定义模型应调用来源供应商的模型接口'
)
assertIncludes(
  formModalSource,
  'await api.providers.deleteModel(record.providerCode, id)',
  '删除自定义模型应调用来源供应商的模型接口'
)
assertIncludes(
  formModalSource,
  'mergeProviderModelMutationResult(props.models, result, editPatch.patch)',
  '编辑模型后应使用窄响应就地合并当前行'
)
assertIncludes(
  formModalSource,
  "emit('saved', { kind: 'updated', result: merged })",
  '编辑成功应以 updated 负载通知挂载方就地替换'
)
assertIncludes(
  plazaViewSource,
  "if (payload.kind === 'updated') {",
  '编辑成功应就地替换同 id 行，不得整表重载'
)
assertIncludes(
  plazaViewSource,
  'void reloadPlaza()',
  '创建与删除成功后应重载聚合数据'
)
assertNotIncludes(
  formModalSource,
  'result.defaultHealthCheckModelCleared',
  '默认检查模型已改为目录派生，窄响应不得再维护可写默认标记'
)
assertNotIncludes(
  formModalSource,
  'record.providerCode !== activeProvider.value.code',
  '聚合目录里可见的自定义模型不应因来源供应商不同而隐藏操作'
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
