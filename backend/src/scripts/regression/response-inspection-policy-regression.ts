import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import {
  inspectResponseSemanticFrames,
  resolveRuntimeResponseInspectionPolicies,
  type RuntimeResponseInspectionPolicy
} from '../../modules/gateway/response/inspection.js'
import {
  OpenAIResponseInspectionBuffer
} from '../../modules/gateway/protocols/openai-v1/response-inspection-buffer.js'
import {
  validateAccountResponseInspectionRules
} from '../../modules/accounts/account-response-inspection-policy-validation.js'
import {
  extractOpenAIJsonSemanticFrames
} from '../../modules/gateway/protocols/openai-v1/response-semantics.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'
import { GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { closeStorageDatabases, getBusinessDatabase } from '../../storage/database.js'
import {
  createResponseInspectionPolicy,
  listResponseInspectionPolicyDefaultRules,
  updateResponseInspectionPolicy
} from '../../storage/response-inspection-policy.repository.js'

const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 60,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10
}

function responsePolicy(overrides: Partial<RuntimeResponseInspectionPolicy>): RuntimeResponseInspectionPolicy {
  return {
    id: 'policy_response_regression',
    source: 'management',
    name: '响应语义检查回归规则',
    enabled: true,
    priority: 10,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE,
    match: {},
    action: 'retry_next_account',
    executionMode: 'intercept',
    dataHandling: 'replace_with_failure',
    retryEnabled: true,
    accountSwitch: 'request_next_account',
    accountState: 'none',
    ...overrides
  }
}

function sseEvent(type: string, data: Record<string, unknown>, eventName = type): Buffer {
  return Buffer.from(`event: ${eventName}\ndata: ${JSON.stringify({ type, ...data })}\n\n`, 'utf8')
}

function dataEvent(data: Record<string, unknown>): Buffer {
  return Buffer.from(`data: ${JSON.stringify(data)}\n\n`, 'utf8')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    id: 'chatcmpl_local_dj6ppb8f9dn1',
    object: 'chat.completion',
    model: 'gpt-5.5',
    choices: [
      {
        index: 0,
        message: {
          role: 'assistant',
          content: '公益服务器压力很大，欢迎加入 https://dc.hhhl.cc/chat/room/amlc1bekzi'
        },
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 0,
      completion_tokens: 0,
      total_tokens: 0
    }
  }, 'chat_completions')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        match: {
          outputTextIncludes: ['dc.hhhl.cc', '公益服务器']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json'
  })
  assert.equal(result.decision?.transport, 'json', 'Chat JSON 广告响应必须按 JSON 语义检查命中')
  assert.equal(result.decision?.endpointFamily, 'chat_completions', 'Chat JSON 必须保留端点家族')
  assert.equal(result.decision?.matchedField, 'outputTextIncludes', 'Chat JSON 应在 message.content 上命中文本规则')
  assert.equal(result.decision?.retryEnabled, true, '命中 retry 策略时必须允许服务端换号重试')
}

assert.equal(validateAccountResponseInspectionRules([
  {
    enabled: true,
    name: '账户响应检查',
    priority: 10,
    match: {
      clientProfiles: ['codex'],
      accountClientCompatibilities: ['codex_responses'],
      outputTextIncludes: ['污染']
    },
    action: 'retry_next_account'
  }
]).valid, true, '账户级响应检查规则应接受新响应检查 matcher')
assert.equal(validateAccountResponseInspectionRules([
  {
    enabled: true,
    name: '无效客户端维度',
    priority: 10,
    match: {
      clientProfiles: ['desktop_codex'],
      outputTextIncludes: ['污染']
    },
    action: 'retry_next_account'
  }
]).valid, false, '账户级响应检查规则必须拒绝未知客户端画像')
assert.equal(validateAccountResponseInspectionRules([
  {
    enabled: true,
    name: '只有排除条件',
    priority: 10,
    match: {
      outputTextExcludes: ['正常']
    },
    action: 'observe'
  }
]).valid, false, '账户级响应检查规则不能只填输出文本排除条件')

