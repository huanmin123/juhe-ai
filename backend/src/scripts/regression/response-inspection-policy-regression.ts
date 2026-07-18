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
import {
  codexCompactionContractMismatchFrame,
  codexCompactionExpectedForRequest,
  countCodexCompactionOutputItemsFromJson
} from '../../modules/gateway/response/codex-compaction-contract.js'
import {
  extractAnthropicJsonSemanticFrames,
  extractAnthropicSseSemanticFrames
} from '../../modules/gateway/protocols/anthropic-v1/response-semantics.js'
import {
  extractGeminiJsonSemanticFrames
} from '../../modules/gateway/protocols/gemini-v1beta/response-semantics.js'
import { parseOpenAISseEventText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'
import {
  shouldRetryResponseInspectionDecisionOnServer
} from '../../modules/gateway/response/stream-finalization-retry-decision.js'
import { ANTHROPIC_PROVIDER_CODE, ANTHROPIC_PROTOCOL_CODE, GEMINI_PROTOCOL_CODE, GEMINI_PROVIDER_CODE, GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { closeStorageDatabases, getBusinessDatabase } from '../../storage/database.js'
import {
  createResponseInspectionPolicy,
  createResponseInspectionPolicyAsync,
  deleteResponseInspectionPolicyAsync,
  listResponseInspectionPolicyDefaultRules,
  listResponseInspectionPoliciesAsync,
  updateResponseInspectionPolicy,
  updateResponseInspectionPolicyAsync
} from '../../storage/response-inspection-policy.repository.js'
import { closeSqliteReadWorkerPool } from '../../storage/sqlite-read-worker-pool.js'

const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  textFirstResponseTimeoutSeconds: 120,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 1800,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 3600,
  noAvailableAccountWaitTimeoutSeconds: 270,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
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

async function assertMalformedResponsesSseFailsBeforeDownstreamCommit(
  name: string,
  chunks: Buffer[]
): Promise<void> {
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
      throw new Error(`${name} 写下游前应交给调度层重试`)
    },
    end() {
      this.writableEnded = true
      return this
    }
  }
  async function* upstreamChunks(): AsyncIterable<Uint8Array> {
    for (const chunk of chunks) {
      yield chunk
    }
  }
  const result = await pipeUpstreamStream(
    upstreamChunks(),
    response as never,
    settings,
    Date.now(),
    async () => { failureCalled = true },
    undefined,
    {
      clientRetryEnabled: true,
      retryBeforeDownstreamWriteUntilOutput: true,
      endpointFamily: 'responses'
    }
  )
  assert.equal(result.completed, false, `${name} 应在缺少可解析终止事件时返回失败结果`)
  assert.equal(result.errorCode, 'upstream_retryable_error', `${name} 应按 Codex 可重试流错误收口`)
  assert.equal(failureCalled, true, `${name} 应触发流失败回调供调度层处理`)
  assert.equal(response.headersSent, false, `${name} 不应提前发送响应头`)
  assert.equal(response.writableEnded, false, `${name} 不应结束客户端响应`)
  assert.equal(result.downstreamBytesWritten, 0, `${name} 不应写出畸形 SSE 到客户端`)
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
  const frames = extractAnthropicJsonSemanticFrames({
    id: 'msg_anthropic_semantics',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [
      {
        type: 'text',
        text: '这里包含 Anthropic 污染文本'
      }
    ],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 9,
      output_tokens: 3,
      cache_read_input_tokens: 2
    }
  }, 'messages')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        protocolCode: ANTHROPIC_PROTOCOL_CODE,
        providerCode: ANTHROPIC_PROVIDER_CODE,
        match: {
          outputTextIncludes: ['Anthropic 污染文本'],
          finishReasons: ['end_turn'],
          clientProfiles: ['generic_anthropic']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_anthropic'
    }
  })
  assert.equal(result.decision?.endpointFamily, 'messages', 'Anthropic Messages JSON 必须保留端点家族')
  assert.equal(result.decision?.policyProtocolCode, ANTHROPIC_PROTOCOL_CODE, 'Anthropic 响应策略必须保留协议维度')
  assert.equal(result.decision?.matchedField, 'outputTextIncludes', 'Anthropic JSON content[].text 应映射为输出文本语义帧')
}

