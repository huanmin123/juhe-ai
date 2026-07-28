import assert from 'node:assert/strict'

import type { AxiosAdapter } from 'axios'

import { apiKeysApi, myApiKeysApi } from '../../src/api/domains/apiKeys'
import { http } from '../../src/api/http'

interface CapturedRequest {
  method: string
  url: string
}

const apiKeyId = 'key/a?b#c% d'
const encodedApiKeyId = 'key%2Fa%3Fb%23c%25%20d'
const capturedRequests: CapturedRequest[] = []
const originalAdapter = http.defaults.adapter

const requestCaptureAdapter: AxiosAdapter = async (config) => {
  capturedRequests.push({
    method: String(config.method ?? '').toUpperCase(),
    url: String(config.url ?? '')
  })

  return {
    data: { data: {} },
    status: 200,
    statusText: 'OK',
    headers: {},
    config
  }
}

try {
  http.defaults.adapter = requestCaptureAdapter

  await apiKeysApi.update(apiKeyId, { expectedRevision: 'revision-1', name: '管理端 Key' })
  await apiKeysApi.secret(apiKeyId)
  await apiKeysApi.refreshKey(apiKeyId)
  await apiKeysApi.delete(apiKeyId)

  await myApiKeysApi.update(apiKeyId, { expectedRevision: 'revision-1', name: '个人 Key' })
  await myApiKeysApi.secret(apiKeyId)
  await myApiKeysApi.refreshKey(apiKeyId)
  await myApiKeysApi.delete(apiKeyId)
} finally {
  http.defaults.adapter = originalAdapter
}

assert.deepEqual(capturedRequests, [
  { method: 'PATCH', url: `/api-keys/${encodedApiKeyId}` },
  { method: 'GET', url: `/api-keys/${encodedApiKeyId}/secret` },
  { method: 'POST', url: `/api-keys/${encodedApiKeyId}/refresh-key` },
  { method: 'DELETE', url: `/api-keys/${encodedApiKeyId}` },
  { method: 'PATCH', url: `/my-api-keys/${encodedApiKeyId}` },
  { method: 'GET', url: `/my-api-keys/${encodedApiKeyId}/secret` },
  { method: 'POST', url: `/my-api-keys/${encodedApiKeyId}/refresh-key` },
  { method: 'DELETE', url: `/my-api-keys/${encodedApiKeyId}` }
], 'API Key 动态路径段必须逐段进行 encodeURIComponent 编码')

console.log('API Key API path regression passed')
