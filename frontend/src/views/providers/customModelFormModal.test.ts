import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

import type { ProviderModelUpsertPayload } from '@/types/domain'
import {
  buildCustomModelMutationPatch,
  buildCustomModelPayload,
  emptyCustomModelForm,
  hasCustomModelMutationChanges,
  type CustomModelForm
} from './customProviderModelForm'

// 契约：docs/functions/模型广场页与供应商目录退役设计.md §3、§4.3。
// 表单创建/编辑载荷的构建核心在 customProviderModelForm 纯函数域，直接测函数；
// 注：vitest 配置未接入 @vitejs/plugin-vue（依赖也无 @vue/test-utils），
// .vue 文件无法在单测中导入挂载，CustomModelFormModal 的薄层组装（归属 pin、
// builtin 编辑门控、expectedUpdatedAt 组装、saved 契约、api 接线）按项目惯例
// 对组件源码做锚点断言（UsageSummaryTags.test.ts 先例）。

const componentSource = readFileSync(path.resolve(process.cwd(), 'src/views/providers/CustomModelFormModal.vue'), 'utf8')
// 模板含嵌套 <template v-if> 块，取最后一个 </template> 才是根模板闭合。
const templateSource = componentSource.slice(componentSource.indexOf('<template>'), componentSource.lastIndexOf('</template>'))
const plainScriptSource = componentSource.slice(
  componentSource.indexOf('<script lang="ts">'),
  componentSource.indexOf('<script setup lang="ts">')
)
const setupSource = componentSource.slice(componentSource.indexOf('<script setup lang="ts">'))

function personalTextForm(overrides: Partial<CustomModelForm> = {}): CustomModelForm {
  return {
    ...emptyCustomModelForm,
    scope: 'personal',
    model: ' gpt-5.5-pro ',
    status: 'active',
    mode: 'text',
    supportedApiProtocols: ['responses', 'chat_completions'],
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['high'],
    inputUsdPer1M: 1.25,
    outputUsdPer1M: 10,
    serviceTierPrices: { priority: { inputUsdPer1M: 2.5 } },
    ...overrides
  }
}

describe('创建载荷构建（customProviderModelForm 纯函数域）', () => {
  it('personal scope 载荷：model 去空格、字段全集随载荷下发', () => {
    const payload = buildCustomModelPayload(personalTextForm({ configurationTemplateId: 'tpl-1' }), 'text', {
      includeRequestCapabilities: true,
      includePrices: true,
      includeDefaultReasoningEffort: false
    })
    expect(payload).toMatchObject({
      scope: 'personal',
      model: 'gpt-5.5-pro',
      status: 'active',
      mode: 'text',
      configurationTemplateId: 'tpl-1',
      supportedApiProtocols: ['responses', 'chat_completions'],
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high'],
      defaultReasoningEffort: null,
      inputUsdPer1M: 1.25,
      outputUsdPer1M: 10,
      serviceTierPrices: { priority: { inputUsdPer1M: 2.5 } }
    })
  })

  it('模型 ID 为空白时无载荷（表单层提示「请填写模型 ID」的判定基础）', () => {
    const payload = buildCustomModelPayload(personalTextForm({ model: '   ' }), 'text')
    expect(payload).toBeUndefined()
  })
})

describe('编辑差量 PATCH 载荷（customProviderModelForm 纯函数域）', () => {
  const baseline: Partial<ProviderModelUpsertPayload> = {
    status: 'active',
    mode: 'text',
    inputUsdPer1M: 1.25,
    outputUsdPer1M: 10,
    serviceTierPrices: { priority: { inputUsdPer1M: 2.5 } }
  }

  it('基线与当前载荷比对仅保留变更字段，携带 expectedUpdatedAt 组装为 PATCH 载荷', () => {
    const current = { ...baseline, outputUsdPer1M: 12 }
    const patch = buildCustomModelMutationPatch(baseline, current)
    expect(Object.keys(patch)).toEqual(['outputUsdPer1M'])
    expect(hasCustomModelMutationChanges(patch)).toBe(true)
    // CustomModelFormModal 的 buildProviderModelEditPatch 即该差量 + 乐观锁版本的组装。
    const patchPayload = { expectedUpdatedAt: '2026-10-28T00:00:00Z', ...patch }
    expect(patchPayload).toEqual({ expectedUpdatedAt: '2026-10-28T00:00:00Z', outputUsdPer1M: 12 })
  })

  it('与基线完全一致时无变更，不发 PATCH（表单层提示「没有需要保存的修改」）', () => {
    const patch = buildCustomModelMutationPatch(baseline, { ...baseline })
    expect(hasCustomModelMutationChanges(patch)).toBe(false)
  })

  it('内置模型编辑载荷：defaultReasoningEffort 随载荷下发（includeDefaultReasoningEffort: true 门）', () => {
    const payload = buildCustomModelPayload(
      personalTextForm({ defaultReasoningEffort: 'high' }),
      'text',
      { includeRequestCapabilities: true, includePrices: true, includeDefaultReasoningEffort: true }
    )
    expect(payload?.defaultReasoningEffort).toBe('high')
  })
})

