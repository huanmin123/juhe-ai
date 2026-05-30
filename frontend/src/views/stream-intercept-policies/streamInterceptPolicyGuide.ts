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
    note: '适合确认最终是否失败、耗时、成本、命中账号和后续切号效果。'
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
    field: '文本包含',
    source: '单个完整 SSE event 的文本内容',
    example: '广告, subscribe',
    note: '只建议处理广告污染或固定文案；文本匹配会跳过图像和超大事件。'
  },
  {
    key: 'jsonPathsExists',
    field: 'JSON 字段存在',
    source: 'SSE data JSON 的字段路径',
    example: 'response.error, error',
    note: '适合判断某类结构是否出现。'
  }
]

export const streamInterceptPolicyGuideActions = [
  {
    key: 'dry-run',
    action: '试运行',
    when: '新规则、文本包含、或不确定误杀范围时',
    note: '只记录命中，不改变下游响应，适合观察几轮真实流量。'
  },
  {
    key: 'discard-stream',
    action: '丢弃当前流',
    when: '上游已经返回污染事件或明确错误，需要阻断继续输出时',
    note: '常与重试、切号和短期避让配合使用。'
  },
  {
    key: 'replace-failure',
    action: '替换为失败事件',
    when: '需要给客户端明确失败信号，而不是静默丢弃流时',
    note: '适合需要向下游明确写出标准失败事件的场景。'
  },
  {
    key: 'account-avoidance',
    action: '切号 / 运行态避让',
    when: '同一账号或同一上游桶短时间持续污染或失败时',
    note: '避让只影响运行态，不会直接停用账户。'
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
