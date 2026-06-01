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

function policy(overrides: Partial<RuntimeStreamInterceptPolicy>): RuntimeStreamInterceptPolicy {
  return {
    id: 'policy_test',
    source: 'management',
    name: '测试流式拦截策略',
    enabled: true,
    action: 'fail_stream',
    executionMode: 'intercept',
    priority: 10,
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

const pollutedEvent = sseEvent('response.output_text.delta', {
  delta: '这里被插入了广告污染'
})

const visibleOutputEvent = sseEvent('response.output_text.delta', {
  delta: '这段正常输出还没有写给客户端'
})

const settings: GatewaySettings = {
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
  const missingRuntimeFieldsValidation = validateAccountStreamInterceptRules([
    {
      action: 'fail_stream',
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
      action: 'fail_stream'
    }
  ])
  assert.equal(tooManyMatchersValidation.valid, false, '账户级流式规则不应静默截断超过 50 项的匹配列表')
  assert.throws(
    () => resolveRuntimeStreamInterceptPolicies({
      account: {
        providerCode: 'openai',
        credentials: {
          stream_intercept_rules: [
            {
              enabled: true,
              name: '运行时过多匹配项账户规则',
              priority: 30,
              match: {
                textIncludes: Array.from({ length: 51 }, (_, index) => `污染-${index}`)
              },
              action: 'fail_stream'
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
        accountState: 'runtime_avoidance',
        avoidanceTtlSeconds: 300
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
  const defaultPolicies = resolveRuntimeStreamInterceptPolicies({
    account: {
      providerCode: 'openai',
      credentials: {}
    } as never,
    managementPolicies: listStreamInterceptPolicyDefaultRules()
  })
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
      providerCode: 'openai',
      credentials: {
        stream_intercept_rules: [
          {
            id: 'account_rule_high_priority',
            name: '账户高优先级数字规则',
            enabled: true,
            priority: 9999,
            match: { textIncludes: ['广告污染'] },
            action: 'fail_stream'
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
        providerCode: 'openai',
        match: { textIncludes: ['广告污染'] },
        action: 'retry_next_account'
      },
      {
        id: 'management_rule_low_priority',
        defaultRule: false,
        editable: true,
        name: '管理端低数字规则',
        enabled: true,
        priority: 1,
        providerCode: 'openai',
        match: { textIncludes: ['广告污染'] },
        action: 'retry_next_account'
      }
    ]
  })
  assert.deepEqual(
    resolved.map((item) => item.id),
    ['account_rule_high_priority', 'management_rule_low_priority', 'default_rule_low_priority'],
    '运行时合并必须先按账户 / 管理端 / 默认来源排序，同来源内再按优先级排序'
  )
}

console.log('流式拦截策略回归通过：策略读取上限、事件丢弃、流丢弃、失败替换、试运行、来源排序、写下游前服务端重试和图像文本扫描跳过符合预期')

function assertStreamInterceptPolicyRepositoryGuards(): void {
  const repositorySource = readFileSync(new URL('../../storage/stream-intercept-policy.repository.ts', import.meta.url), 'utf8')
  const gatewayListBody = sourceFunctionBlock(repositorySource, 'export function listActiveStreamInterceptPoliciesForGateway')
  const listBody = sourceFunctionBlock(repositorySource, 'function listStreamInterceptPolicyRows')
  const createBody = sourceFunctionBlock(repositorySource, 'export function createStreamInterceptPolicy')
  assert(repositorySource.includes('maxManagementStreamInterceptPolicies'), '管理端流式拦截策略必须有固定数量上限')
  assert(!repositorySource.includes('normalizeSetValue('), '流式拦截策略不应再用旧动作兜底模板吞掉非法 action')
  assert(!repositorySource.includes('Number(value)'), '流式拦截策略写入路径不应接收数字字符串')
  assert(!repositorySource.includes('value.split(/[,;'), '流式拦截策略匹配条件不应接收旧字符串列表格式')
  assert(!repositorySource.includes("fallback = 'openai'"), '流式拦截策略不应缺省回填 OpenAI 供应商')
  assert(!repositorySource.includes("normalizeProviderCode(row.provider_code, 'openai')"), '流式拦截策略读取不应缺省回填 OpenAI 供应商')
  assert(repositorySource.includes('normalizePolicyAction'), '流式拦截策略必须显式校验 action 模板')
  assert(repositorySource.includes('短期避让模板需要配置避让秒数'), '短期避让策略必须显式要求 TTL')
  assert(gatewayListBody.includes('provider_code = ?'), '网关运行态读取流式拦截策略必须按供应商收窄')
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