{
  const frames = extractOpenAIJsonSemanticFrames({
    id: 'chatcmpl_and_semantics',
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: {
          role: 'assistant',
          content: '这里包含账户污染文本'
        },
        finish_reason: 'stop'
      }
    ]
  }, 'chat_completions')
  const notMatched = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        match: {
          outputTextIncludes: ['污染文本'],
          finishReasons: ['length']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json'
  })
  assert.equal(notMatched.decision, undefined, '响应检查不同字段必须同时命中，不能只因输出文本命中就提前触发')
  const matched = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        match: {
          outputTextIncludes: ['污染文本'],
          finishReasons: ['stop']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json'
  })
  assert.equal(matched.decision?.matchedField, 'outputTextIncludes', '不同字段同时命中后应返回带摘要的输出文本命中')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    id: 'resp_regression',
    status: 'completed',
    output_text: '正常前缀 但是包含 UniverseFederation TG https://t.me/UniverseFederation',
    usage: {
      input_tokens: 2,
      output_tokens: 4,
      total_tokens: 6
    }
  }, 'responses')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        match: {
          outputTextIncludes: ['UniverseFederation']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json'
  })
  assert.equal(result.decision?.endpointFamily, 'responses', 'Responses JSON 必须按 Responses 语义检查')
  assert.equal(result.decision?.frameType, 'output_text_done', 'Responses JSON output_text 应映射为完整输出语义帧')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    id: 'raw_json_path_regression',
    object: 'chat.completion',
    vendor_payload: {
      blocked: {
        reason: 'policy'
      }
    }
  }, 'chat_completions')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        match: {
          jsonPathsExists: ['vendor_payload.blocked.reason']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json'
  })
  assert.equal(result.decision?.matchedField, 'jsonPathsExists', 'jsonPathsExists 必须检查原始 JSON 任意路径，而不是只检查语义帧路径')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    id: 'raw_json_array_path_regression',
    object: 'chat.completion',
    vendor_payload: {
      blocks: [
        {
          reason: 'policy'
        }
      ]
    }
  }, 'chat_completions')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        match: {
          jsonPathsExists: ['vendor_payload.blocks.0.reason']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json'
  })
  assert.equal(result.decision?.matchedField, 'jsonPathsExists', 'jsonPathsExists 必须支持原始 JSON 数组下标路径')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    error: {
      code: 'client_scoped_error',
      message: 'only codex compatible accounts should match'
    }
  }, 'responses')
  const policies = [
    responsePolicy({
      match: {
        clientProfiles: ['codex'],
        accountClientCompatibilities: ['codex_responses'],
        errorCodes: ['client_scoped_error']
      }
    })
  ]
  const genericResult = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_openai',
      accountClientCompatibility: 'codex_responses'
    }
  })
  assert.equal(genericResult.decision, undefined, 'clientProfiles 不匹配时不能命中响应检查策略')
  const accountCompatibilityResult = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'codex',
      accountClientCompatibility: 'openai_standard'
    }
  })
  assert.equal(accountCompatibilityResult.decision, undefined, 'accountClientCompatibilities 不匹配时不能命中响应检查策略')
  const codexResult = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'codex',
      accountClientCompatibility: 'codex_responses'
    }
  })
  assert.equal(codexResult.decision?.matchedField, 'errorCodes', '客户端维度匹配后仍必须由语义字段触发命中')
  assert.equal(codexResult.decision?.clientProfile, 'codex', '响应检查决策应记录命中时的客户端画像')
  assert.equal(codexResult.decision?.accountClientCompatibility, 'codex_responses', '响应检查决策应记录命中时的账号兼容模式')
}

