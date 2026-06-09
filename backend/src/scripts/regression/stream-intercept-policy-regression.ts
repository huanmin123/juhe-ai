import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  OpenAIStreamInterceptBuffer
} from '../../modules/gateway/openai-gateway-stream-intercept.js'
import { validateAccountStreamInterceptRules } from '../../modules/accounts/account-stream-intercept-policy-validation.js'
import { pipeUpstreamStream } from '../../modules/gateway/openai-gateway-stream.js'
import {
  gatewayStreamClientRetryErrorCode
} from '../../modules/gateway/openai-gateway-responses.js'
import type { GatewaySettings } from '../../modules/gateway/account-error-policy.service.js'
import {
  resolveRuntimeStreamInterceptPolicies,
  type RuntimeStreamInterceptPolicy
} from '../../modules/gateway/openai-gateway-stream-policy.js'
import { listStreamInterceptPolicyDefaultRules } from '../../storage/stream-intercept-policy.repository.js'
import { GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'

function policy(overrides: Partial<RuntimeStreamInterceptPolicy>): RuntimeStreamInterceptPolicy {
  return {
    id: 'policy_test',
    source: 'management',
    name: '测试流式拦截策略',
    enabled: true,
    action: 'retry_no_avoidance',
    executionMode: 'intercept',
    priority: 10,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: GPT_VENDOR_CODE,
    match: {},
    dataHandling: 'discard_stream',
    retryEnabled: false,
    accountSwitch: 'none',
    accountState: 'none',
    ...overrides
  }
}

function sseEvent(type: string, data: Record<string, unknown>, eventName = type): Buffer {
  return Buffer.from(`event: ${eventName}\ndata: ${JSON.stringify({ type, ...data })}\n\n`, 'utf8')
}

function sseData(data: Record<string, unknown>): Buffer {
  return Buffer.from(`data: ${JSON.stringify(data)}\n\n`, 'utf8')
}

const pollutedEvent = sseEvent('response.output_text.delta', {
  delta: '这里被插入了广告污染'
})

const visibleOutputEvent = sseEvent('response.output_text.delta', {
  delta: '这段正常输出还没有写给客户端'
})

const leadingChatRoleNoopEvent = sseData({
  id: 'chatcmpl-dummy',
  object: 'chat.completion.chunk',
  created: 1781024363,
  model: 'gpt-5.5',
  choices: [
    {
      index: 0,
      delta: {
        role: 'assistant',
        content: ''
      }
    }
  ]
})

const chatContentEvent = sseData({
  id: 'chatcmpl-normal',
  object: 'chat.completion.chunk',
  created: 1781024364,
  model: 'gpt-5.5',
  choices: [
    {
      index: 0,
      delta: {
        content: '正常输出'
      }
    }
  ]
})

const invalidEncryptedContentEvent = sseData({
  type: 'error',
  error: {
    type: 'invalid_request_error',
    code: 'invalid_encrypted_content',
    message: 'The encrypted content {"ty...":5} could not be verified. Reason: Encrypted content could not be decrypted or parsed.'
  },
  status: 400
})

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

{
  assertStreamInterceptPolicyRepositoryGuards()
  const invalidCombinationValidation = validateAccountStreamInterceptRules([
    {
      enabled: true,
      name: '非法执行参数组合账户规则',
      match: {
        textIncludes: ['广告污染']
      },
      dataHandling: 'discard_stream',
      retryEnabled: true,
      accountSwitch: 'request_next_account'
    }
  ])
  assert.equal(invalidCombinationValidation.valid, false, '账户级流式规则只接受 action 模板，不接受执行参数组合')
  const actionValidation = validateAccountStreamInterceptRules([
    {
      enabled: true,
      name: '模板账户规则',
      priority: 10,
      match: {
        textIncludes: ['广告污染']
      },
      action: 'retry_next_account'
    }
  ])
  assert.equal(actionValidation.valid, true, '账户级流式规则应接受 action 模板')
  const legacyTtlValidation = validateAccountStreamInterceptRules([
    {
      enabled: true,
      name: '旧避让秒数字段',
      priority: 10,
      match: {
        textIncludes: ['广告污染']
      },
      action: 'avoid_account_ttl',
      avoidanceTtlSeconds: 300
    }
  ])
  assert.equal(legacyTtlValidation.valid, false, '账户级流式规则不应再接受用户配置的避让秒数')
  const missingRuntimeFieldsValidation = validateAccountStreamInterceptRules([
    {
      action: 'retry_no_avoidance',
      match: {
        textIncludes: ['广告污染']
      }
    }
  ])
  assert.equal(missingRuntimeFieldsValidation.valid, false, '账户级流式规则保存期必须拒绝缺少运行时必填字段的配置')
  const tooManyMatchersValidation = validateAccountStreamInterceptRules([
    {
      enabled: true,
      name: '过多匹配项账户规则',
      priority: 20,
      match: {
        textIncludes: Array.from({ length: 51 }, (_, index) => `污染-${index}`)
      },
      action: 'retry_no_avoidance'
    }
  ])
  assert.equal(tooManyMatchersValidation.valid, false, '账户级流式规则不应静默截断超过 50 项的匹配列表')
  assert.throws(
    () => resolveRuntimeStreamInterceptPolicies({
      account: {
        providerCode: GPT_VENDOR_CODE,
        credentials: {
          stream_intercept_rules: [
            {
              enabled: true,
              name: '运行时过多匹配项账户规则',
              priority: 30,
              match: {
                textIncludes: Array.from({ length: 51 }, (_, index) => `污染-${index}`)
              },
              action: 'retry_no_avoidance'
            }
          ]
        }
      } as never
    }),
    /不能超过 50 项/,
    '运行时账户流式规则不应截断超过 50 项的匹配列表'
  )
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    clientRetryEnabled: true
  })
  const leading = interceptor.pushChunk(leadingChatRoleNoopEvent)
  assert.equal(leading.chunks.length, 0, '首个空 Chat chunk 不应立刻写给下游')
  assert.equal(leading.intercepted, undefined)
  const failed = interceptor.pushChunk(invalidEncryptedContentEvent)
  const text = Buffer.concat(failed.chunks).toString('utf8')
  assert.equal(failed.intercepted?.reason, 'before_downstream_write_stream_failure', '空 Chat chunk 后的 error 仍应视为写下游前失败')
  assert.equal(failed.intercepted?.upstreamEventType, 'error', '应在上游 type:error 事件处拦截')
  assert.equal(failed.intercepted?.upstreamErrorCode, 'invalid_encrypted_content', '应保留真实上游错误码用于审计')
  assert.equal(failed.intercepted?.downstreamWritten, false, '被缓冲的空 Chat chunk 不应改变下游写入状态')
  assert.match(text, /response\.failed/, '客户端应收到 Responses 协议失败事件')
  assert.match(text, new RegExp(gatewayStreamClientRetryErrorCode), '客户端可重试时应写入可重试错误码')
  assert.doesNotMatch(text, /chatcmpl-dummy/, '失败收尾不应夹带上游空 Chat chunk')
  assert.doesNotMatch(text, /"type":"error"/, '失败收尾不应继续透传原始 type:error 事件')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    clientRetryEnabled: true
  })
  const leading = interceptor.pushChunk(leadingChatRoleNoopEvent)
  assert.equal(leading.chunks.length, 0, '正常输出前也先暂存首个空 Chat chunk')
  const output = interceptor.pushChunk(chatContentEvent)
  const text = Buffer.concat(output.chunks).toString('utf8')
  assert.equal(output.intercepted, undefined)
  assert.match(text, /chatcmpl-dummy/, '正常 Chat 流后续有内容时应补发首个 role chunk')
  assert.match(text, /正常输出/, '正常 Chat 内容仍应继续转发')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: { textIncludes: ['广告污染'] },
        dataHandling: 'discard_event'
      })
    ]
  })
  const result = interceptor.pushChunk(pollutedEvent)
  assert.equal(result.chunks.length, 0, 'discard_event 应只丢弃命中事件')
  assert.equal(result.intercepted, undefined, 'discard_event 不应结束当前流')
  assert.equal(result.observations?.[0]?.action, 'discard_event', 'discard_event 应记录非终止命中观察')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: { textIncludes: ['广告污染'] },
        dataHandling: 'discard_stream',
        retryEnabled: true,
        accountSwitch: 'avoid_account_ttl',
        accountState: 'runtime_avoidance'
      })
    ]
  })
  const result = interceptor.pushChunk(pollutedEvent)
  assert.equal(result.chunks.length, 0, 'discard_stream 不应写入失败事件')
  assert.equal(result.intercepted?.action, 'discard_stream', 'discard_stream 应结束当前流')
  assert.equal(result.intercepted?.policyId, 'policy_test')
  assert.equal(result.intercepted?.retryEnabled, true)
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: {
          eventTypes: ['response.output_text.delta'],
          textIncludes: ['广告污染']
        },
        dataHandling: 'discard_stream'
      })
    ]
  })
  const normalResult = interceptor.pushChunk(visibleOutputEvent)
  assert.equal(normalResult.chunks.length, 1, '多字段匹配必须同时命中，不能只因 event 类型命中就拦截')
  assert.equal(normalResult.intercepted, undefined)
  const pollutedResult = interceptor.pushChunk(pollutedEvent)
  assert.equal(pollutedResult.intercepted?.action, 'discard_stream', '多字段匹配在 event 类型和文本同时命中时应拦截')
  assert.equal(pollutedResult.chunks.length, 0)
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    clientRetryEnabled: true,
    policies: [
      policy({
        match: { errorCodes: ['bad_gateway'] },
        dataHandling: 'replace_with_failure',
        retryEnabled: true,
        accountSwitch: 'request_next_account'
      })
    ]
  })
  const result = interceptor.pushChunk(sseEvent('response.failed', {
    response: {
      error: {
        code: 'bad_gateway',
        message: '上游失败'
      }
    }
  }))
  const text = Buffer.concat(result.chunks).toString('utf8')
  assert.equal(result.intercepted?.action, 'replace_with_failure', 'replace_with_failure 应结束当前流')
  assert.match(text, /response\.failed/, 'replace_with_failure 应写入失败事件')
  assert.match(text, new RegExp(gatewayStreamClientRetryErrorCode), '允许客户端重试时应写入客户端可重试错误码')
}

