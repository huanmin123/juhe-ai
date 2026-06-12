export const responseInspectionPolicyGuideSources = [
  {
    key: 'runtime-logs',
    name: '运行日志',
    where: '系统运维 / 运行日志，按 traceId、response_inspection 或策略名称搜索',
    note: '适合确认策略是否命中、写出状态、处置动作和重试结果。'
  },
  {
    key: 'audit-logs',
    name: '审计日志',
    where: '系统运维 / 审计日志，查看失败请求、响应检查和上游错误摘要',
    note: '适合定位上游返回的 error.code、error.type、状态码和响应结构。'
  },
  {
    key: 'usage-records',
    name: '使用记录',
    where: '使用记录列表，按 traceId 或账户筛选异常请求',
    note: '适合确认最终是否失败、耗时、成本、命中账号和后续重试 / 避让效果。'
  }
]

export const responseInspectionPolicyGuideFields = [
  {
    key: 'outputTextIncludes',
    field: '输出文本包含',
    source: 'Chat message.content、Responses output_text 或对应 SSE 增量文本',
    example: '公益服务器, subscribe',
    note: '适合识别广告污染、异常提示或固定污染文案。'
  },
  {
    key: 'finishReasons',
    field: '完成原因 / 状态',
    source: 'Chat choices[].finish_reason、Responses status 或失败事件状态',
    example: 'failed, content_filter, length',
    note: '适合识别协议内失败、内容过滤或异常结束。'
  },
  {
    key: 'errorCodes',
    field: 'error.code',
    source: 'Chat / Responses JSON 或 SSE 事件中的 error.code',
    example: 'cyber_policy',
    note: '适合拦截明确错误码或中转自定义错误码。'
  },
  {
    key: 'errorTypes',
    field: 'error.type',
    source: 'Chat / Responses JSON 或 SSE 事件中的 error.type',
    example: 'server_error',
    note: '错误码不稳定时作为辅助条件。'
  },
  {
    key: 'errorMessageIncludes',
    field: '错误消息包含',
    source: 'Chat / Responses JSON 或 SSE 事件中的 error.message',
    example: 'upstream policy blocked',
    note: '适合错误码缺失但错误文案稳定的上游。'
  },
  {
    key: 'rawTextIncludes',
    field: '原始事件文本包含',
    source: '当前单个 SSE 事件原文或受限窗口内的原始 JSON 文本',
    example: 'event: response.failed',
    note: '只作为兜底排障条件；优先使用语义字段。'
  },
  {
    key: 'outputTextExcludes',
    field: '输出文本排除',
    source: '已解析出的输出文本',
    example: '正常业务提示',
    note: '作为排除条件使用；输出文本包含这些关键词时，本规则不命中。'
  },
  {
    key: 'jsonPathsExists',
    field: 'JSON字段路径存在',
    source: 'Chat / Responses JSON 或 SSE data JSON 内的字段路径',
    example: 'response.error, error',
    note: '只判断路径是否存在，不匹配字段值；适合判断某类错误结构是否出现。'
  }
]

export const responseInspectionPolicyGuideActions = [
  {
    key: 'observe',
    action: '先观察命中',
    when: '新规则、原始文本条件、或不确定误杀范围时',
    note: '只写日志，不拦截、不重试，适合先观察几轮真实流量。'
  },
  {
    key: 'drop_event',
    action: '只丢弃命中事件',
    when: '命中内容是独立 SSE 广告事件或无害污染事件时',
    note: '只对 SSE 生效：丢掉这一条命中事件，后面的流继续转发；JSON 响应命中时不会使用该处置。'
  },
  {
    key: 'retry_no_avoidance',
    action: '重试但不避让账号',
    when: '当前结果不可接受，但证据不足以避让账号时',
    note: '在可行时重新请求一次，但不拉黑当前账号；重试和后续请求仍可能选到它。'
  },
  {
    key: 'retry_next_account',
    action: '本次重试避开当前账号',
    when: '当前账号本次结果不可接受，但不想影响后续请求时',
    note: '这次重试不再选当前账号；不写短期避让，后续请求仍可使用它。'
  },
  {
    key: 'avoid_account_ttl',
    action: '短期避让当前账号',
    when: '确认当前账号短时间内持续返回污染或错误时',
    note: '按系统临时不可调用策略短期避让当前账号，并在可行时重试。'
  },
  {
    key: 'avoid_upstream_bucket_ttl',
    action: '短期避让上游桶',
    when: '同代理、baseUrl 或供应商协议档案桶内多个账号都可能受影响时',
    note: '按系统临时不可调用策略避让同代理、同 baseUrl 或同供应商协议档案桶的账号，并在可行时重试。'
  }
]

export const responseInspectionPolicyGuideExample = `Chat JSON:
{
  "choices": [
    {
      "message": {
        "content": "公益服务器压力很大，休息十分钟换key开放"
      },
      "finish_reason": "stop"
    }
  ]
}

Responses SSE:
event: response.failed
data: {
  "type": "response.failed",
  "response": {
    "error": {
      "code": "cyber_policy",
      "type": "server_error",
      "message": "upstream policy blocked"
    }
  }
}`