{
  const frames = extractAnthropicSseSemanticFrames(parseOpenAISseEventText([
      'event: content_block_delta',
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Anthropic SSE 污染片段"}}',
      ''
    ].join('\n')), 'messages')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        protocolCode: ANTHROPIC_PROTOCOL_CODE,
        providerCode: ANTHROPIC_PROVIDER_CODE,
        match: {
          outputTextIncludes: ['Anthropic SSE 污染片段'],
          clientProfiles: ['claude_code']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'sse',
    context: {
      clientProfile: 'claude_code'
    }
  })
  assert.equal(result.decision?.endpointFamily, 'messages', 'Anthropic Messages SSE 必须保留端点家族')
  assert.equal(result.decision?.clientProfile, 'claude_code', 'Claude Code 画像应作为策略上下文记录')
  assert.equal(result.decision?.matchedField, 'outputTextIncludes', 'Anthropic SSE text_delta 应映射为输出文本语义帧')
}

assert.equal(validateAccountResponseInspectionRules([
  {
    enabled: true,
    name: '账户响应检查',
    priority: 10,
    match: {
      clientProfiles: ['codex'],
      outputTextIncludes: ['污染']
    },
    action: 'retry_next_account'
  }
]).valid, true, '账户级响应检查规则应接受新响应检查 matcher')
assert.equal(validateAccountResponseInspectionRules([
  {
    enabled: true,
    name: 'Gemini CLI 响应检查',
    priority: 10,
    match: {
      clientProfiles: ['gemini_cli', 'generic_gemini'],
      errorTypes: ['UNAVAILABLE']
    },
    action: 'retry_next_account'
  }
]).valid, true, '账户级响应检查规则应接受 Gemini 客户端画像')
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
]).valid, false, '账户级响应检查规则必须拒绝未知请求客户端')
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
  assert.equal(codexResult.decision?.clientProfile, 'codex', '响应检查决策应记录命中时的请求客户端')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    error: {
      code: 'unscoped_upstream_error_code',
      message: 'unscoped upstream code must not drive routing'
    }
  }, 'responses')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        scopeType: 'protocol',
        providerCode: undefined,
        match: {
          errorCodes: ['unscoped_upstream_error_code']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_openai',
      accountClientCompatibility: 'openai_standard'
    }
  })
  assert.equal(result.decision, undefined, '未绑定客户端画像的协议级响应检查策略不能只靠上游 errorCode 命中')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    error: {
      code: 'cyber_policy',
      message: 'GPT provider policy applies to every downstream client'
    }
  }, 'responses')
  const defaultRules = listResponseInspectionPolicyDefaultRules()
  const gptPolicies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_gpt_generic_client',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  const genericResult = inspectResponseSemanticFrames({
    frames,
    policies: gptPolicies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_openai',
      accountClientCompatibility: 'openai_standard'
    }
  })
  assert.equal(genericResult.decision?.policyId, 'default_gpt_cyber_policy', 'GPT cyber_policy 必须覆盖普通 OpenAI 下游客户端')

  const compatiblePolicies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_openai_compatible_generic_client',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  const compatibleResult = inspectResponseSemanticFrames({
    frames,
    policies: compatiblePolicies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_openai',
      accountClientCompatibility: 'openai_standard'
    }
  })
  assert.notEqual(compatibleResult.decision?.policyId, 'default_gpt_cyber_policy', 'GPT cyber_policy 供应商规则不得扩散到普通 OpenAI-compatible 供应商')
}

{
  const frames = extractAnthropicJsonSemanticFrames({
    type: 'error',
    error: {
      type: 'overloaded_error',
      message: 'overloaded error type should drive provider routing'
    }
  }, 'messages')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        protocolCode: ANTHROPIC_PROTOCOL_CODE,
        providerCode: ANTHROPIC_PROVIDER_CODE,
        match: {
          errorTypes: ['overloaded_error']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_anthropic'
    }
  })
  assert.equal(result.decision?.matchedField, 'errorTypes', '未绑定客户端画像的响应检查策略应允许按协议 errorType 命中')
}

