import assert from 'node:assert/strict'
import type { Request } from 'express'

import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID } from '../../domain/provider-protocol.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamUrlsForAccount
} from '../../modules/providers/drivers/registry.js'
import { markGatewayUpstreamModelsProbe } from '../../modules/gateway/request/upstream-models-probe.js'

const geminiAccount = providerAccount({
  id: 'acc_gemini_models_probe',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  baseUrl: 'https://generativelanguage.googleapis.com/v1beta',
  supportedEndpointModes: ['generate_content_json', 'generate_content_sse']
})
const deepSeekAccount = providerAccount({
  id: 'acc_deepseek_models_probe',
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  baseUrl: 'https://api.deepseek.com',
  supportedEndpointModes: ['chat_json', 'chat_sse']
})
const glmAccount = providerAccount({
  id: 'acc_glm_models_probe',
  providerCode: 'glm',
  providerProtocolProfileId: 'profile_glm_general_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  baseUrl: 'https://open.bigmodel.cn/api/paas/v4/',
  supportedEndpointModes: ['chat_json', 'chat_sse']
})
const glmCodingAccount = providerAccount({
  ...glmAccount,
  id: 'acc_glm_coding_models_probe',
  providerProtocolProfileId: 'profile_glm_coding_openai_v1',
  baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4/'
})
const hybridGeminiAccount = providerAccount({
  id: 'acc_hybrid_gemini_models_probe',
  providerCode: 'hybrid',
  providerProtocolProfileId: HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID,
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  baseUrl: 'https://generativelanguage.googleapis.com/v1beta',
  supportedEndpointModes: ['generate_content_json', 'generate_content_sse']
})

assertModelsProbeContract({
  account: geminiAccount,
  modelsPath: '/v1beta/models?trace=gemini-catalog',
  expectedUrl: 'https://generativelanguage.googleapis.com/v1beta/models?trace=gemini-catalog'
})
assertModelsProbeContract({
  account: deepSeekAccount,
  modelsPath: '/v1/models?trace=deepseek-catalog',
  expectedUrl: 'https://api.deepseek.com/v1/models?trace=deepseek-catalog'
})
assertModelsProbeContract({
  account: glmAccount,
  modelsPath: '/v1/models?trace=glm-catalog',
  expectedUrl: 'https://open.bigmodel.cn/api/paas/v4/models?trace=glm-catalog'
})
assertModelsProbeContract({
  account: glmCodingAccount,
  modelsPath: '/v1/models?trace=glm-coding-catalog',
  expectedUrl: 'https://open.bigmodel.cn/api/coding/paas/v4/models?trace=glm-coding-catalog'
})
assertModelsProbeContract({
  account: hybridGeminiAccount,
  modelsPath: '/v1beta/models?trace=hybrid-gemini-catalog',
  expectedUrl: 'https://generativelanguage.googleapis.com/v1beta/models?trace=hybrid-gemini-catalog',
  assertWrongPath: false
})

assertRejectedMarkedModelsProbe(providerAccount({
  ...deepSeekAccount,
  id: 'acc_deepseek_oauth_models_probe',
  type: 'oauth'
}), 'DeepSeek 非 API Key 账户')
assertRejectedMarkedModelsProbe(providerAccount({
  ...glmAccount,
  id: 'acc_glm_oauth_models_probe',
  type: 'oauth'
}), 'GLM 非 API Key 账户')

const geminiAiStudioAccount = providerAccount({
  ...geminiAccount,
  id: 'acc_gemini_ai_studio_models_probe',
  type: 'google_oauth',
  apiKey: 'gemini-ai-studio-access-token',
  credentials: { oauth_type: 'ai_studio' }
})
assertModelsProbeContract({
  account: geminiAiStudioAccount,
  modelsPath: '/v1beta/models?trace=gemini-ai-studio-catalog',
  expectedUrl: 'https://generativelanguage.googleapis.com/v1beta/models?trace=gemini-ai-studio-catalog'
})