{
  const defaultRules = listStreamInterceptPolicyDefaultRules()
  assert.deepEqual(
    defaultRules.map((policy) => policy.priority),
    [1, 2, 3, 4, 5],
    '默认流式拦截规则优先级应从 1 开始连续注册'
  )
  const defaultCyberPolicy = defaultRules.find((item) => item.id === 'default_gpt_cyber_policy')
  assert(defaultCyberPolicy, 'cyber_policy 默认规则必须注册为 GPT 供应商层规则')
  assert.equal(defaultCyberPolicy.scopeType, 'provider', 'cyber_policy 默认规则不能落在 OpenAI 协议层')
  assert.equal(defaultCyberPolicy.providerCode, GPT_VENDOR_CODE, 'cyber_policy 默认规则必须绑定 GPT 供应商')
  const defaultPolicies = resolveRuntimeStreamInterceptPolicies({
    account: {
      providerCode: GPT_VENDOR_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  const cyberPolicyResult = new OpenAIStreamInterceptBuffer({
    policies: defaultPolicies
  }).pushChunk(sseEvent('response.failed', {
    response: {
      error: {
        code: 'cyber_policy',
        message: '安全策略拦截'
      }
    }
  }))
  assert.equal(cyberPolicyResult.intercepted?.policyId, 'default_gpt_cyber_policy', 'GPT cyber_policy 应由 GPT 供应商层默认规则命中')
  const genericOpenAIPolicies = resolveRuntimeStreamInterceptPolicies({
    account: {
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  assert(
    !genericOpenAIPolicies.some((item) => item.id === 'default_gpt_cyber_policy'),
    '通用 OpenAI-compatible 供应商不能继承 GPT cyber_policy 默认规则'
  )
  const genericCyberPolicyResult = new OpenAIStreamInterceptBuffer({
    policies: genericOpenAIPolicies
  }).pushChunk(sseEvent('response.failed', {
    response: {
      error: {
        code: 'cyber_policy',
        message: '安全策略拦截'
      }
    }
  }))
  assert.equal(genericCyberPolicyResult.intercepted?.policyId, 'default_openai_response_failed', '非 GPT 供应商只能由协议层 response.failed 规则兜底，不应命中 GPT cyber_policy')
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: defaultPolicies
  })
  const completed = interceptor.pushChunk(sseEvent('response.completed', {
    response: {
      error: null,
      output: [
        {
          type: 'message',
          content: [{ type: 'output_text', text: '正常完成' }]
        }
      ]
    }
  }))
  assert.equal(completed.intercepted, undefined, '默认 response.error 规则不应把 response.error:null 当成错误')
  assert.equal(completed.chunks.length, 1, 'response.error:null 的成功事件应继续转发')
  const deltaWithNullError = interceptor.pushChunk(sseEvent('response.output_text.delta', {
    delta: '正常输出',
    error: null
  }))
  assert.equal(deltaWithNullError.intercepted, undefined, '默认 data.error 规则不应把 error:null 当成错误')
  assert.equal(deltaWithNullError.chunks.length, 1, 'error:null 的普通事件应继续转发')

  const responseError = interceptor.pushChunk(sseEvent('response.completed', {
    response: {
      error: {
        code: 'bad_gateway',
        message: '上游失败'
      }
    }
  }))
  assert.equal(responseError.intercepted?.policyId, 'default_openai_response_error', '默认 response.error 规则应拦截真实错误对象')
  const dataError = interceptor.pushChunk(sseEvent('response.output_text.delta', {
    error: {
      code: 'upstream_error',
      message: '上游事件错误'
    }
  }))
  assert.equal(dataError.intercepted?.policyId, 'default_openai_data_error', '默认 data.error 规则应拦截真实错误对象')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: { textIncludes: ['广告污染'] },
        executionMode: 'dry_run'
      })
    ]
  })
  const result = interceptor.pushChunk(pollutedEvent)
  assert.equal(result.chunks.length, 1, '试运行策略不应拦截输出')
  assert.equal(result.intercepted, undefined)
  assert.equal(result.observations?.[0]?.action, 'dry_run', '试运行策略应记录命中观察')
  assert.equal(result.observations?.[0]?.executionMode, 'dry_run')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: { textIncludes: ['广告污染'] },
        dataHandling: 'discard_stream',
        retryEnabled: true
      })
    ]
  })
  const result = interceptor.pushChunk(Buffer.concat([visibleOutputEvent, pollutedEvent]))
  assert.equal(result.intercepted?.runtimePhase, 'before_downstream_write', '同一上游 chunk 内尚未实际写给客户端的前序事件不应改变运行时阶段')
  assert.equal(result.intercepted?.downstreamWritten, false, '只有实际写入客户端后才算写入下游后')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: { textIncludes: ['广告污染'] },
        dataHandling: 'discard_stream'
      })
    ]
  })
  const first = interceptor.pushChunk(visibleOutputEvent)
  assert.equal(first.intercepted, undefined)
  interceptor.markDownstreamWrite()
  const result = interceptor.pushChunk(pollutedEvent)
  assert.equal(result.intercepted?.runtimePhase, 'after_downstream_write', '实际写入下游后，后续命中才进入写入下游后阶段')
  assert.equal(result.intercepted?.downstreamWritten, true)
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
    write() {
      throw new Error('写下游前服务端重试不应写 response body')
    },
    end() {
      this.writableEnded = true
      return this
    }
  }
  async function* upstreamChunks(): AsyncIterable<Uint8Array> {
    yield pollutedEvent
  }
  const result = await pipeUpstreamStream(
    upstreamChunks(),
    response as never,
    settings,
    Date.now(),
    () => { failureCalled = true },
    undefined,
    {
      prepareDownstream: () => { downstreamPrepared = true },
      streamInterceptPolicies: [
        policy({
          match: { textIncludes: ['广告污染'] },
          dataHandling: 'discard_stream',
          retryEnabled: true,
          accountSwitch: 'request_next_account'
        })
      ]
    }
  )
  assert.equal(result.completed, false)
  assert.equal(result.streamIntercept?.action, 'discard_stream')
  assert.equal(result.streamIntercept?.retryEnabled, true)
  assert.equal(response.headersSent, false, '写下游前服务端重试不应提前发送响应头')
  assert.equal(response.writableEnded, false, '写下游前服务端重试不应结束客户端响应')
  assert.equal(downstreamPrepared, false, '写下游前服务端重试不应准备下游响应')
  assert.equal(failureCalled, false, '配置化写下游前拦截应交给调度重试，不应走流失败副作用')
}