describe('CustomModelFormModal 组件契约（源码级锚点）', () => {
  it('builtin 编辑门控：非管理面拒绝 built_in 行，openEdit 首个守卫走该判定', () => {
    expect(plainScriptSource).toContain("if (record.scope === 'built_in' && !isManagementView) return false")
    expect(setupSource).toContain('if (!canEditProviderModelRecord(record, props.isManagementView)) return')
  })

  it('归属 pin：管理面 personal 创建携带归属筛选 query，global 与用户面不传；归属缺失被表单校验拦截', () => {
    expect(plainScriptSource).toContain("if (!isManagementView || scope === 'global') return undefined")
    expect(plainScriptSource).toContain("return trimmed ? { viewScope: 'admin', systemAccountId: trimmed } : { viewScope: 'admin' }")
    expect(setupSource).toContain('请先选择模型归属用户')
    expect(setupSource).toContain("payload.scope === 'personal' && !systemAccountFilterValue.value")
    expect(setupSource).toContain('providerModelOperationQueryParams(')
  })

  it('编辑 PATCH 组装：expectedUpdatedAt + 仅变更字段，编辑后就地合并 props.models 后 emit updated', () => {
    expect(plainScriptSource).toContain('const patch = buildCustomModelMutationPatch(baseline, current)')
    expect(plainScriptSource).toContain('return { expectedUpdatedAt, patch }')
    expect(setupSource).toContain('expectedUpdatedAt: editPatch.expectedUpdatedAt')
    expect(setupSource).toContain('mergeProviderModelMutationResult(props.models, result, editPatch.patch)')
    expect(setupSource).toContain("emit('saved', { kind: 'updated', result: merged })")
  })

  it('saved 契约：created/updated/deleted 三种负载，created/deleted 后由父级重载', () => {
    expect(plainScriptSource).toContain("kind: 'created' | 'updated' | 'deleted'")
    expect(setupSource).toContain("emit('saved', { kind: 'created', result })")
    expect(setupSource).toContain("emit('saved', { kind: 'deleted', result: record })")
  })

  it('api 接线：createModel/updateModel/deleteModel 走现有写接口，删除自含确认与错误原文透出', () => {
    expect(setupSource).toContain('api.providers.updateModel(targetProviderCode, editingCustomModelId.value,')
    expect(setupSource).toContain('api.providers.createModel(targetProviderCode, payload,')
    expect(setupSource).toContain('api.providers.deleteModel(record.providerCode, id)')
    expect(setupSource).toContain('extractModelErrorMessage(error, \'自定义模型删除失败\')')
    expect(setupSource).toContain('Modal.confirm(')
  })

  it('公开入口：openCreate/openEdit/deleteCustomModel 经 defineExpose 暴露', () => {
    expect(setupSource).toContain('defineExpose({')
    expect(setupSource).toMatch(/defineExpose\(\{[^}]*openCreate[^}]*openEdit[^}]*deleteCustomModel/s)
  })

  it('模板契约：820px modal 与字段全集自 ProvidersView 原样平移', () => {
    expect(templateSource).toContain('width="820px"')
    for (const label of [
      '配置模板',
      '作用域',
      '个人模型',
      '全局模型',
      '模型 ID',
      '状态',
      '用途',
      '接口协议',
      '服务等级',
      '思考能力',
      '默认思考级别',
      '发布时间',
      '停用日期',
      '上下文',
      '最大输入',
      '最大输出'
    ]) {
      expect(templateSource).toContain(label)
    }
    expect(setupSource).toContain('Token 计费 · USD / 1M Token')
    expect(setupSource).toContain('图像计费 · USD')
    expect(setupSource).toContain('音频计费 · USD')
  })
})