{
  const defaultRules = listResponseInspectionPolicyDefaultRules()
  assert(defaultRules.some((rule) => rule.match.jsonPathsExists?.includes('error')), '默认规则必须覆盖 OpenAI JSON / SSE error 对象')
  assert(defaultRules.some((rule) => rule.match.clientProfiles?.includes('codex') && rule.match.finishReasons?.includes('incomplete')), '默认规则必须覆盖 Codex response.incomplete')
  assert(defaultRules.some((rule) => rule.providerCode === GPT_VENDOR_CODE && rule.match.errorCodes?.includes('cyber_policy')), 'GPT cyber_policy 只能作为 GPT provider 规则存在')
  const gptPolicies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_gpt',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  const genericPolicies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_openai',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  assert(gptPolicies.some((policy) => policy.match.errorCodes?.includes('cyber_policy')), 'GPT 供应商应加载 cyber_policy 默认规则')
  assert.equal(genericPolicies.some((policy) => policy.match.errorCodes?.includes('cyber_policy')), false, '通用 OpenAI-compatible 供应商不应继承 GPT cyber_policy 规则')
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_incomplete',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      clientCompatibility: 'codex_responses',
      credentials: {}
    } as never,
    managementPolicies: listResponseInspectionPolicyDefaultRules()
  })
  const buffer = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses',
    policies,
    context: {
      clientProfile: 'codex',
      accountClientCompatibility: 'codex_responses'
    }
  })
  const result = buffer.pushChunk(sseEvent('response.incomplete', {
    response: {
      status: 'incomplete',
      incomplete_details: {
        reason: 'max_output_tokens'
      }
    }
  }))
  const responseBody = Buffer.concat(result.chunks).toString('utf8')
  assert.equal(result.intercepted?.policyId, 'default_codex_response_incomplete', 'Codex response.incomplete 应命中专用默认规则')
  assert(responseBody.includes('upstream_retryable_error'), `Codex response.incomplete 应改写为客户端可重试失败：${responseBody}`)
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_account_rule',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      credentials: {
        response_inspection_rules: [
          {
            enabled: true,
            name: '账户级响应检查规则',
            priority: 100,
            match: {
              errorCodes: ['account_level_error']
            },
            action: 'retry_next_account'
          }
        ]
      }
    } as never,
    managementPolicies: [
      {
        id: 'rip_management_preempt',
        defaultRule: false,
        editable: true,
        name: '管理端响应检查规则',
        enabled: true,
        priority: 1,
        scopeType: 'provider',
        protocolCode: OPENAI_PROTOCOL_CODE,
        providerCode: GPT_VENDOR_CODE,
        match: {
          errorCodes: ['account_level_error']
        },
        action: 'observe'
      }
    ]
  })
  assert.equal(policies[0]?.source, 'account', '账户级响应检查规则必须优先于管理端和默认规则执行')
  assert.equal(policies[0]?.accountSwitch, 'request_next_account', '账户级响应检查规则应展开新响应检查 action 运行时语义')
}

{
  const defaultRules = listResponseInspectionPolicyDefaultRules()
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_gpt_retry',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  const buffer = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses',
    policies
  })
  const result = buffer.pushChunk(sseEvent('response.failed', {
    response: {
      status: 'failed',
      error: {
        code: 'internal_server_error',
        message: 'mock upstream failed before output'
      }
    }
  }))
  const responseBody = Buffer.concat(result.chunks).toString('utf8')
  assert.equal(result.intercepted?.policySource, 'system_default', 'Responses SSE failed 默认规则应保留 system_default 来源')
  assert.equal(result.intercepted?.upstreamErrorCode, 'internal_server_error', '响应检查决策应保留上游原始错误码')
  assert(responseBody.includes('upstream_retryable_error'), `Codex 客户端预输出失败应改写为客户端可重试码：${responseBody}`)
  assert(!responseBody.includes('internal_server_error'), `Codex 客户端失败事件不应透出上游原始错误码：${responseBody}`)
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_gpt_default_delta_fastpath',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      credentials: {}
    } as never,
    managementPolicies: listResponseInspectionPolicyDefaultRules()
  })
  const buffer = new OpenAIResponseInspectionBuffer({
    endpointFamily: 'responses',
    policies
  })
  const event = sseEvent('response.output_text.delta', {
    delta: '普通输出'
  })
  const result = buffer.pushChunk(event)
  assert.equal(result.intercepted, undefined, '默认错误规则下普通 Responses delta 不应触发响应检查')
  assert.equal(Buffer.concat(result.chunks).toString('utf8'), event.toString('utf8'), '默认错误规则下普通 Responses delta 应原样透传')
}

