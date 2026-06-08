export interface AccountErrorPolicyContextGuide {
  emptyDescription: string
}

export interface AccountErrorPolicyGuideField {
  key: string
  field: string
  source: string
  example: string
  note: string
}

export interface AccountErrorPolicyGuideSource {
  key: string
  name: string
  where: string
  note: string
}

const emptyErrorPolicyRulesDescription = '未配置账号专属规则；需要特殊处理时再添加'

export function resolveAccountErrorPolicyContextGuide(_input: {
  accountType?: string
  baseUrl?: string
  providerCode?: string
}): AccountErrorPolicyContextGuide {
  return {
    emptyDescription: emptyErrorPolicyRulesDescription
  }
}

export const accountErrorPolicyGuideFields: AccountErrorPolicyGuideField[] = [
  {
    key: 'status_codes',
    field: '状态码',
    source: '上游 HTTP 状态码',
    example: '429, 502, 503',
    note: '只填数字；多个值用逗号、分号或换行分隔'
  },
  {
    key: 'error_codes',
    field: '错误码',
    source: '响应体 error.code；同时读取 root.code',
    example: 'insufficient_quota',
    note: '适合稳定机器码；不要填中文说明'
  },
  {
    key: 'error_types',
    field: '错误类型',
    source: '响应体 error.type；同时读取 root.type',
    example: 'rate_limit_exceeded',
    note: '用于区分同状态码下不同语义'
  },
  {
    key: 'keywords',
    field: '关键词',
    source: '完整上游响应正文，通常来自 error.message',
    example: 'quota exceeded, 额度不足',
    note: '适合上游只提供文本错误时使用'
  }
]

export const accountErrorPolicyGuideSources: AccountErrorPolicyGuideSource[] = [
  {
    key: 'audit',
    name: '原始审计日志',
    where: '找到失败请求，查看上游尝试的状态码、响应体和错误摘要',
    note: '最接近真实客户端链路，适合生产问题取样'
  },
  {
    key: 'test',
    name: '账号测试结果',
    where: '账号测试弹窗的完整 JSON',
    note: '适合验证凭据、代理和单账号可用性'
  },
  {
    key: 'logs',
    name: '运行日志',
    where: '按 traceId 搜索 gateway / upstream 相关日志',
    note: '适合关联网关判断、切号和账号副作用'
  }
]