{
  const frames = extractOpenAIJsonSemanticFrames({
    choices: [
      {
        message: {
          role: 'assistant',
          content: '命中服务端换号检查'
        }
      }
    ]
  }, 'chat_completions')
  const result = inspectResponseSemanticFrames({
    frames,
    policies: [
      responsePolicy({
        retryEnabled: false,
        match: {
          outputTextIncludes: ['服务端换号检查']
        }
      })
    ],
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_openai',
      accountClientCompatibility: 'openai_standard'
    }
  })
  assert.equal(result.decision?.retryEnabled, false, '策略自身的客户端重试标记应保留原值')
  assert.equal(shouldRetryResponseInspectionDecisionOnServer(result.decision, {
    headersSent: false,
    writableEnded: false,
    destroyed: false
  }), true, '下游未提交时，配置化响应检查命中应允许服务端换号，不依赖 retryEnabled')
}

{
  const frames = extractGeminiJsonSemanticFrames({
    error: {
      code: 503,
      status: 'UNAVAILABLE',
      message: 'Gemini upstream temporary unavailable'
    }
  }, 'generate_content')
  const policies = [
    responsePolicy({
      scopeType: 'protocol',
      protocolCode: GEMINI_PROTOCOL_CODE,
      providerCode: undefined,
      match: {
        clientProfiles: ['gemini_cli'],
        errorTypes: ['UNAVAILABLE']
      }
    })
  ]
  const genericResult = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_gemini'
    }
  })
  assert.equal(genericResult.decision, undefined, 'Gemini CLI 专属响应检查不能污染通用 Gemini 客户端')
  const cliResult = inspectResponseSemanticFrames({
    frames,
    policies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'gemini_cli'
    }
  })
  assert.equal(cliResult.decision?.matchedField, 'errorTypes', 'Gemini CLI 专属规则应由 Gemini error.status 语义字段触发')
  assert.equal(cliResult.decision?.clientProfile, 'gemini_cli', 'Gemini CLI 画像应作为策略上下文记录')
  assert.equal(cliResult.decision?.policyProtocolCode, GEMINI_PROTOCOL_CODE, 'Gemini CLI 响应策略必须保留 Gemini 协议维度')
}