{
  const buffer = new OpenAIResponseInspectionBuffer({
    endpointFamily: 'responses',
    policies: [
      responsePolicy({
        match: {
          outputTextIncludes: ['广告污染']
        }
      })
    ]
  })
  const event = sseEvent('response.output_text.delta', {
    delta: '这里被插入了广告污染'
  })
  const first = buffer.pushChunk(event.subarray(0, event.length - 1))
  assert.equal(first.intercepted, undefined, '跨 chunk SSE 事件未完成前不应误判')
  assert.equal(first.chunks.length, 0, '跨 chunk SSE 事件未闭合前不应提前转发')
  const second = buffer.pushChunk(event.subarray(event.length - 1))
  assert.equal(second.intercepted?.transport, 'sse', 'Responses SSE 跨 chunk 完整后必须命中语义检查')
  assert.equal(second.intercepted?.endpointFamily, 'responses', 'Responses SSE 必须保留端点家族')
  assert.match(Buffer.concat(second.chunks).toString('utf8'), /response\.failed/, 'SSE 命中替换策略时应写出失败事件')
}

{
  const buffer = new OpenAIResponseInspectionBuffer({
    endpointFamily: 'responses',
    policies: [
      responsePolicy({
        match: {
          jsonPathsExists: ['vendor.flag']
        }
      })
    ]
  })
  const result = buffer.pushChunk(sseEvent('vendor.custom_event', {
    vendor: {
      flag: true
    }
  }))
  assert.equal(result.intercepted?.matchedField, 'jsonPathsExists', '无标准语义帧的 SSE data JSON 仍应支持原始 JSON 路径匹配')
}

{
  const buffer = new OpenAIResponseInspectionBuffer({
    endpointFamily: 'chat_completions',
    policies: [
      responsePolicy({
        match: {
          outputTextIncludes: ['丢弃这段']
        },
        action: 'drop_event',
        dataHandling: 'discard_event',
        retryEnabled: false,
        accountSwitch: 'none'
      })
    ]
  })
  const dropped = buffer.pushChunk(dataEvent({
    id: 'chatcmpl_regression',
    object: 'chat.completion.chunk',
    choices: [
      {
        index: 0,
        delta: {
          content: '丢弃这段'
        }
      }
    ]
  }))
  assert.equal(dropped.intercepted, undefined, 'drop_event 不应结束整个响应')
  assert.equal(Buffer.concat(dropped.chunks).length, 0, 'drop_event 应只丢弃当前污染事件')
  assert.equal(dropped.observations?.[0]?.action, 'discard_event', 'drop_event 应保留观察元数据用于审计')
}

{
  let downstreamPrepared = false
  let failureCalled = false
  const response = {
    headersSent: false,
    writableEnded: false,
    destroyed: false,
    writableLength: 0,
    writableHighWaterMark: 0,
    once() { return this },
    off() { return this },
    hasHeader() { return false },
    setHeader() { return this },
    status() { return this },
    write() {
      throw new Error('写下游前服务端重试不应写 response body')
    },
    end() {
      this.writableEnded = true
      return this
    }
  }
  async function* upstreamChunks(): AsyncIterable<Uint8Array> {
    yield sseEvent('response.output_text.delta', {
      delta: '这里被插入了广告污染'
    })
  }
  const result = await pipeUpstreamStream(
    upstreamChunks(),
    response as never,
    settings,
    Date.now(),
    () => { failureCalled = true },
    undefined,
    {
      endpointFamily: 'responses',
      prepareDownstream: () => { downstreamPrepared = true },
      responseInspectionPolicies: [
        responsePolicy({
          match: {
            outputTextIncludes: ['广告污染']
          }
        })
      ]
    }
  )
  assert.equal(result.completed, false, '写下游前命中响应检查应返回失败结果交给调度层')
  assert.equal(result.responseInspection?.retryEnabled, true, '写下游前命中 retry 策略应保留重试标记')
  assert.equal(result.responseInspection?.accountSwitch, 'request_next_account', '服务端重试应保留换号动作')
  assert.equal(response.headersSent, false, '服务端重试不应提前发送响应头')
  assert.equal(response.writableEnded, false, '服务端重试不应结束客户端响应')
  assert.equal(downstreamPrepared, false, '服务端重试不应准备下游响应')
  assert.equal(failureCalled, false, '配置化写前检查应交给调度重试，不应走流失败副作用')
}

