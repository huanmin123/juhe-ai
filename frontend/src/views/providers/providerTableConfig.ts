import type { RowActionItem } from '@/components/rowActions'

const managementProviderColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 160 },
  { title: '状态', key: 'status', width: 90 },
  { title: '账户类型', key: 'accountTypes', width: 180 },
  { title: '接口能力', key: 'capabilities', width: 280 },
  { title: '默认 Base URL', dataIndex: 'baseUrl', key: 'baseUrl', width: 240 },
  { title: '默认测试模型', dataIndex: 'defaultTestModel', key: 'defaultTestModel', width: 160 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

const selfProviderColumns = [
  { title: '模型目录', dataIndex: 'name', key: 'name', width: 180 },
  { title: '状态', key: 'status', width: 90 },
  { title: '接口能力', key: 'capabilities', width: 260 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 260 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

export function providerColumnsForScope(isManagementView: boolean) {
  return isManagementView ? managementProviderColumns : selfProviderColumns
}

export function providerScrollXForScope(isManagementView: boolean): number {
  return isManagementView ? 1320 : 850
}

export function providerEmptyDescriptionForScope(isManagementView: boolean): string {
  return isManagementView
    ? '当前内置 OpenAI 兼容、GPT 与 Anthropic 供应商，后续新供应商会在这里扩展。'
    : '当前没有可用模型目录。'
}

export function providerActionsForScope(isManagementView: boolean): RowActionItem[] {
  return [
    { key: 'models', label: isManagementView ? '模型目录' : '查看模型', icon: 'detail', tone: 'info' }
  ]
}
