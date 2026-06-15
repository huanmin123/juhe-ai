export const responseInspectionPolicyGuideSources = [
  {
    key: 'runtime-logs',
    name: '运行日志',
    where: '系统运维 / 运行日志，按 traceId、response_inspection 或策略名称搜索',
    note: '看这条规则有没有命中、当时执行了什么处置、有没有触发重试。'
  },
  {
    key: 'audit-logs',
    name: '审计日志',
    where: '系统运维 / 审计日志，查看失败请求、响应检查和上游错误摘要',
    note: '看上游实际返回了什么错误码、错误消息、状态码和响应内容。'
  },
  {
    key: 'usage-records',
    name: '使用记录',
    where: '使用记录列表，按 traceId 或账户筛选异常请求',
    note: '看这次请求最后成功还是失败、用了哪个账号、耗时和成本是多少。'
  }
]

export const responseInspectionPolicyGuideFields = [
  {
    key: 'clientProfiles',
    field: '客户端画像',
    source: '检查当前下游请求被网关识别成 Codex 还是通用 OpenAI 客户端',
    example: 'Codex',
    required: '否，适用范围',
    note: '只限制规则适用的客户端范围，不能单独构成命中；Codex 专属错误改写必须用它收窄。'
  },
  {
    key: 'accountClientCompatibilities',
    field: '账号兼容模式',
    source: '检查当前命中账号的客户端兼容模式',
    example: 'Codex Responses',
    required: '否，适用范围',
    note: '适合把规则限制在 Codex Responses 账号；它是账号维度，不等同于下游客户端画像。'
  },
  {
    key: 'outputTextIncludes',
    field: '输出文本包含',
    source: '检查模型最终答复或流式输出里能被用户看到的文字',
    example: '公益服务器, subscribe',
    required: '否，正向条件之一',
    note: '填广告词、污染词或异常提示片段；任意一个片段出现在输出里就命中。'
  },
  {
    key: 'finishReasons',
    field: '完成原因 / 状态',
    source: '检查响应里的 finish_reason、status 这类结束状态',
    example: 'failed, content_filter, length',
    required: '否，正向条件之一',
    note: '必须填完整状态值；例如只想拦内容过滤结束，就填 content_filter。'
  },
  {
    key: 'errorCodes',
    field: 'error.code',
    source: '检查上游返回的 error.code',
    example: 'cyber_policy',
    required: '否，正向条件之一',
    note: '必须和错误码完全一致；适合上游错误码稳定的情况。'
  },
  {
    key: 'errorTypes',
    field: 'error.type',
    source: '检查上游返回的 error.type',
    example: 'server_error',
    required: '否，正向条件之一',
    note: '必须和错误类型完全一致；常和 error.code 一起填，减少误命中。'
  },
  {
    key: 'errorMessageIncludes',
    field: '错误消息包含',
    source: '检查上游返回的 error.message 文本',
    example: 'upstream policy blocked',
    required: '否，正向条件之一',
    note: '填错误消息里比较特别的一小段；错误码太泛时用它补充判断。'
  },
  {
    key: 'rawTextIncludes',
    field: 'SSE 事件原文包含',
    source: '只检查当前这一条 SSE 原文，包括 event: 行和 data: 行',
    example: 'event: response.failed, "type":"error"',
    required: '否，正向条件之一',
    note: '不要把它当成 data 字段；只填一小段原文。能用 error.code、错误消息或 JSON 字段路径时，不用它。'
  },
  {
    key: 'outputTextExcludes',
    field: '输出文本排除',
    source: '检查同一段可见输出文字',
    example: '正常业务提示',
    required: '否，排除条件',
    note: '只有先填了“输出文本包含”才有意义；排除词出现时，这条规则不命中。'
  },
  {
    key: 'jsonPathsExists',
    field: 'JSON字段路径存在',
    source: '检查响应 JSON 里有没有某个字段',
    example: 'response.error, choices.0.message.content',
    required: '否，正向条件之一',
    note: '只判断字段是否存在，不判断字段值；数组下标用 0、1 这种数字。'
  }
]

export const responseInspectionPolicyGuideActions = [
  {
    key: 'observe',
    action: '先观察命中',
    when: '刚写好规则，还不确定会不会误伤正常请求',
    note: '只记日志，不拦截、不重试；先跑几轮看命中记录。'
  },
  {
    key: 'drop_event',
    action: '只丢弃命中事件',
    when: '命中的是单独一条流式广告事件，删掉它不影响后续回答',
    note: '只对 SSE 流生效；丢掉这一条事件，后面的流继续发给客户端。'
  },
  {
    key: 'retry_no_avoidance',
    action: '重试但不避让账号',
    when: '这次响应不能用，但还不能判断账号有问题',
    note: '如果客户端还没收到内容，就重新请求一次；不临时停用当前账号。'
  },
  {
    key: 'retry_next_account',
    action: '本次重试避开当前账号',
    when: '这次响应像是当前账号的问题，但还不想临时停用它',
    note: '只在本次重试里跳过当前账号；后面的新请求仍然可以选到它。'
  },
  {
    key: 'avoid_account_ttl',
    action: '短期避让当前账号',
    when: '当前账号反复返回同类污染或错误',
    note: '把当前账号临时标记为不可用；后续一段时间调度会避开它。'
  },
  {
    key: 'avoid_upstream_bucket_ttl',
    action: '短期避让上游桶',
    when: '同一个代理、Base URL 或供应商入口下的多个账号都可能有问题',
    note: '临时避开同一类上游入口下的账号；影响范围比“短期避让当前账号”更大。'
  }
]

export const responseInspectionPolicyGuideExample = `Chat JSON 示例:
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

Responses SSE 示例:
event: response.failed
data: {"type":"response.failed","response":{"error":{"code":"cyber_policy","type":"server_error","message":"upstream policy blocked"}}}`