{
  let failureCalled = false
  const response = {
    headersSent: false,
    writableEnded: false,
    destroyed: false,
    writableLength: 0,
    writableHighWaterMark: 0,
    once() { return this },
    off() { return this },
    hasHeader() { return false },
    setHeader() { return this },
    status() { return this },
    write() {
      return true
    },
    end() {
      this.writableEnded = true
      return this
    }
  }
  async function* upstreamChunks(): AsyncIterable<Uint8Array> {
    yield Buffer.from([
      'event: response.completed',
      'data: {"type":"response.completed","response":{"status":"completed"}}',
      '',
      'event: response.failed',
      'data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_overloaded","message":"late failure"}}}',
      ''
    ].join('\n'), 'utf8')
  }
  const result = await pipeUpstreamStream(
    upstreamChunks(),
    response as never,
    settings,
    Date.now(),
    () => { failureCalled = true },
    undefined,
    {
      endpointFamily: 'responses'
    }
  )
  assert.equal(result.completed, false, '同批终止后失败应返回失败结果')
  assert.equal(result.errorCode, 'server_overloaded', '同批终止后失败应保留失败错误码')
  assert.equal(failureCalled, true, '同批终止后失败应触发失败回调')
  assert.equal(response.writableEnded, true, '同批终止后失败应结束客户端响应')
}

{
  const repositorySource = readFileSync(new URL('../../storage/response-inspection-policy.repository.ts', import.meta.url), 'utf8')
  const responseFinalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
  const routeSource = readFileSync(new URL('../../modules/response-inspection-policies/response-inspection-policies.routes.ts', import.meta.url), 'utf8')
  const schemaSource = readFileSync(new URL('../../storage/schema/business-schema.ts', import.meta.url), 'utf8')
  const gatewayPreflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
  const fallbackCandidateSource = readFileSync(new URL('../../modules/gateway/dispatch/api-key-group-fallback-candidate.ts', import.meta.url), 'utf8')
  assert(repositorySource.includes('maxManagementResponseInspectionPolicies'), '管理端响应检查策略必须有固定数量上限')
  assert(repositorySource.includes('SELECT id FROM response_inspection_policies LIMIT ?'), '创建管理端响应检查策略容量预检必须使用固定窗口')
  assert(!repositorySource.includes('COUNT(*) AS total FROM response_inspection_policies'), '创建管理端响应检查策略不能用 COUNT(*) 容量预检')
  assert(repositorySource.includes('positiveMatchKeys'), '排除条件不能作为独立正向 matcher 使用')
  assert(responseFinalizationSource.includes('nonStreamResponseInspectionMaxBytes'), '非流式 JSON 响应检查必须有固定检查窗口')
  assert(responseFinalizationSource.includes('pipeNonStreamUpstreamResponseForInspection'), '非流式 JSON 必须先经过写前检查管道')
  assert(routeSource.includes("responseInspectionPoliciesRouter.put('/:id'"), '响应检查策略更新接口必须使用 PUT 全量替换，不保留 PATCH partial 语义')
  assert(!routeSource.includes("responseInspectionPoliciesRouter.patch('/:id'"), '响应检查策略更新接口不应继续暴露 PATCH partial 入口')
  assert(schemaSource.includes("CHECK (action IN ('observe', 'drop_event', 'retry_no_avoidance', 'retry_next_account', 'avoid_account_ttl', 'avoid_upstream_bucket_ttl'))"), '响应检查策略动作必须有数据库 CHECK 约束')
  assert(schemaSource.includes('json_valid(match_json)'), '响应检查策略 match_json 必须有 JSON 有效性约束')
  assert(fallbackCandidateSource.includes('listCachedActiveResponseInspectionPoliciesAsync'), 'API Key 分组 fallback 候选必须按目标分组协议和供应商加载响应检查策略')
  assert(gatewayPreflightSource.includes('responseInspectionPolicies: candidate.responseInspectionPolicies'), 'fallback dispatch context 必须使用目标候选分组的响应检查策略')
  assert(!gatewayPreflightSource.includes('responseInspectionPolicies: input.responseInspectionPolicies'), 'fallback dispatch context 不得沿用原分组传入的响应检查策略')
}