{
  let downstreamPrepared = false
  const responseChunks: Buffer[] = []
  const response = {
    headersSent: false,
    writableEnded: false,
    destroyed: false,
    writableLength: 0,
    writableHighWaterMark: 1024,
    once() { return this },
    off() { return this },
    write(buffer: Buffer) {
      this.headersSent = true
      responseChunks.push(Buffer.from(buffer))
      return true
    },
    end() {
      this.writableEnded = true
      return this
    }
  }
  async function* upstreamChunks(): AsyncIterable<Uint8Array> {
    yield leadingChatRoleNoopEvent
    yield invalidEncryptedContentEvent
    yield sseEvent('response.failed', {
      response: {
        error: {
          code: 'upstream_error',
          message: 'Upstream request failed'
        }
      }
    })
  }
  const result = await pipeUpstreamStream(
    upstreamChunks(),
    response as never,
    settings,
    Date.now(),
    () => {},
    undefined,
    {
      clientRetryEnabled: true,
      prepareDownstream: () => { downstreamPrepared = true }
    }
  )
  const text = Buffer.concat(responseChunks).toString('utf8')
  assert.equal(result.completed, false)
  assert.equal(result.streamIntercept?.upstreamErrorCode, 'invalid_encrypted_content', 'mock 上游样本应在真实 error 事件处结束')
  assert.equal(result.streamIntercept?.downstreamWritten, false, 'mock 上游样本的空 Chat chunk 不应算作已写下游')
  assert.equal(downstreamPrepared, true, '网关应准备下游响应并写入干净失败事件')
  assert.equal(response.writableEnded, true, '失败事件写入后应结束下游响应')
  assert.match(text, /response\.failed/, 'mock 上游样本应收尾为 Responses 失败事件')
  assert.match(text, new RegExp(gatewayStreamClientRetryErrorCode), 'mock 上游样本应返回客户端可重试错误码')
  assert.doesNotMatch(text, /chatcmpl-dummy/, 'mock 上游样本不应把空 Chat chunk 发给客户端')
  assert.doesNotMatch(text, /"type":"error"/, 'mock 上游样本不应把原始 type:error 发给客户端')
}

