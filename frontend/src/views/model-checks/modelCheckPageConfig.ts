import type { ModelCheckOptions } from '@/types/domain'

export const modelCheckFallbackOptions: ModelCheckOptions = {
  supportedModels: [
    { value: 'gpt-5.6-sol', label: 'gpt-5.6-sol' },
    { value: 'gpt-5.6-terra', label: 'gpt-5.6-terra' },
    { value: 'gpt-5.6-luna', label: 'gpt-5.6-luna' },
    { value: 'gpt-5.5', label: 'gpt-5.5' },
    { value: 'gpt-5.4', label: 'gpt-5.4' },
    { value: 'claude-opus-4-8', label: 'claude-opus-4-8' },
    { value: 'claude-opus-4-7', label: 'claude-opus-4-7' },
    { value: 'glm-5.2', label: 'glm-5.2' },
    { value: 'glm-5.1', label: 'glm-5.1' },
    { value: 'deepseek-v4-flash', label: 'deepseek-v4-flash' },
    { value: 'deepseek-v4-pro', label: 'deepseek-v4-pro' },
    { value: 'gemini-3.5-flash', label: 'gemini-3.5-flash' },
    { value: 'gemini-3.1-pro-preview', label: 'gemini-3.1-pro-preview' }
  ],
  supportedProfiles: [
    { value: 'full', label: '强诊断完整检测', description: '准确优先，不以成本和耗时为约束' }
  ],
  trustedComparison: { enabledByDefault: false, available: true, message: '可信对比默认关闭；选择可信账户后会额外消耗该账户额度。' },
  defaultModel: 'gpt-5.6-sol',
  defaultProfile: 'full'
}

export const modelCheckHistoryColumns: Array<Record<string, any>> = [
  { title: '目标', key: 'target', width: 280 },
  { title: '账户类型', key: 'targetType', width: 110 },
  { title: '供应商', key: 'providerCode', width: 110 },
  { title: '模型', key: 'model', width: 130 },
  { title: '状态', key: 'status', width: 110 },
  { title: '级别', key: 'level', width: 100 },
  { title: '摘要', key: 'summary', width: 320 },
  { title: '创建时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

export const modelCheckPageSize = 20