{
  const defaultRules = listResponseInspectionPolicyDefaultRules()
  assert(defaultRules.some((rule) => rule.match.jsonPathsExists?.includes('error')), '默认规则必须覆盖 OpenAI JSON / SSE error 对象')
  assert(defaultRules.some((rule) => rule.protocolCode === ANTHROPIC_PROTOCOL_CODE && rule.match.jsonPathsExists?.includes('error')), '默认规则必须覆盖 Anthropic JSON / SSE error 对象')
  assert(defaultRules.some((rule) => rule.protocolCode === GEMINI_PROTOCOL_CODE && rule.match.jsonPathsExists?.includes('error')), '默认规则必须覆盖 Gemini JSON / SSE error 对象')
  assert(defaultRules.some((rule) => rule.id === 'default_gemini_cli_retryable_error' && rule.match.clientProfiles?.includes('gemini_cli') && rule.match.errorTypes?.includes('UNAVAILABLE')), '默认规则必须覆盖 Gemini CLI 专属可重试错误')
  assert(defaultRules.some((rule) => rule.match.clientProfiles?.includes('codex') && rule.match.finishReasons?.includes('incomplete')), '默认规则必须覆盖 Codex response.incomplete')
  assert(defaultRules.some((rule) => rule.id === 'default_codex_compaction_contract' && rule.match.errorCodes?.includes('codex_compaction_contract_mismatch')), '默认规则必须覆盖 Codex compact 输出契约错误')
  assert(defaultRules.some((rule) => rule.providerCode === GPT_VENDOR_CODE && !rule.match.clientProfiles?.length && rule.match.errorCodes?.includes('cyber_policy')), 'GPT cyber_policy 必须作为不限制下游客户端的 GPT provider 规则存在')
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
  const anthropicPolicies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_anthropic',
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      providerCode: ANTHROPIC_PROVIDER_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  const geminiPolicies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_gemini',
      protocolCode: GEMINI_PROTOCOL_CODE,
      providerCode: GEMINI_PROVIDER_CODE,
      credentials: {}
    } as never,
    managementPolicies: defaultRules
  })
  assert(gptPolicies.some((policy) => policy.match.errorCodes?.includes('cyber_policy')), 'GPT 供应商应加载 cyber_policy 默认规则')
  assert.equal(genericPolicies.some((policy) => policy.match.errorCodes?.includes('cyber_policy')), false, '通用 OpenAI-compatible 供应商不应继承 GPT cyber_policy 规则')
  assert(anthropicPolicies.some((policy) => policy.protocolCode === ANTHROPIC_PROTOCOL_CODE && policy.match.jsonPathsExists?.includes('error')), 'Anthropic 供应商应加载 Anthropic 协议默认 error 对象规则')
  assert.equal(anthropicPolicies.some((policy) => policy.match.errorCodes?.includes('cyber_policy')), false, 'Anthropic 供应商不应继承 GPT cyber_policy 规则')
  assert(geminiPolicies.some((policy) => policy.id === 'default_gemini_cli_retryable_error' && policy.match.clientProfiles?.includes('gemini_cli')), 'Gemini 供应商应加载 Gemini CLI 专属默认响应策略')
  assert(geminiPolicies.some((policy) => policy.id === 'default_gemini_error_object' && policy.match.jsonPathsExists?.includes('error')), 'Gemini 供应商应加载通用 Gemini error 对象规则')
  const retryableGeminiFrames = extractGeminiJsonSemanticFrames({
    error: {
      code: 503,
      status: 'UNAVAILABLE',
      message: 'transient gemini error'
    }
  }, 'generate_content')
  const genericGeminiInspection = inspectResponseSemanticFrames({
    frames: retryableGeminiFrames,
    policies: geminiPolicies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'generic_gemini'
    }
  })
  assert.equal(genericGeminiInspection.decision?.policyId, 'default_gemini_error_object', '通用 Gemini 客户端只应命中通用 error 对象规则')
  const geminiCliInspection = inspectResponseSemanticFrames({
    frames: retryableGeminiFrames,
    policies: geminiPolicies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'gemini_cli'
    }
  })
  assert.equal(geminiCliInspection.decision?.policyId, 'default_gemini_cli_retryable_error', 'Gemini CLI 客户端应优先命中专属可重试规则')
  assert.equal(geminiCliInspection.decision?.accountSwitch, 'request_next_account', 'Gemini CLI 可重试默认规则必须请求下一个账号')
  assert.equal(shouldRetryResponseInspectionDecisionOnServer(geminiCliInspection.decision, {
    headersSent: false,
    writableEnded: false,
    destroyed: false
  }), true, 'Gemini CLI 可重试默认规则必须允许下游写出前服务端换账号')
}

{
  const compactBody = {
    model: 'gpt-5.5',
    input: [
      {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'compact' }]
      },
      { type: 'compaction_trigger' }
    ],
    stream: true
  }
  const request = {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: compactBody,
    rawBody: Buffer.from(JSON.stringify(compactBody), 'utf8')
  }
  assert.equal(codexCompactionExpectedForRequest(request as never), true, '包含 compaction_trigger 的 Responses 请求必须进入 Codex compact 契约检查')
  assert.equal(codexCompactionExpectedForRequest({ ...request, originalUrl: '/v1/responses/compact', body: {} } as never), true, '/responses/compact 必须进入 Codex compact 契约检查')
  assert.equal(codexCompactionExpectedForRequest({ ...request, body: { model: 'gpt-5.5', input: 'hello', stream: true }, rawBody: Buffer.from('{"input":"hello"}') } as never), false, '普通 Responses 请求不能误启 compact 契约检查')
  const malformedCounts = countCodexCompactionOutputItemsFromJson({
    output: [
      { type: 'compaction', encrypted_content: null }
    ]
  })
  assert.equal(malformedCounts?.compactionItemCount, 0, '缺少字符串 encrypted_content 的 compaction item 不能算作 Codex 可接受 compact 输出')
  const aliasCounts = countCodexCompactionOutputItemsFromJson({
    output: [
      { type: 'compaction_summary', encrypted_content: 'mock-encrypted-context' }
    ]
  })
  assert.equal(aliasCounts?.compactionItemCount, 1, 'Codex 接受的 compaction_summary 别名必须计入合法 compact 输出')
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_contract',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
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
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const first = buffer.pushChunk(sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_msg',
      type: 'message',
      status: 'completed',
      content: [{ type: 'output_text', text: 'bad visible text before compact done' }]
    }
  }))
  assert.equal(first.chunks.length, 0, 'Codex compact 契约确认前不应释放非 compact output item')
  const second = buffer.pushChunk(sseEvent('response.output_item.done', {
    output_index: 1,
    item: {
      id: 'item_call',
      type: 'function_call',
      status: 'completed',
      name: 'mock_tool'
    }
  }))
  assert.equal(second.chunks.length, 0, 'Codex compact 第二个非 compact output item 仍不得提前释放')
  const completed = buffer.pushChunk(sseEvent('response.completed', {
    response: {
      id: 'resp_bad_compaction',
      status: 'completed'
    }
  }))
  const responseBody = Buffer.concat(completed.chunks).toString('utf8')
  assert.equal(completed.intercepted?.policyId, 'default_codex_compaction_contract', 'Codex compact 完成时缺少 compaction item 必须命中默认契约规则')
  assert.equal(completed.intercepted?.codexCompactionExpected, true, '响应检查决策应记录 Codex compact 期望上下文')
  assert(responseBody.includes('upstream_retryable_error'), `Codex compact 契约错误应改写为客户端可重试失败：${responseBody}`)
  assert(!responseBody.includes('bad visible text before compact done'), `坏 compact output 不应在失败前泄露给客户端：${responseBody}`)
  assert.equal(shouldRetryResponseInspectionDecisionOnServer(completed.intercepted, {
    headersSent: false,
    writableEnded: false,
    destroyed: false
  } as never), true, 'Codex compact 默认契约规则必须允许服务端换号重试')
}