{
  const interceptor = new OpenAIStreamInterceptBuffer({
    policies: [
      policy({
        match: { textIncludes: ['广告污染'] },
        dataHandling: 'discard_stream'
      })
    ]
  })
  const result = interceptor.pushChunk(sseEvent('response.image_generation_call.partial_image', {
    partial_image_b64: '广告污染'.repeat(200)
  }))
  assert.equal(result.chunks.length, 1, '图像类事件应跳过文本扫描并继续转发')
  assert.equal(result.intercepted, undefined)
}

{
  const resolved = resolveRuntimeStreamInterceptPolicies({
    account: {
      providerCode: GPT_VENDOR_CODE,
      credentials: {
        stream_intercept_rules: [
          {
            id: 'account_rule_high_priority',
            name: '账户高优先级数字规则',
            enabled: true,
            priority: 9999,
            match: { textIncludes: ['广告污染'] },
            action: 'retry_no_avoidance'
          }
        ]
      }
    } as never,
    managementPolicies: [
      {
        id: 'default_rule_low_priority',
        defaultRule: true,
        editable: false,
        name: '默认低数字规则',
        enabled: true,
        priority: 1,
        scopeType: 'protocol',
        protocolCode: OPENAI_PROTOCOL_CODE,
        match: { textIncludes: ['广告污染'] },
        action: 'retry_next_account'
      },
      {
        id: 'management_protocol_rule_low_priority',
        defaultRule: false,
        editable: true,
        name: '管理端协议低数字规则',
        enabled: true,
        priority: 1,
        scopeType: 'protocol',
        protocolCode: OPENAI_PROTOCOL_CODE,
        match: { textIncludes: ['广告污染'] },
        action: 'retry_next_account'
      },
      {
        id: 'management_provider_rule_high_priority',
        defaultRule: false,
        editable: true,
        name: '管理端供应商高数字规则',
        enabled: true,
        priority: 9999,
        scopeType: 'provider',
        protocolCode: OPENAI_PROTOCOL_CODE,
        providerCode: GPT_VENDOR_CODE,
        match: { textIncludes: ['广告污染'] },
        action: 'retry_no_avoidance'
      },
      {
        id: 'management_other_provider_rule',
        defaultRule: false,
        editable: true,
        name: '其他供应商规则',
        enabled: true,
        priority: 1,
        scopeType: 'provider',
        protocolCode: OPENAI_PROTOCOL_CODE,
        providerCode: 'deepseek',
        match: { textIncludes: ['广告污染'] },
        action: 'avoid_account_ttl'
      }
    ]
  })
  assert.deepEqual(
    resolved.map((item) => item.id),
    ['account_rule_high_priority', 'management_provider_rule_high_priority', 'management_protocol_rule_low_priority', 'default_rule_low_priority'],
    '运行时合并必须先按账户 / 管理端 / 默认来源排序，管理端同来源内供应商层先于协议层，其他供应商策略不得进入当前账户'
  )
}