const geminiCodeAssistAccount = providerAccount({
  ...geminiAccount,
  id: 'acc_gemini_code_assist_models_probe',
  type: 'google_oauth',
  apiKey: 'gemini-access-token',
  credentials: {
    oauth_type: 'code_assist',
    project_id: 'code-assist-project'
  }
})
const geminiCodeAssistProbe = markGatewayUpstreamModelsProbe(request('GET', '/v1beta/models'))
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(geminiCodeAssistAccount, geminiCodeAssistProbe),
  [],
  'Gemini Code Assist / Google One 仍不得把模型目录探针发往 Developer API'
)
assert.equal(
  accountSupportsGatewayRequest(geminiCodeAssistProbe, geminiCodeAssistAccount),
  false,
  'Gemini Code Assist / Google One 仍不得承接模型目录探针'
)
const geminiGoogleOneAccount = providerAccount({
  ...geminiCodeAssistAccount,
  id: 'acc_gemini_google_one_models_probe',
  credentials: {
    oauth_type: 'google_one',
    project_id: 'google-one-project'
  }
})
assertRejectedMarkedModelsProbe(geminiGoogleOneAccount, 'Gemini Google One OAuth 账户')

console.log('供应商模型目录探针回归通过：Gemini native / hybrid、DeepSeek、GLM 仅放行内部标记的正确 GET models，Code Assist、Google One 与客户端请求继续拒绝')

function assertModelsProbeContract(input: {
  account: DispatchAccountSecret
  modelsPath: string
  expectedUrl: string
  assertWrongPath?: boolean
}): void {
  const markedProbe = markGatewayUpstreamModelsProbe(request('GET', input.modelsPath))
  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(input.account, markedProbe),
    [input.expectedUrl],
    `${input.account.providerCode} 的内部模型目录探针必须保留查询参数并构造上游 URL`
  )
  assert.equal(
    accountSupportsGatewayRequest(markedProbe, input.account),
    true,
    `${input.account.providerCode} 的内部模型目录探针必须通过候选能力筛选`
  )

  const unmarkedModelsRequest = request('GET', input.modelsPath)
  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(input.account, unmarkedModelsRequest),
    [],
    `${input.account.providerCode} 不得把未标记的客户端 models 请求转发上游`
  )
  assert.equal(
    accountSupportsGatewayRequest(unmarkedModelsRequest, input.account),
    false,
    `${input.account.providerCode} 不得由未标记的客户端 models 请求命中账户`
  )

  const wrongMethodProbe = markGatewayUpstreamModelsProbe(request('POST', input.modelsPath))
  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(input.account, wrongMethodProbe),
    [],
    `${input.account.providerCode} 不得放行已标记但方法错误的模型目录请求`
  )
  assert.equal(
    accountSupportsGatewayRequest(wrongMethodProbe, input.account),
    false,
    `${input.account.providerCode} 不得承接已标记但方法错误的模型目录请求`
  )

  if (input.assertWrongPath !== false) {
    const wrongPathProbe = markGatewayUpstreamModelsProbe(request('GET', '/v1/not-a-models-path'))
    assert.deepEqual(
      buildGatewayUpstreamUrlsForAccount(input.account, wrongPathProbe),
      [],
      `${input.account.providerCode} 不得放行已标记但路径错误的模型目录请求`
    )
    assert.equal(
      accountSupportsGatewayRequest(wrongPathProbe, input.account),
      false,
      `${input.account.providerCode} 不得承接已标记但路径错误的模型目录请求`
    )
  }
}

function assertRejectedMarkedModelsProbe(account: DispatchAccountSecret, label: string): void {
  const probe = markGatewayUpstreamModelsProbe(request('GET', '/v1/models'))
  assert.deepEqual(buildGatewayUpstreamUrlsForAccount(account, probe), [], `${label} 不得转发模型目录探针`)
  assert.equal(accountSupportsGatewayRequest(probe, account), false, `${label} 不得承接模型目录探针`)
}

function providerAccount(input: Partial<DispatchAccountSecret> & Pick<DispatchAccountSecret, 'id' | 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'baseUrl' | 'supportedEndpointModes'>): DispatchAccountSecret {
  return {
    id: input.id,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: `${input.providerCode} 模型目录探针回归账户`,
    type: input.type ?? 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: input.supportedEndpointModes,
    supportedModels: [],
    healthCheckEndpointMode: input.protocolCode === 'gemini' ? 'generate_content_json' : 'chat_json',
    baseUrl: input.baseUrl,
    apiKey: input.apiKey ?? 'provider-model-catalog-probe-key',
    streamFailureCount: 0,
    credentials: input.credentials ?? {}
  } as DispatchAccountSecret
}

function request(method: string, originalUrl: string): Request {
  return {
    method,
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {
      accept: 'application/json',
      authorization: 'Bearer downstream-key'
    }
  } as unknown as Request
}