{
  const event = sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_generic_message',
      type: 'message',
      status: 'completed',
      content: [{ type: 'output_text', text: 'generic compact passthrough' }]
    }
  })
  const buffer = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses',
    policies: [],
    context: {
      clientProfile: 'generic_openai',
      accountClientCompatibility: 'openai_standard',
      codexCompactionExpected: true
    }
  })
  const result = buffer.pushChunk(event)
  assert.equal(Buffer.concat(result.chunks).toString('utf8'), event.toString('utf8'), '非 Codex 兼容上下文不得启用 Codex compact 暂存检查')
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_valid',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
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
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const outputItem = sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_compaction',
      type: 'compaction',
      status: 'completed',
      encrypted_content: 'mock-encrypted-context'
    }
  })
  const first = buffer.pushChunk(outputItem)
  assert.equal(first.chunks.length, 0, '合法 compact output 也必须等 response.completed 后一起释放')
  const completedEvent = sseEvent('response.completed', {
    response: {
      id: 'resp_valid_compaction',
      status: 'completed'
    }
  })
  const completed = buffer.pushChunk(completedEvent)
  const responseBody = Buffer.concat(completed.chunks).toString('utf8')
  assert.equal(completed.intercepted, undefined, '恰好 1 个 compaction output item 不应命中契约错误')
  assert(responseBody.includes('mock-encrypted-context'), `合法 compact output 应在 completed 后释放：${responseBody}`)
  assert(responseBody.includes('response.completed'), `合法 compact completed 应随暂存事件一起释放：${responseBody}`)
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_eof_before_completed',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
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
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const outputItem = sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_compaction_eof_before_completed',
      type: 'compaction',
      status: 'completed',
      encrypted_content: 'mock-encrypted-context'
    }
  })
  const first = buffer.pushChunk(outputItem)
  assert.equal(first.chunks.length, 0, 'compact EOF 前不能提前释放暂存 output item')
  assert.equal(first.pendingEvent, true, 'compact 暂存事件必须标记为 pending')
  const completed = buffer.flushPendingOnEof()
  const responseBody = Buffer.concat(completed.chunks).toString('utf8')
  assert.equal(completed.pendingEvent, false, 'compact EOF 失败收尾后不应继续保留 pending 状态')
  assert.equal(completed.intercepted?.policyId, 'default_codex_compaction_contract', 'compact EOF 前缺少 response.completed 必须按契约错误拦截')
  assert(responseBody.includes('upstream_retryable_error'), `compact EOF 契约错误应改写为客户端可重试失败：${responseBody}`)
  assert(!responseBody.includes('mock-encrypted-context'), `compact EOF 缺少 completed 时不得释放暂存 output：${responseBody}`)
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_malformed_shape',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
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
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const outputItem = sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_compaction_without_encrypted_content',
      type: 'compaction',
      status: 'completed'
    }
  })
  const first = buffer.pushChunk(outputItem)
  assert.equal(first.chunks.length, 0, '形状不合法的 compact output 在 completed 前也不能释放')
  const completed = buffer.pushChunk(sseEvent('response.completed', {
    response: {
      id: 'resp_malformed_compaction_shape',
      status: 'completed'
    }
  }))
  const responseBody = Buffer.concat(completed.chunks).toString('utf8')
  assert.equal(completed.intercepted?.policyId, 'default_codex_compaction_contract', '看似 compaction 但缺少 encrypted_content 时必须按 Codex compact 契约错误拦截')
  assert(responseBody.includes('upstream_retryable_error'), `形状不合法 compact 输出应改写为客户端可重试失败：${responseBody}`)
  assert(!responseBody.includes('item_compaction_without_encrypted_content'), `形状不合法 compact output 不应泄露给客户端：${responseBody}`)
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_completed_missing_id',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
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
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const outputItem = sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_compaction_before_bad_completed',
      type: 'compaction',
      status: 'completed',
      encrypted_content: 'mock-encrypted-context'
    }
  })
  const first = buffer.pushChunk(outputItem)
  assert.equal(first.chunks.length, 0, 'compact completed 结构确认前不能释放合法 output item')
  const completed = buffer.pushChunk(sseEvent('response.completed', {
    response: {
      status: 'completed'
    }
  }))
  const responseBody = Buffer.concat(completed.chunks).toString('utf8')
  assert.equal(completed.intercepted?.policyId, 'default_codex_compaction_contract', 'compact response.completed 缺少 response.id 时必须按契约错误拦截')
  assert(responseBody.includes('upstream_retryable_error'), `缺少 response.id 的 compact completed 应改写为客户端可重试失败：${responseBody}`)
  assert(!responseBody.includes('item_compaction_before_bad_completed'), `completed 缺少 response.id 时不得释放暂存 compact output：${responseBody}`)
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_oversized',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
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
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const largeEncryptedContent = 'x'.repeat(1200 * 1024)
  const oversizedEvent = sseEvent('response.output_item.done', {
    output_index: 0,
    item: {
      id: 'item_compaction_oversized',
      type: 'compaction',
      status: 'completed',
      encrypted_content: largeEncryptedContent
    }
  })
  const first = buffer.pushChunk(oversizedEvent)
  assert.equal(first.intercepted, undefined, '合法 Codex compact 不能因单事件或累计字节大小被拒绝')
  assert.equal(first.chunks.length, 0, '大型合法 compact output 仍必须等 response.completed 后释放')
  const completed = buffer.pushChunk(sseEvent('response.completed', {
    response: {
      id: 'resp_large_compaction',
      status: 'completed'
    }
  }))
  const responseBody = Buffer.concat(completed.chunks).toString('utf8')
  assert.equal(completed.intercepted, undefined, '超过旧 1 MB 暂存上限的合法 compact 响应必须通过')
  assert(responseBody.includes(largeEncryptedContent), '大型 compact encrypted_content 必须完整透传')
  assert(responseBody.includes('response.completed'), '大型 compact 响应必须连同 response.completed 一起释放')
}