console.log('流式拦截策略回归通过：策略读取上限、协议/供应商层隔离、首个空 Chat chunk 缓冲、事件丢弃、流丢弃、失败替换、试运行、来源排序、写下游前服务端重试和图像文本扫描跳过符合预期')

function assertStreamInterceptPolicyRepositoryGuards(): void {
  const repositorySource = readFileSync(new URL('../../storage/stream-intercept-policy.repository.ts', import.meta.url), 'utf8')
  const routesSource = readFileSync(new URL('../../modules/stream-intercept-policies/stream-intercept-policies.routes.ts', import.meta.url), 'utf8')
  const schemaSource = readFileSync(new URL('../../storage/schema/business-schema.ts', import.meta.url), 'utf8')
  const responseFinalizationSource = readFileSync(new URL('../../modules/gateway/openai-gateway-response-finalization.ts', import.meta.url), 'utf8')
  const accountEffectsSource = readFileSync(new URL('../../modules/gateway/openai-gateway-account-effects.ts', import.meta.url), 'utf8')
  const dbServiceTypesSource = readFileSync(new URL('../../modules/db-service/db-service-types.ts', import.meta.url), 'utf8')
  const dbServiceHandlersSource = readFileSync(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url), 'utf8')
  const gatewayListBody = sourceFunctionBlock(repositorySource, 'export function listActiveStreamInterceptPoliciesForGateway')
  const listBody = sourceFunctionBlock(repositorySource, 'function listStreamInterceptPolicyRows')
  const createBody = sourceFunctionBlock(repositorySource, 'export function createStreamInterceptPolicy')
  const runtimeSideEffectsBody = sourceFunctionBlock(responseFinalizationSource, 'function applyStreamInterceptPolicyRuntimeSideEffects')
  assert(repositorySource.includes('maxManagementStreamInterceptPolicies'), '管理端流式拦截策略必须有固定数量上限')
  assert(repositorySource.includes("id: 'default_gpt_cyber_policy'"), 'cyber_policy 默认规则必须命名为 GPT 供应商层规则')
  assert(!repositorySource.includes("id: 'default_openai_cyber_policy'"), 'cyber_policy 默认规则不能继续命名为 OpenAI 协议层规则')
  assert(!repositorySource.includes('normalizeSetValue('), '流式拦截策略不应再用旧动作兜底模板吞掉非法 action')
  assert(!repositorySource.includes('Number(value)'), '流式拦截策略写入路径不应接收数字字符串')
  assert(!repositorySource.includes('value.split(/[,;'), '流式拦截策略匹配条件不应接收旧字符串列表格式')
  assert(!repositorySource.includes("fallback = 'openai'"), '流式拦截策略不应缺省回填 OpenAI 协议')
  assert(!repositorySource.includes("normalizeProtocolCode(row.protocol_code, 'openai')"), '流式拦截策略读取不应缺省回填 OpenAI 协议')
  assert(repositorySource.includes('normalizePolicyAction'), '流式拦截策略必须显式校验 action 模板')
  assert(!repositorySource.includes('avoidanceTtlSeconds'), '流式拦截策略不应保存用户配置的避让秒数字段')
  assert(!repositorySource.includes('avoidance_ttl_seconds'), '流式拦截策略表不应保留用户配置的避让秒数字段')
  assert(!routesSource.includes('avoidanceTtlSeconds'), '流式拦截策略 API 不应接受用户配置的避让秒数字段')
  assert(!schemaSource.includes('avoidance_ttl_seconds'), '流式拦截策略 schema 不应包含用户配置的避让秒数字段')
  assert(runtimeSideEffectsBody.includes('markGatewayAccountTemporaryUnavailableWithCacheInvalidation'), '流式拦截当前账号短期避让必须写入通用临时不可调用入口')
  assert(!runtimeSideEffectsBody.includes('suppressGatewayAccountLocallyForSeconds(account'), '流式拦截当前账号短期避让不能只做本地 TTL 屏蔽')
  assert(accountEffectsSource.includes("type: 'mark_account_temporary_unavailable'"), '流式拦截账号避让必须通过 db-service 写统一账号状态')
  assert(dbServiceTypesSource.includes("type: 'mark_account_temporary_unavailable'"), 'db-service 必须声明通用临时不可调用写入操作')
  assert(dbServiceHandlersSource.includes('markAccountTemporaryUnavailable(operation.account.id, operation.reason)'), 'db-service 普通账号临时不可调用必须复用统一仓储入口')
  assert(dbServiceHandlersSource.includes('markAuthorizedAccountBindingTemporaryUnavailableByContext'), 'db-service 授权绑定临时不可调用必须复用统一仓储入口')
  assert(gatewayListBody.includes('protocol_code = ?'), '网关运行态读取流式拦截策略必须按协议收窄')
  assert(gatewayListBody.includes("scope_type = 'protocol'"), '网关运行态读取流式拦截策略必须保留协议层策略')
  assert(gatewayListBody.includes("scope_type = 'provider'"), '网关运行态读取流式拦截策略必须按供应商层收窄')
  assert(gatewayListBody.includes('provider_code = ?'), '网关运行态读取流式拦截策略必须按当前供应商过滤')
  assert(gatewayListBody.includes('LIMIT ?'), '网关运行态读取流式拦截策略必须带固定上限')
  assert(gatewayListBody.includes('maxManagementStreamInterceptPolicies'), '网关运行态读取流式拦截策略必须复用管理端数量上限')
  assert(listBody.includes('LIMIT ?'), '管理端策略列表不能无上限读取策略表')
  assert(createBody.includes('assertManagementPolicyCapacity()'), '创建管理端流式拦截策略前必须校验总量上限')
  assert(!repositorySource.includes('COUNT(*) AS total FROM stream_intercept_policies'), '创建管理端流式拦截策略不能用 COUNT(*) 容量预检')
  assert(repositorySource.includes('SELECT id FROM stream_intercept_policies LIMIT ?'), '创建管理端流式拦截策略容量预检必须使用固定窗口')
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const nextFunction = source.indexOf('\nfunction ', start + marker.length)
  const nextExportFunction = source.indexOf('\nexport function ', start + marker.length)
  const candidates = [nextFunction, nextExportFunction].filter((index) => index >= 0)
  const end = candidates.length ? Math.min(...candidates) : source.length
  return source.slice(start, end)
}
