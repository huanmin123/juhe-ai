import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { GatewaySettings } from '../../modules/gateway/account-error-policy.service.js'
import {
  OpenAIResponseInspectionBuffer,
  inspectResponseSemanticFrames,
  resolveRuntimeResponseInspectionPolicies,
  type RuntimeResponseInspectionPolicy
} from '../../modules/gateway/openai-gateway-response-inspection.js'
import {
  extractOpenAIJsonSemanticFrames
} from '../../modules/gateway/openai-gateway-response-semantics.js'
import { pipeUpstreamStream } from '../../modules/gateway/openai-gateway-stream.js'
import { GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { listResponseInspectionPolicyDefaultRules } from '../../storage/response-inspection-policy.repository.js'

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
  const defaultRules = listResponseInspectionPolicyDefaultRules()
  assert(defaultRules.some((rule) => rule.match.jsonPathsExists?.includes('error')), '默认规则必须覆盖 OpenAI JSON / SSE error 对象')
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
  const repositorySource = readFileSync(new URL('../../storage/response-inspection-policy.repository.ts', import.meta.url), 'utf8')
  const responseFinalizationSource = readFileSync(new URL('../../modules/gateway/openai-gateway-response-finalization.ts', import.meta.url), 'utf8')
  assert(repositorySource.includes('maxManagementResponseInspectionPolicies'), '管理端响应检查策略必须有固定数量上限')
  assert(repositorySource.includes('SELECT id FROM response_inspection_policies LIMIT ?'), '创建管理端响应检查策略容量预检必须使用固定窗口')
  assert(!repositorySource.includes('COUNT(*) AS total FROM response_inspection_policies'), '创建管理端响应检查策略不能用 COUNT(*) 容量预检')
  assert(responseFinalizationSource.includes('nonStreamResponseInspectionMaxBytes'), '非流式 JSON 响应检查必须有固定检查窗口')
  assert(responseFinalizationSource.includes('pipeNonStreamUpstreamResponseForInspection'), '非流式 JSON 必须先经过写前检查管道')
}

console.info('response inspection policy regression passed')