{
  const tempRoot = resolve(tmpdir(), `juhe-ai-response-inspection-policy-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.secret = 'response-inspection-policy-regression-secret'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  runtimeConfig.processRole = 'db-service'
  mkdirSync(tempRoot, { recursive: true })
  try {
    assert.throws(() => createResponseInspectionPolicy({
      name: '只有排除条件的策略',
      enabled: true,
      priority: 10,
      scopeType: 'protocol',
      protocolCode: OPENAI_PROTOCOL_CODE,
      match: { outputTextExcludes: ['允许文本'] },
      action: 'observe'
    }), /至少需要一个匹配条件/, 'outputTextExcludes 只能作为排除条件，不能单独构成命中规则')

    const created = createResponseInspectionPolicy({
      name: '全量替换备注清空策略',
      enabled: true,
      priority: 11,
      scopeType: 'protocol',
      protocolCode: OPENAI_PROTOCOL_CODE,
      match: {
        outputTextIncludes: ['污染文本'],
        outputTextExcludes: ['允许文本']
      },
      action: 'observe',
      notes: '等待清空'
    })
    const updated = updateResponseInspectionPolicy(created.id, {
      name: '全量替换备注清空策略',
      enabled: true,
      priority: 12,
      scopeType: 'protocol',
      protocolCode: OPENAI_PROTOCOL_CODE,
      match: { outputTextIncludes: ['新污染文本'] },
      action: 'retry_no_avoidance'
    })
    assert.equal(updated?.notes, undefined, 'PUT 全量替换语义下，未提交 notes 应清空旧备注')
    assert.equal(updated?.priority, 12, 'PUT 全量替换应使用本次提交的优先级')

    const database = getBusinessDatabase()
    const row = database.prepare('SELECT notes, action, priority FROM response_inspection_policies WHERE id = ?')
      .get(created.id) as { notes: string | null; action: string; priority: number } | undefined
    assert.equal(row?.notes, null, '数据库中的旧备注必须被清空')
    assert.equal(row?.action, 'retry_no_avoidance', '数据库中的 action 必须按全量替换更新')
    assert.equal(row?.priority, 12, '数据库中的 priority 必须按全量替换更新')

    const now = new Date().toISOString()
    assert.throws(() => database.prepare(`
      INSERT INTO response_inspection_policies (
        id, name, enabled, priority, scope_type, protocol_code, match_json, action, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run('rip_invalid_action', '非法动作', 1, 20, 'protocol', OPENAI_PROTOCOL_CODE, '{"outputTextIncludes":["x"]}', 'legacy_action', now, now), /constraint|CHECK/i, '数据库必须拒绝非法 action')
    assert.throws(() => database.prepare(`
      INSERT INTO response_inspection_policies (
        id, name, enabled, priority, scope_type, protocol_code, match_json, action, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run('rip_invalid_json', '非法 JSON', 1, 21, 'protocol', OPENAI_PROTOCOL_CODE, '{bad json', 'observe', now, now), /constraint|CHECK/i, '数据库必须拒绝非法 match_json')
  } finally {
    closeStorageDatabases()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

console.info('response inspection policy regression passed')
