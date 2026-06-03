export const streamInterceptPolicyGuideSources = [
  {
    key: 'runtime-logs',
    name: '运行日志',
    where: '系统运维 / 运行日志，按 traceId、stream_intercept 或策略名称搜索',
    note: '适合确认策略是否命中、写出状态、处置动作和重试结果。'
  },
  {
    key: 'audit-logs',
    name: '审计日志',
    where: '系统运维 / 审计日志，查看失败请求、流式中断和上游错误摘要',
    note: '适合定位上游返回的 error.code、error.type、状态码和响应结构。'
  },
  {
    key: 'usage-records',
    name: '使用记录',
    where: '使用记录列表，按 traceId 或账户筛选异常请求',
    note: '适合确认最终是否失败、耗时、成本、命中账号和后续重试 / 避让效果。'
  }
]

export const streamInterceptPolicyGuideFields = [
  {
    key: 'eventTypes',
    field: 'SSE event 类型',
    source: 'SSE 每个事件的 event 行，例如 event: response.failed',
    example: 'response.failed, error',
    note: '优先使用，稳定且误杀少。'
  },
  {
    key: 'dataTypes',
    field: 'data.type',
    source: 'SSE data JSON 内的 type 字段',
    example: 'response.output_text.delta',
    note: '适合按上游事件类型识别。'
  },
  {
    key: 'errorCodes',
    field: 'error.code',
    source: 'SSE data JSON 内 error.code',
    example: 'cyber_policy',
    note: '适合拦截明确错误码或中转自定义错误码。'
  },
  {
    key: 'errorTypes',
    field: 'error.type',
    source: 'SSE data JSON 内 error.type',
    example: 'server_error',
    note: '错误码不稳定时作为辅助条件。'
  },
  {
    key: 'textIncludes',
    field: 'SSE data文本包含',
    source: '当前单个 SSE 事件里 data: 后面的文本',
    example: '广告, subscribe',
    note: '只匹配当前事件，不拼接整条响应；建议处理广告污染或固定文案，图像和超大事件会跳过文本扫描。'
  },
  {
    key: 'textExcludes',
    field: 'SSE data文本不包含',
    source: '当前单个 SSE 事件里 data: 后面的文本',
    example: '正常业务提示',
    note: '作为排除条件使用；当前事件 data 文本包含这些关键词时，本规则不命中。'
  },
  {
    key: 'jsonPathsExists',
    field: 'JSON字段路径存在',
    source: 'SSE data JSON 内的字段路径',
    example: 'response.error, error',
    note: '只判断路径是否存在，不匹配字段值；适合判断某类错误结构是否出现。'
  }
]

export const streamInterceptPolicyGuideActions = [
  {
    key: 'observe',
    action: '先观察命中',
    when: '新规则、SSE data文本包含、或不确定误杀范围时',
    note: '只记录命中，不改变下游响应，适合观察几轮真实流量。'
  },
  {
    key: 'drop_event',
    action: '只丢弃命中事件',
    when: '命中内容是独立广告事件或无害污染事件时',
    note: '不触发重试，也不改变账号候选。'
  },
  {
    key: 'fail_stream',
    action: '结束当前流',
    when: '明确失败但不希望触发重试或账号避让时',
    note: '向下游写普通失败事件。'
  },
  {
    key: 'retry_no_avoidance',
    action: '重试但不避让账号',
    when: '当前结果不可接受，但证据不足以避让账号时',
    note: '触发可行的重试，不改变后续账号候选。'
  },
  {
    key: 'retry_next_account',
    action: '本次重试避开当前账号',
    when: '当前账号本次结果不可接受，但不想影响后续请求时',
    note: '只影响本次服务端重试，不写入短期避让状态。'
  },
  {
    key: 'avoid_account_ttl',
    action: '短期避让当前账号',
    when: '确认当前账号短时间内持续返回污染或错误时',
    note: '短期从候选中避让当前账号，并触发可行的重试。'
  },
  {
    key: 'avoid_upstream_bucket_ttl',
    action: '短期避让上游桶',
    when: '同代理、baseUrl 或供应商桶内多个账号都可能受影响时',
    note: '短期避让同桶候选，并触发可行的重试。'
  }
]

export const streamInterceptPolicyGuideExample = `event: response.failed
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