{
  const policies = resolveRuntimeResponseInspectionPolicies({
    account: {
      id: 'acct_codex_compaction_json',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      clientCompatibility: 'codex_responses',
      credentials: {}
    } as never,
    managementPolicies: listResponseInspectionPolicyDefaultRules()
  })
  const frame = codexCompactionContractMismatchFrame({
    outputItemCount: 2,
    compactionItemCount: 0,
    transport: 'json'
  })
  assert(frame, 'JSON compact 契约计数不满足时必须生成语义错误帧')
  const result = inspectResponseSemanticFrames({
    frames: [frame],
    policies,
    downstreamWritten: false,
    transport: 'json',
    context: {
      clientProfile: 'codex',
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  assert.equal(result.decision?.policyId, 'default_codex_compaction_contract', 'JSON compact 契约错误也必须命中默认规则')
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
  assert(responseBody.includes('上游流式响应在输出前失败，请重试'), `Codex 客户端预输出失败应返回统一可重试文案：${responseBody}`)
  assert(!responseBody.includes('internal_server_error'), `Codex 客户端失败事件不应透出上游原始错误码：${responseBody}`)
  assert(!responseBody.includes('mock upstream failed before output'), `Codex 客户端失败事件不应透出上游原始错误文案：${responseBody}`)
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
    async () => { failureCalled = true },
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
    async () => { failureCalled = true },
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
    yield sseEvent('response.completed', {
      type: 'response.completed',
      response: { status: 'completed' }
    })
    yield sseEvent('response.failed', {
      type: 'response.failed',
      response: {
        status: 'failed',
        error: {
          code: 'server_overloaded',
          message: 'late failure next chunk'
        }
      }
    })
  }
  const result = await pipeUpstreamStream(
    upstreamChunks(),
    response as never,
    settings,
    Date.now(),
    async () => { failureCalled = true },
    undefined,
    {
      endpointFamily: 'responses'
    }
  )
  assert.equal(result.completed, false, '终止事件后一批失败也应返回失败结果')
  assert.equal(result.errorCode, 'server_overloaded', '终止事件后一批失败应保留失败错误码')
  assert.equal(failureCalled, true, '终止事件后一批失败应触发失败回调')
  assert.equal(response.writableEnded, true, '终止事件后一批失败应结束客户端响应')
}

await assertMalformedResponsesSseFailsBeforeDownstreamCommit('response.failed 非 JSON data', [
  Buffer.from('event: response.failed\ndata: upstream gateway html error\n\n', 'utf8')
])

await assertMalformedResponsesSseFailsBeforeDownstreamCommit('response.completed 非 JSON data', [
  Buffer.from('event: response.completed\ndata: not-json\n\n', 'utf8')
])

await assertMalformedResponsesSseFailsBeforeDownstreamCommit('未闭合 data 直到 EOF', [
  Buffer.from('data: {"type":"response.output_text.delta","delta":"partial"', 'utf8')
])

{
  const repositorySource = readFileSync(new URL('../../storage/response-inspection-policy.repository.ts', import.meta.url), 'utf8')
  const responseFinalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
  const routeSource = readFileSync(new URL('../../modules/response-inspection-policies/response-inspection-policies.routes.ts', import.meta.url), 'utf8')
  const schemaSource = readFileSync(new URL('../../storage/schema/business-schema.ts', import.meta.url), 'utf8')
  const gatewayPreflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
  const fallbackCandidateSource = readFileSync(new URL('../../modules/gateway/dispatch/api-key-group-fallback-candidate.ts', import.meta.url), 'utf8')
  assert(repositorySource.includes('maxManagementResponseInspectionPolicies'), '管理端响应检查策略必须有固定数量上限')
  assert(repositorySource.includes('listResponseInspectionPoliciesAsync'), '管理端响应检查策略列表必须提供 async PG 入口')
  assert(repositorySource.includes('createResponseInspectionPolicyAsync'), '管理端响应检查策略创建必须提供 async PG 入口')
  assert(repositorySource.includes('updateResponseInspectionPolicyAsync'), '管理端响应检查策略更新必须提供 async PG 入口')
  assert(repositorySource.includes('deleteResponseInspectionPolicyAsync'), '管理端响应检查策略删除必须提供 async PG 入口')
  assert(repositorySource.includes('isProtocolProviderCodeAsync'), '响应检查策略 PG 写入校验供应商协议时不能回退同步 provider lookup')
  assert(repositorySource.includes('SELECT id FROM response_inspection_policies LIMIT ?'), '创建管理端响应检查策略容量预检必须使用固定窗口')
  assert(!repositorySource.includes('COUNT(*) AS total FROM response_inspection_policies'), '创建管理端响应检查策略不能用 COUNT(*) 容量预检')
  assert(repositorySource.includes('positiveMatchKeys'), '排除条件不能作为独立正向 matcher 使用')
  assert(responseFinalizationSource.includes('nonStreamResponseInspectionMaxBytes'), '非流式 JSON 响应检查必须有固定检查窗口')
  assert(responseFinalizationSource.includes('pipeNonStreamUpstreamResponseForInspection'), '非流式 JSON 必须先经过写前检查管道')
  assert(routeSource.includes("responseInspectionPoliciesRouter.put('/:id'"), '响应检查策略更新接口必须使用 PUT 全量替换，不保留 PATCH partial 语义')
  assert(!routeSource.includes("responseInspectionPoliciesRouter.patch('/:id'"), '响应检查策略更新接口不应继续暴露 PATCH partial 入口')
  assert(routeSource.includes('await listResponseInspectionPoliciesAsync()'), '响应检查策略管理端列表路由必须走 async 仓储入口')
  assert(routeSource.includes('await createResponseInspectionPolicyAsync(parsed.data)'), '响应检查策略管理端创建路由必须走 async 仓储入口')
  assert(routeSource.includes('await updateResponseInspectionPolicyAsync(req.params.id, parsed.data)'), '响应检查策略管理端更新路由必须走 async 仓储入口')
  assert(routeSource.includes('await deleteResponseInspectionPolicyAsync(req.params.id)'), '响应检查策略管理端删除路由必须走 async 仓储入口')
  assert(routeSource.includes('recordOperationLogAsync'), '响应检查策略管理端操作日志必须走 async 设置读取入口')
  assert(!routeSource.includes('recordOperationLog({'), '响应检查策略管理端不得重新调用同步操作日志入口')
  assert(schemaSource.includes("CHECK (action IN ('observe', 'drop_event', 'retry_no_avoidance', 'retry_next_account', 'avoid_account_ttl', 'avoid_upstream_bucket_ttl'))"), '响应检查策略动作必须有数据库 CHECK 约束')
  assert(schemaSource.includes('json_valid(match_json)'), '响应检查策略 match_json 必须有 JSON 有效性约束')
  assert(fallbackCandidateSource.includes('listCachedActiveResponseInspectionPoliciesForAccountsAsync'), 'API Key 分组 fallback 候选必须按目标候选账号集合的协议和供应商加载响应检查策略')
  assert(fallbackCandidateSource.includes('orderedQuotaAllowedAccounts'), 'API Key 分组 fallback 响应检查策略必须基于目标候选分组完成过滤和排序后的账号集合加载')
  assert(gatewayPreflightSource.includes('responseInspectionPolicies: candidate.responseInspectionPolicies'), 'fallback dispatch context 必须使用目标候选分组的响应检查策略')
  assert(!gatewayPreflightSource.includes('responseInspectionPolicies: input.responseInspectionPolicies'), 'fallback dispatch context 不得沿用原分组传入的响应检查策略')
  assert(
    gatewayPreflightSource.includes('runtimeResponseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesForAccountsAsync(candidateFilter.accounts)'),
    '主预检必须始终按模型与能力过滤后的最终候选账号集合重新加载响应检查策略'
  )
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

    const asyncListed = await listResponseInspectionPoliciesAsync()
    assert(asyncListed.policies.some((policy) => policy.id === created.id), 'async 列表 fallback 应读取同步创建的管理端策略')
    const asyncCreated = await createResponseInspectionPolicyAsync({
      name: 'async fallback 策略',
      enabled: true,
      priority: 13,
      scopeType: 'protocol',
      protocolCode: OPENAI_PROTOCOL_CODE,
      match: { errorCodes: ['async_fallback_error'] },
      action: 'observe',
      notes: '等待 async 更新清空'
    })
    const asyncUpdated = await updateResponseInspectionPolicyAsync(asyncCreated.id, {
      name: 'async fallback 策略更新',
      enabled: false,
      priority: 14,
      scopeType: 'protocol',
      protocolCode: OPENAI_PROTOCOL_CODE,
      match: { errorMessageIncludes: ['async fallback'] },
      action: 'retry_no_avoidance'
    })
    assert.equal(asyncUpdated?.notes, undefined, 'async PUT fallback 也应清空旧备注')
    assert.equal(asyncUpdated?.enabled, false, 'async PUT fallback 应按提交值更新启用状态')
    assert.equal(await deleteResponseInspectionPolicyAsync(asyncCreated.id), true, 'async 删除 fallback 应返回 true')
    const asyncListedAfterDelete = await listResponseInspectionPoliciesAsync()
    assert.equal(asyncListedAfterDelete.policies.some((policy) => policy.id === asyncCreated.id), false, 'async 删除 fallback 后列表不应再包含策略')

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
    await closeSqliteReadWorkerPool().catch(() => undefined)
    closeStorageDatabases()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

console.info('response inspection policy regression passed')
